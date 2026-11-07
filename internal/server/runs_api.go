package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/BxnnyG/ansible-control-plane/internal/auth"
	"github.com/BxnnyG/ansible-control-plane/internal/db/dbgen"
	"github.com/BxnnyG/ansible-control-plane/internal/policy"
	"github.com/BxnnyG/ansible-control-plane/internal/projects"
	"github.com/BxnnyG/ansible-control-plane/internal/runs"
)

// RunsAPI is the templates and runs service used by the HTTP layer;
// *runs.Service implements it.
type RunsAPI interface {
	ListTemplates(ctx context.Context, ws uuid.UUID) ([]runs.Template, error)
	GetTemplate(ctx context.Context, ws, id uuid.UUID) (runs.Template, error)
	CreateTemplate(ctx context.Context, ws uuid.UUID, in runs.TemplateInput) (runs.Template, error)
	UpdateTemplate(ctx context.Context, ws, id uuid.UUID, in runs.TemplateInput) (runs.Template, error)
	DeleteTemplate(ctx context.Context, ws, id uuid.UUID) (int, error)
	Enqueue(ctx context.Context, ws, templateID uuid.UUID, ov runs.Overrides, trigger string, actor runs.Actor, scheduleID *uuid.UUID) (runs.Run, error)
	Preview(ctx context.Context, ws, templateID uuid.UUID, ov runs.Overrides, actor runs.Actor) (runs.Preview, error)
	GetRun(ctx context.Context, ws, id uuid.UUID) (runs.RunDetail, error)
	ListRuns(ctx context.Context, ws uuid.UUID, f runs.Filter) (runs.Page, error)
	CancelRun(ctx context.Context, ws, id uuid.UUID) (runs.Run, error)
}

// WithRuns mounts the templates and runs API. It requires WithPolicy.
func WithRuns(r RunsAPI) Option { return func(o *routerOpts) { o.runs = r } }

const maxRunsBody = 256 << 10

// templateBody is the JSON body of template create and update.
type templateBody struct {
	Name                   *string                         `json:"name"`
	Slug                   *string                         `json:"slug"`
	Description            *string                         `json:"description"`
	ProjectID              *uuid.UUID                      `json:"project_id"`
	InventoryID            *uuid.UUID                      `json:"inventory_id"`
	Playbook               *string                         `json:"playbook"`
	ExtraVars              map[string]any                  `json:"extra_vars"`
	EnvVars                map[string]any                  `json:"env_vars"`
	SSHSecretID            projects.Field[uuid.UUID]       `json:"ssh_secret_id"`
	BecomeSecretID         projects.Field[uuid.UUID]       `json:"become_secret_id"`
	VaultIdentityIDs       *[]uuid.UUID                    `json:"vault_identity_ids"`
	DefaultLimit           *string                         `json:"default_limit"`
	DefaultTags            *string                         `json:"default_tags"`
	DefaultSkipTags        *string                         `json:"default_skip_tags"`
	Verbosity              *int                            `json:"verbosity"`
	CheckMode              *bool                           `json:"check_mode"`
	DiffMode               *bool                           `json:"diff_mode"`
	Survey                 projects.Field[json.RawMessage] `json:"survey"`
	MaxConcurrent          *int                            `json:"max_concurrent"`
	AllowLimitOverride     *bool                           `json:"allow_limit_override"`
	AllowTagsOverride      *bool                           `json:"allow_tags_override"`
	AllowVarsOverride      *bool                           `json:"allow_vars_override"`
	AllowCheckOverride     *bool                           `json:"allow_check_override"`
	AllowDiffOverride      *bool                           `json:"allow_diff_override"`
	AllowVerbosityOverride *bool                           `json:"allow_verbosity_override"`
}

func (b templateBody) input() runs.TemplateInput {
	return runs.TemplateInput{
		Name: b.Name, Slug: b.Slug, Description: b.Description, ProjectID: b.ProjectID, InventoryID: b.InventoryID,
		Playbook: b.Playbook, ExtraVars: b.ExtraVars, EnvVars: b.EnvVars, SSHSecretID: b.SSHSecretID,
		BecomeSecretID: b.BecomeSecretID, VaultIdentityIDs: b.VaultIdentityIDs, DefaultLimit: b.DefaultLimit,
		DefaultTags: b.DefaultTags, DefaultSkipTags: b.DefaultSkipTags, Verbosity: b.Verbosity, CheckMode: b.CheckMode,
		DiffMode: b.DiffMode, Survey: b.Survey, MaxConcurrent: b.MaxConcurrent,
		AllowLimitOverride: b.AllowLimitOverride, AllowTagsOverride: b.AllowTagsOverride,
		AllowVarsOverride: b.AllowVarsOverride, AllowCheckOverride: b.AllowCheckOverride,
		AllowDiffOverride: b.AllowDiffOverride, AllowVerbosityOverride: b.AllowVerbosityOverride,
	}
}

func (b templateBody) empty() bool {
	return b.Name == nil && b.Slug == nil && b.Description == nil && b.ProjectID == nil && b.InventoryID == nil &&
		b.Playbook == nil && b.ExtraVars == nil && b.EnvVars == nil && !b.SSHSecretID.Set && !b.BecomeSecretID.Set &&
		b.VaultIdentityIDs == nil && b.DefaultLimit == nil && b.DefaultTags == nil && b.DefaultSkipTags == nil &&
		b.Verbosity == nil && b.CheckMode == nil && b.DiffMode == nil && !b.Survey.Set && b.MaxConcurrent == nil &&
		b.AllowLimitOverride == nil && b.AllowTagsOverride == nil && b.AllowVarsOverride == nil &&
		b.AllowCheckOverride == nil && b.AllowDiffOverride == nil && b.AllowVerbosityOverride == nil
}

// adminRole reports whether the membership may set connection and execution
// variables on templates.
func adminRole(role string) bool { return role == policy.RoleAdmin || role == policy.RoleOwner }

// checkTemplateVars rejects connection and execution variables in template
// extra vars and surveys unless the caller is a workspace admin.
func checkTemplateVars(d policy.Decision, b templateBody) error {
	if adminRole(d.Membership.Role) {
		return nil
	}
	if err := runs.CheckVarsAllowed(b.ExtraVars, "template extra variables of non-admin roles"); err != nil {
		return err
	}
	if b.Survey.Set && b.Survey.Value != nil {
		return runs.CheckSurveyAllowed(*b.Survey.Value)
	}
	return nil
}

// maskTemplate hides variable values from roles that cannot edit templates
// (the keys stay visible).
func maskTemplate(d policy.Decision, t runs.Template) runs.Template {
	if policy.Allowed(d.Membership.Role, policy.TemplateWrite) {
		return t
	}
	mask := func(m map[string]any) map[string]any {
		out := make(map[string]any, len(m))
		for k := range m {
			out[k] = runs.MaskedValue
		}
		return out
	}
	t.ExtraVars = mask(t.ExtraVars)
	t.EnvVars = mask(t.EnvVars)
	return t
}

// maskRun hides the variable values of a run (its params and overrides) from
// roles that cannot edit templates, exactly like maskTemplate; keys stay
// visible. The input is not modified.
func maskRun(d policy.Decision, r runs.Run) runs.Run {
	if policy.Allowed(d.Membership.Role, policy.TemplateWrite) {
		return r
	}
	mask := func(m map[string]any) map[string]any {
		if m == nil {
			return nil
		}
		out := make(map[string]any, len(m))
		for k := range m {
			out[k] = runs.MaskedValue
		}
		return out
	}
	r.Params.ExtraVars = mask(r.Params.ExtraVars)
	r.Params.EnvVars = mask(r.Params.EnvVars)
	if len(r.Overrides) > 0 {
		var ov map[string]json.RawMessage
		if json.Unmarshal(r.Overrides, &ov) == nil {
			if raw, ok := ov["extra_vars"]; ok {
				var vars map[string]any
				if json.Unmarshal(raw, &vars) == nil {
					if b, err := json.Marshal(mask(vars)); err == nil {
						ov["extra_vars"] = b
					}
				}
			}
			if b, err := json.Marshal(ov); err == nil {
				r.Overrides = b
			}
		}
	}
	return r
}

// runRoutes registers templates and runs under /api/v1/workspaces/{workspace}.
func runRoutes(r chi.Router, az *policy.Authorizer, store APIStore, svc RunsAPI, stream *runStream, log *slog.Logger) {
	const tpl = "/api/v1/workspaces/{workspace}/templates"
	const rns = "/api/v1/workspaces/{workspace}/runs"
	fail := func(w http.ResponseWriter, what string, err error) {
		switch {
		case errors.Is(err, runs.ErrNotFound):
			policy.WriteJSON(w, http.StatusNotFound, apiError{Error: "not found", Code: "not_found"})
		case errors.Is(err, policy.ErrForbidden):
			policy.WriteJSON(w, http.StatusForbidden, apiError{Error: "forbidden"})
		case errors.Is(err, runs.ErrNotSynced):
			policy.WriteJSON(w, http.StatusConflict, apiError{Error: err.Error(), Code: "project_not_synced"})
		case errors.Is(err, runs.ErrConflict):
			policy.WriteJSON(w, http.StatusConflict, apiError{Error: err.Error(), Code: "conflict"})
		case errors.Is(err, runs.ErrInvalid):
			policy.WriteJSON(w, http.StatusUnprocessableEntity, apiError{Error: err.Error(), Code: "invalid"})
		default:
			log.Error(what, "error", err)
			policy.WriteJSON(w, http.StatusInternalServerError, apiError{Error: "internal error"})
		}
	}
	notFound := func(w http.ResponseWriter) {
		policy.WriteJSON(w, http.StatusNotFound, apiError{Error: "not found", Code: "not_found"})
	}
	decision := func(w http.ResponseWriter, req *http.Request) (policy.Decision, bool) {
		d, ok := policy.DecisionFromContext(req.Context())
		if !ok {
			policy.WriteJSON(w, http.StatusInternalServerError, apiError{Error: "internal error"})
		}
		return d, ok
	}
	target := func(w http.ResponseWriter, req *http.Request) (policy.Decision, uuid.UUID, bool) {
		d, ok := decision(w, req)
		if !ok {
			return d, uuid.Nil, false
		}
		id, err := uuid.Parse(chi.URLParam(req, "id"))
		if err != nil {
			notFound(w)
			return d, uuid.Nil, false
		}
		return d, id, true
	}
	audit := func(req *http.Request, d policy.Decision, action, targetType string, id uuid.UUID, details map[string]any) {
		b, _ := json.Marshal(details)
		p := auth.FromContext(req.Context())
		actx, cancel := context.WithTimeout(context.WithoutCancel(req.Context()), 5*time.Second)
		defer cancel()
		_, err := store.InsertAudit(actx, dbgen.InsertAuditParams{
			ActorID: pgtype.UUID{Bytes: p.User.ID, Valid: true}, ActorLabel: p.User.DisplayName,
			Action: action, TargetType: targetType, TargetID: id.String(),
			WorkspaceID: pgtype.UUID{Bytes: d.Workspace.ID, Valid: true}, Details: b,
		})
		if err != nil {
			log.Error("audit write failed", "action", action, "error", err)
		}
	}
	decode := func(w http.ResponseWriter, req *http.Request, v any, allowEmpty bool) bool {
		dec := json.NewDecoder(http.MaxBytesReader(w, req.Body, maxRunsBody))
		dec.DisallowUnknownFields()
		if err := dec.Decode(v); err != nil && (!allowEmpty || !errors.Is(err, io.EOF)) {
			policy.WriteJSON(w, http.StatusBadRequest, apiError{Error: "malformed request", Code: "bad_request"})
			return false
		}
		return true
	}
	forbidden := func(w http.ResponseWriter) {
		policy.WriteJSON(w, http.StatusForbidden, apiError{Error: "forbidden"})
	}
	// invOK reports whether a scoped membership can use anything of the
	// inventory; unscoped memberships have already passed the route check.
	invOK := func(req *http.Request, d policy.Decision, inv uuid.UUID) (bool, error) {
		if !d.Scoped() {
			return true, nil
		}
		a, err := az.InventoryAccess(req.Context(), d, inv)
		return err == nil && !a.None(), err
	}
	// visible returns the run visibility predicate of the caller: scoped
	// memberships only see runs whose whole host set lies in their scope.
	visible := func(req *http.Request, d policy.Decision) func(runs.Run) bool {
		if !d.Scoped() {
			return nil
		}
		cache := map[uuid.UUID]policy.Access{}
		return func(run runs.Run) bool {
			inv := run.Params.InventoryID
			a, ok := cache[inv]
			if !ok {
				var err error
				if a, err = az.InventoryAccess(req.Context(), d, inv); err != nil {
					log.Error("resolve run scope", "error", err)
					return false
				}
				cache[inv] = a
			}
			return a.AllHostsIn(run.Params.Hosts)
		}
	}

	az.Handle(r, http.MethodGet, tpl, policy.TemplateRead, func(w http.ResponseWriter, req *http.Request) {
		d, ok := decision(w, req)
		if !ok {
			return
		}
		list, err := svc.ListTemplates(req.Context(), d.Workspace.ID)
		if err != nil {
			fail(w, "list templates", err)
			return
		}
		if d.Scoped() {
			kept := list[:0]
			for _, t := range list {
				allowed, err := invOK(req, d, t.InventoryID)
				if err != nil {
					fail(w, "resolve template scope", err)
					return
				}
				if allowed {
					kept = append(kept, t)
				}
			}
			list = kept
		}
		for i := range list {
			list[i] = maskTemplate(d, list[i])
		}
		policy.WriteJSON(w, http.StatusOK, list)
	})

	az.Handle(r, http.MethodPost, tpl, policy.TemplateWrite, func(w http.ResponseWriter, req *http.Request) {
		d, ok := decision(w, req)
		if !ok {
			return
		}
		if d.Scoped() { // templates span projects, inventories and secrets: workspace-level
			az.Denied(req.Context(), auth.FromContext(req.Context()), d, policy.TemplateWrite, req.Method)
			forbidden(w)
			return
		}
		var body templateBody
		if !decode(w, req, &body, false) {
			return
		}
		if err := checkTemplateVars(d, body); err != nil {
			fail(w, "create template", err)
			return
		}
		in := body.input()
		in.CreatedBy = auth.FromContext(req.Context()).User.ID
		t, err := svc.CreateTemplate(req.Context(), d.Workspace.ID, in)
		if err != nil {
			fail(w, "create template", err)
			return
		}
		audit(req, d, "template.create", "template", t.ID, map[string]any{"name": t.Name, "playbook": t.Playbook})
		policy.WriteJSON(w, http.StatusCreated, t)
	})

	az.Handle(r, http.MethodGet, tpl+"/{id}", policy.TemplateRead, func(w http.ResponseWriter, req *http.Request) {
		d, id, ok := target(w, req)
		if !ok {
			return
		}
		t, err := svc.GetTemplate(req.Context(), d.Workspace.ID, id)
		if err != nil {
			fail(w, "get template", err)
			return
		}
		allowed, err := invOK(req, d, t.InventoryID)
		if err != nil {
			fail(w, "resolve template scope", err)
			return
		}
		if !allowed {
			notFound(w)
			return
		}
		policy.WriteJSON(w, http.StatusOK, maskTemplate(d, t))
	})

	az.Handle(r, http.MethodPut, tpl+"/{id}", policy.TemplateWrite, func(w http.ResponseWriter, req *http.Request) {
		d, id, ok := target(w, req)
		if !ok {
			return
		}
		if d.Scoped() {
			az.Denied(req.Context(), auth.FromContext(req.Context()), d, policy.TemplateWrite, req.Method)
			forbidden(w)
			return
		}
		var body templateBody
		if !decode(w, req, &body, false) {
			return
		}
		if body.empty() {
			policy.WriteJSON(w, http.StatusUnprocessableEntity, apiError{Error: "nothing to update", Code: "invalid"})
			return
		}
		if err := checkTemplateVars(d, body); err != nil {
			fail(w, "update template", err)
			return
		}
		t, err := svc.UpdateTemplate(req.Context(), d.Workspace.ID, id, body.input())
		if err != nil {
			fail(w, "update template", err)
			return
		}
		audit(req, d, "template.update", "template", id, map[string]any{"name": t.Name, "playbook": t.Playbook})
		policy.WriteJSON(w, http.StatusOK, t)
	})

	az.Handle(r, http.MethodDelete, tpl+"/{id}", policy.TemplateWrite, func(w http.ResponseWriter, req *http.Request) {
		d, id, ok := target(w, req)
		if !ok {
			return
		}
		if d.Scoped() {
			az.Denied(req.Context(), auth.FromContext(req.Context()), d, policy.TemplateWrite, req.Method)
			forbidden(w)
			return
		}
		t, err := svc.GetTemplate(req.Context(), d.Workspace.ID, id)
		if err != nil {
			fail(w, "get template", err)
			return
		}
		cancelled, err := svc.DeleteTemplate(req.Context(), d.Workspace.ID, id)
		if err != nil {
			fail(w, "delete template", err)
			return
		}
		audit(req, d, "template.delete", "template", id, map[string]any{"name": t.Name, "cancelled_runs": cancelled})
		w.WriteHeader(http.StatusNoContent)
	})

	az.Handle(r, http.MethodPost, tpl+"/{id}/runs", policy.RunExecute, func(w http.ResponseWriter, req *http.Request) {
		d, id, ok := target(w, req)
		if !ok {
			return
		}
		var ov runs.Overrides
		if !decode(w, req, &ov, true) {
			return
		}
		p := auth.FromContext(req.Context())
		run, err := svc.Enqueue(req.Context(), d.Workspace.ID, id, ov, runs.TriggerManual,
			runs.Actor{UserID: p.User.ID, Principal: &p}, nil)
		if err != nil {
			fail(w, "start run", err)
			return
		}
		varKeys := make([]string, 0, len(ov.ExtraVars))
		for k := range ov.ExtraVars {
			varKeys = append(varKeys, k)
		}
		audit(req, d, "run.start", "run", run.ID, map[string]any{
			"template_id": id, "template": run.TemplateName, "trigger": run.Trigger, "revision": run.ProjectRevision,
			"limit": run.Params.Limit, "hosts": len(run.Params.Hosts), "check": run.Params.Check,
			"override_vars": varKeys, // names only, never values
		})
		run = maskRun(d, run)
		policy.WriteJSON(w, http.StatusCreated, run)
	})

	// Preview validates like start (overrides, limit, host scope) but creates
	// no run and writes no audit entry.
	az.Handle(r, http.MethodPost, tpl+"/{id}/runs/preview", policy.RunExecute, func(w http.ResponseWriter, req *http.Request) {
		d, id, ok := target(w, req)
		if !ok {
			return
		}
		var ov runs.Overrides
		if !decode(w, req, &ov, true) {
			return
		}
		p := auth.FromContext(req.Context())
		pv, err := svc.Preview(req.Context(), d.Workspace.ID, id, ov, runs.Actor{UserID: p.User.ID, Principal: &p})
		if err != nil {
			fail(w, "preview run", err)
			return
		}
		policy.WriteJSON(w, http.StatusOK, pv)
	})

	az.Handle(r, http.MethodGet, rns, policy.RunRead, func(w http.ResponseWriter, req *http.Request) {
		d, ok := decision(w, req)
		if !ok {
			return
		}
		q := req.URL.Query()
		f := runs.Filter{Status: q.Get("status"), Trigger: q.Get("trigger"), Host: q.Get("host"), Cursor: q.Get("cursor"),
			Visible: visible(req, d)}
		for key, dst := range map[string]**uuid.UUID{"template": &f.TemplateID, "user": &f.UserID} {
			if v := q.Get(key); v != "" {
				id, err := uuid.Parse(v)
				if err != nil {
					policy.WriteJSON(w, http.StatusBadRequest, apiError{Error: "invalid " + key + " filter", Code: "bad_request"})
					return
				}
				*dst = &id
			}
		}
		if v := q.Get("limit"); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n < 1 {
				policy.WriteJSON(w, http.StatusBadRequest, apiError{Error: "invalid limit", Code: "bad_request"})
				return
			}
			f.Limit = n
		}
		page, err := svc.ListRuns(req.Context(), d.Workspace.ID, f)
		if err != nil {
			fail(w, "list runs", err)
			return
		}
		for i := range page.Items {
			page.Items[i] = maskRun(d, page.Items[i])
		}
		policy.WriteJSON(w, http.StatusOK, page)
	})

	az.Handle(r, http.MethodGet, rns+"/{id}", policy.RunRead, func(w http.ResponseWriter, req *http.Request) {
		d, id, ok := target(w, req)
		if !ok {
			return
		}
		run, err := svc.GetRun(req.Context(), d.Workspace.ID, id)
		if err != nil {
			fail(w, "get run", err)
			return
		}
		if vis := visible(req, d); vis != nil && !vis(run.Run) {
			notFound(w)
			return
		}
		run.Run = maskRun(d, run.Run)
		policy.WriteJSON(w, http.StatusOK, run)
	})

	az.Handle(r, http.MethodPost, rns+"/{id}/cancel", policy.RunCancel, func(w http.ResponseWriter, req *http.Request) {
		d, id, ok := target(w, req)
		if !ok {
			return
		}
		if vis := visible(req, d); vis != nil {
			cur, err := svc.GetRun(req.Context(), d.Workspace.ID, id)
			if err != nil {
				fail(w, "get run", err)
				return
			}
			if !vis(cur.Run) {
				notFound(w)
				return
			}
		}
		run, err := svc.CancelRun(req.Context(), d.Workspace.ID, id)
		if err != nil {
			fail(w, "cancel run", err)
			return
		}
		audit(req, d, "run.cancel", "run", id, map[string]any{"template": run.TemplateName, "status": run.Status})
		policy.WriteJSON(w, http.StatusOK, maskRun(d, run))
	})

	if stream != nil {
		streamRoutes(r, az, svc, stream, runHelpers{fail: fail, notFound: notFound, decision: decision, target: target, visible: visible}, log)
	}
}
