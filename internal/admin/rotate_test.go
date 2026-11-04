package admin

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BxnnyG/ansible-control-plane/internal/auth"
	"github.com/BxnnyG/ansible-control-plane/internal/db"
	"github.com/BxnnyG/ansible-control-plane/internal/db/dbgen"
	"github.com/BxnnyG/ansible-control-plane/internal/secrets"
)

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
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
	return pool
}

func TestRotateMasterKeyRejectsSameKey(t *testing.T) {
	k := []byte("0123456789abcdef0123456789abcdef")
	if _, err := RotateMasterKey(context.Background(), nil, k, append([]byte(nil), k...)); err == nil {
		t.Fatal("identical keys accepted")
	}
}

func TestRotateMasterKey(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	oldKey := []byte("0123456789abcdef0123456789abcdef")
	newKey := []byte("fedcba9876543210fedcba9876543210")
	oldSP, _ := secrets.NewLocalProvider(oldKey)
	newOnly, _ := secrets.NewLocalProvider(newKey)

	q := dbgen.New(pool)
	ws, err := q.CreateWorkspace(ctx, dbgen.CreateWorkspaceParams{Slug: "rot-" + uuid.NewString()[:8], Name: "Rot"})
	if err != nil {
		t.Fatal(err)
	}
	sid := uuid.New()
	plain := []byte("test-value-not-a-real-secret")
	env, err := secrets.Seal(oldSP, plain, secrets.AAD{SecretID: sid.String(), WorkspaceID: ws.ID.String()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := q.CreateSecret(ctx, dbgen.CreateSecretParams{ID: sid, WorkspaceID: ws.ID, Name: "s", Type: "file", Metadata: []byte("{}"),
		EnvVersion: int32(env.Version), EnvKeyID: env.KeyID, EnvWrapNonce: env.WrapNonce, //nolint:gosec // small constant
		EnvWrappedDek: env.WrappedDEK, EnvNonce: env.Nonce, EnvCiphertext: env.Ciphertext}); err != nil {
		t.Fatal(err)
	}
	name := "rot-" + uuid.NewString()[:8]
	if _, err := CreateBreakGlass(ctx, pool, oldSP, name, "a long enough password"); err != nil {
		t.Fatal(err)
	}

	c, err := RotateMasterKey(ctx, pool, oldKey, newKey)
	if err != nil {
		t.Fatal(err)
	}
	if c.Secrets < 1 || c.TOTP < 1 {
		t.Fatalf("counts %+v", c)
	}
	row, err := q.GetSecret(ctx, dbgen.GetSecretParams{ID: sid, WorkspaceID: ws.ID})
	if err != nil {
		t.Fatal(err)
	}
	got, err := secrets.Open(newOnly, &secrets.Envelope{Version: int(row.EnvVersion), KeyID: row.EnvKeyID, WrapNonce: row.EnvWrapNonce,
		WrappedDEK: row.EnvWrappedDek, Nonce: row.EnvNonce, Ciphertext: row.EnvCiphertext},
		secrets.AAD{SecretID: sid.String(), WorkspaceID: ws.ID.String()})
	if err != nil || string(got.Reveal()) != string(plain) {
		t.Fatalf("secret not readable with new key only: %v", err)
	}
	u, err := q.GetBreakGlassUser(ctx, pgtype.Text{String: name, Valid: true})
	if err != nil {
		t.Fatal(err)
	}
	v, err := auth.OpenTOTPSecret(newOnly, u.ID.String(), u.TotpEnvelope)
	if err != nil {
		t.Fatalf("totp not readable with new key only: %v", err)
	}
	v.Zero()

	// A second run finds everything current and changes nothing.
	c2, err := RotateMasterKey(ctx, pool, oldKey, newKey)
	if err != nil || c2.Secrets != 0 || c2.TOTP != 0 || c2.AlreadyDone < 2 {
		t.Fatalf("second run %+v %v", c2, err)
	}
	// A wrong current key aborts without partial writes.
	if _, err := RotateMasterKey(ctx, pool, []byte("zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz"), []byte("yyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyy")); err == nil {
		t.Fatal("rotation with unrelated key succeeded")
	}
	if _, err := auth.OpenTOTPSecret(newOnly, u.ID.String(), u.TotpEnvelope); err != nil {
		t.Fatalf("data changed after failed rotation: %v", err)
	}
}

func TestSetBreakGlassPassword(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	sp, _ := secrets.NewLocalProvider([]byte("0123456789abcdef0123456789abcdef"))
	name := "pw-" + uuid.NewString()[:8]
	if _, err := CreateBreakGlass(ctx, pool, sp, name, "the first long password"); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "UPDATE users SET failed_logins = 3, locked_until = now() + interval '1 hour' WHERE username = $1", name); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name     string
		user     string
		password string
		wantErr  error
	}{
		{"unknown user", "nobody-" + name, "another long password", ErrNoSuchUser},
		{"weak password", name, "short", auth.ErrWeakPassword},
		{"ok", name, "the second long password", nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := SetBreakGlassPassword(ctx, pool, tc.user, tc.password)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
		})
	}
	u, err := dbgen.New(pool).GetBreakGlassUser(ctx, pgtype.Text{String: name, Valid: true})
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := auth.VerifyPassword(ctx, "the second long password", u.PasswordHash.String); err != nil || !ok {
		t.Fatalf("new password not set: %v", err)
	}
	if u.FailedLogins != 0 || u.LockedUntil.Valid {
		t.Fatalf("lockout not cleared: %d %v", u.FailedLogins, u.LockedUntil)
	}
}
