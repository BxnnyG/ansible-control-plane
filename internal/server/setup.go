package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/BxnnyG/ansible-control-plane/internal/auth"
	"github.com/BxnnyG/ansible-control-plane/internal/db/dbgen"
	"github.com/BxnnyG/ansible-control-plane/internal/policy"
)

// AuthInfo reports the non-secret authentication status; *auth.Service implements it.
type AuthInfo interface {
	Info() auth.Info
}

const maxWorkspaceName = 80

// validWorkspaceName trims the name and rejects empty, overlong or
// control-character input.
func validWorkspaceName(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if s == "" || utf8.RuneCountInString(s) > maxWorkspaceName || !utf8.ValidString(s) {
		return "", false
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return "", false
		}
	}
	return s, true
}

type setupAuth struct {
	OIDC       bool               `json:"oidc"`
	IssuerHost string             `json:"issuer_host"`
	RoleMap    []auth.RoleMapping `json:"role_mappings"`
	BreakGlass bool               `json:"breakglass"`
}

type setupResponse struct {
	Name        string     `json:"name"`
	Completed   bool       `json:"completed"`
	CompletedAt *time.Time `json:"completed_at"`
	Auth        setupAuth  `json:"auth"`
}

func setupResponseFor(ws dbgen.Workspace, info AuthInfo) setupResponse {
	out := setupResponse{Name: ws.Name, Auth: setupAuth{RoleMap: auth.RoleMappings()}}
	if ws.SetupCompletedAt.Valid {
		t := ws.SetupCompletedAt.Time
		out.Completed, out.CompletedAt = true, &t
	}
	if info != nil {
		in := info.Info()
		out.Auth.OIDC, out.Auth.IssuerHost, out.Auth.BreakGlass = in.OIDC, in.IssuerHost, in.BreakGlass
	}
	return out
}

// setupRoutes registers the first-run wizard endpoints.
func setupRoutes(r chi.Router, az *policy.Authorizer, store APIStore, info AuthInfo, log *slog.Logger) {
	internal := func(w http.ResponseWriter, what string, err error) {
		log.Error(what, "error", err)
		policy.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
	}
	audit := func(req *http.Request, d policy.Decision, action string, details map[string]any) {
		b, _ := json.Marshal(details)
		p := auth.FromContext(req.Context())
		// Audit must survive a client disconnect, so detach from request cancellation.
		actx, cancel := context.WithTimeout(context.WithoutCancel(req.Context()), 5*time.Second)
		defer cancel()
		_, err := store.InsertAudit(actx, dbgen.InsertAuditParams{
			ActorID: pgtype.UUID{Bytes: p.User.ID, Valid: true}, ActorLabel: p.User.DisplayName,
			Action: action, TargetType: "workspace", TargetID: d.Workspace.ID.String(),
			WorkspaceID: pgtype.UUID{Bytes: d.Workspace.ID, Valid: true}, Details: b,
		})
		if err != nil {
			log.Error("audit write failed", "action", action, "error", err)
		}
	}

	az.Handle(r, http.MethodGet, "/api/v1/workspaces/{workspace}/setup", policy.WorkspaceRead, func(w http.ResponseWriter, req *http.Request) {
		d, ok := policy.DecisionFromContext(req.Context())
		if !ok {
			policy.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
			return
		}
		policy.WriteJSON(w, http.StatusOK, setupResponseFor(d.Workspace, info))
	})

	az.Handle(r, http.MethodPut, "/api/v1/workspaces/{workspace}/setup", policy.WorkspaceManage, func(w http.ResponseWriter, req *http.Request) {
		d, ok := policy.DecisionFromContext(req.Context())
		if !ok {
			policy.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
			return
		}
		var body struct {
			Name string `json:"name"`
		}
		dec := json.NewDecoder(http.MaxBytesReader(w, req.Body, 4<<10))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&body); err != nil {
			policy.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "malformed request"})
			return
		}
		name, valid := validWorkspaceName(body.Name)
		if !valid {
			policy.WriteJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "name must be 1-80 characters without control characters"})
			return
		}
		ws, err := store.RenameWorkspace(req.Context(), dbgen.RenameWorkspaceParams{ID: d.Workspace.ID, Name: name})
		if err != nil {
			internal(w, "rename workspace", err)
			return
		}
		audit(req, d, "workspace.rename", map[string]any{"old_name": d.Workspace.Name, "new_name": name})
		policy.WriteJSON(w, http.StatusOK, setupResponseFor(ws, info))
	})

	az.Handle(r, http.MethodPost, "/api/v1/workspaces/{workspace}/setup/complete", policy.WorkspaceManage, func(w http.ResponseWriter, req *http.Request) {
		d, ok := policy.DecisionFromContext(req.Context())
		if !ok {
			policy.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
			return
		}
		ws, err := store.CompleteWorkspaceSetup(req.Context(), d.Workspace.ID)
		if err != nil {
			internal(w, "complete workspace setup", err)
			return
		}
		if !d.Workspace.SetupCompletedAt.Valid {
			audit(req, d, "workspace.setup.complete", nil)
		}
		policy.WriteJSON(w, http.StatusOK, setupResponseFor(ws, info))
	})
}
