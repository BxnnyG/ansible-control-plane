package server

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/BxnnyG/ansible-control-plane/internal/auth"
	"github.com/BxnnyG/ansible-control-plane/internal/db/dbgen"
	"github.com/BxnnyG/ansible-control-plane/internal/policy"
	"github.com/BxnnyG/ansible-control-plane/internal/secrets"
)

// SecretsAPI is the secrets service used by the HTTP layer; *secrets.Service implements it.
type SecretsAPI interface {
	Create(ctx context.Context, ws uuid.UUID, in secrets.CreateInput) (secrets.Meta, error)
	List(ctx context.Context, ws uuid.UUID) ([]secrets.Meta, error)
	Get(ctx context.Context, ws, id uuid.UUID) (secrets.Meta, error)
	Update(ctx context.Context, ws, id uuid.UUID, in secrets.UpdateInput) (secrets.Meta, error)
	Delete(ctx context.Context, ws, id uuid.UUID) error
}

// WithSecrets mounts the write-only secrets API. It requires WithPolicy.
func WithSecrets(s SecretsAPI) Option { return func(o *routerOpts) { o.secrets = s } }

const maxSecretBody = 128 << 10

// secretRoutes registers /api/v1/workspaces/{workspace}/secrets. Values are
// accepted on create/replace and never returned or logged.
func secretRoutes(r chi.Router, az *policy.Authorizer, store APIStore, svc SecretsAPI, log *slog.Logger) {
	const base = "/api/v1/workspaces/{workspace}/secrets"
	fail := func(w http.ResponseWriter, what string, err error) {
		switch {
		case errors.Is(err, secrets.ErrNotFound):
			policy.WriteJSON(w, http.StatusNotFound, map[string]string{"error": "secret not found"})
		case errors.Is(err, secrets.ErrConflict):
			policy.WriteJSON(w, http.StatusConflict, map[string]string{"error": "a secret with this name already exists"})
		case errors.Is(err, secrets.ErrInvalid):
			policy.WriteJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
		default:
			log.Error(what, "error", err)
			policy.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		}
	}
	decision := func(w http.ResponseWriter, req *http.Request) (policy.Decision, bool) {
		d, ok := policy.DecisionFromContext(req.Context())
		if !ok {
			policy.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		}
		return d, ok
	}
	secretID := func(w http.ResponseWriter, req *http.Request) (uuid.UUID, bool) {
		id, err := uuid.Parse(chi.URLParam(req, "id"))
		if err != nil {
			policy.WriteJSON(w, http.StatusNotFound, map[string]string{"error": "secret not found"})
			return uuid.Nil, false
		}
		return id, true
	}
	audit := func(req *http.Request, d policy.Decision, action string, id uuid.UUID, details map[string]any) {
		b, _ := json.Marshal(details)
		p := auth.FromContext(req.Context())
		actx, cancel := context.WithTimeout(context.WithoutCancel(req.Context()), 5*time.Second)
		defer cancel()
		_, err := store.InsertAudit(actx, dbgen.InsertAuditParams{
			ActorID: pgtype.UUID{Bytes: p.User.ID, Valid: true}, ActorLabel: p.User.DisplayName,
			Action: action, TargetType: "secret", TargetID: id.String(),
			WorkspaceID: pgtype.UUID{Bytes: d.Workspace.ID, Valid: true}, Details: b,
		})
		if err != nil {
			log.Error("audit write failed", "action", action, "error", err)
		}
	}
	decode := func(w http.ResponseWriter, req *http.Request, v any) bool {
		dec := json.NewDecoder(http.MaxBytesReader(w, req.Body, maxSecretBody))
		dec.DisallowUnknownFields()
		if err := dec.Decode(v); err != nil {
			policy.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "malformed request"})
			return false
		}
		return true
	}

	az.Handle(r, http.MethodGet, base, policy.SecretsMetaRead, func(w http.ResponseWriter, req *http.Request) {
		d, ok := decision(w, req)
		if !ok {
			return
		}
		list, err := svc.List(req.Context(), d.Workspace.ID)
		if err != nil {
			fail(w, "list secrets", err)
			return
		}
		policy.WriteJSON(w, http.StatusOK, list)
	})

	az.Handle(r, http.MethodPost, base, policy.SecretsWrite, func(w http.ResponseWriter, req *http.Request) {
		d, ok := decision(w, req)
		if !ok {
			return
		}
		var body struct {
			Name        string `json:"name"`
			Type        string `json:"type"`
			Description string `json:"description"`
			Value       string `json:"value"`
		}
		if !decode(w, req, &body) {
			return
		}
		m, err := svc.Create(req.Context(), d.Workspace.ID, secrets.CreateInput{
			Name: body.Name, Type: body.Type, Description: body.Description,
			Value: []byte(body.Value), CreatedBy: auth.FromContext(req.Context()).User.ID,
		})
		if err != nil {
			fail(w, "create secret", err)
			return
		}
		audit(req, d, "secret.create", m.ID, map[string]any{"name": m.Name, "type": m.Type})
		policy.WriteJSON(w, http.StatusCreated, m)
	})

	az.Handle(r, http.MethodGet, base+"/{id}", policy.SecretsMetaRead, func(w http.ResponseWriter, req *http.Request) {
		d, ok := decision(w, req)
		if !ok {
			return
		}
		id, ok := secretID(w, req)
		if !ok {
			return
		}
		m, err := svc.Get(req.Context(), d.Workspace.ID, id)
		if err != nil {
			fail(w, "get secret", err)
			return
		}
		policy.WriteJSON(w, http.StatusOK, m)
	})

	az.Handle(r, http.MethodPut, base+"/{id}", policy.SecretsWrite, func(w http.ResponseWriter, req *http.Request) {
		d, ok := decision(w, req)
		if !ok {
			return
		}
		id, ok := secretID(w, req)
		if !ok {
			return
		}
		var body struct {
			Name        *string `json:"name"`
			Description *string `json:"description"`
			Value       *string `json:"value"`
		}
		if !decode(w, req, &body) {
			return
		}
		if body.Name == nil && body.Description == nil && body.Value == nil {
			policy.WriteJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "nothing to update"})
			return
		}
		in := secrets.UpdateInput{Name: body.Name, Description: body.Description}
		if body.Value != nil {
			in.Value = []byte(*body.Value)
			if len(in.Value) == 0 {
				policy.WriteJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "invalid secret: value must not be empty"})
				return
			}
		}
		m, err := svc.Update(req.Context(), d.Workspace.ID, id, in)
		if err != nil {
			fail(w, "update secret", err)
			return
		}
		audit(req, d, "secret.update", id, map[string]any{
			"name": m.Name, "type": m.Type, "renamed": body.Name != nil,
			"description_changed": body.Description != nil, "value_replaced": body.Value != nil,
		})
		policy.WriteJSON(w, http.StatusOK, m)
	})

	az.Handle(r, http.MethodDelete, base+"/{id}", policy.SecretsWrite, func(w http.ResponseWriter, req *http.Request) {
		d, ok := decision(w, req)
		if !ok {
			return
		}
		id, ok := secretID(w, req)
		if !ok {
			return
		}
		// Fetch the name first so the audit entry stays meaningful after deletion.
		m, err := svc.Get(req.Context(), d.Workspace.ID, id)
		if err != nil {
			fail(w, "get secret", err)
			return
		}
		if err := svc.Delete(req.Context(), d.Workspace.ID, id); err != nil {
			fail(w, "delete secret", err)
			return
		}
		audit(req, d, "secret.delete", id, map[string]any{"name": m.Name, "type": m.Type})
		w.WriteHeader(http.StatusNoContent)
	})
}
