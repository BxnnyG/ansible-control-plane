package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/BxnnyG/ansible-control-plane/internal/auth"
	"github.com/BxnnyG/ansible-control-plane/internal/db/dbgen"
	"github.com/BxnnyG/ansible-control-plane/internal/policy"
	"github.com/BxnnyG/ansible-control-plane/internal/projects"
	"github.com/BxnnyG/ansible-control-plane/internal/vaultids"
	"github.com/BxnnyG/ansible-control-plane/internal/vaultops"
)

// VaultIdentitiesAPI is the vault identity service used by the HTTP layer;
// *vaultids.Service implements it.
type VaultIdentitiesAPI interface {
	Create(ctx context.Context, ws uuid.UUID, in vaultids.Input) (vaultids.Identity, error)
	List(ctx context.Context, ws uuid.UUID) ([]vaultids.Identity, error)
	Get(ctx context.Context, ws, id uuid.UUID) (vaultids.Identity, error)
	Update(ctx context.Context, ws, id uuid.UUID, in vaultids.Input) (vaultids.Identity, error)
	Delete(ctx context.Context, ws, id uuid.UUID) error
}

// VaultOpsAPI is the project vault operations service; *vaultops.Service implements it.
type VaultOpsAPI interface {
	List(ctx context.Context, ws, project uuid.UUID) (vaultops.Listing, error)
	Decrypt(ctx context.Context, ws, project uuid.UUID, in vaultops.DecryptInput) (vaultops.Decrypted, error)
	Encrypt(ctx context.Context, ws, project uuid.UUID, in vaultops.EncryptInput) (projects.WriteResult, error)
	Rekey(ctx context.Context, ws, project uuid.UUID, in vaultops.RekeyInput) (projects.WriteResult, error)
}

// WithVault mounts the vault identity and project vault APIs. It requires WithPolicy.
func WithVault(ids VaultIdentitiesAPI, ops VaultOpsAPI) Option {
	return func(o *routerOpts) { o.vaultIDs, o.vaultOps = ids, ops }
}

const (
	maxVaultIdentityBody = 16 << 10
	// The body of an encrypt request carries up to vaultops.MaxPlaintext bytes
	// of plaintext, which JSON may escape.
	maxVaultOpBody = 4 << 20
)

// vaultRoutes registers /api/v1/workspaces/{workspace}/vault-identities and
// /api/v1/workspaces/{workspace}/projects/{id}/vault. Plaintext and passwords
// are never logged; decrypt responses are marked no-store and audited per call.
func vaultRoutes(r chi.Router, az *policy.Authorizer, store APIStore, ids VaultIdentitiesAPI, ops VaultOpsAPI, log *slog.Logger) {
	const idBase = "/api/v1/workspaces/{workspace}/vault-identities"
	const prjBase = "/api/v1/workspaces/{workspace}/projects/{id}/vault"

	fail := func(w http.ResponseWriter, what string, err error) {
		var ce *projects.ConflictError
		switch {
		case errors.Is(err, vaultids.ErrNotFound):
			policy.WriteJSON(w, http.StatusNotFound, apiError{Error: "vault identity not found", Code: "not_found"})
		case errors.Is(err, vaultids.ErrConflict):
			policy.WriteJSON(w, http.StatusConflict, apiError{Error: err.Error(), Code: "conflict"})
		case errors.Is(err, vaultids.ErrProviderNotSupported):
			policy.WriteJSON(w, http.StatusUnprocessableEntity, apiError{
				Error: "the app cannot obtain the password of this identity; script identities are only used by the worker",
				Code:  "provider_not_supported_in_app"})
		case errors.Is(err, vaultids.ErrNoPassword):
			policy.WriteJSON(w, http.StatusUnprocessableEntity, apiError{Error: err.Error(), Code: "no_password_source"})
		case errors.Is(err, vaultids.ErrInvalid), errors.Is(err, vaultops.ErrInvalid), errors.Is(err, projects.ErrInvalid):
			policy.WriteJSON(w, http.StatusUnprocessableEntity, apiError{Error: err.Error(), Code: "invalid"})
		case errors.Is(err, projects.ErrNotFound):
			policy.WriteJSON(w, http.StatusNotFound, apiError{Error: "project not found or not synced", Code: "not_found"})
		case errors.Is(err, vaultops.ErrFileNotFound):
			policy.WriteJSON(w, http.StatusNotFound, apiError{Error: err.Error(), Code: "file_not_found"})
		case errors.Is(err, vaultops.ErrKeyNotFound):
			policy.WriteJSON(w, http.StatusNotFound, apiError{Error: err.Error(), Code: "key_not_found"})
		case errors.Is(err, vaultops.ErrNotVault):
			policy.WriteJSON(w, http.StatusUnprocessableEntity, apiError{Error: err.Error(), Code: "not_vault"})
		case errors.Is(err, vaultops.ErrNoIdentity):
			policy.WriteJSON(w, http.StatusUnprocessableEntity, apiError{Error: err.Error(), Code: "no_identity"})
		case errors.Is(err, vaultops.ErrDecrypt):
			policy.WriteJSON(w, http.StatusUnprocessableEntity, apiError{Error: err.Error(), Code: "decrypt_failed"})
		case errors.Is(err, vaultops.ErrTooLarge):
			policy.WriteJSON(w, http.StatusRequestEntityTooLarge, apiError{Error: err.Error(), Code: "too_large"})
		case errors.As(err, &ce):
			paths := ce.Paths
			if paths == nil {
				paths = []string{}
			}
			policy.WriteJSON(w, http.StatusConflict, struct {
				apiError
				Paths []string `json:"paths"`
			}{apiError{Error: err.Error(), Code: "git_conflict"}, paths})
		case errors.As(err, new(*projects.RemoteError)):
			policy.WriteJSON(w, http.StatusBadGateway, apiError{Error: "the git remote rejected the operation", Code: "remote_error", Detail: err.Error()})
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
	uid := func(w http.ResponseWriter, req *http.Request, what string) (policy.Decision, uuid.UUID, bool) {
		d, ok := decision(w, req)
		if !ok {
			return d, uuid.Nil, false
		}
		id, err := uuid.Parse(chi.URLParam(req, "id"))
		if err != nil {
			policy.WriteJSON(w, http.StatusNotFound, apiError{Error: what + " not found", Code: "not_found"})
			return d, uuid.Nil, false
		}
		return d, id, true
	}
	// writeAudit stores an audit entry and reports whether it was stored.
	writeAudit := func(req *http.Request, d policy.Decision, action, targetType string, id uuid.UUID, details map[string]any) bool {
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
			return false
		}
		return true
	}
	decode := func(w http.ResponseWriter, req *http.Request, limit int64, v any) bool {
		dec := json.NewDecoder(http.MaxBytesReader(w, req.Body, limit))
		dec.DisallowUnknownFields()
		if err := dec.Decode(v); err != nil {
			code := http.StatusBadRequest
			var mbe *http.MaxBytesError
			if errors.As(err, &mbe) {
				code = http.StatusRequestEntityTooLarge
			}
			policy.WriteJSON(w, code, apiError{Error: "malformed request", Code: "bad_request"})
			return false
		}
		return true
	}

	// --- vault identities -------------------------------------------------

	type identityBody struct {
		Label       string     `json:"label"`
		Description string     `json:"description"`
		Provider    string     `json:"provider"`
		SecretID    *uuid.UUID `json:"secret_id"`
		ScriptPath  string     `json:"script_path"`
		ScriptArgs  []string   `json:"script_args"`
	}
	input := func(req *http.Request, b identityBody) vaultids.Input {
		return vaultids.Input{Label: b.Label, Description: b.Description, Provider: b.Provider, SecretID: b.SecretID,
			ScriptPath: b.ScriptPath, ScriptArgs: b.ScriptArgs, CreatedBy: auth.FromContext(req.Context()).User.ID}
	}
	idDetails := func(i vaultids.Identity) map[string]any {
		return map[string]any{"label": i.Label, "provider": i.Provider}
	}

	az.Handle(r, http.MethodGet, idBase, policy.VaultRead, func(w http.ResponseWriter, req *http.Request) {
		d, ok := decision(w, req)
		if !ok {
			return
		}
		list, err := ids.List(req.Context(), d.Workspace.ID)
		if err != nil {
			fail(w, "list vault identities", err)
			return
		}
		policy.WriteJSON(w, http.StatusOK, list)
	})

	az.Handle(r, http.MethodPost, idBase, policy.VaultManage, func(w http.ResponseWriter, req *http.Request) {
		d, ok := decision(w, req)
		if !ok {
			return
		}
		var body identityBody
		if !decode(w, req, maxVaultIdentityBody, &body) {
			return
		}
		i, err := ids.Create(req.Context(), d.Workspace.ID, input(req, body))
		if err != nil {
			fail(w, "create vault identity", err)
			return
		}
		writeAudit(req, d, "vault_identity.create", "vault_identity", i.ID, idDetails(i))
		policy.WriteJSON(w, http.StatusCreated, i)
	})

	az.Handle(r, http.MethodGet, idBase+"/{id}", policy.VaultRead, func(w http.ResponseWriter, req *http.Request) {
		d, id, ok := uid(w, req, "vault identity")
		if !ok {
			return
		}
		i, err := ids.Get(req.Context(), d.Workspace.ID, id)
		if err != nil {
			fail(w, "get vault identity", err)
			return
		}
		policy.WriteJSON(w, http.StatusOK, i)
	})

	az.Handle(r, http.MethodPut, idBase+"/{id}", policy.VaultManage, func(w http.ResponseWriter, req *http.Request) {
		d, id, ok := uid(w, req, "vault identity")
		if !ok {
			return
		}
		var body identityBody
		if !decode(w, req, maxVaultIdentityBody, &body) {
			return
		}
		i, err := ids.Update(req.Context(), d.Workspace.ID, id, input(req, body))
		if err != nil {
			fail(w, "update vault identity", err)
			return
		}
		writeAudit(req, d, "vault_identity.update", "vault_identity", id, idDetails(i))
		policy.WriteJSON(w, http.StatusOK, i)
	})

	az.Handle(r, http.MethodDelete, idBase+"/{id}", policy.VaultManage, func(w http.ResponseWriter, req *http.Request) {
		d, id, ok := uid(w, req, "vault identity")
		if !ok {
			return
		}
		cur, err := ids.Get(req.Context(), d.Workspace.ID, id)
		if err != nil {
			fail(w, "get vault identity", err)
			return
		}
		if err := ids.Delete(req.Context(), d.Workspace.ID, id); err != nil {
			fail(w, "delete vault identity", err)
			return
		}
		writeAudit(req, d, "vault_identity.delete", "vault_identity", id, idDetails(cur))
		w.WriteHeader(http.StatusNoContent)
	})

	// --- project vault operations ----------------------------------------

	author := func(req *http.Request) projects.Author {
		u := auth.FromContext(req.Context()).User
		return projects.Author{Name: u.DisplayName, Email: u.Email}
	}
	writeResult := func(w http.ResponseWriter, req *http.Request, d policy.Decision, id uuid.UUID, action string, res projects.WriteResult, details map[string]any) {
		if !res.NoOp {
			details["branch"], details["sha"], details["paths"] = res.Branch, res.SHA, res.Paths
			writeAudit(req, d, action, "project", id, details)
		}
		policy.WriteJSON(w, http.StatusOK, res)
	}

	az.Handle(r, http.MethodGet, prjBase, policy.VaultRead, func(w http.ResponseWriter, req *http.Request) {
		d, id, ok := uid(w, req, "project")
		if !ok {
			return
		}
		l, err := ops.List(req.Context(), d.Workspace.ID, id)
		if err != nil {
			fail(w, "list project vault", err)
			return
		}
		policy.WriteJSON(w, http.StatusOK, l)
	})

	az.Handle(r, http.MethodPost, prjBase+"/decrypt", policy.VaultDecrypt, func(w http.ResponseWriter, req *http.Request) {
		d, id, ok := uid(w, req, "project")
		if !ok {
			return
		}
		var body struct {
			Path    string `json:"path"`
			KeyPath string `json:"key_path"`
		}
		if !decode(w, req, maxVaultIdentityBody, &body) {
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		details := map[string]any{"path": body.Path}
		if body.KeyPath != "" {
			details["key_path"] = body.KeyPath
		}
		out, err := ops.Decrypt(req.Context(), d.Workspace.ID, id, vaultops.DecryptInput{Path: body.Path, KeyPath: body.KeyPath})
		if err != nil {
			details["outcome"] = "failed"
			writeAudit(req, d, "vault.decrypt", "project", id, details)
			fail(w, "decrypt vault", err)
			return
		}
		details["outcome"], details["identity"] = "ok", out.Identity
		// Fail closed: no audit entry, no plaintext.
		if !writeAudit(req, d, "vault.decrypt", "project", id, details) {
			policy.WriteJSON(w, http.StatusInternalServerError, apiError{Error: "internal error"})
			return
		}
		resp := struct {
			Plaintext string `json:"plaintext"`
			Encoding  string `json:"encoding"`
			Identity  string `json:"identity"`
		}{Identity: out.Identity}
		if utf8.Valid(out.Plaintext) {
			resp.Plaintext, resp.Encoding = string(out.Plaintext), "utf-8"
		} else {
			resp.Plaintext, resp.Encoding = base64.StdEncoding.EncodeToString(out.Plaintext), "base64"
		}
		policy.WriteJSON(w, http.StatusOK, resp)
	})

	az.Handle(r, http.MethodPost, prjBase+"/encrypt", policy.VaultManage, func(w http.ResponseWriter, req *http.Request) {
		d, id, ok := uid(w, req, "project")
		if !ok {
			return
		}
		var body struct {
			Path      string  `json:"path"`
			KeyPath   string  `json:"key_path"`
			Plaintext *string `json:"plaintext"`
			Identity  string  `json:"identity"`
			Branch    string  `json:"branch"`
			Message   string  `json:"message"`
		}
		if !decode(w, req, maxVaultOpBody, &body) {
			return
		}
		res, err := ops.Encrypt(req.Context(), d.Workspace.ID, id, vaultops.EncryptInput{
			Path: body.Path, KeyPath: body.KeyPath, Plaintext: body.Plaintext, Identity: body.Identity,
			WriteOptions: vaultops.WriteOptions{Branch: body.Branch, Message: body.Message, Author: author(req)},
		})
		if err != nil {
			fail(w, "encrypt vault", err)
			return
		}
		details := map[string]any{"path": body.Path, "identity": identityOrDefault(body.Identity)}
		if body.KeyPath != "" {
			details["key_path"] = body.KeyPath
		}
		writeResult(w, req, d, id, "vault.encrypt", res, details)
	})

	az.Handle(r, http.MethodPost, prjBase+"/rekey", policy.VaultManage, func(w http.ResponseWriter, req *http.Request) {
		d, id, ok := uid(w, req, "project")
		if !ok {
			return
		}
		var body struct {
			Paths   []string `json:"paths"`
			From    string   `json:"from"`
			To      string   `json:"to"`
			Branch  string   `json:"branch"`
			Message string   `json:"message"`
		}
		if !decode(w, req, maxVaultIdentityBody, &body) {
			return
		}
		res, err := ops.Rekey(req.Context(), d.Workspace.ID, id, vaultops.RekeyInput{
			Paths: body.Paths, From: body.From, To: body.To,
			WriteOptions: vaultops.WriteOptions{Branch: body.Branch, Message: body.Message, Author: author(req)},
		})
		if err != nil {
			fail(w, "rekey vault", err)
			return
		}
		writeResult(w, req, d, id, "vault.rekey", res, map[string]any{"from": body.From, "to": body.To})
	})
}

func identityOrDefault(l string) string {
	if l == "" {
		return vaultids.DefaultLabel
	}
	return l
}
