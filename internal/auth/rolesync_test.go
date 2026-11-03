package auth

import (
	"context"
	"io"
	"log/slog"
	"os"
	"testing"

	"github.com/google/uuid"

	"github.com/BxnnyG/ansible-control-plane/internal/db"
	"github.com/BxnnyG/ansible-control-plane/internal/db/dbgen"
)

func TestMapIdPRoles(t *testing.T) {
	tests := []struct {
		name  string
		roles []string
		want  string
	}{
		{"none", nil, ""},
		{"unknown only", []string{"foo", "acp-owner", "admin"}, ""},
		{"viewer", []string{"acp-viewer"}, "viewer"},
		{"highest wins", []string{"acp-viewer", "acp-admin", "acp-operator"}, "admin"},
		{"editor over operator", []string{"acp-operator", "acp-editor"}, "editor"},
		{"unknown ignored", []string{"x", "acp-operator"}, "operator"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := MapIdPRoles(tc.roles); got != tc.want {
				t.Errorf("got %q want %q", got, tc.want)
			}
		})
	}
}

func TestExtractRolesShapes(t *testing.T) {
	zitadel := map[string]any{"c": map[string]any{"acp-viewer": map[string]any{"org-id": "d"}, "acp-admin": map[string]any{}}}
	if got := MapIdPRoles(extractRoles(zitadel, "c")); got != "admin" {
		t.Errorf("object shape: %q", got)
	}
	arr := map[string]any{"c": []any{"acp-operator", 5}}
	if got := MapIdPRoles(extractRoles(arr, "c")); got != "operator" {
		t.Errorf("array shape: %q", got)
	}
	if got := extractRoles(map[string]any{"c": "acp-admin"}, "c"); got != nil {
		t.Errorf("string shape yields %v", got)
	}
}

// TestRoleSyncAgainstPostgres needs ACP_TEST_DATABASE_URL (throwaway database).
func TestRoleSyncAgainstPostgres(t *testing.T) {
	dsn := os.Getenv("ACP_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("ACP_TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := db.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := db.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	q := dbgen.New(pool)
	slug := "sync-" + uuid.NewString()[:8]
	ws, err := q.CreateWorkspace(ctx, dbgen.CreateWorkspaceParams{Slug: slug, Name: "Sync test"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = q.DeleteWorkspace(ctx, ws.ID) })
	newUser := func(name string) dbgen.User {
		u, err := q.UpsertOIDCUser(ctx, dbgen.UpsertOIDCUserParams{
			OidcIssuer: textVal("https://idp.example.test"), OidcSubject: textVal(uuid.NewString()),
			Email: name + "@example.test", DisplayName: name})
		if err != nil {
			t.Fatal(err)
		}
		return u
	}
	sy := &IdPRoleSyncer{store: q, slug: slug, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	role := func(u dbgen.User) (dbgen.Membership, bool) {
		m, err := q.GetMembership(ctx, dbgen.GetMembershipParams{WorkspaceID: ws.ID, UserID: u.ID})
		return m, err == nil
	}
	sync := func(u dbgen.User, roles ...string) {
		t.Helper()
		if err := sy.SyncRoles(ctx, u, roles); err != nil {
			t.Fatal(err)
		}
	}

	u := newUser("idp-user")
	sync(u) // no roles, no membership: nothing happens
	if _, ok := role(u); ok {
		t.Fatal("membership created without roles")
	}
	sync(u, "acp-viewer", "acp-editor")
	if m, ok := role(u); !ok || m.Role != "editor" || m.Source != "idp" {
		t.Fatalf("create: %+v %v", m, ok)
	}
	sync(u, "acp-editor") // unchanged: no extra audit row
	sync(u, "acp-admin")
	if m, _ := role(u); m.Role != "admin" {
		t.Fatalf("update: %+v", m)
	}
	sync(u, "unrelated")
	if _, ok := role(u); ok {
		t.Fatal("idp membership must be removed when roles vanish")
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE workspace_id = $1 AND action = 'membership.idp_sync'`, ws.ID).Scan(&n); err != nil || n != 3 {
		t.Errorf("audit rows %d err %v, want 3 (create, update, remove)", n, err)
	}

	// Local memberships (owner included) are never changed or removed.
	owner := newUser("local-owner")
	lm, err := q.CreateMembership(ctx, dbgen.CreateMembershipParams{WorkspaceID: ws.ID, UserID: owner.ID, Role: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	sync(owner, "acp-viewer")
	sync(owner)
	if m, ok := role(owner); !ok || m.Role != "owner" || m.ID != lm.ID || m.Source != "local" {
		t.Fatalf("local membership touched: %+v", m)
	}

	// A missing default workspace is not an error.
	sy2 := &IdPRoleSyncer{store: q, slug: "does-not-exist", log: sy.log}
	if err := sy2.SyncRoles(ctx, u, []string{"acp-admin"}); err != nil {
		t.Errorf("missing workspace: %v", err)
	}
}
