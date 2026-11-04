package server

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/BxnnyG/ansible-control-plane/internal/auth"
	"github.com/BxnnyG/ansible-control-plane/internal/db/dbgen"
	"github.com/BxnnyG/ansible-control-plane/internal/policy"
	"github.com/BxnnyG/ansible-control-plane/internal/secrets"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

// TestEveryAPIRouteDeclaresPermission fails when an /api/v1 route is registered
// without going through the policy helpers (ADR-0007).
func TestEveryAPIRouteDeclaresPermission(t *testing.T) {
	q := dbgen.New(nil) // never queried: only the route table is inspected
	svc, err := auth.New(auth.Options{
		Issuer: "https://idp.example.test", ClientID: "c", PublicURL: "https://acp.example.test",
		IdleTimeout: time.Hour, AbsoluteTimeout: time.Hour, StateKey: []byte("0123456789abcdef"),
		Store: q, Log: quiet,
	})
	if err != nil {
		t.Fatal(err)
	}
	az := policy.New(q, nil, svc.Require, quiet)
	prov, err := secrets.NewLocalProvider([]byte("0123456789abcdef0123456789ABCDEF"))
	if err != nil {
		t.Fatal(err)
	}
	h := Router(quiet, fakePing{}, DefaultStatic(), WithAuth(svc), WithPolicy(az, q), WithSecrets(secrets.NewService(q, prov)))
	root, ok := h.(chi.Routes)
	if !ok {
		t.Fatal("router does not expose its routes")
	}
	// /api/v1/me acts on the caller's own data and is registered by the auth package.
	missing, err := az.UndeclaredRoutes(root, "GET /api/v1/me")
	if err != nil {
		t.Fatal(err)
	}
	if len(missing) != 0 {
		t.Errorf("routes without a declared permission: %v", missing)
	}
}

type fakeAPIStore struct {
	ws   dbgen.Workspace
	user uuid.UUID
	role string
	// scopes are returned as the membership scopes.
	scopes []dbgen.MembershipScope
	// audits records audit actions; shared by value copies through the pointer.
	audits *[]string
	// auditCtxErrs records ctx.Err() seen by InsertAudit.
	auditCtxErrs *[]error
}

func (f fakeAPIStore) RenameWorkspace(_ context.Context, a dbgen.RenameWorkspaceParams) (dbgen.Workspace, error) {
	w := f.ws
	w.Name = a.Name
	return w, nil
}

func (f fakeAPIStore) CompleteWorkspaceSetup(context.Context, uuid.UUID) (dbgen.Workspace, error) {
	w := f.ws
	if !w.SetupCompletedAt.Valid {
		w.SetupCompletedAt = pgtype.Timestamptz{Time: time.Now(), Valid: true}
	}
	return w, nil
}

func (f fakeAPIStore) GetWorkspaceBySlug(_ context.Context, slug string) (dbgen.Workspace, error) {
	if slug != f.ws.Slug {
		return dbgen.Workspace{}, pgx.ErrNoRows
	}
	return f.ws, nil
}

func (f fakeAPIStore) GetMembership(_ context.Context, a dbgen.GetMembershipParams) (dbgen.Membership, error) {
	if a.UserID != f.user {
		return dbgen.Membership{}, pgx.ErrNoRows
	}
	return dbgen.Membership{ID: uuid.New(), WorkspaceID: f.ws.ID, UserID: f.user, Role: f.role}, nil
}

func (f fakeAPIStore) ListMembershipScopes(context.Context, uuid.UUID) ([]dbgen.MembershipScope, error) {
	return f.scopes, nil
}

func (f fakeAPIStore) InsertAudit(ctx context.Context, p dbgen.InsertAuditParams) (dbgen.InsertAuditRow, error) {
	if f.auditCtxErrs != nil {
		*f.auditCtxErrs = append(*f.auditCtxErrs, ctx.Err())
	}
	if f.audits != nil {
		*f.audits = append(*f.audits, p.Action)
	}
	return dbgen.InsertAuditRow{}, nil
}

func (f fakeAPIStore) ListMembershipSummariesForUser(_ context.Context, id uuid.UUID) ([]dbgen.ListMembershipSummariesForUserRow, error) {
	if id != f.user {
		return nil, nil
	}
	return []dbgen.ListMembershipSummariesForUserRow{{WorkspaceID: f.ws.ID, Slug: f.ws.Slug, Name: f.ws.Name, Role: f.role}}, nil
}

func TestWorkspaceEndpoints(t *testing.T) {
	uid := uuid.New()
	st := fakeAPIStore{ws: dbgen.Workspace{ID: uuid.New(), Slug: "default", Name: "Default"}, user: uid, role: policy.RoleViewer}
	authn := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id := uid
			if r.Header.Get("X-Other") != "" {
				id = uuid.New()
			}
			next.ServeHTTP(w, r.WithContext(auth.WithPrincipal(r.Context(), auth.Principal{User: dbgen.User{ID: id}})))
		})
	}
	h := Router(quiet, fakePing{}, DefaultStatic(), WithPolicy(policy.New(st, nil, authn, quiet), st))

	call := func(path string, hdr ...string) *httptest.ResponseRecorder {
		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, path, nil)
		for i := 0; i+1 < len(hdr); i += 2 {
			req.Header.Set(hdr[i], hdr[i+1])
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	rec := call("/api/v1/workspaces")
	var list []map[string]any
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &list) != nil || len(list) != 1 || list[0]["role"] != "viewer" || list[0]["scoped"] != false {
		t.Fatalf("list: %d %s", rec.Code, rec.Body.String())
	}
	if rec := call("/api/v1/workspaces", "X-Other", "1"); rec.Body.String() != "[]\n" {
		t.Errorf("non-member list: %s", rec.Body.String())
	}
	rec = call("/api/v1/workspaces/default")
	var ws struct {
		Role        string   `json:"role"`
		Permissions []string `json:"permissions"`
	}
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &ws) != nil || ws.Role != "viewer" || len(ws.Permissions) != 6 {
		t.Fatalf("get: %d %s", rec.Code, rec.Body.String())
	}
	if c := call("/api/v1/workspaces/default", "X-Other", "1").Code; c != 404 {
		t.Errorf("non-member: %d", c)
	}
	if c := call("/api/v1/workspaces/missing").Code; c != 404 {
		t.Errorf("unknown workspace: %d", c)
	}
}
