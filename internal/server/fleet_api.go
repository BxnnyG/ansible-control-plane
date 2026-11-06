package server

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/netip"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/BxnnyG/ansible-control-plane/internal/auth"
	"github.com/BxnnyG/ansible-control-plane/internal/db/dbgen"
	"github.com/BxnnyG/ansible-control-plane/internal/fleet"
	"github.com/BxnnyG/ansible-control-plane/internal/policy"
)

// FleetAPI is the fleet read model used by the HTTP layer; *fleet.Store
// implements it. Visible narrows results to the caller's network scopes.
type FleetAPI interface {
	Networks(ctx context.Context, ws uuid.UUID, allow func(id uuid.UUID, cidr netip.Prefix) bool, visible fleet.Visible) ([]fleet.NetworkView, error)
	Hosts(ctx context.Context, ws uuid.UUID, f fleet.HostFilter, visible fleet.Visible) ([]fleet.HostView, error)
	Host(ctx context.Context, ws, id uuid.UUID, visible fleet.Visible) (fleet.HostDetail, error)
	Conflicts(ctx context.Context, ws uuid.UUID, state string, visible fleet.Visible) ([]fleet.ConflictView, error)
	Acknowledge(ctx context.Context, ws, id, user uuid.UUID, visible fleet.Visible) (fleet.ConflictView, error)
}

// WithFleet mounts the fleet API. It requires WithPolicy.
func WithFleet(f FleetAPI) Option { return func(o *routerOpts) { o.fleet = f } }

// fleetRoutes registers networks, hosts and conflicts. Scoped memberships see
// the hosts whose addresses lie in their network scopes (ADR-0018).
func fleetRoutes(r chi.Router, az *policy.Authorizer, store APIStore, svc FleetAPI, log *slog.Logger) {
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
	audit := func(req *http.Request, d policy.Decision, action, id string, details map[string]any) {
		b, _ := json.Marshal(details)
		p := auth.FromContext(req.Context())
		actx, cancel := context.WithTimeout(context.WithoutCancel(req.Context()), 5*time.Second)
		defer cancel()
		_, err := store.InsertAudit(actx, dbgen.InsertAuditParams{
			ActorID: pgtype.UUID{Bytes: p.User.ID, Valid: true}, ActorLabel: p.User.DisplayName,
			Action: action, TargetType: "host_conflict", TargetID: id,
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

	az.Handle(r, http.MethodGet, base+"/hosts", policy.FleetRead, func(w http.ResponseWriter, req *http.Request) {
		d, vis, ok := access(w, req)
		if !ok {
			return
		}
		q := req.URL.Query()
		f := fleet.HostFilter{Q: q.Get("q")}
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
			hosts, herr := svc.Conflicts(req.Context(), d.Workspace.ID, "", vis)
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
		list, err := svc.Conflicts(req.Context(), d.Workspace.ID, state, vis)
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
		audit(req, d, "fleet.conflict.ack", id.String(), map[string]any{"type": c.Type, "fingerprint": c.Fingerprint})
		policy.WriteJSON(w, http.StatusOK, c)
	})
}
