package server

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/BxnnyG/ansible-control-plane/internal/auth"
	"github.com/BxnnyG/ansible-control-plane/internal/db/dbgen"
	"github.com/BxnnyG/ansible-control-plane/internal/inventory"
	"github.com/BxnnyG/ansible-control-plane/internal/policy"
	"github.com/BxnnyG/ansible-control-plane/internal/projects"
)

// InventoriesAPI is the inventory service used by the HTTP layer;
// *inventory.Service implements it. Mutations take the optimistic version
// (from If-Match) and return the new inventory version.
type InventoriesAPI interface {
	Create(ctx context.Context, ws uuid.UUID, in inventory.CreateInput) (inventory.Inventory, error)
	List(ctx context.Context, ws uuid.UUID) ([]inventory.Inventory, error)
	Get(ctx context.Context, ws, id uuid.UUID) (inventory.Inventory, error)
	Update(ctx context.Context, ws, id uuid.UUID, in inventory.UpdateInput, ifMatch *int) (inventory.Inventory, error)
	Delete(ctx context.Context, ws, id uuid.UUID) error
	Import(ctx context.Context, ws, id uuid.UUID, in inventory.ImportInput, ifMatch *int) (inventory.ImportResult, error)
	Render(ctx context.Context, ws, id uuid.UUID, export bool) (map[string]any, error)
	Commit(ctx context.Context, ws, id uuid.UUID, in inventory.CommitInput, ifMatch *int) (inventory.CommitResult, error)
	Diff(ctx context.Context, ws, id uuid.UUID) (inventory.DiffResult, error)
	RenderVisible(ctx context.Context, ws, id uuid.UUID, export bool, v inventory.Visible) (map[string]any, error)

	ListGroups(ctx context.Context, ws, inv uuid.UUID) ([]inventory.GroupView, error)
	GetGroup(ctx context.Context, ws, inv, id uuid.UUID) (inventory.GroupView, error)
	CreateGroup(ctx context.Context, ws, inv uuid.UUID, in inventory.GroupInput, ifMatch *int) (inventory.GroupView, int, error)
	UpdateGroup(ctx context.Context, ws, inv, id uuid.UUID, in inventory.GroupUpdate, ifMatch *int) (inventory.GroupView, int, error)
	DeleteGroup(ctx context.Context, ws, inv, id uuid.UUID, ifMatch *int) (int, error)

	ListHosts(ctx context.Context, ws, inv uuid.UUID) ([]inventory.HostView, error)
	GetHost(ctx context.Context, ws, inv, id uuid.UUID) (inventory.HostView, error)
	CreateHost(ctx context.Context, ws, inv uuid.UUID, in inventory.HostInput, ifMatch *int) (inventory.HostView, int, error)
	UpdateHost(ctx context.Context, ws, inv, id uuid.UUID, in inventory.HostUpdate, ifMatch *int) (inventory.HostView, int, error)
	DeleteHost(ctx context.Context, ws, inv, id uuid.UUID, ifMatch *int) (int, error)

	AddMember(ctx context.Context, ws, inv, group, host uuid.UUID, ifMatch *int) (int, error)
	RemoveMember(ctx context.Context, ws, inv, group, host uuid.UUID, ifMatch *int) (int, error)
}

// WithInventories mounts the inventories API. It requires WithPolicy.
func WithInventories(i InventoriesAPI) Option { return func(o *routerOpts) { o.inventories = i } }

const maxInventoryBody = 256 << 10

// inventoryRoutes registers /api/v1/workspaces/{workspace}/inventories.
func inventoryRoutes(r chi.Router, az *policy.Authorizer, store APIStore, svc InventoriesAPI, log *slog.Logger) {
	const base = "/api/v1/workspaces/{workspace}/inventories"
	const item = base + "/{id}"
	notFound := func(w http.ResponseWriter, what string) {
		policy.WriteJSON(w, http.StatusNotFound, apiError{Error: what + " not found", Code: "not_found"})
	}
	fail := func(w http.ResponseWriter, what string, err error) {
		switch {
		case errors.Is(err, inventory.ErrNotFound):
			notFound(w, "inventory resource")
		case errors.Is(err, inventory.ErrConflict):
			policy.WriteJSON(w, http.StatusConflict, apiError{Error: err.Error(), Code: "conflict"})
		case errors.Is(err, inventory.ErrPrecondition):
			policy.WriteJSON(w, http.StatusPreconditionFailed, apiError{Error: err.Error(), Code: "version_conflict"})
		case errors.Is(err, projects.ErrNotFound):
			policy.WriteJSON(w, http.StatusConflict, apiError{Error: "the project has not been synced yet", Code: "project_not_synced"})
		case errors.Is(err, projects.ErrGitConflict):
			var ce *projects.ConflictError
			paths := []string{}
			if errors.As(err, &ce) && ce.Paths != nil {
				paths = ce.Paths
			}
			policy.WriteJSON(w, http.StatusConflict, struct {
				apiError
				Paths []string `json:"paths"`
			}{apiError{Error: err.Error(), Code: "git_conflict"}, paths})
		case errors.Is(err, inventory.ErrVaultInINI):
			policy.WriteJSON(w, http.StatusUnprocessableEntity, apiError{
				Error: "this inventory is in INI format and contains vault-encrypted values, which INI cannot hold; convert the inventory file to YAML and import it again",
				Code:  "vault_requires_yaml"})
		case errors.As(err, new(*projects.RemoteError)):
			policy.WriteJSON(w, http.StatusBadGateway, apiError{Error: "the git remote rejected the operation", Code: "remote_error", Detail: err.Error()})
		case errors.Is(err, projects.ErrInvalid), errors.Is(err, projects.ErrHostKeyNotPinned):
			policy.WriteJSON(w, http.StatusUnprocessableEntity, apiError{Error: err.Error(), Code: "invalid"})
		case errors.Is(err, inventory.ErrInvalid):
			policy.WriteJSON(w, http.StatusUnprocessableEntity, apiError{Error: err.Error(), Code: "invalid"})
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
	// scopeAllows narrows scoped memberships to the inventory they are scoped
	// to; unscoped memberships have already passed the route check.
	scopeAllows := func(req *http.Request, d policy.Decision, perm policy.Permission, inv uuid.UUID) (bool, error) {
		if !d.Scoped() {
			return true, nil
		}
		err := az.Authorize(req.Context(), auth.FromContext(req.Context()), d.Workspace.ID, perm,
			policy.Target{Refs: []policy.Ref{{Kind: policy.KindInventory, ID: inv.String()}}})
		if errors.Is(err, policy.ErrForbidden) {
			return false, nil
		}
		return err == nil, err
	}
	// target resolves workspace decision and {id}, and applies scope narrowing.
	target := func(w http.ResponseWriter, req *http.Request, perm policy.Permission) (policy.Decision, uuid.UUID, bool) {
		d, ok := decision(w, req)
		if !ok {
			return d, uuid.Nil, false
		}
		id, err := uuid.Parse(chi.URLParam(req, "id"))
		if err != nil {
			notFound(w, "inventory")
			return d, uuid.Nil, false
		}
		allowed, err := scopeAllows(req, d, perm, id)
		if err != nil {
			fail(w, "check inventory scope", err)
			return d, uuid.Nil, false
		}
		if !allowed {
			policy.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden"})
			return d, uuid.Nil, false
		}
		return d, id, true
	}
	forbidden := func(w http.ResponseWriter, req *http.Request, d policy.Decision, perm policy.Permission) {
		az.Denied(req.Context(), auth.FromContext(req.Context()), d, perm, req.Method)
		policy.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden"})
	}
	// access resolves the part of inventory inv the caller's scopes grant
	// (ADR-0007). It answers 403 when nothing of the inventory is accessible,
	// so unknown and out-of-scope inventories look the same.
	access := func(w http.ResponseWriter, req *http.Request, d policy.Decision, perm policy.Permission, inv uuid.UUID) (policy.Access, bool) {
		a, err := az.InventoryAccess(req.Context(), d, inv)
		if err != nil {
			fail(w, "resolve inventory scope", err)
			return a, false
		}
		if a.None() {
			forbidden(w, req, d, perm)
			return a, false
		}
		return a, true
	}
	// scoped resolves {id} plus the caller's access to it.
	scoped := func(w http.ResponseWriter, req *http.Request, perm policy.Permission) (policy.Decision, uuid.UUID, policy.Access, bool) {
		d, ok := decision(w, req)
		if !ok {
			return d, uuid.Nil, policy.Access{}, false
		}
		id, err := uuid.Parse(chi.URLParam(req, "id"))
		if err != nil {
			notFound(w, "inventory")
			return d, uuid.Nil, policy.Access{}, false
		}
		a, ok := access(w, req, d, perm, id)
		return d, id, a, ok
	}
	// visibleGroup hides parents outside the caller's scope.
	visibleGroup := func(a policy.Access, g inventory.GroupView) inventory.GroupView {
		if a.All {
			return g
		}
		g.ParentIDs = filterIDs(g.ParentIDs, a.Group)
		return g
	}
	visibleHost := func(a policy.Access, h inventory.HostView) inventory.HostView {
		if a.All {
			return h
		}
		h.GroupIDs = filterIDs(h.GroupIDs, a.Group)
		return h
	}
	sub := func(req *http.Request, param string) (uuid.UUID, bool) {
		id, err := uuid.Parse(chi.URLParam(req, param))
		return id, err == nil
	}
	audit := func(req *http.Request, d policy.Decision, action string, id uuid.UUID, details map[string]any) {
		b, _ := json.Marshal(details)
		p := auth.FromContext(req.Context())
		actx, cancel := context.WithTimeout(context.WithoutCancel(req.Context()), 5*time.Second)
		defer cancel()
		_, err := store.InsertAudit(actx, dbgen.InsertAuditParams{
			ActorID: pgtype.UUID{Bytes: p.User.ID, Valid: true}, ActorLabel: p.User.DisplayName,
			Action: action, TargetType: "inventory", TargetID: id.String(),
			WorkspaceID: pgtype.UUID{Bytes: d.Workspace.ID, Valid: true}, Details: b,
		})
		if err != nil {
			log.Error("audit write failed", "action", action, "error", err)
		}
	}
	decode := func(w http.ResponseWriter, req *http.Request, v any) bool {
		dec := json.NewDecoder(http.MaxBytesReader(w, req.Body, maxInventoryBody))
		dec.DisallowUnknownFields()
		if err := dec.Decode(v); err != nil {
			policy.WriteJSON(w, http.StatusBadRequest, apiError{Error: "malformed request", Code: "bad_request"})
			return false
		}
		return true
	}
	// ifMatch parses the optional If-Match header: a quoted version number.
	// A missing header or "*" skips the optimistic check.
	ifMatch := func(w http.ResponseWriter, req *http.Request) (*int, bool) {
		h := strings.TrimSpace(req.Header.Get("If-Match"))
		if h == "" || h == "*" {
			return nil, true
		}
		v, err := strconv.Atoi(strings.Trim(strings.TrimPrefix(h, "W/"), `"`))
		if err != nil || v < 0 {
			policy.WriteJSON(w, http.StatusBadRequest, apiError{Error: "If-Match must be an inventory version", Code: "bad_request"})
			return nil, false
		}
		return &v, true
	}
	etag := func(w http.ResponseWriter, version int) { w.Header().Set("ETag", `"`+strconv.Itoa(version)+`"`) }

	az.Handle(r, http.MethodGet, base, policy.InventoryRead, func(w http.ResponseWriter, req *http.Request) {
		d, ok := decision(w, req)
		if !ok {
			return
		}
		list, err := svc.List(req.Context(), d.Workspace.ID)
		if err != nil {
			fail(w, "list inventories", err)
			return
		}
		visible := list[:0]
		for _, inv := range list {
			a, err := az.InventoryAccess(req.Context(), d, inv.ID)
			if err != nil {
				fail(w, "check inventory scope", err)
				return
			}
			if !a.None() {
				visible = append(visible, narrowInventory(inv, a))
			}
		}
		policy.WriteJSON(w, http.StatusOK, visible)
	})

	az.Handle(r, http.MethodPost, base, policy.InventoryWrite, func(w http.ResponseWriter, req *http.Request) {
		d, ok := decision(w, req)
		if !ok {
			return
		}
		if d.Scoped() { // creating inventories is a workspace-level action
			policy.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden"})
			return
		}
		var body struct {
			Name        string     `json:"name"`
			Slug        string     `json:"slug"`
			Mode        string     `json:"mode"`
			ProjectID   *uuid.UUID `json:"project_id"`
			Path        string     `json:"path"`
			WriteBranch string     `json:"write_branch"`
		}
		if !decode(w, req, &body) {
			return
		}
		inv, err := svc.Create(req.Context(), d.Workspace.ID, inventory.CreateInput{
			Name: body.Name, Slug: body.Slug, Mode: body.Mode, ProjectID: body.ProjectID, Path: body.Path,
			WriteBranch: body.WriteBranch, CreatedBy: auth.FromContext(req.Context()).User.ID,
		})
		if err != nil {
			fail(w, "create inventory", err)
			return
		}
		audit(req, d, "inventory.create", inv.ID, map[string]any{"name": inv.Name, "mode": inv.Mode})
		etag(w, inv.Version)
		policy.WriteJSON(w, http.StatusCreated, inv)
	})

	az.Handle(r, http.MethodGet, item, policy.InventoryRead, func(w http.ResponseWriter, req *http.Request) {
		d, id, a, ok := scoped(w, req, policy.InventoryRead)
		if !ok {
			return
		}
		inv, err := svc.Get(req.Context(), d.Workspace.ID, id)
		if err != nil {
			fail(w, "get inventory", err)
			return
		}
		etag(w, inv.Version)
		policy.WriteJSON(w, http.StatusOK, narrowInventory(inv, a))
	})

	az.Handle(r, http.MethodPut, item, policy.InventoryWrite, func(w http.ResponseWriter, req *http.Request) {
		d, id, ok := target(w, req, policy.InventoryWrite)
		if !ok {
			return
		}
		match, ok := ifMatch(w, req)
		if !ok {
			return
		}
		var body struct {
			Name        *string    `json:"name"`
			Slug        *string    `json:"slug"`
			ProjectID   *uuid.UUID `json:"project_id"`
			Path        *string    `json:"path"`
			WriteBranch *string    `json:"write_branch"`
		}
		if !decode(w, req, &body) {
			return
		}
		if body.Name == nil && body.Slug == nil && body.ProjectID == nil && body.Path == nil && body.WriteBranch == nil {
			policy.WriteJSON(w, http.StatusUnprocessableEntity, apiError{Error: "nothing to update", Code: "invalid"})
			return
		}
		inv, err := svc.Update(req.Context(), d.Workspace.ID, id, inventory.UpdateInput{
			Name: body.Name, Slug: body.Slug, ProjectID: body.ProjectID, Path: body.Path, WriteBranch: body.WriteBranch,
		}, match)
		if err != nil {
			fail(w, "update inventory", err)
			return
		}
		audit(req, d, "inventory.update", id, map[string]any{"name": inv.Name})
		etag(w, inv.Version)
		policy.WriteJSON(w, http.StatusOK, inv)
	})

	az.Handle(r, http.MethodDelete, item, policy.InventoryWrite, func(w http.ResponseWriter, req *http.Request) {
		d, id, ok := target(w, req, policy.InventoryWrite)
		if !ok {
			return
		}
		inv, err := svc.Get(req.Context(), d.Workspace.ID, id)
		if err != nil {
			fail(w, "get inventory", err)
			return
		}
		if err := svc.Delete(req.Context(), d.Workspace.ID, id); err != nil {
			fail(w, "delete inventory", err)
			return
		}
		audit(req, d, "inventory.delete", id, map[string]any{"name": inv.Name, "slug": inv.Slug})
		w.WriteHeader(http.StatusNoContent)
	})

	az.Handle(r, http.MethodPost, item+"/import", policy.InventoryWrite, func(w http.ResponseWriter, req *http.Request) {
		d, id, ok := target(w, req, policy.InventoryWrite)
		if !ok {
			return
		}
		match, ok := ifMatch(w, req)
		if !ok {
			return
		}
		var body struct {
			ProjectID *uuid.UUID `json:"project_id"`
			Path      string     `json:"path"`
			Format    string     `json:"format"`
		}
		if req.ContentLength != 0 && !decode(w, req, &body) {
			return
		}
		res, err := svc.Import(req.Context(), d.Workspace.ID, id, inventory.ImportInput{ProjectID: body.ProjectID, Path: body.Path, Format: body.Format}, match)
		if err != nil {
			fail(w, "import inventory", err)
			return
		}
		audit(req, d, "inventory.import", id, map[string]any{"sha": res.SHA, "path": res.Inventory.Path, "hosts": res.Hosts, "groups": res.Groups})
		etag(w, res.Inventory.Version)
		policy.WriteJSON(w, http.StatusOK, res)
	})

	// Commit and diff touch the whole inventory file, so a scoped membership
	// must cover the whole inventory (ADR-0007, ADR-0014).
	wholeInventory := func(w http.ResponseWriter, req *http.Request, perm policy.Permission) (policy.Decision, uuid.UUID, bool) {
		d, id, a, ok := scoped(w, req, perm)
		if !ok {
			return d, id, false
		}
		if !a.All {
			forbidden(w, req, d, perm)
			return d, id, false
		}
		return d, id, true
	}
	registerInventoryCommitRoutes(r, az, svc, item, commitHelpers{
		target: wholeInventory, ifMatch: ifMatch, decode: decode, audit: audit, etag: etag, fail: fail})

	az.Handle(r, http.MethodGet, item+"/render", policy.InventoryRead, func(w http.ResponseWriter, req *http.Request) {
		d, id, a, ok := scoped(w, req, policy.InventoryRead)
		if !ok {
			return
		}
		export := req.URL.Query().Get("export") == "true"
		var out map[string]any
		var err error
		if a.All {
			out, err = svc.Render(req.Context(), d.Workspace.ID, id, export)
		} else {
			out, err = svc.RenderVisible(req.Context(), d.Workspace.ID, id, export, inventory.Visible{Groups: a.Groups, Hosts: a.Hosts})
		}
		if err != nil {
			fail(w, "render inventory", err)
			return
		}
		policy.WriteJSON(w, http.StatusOK, out)
	})

	// Groups.
	az.Handle(r, http.MethodGet, item+"/groups", policy.InventoryRead, func(w http.ResponseWriter, req *http.Request) {
		d, id, a, ok := scoped(w, req, policy.InventoryRead)
		if !ok {
			return
		}
		list, err := svc.ListGroups(req.Context(), d.Workspace.ID, id)
		if err != nil {
			fail(w, "list groups", err)
			return
		}
		out := make([]inventory.GroupView, 0, len(list))
		for _, g := range list {
			if a.Group(g.ID) {
				out = append(out, visibleGroup(a, g))
			}
		}
		policy.WriteJSON(w, http.StatusOK, out)
	})

	az.Handle(r, http.MethodPost, item+"/groups", policy.InventoryWrite, func(w http.ResponseWriter, req *http.Request) {
		d, id, a, ok := scoped(w, req, policy.InventoryWrite)
		if !ok {
			return
		}
		match, ok := ifMatch(w, req)
		if !ok {
			return
		}
		var body struct {
			Name      string      `json:"name"`
			ParentIDs []uuid.UUID `json:"parent_ids"`
			VarsYAML  string      `json:"vars_yaml"`
		}
		if !decode(w, req, &body) {
			return
		}
		// A scoped member may only create groups below groups in scope.
		if !a.All && (len(body.ParentIDs) == 0 || !a.AllGroupsIn(body.ParentIDs)) {
			forbidden(w, req, d, policy.InventoryWrite)
			return
		}
		g, ver, err := svc.CreateGroup(req.Context(), d.Workspace.ID, id, inventory.GroupInput{Name: body.Name, ParentIDs: body.ParentIDs, VarsYAML: body.VarsYAML}, match)
		if err != nil {
			fail(w, "create group", err)
			return
		}
		audit(req, d, "inventory.group_create", id, map[string]any{"group": g.Name})
		etag(w, ver)
		policy.WriteJSON(w, http.StatusCreated, visibleGroup(a, g))
	})

	az.Handle(r, http.MethodGet, item+"/groups/{gid}", policy.InventoryRead, func(w http.ResponseWriter, req *http.Request) {
		d, id, a, ok := scoped(w, req, policy.InventoryRead)
		if !ok {
			return
		}
		gid, ok := sub(req, "gid")
		if !ok {
			notFound(w, "group")
			return
		}
		if !a.Group(gid) {
			forbidden(w, req, d, policy.InventoryRead)
			return
		}
		g, err := svc.GetGroup(req.Context(), d.Workspace.ID, id, gid)
		if err != nil {
			fail(w, "get group", err)
			return
		}
		policy.WriteJSON(w, http.StatusOK, visibleGroup(a, g))
	})

	az.Handle(r, http.MethodPut, item+"/groups/{gid}", policy.InventoryWrite, func(w http.ResponseWriter, req *http.Request) {
		d, id, a, ok := scoped(w, req, policy.InventoryWrite)
		if !ok {
			return
		}
		gid, ok := sub(req, "gid")
		if !ok {
			notFound(w, "group")
			return
		}
		if !a.Group(gid) {
			forbidden(w, req, d, policy.InventoryWrite)
			return
		}
		match, ok := ifMatch(w, req)
		if !ok {
			return
		}
		var body struct {
			Name      *string      `json:"name"`
			ParentIDs *[]uuid.UUID `json:"parent_ids"`
			VarsYAML  *string      `json:"vars_yaml"`
		}
		if !decode(w, req, &body) {
			return
		}
		if body.Name == nil && body.ParentIDs == nil && body.VarsYAML == nil {
			policy.WriteJSON(w, http.StatusUnprocessableEntity, apiError{Error: "nothing to update", Code: "invalid"})
			return
		}
		if !a.All && body.ParentIDs != nil {
			// The scoped member manages only the parents inside the scope;
			// parents outside stay as they are and are never revealed.
			if !a.AllGroupsIn(*body.ParentIDs) {
				forbidden(w, req, d, policy.InventoryWrite)
				return
			}
			old, err := svc.GetGroup(req.Context(), d.Workspace.ID, id, gid)
			if err != nil {
				fail(w, "get group", err)
				return
			}
			merged := append(keepOutside(old.ParentIDs, a.Group), *body.ParentIDs...)
			body.ParentIDs = &merged
		}
		g, ver, err := svc.UpdateGroup(req.Context(), d.Workspace.ID, id, gid, inventory.GroupUpdate{Name: body.Name, ParentIDs: body.ParentIDs, VarsYAML: body.VarsYAML}, match)
		if err != nil {
			fail(w, "update group", err)
			return
		}
		audit(req, d, "inventory.group_update", id, map[string]any{"group": g.Name})
		etag(w, ver)
		policy.WriteJSON(w, http.StatusOK, visibleGroup(a, g))
	})

	az.Handle(r, http.MethodDelete, item+"/groups/{gid}", policy.InventoryWrite, func(w http.ResponseWriter, req *http.Request) {
		d, id, a, ok := scoped(w, req, policy.InventoryWrite)
		if !ok {
			return
		}
		gid, ok := sub(req, "gid")
		if !ok {
			notFound(w, "group")
			return
		}
		match, ok := ifMatch(w, req)
		if !ok {
			return
		}
		if !a.All {
			// Deleting also detaches the group from its parents, so all of
			// them must be in scope too (the scope root itself cannot go).
			if !a.Group(gid) {
				forbidden(w, req, d, policy.InventoryWrite)
				return
			}
			old, err := svc.GetGroup(req.Context(), d.Workspace.ID, id, gid)
			if err != nil {
				fail(w, "get group", err)
				return
			}
			if !a.AllGroupsIn(old.ParentIDs) {
				forbidden(w, req, d, policy.InventoryWrite)
				return
			}
		}
		ver, err := svc.DeleteGroup(req.Context(), d.Workspace.ID, id, gid, match)
		if err != nil {
			fail(w, "delete group", err)
			return
		}
		audit(req, d, "inventory.group_delete", id, map[string]any{"group_id": gid.String()})
		etag(w, ver)
		w.WriteHeader(http.StatusNoContent)
	})

	// Hosts.
	az.Handle(r, http.MethodGet, item+"/hosts", policy.InventoryRead, func(w http.ResponseWriter, req *http.Request) {
		d, id, a, ok := scoped(w, req, policy.InventoryRead)
		if !ok {
			return
		}
		list, err := svc.ListHosts(req.Context(), d.Workspace.ID, id)
		if err != nil {
			fail(w, "list hosts", err)
			return
		}
		out := make([]inventory.HostView, 0, len(list))
		for _, h := range list {
			if a.Host(h.ID) {
				out = append(out, visibleHost(a, h))
			}
		}
		policy.WriteJSON(w, http.StatusOK, out)
	})

	az.Handle(r, http.MethodPost, item+"/hosts", policy.InventoryWrite, func(w http.ResponseWriter, req *http.Request) {
		d, id, a, ok := scoped(w, req, policy.InventoryWrite)
		if !ok {
			return
		}
		match, ok := ifMatch(w, req)
		if !ok {
			return
		}
		var body struct {
			Name     string      `json:"name"`
			GroupIDs []uuid.UUID `json:"group_ids"`
			VarsYAML string      `json:"vars_yaml"`
		}
		if !decode(w, req, &body) {
			return
		}
		// A scoped member may only add hosts that land in groups in scope.
		if !a.All && (len(body.GroupIDs) == 0 || !a.AllGroupsIn(body.GroupIDs)) {
			forbidden(w, req, d, policy.InventoryWrite)
			return
		}
		h, ver, err := svc.CreateHost(req.Context(), d.Workspace.ID, id, inventory.HostInput{Name: body.Name, GroupIDs: body.GroupIDs, VarsYAML: body.VarsYAML}, match)
		if err != nil {
			fail(w, "create host", err)
			return
		}
		audit(req, d, "inventory.host_create", id, map[string]any{"host": h.Name})
		etag(w, ver)
		policy.WriteJSON(w, http.StatusCreated, visibleHost(a, h))
	})

	az.Handle(r, http.MethodGet, item+"/hosts/{hid}", policy.InventoryRead, func(w http.ResponseWriter, req *http.Request) {
		d, id, a, ok := scoped(w, req, policy.InventoryRead)
		if !ok {
			return
		}
		hid, ok := sub(req, "hid")
		if !ok {
			notFound(w, "host")
			return
		}
		if !a.Host(hid) {
			forbidden(w, req, d, policy.InventoryRead)
			return
		}
		h, err := svc.GetHost(req.Context(), d.Workspace.ID, id, hid)
		if err != nil {
			fail(w, "get host", err)
			return
		}
		policy.WriteJSON(w, http.StatusOK, visibleHost(a, h))
	})

	az.Handle(r, http.MethodPut, item+"/hosts/{hid}", policy.InventoryWrite, func(w http.ResponseWriter, req *http.Request) {
		d, id, a, ok := scoped(w, req, policy.InventoryWrite)
		if !ok {
			return
		}
		hid, ok := sub(req, "hid")
		if !ok {
			notFound(w, "host")
			return
		}
		if !a.Host(hid) {
			forbidden(w, req, d, policy.InventoryWrite)
			return
		}
		match, ok := ifMatch(w, req)
		if !ok {
			return
		}
		var body struct {
			Name     *string      `json:"name"`
			GroupIDs *[]uuid.UUID `json:"group_ids"`
			VarsYAML *string      `json:"vars_yaml"`
		}
		if !decode(w, req, &body) {
			return
		}
		if body.Name == nil && body.GroupIDs == nil && body.VarsYAML == nil {
			policy.WriteJSON(w, http.StatusUnprocessableEntity, apiError{Error: "nothing to update", Code: "invalid"})
			return
		}
		if !a.All && (body.Name != nil || body.GroupIDs != nil) {
			old, err := svc.GetHost(req.Context(), d.Workspace.ID, id, hid)
			if err != nil {
				fail(w, "get host", err)
				return
			}
			// Renaming changes the host in every group it belongs to.
			if body.Name != nil && !a.AllGroupsIn(old.GroupIDs) {
				forbidden(w, req, d, policy.InventoryWrite)
				return
			}
			// Moving a host: only memberships inside the scope are managed,
			// others stay untouched; the target groups must be in scope.
			if body.GroupIDs != nil {
				if !a.AllGroupsIn(*body.GroupIDs) {
					forbidden(w, req, d, policy.InventoryWrite)
					return
				}
				merged := append(keepOutside(old.GroupIDs, a.Group), *body.GroupIDs...)
				body.GroupIDs = &merged
			}
		}
		h, ver, err := svc.UpdateHost(req.Context(), d.Workspace.ID, id, hid, inventory.HostUpdate{Name: body.Name, GroupIDs: body.GroupIDs, VarsYAML: body.VarsYAML}, match)
		if err != nil {
			fail(w, "update host", err)
			return
		}
		audit(req, d, "inventory.host_update", id, map[string]any{"host": h.Name})
		etag(w, ver)
		policy.WriteJSON(w, http.StatusOK, visibleHost(a, h))
	})

	az.Handle(r, http.MethodDelete, item+"/hosts/{hid}", policy.InventoryWrite, func(w http.ResponseWriter, req *http.Request) {
		d, id, a, ok := scoped(w, req, policy.InventoryWrite)
		if !ok {
			return
		}
		hid, ok := sub(req, "hid")
		if !ok {
			notFound(w, "host")
			return
		}
		match, ok := ifMatch(w, req)
		if !ok {
			return
		}
		if !a.All {
			// Deleting removes the host from every group it is in.
			if !a.Host(hid) {
				forbidden(w, req, d, policy.InventoryWrite)
				return
			}
			old, err := svc.GetHost(req.Context(), d.Workspace.ID, id, hid)
			if err != nil {
				fail(w, "get host", err)
				return
			}
			if !a.AllGroupsIn(old.GroupIDs) {
				forbidden(w, req, d, policy.InventoryWrite)
				return
			}
		}
		ver, err := svc.DeleteHost(req.Context(), d.Workspace.ID, id, hid, match)
		if err != nil {
			fail(w, "delete host", err)
			return
		}
		audit(req, d, "inventory.host_delete", id, map[string]any{"host_id": hid.String()})
		etag(w, ver)
		w.WriteHeader(http.StatusNoContent)
	})

	// Group membership.
	member := func(add bool) http.HandlerFunc {
		return func(w http.ResponseWriter, req *http.Request) {
			d, id, a, ok := scoped(w, req, policy.InventoryWrite)
			if !ok {
				return
			}
			gid, gok := sub(req, "gid")
			hid, hok := sub(req, "hid")
			if !gok || !hok {
				notFound(w, "group or host")
				return
			}
			// Both ends must be in scope: a host outside cannot be pulled in.
			if !a.Group(gid) || !a.Host(hid) {
				forbidden(w, req, d, policy.InventoryWrite)
				return
			}
			match, ok := ifMatch(w, req)
			if !ok {
				return
			}
			var ver int
			var err error
			action := "inventory.member_remove"
			if add {
				action = "inventory.member_add"
				ver, err = svc.AddMember(req.Context(), d.Workspace.ID, id, gid, hid, match)
			} else {
				ver, err = svc.RemoveMember(req.Context(), d.Workspace.ID, id, gid, hid, match)
			}
			if err != nil {
				fail(w, "change membership", err)
				return
			}
			audit(req, d, action, id, map[string]any{"group_id": gid.String(), "host_id": hid.String()})
			etag(w, ver)
			w.WriteHeader(http.StatusNoContent)
		}
	}
	az.Handle(r, http.MethodPut, item+"/groups/{gid}/hosts/{hid}", policy.InventoryWrite, member(true))
	az.Handle(r, http.MethodDelete, item+"/groups/{gid}/hosts/{hid}", policy.InventoryWrite, member(false))
}

// filterIDs returns the ids for which keep is true (never nil).
func filterIDs(ids []uuid.UUID, keep func(uuid.UUID) bool) []uuid.UUID {
	out := make([]uuid.UUID, 0, len(ids))
	for _, id := range ids {
		if keep(id) {
			out = append(out, id)
		}
	}
	return out
}

// keepOutside returns the ids for which inside is false.
func keepOutside(ids []uuid.UUID, inside func(uuid.UUID) bool) []uuid.UUID {
	return filterIDs(ids, func(id uuid.UUID) bool { return !inside(id) })
}

// narrowInventory reduces an inventory to what a partial (group-scoped)
// access shows: visible counts only, no source details.
func narrowInventory(inv inventory.Inventory, a policy.Access) inventory.Inventory {
	if a.All {
		return inv
	}
	inv.Hosts, inv.Groups = len(a.Hosts), len(a.Groups)
	inv.ProjectID, inv.Path, inv.WriteBranch, inv.BaseSHA = nil, "", "", ""
	return inv
}
