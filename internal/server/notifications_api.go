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
	"github.com/BxnnyG/ansible-control-plane/internal/notify"
	"github.com/BxnnyG/ansible-control-plane/internal/policy"
	"github.com/BxnnyG/ansible-control-plane/internal/projects"
)

// NotificationsAPI is the notification service used by the HTTP layer;
// *notify.Service implements it.
type NotificationsAPI interface {
	CreateChannel(ctx context.Context, ws uuid.UUID, in notify.ChannelInput, actor uuid.UUID) (notify.ChannelView, error)
	GetChannel(ctx context.Context, ws, id uuid.UUID) (notify.ChannelView, error)
	ListChannels(ctx context.Context, ws uuid.UUID) ([]notify.ChannelView, error)
	UpdateChannel(ctx context.Context, ws, id uuid.UUID, in notify.ChannelInput, actor uuid.UUID) (notify.ChannelView, error)
	DeleteChannel(ctx context.Context, ws, id uuid.UUID) error
	CreateRule(ctx context.Context, ws uuid.UUID, in notify.RuleInput, actor uuid.UUID) (notify.RuleView, error)
	GetRule(ctx context.Context, ws, id uuid.UUID) (notify.RuleView, error)
	ListRules(ctx context.Context, ws uuid.UUID) ([]notify.RuleView, error)
	UpdateRule(ctx context.Context, ws, id uuid.UUID, in notify.RuleInput) (notify.RuleView, error)
	DeleteRule(ctx context.Context, ws, id uuid.UUID) error
	ListDeliveries(ctx context.Context, ws, channelID uuid.UUID, limit int) ([]notify.DeliveryView, error)
	SendTest(ctx context.Context, ws, id uuid.UUID) error
}

// WithNotifications mounts the notification API. It requires WithPolicy.
func WithNotifications(n NotificationsAPI) Option { return func(o *routerOpts) { o.notifications = n } }

const maxNotifyBody = 64 << 10

type channelCreateBody struct {
	Name          string          `json:"name"`
	Type          string          `json:"type"`
	Config        json.RawMessage `json:"config"`
	SecretID      *uuid.UUID      `json:"secret_id"`
	TLSCAPEM      string          `json:"tls_ca_pem"`
	TLSSkipVerify bool            `json:"tls_skip_verify"`
	Enabled       *bool           `json:"enabled"`
}

type channelPatchBody struct {
	Name          *string                   `json:"name"`
	Config        json.RawMessage           `json:"config"`
	SecretID      projects.Field[uuid.UUID] `json:"secret_id"`
	TLSCAPEM      *string                   `json:"tls_ca_pem"`
	TLSSkipVerify *bool                     `json:"tls_skip_verify"`
	Enabled       *bool                     `json:"enabled"`
}

func (b channelPatchBody) empty() bool {
	return b.Name == nil && len(b.Config) == 0 && !b.SecretID.Set && b.TLSCAPEM == nil && b.TLSSkipVerify == nil && b.Enabled == nil
}

type ruleCreateBody struct {
	ChannelID  uuid.UUID  `json:"channel_id"`
	Events     []string   `json:"events"`
	TemplateID *uuid.UUID `json:"template_id"`
	Enabled    *bool      `json:"enabled"`
}

type rulePatchBody struct {
	ChannelID  *uuid.UUID                `json:"channel_id"`
	Events     *[]string                 `json:"events"`
	TemplateID projects.Field[uuid.UUID] `json:"template_id"`
	Enabled    *bool                     `json:"enabled"`
}

func (b rulePatchBody) empty() bool {
	return b.ChannelID == nil && b.Events == nil && !b.TemplateID.Set && b.Enabled == nil
}

// notificationRoutes registers the notification API under
// /api/v1/workspaces/{workspace}/notifications. All routes are workspace-wide.
func notificationRoutes(r chi.Router, az *policy.Authorizer, store APIStore, svc NotificationsAPI, log *slog.Logger) {
	const base = "/api/v1/workspaces/{workspace}/notifications"
	fail := func(w http.ResponseWriter, what string, err error) {
		switch {
		case errors.Is(err, notify.ErrNotFound):
			policy.WriteJSON(w, http.StatusNotFound, apiError{Error: "not found", Code: "not_found"})
		case errors.Is(err, notify.ErrConflict):
			policy.WriteJSON(w, http.StatusConflict, apiError{Error: err.Error(), Code: "conflict"})
		case errors.Is(err, notify.ErrInvalid):
			policy.WriteJSON(w, http.StatusUnprocessableEntity, apiError{Error: err.Error(), Code: "invalid"})
		default:
			log.Error(what, "error", err)
			policy.WriteJSON(w, http.StatusInternalServerError, apiError{Error: "internal error"})
		}
	}
	invalid := func(w http.ResponseWriter, msg string) {
		policy.WriteJSON(w, http.StatusUnprocessableEntity, apiError{Error: msg, Code: "invalid"})
	}
	target := func(w http.ResponseWriter, req *http.Request) (policy.Decision, uuid.UUID, bool) {
		d, ok := policy.DecisionFromContext(req.Context())
		if !ok {
			policy.WriteJSON(w, http.StatusInternalServerError, apiError{Error: "internal error"})
			return d, uuid.Nil, false
		}
		id := uuid.Nil
		if raw := chi.URLParam(req, "id"); raw != "" {
			var err error
			if id, err = uuid.Parse(raw); err != nil {
				policy.WriteJSON(w, http.StatusNotFound, apiError{Error: "not found", Code: "not_found"})
				return d, uuid.Nil, false
			}
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
	decode := func(w http.ResponseWriter, req *http.Request, v any) bool {
		dec := json.NewDecoder(http.MaxBytesReader(w, req.Body, maxNotifyBody))
		dec.DisallowUnknownFields()
		if err := dec.Decode(v); err != nil {
			policy.WriteJSON(w, http.StatusBadRequest, apiError{Error: "malformed request", Code: "bad_request"})
			return false
		}
		return true
	}
	actor := func(req *http.Request) uuid.UUID { return auth.FromContext(req.Context()).User.ID }

	// Channels.
	az.Handle(r, http.MethodGet, base+"/channels", policy.NotificationRead, func(w http.ResponseWriter, req *http.Request) {
		d, _, ok := target(w, req)
		if !ok {
			return
		}
		list, err := svc.ListChannels(req.Context(), d.Workspace.ID)
		if err != nil {
			fail(w, "list notification channels", err)
			return
		}
		policy.WriteJSON(w, http.StatusOK, list)
	})
	az.Handle(r, http.MethodPost, base+"/channels", policy.NotificationManage, func(w http.ResponseWriter, req *http.Request) {
		d, _, ok := target(w, req)
		if !ok {
			return
		}
		var b channelCreateBody
		if !decode(w, req, &b) {
			return
		}
		in := notify.ChannelInput{Name: b.Name, Type: b.Type, Config: b.Config, SecretID: b.SecretID,
			TLSCAPEM: b.TLSCAPEM, TLSSkipVerify: b.TLSSkipVerify, Enabled: b.Enabled == nil || *b.Enabled}
		c, err := svc.CreateChannel(req.Context(), d.Workspace.ID, in, actor(req))
		if err != nil {
			fail(w, "create notification channel", err)
			return
		}
		audit(req, d, "notification.channel.create", "notification_channel", c.ID, map[string]any{"name": c.Name, "type": c.Type})
		policy.WriteJSON(w, http.StatusCreated, c)
	})
	az.Handle(r, http.MethodGet, base+"/channels/{id}", policy.NotificationRead, func(w http.ResponseWriter, req *http.Request) {
		d, id, ok := target(w, req)
		if !ok {
			return
		}
		c, err := svc.GetChannel(req.Context(), d.Workspace.ID, id)
		if err != nil {
			fail(w, "get notification channel", err)
			return
		}
		policy.WriteJSON(w, http.StatusOK, c)
	})
	az.Handle(r, http.MethodPatch, base+"/channels/{id}", policy.NotificationManage, func(w http.ResponseWriter, req *http.Request) {
		d, id, ok := target(w, req)
		if !ok {
			return
		}
		var b channelPatchBody
		if !decode(w, req, &b) {
			return
		}
		if b.empty() {
			invalid(w, "nothing to update")
			return
		}
		cur, err := svc.GetChannel(req.Context(), d.Workspace.ID, id)
		if err != nil {
			fail(w, "get notification channel", err)
			return
		}
		in := notify.ChannelInput{Name: cur.Name, Config: cur.Config, SecretID: cur.SecretID,
			TLSCAPEM: cur.TLSCAPEM, TLSSkipVerify: cur.TLSSkipVerify, Enabled: cur.Enabled}
		if b.Name != nil {
			in.Name = *b.Name
		}
		if len(b.Config) > 0 {
			in.Config = b.Config
		}
		if b.SecretID.Set {
			in.SecretID = b.SecretID.Value
		}
		if b.TLSCAPEM != nil {
			in.TLSCAPEM = *b.TLSCAPEM
		}
		if b.TLSSkipVerify != nil {
			in.TLSSkipVerify = *b.TLSSkipVerify
		}
		if b.Enabled != nil {
			in.Enabled = *b.Enabled
		}
		c, err := svc.UpdateChannel(req.Context(), d.Workspace.ID, id, in, actor(req))
		if err != nil {
			fail(w, "update notification channel", err)
			return
		}
		audit(req, d, "notification.channel.update", "notification_channel", c.ID, map[string]any{"name": c.Name, "enabled": c.Enabled})
		policy.WriteJSON(w, http.StatusOK, c)
	})
	az.Handle(r, http.MethodDelete, base+"/channels/{id}", policy.NotificationManage, func(w http.ResponseWriter, req *http.Request) {
		d, id, ok := target(w, req)
		if !ok {
			return
		}
		if err := svc.DeleteChannel(req.Context(), d.Workspace.ID, id); err != nil {
			fail(w, "delete notification channel", err)
			return
		}
		audit(req, d, "notification.channel.delete", "notification_channel", id, nil)
		w.WriteHeader(http.StatusNoContent)
	})
	az.Handle(r, http.MethodPost, base+"/channels/{id}/test", policy.NotificationManage, func(w http.ResponseWriter, req *http.Request) {
		d, id, ok := target(w, req)
		if !ok {
			return
		}
		if _, err := svc.GetChannel(req.Context(), d.Workspace.ID, id); err != nil {
			fail(w, "get notification channel", err)
			return
		}
		// A failed send is a result, not an API error; the text is sanitized.
		res := struct {
			OK    bool   `json:"ok"`
			Error string `json:"error"`
		}{OK: true}
		if err := svc.SendTest(req.Context(), d.Workspace.ID, id); err != nil {
			res.OK, res.Error = false, err.Error()
		}
		audit(req, d, "notification.channel.test", "notification_channel", id, map[string]any{"ok": res.OK})
		policy.WriteJSON(w, http.StatusOK, res)
	})

	// Rules.
	az.Handle(r, http.MethodGet, base+"/rules", policy.NotificationRead, func(w http.ResponseWriter, req *http.Request) {
		d, _, ok := target(w, req)
		if !ok {
			return
		}
		list, err := svc.ListRules(req.Context(), d.Workspace.ID)
		if err != nil {
			fail(w, "list notification rules", err)
			return
		}
		policy.WriteJSON(w, http.StatusOK, list)
	})
	az.Handle(r, http.MethodPost, base+"/rules", policy.NotificationManage, func(w http.ResponseWriter, req *http.Request) {
		d, _, ok := target(w, req)
		if !ok {
			return
		}
		var b ruleCreateBody
		if !decode(w, req, &b) {
			return
		}
		in := notify.RuleInput{ChannelID: b.ChannelID, Events: b.Events, TemplateID: b.TemplateID, Enabled: b.Enabled == nil || *b.Enabled}
		v, err := svc.CreateRule(req.Context(), d.Workspace.ID, in, actor(req))
		if err != nil {
			fail(w, "create notification rule", err)
			return
		}
		audit(req, d, "notification.rule.create", "notification_rule", v.ID, map[string]any{"channel_id": v.ChannelID, "events": v.Events})
		policy.WriteJSON(w, http.StatusCreated, v)
	})
	az.Handle(r, http.MethodPatch, base+"/rules/{id}", policy.NotificationManage, func(w http.ResponseWriter, req *http.Request) {
		d, id, ok := target(w, req)
		if !ok {
			return
		}
		var b rulePatchBody
		if !decode(w, req, &b) {
			return
		}
		if b.empty() {
			invalid(w, "nothing to update")
			return
		}
		cur, err := svc.GetRule(req.Context(), d.Workspace.ID, id)
		if err != nil {
			fail(w, "get notification rule", err)
			return
		}
		in := notify.RuleInput{ChannelID: cur.ChannelID, Events: cur.Events, TemplateID: cur.TemplateID, Enabled: cur.Enabled}
		if b.ChannelID != nil {
			in.ChannelID = *b.ChannelID
		}
		if b.Events != nil {
			in.Events = *b.Events
		}
		if b.TemplateID.Set {
			in.TemplateID = b.TemplateID.Value
		}
		if b.Enabled != nil {
			in.Enabled = *b.Enabled
		}
		v, err := svc.UpdateRule(req.Context(), d.Workspace.ID, id, in)
		if err != nil {
			fail(w, "update notification rule", err)
			return
		}
		audit(req, d, "notification.rule.update", "notification_rule", v.ID, map[string]any{"channel_id": v.ChannelID, "events": v.Events, "enabled": v.Enabled})
		policy.WriteJSON(w, http.StatusOK, v)
	})
	az.Handle(r, http.MethodDelete, base+"/rules/{id}", policy.NotificationManage, func(w http.ResponseWriter, req *http.Request) {
		d, id, ok := target(w, req)
		if !ok {
			return
		}
		if err := svc.DeleteRule(req.Context(), d.Workspace.ID, id); err != nil {
			fail(w, "delete notification rule", err)
			return
		}
		audit(req, d, "notification.rule.delete", "notification_rule", id, nil)
		w.WriteHeader(http.StatusNoContent)
	})

	// Deliveries: listed per channel (channel_id is required), newest first.
	az.Handle(r, http.MethodGet, base+"/deliveries", policy.NotificationRead, func(w http.ResponseWriter, req *http.Request) {
		d, _, ok := target(w, req)
		if !ok {
			return
		}
		q := req.URL.Query()
		ch, err := uuid.Parse(q.Get("channel_id"))
		if err != nil {
			invalid(w, "channel_id is required")
			return
		}
		status := q.Get("status")
		switch status {
		case "", "pending", "sent", "failed":
		default:
			invalid(w, "status must be pending, sent or failed")
			return
		}
		limit := 50
		if s := q.Get("limit"); s != "" {
			n, err := strconv.Atoi(s)
			if err != nil || n < 1 || n > 200 {
				invalid(w, "limit must be between 1 and 200")
				return
			}
			limit = n
		}
		fetch := limit
		if status != "" {
			fetch = 200 // filter after fetching the newest page
		}
		list, err := svc.ListDeliveries(req.Context(), d.Workspace.ID, ch, fetch)
		if err != nil {
			fail(w, "list notification deliveries", err)
			return
		}
		if status != "" {
			kept := list[:0]
			for _, v := range list {
				if v.Status == status && len(kept) < limit {
					kept = append(kept, v)
				}
			}
			list = kept
		}
		policy.WriteJSON(w, http.StatusOK, list)
	})
}
