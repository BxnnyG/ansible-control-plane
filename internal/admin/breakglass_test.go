package admin

import (
	"bytes"
	"context"
	"encoding/base32"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/BxnnyG/ansible-control-plane/internal/auth"
	"github.com/BxnnyG/ansible-control-plane/internal/db"
	"github.com/BxnnyG/ansible-control-plane/internal/db/dbgen"
	"github.com/BxnnyG/ansible-control-plane/internal/secrets"
)

// TestBreakGlassBootstrapAndLogin covers the CLI logic against a real schema and
// logs in through the HTTP handler. Needs ACP_TEST_DATABASE_URL (throwaway DB).
func TestBreakGlassBootstrapAndLogin(t *testing.T) {
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
	sp, err := secrets.NewLocalProvider([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	name := "bg-" + uuid.NewString()[:8]
	const pw = "a long enough password"

	if _, err := CreateBreakGlass(ctx, pool, sp, name, "short"); !errors.Is(err, auth.ErrWeakPassword) {
		t.Fatalf("weak password: %v", err)
	}
	enr, err := CreateBreakGlass(ctx, pool, sp, name, pw)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(enr.URI, "otpauth://totp/ACP:"+name) || enr.Secret == "" {
		t.Fatalf("bad enrollment %q", enr.URI)
	}
	if _, err := CreateBreakGlass(ctx, pool, sp, name, pw); !errors.Is(err, ErrUserExists) {
		t.Fatalf("duplicate: %v", err)
	}

	q := dbgen.New(pool)
	u, err := q.GetBreakGlassUser(ctx, pgtype.Text{String: name, Valid: true})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(u.TotpEnvelope, []byte(enr.Secret)) || strings.Contains(u.PasswordHash.String, pw) {
		t.Fatal("plaintext secret stored")
	}
	ws, err := q.GetWorkspaceBySlug(ctx, DefaultWorkspaceSlug)
	if err != nil {
		t.Fatal(err)
	}
	m, err := q.GetMembership(ctx, dbgen.GetMembershipParams{WorkspaceID: ws.ID, UserID: u.ID})
	if err != nil || m.Role != "owner" {
		t.Fatalf("membership %v %v", m.Role, err)
	}
	// A second break-glass user reuses the default workspace.
	if _, err := CreateBreakGlass(ctx, pool, sp, name+"-2", pw); err != nil {
		t.Fatalf("second user: %v", err)
	}

	svc, err := auth.New(auth.Options{PublicURL: "https://acp.example.com", IdleTimeout: time.Hour, AbsoluteTimeout: 2 * time.Hour,
		StateKey: []byte("0123456789abcdef"), Store: q, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		BreakGlass: true, Secrets: sp})
	if err != nil {
		t.Fatal(err)
	}
	r := chi.NewRouter()
	svc.Routes(r)
	login := func(code string) *httptest.ResponseRecorder {
		b, _ := json.Marshal(map[string]string{"username": name, "password": pw, "totp": code})
		req := httptest.NewRequestWithContext(ctx, "POST", "/api/auth/breakglass/login", bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		return rec
	}
	secret := decodeSecret(t, enr.Secret)
	step := auth.TOTPStep(time.Now())
	code := auth.TOTPCode(secret, step)
	if rec := login(code); rec.Code != http.StatusOK {
		t.Fatalf("login: %d %s", rec.Code, rec.Body)
	}
	if rec := login(code); rec.Code != http.StatusUnauthorized {
		t.Fatalf("replay against real DB accepted: %d", rec.Code)
	}

	// Reset replaces the secret: the old one no longer works, the new one does.
	enr2, err := ResetBreakGlassTOTP(ctx, pool, sp, name)
	if err != nil {
		t.Fatal(err)
	}
	if enr2.Secret == enr.Secret {
		t.Fatal("secret not rotated")
	}
	if rec := login(auth.TOTPCode(secret, auth.TOTPStep(time.Now())+1)); rec.Code != http.StatusUnauthorized {
		t.Fatalf("old secret still valid: %d", rec.Code)
	}
	if rec := login(auth.TOTPCode(decodeSecret(t, enr2.Secret), auth.TOTPStep(time.Now()))); rec.Code != http.StatusOK {
		t.Fatalf("new secret rejected: %d", rec.Code)
	}
	if _, err := ResetBreakGlassTOTP(ctx, pool, sp, "does-not-exist"); !errors.Is(err, ErrNoSuchUser) {
		t.Fatalf("reset unknown: %v", err)
	}

	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE action IN
		('admin.breakglass_created','admin.breakglass_totp_reset','auth.breakglass_login','auth.breakglass_failed')`).Scan(&n); err != nil || n < 5 {
		t.Errorf("audit rows %d err %v", n, err)
	}
}

func decodeSecret(t *testing.T, s string) []byte {
	t.Helper()
	b, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
