package auth

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/BxnnyG/ansible-control-plane/internal/db"
	"github.com/BxnnyG/ansible-control-plane/internal/db/dbgen"
)

// TestFlowAgainstPostgres runs login, /me and logout against the real schema.
// It needs ACP_TEST_DATABASE_URL (a throwaway database) and is skipped otherwise.
func TestFlowAgainstPostgres(t *testing.T) {
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
	idp := newFakeIdP(t)
	svc, err := New(Options{Issuer: idp.srv.URL, ClientID: "client-1", RolesClaim: "x", PublicURL: publicURL,
		IdleTimeout: time.Hour, AbsoluteTimeout: 2 * time.Hour, StateKey: []byte("0123456789abcdef"),
		Store: dbgen.New(pool), Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	r := chi.NewRouter()
	svc.Routes(r)
	do := func(method, path string, hdr map[string]string, cs ...*http.Cookie) *httptest.ResponseRecorder {
		req := httptest.NewRequestWithContext(ctx, method, path, nil)
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		for _, c := range cs {
			req.AddCookie(c)
		}
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		return rec
	}
	lr := do("GET", "/api/auth/oidc/login", nil)
	loc, _ := url.Parse(lr.Header().Get("Location"))
	q := loc.Query()
	code := idp.authorize(q.Get("code_challenge"), q.Get("nonce"))
	cb := do("GET", callbackPath+"?code="+code+"&state="+url.QueryEscape(q.Get("state")), nil, cookieNamed(lr, "__Host-acp_oidc_flow"))
	sc, cc := cookieNamed(cb, "__Host-acp_session"), cookieNamed(cb, "__Host-acp_csrf")
	if cb.Code != http.StatusSeeOther || sc == nil {
		t.Fatalf("callback %d %s", cb.Code, cb.Header().Get("Location"))
	}
	if c := do("GET", "/api/v1/me", nil, sc).Code; c != 200 {
		t.Fatalf("me %d", c)
	}
	if c := do("POST", "/api/auth/logout", map[string]string{"X-CSRF-Token": cc.Value}, sc).Code; c != 200 {
		t.Fatalf("logout %d", c)
	}
	if c := do("GET", "/api/v1/me", nil, sc).Code; c != 401 {
		t.Fatalf("me after logout %d", c)
	}
	var n int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM audit_log WHERE action IN ('auth.login','auth.logout')").Scan(&n); err != nil || n < 2 {
		t.Errorf("audit rows %d err %v", n, err)
	}
}
