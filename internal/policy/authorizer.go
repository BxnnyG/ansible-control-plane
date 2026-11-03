package policy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/BxnnyG/ansible-control-plane/internal/auth"
	"github.com/BxnnyG/ansible-control-plane/internal/db/dbgen"
)

// Errors returned by Authorize.
var (
	// ErrNotMember means the principal has no membership in the workspace.
	// HTTP handlers translate it to 404 so workspaces do not leak.
	ErrNotMember = errors.New("policy: not a member of the workspace")
	// ErrForbidden means the membership lacks the permission or scope.
	ErrForbidden = errors.New("policy: permission denied")
)

// WorkspaceParam is the chi URL parameter holding the workspace slug.
const WorkspaceParam = "workspace"

// Store is the persistence the authorizer needs; *dbgen.Queries implements it.
type Store interface {
	GetWorkspaceBySlug(ctx context.Context, slug string) (dbgen.Workspace, error)
	GetMembership(ctx context.Context, arg dbgen.GetMembershipParams) (dbgen.Membership, error)
	ListMembershipScopes(ctx context.Context, membershipID uuid.UUID) ([]dbgen.MembershipScope, error)
	InsertAudit(ctx context.Context, arg dbgen.InsertAuditParams) (dbgen.InsertAuditRow, error)
}

// Decision is what the middleware resolved for the request.
type Decision struct {
	Workspace  dbgen.Workspace
	Membership dbgen.Membership
	Scopes     []Scope
}

// Scoped reports whether the membership is restricted to a subset of the workspace.
func (d Decision) Scoped() bool { return len(d.Scopes) > 0 }

type ctxKey struct{}

// DecisionFromContext returns the decision stored by the workspace middleware.
func DecisionFromContext(ctx context.Context) (Decision, bool) {
	d, ok := ctx.Value(ctxKey{}).(Decision)
	return d, ok
}

// Authorizer evaluates permissions and registers routes with declared permissions.
type Authorizer struct {
	store    Store
	resolver ScopeResolver
	authn    func(http.Handler) http.Handler
	log      *slog.Logger

	mu     sync.Mutex
	routes map[string]string // "METHOD pattern" -> declared permission
}

// New builds an Authorizer. authn is the authentication middleware
// (auth.Service.Require); a nil resolver selects RefResolver.
func New(store Store, resolver ScopeResolver, authn func(http.Handler) http.Handler, log *slog.Logger) *Authorizer {
	if resolver == nil {
		resolver = RefResolver{}
	}
	return &Authorizer{store: store, resolver: resolver, authn: authn, log: log, routes: map[string]string{}}
}

// load resolves the caller's membership and scopes in the workspace.
func (a *Authorizer) load(ctx context.Context, ws dbgen.Workspace, userID uuid.UUID) (Decision, error) {
	m, err := a.store.GetMembership(ctx, dbgen.GetMembershipParams{WorkspaceID: ws.ID, UserID: userID})
	if errors.Is(err, pgx.ErrNoRows) {
		return Decision{}, ErrNotMember
	}
	if err != nil {
		return Decision{}, fmt.Errorf("load membership: %w", err)
	}
	rows, err := a.store.ListMembershipScopes(ctx, m.ID)
	if err != nil {
		return Decision{}, fmt.Errorf("load scopes: %w", err)
	}
	d := Decision{Workspace: ws, Membership: m}
	for _, r := range rows {
		d.Scopes = append(d.Scopes, Scope{Kind: r.Kind, RefID: r.RefID})
	}
	return d, nil
}

// check evaluates the role and scope rules. target nil means a workspace-level
// check (middleware): scoped memberships pass for non-workspace-wide permissions
// and the handler must narrow further through Authorize.
func (a *Authorizer) check(ctx context.Context, d Decision, perm Permission, target *Target) error {
	if !Allowed(d.Membership.Role, perm) {
		return ErrForbidden
	}
	if !d.Scoped() {
		return nil
	}
	if IsWorkspaceWide(perm) {
		return ErrForbidden
	}
	if target == nil {
		return nil
	}
	ok, err := a.resolver.InScope(ctx, d.Workspace.ID.String(), d.Scopes, *target)
	if err != nil {
		return fmt.Errorf("resolve scope: %w", err)
	}
	if !ok {
		return ErrForbidden
	}
	return nil
}

// Authorize checks a resource-level action for the principal. Scoped
// memberships are denied unless every part of target is inside their scopes.
func (a *Authorizer) Authorize(ctx context.Context, p auth.Principal, workspaceID uuid.UUID, perm Permission, target Target) error {
	d, err := a.load(ctx, dbgen.Workspace{ID: workspaceID}, p.User.ID)
	if err != nil {
		return err
	}
	err = a.check(ctx, d, perm, &target)
	if errors.Is(err, ErrForbidden) {
		a.auditDenied(ctx, p, d, perm, "")
	}
	return err
}

// Require returns middleware that resolves {workspace}, demands membership
// (404 otherwise) and the permission (403 otherwise). It must run after
// authentication.
func (a *Authorizer) Require(perm Permission) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p := auth.FromContext(r.Context())
			slug := chi.URLParam(r, WorkspaceParam)
			ws, err := a.store.GetWorkspaceBySlug(r.Context(), slug)
			if errors.Is(err, pgx.ErrNoRows) {
				notFound(w)
				return
			}
			if err != nil {
				a.fail(w, "load workspace", err)
				return
			}
			d, err := a.load(r.Context(), ws, p.User.ID)
			if errors.Is(err, ErrNotMember) {
				notFound(w)
				return
			}
			if err != nil {
				a.fail(w, "load membership", err)
				return
			}
			switch err := a.check(r.Context(), d, perm, nil); {
			case errors.Is(err, ErrForbidden):
				a.auditDenied(r.Context(), p, d, perm, r.Method)
				WriteJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden"})
				return
			case err != nil:
				a.fail(w, "check permission", err)
				return
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, d)))
		})
	}
}

// Handle registers an /api/v1 workspace route with its required permission.
// The pattern must contain {workspace}. Register on the root router only, so
// the pattern equals the full route.
func (a *Authorizer) Handle(r chi.Router, method, pattern string, perm Permission, h http.HandlerFunc) {
	if perm == "" {
		panic("policy: empty permission for " + method + " " + pattern)
	}
	if !containsParam(pattern) {
		panic("policy: pattern lacks {workspace}: " + pattern)
	}
	a.declare(method, pattern, string(perm))
	r.With(a.authn, a.Require(perm)).MethodFunc(method, pattern, h)
}

// HandleAuthenticated registers a route that needs only a session and acts on
// the caller's own data (never on another principal's workspace data).
func (a *Authorizer) HandleAuthenticated(r chi.Router, method, pattern string, h http.HandlerFunc) {
	a.declare(method, pattern, "authenticated")
	r.With(a.authn).MethodFunc(method, pattern, h)
}

func (a *Authorizer) declare(method, pattern, perm string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.routes[method+" "+pattern] = perm
}

// Declared returns the permission declared for a route, if any.
func (a *Authorizer) Declared(method, pattern string) (string, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	p, ok := a.routes[method+" "+pattern]
	return p, ok
}

// UndeclaredRoutes walks the router and returns every /api/v1 route that was
// not registered through Handle/HandleAuthenticated and is not allowlisted
// (entries are "METHOD pattern"). Used by a test to guard against handlers
// without an authorization decision.
func (a *Authorizer) UndeclaredRoutes(root chi.Routes, allow ...string) ([]string, error) {
	ok := map[string]bool{}
	for _, s := range allow {
		ok[s] = true
	}
	var missing []string
	err := chi.Walk(root, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if !strings.HasPrefix(route, "/api/v1/") {
			return nil
		}
		key := method + " " + route
		if _, declared := a.Declared(method, route); !declared && !ok[key] {
			missing = append(missing, key)
		}
		return nil
	})
	sort.Strings(missing)
	return missing, err
}

func containsParam(p string) bool { return strings.Contains(p, "{"+WorkspaceParam+"}") }

func (a *Authorizer) fail(w http.ResponseWriter, what string, err error) {
	a.log.Error("policy: "+what, "error", err)
	WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
}

func notFound(w http.ResponseWriter) {
	WriteJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
}

// WriteJSON writes a JSON response with no-store caching.
func WriteJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func (a *Authorizer) auditDenied(ctx context.Context, p auth.Principal, d Decision, perm Permission, method string) {
	details := map[string]any{"permission": string(perm), "role": d.Membership.Role, "scoped": d.Scoped()}
	if method != "" {
		details["method"] = method
	}
	b, _ := json.Marshal(details)
	actx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	_, err := a.store.InsertAudit(actx, dbgen.InsertAuditParams{
		ActorID: pgtype.UUID{Bytes: p.User.ID, Valid: true}, ActorLabel: p.User.DisplayName,
		Action: "authz.denied", TargetType: "workspace", TargetID: d.Workspace.ID.String(),
		WorkspaceID: pgtype.UUID{Bytes: d.Workspace.ID, Valid: true}, Details: b,
	})
	if err != nil {
		a.log.Error("audit write failed", "action", "authz.denied", "error", err)
	}
}
