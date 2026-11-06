package server

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/BxnnyG/ansible-control-plane/internal/auth"
	"github.com/BxnnyG/ansible-control-plane/internal/db/dbgen"
	"github.com/BxnnyG/ansible-control-plane/internal/fleet"
	"github.com/BxnnyG/ansible-control-plane/internal/policy"
	"github.com/BxnnyG/ansible-control-plane/internal/remediation"
	"github.com/BxnnyG/ansible-control-plane/internal/runs"
)

// RemediationAPI is the remediation rule and bulk run service used by the HTTP
// layer; *remediation.Service implements it.
type RemediationAPI interface {
	List(ctx context.Context, ws uuid.UUID) ([]remediation.Rule, error)
	Get(ctx context.Context, ws, id uuid.UUID) (remediation.Rule, error)
	Create(ctx context.Context, ws uuid.UUID, in remediation.Input, by uuid.UUID) (remediation.Rule, error)
	Update(ctx context.Context, ws, id uuid.UUID, in remediation.Input, by uuid.UUID) (remediation.Rule, error)
	Delete(ctx context.Context, ws, id uuid.UUID) error
	Preview(ctx context.Context, ws uuid.UUID, c remediation.Criteria) ([]fleet.AlertView, error)
	Applicable(ctx context.Context, ws uuid.UUID, alerts []fleet.AlertView) (map[uuid.UUID][]remediation.RuleRef, error)
	Remediate(ctx context.Context, ws, alertID, ruleID uuid.UUID, skipCheck bool, actor runs.Actor, visible fleet.Visible) (remediation.Result, error)
	BulkRun(ctx context.Context, ws uuid.UUID, in remediation.BulkInput, actor runs.Actor, visible fleet.Visible) (remediation.Result, error)
}

// WithRemediation mounts the remediation and bulk run API. It requires
// WithPolicy and WithFleet.
func WithRemediation(s RemediationAPI) Option { return func(o *routerOpts) { o.remediation = s } }

const maxRemediationBody = 16 << 10

// remediationRoutes registers remediation rules, the fix action for alerts and
// the fleet bulk run. Rule management needs fleet.manage on an unscoped
// membership; fixing and bulk runs need fleet.remediate / run.execute, and the
// runs service authorizes the effective host set against the caller's scopes
// before a run is queued.
func remediationRoutes(r chi.Router, az *policy.Authorizer, store APIStore, svc RemediationAPI, fl FleetAPI, log *slog.Logger) {
	const base = "/api/v1/workspaces/{workspace}"
	const rules = base + "/remediation-rules"
	apiErr := func(w http.ResponseWriter, status int, msg, code string) {
		policy.WriteJSON(w, status, apiError{Error: msg, Code: code})
	}
	fail := func(w http.ResponseWriter, what string, err error) {
		var nie *remediation.NotInInventoryError
		switch {
		case errors.Is(err, remediation.ErrNotFound):
			apiErr(w, http.StatusNotFound, "remediation rule not found", "not_found")
		case errors.Is(err, remediation.ErrAlertNotFound):
			apiErr(w, http.StatusNotFound, "alert not found", "not_found")
		case errors.Is(err, policy.ErrForbidden):
			policy.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden"})
		case errors.Is(err, remediation.ErrConflict):
			apiErr(w, http.StatusConflict, err.Error(), "conflict")
		case errors.Is(err, remediation.ErrDisabled):
			apiErr(w, http.StatusConflict, err.Error(), "rule_disabled")
		case errors.Is(err, remediation.ErrNotApplicable):
			apiErr(w, http.StatusConflict, err.Error(), "not_applicable")
		case errors.As(err, &nie):
			policy.WriteJSON(w, http.StatusUnprocessableEntity, struct {
				apiError
				Hosts []string `json:"hosts"`
			}{apiError{Error: err.Error(), Code: "hosts_not_in_inventory"}, nie.Hosts})
		case errors.Is(err, remediation.ErrInvalid), errors.Is(err, runs.ErrInvalid):
			apiErr(w, http.StatusUnprocessableEntity, err.Error(), "invalid")
		case errors.Is(err, runs.ErrNotSynced):
			apiErr(w, http.StatusConflict, err.Error(), "project_not_synced")
		case errors.Is(err, runs.ErrNotFound):
			apiErr(w, http.StatusNotFound, "not found", "not_found")
		default:
			log.Error(what, "error", err)
			policy.WriteJSON(w, http.StatusInternalServerError, apiError{Error: "internal error"})
		}
	}
	decision := func(w http.ResponseWriter, req *http.Request) (policy.Decision, bool) {
		d, ok := policy.DecisionFromContext(req.Context())
		if !ok {
			policy.WriteJSON(w, http.StatusInternalServerError, apiError{Error: "internal error"})
		}
		return d, ok
	}
	decode := func(w http.ResponseWriter, req *http.Request, v any) bool {
		dec := json.NewDecoder(http.MaxBytesReader(w, req.Body, maxRemediationBody))
		dec.DisallowUnknownFields()
		if err := dec.Decode(v); err != nil {
			apiErr(w, http.StatusBadRequest, "malformed request", "bad_request")
			return false
		}
		return true
	}
	audit := func(req *http.Request, d policy.Decision, action, targetType, id string, details map[string]any) {
		b, _ := json.Marshal(details)
		p := auth.FromContext(req.Context())
		actx, cancel := context.WithTimeout(context.WithoutCancel(req.Context()), 5*time.Second)
		defer cancel()
		_, err := store.InsertAudit(actx, dbgen.InsertAuditParams{
			ActorID: pgtype.UUID{Bytes: p.User.ID, Valid: true}, ActorLabel: p.User.DisplayName,
			Action: action, TargetType: targetType, TargetID: id,
			WorkspaceID: pgtype.UUID{Bytes: d.Workspace.ID, Valid: true}, Details: b,
		})
		if err != nil {
			log.Error("audit write failed", "action", action, "error", err)
		}
	}
	// manage answers 403 (audited) unless the caller may manage rules: an
	// unscoped membership that sees the whole fleet and may edit templates.
	manage := func(w http.ResponseWriter, req *http.Request, perm policy.Permission) (policy.Decision, bool) {
		d, ok := decision(w, req)
		if !ok {
			return d, false
		}
		if d.Scoped() || !policy.Allowed(d.Membership.Role, policy.TemplateWrite) {
			az.Denied(req.Context(), auth.FromContext(req.Context()), d, perm, req.Method)
			policy.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden"})
			return d, false
		}
		return d, true
	}
	visibility := func(w http.ResponseWriter, req *http.Request, d policy.Decision) (fleet.Visible, bool) {
		fa, err := az.FleetAccess(req.Context(), d)
		if err != nil {
			fail(w, "resolve fleet scope", err)
			return nil, false
		}
		if fa.All {
			return nil, true
		}
		return fa.Host, true
	}
	ruleDetails := func(r remediation.Rule) map[string]any {
		return map[string]any{"name": r.Name, "enabled": r.Enabled, "connector_type": r.ConnectorType, "min_severity": r.MinSeverity,
			"template_id": r.TemplateID, "target": r.Target, "group": r.Group, "check_first": r.CheckFirst}
	}
	ruleID := func(w http.ResponseWriter, req *http.Request) (uuid.UUID, bool) {
		id, err := uuid.Parse(chi.URLParam(req, "id"))
		if err != nil {
			apiErr(w, http.StatusNotFound, "remediation rule not found", "not_found")
		}
		return id, err == nil
	}

	az.Handle(r, http.MethodGet, rules, policy.FleetRead, func(w http.ResponseWriter, req *http.Request) {
		d, ok := manageRead(w, req, az, decision)
		if !ok {
			return
		}
		list, err := svc.List(req.Context(), d.Workspace.ID)
		if err != nil {
			fail(w, "list remediation rules", err)
			return
		}
		policy.WriteJSON(w, http.StatusOK, list)
	})

	az.Handle(r, http.MethodPost, rules, policy.FleetManage, func(w http.ResponseWriter, req *http.Request) {
		d, ok := manage(w, req, policy.FleetManage)
		if !ok {
			return
		}
		var in remediation.Input
		if !decode(w, req, &in) {
			return
		}
		rule, err := svc.Create(req.Context(), d.Workspace.ID, in, auth.FromContext(req.Context()).User.ID)
		if err != nil {
			fail(w, "create remediation rule", err)
			return
		}
		audit(req, d, "remediation.rule.create", "remediation_rule", rule.ID.String(), ruleDetails(rule))
		policy.WriteJSON(w, http.StatusCreated, rule)
	})

	// Which open alerts would a rule (saved or not) match? Pure read.
	az.Handle(r, http.MethodPost, rules+"/preview", policy.FleetManage, func(w http.ResponseWriter, req *http.Request) {
		d, ok := manage(w, req, policy.FleetManage)
		if !ok {
			return
		}
		var c struct {
			ConnectorType *string `json:"connector_type"`
			ServiceRegex  string  `json:"service_regex"`
			SummaryRegex  string  `json:"summary_regex"`
			MinSeverity   string  `json:"min_severity"`
		}
		if !decode(w, req, &c) {
			return
		}
		alerts, err := svc.Preview(req.Context(), d.Workspace.ID, remediation.Criteria{
			ConnectorType: c.ConnectorType, ServiceRegex: c.ServiceRegex, SummaryRegex: c.SummaryRegex, MinSeverity: c.MinSeverity})
		if err != nil {
			fail(w, "preview remediation rule", err)
			return
		}
		policy.WriteJSON(w, http.StatusOK, alerts)
	})

	// Applicable rules of all open alerts the caller can see, keyed by alert id.
	az.Handle(r, http.MethodGet, rules+"/applicable", policy.FleetRead, func(w http.ResponseWriter, req *http.Request) {
		d, ok := decision(w, req)
		if !ok {
			return
		}
		vis, ok := visibility(w, req, d)
		if !ok {
			return
		}
		alerts, err := fl.Alerts(req.Context(), d.Workspace.ID, fleet.AlertFilter{State: fleet.AlertOpen}, vis)
		if err != nil {
			fail(w, "list alerts", err)
			return
		}
		out, err := svc.Applicable(req.Context(), d.Workspace.ID, alerts)
		if err != nil {
			fail(w, "applicable remediation rules", err)
			return
		}
		policy.WriteJSON(w, http.StatusOK, out)
	})

	az.Handle(r, http.MethodGet, rules+"/{id}", policy.FleetRead, func(w http.ResponseWriter, req *http.Request) {
		d, ok := manageRead(w, req, az, decision)
		if !ok {
			return
		}
		id, ok := ruleID(w, req)
		if !ok {
			return
		}
		rule, err := svc.Get(req.Context(), d.Workspace.ID, id)
		if err != nil {
			fail(w, "get remediation rule", err)
			return
		}
		policy.WriteJSON(w, http.StatusOK, rule)
	})

	az.Handle(r, http.MethodPut, rules+"/{id}", policy.FleetManage, func(w http.ResponseWriter, req *http.Request) {
		d, ok := manage(w, req, policy.FleetManage)
		if !ok {
			return
		}
		id, ok := ruleID(w, req)
		if !ok {
			return
		}
		var in remediation.Input
		if !decode(w, req, &in) {
			return
		}
		rule, err := svc.Update(req.Context(), d.Workspace.ID, id, in, auth.FromContext(req.Context()).User.ID)
		if err != nil {
			fail(w, "update remediation rule", err)
			return
		}
		audit(req, d, "remediation.rule.update", "remediation_rule", rule.ID.String(), ruleDetails(rule))
		policy.WriteJSON(w, http.StatusOK, rule)
	})

	az.Handle(r, http.MethodDelete, rules+"/{id}", policy.FleetManage, func(w http.ResponseWriter, req *http.Request) {
		d, ok := manage(w, req, policy.FleetManage)
		if !ok {
			return
		}
		id, ok := ruleID(w, req)
		if !ok {
			return
		}
		rule, err := svc.Get(req.Context(), d.Workspace.ID, id)
		if err == nil {
			err = svc.Delete(req.Context(), d.Workspace.ID, id)
		}
		if err != nil {
			fail(w, "delete remediation rule", err)
			return
		}
		audit(req, d, "remediation.rule.delete", "remediation_rule", id.String(), map[string]any{"name": rule.Name})
		w.WriteHeader(http.StatusNoContent)
	})

	// Rules that can fix one alert.
	az.Handle(r, http.MethodGet, base+"/alerts/{id}/remediations", policy.FleetRead, func(w http.ResponseWriter, req *http.Request) {
		d, ok := decision(w, req)
		if !ok {
			return
		}
		id, err := uuid.Parse(chi.URLParam(req, "id"))
		if err != nil {
			apiErr(w, http.StatusNotFound, "alert not found", "not_found")
			return
		}
		vis, ok := visibility(w, req, d)
		if !ok {
			return
		}
		alerts, err := fl.Alerts(req.Context(), d.Workspace.ID, fleet.AlertFilter{ID: &id}, vis)
		if err != nil {
			fail(w, "get alert", err)
			return
		}
		if len(alerts) != 1 {
			apiErr(w, http.StatusNotFound, "alert not found", "not_found")
			return
		}
		m, err := svc.Applicable(req.Context(), d.Workspace.ID, alerts)
		if err != nil {
			fail(w, "applicable remediation rules", err)
			return
		}
		list := m[id]
		if list == nil {
			list = []remediation.RuleRef{}
		}
		policy.WriteJSON(w, http.StatusOK, list)
	})

	az.Handle(r, http.MethodPost, base+"/alerts/{id}/remediate", policy.FleetRemediate, func(w http.ResponseWriter, req *http.Request) {
		d, ok := decision(w, req)
		if !ok {
			return
		}
		id, err := uuid.Parse(chi.URLParam(req, "id"))
		if err != nil {
			apiErr(w, http.StatusNotFound, "alert not found", "not_found")
			return
		}
		var body struct {
			RuleID    uuid.UUID `json:"rule_id"`
			SkipCheck bool      `json:"skip_check"`
		}
		if !decode(w, req, &body) {
			return
		}
		if body.RuleID == uuid.Nil {
			apiErr(w, http.StatusBadRequest, "rule_id is required", "bad_request")
			return
		}
		vis, ok := visibility(w, req, d)
		if !ok {
			return
		}
		p := auth.FromContext(req.Context())
		res, err := svc.Remediate(req.Context(), d.Workspace.ID, id, body.RuleID, body.SkipCheck, runs.Actor{UserID: p.User.ID, Principal: &p}, vis)
		if err != nil {
			fail(w, "remediate alert", err)
			return
		}
		audit(req, d, "remediation.run", "run", res.Run.ID.String(), map[string]any{
			"rule_id": body.RuleID, "alert_id": id, "phase": res.Phase, "template_id": res.Run.TemplateID,
			"limit": res.Limit, "hosts": len(res.Hosts), "check": res.Run.Params.Check, "skip_check": body.SkipCheck,
		})
		policy.WriteJSON(w, http.StatusCreated, res)
	})

	az.Handle(r, http.MethodPost, base+"/fleet/bulk-run", policy.RunExecute, func(w http.ResponseWriter, req *http.Request) {
		d, ok := decision(w, req)
		if !ok {
			return
		}
		var body struct {
			HostIDs    []uuid.UUID `json:"host_ids"`
			TemplateID uuid.UUID   `json:"template_id"`
			CheckMode  bool        `json:"check_mode"`
		}
		if !decode(w, req, &body) {
			return
		}
		if body.TemplateID == uuid.Nil {
			apiErr(w, http.StatusBadRequest, "template_id is required", "bad_request")
			return
		}
		vis, ok := visibility(w, req, d)
		if !ok {
			return
		}
		p := auth.FromContext(req.Context())
		res, err := svc.BulkRun(req.Context(), d.Workspace.ID,
			remediation.BulkInput{HostIDs: body.HostIDs, TemplateID: body.TemplateID, CheckMode: body.CheckMode},
			runs.Actor{UserID: p.User.ID, Principal: &p}, vis)
		if err != nil {
			fail(w, "bulk run", err)
			return
		}
		audit(req, d, "fleet.bulk_run", "run", res.Run.ID.String(), map[string]any{
			"template_id": body.TemplateID, "requested": len(body.HostIDs), "hosts": len(res.Hosts), "skipped": len(res.Skipped),
			"limit": res.Limit, "check": res.Run.Params.Check,
		})
		policy.WriteJSON(w, http.StatusCreated, res)
	})
}

// manageRead lets the fleet.read routes of the rule list and detail serve only
// unscoped callers (rules reveal templates of the whole workspace).
func manageRead(w http.ResponseWriter, req *http.Request, az *policy.Authorizer, decision func(http.ResponseWriter, *http.Request) (policy.Decision, bool)) (policy.Decision, bool) {
	d, ok := decision(w, req)
	if !ok {
		return d, false
	}
	if d.Scoped() {
		az.Denied(req.Context(), auth.FromContext(req.Context()), d, policy.FleetRead, req.Method)
		policy.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden"})
		return d, false
	}
	return d, true
}
