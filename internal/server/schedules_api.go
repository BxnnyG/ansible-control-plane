package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
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
	"github.com/BxnnyG/ansible-control-plane/internal/schedules"
)

// SchedulesAPI is the schedules and template webhook service used by the HTTP
// layer; *schedules.Service implements it.
type SchedulesAPI interface {
	List(ctx context.Context, ws, tpl uuid.UUID) ([]schedules.Schedule, error)
	Create(ctx context.Context, ws, tpl, createdBy uuid.UUID, in schedules.Input) (schedules.Schedule, error)
	Get(ctx context.Context, ws, id uuid.UUID) (schedules.Schedule, error)
	Update(ctx context.Context, ws, id uuid.UUID, in schedules.Input) (schedules.Schedule, error)
	Delete(ctx context.Context, ws, id uuid.UUID) error
	Preview(expr, tz string, n int) ([]time.Time, error)
	GetWebhook(ctx context.Context, ws, id uuid.UUID) (schedules.Webhook, error)
	RotateWebhook(ctx context.Context, ws, id uuid.UUID) (schedules.Webhook, error)
	DisableWebhook(ctx context.Context, ws, id uuid.UUID) error
	HandleWebhook(ctx context.Context, id uuid.UUID, header func(string) string, body []byte) (runs.Run, error)
}

// WithSchedules mounts the schedules API and the public template webhook. It
// requires WithPolicy.
func WithSchedules(s SchedulesAPI) Option { return func(o *routerOpts) { o.schedules = s } }

// scheduleRoutes registers schedules and template webhooks. lim is shared with
// the project hook endpoint.
func scheduleRoutes(r chi.Router, az *policy.Authorizer, store APIStore, svc SchedulesAPI, lim *hookLimiter, publicURL string, log *slog.Logger) {
	const ws = "/api/v1/workspaces/{workspace}"
	fail := func(w http.ResponseWriter, what string, err error) {
		switch {
		case errors.Is(err, schedules.ErrNotFound), errors.Is(err, runs.ErrNotFound):
			policy.WriteJSON(w, http.StatusNotFound, apiError{Error: "not found", Code: "not_found"})
		case errors.Is(err, schedules.ErrInvalid), errors.Is(err, runs.ErrInvalid):
			policy.WriteJSON(w, http.StatusUnprocessableEntity, apiError{Error: err.Error(), Code: "invalid"})
		case errors.Is(err, runs.ErrNotSynced):
			policy.WriteJSON(w, http.StatusConflict, apiError{Error: err.Error(), Code: "project_not_synced"})
		default:
			log.Error(what, "error", err)
			policy.WriteJSON(w, http.StatusInternalServerError, apiError{Error: "internal error"})
		}
	}
	notFound := func(w http.ResponseWriter) {
		policy.WriteJSON(w, http.StatusNotFound, apiError{Error: "not found", Code: "not_found"})
	}
	target := func(w http.ResponseWriter, req *http.Request) (policy.Decision, uuid.UUID, bool) {
		d, ok := policy.DecisionFromContext(req.Context())
		if !ok {
			policy.WriteJSON(w, http.StatusInternalServerError, apiError{Error: "internal error"})
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
	// Schedules and webhooks start runs with workspace authority, so scoped
	// memberships may not manage them.
	workspaceLevel := func(w http.ResponseWriter, req *http.Request, d policy.Decision, perm policy.Permission) bool {
		if !d.Scoped() {
			return true
		}
		az.Denied(req.Context(), auth.FromContext(req.Context()), d, perm, req.Method)
		policy.WriteJSON(w, http.StatusForbidden, apiError{Error: "forbidden"})
		return false
	}
	decodeInput := func(w http.ResponseWriter, req *http.Request) (schedules.Input, bool) {
		var in schedules.Input
		dec := json.NewDecoder(http.MaxBytesReader(w, req.Body, maxRunsBody))
		dec.DisallowUnknownFields()
		if dec.Decode(&in) != nil {
			policy.WriteJSON(w, http.StatusBadRequest, apiError{Error: "malformed request", Code: "bad_request"})
			return in, false
		}
		return in, true
	}

	az.Handle(r, http.MethodGet, ws+"/templates/{id}/schedules", policy.ScheduleRead, func(w http.ResponseWriter, req *http.Request) {
		d, id, ok := target(w, req)
		if !ok || !workspaceLevel(w, req, d, policy.ScheduleRead) {
			return
		}
		list, err := svc.List(req.Context(), d.Workspace.ID, id)
		if err != nil {
			fail(w, "list schedules", err)
			return
		}
		policy.WriteJSON(w, http.StatusOK, list)
	})

	az.Handle(r, http.MethodPost, ws+"/templates/{id}/schedules", policy.ScheduleWrite, func(w http.ResponseWriter, req *http.Request) {
		d, id, ok := target(w, req)
		if !ok || !workspaceLevel(w, req, d, policy.ScheduleWrite) {
			return
		}
		in, ok := decodeInput(w, req)
		if !ok {
			return
		}
		s, err := svc.Create(req.Context(), d.Workspace.ID, id, auth.FromContext(req.Context()).User.ID, in)
		if err != nil {
			fail(w, "create schedule", err)
			return
		}
		audit(req, d, "schedule.create", "schedule", s.ID, map[string]any{"template_id": id, "cron": s.Cron, "timezone": s.Timezone, "enabled": s.Enabled})
		policy.WriteJSON(w, http.StatusCreated, s)
	})

	// Registered before schedules/{id} so "preview" is not taken for an id.
	az.Handle(r, http.MethodGet, ws+"/schedules/preview", policy.ScheduleRead, func(w http.ResponseWriter, req *http.Request) {
		n := 5
		if v := req.URL.Query().Get("n"); v != "" {
			var err error
			if n, err = strconv.Atoi(v); err != nil {
				policy.WriteJSON(w, http.StatusUnprocessableEntity, apiError{Error: "n must be a number", Code: "invalid"})
				return
			}
		}
		tz := req.URL.Query().Get("tz")
		times, err := svc.Preview(req.URL.Query().Get("cron"), tz, n)
		if err != nil {
			fail(w, "preview schedule", err)
			return
		}
		policy.WriteJSON(w, http.StatusOK, map[string]any{"next_runs": times})
	})

	az.Handle(r, http.MethodGet, ws+"/schedules/{id}", policy.ScheduleRead, func(w http.ResponseWriter, req *http.Request) {
		d, id, ok := target(w, req)
		if !ok || !workspaceLevel(w, req, d, policy.ScheduleRead) {
			return
		}
		s, err := svc.Get(req.Context(), d.Workspace.ID, id)
		if err != nil {
			fail(w, "get schedule", err)
			return
		}
		policy.WriteJSON(w, http.StatusOK, s)
	})

	az.Handle(r, http.MethodPut, ws+"/schedules/{id}", policy.ScheduleWrite, func(w http.ResponseWriter, req *http.Request) {
		d, id, ok := target(w, req)
		if !ok || !workspaceLevel(w, req, d, policy.ScheduleWrite) {
			return
		}
		in, ok := decodeInput(w, req)
		if !ok {
			return
		}
		s, err := svc.Update(req.Context(), d.Workspace.ID, id, in)
		if err != nil {
			fail(w, "update schedule", err)
			return
		}
		audit(req, d, "schedule.update", "schedule", id, map[string]any{"cron": s.Cron, "timezone": s.Timezone, "enabled": s.Enabled})
		policy.WriteJSON(w, http.StatusOK, s)
	})

	az.Handle(r, http.MethodDelete, ws+"/schedules/{id}", policy.ScheduleWrite, func(w http.ResponseWriter, req *http.Request) {
		d, id, ok := target(w, req)
		if !ok || !workspaceLevel(w, req, d, policy.ScheduleWrite) {
			return
		}
		if err := svc.Delete(req.Context(), d.Workspace.ID, id); err != nil {
			fail(w, "delete schedule", err)
			return
		}
		audit(req, d, "schedule.delete", "schedule", id, map[string]any{})
		w.WriteHeader(http.StatusNoContent)
	})

	// Template webhook management.
	view := func(w http.ResponseWriter, code int, wh schedules.Webhook) {
		out := map[string]any{"enabled": wh.Enabled, "has_secret": wh.HasSecret, "path": wh.Path}
		if publicURL != "" {
			out["url"] = publicURL + wh.Path
		}
		if wh.Secret != "" {
			out["secret"] = wh.Secret // shown once
		}
		w.Header().Set("Cache-Control", "no-store")
		policy.WriteJSON(w, code, out)
	}
	const hook = ws + "/templates/{id}/webhook"
	az.Handle(r, http.MethodGet, hook, policy.TemplateWrite, func(w http.ResponseWriter, req *http.Request) {
		d, id, ok := target(w, req)
		if !ok || !workspaceLevel(w, req, d, policy.TemplateWrite) {
			return
		}
		wh, err := svc.GetWebhook(req.Context(), d.Workspace.ID, id)
		if err != nil {
			fail(w, "get template webhook", err)
			return
		}
		view(w, http.StatusOK, wh)
	})
	az.Handle(r, http.MethodPost, hook, policy.TemplateWrite, func(w http.ResponseWriter, req *http.Request) {
		d, id, ok := target(w, req)
		if !ok || !workspaceLevel(w, req, d, policy.TemplateWrite) {
			return
		}
		wh, err := svc.RotateWebhook(req.Context(), d.Workspace.ID, id)
		if err != nil {
			fail(w, "rotate template webhook", err)
			return
		}
		audit(req, d, "template.webhook_set", "template", id, map[string]any{"enabled": true})
		view(w, http.StatusOK, wh)
	})
	az.Handle(r, http.MethodDelete, hook, policy.TemplateWrite, func(w http.ResponseWriter, req *http.Request) {
		d, id, ok := target(w, req)
		if !ok || !workspaceLevel(w, req, d, policy.TemplateWrite) {
			return
		}
		if err := svc.DisableWebhook(req.Context(), d.Workspace.ID, id); err != nil {
			fail(w, "disable template webhook", err)
			return
		}
		audit(req, d, "template.webhook_delete", "template", id, map[string]any{})
		w.WriteHeader(http.StatusNoContent)
	})

	// Public endpoint: authenticated by signature only, no session and no CSRF.
	r.Post("/api/hooks/templates/{id}", func(w http.ResponseWriter, req *http.Request) {
		host, _, err := net.SplitHostPort(req.RemoteAddr)
		if err != nil {
			host = req.RemoteAddr
		}
		if !lim.allow(host, time.Now()) {
			w.Header().Set("Retry-After", "60")
			http.Error(w, "too many requests", http.StatusTooManyRequests)
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, req.Body, projects.MaxWebhookBody))
		if err != nil {
			http.Error(w, "request too large", http.StatusRequestEntityTooLarge)
			return
		}
		id, err := uuid.Parse(chi.URLParam(req, "id"))
		if err != nil {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		run, err := svc.HandleWebhook(req.Context(), id, req.Header.Get, body)
		switch {
		case err == nil:
			policy.WriteJSON(w, http.StatusAccepted, map[string]any{"status": "accepted", "run_id": run.ID})
		case errors.Is(err, schedules.ErrRejected):
			http.Error(w, "not found", http.StatusNotFound)
		default: // the signature was valid: report why no run was started
			fail(w, "template webhook", err)
		}
	})
}
