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
	"github.com/BxnnyG/ansible-control-plane/internal/connectors"
	"github.com/BxnnyG/ansible-control-plane/internal/db/dbgen"
	"github.com/BxnnyG/ansible-control-plane/internal/metrics"
	"github.com/BxnnyG/ansible-control-plane/internal/policy"
)

// ConnectorsAPI is the connector service used by the HTTP layer;
// *connectors.Service implements it.
type ConnectorsAPI interface {
	Specs() []connectors.Spec
	List(ctx context.Context, ws uuid.UUID) ([]dbgen.Connector, error)
	Get(ctx context.Context, ws, id uuid.UUID) (dbgen.Connector, error)
	CreateGenerating(ctx context.Context, ws uuid.UUID, in connectors.CreateInput) (dbgen.Connector, string, error)
	HandleAlertWebhook(ctx context.Context, id uuid.UUID, authorization string, body []byte) (uuid.UUID, int, error)
	Update(ctx context.Context, ws, id uuid.UUID, in connectors.UpdateInput) (dbgen.Connector, error)
	Delete(ctx context.Context, ws, id uuid.UUID) error
	Test(ctx context.Context, ws, id uuid.UUID) error
	TestConfig(ctx context.Context, ws uuid.UUID, in connectors.TestInput) error
	RequestSync(ctx context.Context, ws, id uuid.UUID) error
	ListSyncs(ctx context.Context, ws, id uuid.UUID, limit int) ([]dbgen.ConnectorSync, error)
	SecretRefs(ctx context.Context, ws uuid.UUID) (map[uuid.UUID]connectors.SecretRef, error)
}

// WithConnectors mounts the connector API. It requires WithPolicy.
func WithConnectors(c ConnectorsAPI) Option { return func(o *routerOpts) { o.connectors = c } }

const (
	maxConnectorBody   = 64 << 10
	maxConnectorSecs   = 7 * 24 * 3600
	defaultSyncHistory = 50
	maxSyncHistory     = 200
)

// connectorView is the API representation of a connector. The credential is
// only referenced (id, name, type), never returned.
type connectorView struct {
	ID              uuid.UUID             `json:"id"`
	Type            string                `json:"type"`
	Name            string                `json:"name"`
	BaseURL         string                `json:"base_url"`
	Config          map[string]any        `json:"config"`
	Secret          *connectors.SecretRef `json:"secret"`
	IntervalSeconds int                   `json:"interval_seconds"`
	Enabled         bool                  `json:"enabled"`
	Status          string                `json:"status"` // disabled, pending, ok or error
	LastSyncAt      *time.Time            `json:"last_sync_at"`
	LastSuccessAt   *time.Time            `json:"last_success_at"`
	NextSyncAt      time.Time             `json:"next_sync_at"`
	LastError       string                `json:"last_error"`
	LastDurationMs  int                   `json:"last_duration_ms"`
	BackoffCount    int                   `json:"backoff_count"`
	Counts          connectors.Counts     `json:"counts"`
	Warnings        []string              `json:"warnings"`
	// WebhookPath is the receiver path of alert_webhook connectors.
	WebhookPath string    `json:"webhook_path,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

func tsPtr(t pgtype.Timestamptz) *time.Time {
	if !t.Valid {
		return nil
	}
	u := t.Time.UTC()
	return &u
}

func newConnectorView(c dbgen.Connector, secrets map[uuid.UUID]connectors.SecretRef) connectorView {
	v := connectorView{ID: c.ID, Type: c.Type, Name: c.Name, BaseURL: c.BaseUrl, Config: map[string]any{},
		IntervalSeconds: int(c.IntervalSeconds), Enabled: c.Enabled, LastSyncAt: tsPtr(c.LastSyncAt), LastSuccessAt: tsPtr(c.LastSuccessAt),
		NextSyncAt: c.NextSyncAt.Time.UTC(), LastError: c.LastError, LastDurationMs: int(c.LastDurationMs), BackoffCount: int(c.BackoffCount),
		Warnings: []string{}, CreatedAt: c.CreatedAt.Time.UTC(), UpdatedAt: c.UpdatedAt.Time.UTC()}
	if c.Type == connectors.AlertWebhookType {
		v.WebhookPath = connectors.AlertHookPath(c.ID)
	}
	_ = json.Unmarshal(c.Config, &v.Config)
	_ = json.Unmarshal(c.LastCounts, &v.Counts)
	if v.Counts.Warnings != nil {
		v.Warnings = v.Counts.Warnings
	}
	switch {
	case !c.Enabled:
		v.Status = "disabled"
	case c.LastStatus == "":
		v.Status = "pending"
	default:
		v.Status = c.LastStatus
	}
	if c.SecretID.Valid {
		ref := connectors.SecretRef{ID: c.SecretID.Bytes}
		if s, ok := secrets[c.SecretID.Bytes]; ok {
			ref = s
		}
		v.Secret = &ref
	}
	return v
}

type syncView struct {
	ID         uuid.UUID         `json:"id"`
	Trigger    string            `json:"trigger"`
	Status     string            `json:"status"`
	StartedAt  time.Time         `json:"started_at"`
	FinishedAt time.Time         `json:"finished_at"`
	DurationMs int               `json:"duration_ms"`
	Counts     connectors.Counts `json:"counts"`
	Error      string            `json:"error"`
}

// connectorRoutes registers the connector endpoints under
// /api/v1/workspaces/{workspace}. All of them are workspace-wide permissions.
func connectorRoutes(r chi.Router, az *policy.Authorizer, store APIStore, svc ConnectorsAPI, lim *hookLimiter, publicURL string, log *slog.Logger) {
	const base = "/api/v1/workspaces/{workspace}/connectors"
	const item = base + "/{id}"
	fail := func(w http.ResponseWriter, what string, err error) {
		switch {
		case errors.Is(err, connectors.ErrNotFound):
			policy.WriteJSON(w, http.StatusNotFound, apiError{Error: "connector not found", Code: "not_found"})
		case errors.Is(err, connectors.ErrConflict):
			policy.WriteJSON(w, http.StatusConflict, apiError{Error: "a connector with this name already exists", Code: "conflict"})
		case errors.Is(err, connectors.ErrInvalidConfig), errors.Is(err, connectors.ErrUnknownType):
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
	connID := func(w http.ResponseWriter, req *http.Request) (uuid.UUID, bool) {
		id, err := uuid.Parse(chi.URLParam(req, "id"))
		if err != nil {
			policy.WriteJSON(w, http.StatusNotFound, apiError{Error: "connector not found", Code: "not_found"})
			return uuid.Nil, false
		}
		return id, true
	}
	decode := func(w http.ResponseWriter, req *http.Request, v any) bool {
		dec := json.NewDecoder(http.MaxBytesReader(w, req.Body, maxConnectorBody))
		dec.DisallowUnknownFields()
		if err := dec.Decode(v); err != nil {
			policy.WriteJSON(w, http.StatusBadRequest, apiError{Error: "malformed request", Code: "bad_request"})
			return false
		}
		return true
	}
	audit := func(req *http.Request, d policy.Decision, action string, id string, details map[string]any) {
		b, _ := json.Marshal(details)
		p := auth.FromContext(req.Context())
		actx, cancel := context.WithTimeout(context.WithoutCancel(req.Context()), 5*time.Second)
		defer cancel()
		_, err := store.InsertAudit(actx, dbgen.InsertAuditParams{
			ActorID: pgtype.UUID{Bytes: p.User.ID, Valid: true}, ActorLabel: p.User.DisplayName,
			Action: action, TargetType: "connector", TargetID: id,
			WorkspaceID: pgtype.UUID{Bytes: d.Workspace.ID, Valid: true}, Details: b,
		})
		if err != nil {
			log.Error("audit write failed", "action", action, "error", err)
		}
	}
	view := func(ctx context.Context, d policy.Decision, c dbgen.Connector) (connectorView, error) {
		refs, err := svc.SecretRefs(ctx, d.Workspace.ID)
		if err != nil {
			return connectorView{}, err
		}
		return newConnectorView(c, refs), nil
	}
	checkInterval := func(w http.ResponseWriter, secs *int) bool {
		if secs != nil && *secs > maxConnectorSecs {
			policy.WriteJSON(w, http.StatusUnprocessableEntity, apiError{Error: "invalid connector configuration: interval must be at most 7 days", Code: "invalid"})
			return false
		}
		return true
	}

	// Public endpoint: authenticated by the connector's bearer secret only, no
	// session and no CSRF. Every rejection looks like an unknown path.
	r.Post("/api/hooks/alerts/{id}", func(w http.ResponseWriter, req *http.Request) {
		host, _, err := net.SplitHostPort(req.RemoteAddr)
		if err != nil {
			host = req.RemoteAddr
		}
		if !lim.allow(host, time.Now()) {
			metrics.WebhookRequests.WithLabelValues("alert", "rate_limited").Inc()
			w.Header().Set("Retry-After", "60")
			http.Error(w, "too many requests", http.StatusTooManyRequests)
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, req.Body, connectors.MaxAlertWebhookBody))
		if err != nil {
			metrics.WebhookRequests.WithLabelValues("alert", "too_large").Inc()
			http.Error(w, "request too large", http.StatusRequestEntityTooLarge)
			return
		}
		id, err := uuid.Parse(chi.URLParam(req, "id"))
		if err != nil {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		ws, n, err := svc.HandleAlertWebhook(req.Context(), id, req.Header.Get("Authorization"), body)
		switch {
		case err == nil:
			metrics.WebhookRequests.WithLabelValues("alert", "accepted").Inc()
		case errors.Is(err, connectors.ErrWebhookRejected):
			metrics.WebhookRequests.WithLabelValues("alert", "rejected").Inc()
		case errors.Is(err, connectors.ErrWebhookPayload):
			metrics.WebhookRequests.WithLabelValues("alert", "bad_payload").Inc()
		default:
			metrics.WebhookRequests.WithLabelValues("alert", "error").Inc()
		}
		switch {
		case err == nil:
			b, _ := json.Marshal(map[string]any{"alerts": n})
			actx, cancel := context.WithTimeout(context.WithoutCancel(req.Context()), 5*time.Second)
			defer cancel()
			if _, aerr := store.InsertAudit(actx, dbgen.InsertAuditParams{ActorLabel: "alert-webhook", Action: "connector.alert_webhook",
				TargetType: "connector", TargetID: id.String(), WorkspaceID: pgtype.UUID{Bytes: ws, Valid: true}, Details: b}); aerr != nil {
				log.Error("audit write failed", "action", "connector.alert_webhook", "error", aerr)
			}
			policy.WriteJSON(w, http.StatusAccepted, map[string]any{"status": "accepted", "alerts": n})
		case errors.Is(err, connectors.ErrWebhookRejected):
			http.Error(w, "not found", http.StatusNotFound)
		case errors.Is(err, connectors.ErrWebhookPayload): // authenticated: say what is wrong
			policy.WriteJSON(w, http.StatusBadRequest, apiError{Error: err.Error(), Code: "bad_request"})
		default:
			log.Error("alert webhook", "connector", id, "error", err)
			policy.WriteJSON(w, http.StatusInternalServerError, apiError{Error: "internal error"})
		}
	})

	az.Handle(r, http.MethodGet, "/api/v1/workspaces/{workspace}/connector-types", policy.ConnectorRead, func(w http.ResponseWriter, _ *http.Request) {
		specs := svc.Specs()
		for i := range specs { // JSON arrays, never null
			if specs[i].SecretTypes == nil {
				specs[i].SecretTypes = []string{}
			}
			if specs[i].Fields == nil {
				specs[i].Fields = []connectors.Field{}
			}
		}
		policy.WriteJSON(w, http.StatusOK, specs)
	})

	az.Handle(r, http.MethodGet, base, policy.ConnectorRead, func(w http.ResponseWriter, req *http.Request) {
		d, ok := decision(w, req)
		if !ok {
			return
		}
		list, err := svc.List(req.Context(), d.Workspace.ID)
		if err != nil {
			fail(w, "list connectors", err)
			return
		}
		refs, err := svc.SecretRefs(req.Context(), d.Workspace.ID)
		if err != nil {
			fail(w, "list connector secrets", err)
			return
		}
		out := make([]connectorView, 0, len(list))
		for _, c := range list {
			out = append(out, newConnectorView(c, refs))
		}
		policy.WriteJSON(w, http.StatusOK, out)
	})

	az.Handle(r, http.MethodPost, base, policy.ConnectorWrite, func(w http.ResponseWriter, req *http.Request) {
		d, ok := decision(w, req)
		if !ok {
			return
		}
		var body struct {
			Type            string         `json:"type"`
			Name            string         `json:"name"`
			BaseURL         string         `json:"base_url"`
			Config          map[string]any `json:"config"`
			SecretID        *uuid.UUID     `json:"secret_id"`
			IntervalSeconds int            `json:"interval_seconds"`
			Enabled         *bool          `json:"enabled"`
		}
		if !decode(w, req, &body) || !checkInterval(w, &body.IntervalSeconds) {
			return
		}
		uid := auth.FromContext(req.Context()).User.ID
		c, generated, err := svc.CreateGenerating(req.Context(), d.Workspace.ID, connectors.CreateInput{Type: body.Type, Name: body.Name, BaseURL: body.BaseURL,
			Config: body.Config, SecretID: body.SecretID, IntervalSeconds: body.IntervalSeconds,
			Enabled: body.Enabled == nil || *body.Enabled, CreatedBy: &uid})
		if err != nil {
			fail(w, "create connector", err)
			return
		}
		audit(req, d, "connector.create", c.ID.String(), map[string]any{"name": c.Name, "type": c.Type, "enabled": c.Enabled, "has_secret": c.SecretID.Valid})
		v, err := view(req.Context(), d, c)
		if err != nil {
			fail(w, "connector secret refs", err)
			return
		}
		if generated == "" {
			policy.WriteJSON(w, http.StatusCreated, v)
			return
		}
		// A generated webhook secret is shown exactly once.
		w.Header().Set("Cache-Control", "no-store")
		out := struct {
			connectorView
			GeneratedSecret string `json:"generated_secret"`
			WebhookURL      string `json:"webhook_url,omitempty"`
		}{connectorView: v, GeneratedSecret: generated}
		if publicURL != "" {
			out.WebhookURL = publicURL + v.WebhookPath
		}
		policy.WriteJSON(w, http.StatusCreated, out)
	})

	az.Handle(r, http.MethodPost, base+"/test", policy.ConnectorWrite, func(w http.ResponseWriter, req *http.Request) {
		d, ok := decision(w, req)
		if !ok {
			return
		}
		var body struct {
			Type     string         `json:"type"`
			BaseURL  string         `json:"base_url"`
			Config   map[string]any `json:"config"`
			SecretID *uuid.UUID     `json:"secret_id"`
		}
		if !decode(w, req, &body) {
			return
		}
		err := svc.TestConfig(req.Context(), d.Workspace.ID, connectors.TestInput{Type: body.Type, BaseURL: body.BaseURL, Config: body.Config, SecretID: body.SecretID})
		if err != nil && (errors.Is(err, connectors.ErrInvalidConfig) || errors.Is(err, connectors.ErrUnknownType)) {
			fail(w, "test connector config", err)
			return
		}
		writeTestResult(w, req, d, audit, "unsaved", map[string]any{"type": body.Type}, err)
	})

	az.Handle(r, http.MethodGet, item, policy.ConnectorRead, func(w http.ResponseWriter, req *http.Request) {
		d, ok := decision(w, req)
		if !ok {
			return
		}
		id, ok := connID(w, req)
		if !ok {
			return
		}
		c, err := svc.Get(req.Context(), d.Workspace.ID, id)
		if err != nil {
			fail(w, "get connector", err)
			return
		}
		v, err := view(req.Context(), d, c)
		if err != nil {
			fail(w, "connector secret refs", err)
			return
		}
		policy.WriteJSON(w, http.StatusOK, v)
	})

	az.Handle(r, http.MethodPatch, item, policy.ConnectorWrite, func(w http.ResponseWriter, req *http.Request) {
		d, ok := decision(w, req)
		if !ok {
			return
		}
		id, ok := connID(w, req)
		if !ok {
			return
		}
		var body struct {
			Name            *string         `json:"name"`
			BaseURL         *string         `json:"base_url"`
			Config          map[string]any  `json:"config"`
			SecretID        json.RawMessage `json:"secret_id"` // absent: keep, null: remove
			IntervalSeconds *int            `json:"interval_seconds"`
			Enabled         *bool           `json:"enabled"`
		}
		if !decode(w, req, &body) || !checkInterval(w, body.IntervalSeconds) {
			return
		}
		in := connectors.UpdateInput{Name: body.Name, BaseURL: body.BaseURL, Config: body.Config, IntervalSeconds: body.IntervalSeconds, Enabled: body.Enabled}
		changed := []string{}
		add := func(name string, set bool) {
			if set {
				changed = append(changed, name)
			}
		}
		if body.SecretID != nil {
			if string(body.SecretID) == "null" {
				in.ClearSecret = true
			} else {
				var sid uuid.UUID
				if err := json.Unmarshal(body.SecretID, &sid); err != nil {
					policy.WriteJSON(w, http.StatusBadRequest, apiError{Error: "malformed request", Code: "bad_request"})
					return
				}
				in.SecretID = &sid
			}
		}
		add("name", body.Name != nil)
		add("base_url", body.BaseURL != nil)
		add("config", body.Config != nil)
		add("secret", body.SecretID != nil)
		add("interval", body.IntervalSeconds != nil)
		add("enabled", body.Enabled != nil)
		if len(changed) == 0 {
			policy.WriteJSON(w, http.StatusUnprocessableEntity, apiError{Error: "nothing to update", Code: "invalid"})
			return
		}
		c, err := svc.Update(req.Context(), d.Workspace.ID, id, in)
		if err != nil {
			fail(w, "update connector", err)
			return
		}
		audit(req, d, "connector.update", id.String(), map[string]any{"name": c.Name, "type": c.Type, "changed": changed})
		v, err := view(req.Context(), d, c)
		if err != nil {
			fail(w, "connector secret refs", err)
			return
		}
		policy.WriteJSON(w, http.StatusOK, v)
	})

	az.Handle(r, http.MethodDelete, item, policy.ConnectorWrite, func(w http.ResponseWriter, req *http.Request) {
		d, ok := decision(w, req)
		if !ok {
			return
		}
		id, ok := connID(w, req)
		if !ok {
			return
		}
		c, err := svc.Get(req.Context(), d.Workspace.ID, id)
		if err != nil {
			fail(w, "get connector", err)
			return
		}
		if err := svc.Delete(req.Context(), d.Workspace.ID, id); err != nil {
			fail(w, "delete connector", err)
			return
		}
		audit(req, d, "connector.delete", id.String(), map[string]any{"name": c.Name, "type": c.Type})
		w.WriteHeader(http.StatusNoContent)
	})

	az.Handle(r, http.MethodPost, item+"/test", policy.ConnectorWrite, func(w http.ResponseWriter, req *http.Request) {
		d, ok := decision(w, req)
		if !ok {
			return
		}
		id, ok := connID(w, req)
		if !ok {
			return
		}
		c, err := svc.Get(req.Context(), d.Workspace.ID, id)
		if err != nil {
			fail(w, "get connector", err)
			return
		}
		err = svc.Test(req.Context(), d.Workspace.ID, id)
		if errors.Is(err, connectors.ErrNotFound) {
			fail(w, "test connector", err)
			return
		}
		writeTestResult(w, req, d, audit, id.String(), map[string]any{"name": c.Name, "type": c.Type}, err)
	})

	az.Handle(r, http.MethodPost, item+"/sync", policy.ConnectorWrite, func(w http.ResponseWriter, req *http.Request) {
		d, ok := decision(w, req)
		if !ok {
			return
		}
		id, ok := connID(w, req)
		if !ok {
			return
		}
		c, err := svc.Get(req.Context(), d.Workspace.ID, id)
		if err != nil {
			fail(w, "get connector", err)
			return
		}
		if !c.Enabled {
			policy.WriteJSON(w, http.StatusConflict, apiError{Error: "the connector is disabled", Code: "disabled"})
			return
		}
		if err := svc.RequestSync(req.Context(), d.Workspace.ID, id); err != nil {
			fail(w, "request connector sync", err)
			return
		}
		audit(req, d, "connector.sync", id.String(), map[string]any{"name": c.Name, "type": c.Type})
		policy.WriteJSON(w, http.StatusAccepted, map[string]string{"status": "queued"})
	})

	az.Handle(r, http.MethodGet, item+"/syncs", policy.ConnectorRead, func(w http.ResponseWriter, req *http.Request) {
		d, ok := decision(w, req)
		if !ok {
			return
		}
		id, ok := connID(w, req)
		if !ok {
			return
		}
		limit := defaultSyncHistory
		if s := req.URL.Query().Get("limit"); s != "" {
			n, err := strconv.Atoi(s)
			if err != nil || n < 1 || n > maxSyncHistory {
				policy.WriteJSON(w, http.StatusBadRequest, apiError{Error: "limit must be between 1 and 200", Code: "bad_request"})
				return
			}
			limit = n
		}
		rows, err := svc.ListSyncs(req.Context(), d.Workspace.ID, id, limit)
		if err != nil {
			fail(w, "list connector syncs", err)
			return
		}
		out := make([]syncView, 0, len(rows))
		for _, s := range rows {
			v := syncView{ID: s.ID, Trigger: s.Trigger, Status: s.Status, StartedAt: s.StartedAt.Time.UTC(),
				FinishedAt: s.FinishedAt.Time.UTC(), DurationMs: int(s.DurationMs), Error: s.Error}
			_ = json.Unmarshal(s.Counts, &v.Counts)
			out = append(out, v)
		}
		policy.WriteJSON(w, http.StatusOK, out)
	})
}

// writeTestResult audits and answers a connector probe. A failed probe is an
// expected result (200 with ok=false), not an HTTP error; the message is
// already sanitized by the service.
func writeTestResult(w http.ResponseWriter, req *http.Request, d policy.Decision,
	audit func(*http.Request, policy.Decision, string, string, map[string]any), id string, details map[string]any, err error) {
	details["ok"] = err == nil
	audit(req, d, "connector.test", id, details)
	res := struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}{OK: err == nil}
	if err != nil {
		res.Error = err.Error()
	}
	policy.WriteJSON(w, http.StatusOK, res)
}
