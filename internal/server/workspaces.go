package server

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/BxnnyG/ansible-control-plane/internal/auth"
	"github.com/BxnnyG/ansible-control-plane/internal/db/dbgen"
	"github.com/BxnnyG/ansible-control-plane/internal/policy"
)

// MembershipLister lists the caller's workspaces; *dbgen.Queries implements it.
type MembershipLister interface {
	ListMembershipSummariesForUser(ctx context.Context, userID uuid.UUID) ([]dbgen.ListMembershipSummariesForUserRow, error)
}

// APIStore is the persistence of the workspace API; *dbgen.Queries implements it.
type APIStore interface {
	MembershipLister
	RenameWorkspace(ctx context.Context, arg dbgen.RenameWorkspaceParams) (dbgen.Workspace, error)
	CompleteWorkspaceSetup(ctx context.Context, id uuid.UUID) (dbgen.Workspace, error)
	InsertAudit(ctx context.Context, arg dbgen.InsertAuditParams) (dbgen.InsertAuditRow, error)
}

// WithPolicy mounts the workspace API. Every route is registered through the
// authorizer so it declares its permission.
func WithPolicy(az *policy.Authorizer, store APIStore) Option {
	return func(o *routerOpts) { o.az, o.store = az, store }
}

// apiRoutes registers /api/v1 routes on the root router.
func apiRoutes(r chi.Router, az *policy.Authorizer, store APIStore, info AuthInfo, log *slog.Logger) {
	setupRoutes(r, az, store, info, log)
	az.HandleAuthenticated(r, http.MethodGet, "/api/v1/workspaces", func(w http.ResponseWriter, req *http.Request) {
		rows, err := store.ListMembershipSummariesForUser(req.Context(), auth.FromContext(req.Context()).User.ID)
		if err != nil {
			log.Error("list workspaces", "error", err)
			policy.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
			return
		}
		type item struct {
			Slug   string `json:"slug"`
			Name   string `json:"name"`
			Role   string `json:"role"`
			Scoped bool   `json:"scoped"`
		}
		out := make([]item, 0, len(rows))
		for _, m := range rows {
			out = append(out, item{m.Slug, m.Name, m.Role, m.Scoped})
		}
		policy.WriteJSON(w, http.StatusOK, out)
	})
	az.Handle(r, http.MethodGet, "/api/v1/workspaces/{workspace}", policy.WorkspaceRead, func(w http.ResponseWriter, req *http.Request) {
		d, ok := policy.DecisionFromContext(req.Context())
		if !ok {
			policy.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
			return
		}
		perms := policy.Permissions(d.Membership.Role, d.Scoped())
		names := make([]string, len(perms))
		for i, p := range perms {
			names[i] = string(p)
		}
		policy.WriteJSON(w, http.StatusOK, map[string]any{
			"slug": d.Workspace.Slug, "name": d.Workspace.Name, "description": d.Workspace.Description,
			"role": d.Membership.Role, "scoped": d.Scoped(), "permissions": names,
		})
	})
}
