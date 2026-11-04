package db

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/BxnnyG/ansible-control-plane/internal/db/dbgen"
)

// testPool connects to ACP_TEST_DATABASE_URL (a throwaway database) or skips.
func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("ACP_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("ACP_TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func TestMigrateUpDownUp(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	goose.SetBaseFS(migrations)
	if err := goose.SetDialect("postgres"); err != nil {
		t.Fatal(err)
	}
	sqlDB := stdlib.OpenDBFromPool(pool)
	defer func() { _ = sqlDB.Close() }()
	if err := goose.DownToContext(ctx, sqlDB, "migrations", 0); err != nil {
		t.Fatalf("down: %v", err)
	}
	if err := Migrate(ctx, pool); err != nil {
		t.Fatalf("up: %v", err)
	}
}

func TestAuthSchema(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	q := dbgen.New(pool)
	sfx := uuid.NewString()

	u, err := q.UpsertOIDCUser(ctx, dbgen.UpsertOIDCUserParams{
		OidcIssuer:  pgtype.Text{String: "https://idp.example.test", Valid: true},
		OidcSubject: pgtype.Text{String: "sub-" + sfx, Valid: true},
		Email:       "a@example.test", DisplayName: "A",
	})
	if err != nil {
		t.Fatal(err)
	}
	u2, err := q.UpsertOIDCUser(ctx, dbgen.UpsertOIDCUserParams{
		OidcIssuer: u.OidcIssuer, OidcSubject: u.OidcSubject, Email: "b@example.test", DisplayName: "A",
	})
	if err != nil || u2.ID != u.ID || u2.Email != "b@example.test" {
		t.Fatalf("upsert should be idempotent: %v", err)
	}

	// break-glass user requires username+hash; OIDC identity must be empty.
	if _, err := q.CreateBreakGlassUser(ctx, dbgen.CreateBreakGlassUserParams{
		ID:       uuid.New(),
		Username: pgtype.Text{String: "bg-" + sfx, Valid: true}, PasswordHash: pgtype.Text{String: "$argon2id$test", Valid: true},
		TotpEnvelope: []byte(`{"version":1}`),
	}); err != nil {
		t.Fatalf("break-glass: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO users (is_break_glass, username) VALUES (true, 'x-`+sfx+`')`); err == nil {
		t.Fatal("break-glass without password hash must be rejected")
	}

	ws, err := q.CreateWorkspace(ctx, dbgen.CreateWorkspaceParams{Slug: "ws-" + sfx[:8], Name: "WS"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO workspaces (slug, name) VALUES ('Bad Slug', 'x')`); err == nil {
		t.Fatal("invalid slug must be rejected")
	}
	m, err := q.CreateMembership(ctx, dbgen.CreateMembershipParams{WorkspaceID: ws.ID, UserID: u.ID, Role: "operator"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := q.CreateMembership(ctx, dbgen.CreateMembershipParams{WorkspaceID: ws.ID, UserID: u.ID, Role: "viewer"}); err == nil {
		t.Fatal("duplicate membership must be rejected")
	}
	if _, err := q.CreateMembership(ctx, dbgen.CreateMembershipParams{WorkspaceID: ws.ID, UserID: uuid.New(), Role: "root"}); err == nil {
		t.Fatal("unknown role must be rejected")
	}
	for i := 0; i < 2; i++ { // second add is a no-op
		if _, err := q.AddMembershipScope(ctx, dbgen.AddMembershipScopeParams{MembershipID: m.ID, Kind: "network", RefID: "servers"}); err != nil {
			t.Fatal(err)
		}
	}
	if sc, err := q.ListMembershipScopes(ctx, m.ID); err != nil || len(sc) != 1 {
		t.Fatalf("scopes: %v len=%d", err, len(sc))
	}

	// sessions
	now := time.Now()
	s, err := q.CreateSession(ctx, dbgen.CreateSessionParams{
		TokenHash: []byte("hash-" + sfx), CsrfHash: []byte("csrf"), UserID: u.ID,
		IdleExpiresAt: pgtype.Timestamptz{Time: now.Add(time.Hour), Valid: true},
		ExpiresAt:     pgtype.Timestamptz{Time: now.Add(8 * time.Hour), Valid: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := q.GetActiveSession(ctx, s.TokenHash); err != nil {
		t.Fatalf("active session: %v", err)
	}
	if err := q.TouchSession(ctx, dbgen.TouchSessionParams{ID: s.ID, Column2: pgtype.Timestamptz{Time: now.Add(24 * time.Hour), Valid: true}}); err != nil {
		t.Fatal(err)
	}
	if got, _ := q.GetActiveSession(ctx, s.TokenHash); !got.IdleExpiresAt.Time.Equal(s.ExpiresAt.Time) {
		t.Fatal("idle expiry must be capped at absolute expiry")
	}
	if _, err := pool.Exec(ctx, `UPDATE sessions SET idle_expires_at = now() - interval '1 second' WHERE id = $1`, s.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := q.GetActiveSession(ctx, s.TokenHash); err == nil {
		t.Fatal("idle-expired session must not be returned")
	}
	if n, err := q.DeleteExpiredSessions(ctx); err != nil || n < 1 {
		t.Fatalf("cleanup: %v n=%d", err, n)
	}

	// secrets: opaque envelope columns, unique name per workspace
	sec := dbgen.CreateSecretParams{
		ID: uuid.New(), WorkspaceID: ws.ID, Name: "k", Type: "ssh_key", EnvVersion: 1, EnvKeyID: "k1",
		EnvWrapNonce: []byte{1}, EnvWrappedDek: []byte{2}, EnvNonce: []byte{3}, EnvCiphertext: []byte{4},
		Metadata: []byte("{}"),
	}
	if _, err := q.CreateSecret(ctx, sec); err != nil {
		t.Fatal(err)
	}
	sec.ID = uuid.New()
	if _, err := q.CreateSecret(ctx, sec); err == nil {
		t.Fatal("duplicate secret name must be rejected")
	}
	if _, err := q.GetSecret(ctx, dbgen.GetSecretParams{ID: sec.ID, WorkspaceID: ws.ID}); err == nil {
		t.Fatal("unexpected secret found")
	}

	// audit is append-only and survives workspace deletion
	if _, err := q.InsertAudit(ctx, dbgen.InsertAuditParams{
		ActorID: pgtype.UUID{Bytes: u.ID, Valid: true}, Action: "test.action",
		WorkspaceID: pgtype.UUID{Bytes: ws.ID, Valid: true}, Details: []byte(`{}`),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE audit_log SET action = 'x'`); err == nil {
		t.Fatal("audit update must be rejected")
	}
	if _, err := pool.Exec(ctx, `DELETE FROM audit_log`); err == nil {
		t.Fatal("audit delete must be rejected")
	}
	if _, err := pool.Exec(ctx, `TRUNCATE audit_log`); err == nil {
		t.Fatal("audit truncate must be rejected")
	}
	if err := q.DeleteWorkspace(ctx, ws.ID); err != nil {
		t.Fatal(err)
	}
	rows, err := q.ListAudit(ctx, dbgen.ListAuditParams{Limit: 10, WorkspaceID: pgtype.UUID{Bytes: ws.ID, Valid: true}})
	if err != nil || len(rows) != 1 {
		t.Fatalf("audit after workspace delete: %v len=%d", err, len(rows))
	}
}
