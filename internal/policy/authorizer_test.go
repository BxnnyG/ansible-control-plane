package policy

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/BxnnyG/ansible-control-plane/internal/auth"
	"github.com/BxnnyG/ansible-control-plane/internal/db/dbgen"
)

type fakeStore struct {
	mu     sync.Mutex
	ws     dbgen.Workspace
	member map[uuid.UUID]dbgen.Membership
	scopes []dbgen.MembershipScope
	audit  []dbgen.InsertAuditParams
}

func (f *fakeStore) GetWorkspaceBySlug(_ context.Context, slug string) (dbgen.Workspace, error) {
	if slug != f.ws.Slug {
		return dbgen.Workspace{}, pgx.ErrNoRows
	}
	return f.ws, nil
}

func (f *fakeStore) GetMembership(_ context.Context, a dbgen.GetMembershipParams) (dbgen.Membership, error) {
	m, ok := f.member[a.UserID]
	if !ok || a.WorkspaceID != f.ws.ID {
		return dbgen.Membership{}, pgx.ErrNoRows
	}
	return m, nil
}

func (f *fakeStore) ListMembershipScopes(_ context.Context, id uuid.UUID) ([]dbgen.MembershipScope, error) {
	var out []dbgen.MembershipScope
	for _, s := range f.scopes {
		if s.MembershipID == id {
			out = append(out, s)
		}
	}
	return out, nil
}

func (f *fakeStore) InsertAudit(_ context.Context, a dbgen.InsertAuditParams) (dbgen.InsertAuditRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.audit = append(f.audit, a)
	return dbgen.InsertAuditRow{}, nil
}

func newFixture(t *testing.T) (*Authorizer, *fakeStore, map[string]auth.Principal) {
	t.Helper()
	f := &fakeStore{ws: dbgen.Workspace{ID: uuid.New(), Slug: "default"}, member: map[uuid.UUID]dbgen.Membership{}}
	users := map[string]auth.Principal{}
	for _, role := range []string{RoleViewer, RoleOperator, RoleEditor, RoleAdmin, RoleOwner, "scoped-admin", "scoped-operator", "outsider"} {
		u := dbgen.User{ID: uuid.New(), DisplayName: role}
		users[role] = auth.Principal{User: u}
		if role == "outsider" {
			continue
		}
		r := role
		if role == "scoped-admin" {
			r = RoleAdmin
		}
		if role == "scoped-operator" {
			r = RoleOperator
		}
		m := dbgen.Membership{ID: uuid.New(), WorkspaceID: f.ws.ID, UserID: u.ID, Role: r}
		f.member[u.ID] = m
		if r != role {
			f.scopes = append(f.scopes, dbgen.MembershipScope{MembershipID: m.ID, Kind: KindNetwork, RefID: "net-1"})
		}
	}
	// authn stub: principal comes from the X-Test-User header.
	authn := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			pr, ok := users[r.Header.Get("X-Test-User")]
			if !ok {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r.WithContext(auth.WithPrincipal(r.Context(), pr)))
		})
	}
	return New(f, nil, authn, slog.New(slog.NewTextHandler(io.Discard, nil))), f, users
}

func TestMiddleware(t *testing.T) {
	a, f, _ := newFixture(t)
	r := chi.NewRouter()
	ok := func(w http.ResponseWriter, req *http.Request) {
		if _, found := DecisionFromContext(req.Context()); !found {
			t.Error("decision missing in context")
		}
		w.WriteHeader(http.StatusOK)
	}
	a.Handle(r, "GET", "/api/v1/workspaces/{workspace}/runs", RunRead, ok)
	a.Handle(r, "POST", "/api/v1/workspaces/{workspace}/runs", RunExecute, ok)
	a.Handle(r, "POST", "/api/v1/workspaces/{workspace}/secrets", SecretsWrite, ok)
	a.Handle(r, "GET", "/api/v1/workspaces/{workspace}/audit", AuditRead, ok)

	tests := []struct {
		name, user, method, path string
		want                     int
		audited                  bool
	}{
		{"viewer reads runs", RoleViewer, "GET", "/api/v1/workspaces/default/runs", 200, false},
		{"viewer cannot execute", RoleViewer, "POST", "/api/v1/workspaces/default/runs", 403, true},
		{"operator executes", RoleOperator, "POST", "/api/v1/workspaces/default/runs", 200, false},
		{"operator cannot write secrets", RoleOperator, "POST", "/api/v1/workspaces/default/secrets", 403, true},
		{"editor writes secrets", RoleEditor, "POST", "/api/v1/workspaces/default/secrets", 200, false},
		{"editor cannot read audit", RoleEditor, "GET", "/api/v1/workspaces/default/audit", 403, true},
		{"admin reads audit", RoleAdmin, "GET", "/api/v1/workspaces/default/audit", 200, false},
		{"scoped admin: workspace-wide denied", "scoped-admin", "GET", "/api/v1/workspaces/default/audit", 403, true},
		{"scoped admin: secrets write denied", "scoped-admin", "POST", "/api/v1/workspaces/default/secrets", 403, true},
		{"scoped operator executes (handler narrows)", "scoped-operator", "POST", "/api/v1/workspaces/default/runs", 200, false},
		{"non-member gets 404", "outsider", "GET", "/api/v1/workspaces/default/runs", 404, false},
		{"unknown workspace gets 404", RoleOwner, "GET", "/api/v1/workspaces/nope/runs", 404, false},
		{"unauthenticated", "", "GET", "/api/v1/workspaces/default/runs", 401, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			before := len(f.audit)
			req := httptest.NewRequestWithContext(context.Background(), tc.method, tc.path, nil)
			req.Header.Set("X-Test-User", tc.user)
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("status %d want %d", rec.Code, tc.want)
			}
			if got := len(f.audit) - before; (got == 1) != tc.audited || got > 1 {
				t.Errorf("audit rows %d, audited=%v", got, tc.audited)
			}
			if tc.audited && f.audit[len(f.audit)-1].Action != "authz.denied" {
				t.Errorf("action %s", f.audit[len(f.audit)-1].Action)
			}
		})
	}
}

func TestAuthorize(t *testing.T) {
	a, f, users := newFixture(t)
	net1 := Target{Refs: []Ref{{KindNetwork, "net-1"}}}
	mixed := Target{Refs: []Ref{{KindNetwork, "net-1"}, {KindNetwork, "net-2"}}}
	tests := []struct {
		name   string
		user   string
		perm   Permission
		target Target
		want   error
	}{
		{"unscoped operator any target", RoleOperator, RunExecute, mixed, nil},
		{"unscoped viewer cannot execute", RoleViewer, RunExecute, net1, ErrForbidden},
		{"scoped operator in scope", "scoped-operator", RunExecute, net1, nil},
		{"scoped operator partly out of scope", "scoped-operator", RunExecute, mixed, ErrForbidden},
		{"scoped operator empty target", "scoped-operator", RunExecute, Target{}, ErrForbidden},
		{"scoped admin workspace-wide", "scoped-admin", MembersManage, net1, ErrForbidden},
		{"outsider", "outsider", RunRead, net1, ErrNotMember},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := a.Authorize(context.Background(), users[tc.user], f.ws.ID, tc.perm, tc.target)
			if !errors.Is(err, tc.want) {
				t.Errorf("got %v want %v", err, tc.want)
			}
		})
	}
}

func TestRouteDeclarations(t *testing.T) {
	a, _, _ := newFixture(t)
	r := chi.NewRouter()
	a.Handle(r, "GET", "/api/v1/workspaces/{workspace}", WorkspaceRead, func(http.ResponseWriter, *http.Request) {})
	a.HandleAuthenticated(r, "GET", "/api/v1/workspaces", func(http.ResponseWriter, *http.Request) {})
	r.Get("/api/v1/me", func(http.ResponseWriter, *http.Request) {})
	r.Get("/healthz", func(http.ResponseWriter, *http.Request) {})
	r.Get("/api/v1/rogue", func(http.ResponseWriter, *http.Request) {})

	missing, err := a.UndeclaredRoutes(r, "GET /api/v1/me")
	if err != nil {
		t.Fatal(err)
	}
	if len(missing) != 1 || missing[0] != "GET /api/v1/rogue" {
		t.Errorf("undeclared routes: %v", missing)
	}
	defer func() {
		if recover() == nil {
			t.Error("expected panic for pattern without {workspace}")
		}
	}()
	a.Handle(r, "GET", "/api/v1/nope", RunRead, func(http.ResponseWriter, *http.Request) {})
}
