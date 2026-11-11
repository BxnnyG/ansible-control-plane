package server

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/netip"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/BxnnyG/ansible-control-plane/internal/auth"
	"github.com/BxnnyG/ansible-control-plane/internal/db/dbgen"
	"github.com/BxnnyG/ansible-control-plane/internal/fleet"
	"github.com/BxnnyG/ansible-control-plane/internal/policy"
	"github.com/BxnnyG/ansible-control-plane/internal/projects"
)

// FleetAPI is the fleet read model used by the HTTP layer; *fleet.Store
// implements it. Visible narrows results to the caller's network scopes.
type FleetAPI interface {
	Networks(ctx context.Context, ws uuid.UUID, allow func(id uuid.UUID, cidr netip.Prefix) bool, visible fleet.Visible) ([]fleet.NetworkView, error)
	Hosts(ctx context.Context, ws uuid.UUID, f fleet.HostFilter, visible fleet.Visible) ([]fleet.HostView, error)
	Host(ctx context.Context, ws, id uuid.UUID, visible fleet.Visible) (fleet.HostDetail, error)
	Conflicts(ctx context.Context, ws uuid.UUID, state string, excludeUntracked bool, visible fleet.Visible) ([]fleet.ConflictView, error)
	Alerts(ctx context.Context, ws uuid.UUID, f fleet.AlertFilter, visible fleet.Visible) ([]fleet.AlertView, error)
	Acknowledge(ctx context.Context, ws, id, user uuid.UUID, visible fleet.Visible) (fleet.ConflictView, error)
	SetTracked(ctx context.Context, ws uuid.UUID, ids []uuid.UUID, tracked bool) (int, error)
	TrackOnly(ctx context.Context, ws uuid.UUID, ids []uuid.UUID) (int, error)
	NewNetworksTracked(ctx context.Context, ws uuid.UUID) (bool, error)
	SetNewNetworksTracked(ctx context.Context, ws uuid.UUID, tracked bool) error
}

// FleetAdder adds unmanaged fleet hosts to inventories; fleet.Adder implements
// it.
type FleetAdder interface {
	AddToInventory(ctx context.Context, ws, hostID uuid.UUID, in fleet.AddInput, visible fleet.Visible) (fleet.AddResult, error)
}

// WithFleetAdd enables the "add to inventory" action of the fleet API.
func WithFleetAdd(a FleetAdder) Option { return func(o *routerOpts) { o.fleetAdd = a } }

const maxFleetAddBody = 4 << 10

// maxFleetTrackBody bounds the network tracking bodies (a few hundred UUIDs).
const maxFleetTrackBody = 64 << 10

// includeUntracked parses the include_untracked query parameter. It reports
// false and answers 400 on a malformed value.
func includeUntracked(w http.ResponseWriter, req *http.Request) (include, ok bool) {
	switch req.URL.Query().Get("include_untracked") {
	case "", "false", "0":
		return false, true
	case "true", "1":
		return true, true
	}
	policy.WriteJSON(w, http.StatusBadRequest, apiError{Error: "include_untracked must be true or false", Code: "bad_request"})
	return false, false
}

// WithFleet mounts the fleet API. It requires WithPolicy.
func WithFleet(f FleetAPI) Option { return func(o *routerOpts) { o.fleet = f } }

// fleetRoutes registers networks, hosts and conflicts. Scoped memberships see
// the hosts whose addresses lie in their network scopes (ADR-0018).
func fleetRoutes(r chi.Router, az *policy.Authorizer, store APIStore, svc FleetAPI, adder FleetAdder, log *slog.Logger) {
	const base = "/api/v1/workspaces/{workspace}"
	notFound := func(w http.ResponseWriter, what string) {
		policy.WriteJSON(w, http.StatusNotFound, apiError{Error: what + " not found", Code: "not_found"})
	}
	fail := func(w http.ResponseWriter, what string, err error) {
		if errors.Is(err, fleet.ErrNotFound) {
			notFound(w, "resource")
			return
		}
		log.Error(what, "error", err)
		policy.WriteJSON(w, http.StatusInternalServerError, apiError{Error: "internal error"})
	}
	// access returns the decision and the host visibility of the caller. A
	// nil Visible means unrestricted; scoped callers without any network get
	// an always-false filter.
	access := func(w http.ResponseWriter, req *http.Request) (policy.Decision, fleet.Visible, bool) {
		d, ok := policy.DecisionFromContext(req.Context())
		if !ok {
			policy.WriteJSON(w, http.StatusInternalServerError, apiError{Error: "internal error"})
			return d, nil, false
		}
		fa, err := az.FleetAccess(req.Context(), d)
		if err != nil {
			fail(w, "resolve fleet scope", err)
			return d, nil, false
		}
		if fa.All {
			return d, nil, true
		}
		return d, fa.Host, true
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

	az.Handle(r, http.MethodGet, base+"/networks", policy.FleetRead, func(w http.ResponseWriter, req *http.Request) {
		d, vis, ok := access(w, req)
		if !ok {
			return
		}
		var allow func(uuid.UUID, netip.Prefix) bool
		if vis != nil {
			own := map[uuid.UUID]bool{}
			for _, s := range d.Scopes {
				if id, err := uuid.Parse(s.RefID); err == nil && s.Kind == policy.KindNetwork {
					own[id] = true
				}
			}
			allow = func(id uuid.UUID, _ netip.Prefix) bool { return own[id] }
		}
		list, err := svc.Networks(req.Context(), d.Workspace.ID, allow, vis)
		if err != nil {
			fail(w, "list networks", err)
			return
		}
		policy.WriteJSON(w, http.StatusOK, list)
	})

	// Tracking changes are workspace-wide, so scoped memberships may not make them.
	unrestricted := func(w http.ResponseWriter, req *http.Request) (policy.Decision, bool) {
		d, vis, ok := access(w, req)
		if !ok {
			return d, false
		}
		if vis != nil {
			policy.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden"})
			return d, false
		}
		return d, true
	}
	decode := func(w http.ResponseWriter, req *http.Request, dst any) bool {
		dec := json.NewDecoder(http.MaxBytesReader(w, req.Body, maxFleetTrackBody))
		dec.DisallowUnknownFields()
		if err := dec.Decode(dst); err != nil {
			policy.WriteJSON(w, http.StatusBadRequest, apiError{Error: "malformed request", Code: "bad_request"})
			return false
		}
		return true
	}
	az.Handle(r, http.MethodPatch, base+"/networks", policy.FleetManage, func(w http.ResponseWriter, req *http.Request) {
		d, ok := unrestricted(w, req)
		if !ok {
			return
		}
		var body struct {
			IDs       []uuid.UUID  `json:"ids"`
			Tracked   *bool        `json:"tracked"`
			TrackOnly *[]uuid.UUID `json:"track_only"`
		}
		if !decode(w, req, &body) {
			return
		}
		if (body.TrackOnly != nil) == (body.Tracked != nil) || (body.TrackOnly != nil && len(body.IDs) > 0) {
			policy.WriteJSON(w, http.StatusBadRequest, apiError{Error: "give either ids with tracked, or track_only", Code: "bad_request"})
			return
		}
		var n int
		var err error
		details := map[string]any{}
		if body.TrackOnly != nil {
			n, err = svc.TrackOnly(req.Context(), d.Workspace.ID, *body.TrackOnly)
			details["track_only"] = len(*body.TrackOnly)
		} else {
			n, err = svc.SetTracked(req.Context(), d.Workspace.ID, body.IDs, *body.Tracked)
			details["tracked"], details["networks"] = *body.Tracked, len(body.IDs)
		}
		if err != nil {
			fail(w, "track networks", err)
			return
		}
		details["updated"] = n
		audit(req, d, "fleet.networks.track", "workspace", d.Workspace.ID.String(), details)
		policy.WriteJSON(w, http.StatusOK, map[string]int{"updated": n})
	})

	az.Handle(r, http.MethodPatch, base+"/networks/{id}", policy.FleetManage, func(w http.ResponseWriter, req *http.Request) {
		d, ok := unrestricted(w, req)
		if !ok {
			return
		}
		id, err := uuid.Parse(chi.URLParam(req, "id"))
		if err != nil {
			notFound(w, "network")
			return
		}
		var body struct {
			Tracked *bool `json:"tracked"`
		}
		if !decode(w, req, &body) {
			return
		}
		if body.Tracked == nil {
			policy.WriteJSON(w, http.StatusBadRequest, apiError{Error: "tracked is required", Code: "bad_request"})
			return
		}
		n, err := svc.SetTracked(req.Context(), d.Workspace.ID, []uuid.UUID{id}, *body.Tracked)
		if err != nil {
			fail(w, "track network", err)
			return
		}
		if n == 0 {
			notFound(w, "network")
			return
		}
		audit(req, d, "fleet.network.track", "network", id.String(), map[string]any{"tracked": *body.Tracked})
		policy.WriteJSON(w, http.StatusOK, map[string]int{"updated": n})
	})

	az.Handle(r, http.MethodGet, base+"/fleet/settings", policy.FleetRead, func(w http.ResponseWriter, req *http.Request) {
		d, _, ok := access(w, req)
		if !ok {
			return
		}
		v, err := svc.NewNetworksTracked(req.Context(), d.Workspace.ID)
		if err != nil {
			fail(w, "get fleet settings", err)
			return
		}
		policy.WriteJSON(w, http.StatusOK, map[string]bool{"new_networks_tracked": v})
	})

	az.Handle(r, http.MethodPut, base+"/fleet/settings", policy.FleetManage, func(w http.ResponseWriter, req *http.Request) {
		d, ok := unrestricted(w, req)
		if !ok {
			return
		}
		var body struct {
			NewNetworksTracked *bool `json:"new_networks_tracked"`
		}
		if !decode(w, req, &body) {
			return
		}
		if body.NewNetworksTracked == nil {
			policy.WriteJSON(w, http.StatusBadRequest, apiError{Error: "new_networks_tracked is required", Code: "bad_request"})
			return
		}
		if err := svc.SetNewNetworksTracked(req.Context(), d.Workspace.ID, *body.NewNetworksTracked); err != nil {
			fail(w, "set fleet settings", err)
			return
		}
		audit(req, d, "fleet.settings.update", "workspace", d.Workspace.ID.String(), map[string]any{"new_networks_tracked": *body.NewNetworksTracked})
		policy.WriteJSON(w, http.StatusOK, map[string]bool{"new_networks_tracked": *body.NewNetworksTracked})
	})

	az.Handle(r, http.MethodGet, base+"/hosts", policy.FleetRead, func(w http.ResponseWriter, req *http.Request) {
		d, vis, ok := access(w, req)
		if !ok {
			return
		}
		q := req.URL.Query()
		inc, ok := includeUntracked(w, req)
		if !ok {
			return
		}
		f := fleet.HostFilter{Q: q.Get("q"), ExcludeUntracked: !inc}
		if s := q.Get("network"); s != "" {
			id, err := uuid.Parse(s)
			if err != nil {
				notFound(w, "network")
				return
			}
			f.NetworkID = &id
		}
		switch q.Get("conflicts") {
		case "", "false", "0":
		case "true", "1":
			f.Conflicts = true
		default:
			policy.WriteJSON(w, http.StatusBadRequest, apiError{Error: "conflicts must be true or false", Code: "bad_request"})
			return
		}
		switch q.Get("managed") {
		case "":
		case "true", "1", "false", "0":
			m := q.Get("managed") == "true" || q.Get("managed") == "1"
			f.Managed = &m
		default:
			policy.WriteJSON(w, http.StatusBadRequest, apiError{Error: "managed must be true or false", Code: "bad_request"})
			return
		}
		switch q.Get("alerts") {
		case "", "false", "0":
		case "true", "1":
			f.Alerts = true
		default:
			policy.WriteJSON(w, http.StatusBadRequest, apiError{Error: "alerts must be true or false", Code: "bad_request"})
			return
		}
		list, err := svc.Hosts(req.Context(), d.Workspace.ID, f, vis)
		if errors.Is(err, fleet.ErrNotFound) {
			notFound(w, "network")
			return
		}
		if err != nil {
			fail(w, "list hosts", err)
			return
		}
		policy.WriteJSON(w, http.StatusOK, list)
	})

	az.Handle(r, http.MethodGet, base+"/hosts/{id}", policy.FleetRead, func(w http.ResponseWriter, req *http.Request) {
		d, vis, ok := access(w, req)
		if !ok {
			return
		}
		id, err := uuid.Parse(chi.URLParam(req, "id"))
		if err != nil {
			notFound(w, "host")
			return
		}
		h, err := svc.Host(req.Context(), d.Workspace.ID, id, vis)
		if errors.Is(err, fleet.ErrNotFound) {
			notFound(w, "host")
			return
		}
		if err != nil {
			fail(w, "get host", err)
			return
		}
		if vis != nil { // conflicts may reference hosts outside the scope
			kept := h.Conflicts[:0]
			hosts, herr := svc.Conflicts(req.Context(), d.Workspace.ID, "", false, vis)
			if herr != nil {
				fail(w, "list conflicts", herr)
				return
			}
			allowed := map[uuid.UUID]bool{}
			for _, c := range hosts {
				allowed[c.ID] = true
			}
			for _, c := range h.Conflicts {
				if allowed[c.ID] {
					kept = append(kept, c)
				}
			}
			h.Conflicts = kept
		}
		policy.WriteJSON(w, http.StatusOK, h)
	})

	az.Handle(r, http.MethodGet, base+"/alerts", policy.FleetRead, func(w http.ResponseWriter, req *http.Request) {
		d, vis, ok := access(w, req)
		if !ok {
			return
		}
		q := req.URL.Query()
		inc, ok := includeUntracked(w, req)
		if !ok {
			return
		}
		f := fleet.AlertFilter{State: q.Get("state"), ExcludeUntracked: !inc}
		switch f.State {
		case "", fleet.AlertOpen, fleet.AlertResolved:
		default:
			policy.WriteJSON(w, http.StatusBadRequest, apiError{Error: "state must be open or resolved", Code: "bad_request"})
			return
		}
		for _, p := range []struct {
			name string
			dst  **uuid.UUID
		}{{"host_id", &f.HostID}, {"connector", &f.ConnectorID}} {
			if s := q.Get(p.name); s != "" {
				id, err := uuid.Parse(s)
				if err != nil {
					policy.WriteJSON(w, http.StatusBadRequest, apiError{Error: p.name + " must be a UUID", Code: "bad_request"})
					return
				}
				*p.dst = &id
			}
		}
		list, err := svc.Alerts(req.Context(), d.Workspace.ID, f, vis)
		if err != nil {
			fail(w, "list alerts", err)
			return
		}
		policy.WriteJSON(w, http.StatusOK, list)
	})

	az.Handle(r, http.MethodGet, base+"/conflicts", policy.FleetRead, func(w http.ResponseWriter, req *http.Request) {
		d, vis, ok := access(w, req)
		if !ok {
			return
		}
		state := req.URL.Query().Get("state")
		switch state {
		case "", fleet.StateOpen, fleet.StateAcknowledged, fleet.StateResolved:
		default:
			policy.WriteJSON(w, http.StatusBadRequest, apiError{Error: "state must be open, acknowledged or resolved", Code: "bad_request"})
			return
		}
		inc, ok := includeUntracked(w, req)
		if !ok {
			return
		}
		list, err := svc.Conflicts(req.Context(), d.Workspace.ID, state, !inc, vis)
		if err != nil {
			fail(w, "list conflicts", err)
			return
		}
		policy.WriteJSON(w, http.StatusOK, list)
	})

	az.Handle(r, http.MethodPost, base+"/conflicts/{id}/ack", policy.FleetManage, func(w http.ResponseWriter, req *http.Request) {
		d, vis, ok := access(w, req)
		if !ok {
			return
		}
		id, err := uuid.Parse(chi.URLParam(req, "id"))
		if err != nil {
			notFound(w, "conflict")
			return
		}
		c, err := svc.Acknowledge(req.Context(), d.Workspace.ID, id, auth.FromContext(req.Context()).User.ID, vis)
		if errors.Is(err, fleet.ErrNotFound) {
			notFound(w, "open conflict")
			return
		}
		if err != nil {
			fail(w, "acknowledge conflict", err)
			return
		}
		audit(req, d, "fleet.conflict.ack", "host_conflict", id.String(), map[string]any{"type": c.Type, "fingerprint": c.Fingerprint})
		policy.WriteJSON(w, http.StatusOK, c)
	})

	if adder == nil {
		return
	}
	// Adding a host to an inventory needs inventory.write on the route and
	// on the target inventory, checked with scope narrowing.
	az.Handle(r, http.MethodPost, base+"/hosts/{id}/add-to-inventory", policy.InventoryWrite, func(w http.ResponseWriter, req *http.Request) {
		d, vis, ok := access(w, req)
		if !ok {
			return
		}
		id, err := uuid.Parse(chi.URLParam(req, "id"))
		if err != nil {
			notFound(w, "host")
			return
		}
		var body struct {
			InventoryID uuid.UUID `json:"inventory_id"`
			Group       string    `json:"group"`
			Name        string    `json:"name"`
		}
		dec := json.NewDecoder(http.MaxBytesReader(w, req.Body, maxFleetAddBody))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&body); err != nil || body.InventoryID == uuid.Nil {
			policy.WriteJSON(w, http.StatusBadRequest, apiError{Error: "malformed request", Code: "bad_request"})
			return
		}
		p := auth.FromContext(req.Context())
		err = az.Authorize(req.Context(), p, d.Workspace.ID, policy.InventoryWrite,
			policy.Target{Refs: []policy.Ref{{Kind: policy.KindInventory, ID: body.InventoryID.String()}}})
		if errors.Is(err, policy.ErrForbidden) {
			policy.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden"})
			return
		}
		if err != nil {
			fail(w, "check inventory write", err)
			return
		}
		match, ok := parseIfMatch(w, req)
		if !ok {
			return
		}
		res, err := adder.AddToInventory(req.Context(), d.Workspace.ID, id, fleet.AddInput{
			InventoryID: body.InventoryID, Group: body.Group, Name: body.Name, IfMatch: match,
			Author: projects.Author{Name: p.User.DisplayName, Email: p.User.Email},
		}, vis)
		switch {
		case errors.Is(err, fleet.ErrNotFound):
			notFound(w, "host")
			return
		case errors.Is(err, fleet.ErrManaged), errors.Is(err, fleet.ErrNameTaken):
			policy.WriteJSON(w, http.StatusConflict, apiError{Error: err.Error(), Code: "conflict"})
			return
		case errors.Is(err, fleet.ErrInvalid):
			policy.WriteJSON(w, http.StatusUnprocessableEntity, apiError{Error: err.Error(), Code: "invalid"})
			return
		case err != nil:
			inventoryFail(log, w, "add host to inventory", err)
			return
		}
		details := map[string]any{"inventory_id": body.InventoryID, "group": body.Group, "host": res.Host, "ansible_host": res.AnsibleHost,
			"group_created": res.Result.GroupCreated, "host_created": len(res.Result.HostsCreated) > 0}
		if c := res.Result.Commit; c != nil && !c.NoOp {
			details["commit"] = map[string]any{"branch": c.Branch, "sha": c.SHA, "paths": c.Paths}
		}
		audit(req, d, "fleet.host.add_to_inventory", "host", id.String(), details)
		w.Header().Set("ETag", `"`+strconv.Itoa(res.Result.Version)+`"`)
		policy.WriteJSON(w, http.StatusOK, res)
	})
}
