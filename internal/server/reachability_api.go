package server

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/BxnnyG/ansible-control-plane/internal/auth"
	"github.com/BxnnyG/ansible-control-plane/internal/db/dbgen"
	"github.com/BxnnyG/ansible-control-plane/internal/policy"
	"github.com/BxnnyG/ansible-control-plane/internal/reachability"
	"github.com/BxnnyG/ansible-control-plane/internal/runs"
)

// ReachabilityAPI is the reachability matrix service used by the HTTP layer;
// *reachability.Service implements it.
type ReachabilityAPI interface {
	GetMatrix(ctx context.Context, ws uuid.UUID) (reachability.Matrix, error)
	GetConfig(ctx context.Context, ws uuid.UUID) (reachability.Config, error)
	SetConfig(ctx context.Context, ws uuid.UUID, in reachability.ConfigInput) (reachability.Config, error)
	ListSources(ctx context.Context, ws uuid.UUID) ([]reachability.Source, error)
	CreateSource(ctx context.Context, ws, hostID uuid.UUID) (reachability.Source, error)
	DeleteSource(ctx context.Context, ws, id uuid.UUID) error
	ListTargets(ctx context.Context, ws uuid.UUID) ([]reachability.Target, error)
	CreateTarget(ctx context.Context, ws uuid.UUID, in reachability.TargetInput) (reachability.Target, error)
	UpdateTarget(ctx context.Context, ws, id uuid.UUID, in reachability.TargetInput) (reachability.Target, error)
	DeleteTarget(ctx context.Context, ws, id uuid.UUID) error
	RunNow(ctx context.Context, ws uuid.UUID, actor runs.Actor) (runs.Run, error)
}

// WithReachability mounts the reachability matrix API. It requires WithPolicy.
func WithReachability(s ReachabilityAPI) Option { return func(o *routerOpts) { o.reachability = s } }

const maxReachabilityBody = 16 << 10

type sourceBody struct {
	HostID uuid.UUID `json:"host_id"`
}

func reachabilityRoutes(r chi.Router, az *policy.Authorizer, store APIStore, svc ReachabilityAPI, log *slog.Logger) {
	const base = "/api/v1/workspaces/{workspace}/reachability"
	fail := func(w http.ResponseWriter, what string, err error) {
		switch {
		case errors.Is(err, reachability.ErrNotFound), errors.Is(err, runs.ErrNotFound):
			policy.WriteJSON(w, http.StatusNotFound, apiError{Error: "not found", Code: "not_found"})
		case errors.Is(err, reachability.ErrInvalid), errors.Is(err, runs.ErrInvalid):
			policy.WriteJSON(w, http.StatusUnprocessableEntity, apiError{Error: err.Error(), Code: "invalid"})
		case errors.Is(err, reachability.ErrConflict):
			policy.WriteJSON(w, http.StatusConflict, apiError{Error: err.Error(), Code: "conflict"})
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
	target := func(w http.ResponseWriter, req *http.Request) (policy.Decision, uuid.UUID, bool) {
		d, ok := decision(w, req)
		if !ok {
			return d, uuid.Nil, false
		}
		id, err := uuid.Parse(chi.URLParam(req, "id"))
		if err != nil {
			policy.WriteJSON(w, http.StatusNotFound, apiError{Error: "not found", Code: "not_found"})
			return d, uuid.Nil, false
		}
		return d, id, true
	}
	decode := func(w http.ResponseWriter, req *http.Request, v any) bool {
		dec := json.NewDecoder(http.MaxBytesReader(w, req.Body, maxReachabilityBody))
		dec.DisallowUnknownFields()
		if dec.Decode(v) != nil {
			policy.WriteJSON(w, http.StatusBadRequest, apiError{Error: "malformed request", Code: "bad_request"})
			return false
		}
		return true
	}
	audit := func(req *http.Request, d policy.Decision, action, targetType string, id uuid.UUID, details map[string]any) {
		b, _ := json.Marshal(details)
		p := auth.FromContext(req.Context())
		_, err := store.InsertAudit(context.WithoutCancel(req.Context()), dbgen.InsertAuditParams{
			ActorID: pgtype.UUID{Bytes: p.User.ID, Valid: true}, ActorLabel: p.User.DisplayName,
			Action: action, TargetType: targetType, TargetID: id.String(),
			WorkspaceID: pgtype.UUID{Bytes: d.Workspace.ID, Valid: true}, Details: b,
		})
		if err != nil {
			log.Error("audit write failed", "action", action, "error", err)
		}
	}

	az.Handle(r, http.MethodGet, base, policy.ReachabilityRead, func(w http.ResponseWriter, req *http.Request) {
		d, ok := decision(w, req)
		if !ok {
			return
		}
		m, err := svc.GetMatrix(req.Context(), d.Workspace.ID)
		if err != nil {
			fail(w, "get reachability matrix", err)
			return
		}
		policy.WriteJSON(w, http.StatusOK, m)
	})

	az.Handle(r, http.MethodGet, base+"/config", policy.ReachabilityRead, func(w http.ResponseWriter, req *http.Request) {
		d, ok := decision(w, req)
		if !ok {
			return
		}
		c, err := svc.GetConfig(req.Context(), d.Workspace.ID)
		if err != nil {
			fail(w, "get reachability config", err)
			return
		}
		policy.WriteJSON(w, http.StatusOK, c)
	})

	az.Handle(r, http.MethodPut, base+"/config", policy.ReachabilityWrite, func(w http.ResponseWriter, req *http.Request) {
		d, ok := decision(w, req)
		if !ok {
			return
		}
		var in reachability.ConfigInput
		if !decode(w, req, &in) {
			return
		}
		c, err := svc.SetConfig(req.Context(), d.Workspace.ID, in)
		if err != nil {
			fail(w, "set reachability config", err)
			return
		}
		audit(req, d, "reachability.config", "reachability", d.Workspace.ID, map[string]any{"interval_seconds": c.IntervalSeconds})
		policy.WriteJSON(w, http.StatusOK, c)
	})

	az.Handle(r, http.MethodGet, base+"/sources", policy.ReachabilityRead, func(w http.ResponseWriter, req *http.Request) {
		d, ok := decision(w, req)
		if !ok {
			return
		}
		list, err := svc.ListSources(req.Context(), d.Workspace.ID)
		if err != nil {
			fail(w, "list reachability sources", err)
			return
		}
		policy.WriteJSON(w, http.StatusOK, list)
	})

	az.Handle(r, http.MethodPost, base+"/sources", policy.ReachabilityWrite, func(w http.ResponseWriter, req *http.Request) {
		d, ok := decision(w, req)
		if !ok {
			return
		}
		var in sourceBody
		if !decode(w, req, &in) {
			return
		}
		s, err := svc.CreateSource(req.Context(), d.Workspace.ID, in.HostID)
		if err != nil {
			fail(w, "create reachability source", err)
			return
		}
		audit(req, d, "reachability.source.create", "reachability_source", s.ID, map[string]any{"host": s.HostName})
		policy.WriteJSON(w, http.StatusCreated, s)
	})

	az.Handle(r, http.MethodDelete, base+"/sources/{id}", policy.ReachabilityWrite, func(w http.ResponseWriter, req *http.Request) {
		d, id, ok := target(w, req)
		if !ok {
			return
		}
		if err := svc.DeleteSource(req.Context(), d.Workspace.ID, id); err != nil {
			fail(w, "delete reachability source", err)
			return
		}
		audit(req, d, "reachability.source.delete", "reachability_source", id, nil)
		w.WriteHeader(http.StatusNoContent)
	})

	az.Handle(r, http.MethodGet, base+"/targets", policy.ReachabilityRead, func(w http.ResponseWriter, req *http.Request) {
		d, ok := decision(w, req)
		if !ok {
			return
		}
		list, err := svc.ListTargets(req.Context(), d.Workspace.ID)
		if err != nil {
			fail(w, "list reachability targets", err)
			return
		}
		policy.WriteJSON(w, http.StatusOK, list)
	})

	az.Handle(r, http.MethodPost, base+"/targets", policy.ReachabilityWrite, func(w http.ResponseWriter, req *http.Request) {
		d, ok := decision(w, req)
		if !ok {
			return
		}
		var in reachability.TargetInput
		if !decode(w, req, &in) {
			return
		}
		t, err := svc.CreateTarget(req.Context(), d.Workspace.ID, in)
		if err != nil {
			fail(w, "create reachability target", err)
			return
		}
		audit(req, d, "reachability.target.create", "reachability_target", t.ID, map[string]any{"name": t.Name, "check": t.Check})
		policy.WriteJSON(w, http.StatusCreated, t)
	})

	az.Handle(r, http.MethodPut, base+"/targets/{id}", policy.ReachabilityWrite, func(w http.ResponseWriter, req *http.Request) {
		d, id, ok := target(w, req)
		if !ok {
			return
		}
		var in reachability.TargetInput
		if !decode(w, req, &in) {
			return
		}
		t, err := svc.UpdateTarget(req.Context(), d.Workspace.ID, id, in)
		if err != nil {
			fail(w, "update reachability target", err)
			return
		}
		audit(req, d, "reachability.target.update", "reachability_target", id, map[string]any{"name": t.Name, "check": t.Check, "enabled": t.Enabled})
		policy.WriteJSON(w, http.StatusOK, t)
	})

	az.Handle(r, http.MethodDelete, base+"/targets/{id}", policy.ReachabilityWrite, func(w http.ResponseWriter, req *http.Request) {
		d, id, ok := target(w, req)
		if !ok {
			return
		}
		if err := svc.DeleteTarget(req.Context(), d.Workspace.ID, id); err != nil {
			fail(w, "delete reachability target", err)
			return
		}
		audit(req, d, "reachability.target.delete", "reachability_target", id, nil)
		w.WriteHeader(http.StatusNoContent)
	})

	az.Handle(r, http.MethodPost, base+"/run", policy.ReachabilityRun, func(w http.ResponseWriter, req *http.Request) {
		d, ok := decision(w, req)
		if !ok {
			return
		}
		run, err := svc.RunNow(req.Context(), d.Workspace.ID, runs.Actor{UserID: auth.FromContext(req.Context()).User.ID})
		if err != nil {
			fail(w, "start reachability probe", err)
			return
		}
		audit(req, d, "reachability.run", "run", run.ID, nil)
		policy.WriteJSON(w, http.StatusAccepted, run)
	})
}
