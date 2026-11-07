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
	"github.com/BxnnyG/ansible-control-plane/internal/policy"
	"github.com/BxnnyG/ansible-control-plane/internal/projects"
)

// ProjectsAPI is the projects service used by the HTTP layer; *projects.Service implements it.
type ProjectsAPI interface {
	Create(ctx context.Context, ws uuid.UUID, in projects.CreateInput) (projects.Project, error)
	List(ctx context.Context, ws uuid.UUID) ([]projects.Project, error)
	Get(ctx context.Context, ws, id uuid.UUID) (projects.Project, error)
	Update(ctx context.Context, ws, id uuid.UUID, in projects.UpdateInput) (projects.Project, error)
	Delete(ctx context.Context, ws, id uuid.UUID) error
	ScanHostKeys(ctx context.Context, ws, id uuid.UUID) ([]projects.HostKeyInfo, error)
	ConfirmHostKey(ctx context.Context, ws, id uuid.UUID, fingerprint string) (projects.Project, error)
	Test(ctx context.Context, ws, id uuid.UUID) error
	Sync(ctx context.Context, ws, id uuid.UUID) (projects.Revision, error)
	Revisions(ctx context.Context, ws, id uuid.UUID, limit int) ([]projects.Revision, error)
	LatestRevision(ctx context.Context, ws, id uuid.UUID) (projects.Revision, error)
	WebhooksAPI
}

// WithProjects mounts the projects API. It requires WithPolicy.
func WithProjects(p ProjectsAPI) Option { return func(o *routerOpts) { o.projects = p } }

const maxProjectBody = 16 << 10

type apiError struct {
	Error  string `json:"error"`
	Code   string `json:"code,omitempty"`
	Detail string `json:"detail,omitempty"`
}

// projectRoutes registers /api/v1/workspaces/{workspace}/projects.
func projectRoutes(r chi.Router, az *policy.Authorizer, store APIStore, svc ProjectsAPI, log *slog.Logger) {
	const base = "/api/v1/workspaces/{workspace}/projects"
	fail := func(w http.ResponseWriter, what string, err error) {
		var re *projects.RemoteError
		switch {
		case errors.Is(err, projects.ErrNotFound):
			policy.WriteJSON(w, http.StatusNotFound, apiError{Error: "project not found", Code: "not_found"})
		case errors.Is(err, projects.ErrConflict):
			policy.WriteJSON(w, http.StatusConflict, apiError{Error: err.Error(), Code: "conflict"})
		case errors.Is(err, projects.ErrHostKeyNotPinned):
			policy.WriteJSON(w, http.StatusConflict, apiError{Error: err.Error(), Code: "hostkey_not_pinned"})
		case errors.Is(err, projects.ErrSyncInProgress):
			policy.WriteJSON(w, http.StatusConflict, apiError{Error: err.Error(), Code: "sync_in_progress"})
		case errors.Is(err, projects.ErrHostKeyMismatch):
			policy.WriteJSON(w, http.StatusConflict, apiError{Error: err.Error(), Code: "hostkey_mismatch"})
		case errors.Is(err, projects.ErrNotSSH):
			policy.WriteJSON(w, http.StatusUnprocessableEntity, apiError{Error: err.Error(), Code: "not_ssh"})
		case errors.Is(err, projects.ErrInsecureCredential):
			policy.WriteJSON(w, http.StatusUnprocessableEntity, apiError{Error: err.Error(), Code: "insecure_credential"})
		case errors.Is(err, projects.ErrCredentialReselect):
			policy.WriteJSON(w, http.StatusUnprocessableEntity, apiError{Error: err.Error(), Code: "credential_reselect_required"})
		case errors.Is(err, projects.ErrInvalid):
			policy.WriteJSON(w, http.StatusUnprocessableEntity, apiError{Error: err.Error(), Code: "invalid"})
		case errors.As(err, &re):
			policy.WriteJSON(w, http.StatusBadGateway, apiError{Error: "git operation failed", Code: "remote_failed", Detail: re.Msg})
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
	// target resolves the workspace decision and the {id} path parameter.
	target := func(w http.ResponseWriter, req *http.Request) (policy.Decision, uuid.UUID, bool) {
		d, ok := decision(w, req)
		if !ok {
			return d, uuid.Nil, false
		}
		id, err := uuid.Parse(chi.URLParam(req, "id"))
		if err != nil {
			policy.WriteJSON(w, http.StatusNotFound, apiError{Error: "project not found", Code: "not_found"})
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
	decode := func(w http.ResponseWriter, req *http.Request, v any) bool {
		dec := json.NewDecoder(http.MaxBytesReader(w, req.Body, maxProjectBody))
		dec.DisallowUnknownFields()
		if err := dec.Decode(v); err != nil {
			policy.WriteJSON(w, http.StatusBadRequest, apiError{Error: "malformed request", Code: "bad_request"})
			return false
		}
		return true
	}

	az.Handle(r, http.MethodGet, base, policy.ProjectRead, func(w http.ResponseWriter, req *http.Request) {
		d, ok := decision(w, req)
		if !ok {
			return
		}
		list, err := svc.List(req.Context(), d.Workspace.ID)
		if err != nil {
			fail(w, "list projects", err)
			return
		}
		policy.WriteJSON(w, http.StatusOK, list)
	})

	az.Handle(r, http.MethodPost, base, policy.ProjectWrite, func(w http.ResponseWriter, req *http.Request) {
		d, ok := decision(w, req)
		if !ok {
			return
		}
		var body struct {
			Name                string     `json:"name"`
			Slug                string     `json:"slug"`
			URL                 string     `json:"url"`
			DefaultRef          string     `json:"default_ref"`
			CredentialSecretID  *uuid.UUID `json:"credential_secret_id"`
			HTTPSUsername       string     `json:"https_username"`
			SyncIntervalSeconds *int       `json:"sync_interval_seconds"`
		}
		if !decode(w, req, &body) {
			return
		}
		p, err := svc.Create(req.Context(), d.Workspace.ID, projects.CreateInput{
			Name: body.Name, Slug: body.Slug, URL: body.URL, DefaultRef: body.DefaultRef,
			CredentialSecretID: body.CredentialSecretID, HTTPSUsername: body.HTTPSUsername,
			SyncIntervalSeconds: body.SyncIntervalSeconds, CreatedBy: auth.FromContext(req.Context()).User.ID,
		})
		if err != nil {
			fail(w, "create project", err)
			return
		}
		audit(req, d, "project.create", p.ID, map[string]any{"name": p.Name, "slug": p.Slug})
		policy.WriteJSON(w, http.StatusCreated, p)
	})

	az.Handle(r, http.MethodGet, base+"/{id}", policy.ProjectRead, func(w http.ResponseWriter, req *http.Request) {
		d, id, ok := target(w, req)
		if !ok {
			return
		}
		p, err := svc.Get(req.Context(), d.Workspace.ID, id)
		if err != nil {
			fail(w, "get project", err)
			return
		}
		policy.WriteJSON(w, http.StatusOK, p)
	})

	az.Handle(r, http.MethodPut, base+"/{id}", policy.ProjectWrite, func(w http.ResponseWriter, req *http.Request) {
		d, id, ok := target(w, req)
		if !ok {
			return
		}
		var body struct {
			Name                *string                   `json:"name"`
			Slug                *string                   `json:"slug"`
			URL                 *string                   `json:"url"`
			DefaultRef          *string                   `json:"default_ref"`
			CredentialSecretID  projects.Field[uuid.UUID] `json:"credential_secret_id"`
			HTTPSUsername       *string                   `json:"https_username"`
			SyncIntervalSeconds projects.Field[int]       `json:"sync_interval_seconds"`
		}
		if !decode(w, req, &body) {
			return
		}
		if body.Name == nil && body.Slug == nil && body.URL == nil && body.DefaultRef == nil && body.HTTPSUsername == nil &&
			!body.CredentialSecretID.Set && !body.SyncIntervalSeconds.Set {
			policy.WriteJSON(w, http.StatusUnprocessableEntity, apiError{Error: "nothing to update", Code: "invalid"})
			return
		}
		p, err := svc.Update(req.Context(), d.Workspace.ID, id, projects.UpdateInput{
			Name: body.Name, Slug: body.Slug, URL: body.URL, DefaultRef: body.DefaultRef,
			HTTPSUsername: body.HTTPSUsername, CredentialSecretID: body.CredentialSecretID,
			SyncIntervalSeconds: body.SyncIntervalSeconds,
		})
		if err != nil {
			fail(w, "update project", err)
			return
		}
		audit(req, d, "project.update", id, map[string]any{"name": p.Name, "url_changed": body.URL != nil,
			"credential_changed": body.CredentialSecretID.Set})
		policy.WriteJSON(w, http.StatusOK, p)
	})

	az.Handle(r, http.MethodDelete, base+"/{id}", policy.ProjectWrite, func(w http.ResponseWriter, req *http.Request) {
		d, id, ok := target(w, req)
		if !ok {
			return
		}
		p, err := svc.Get(req.Context(), d.Workspace.ID, id)
		if err != nil {
			fail(w, "get project", err)
			return
		}
		if err := svc.Delete(req.Context(), d.Workspace.ID, id); err != nil {
			fail(w, "delete project", err)
			return
		}
		audit(req, d, "project.delete", id, map[string]any{"name": p.Name, "slug": p.Slug})
		w.WriteHeader(http.StatusNoContent)
	})

	az.Handle(r, http.MethodPost, base+"/{id}/hostkey/scan", policy.ProjectWrite, func(w http.ResponseWriter, req *http.Request) {
		d, id, ok := target(w, req)
		if !ok {
			return
		}
		keys, err := svc.ScanHostKeys(req.Context(), d.Workspace.ID, id)
		if err != nil {
			fail(w, "scan host keys", err)
			return
		}
		policy.WriteJSON(w, http.StatusOK, map[string]any{"keys": keys})
	})

	az.Handle(r, http.MethodPost, base+"/{id}/hostkey/confirm", policy.ProjectWrite, func(w http.ResponseWriter, req *http.Request) {
		d, id, ok := target(w, req)
		if !ok {
			return
		}
		var body struct {
			Fingerprint string `json:"fingerprint"`
		}
		if !decode(w, req, &body) {
			return
		}
		p, err := svc.ConfirmHostKey(req.Context(), d.Workspace.ID, id, body.Fingerprint)
		if err != nil {
			fail(w, "confirm host key", err)
			return
		}
		audit(req, d, "project.hostkey_pin", id, map[string]any{"fingerprint": body.Fingerprint})
		policy.WriteJSON(w, http.StatusOK, p)
	})

	az.Handle(r, http.MethodPost, base+"/{id}/test", policy.ProjectSync, func(w http.ResponseWriter, req *http.Request) {
		d, id, ok := target(w, req)
		if !ok {
			return
		}
		if err := svc.Test(req.Context(), d.Workspace.ID, id); err != nil {
			fail(w, "test project", err)
			return
		}
		policy.WriteJSON(w, http.StatusOK, map[string]bool{"ok": true})
	})

	az.Handle(r, http.MethodPost, base+"/{id}/sync", policy.ProjectSync, func(w http.ResponseWriter, req *http.Request) {
		d, id, ok := target(w, req)
		if !ok {
			return
		}
		rev, err := svc.Sync(req.Context(), d.Workspace.ID, id)
		if err != nil {
			var re *projects.RemoteError
			if errors.As(err, &re) {
				audit(req, d, "project.sync_failed", id, map[string]any{"error": re.Msg})
			}
			fail(w, "sync project", err)
			return
		}
		audit(req, d, "project.sync", id, map[string]any{"sha": rev.SHA})
		policy.WriteJSON(w, http.StatusOK, rev)
	})

	az.Handle(r, http.MethodGet, base+"/{id}/revisions", policy.ProjectRead, func(w http.ResponseWriter, req *http.Request) {
		d, id, ok := target(w, req)
		if !ok {
			return
		}
		limit, _ := strconv.Atoi(req.URL.Query().Get("limit"))
		revs, err := svc.Revisions(req.Context(), d.Workspace.ID, id, limit)
		if err != nil {
			fail(w, "list revisions", err)
			return
		}
		policy.WriteJSON(w, http.StatusOK, revs)
	})

	az.Handle(r, http.MethodGet, base+"/{id}/revisions/latest", policy.ProjectRead, func(w http.ResponseWriter, req *http.Request) {
		d, id, ok := target(w, req)
		if !ok {
			return
		}
		rev, err := svc.LatestRevision(req.Context(), d.Workspace.ID, id)
		if err != nil {
			fail(w, "latest revision", err)
			return
		}
		policy.WriteJSON(w, http.StatusOK, rev)
	})
}
