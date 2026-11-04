package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/BxnnyG/ansible-control-plane/internal/auth"
	"github.com/BxnnyG/ansible-control-plane/internal/db/dbgen"
	"github.com/BxnnyG/ansible-control-plane/internal/policy"
	"github.com/BxnnyG/ansible-control-plane/internal/projects"
)

// WebhooksAPI is the webhook part of the projects service.
type WebhooksAPI interface {
	GetWebhook(ctx context.Context, ws, id uuid.UUID) (projects.Webhook, error)
	SetWebhook(ctx context.Context, ws, id uuid.UUID, enabled bool, provider string) (projects.WebhookSecret, error)
	RotateWebhook(ctx context.Context, ws, id uuid.UUID) (projects.WebhookSecret, error)
	DeleteWebhook(ctx context.Context, ws, id uuid.UUID) error
	HandleWebhook(ctx context.Context, id uuid.UUID, header func(string) string, body []byte) projects.HookOutcome
}

// WithPublicURL sets the base used to show absolute webhook URLs.
func WithPublicURL(u string) Option {
	return func(o *routerOpts) { o.publicURL = strings.TrimRight(u, "/") }
}

// hookLimiter is a fixed-window per-source limiter for the public hook endpoint.
type hookLimiter struct {
	mu     sync.Mutex
	max    int
	window time.Duration
	m      map[string]*hookEntry
}

type hookEntry struct {
	n     int
	reset time.Time
}

const maxHookLimiterEntries = 10000

func newHookLimiter(max int, window time.Duration) *hookLimiter {
	return &hookLimiter{max: max, window: window, m: map[string]*hookEntry{}}
}

func (l *hookLimiter) allow(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.m) >= maxHookLimiterEntries {
		for k, e := range l.m {
			if !now.Before(e.reset) {
				delete(l.m, k)
			}
		}
		if len(l.m) >= maxHookLimiterEntries {
			return false
		}
	}
	e := l.m[key]
	if e == nil || !now.Before(e.reset) {
		e = &hookEntry{reset: now.Add(l.window)}
		l.m[key] = e
	}
	e.n++
	return e.n <= l.max
}

// webhookRoutes registers the public hook endpoint and the management API.
func webhookRoutes(r chi.Router, az *policy.Authorizer, store APIStore, svc WebhooksAPI, publicURL string, log *slog.Logger) {
	// Public endpoint: authenticated by signature only, no session and no CSRF.
	lim := newHookLimiter(60, time.Minute)
	r.Post("/api/hooks/projects/{id}", func(w http.ResponseWriter, req *http.Request) {
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
		out := projects.HookRejected
		if err == nil {
			out = svc.HandleWebhook(req.Context(), id, req.Header.Get, body)
		}
		if out == projects.HookRejected { // identical for unknown project, disabled hook and bad signature
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		status := "accepted"
		if out == projects.HookIgnored {
			status = "ignored"
		}
		policy.WriteJSON(w, http.StatusAccepted, map[string]string{"status": status})
	})

	const base = "/api/v1/workspaces/{workspace}/projects/{id}/webhook"
	notFound := func(w http.ResponseWriter) {
		policy.WriteJSON(w, http.StatusNotFound, apiError{Error: "project not found", Code: "not_found"})
	}
	fail := func(w http.ResponseWriter, what string, err error) {
		switch {
		case errors.Is(err, projects.ErrNotFound):
			notFound(w)
		case errors.Is(err, projects.ErrInvalid):
			policy.WriteJSON(w, http.StatusUnprocessableEntity, apiError{Error: err.Error(), Code: "invalid"})
		default:
			log.Error(what, "error", err)
			policy.WriteJSON(w, http.StatusInternalServerError, apiError{Error: "internal error"})
		}
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
	audit := func(req *http.Request, d policy.Decision, action string, id uuid.UUID, details map[string]any) {
		b, _ := json.Marshal(details)
		p := auth.FromContext(req.Context())
		actx, cancel := context.WithTimeout(context.WithoutCancel(req.Context()), 5*time.Second)
		defer cancel()
		_, err := store.InsertAudit(actx, dbgen.InsertAuditParams{
			ActorID: pgtype.UUID{Bytes: p.User.ID, Valid: true}, ActorLabel: p.User.DisplayName,
			Action: action, TargetType: "project", TargetID: id.String(),
			WorkspaceID: pgtype.UUID{Bytes: d.Workspace.ID, Valid: true}, Details: b,
		})
		if err != nil {
			log.Error("audit write failed", "action", action, "error", err)
		}
	}
	view := func(w http.ResponseWriter, code int, ws projects.WebhookSecret) {
		out := map[string]any{"enabled": ws.Enabled, "provider": ws.Provider, "has_secret": ws.HasSecret, "path": ws.Path}
		if publicURL != "" {
			out["url"] = publicURL + ws.Path
		}
		if ws.Secret != "" {
			out["secret"] = ws.Secret // shown once
		}
		w.Header().Set("Cache-Control", "no-store")
		policy.WriteJSON(w, code, out)
	}

	az.Handle(r, http.MethodGet, base, policy.ProjectWrite, func(w http.ResponseWriter, req *http.Request) {
		d, id, ok := target(w, req)
		if !ok {
			return
		}
		wh, err := svc.GetWebhook(req.Context(), d.Workspace.ID, id)
		if err != nil {
			fail(w, "get webhook", err)
			return
		}
		view(w, http.StatusOK, projects.WebhookSecret{Webhook: wh})
	})

	az.Handle(r, http.MethodPut, base, policy.ProjectWrite, func(w http.ResponseWriter, req *http.Request) {
		d, id, ok := target(w, req)
		if !ok {
			return
		}
		var body struct {
			Enabled  bool   `json:"enabled"`
			Provider string `json:"provider"`
		}
		dec := json.NewDecoder(http.MaxBytesReader(w, req.Body, maxProjectBody))
		dec.DisallowUnknownFields()
		if dec.Decode(&body) != nil {
			policy.WriteJSON(w, http.StatusBadRequest, apiError{Error: "malformed request", Code: "bad_request"})
			return
		}
		res, err := svc.SetWebhook(req.Context(), d.Workspace.ID, id, body.Enabled, body.Provider)
		if err != nil {
			fail(w, "set webhook", err)
			return
		}
		audit(req, d, "project.webhook_set", id, map[string]any{"enabled": res.Enabled, "provider": res.Provider,
			"secret_generated": res.Secret != ""})
		view(w, http.StatusOK, res)
	})

	az.Handle(r, http.MethodPost, base+"/rotate", policy.ProjectWrite, func(w http.ResponseWriter, req *http.Request) {
		d, id, ok := target(w, req)
		if !ok {
			return
		}
		res, err := svc.RotateWebhook(req.Context(), d.Workspace.ID, id)
		if err != nil {
			fail(w, "rotate webhook", err)
			return
		}
		audit(req, d, "project.webhook_rotate", id, map[string]any{"provider": res.Provider})
		view(w, http.StatusOK, res)
	})

	az.Handle(r, http.MethodDelete, base, policy.ProjectWrite, func(w http.ResponseWriter, req *http.Request) {
		d, id, ok := target(w, req)
		if !ok {
			return
		}
		if err := svc.DeleteWebhook(req.Context(), d.Workspace.ID, id); err != nil {
			fail(w, "delete webhook", err)
			return
		}
		audit(req, d, "project.webhook_delete", id, map[string]any{})
		w.WriteHeader(http.StatusNoContent)
	})
}
