package server

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/BxnnyG/ansible-control-plane/internal/auth"
	"github.com/BxnnyG/ansible-control-plane/internal/db/dbgen"
	"github.com/BxnnyG/ansible-control-plane/internal/grouprules"
	"github.com/BxnnyG/ansible-control-plane/internal/policy"
	"github.com/BxnnyG/ansible-control-plane/internal/projects"
)

// GroupRulesAPI is the group rule service used by the HTTP layer;
// *grouprules.Service implements it.
type GroupRulesAPI interface {
	List(ctx context.Context, ws uuid.UUID) ([]grouprules.Rule, error)
	Get(ctx context.Context, ws, id uuid.UUID) (grouprules.Rule, error)
	Create(ctx context.Context, ws uuid.UUID, in grouprules.Input, by uuid.UUID) (grouprules.Rule, error)
	Update(ctx context.Context, ws, id uuid.UUID, in grouprules.Update) (grouprules.Rule, error)
	Delete(ctx context.Context, ws, id uuid.UUID) error
	Preview(ctx context.Context, ws, id uuid.UUID) (grouprules.Preview, error)
	Apply(ctx context.Context, ws, id uuid.UUID, in grouprules.ApplyInput) (grouprules.Applied, error)
}

// WithGroupRules mounts the group rule API. It requires WithPolicy.
func WithGroupRules(g GroupRulesAPI) Option { return func(o *routerOpts) { o.groupRules = g } }

const maxGroupRuleBody = 16 << 10

// groupRuleRoutes registers /api/v1/workspaces/{workspace}/group-rules.
//
// Reading needs fleet.read plus read access to the rule's inventory. Creating,
// changing, deleting and applying need fleet.manage plus inventory.write on the
// target inventory, and they act on the whole fleet, so scoped memberships
// (which see only part of the fleet) are refused.
func groupRuleRoutes(r chi.Router, az *policy.Authorizer, store APIStore, svc GroupRulesAPI, log *slog.Logger) {
	const base = "/api/v1/workspaces/{workspace}/group-rules"
	const item = base + "/{id}"
	notFound := func(w http.ResponseWriter, what string) {
		policy.WriteJSON(w, http.StatusNotFound, apiError{Error: what + " not found", Code: "not_found"})
	}
	fail := func(w http.ResponseWriter, what string, err error) {
		switch {
		case errors.Is(err, grouprules.ErrNotFound):
			notFound(w, "group rule")
		case errors.Is(err, grouprules.ErrConflict):
			policy.WriteJSON(w, http.StatusConflict, apiError{Error: err.Error(), Code: "conflict"})
		case errors.Is(err, grouprules.ErrDisabled):
			policy.WriteJSON(w, http.StatusConflict, apiError{Error: err.Error(), Code: "rule_disabled"})
		case errors.Is(err, grouprules.ErrRuleChanged):
			policy.WriteJSON(w, http.StatusConflict, apiError{Error: err.Error(), Code: "rule_changed"})
		case errors.Is(err, grouprules.ErrInvalid):
			policy.WriteJSON(w, http.StatusUnprocessableEntity, apiError{Error: err.Error(), Code: "invalid"})
		default:
			inventoryFail(log, w, what, err)
		}
	}
	forbidden := func(w http.ResponseWriter, req *http.Request, d policy.Decision, perm policy.Permission) {
		az.Denied(req.Context(), auth.FromContext(req.Context()), d, perm, req.Method)
		policy.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden"})
	}
	decision := func(w http.ResponseWriter, req *http.Request) (policy.Decision, bool) {
		d, ok := policy.DecisionFromContext(req.Context())
		if !ok {
			policy.WriteJSON(w, http.StatusInternalServerError, apiError{Error: "internal error"})
		}
		return d, ok
	}
	ruleID := func(w http.ResponseWriter, req *http.Request) (uuid.UUID, bool) {
		id, err := uuid.Parse(chi.URLParam(req, "id"))
		if err != nil {
			notFound(w, "group rule")
			return uuid.Nil, false
		}
		return id, true
	}
	decode := func(w http.ResponseWriter, req *http.Request, v any) bool {
		dec := json.NewDecoder(http.MaxBytesReader(w, req.Body, maxGroupRuleBody))
		dec.DisallowUnknownFields()
		if err := dec.Decode(v); err != nil {
			policy.WriteJSON(w, http.StatusBadRequest, apiError{Error: "malformed request", Code: "bad_request"})
			return false
		}
		return true
	}
	audit := func(req *http.Request, d policy.Decision, action string, id uuid.UUID, details map[string]any) {
		b, _ := json.Marshal(details)
		p := auth.FromContext(req.Context())
		actx, cancel := context.WithTimeout(context.WithoutCancel(req.Context()), 5*time.Second)
		defer cancel()
		_, err := store.InsertAudit(actx, dbgen.InsertAuditParams{
			ActorID: pgtype.UUID{Bytes: p.User.ID, Valid: true}, ActorLabel: p.User.DisplayName,
			Action: action, TargetType: "group_rule", TargetID: id.String(),
			WorkspaceID: pgtype.UUID{Bytes: d.Workspace.ID, Valid: true}, Details: b,
		})
		if err != nil {
			log.Error("audit write failed", "action", action, "error", err)
		}
	}
	// canReadInventory reports whether the caller may see anything of the
	// rule's inventory.
	canReadInventory := func(w http.ResponseWriter, req *http.Request, d policy.Decision, inv uuid.UUID) (bool, bool) {
		a, err := az.InventoryAccess(req.Context(), d, inv)
		if err != nil {
			fail(w, "resolve inventory scope", err)
			return false, false
		}
		return !a.None(), true
	}
	// wholeFleet answers 403 unless the caller sees the whole fleet.
	wholeFleet := func(w http.ResponseWriter, req *http.Request, d policy.Decision, perm policy.Permission) bool {
		fa, err := az.FleetAccess(req.Context(), d)
		if err != nil {
			fail(w, "resolve fleet scope", err)
			return false
		}
		if !fa.All {
			forbidden(w, req, d, perm)
			return false
		}
		return true
	}
	// canWriteInventory enforces inventory.write on the target inventory with
	// scope narrowing (audited when denied).
	canWriteInventory := func(w http.ResponseWriter, req *http.Request, d policy.Decision, inv uuid.UUID) bool {
		err := az.Authorize(req.Context(), auth.FromContext(req.Context()), d.Workspace.ID, policy.InventoryWrite,
			policy.Target{Refs: []policy.Ref{{Kind: policy.KindInventory, ID: inv.String()}}})
		if errors.Is(err, policy.ErrForbidden) {
			policy.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden"})
			return false
		}
		if err != nil {
			fail(w, "check inventory write", err)
			return false
		}
		return true
	}
	// readable loads a rule the caller may read.
	readable := func(w http.ResponseWriter, req *http.Request, perm policy.Permission) (policy.Decision, grouprules.Rule, bool) {
		d, ok := decision(w, req)
		if !ok {
			return d, grouprules.Rule{}, false
		}
		id, ok := ruleID(w, req)
		if !ok {
			return d, grouprules.Rule{}, false
		}
		rule, err := svc.Get(req.Context(), d.Workspace.ID, id)
		if err != nil {
			fail(w, "get group rule", err)
			return d, rule, false
		}
		ok, fine := canReadInventory(w, req, d, rule.InventoryID)
		if !fine {
			return d, rule, false
		}
		if !ok {
			forbidden(w, req, d, perm)
			return d, rule, false
		}
		return d, rule, true
	}
	// writable additionally requires inventory.write and the whole fleet.
	writable := func(w http.ResponseWriter, req *http.Request) (policy.Decision, grouprules.Rule, bool) {
		d, rule, ok := readable(w, req, policy.FleetManage)
		if !ok {
			return d, rule, false
		}
		if !wholeFleet(w, req, d, policy.FleetManage) || !canWriteInventory(w, req, d, rule.InventoryID) {
			return d, rule, false
		}
		return d, rule, true
	}
	ruleDetails := func(r grouprules.Rule) map[string]any {
		return map[string]any{"name": r.Name, "kind": r.Kind, "params": r.Params, "inventory_id": r.InventoryID, "group": r.Group,
			"mode": r.Mode, "enabled": r.Enabled}
	}

	az.Handle(r, http.MethodGet, base, policy.FleetRead, func(w http.ResponseWriter, req *http.Request) {
		d, ok := decision(w, req)
		if !ok {
			return
		}
		list, err := svc.List(req.Context(), d.Workspace.ID)
		if err != nil {
			fail(w, "list group rules", err)
			return
		}
		out := make([]grouprules.Rule, 0, len(list))
		for _, rule := range list {
			ok, fine := canReadInventory(w, req, d, rule.InventoryID)
			if !fine {
				return
			}
			if ok {
				out = append(out, rule)
			}
		}
		policy.WriteJSON(w, http.StatusOK, out)
	})

	az.Handle(r, http.MethodPost, base, policy.FleetManage, func(w http.ResponseWriter, req *http.Request) {
		d, ok := decision(w, req)
		if !ok {
			return
		}
		var body struct {
			Name        string          `json:"name"`
			Kind        string          `json:"kind"`
			Params      json.RawMessage `json:"params"`
			InventoryID uuid.UUID       `json:"inventory_id"`
			Group       string          `json:"group"`
			Mode        string          `json:"mode"`
			Enabled     *bool           `json:"enabled"`
		}
		if !decode(w, req, &body) {
			return
		}
		if !wholeFleet(w, req, d, policy.FleetManage) || !canWriteInventory(w, req, d, body.InventoryID) {
			return
		}
		rule, err := svc.Create(req.Context(), d.Workspace.ID, grouprules.Input{Name: body.Name, Kind: body.Kind, Params: body.Params,
			InventoryID: body.InventoryID, Group: body.Group, Mode: body.Mode, Enabled: body.Enabled}, auth.FromContext(req.Context()).User.ID)
		if err != nil {
			fail(w, "create group rule", err)
			return
		}
		audit(req, d, "group_rule.create", rule.ID, ruleDetails(rule))
		policy.WriteJSON(w, http.StatusCreated, rule)
	})

	az.Handle(r, http.MethodGet, item, policy.FleetRead, func(w http.ResponseWriter, req *http.Request) {
		_, rule, ok := readable(w, req, policy.FleetRead)
		if !ok {
			return
		}
		policy.WriteJSON(w, http.StatusOK, rule)
	})

	az.Handle(r, http.MethodPut, item, policy.FleetManage, func(w http.ResponseWriter, req *http.Request) {
		d, rule, ok := writable(w, req)
		if !ok {
			return
		}
		var body struct {
			Name        *string          `json:"name"`
			Kind        *string          `json:"kind"`
			Params      *json.RawMessage `json:"params"`
			InventoryID *uuid.UUID       `json:"inventory_id"`
			Group       *string          `json:"group"`
			Mode        *string          `json:"mode"`
			Enabled     *bool            `json:"enabled"`
		}
		if !decode(w, req, &body) {
			return
		}
		if body.Name == nil && body.Kind == nil && body.Params == nil && body.InventoryID == nil && body.Group == nil && body.Mode == nil && body.Enabled == nil {
			policy.WriteJSON(w, http.StatusUnprocessableEntity, apiError{Error: "nothing to update", Code: "invalid"})
			return
		}
		if body.InventoryID != nil && *body.InventoryID != rule.InventoryID && !canWriteInventory(w, req, d, *body.InventoryID) {
			return
		}
		out, err := svc.Update(req.Context(), d.Workspace.ID, rule.ID, grouprules.Update{Name: body.Name, Kind: body.Kind, Params: body.Params,
			InventoryID: body.InventoryID, Group: body.Group, Mode: body.Mode, Enabled: body.Enabled})
		if err != nil {
			fail(w, "update group rule", err)
			return
		}
		audit(req, d, "group_rule.update", out.ID, ruleDetails(out))
		policy.WriteJSON(w, http.StatusOK, out)
	})

	az.Handle(r, http.MethodDelete, item, policy.FleetManage, func(w http.ResponseWriter, req *http.Request) {
		d, rule, ok := writable(w, req)
		if !ok {
			return
		}
		if err := svc.Delete(req.Context(), d.Workspace.ID, rule.ID); err != nil {
			fail(w, "delete group rule", err)
			return
		}
		audit(req, d, "group_rule.delete", rule.ID, map[string]any{"name": rule.Name})
		w.WriteHeader(http.StatusNoContent)
	})

	// The preview is a pure read; it is offered as GET and POST (the latter
	// for clients that prefer not to cache it).
	preview := func(w http.ResponseWriter, req *http.Request) {
		d, rule, ok := readable(w, req, policy.FleetRead)
		if !ok || !wholeFleet(w, req, d, policy.FleetRead) {
			return
		}
		out, err := svc.Preview(req.Context(), d.Workspace.ID, rule.ID)
		if err != nil {
			fail(w, "preview group rule", err)
			return
		}
		policy.WriteJSON(w, http.StatusOK, out)
	}
	az.Handle(r, http.MethodGet, item+"/preview", policy.FleetRead, preview)
	az.Handle(r, http.MethodPost, item+"/preview", policy.FleetRead, preview)

	az.Handle(r, http.MethodPost, item+"/apply", policy.FleetManage, func(w http.ResponseWriter, req *http.Request) {
		d, rule, ok := writable(w, req)
		if !ok {
			return
		}
		match, ok := parseIfMatch(w, req)
		if !ok {
			return
		}
		u := auth.FromContext(req.Context()).User
		out, err := svc.Apply(req.Context(), d.Workspace.ID, rule.ID, grouprules.ApplyInput{User: u.ID,
			Author: projects.Author{Name: u.DisplayName, Email: u.Email}, IfMatch: match})
		if err != nil {
			fail(w, "apply group rule", err)
			return
		}
		details := map[string]any{"inventory_id": rule.InventoryID, "group": rule.Group, "mode": rule.Mode,
			"group_created": out.Result.GroupCreated, "hosts_created": out.Result.HostsCreated,
			"added": out.Result.Added, "removed": out.Result.Removed}
		if c := out.Result.Commit; c != nil && !c.NoOp {
			details["commit"] = map[string]any{"branch": c.Branch, "sha": c.SHA, "paths": c.Paths}
		}
		audit(req, d, "group_rule.apply", rule.ID, details)
		etag := out.Result.Version
		w.Header().Set("ETag", `"`+strconv.Itoa(etag)+`"`)
		policy.WriteJSON(w, http.StatusOK, out)
	})
}
