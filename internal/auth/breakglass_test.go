package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/BxnnyG/ansible-control-plane/internal/db/dbgen"
	"github.com/BxnnyG/ansible-control-plane/internal/secrets"
)

const (
	bgUser = "admin"
	bgPass = "correct horse battery staple"
)

type bgEnv struct {
	store  *memStore
	svc    *Service
	h      http.Handler
	clock  *atomic.Int64
	secret []byte
	user   dbgen.User
}

func (e *bgEnv) now() time.Time { return time.Unix(0, e.clock.Load()) }

func newBGEnv(t *testing.T, mod func(*Options)) *bgEnv {
	t.Helper()
	key := []byte("0123456789abcdef0123456789abcdef")
	sp, err := secrets.NewLocalProvider(key)
	if err != nil {
		t.Fatal(err)
	}
	e := &bgEnv{clock: &atomic.Int64{}}
	e.clock.Store(time.Now().UnixNano())
	e.store = newMemStore(e.now)
	hash, err := HashPassword(context.Background(), bgPass)
	if err != nil {
		t.Fatal(err)
	}
	e.secret, _ = GenerateTOTPSecret()
	id := uuid.New()
	env, err := SealTOTPSecret(sp, id.String(), e.secret)
	if err != nil {
		t.Fatal(err)
	}
	e.user = dbgen.User{ID: id, IsBreakGlass: true, Username: textVal(bgUser), DisplayName: bgUser,
		PasswordHash: textVal(hash), TotpEnvelope: env}
	e.store.addBreakGlass(e.user)
	o := Options{PublicURL: publicURL, IdleTimeout: time.Hour, AbsoluteTimeout: 4 * time.Hour,
		StateKey: []byte("0123456789abcdef0123456789abcdef"), Store: e.store, Now: e.now,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)), BreakGlass: true, Secrets: sp}
	if mod != nil {
		mod(&o)
	}
	e.svc, err = New(o)
	if err != nil {
		t.Fatal(err)
	}
	r := chi.NewRouter()
	e.svc.Routes(r)
	e.h = r
	return e
}

func (e *bgEnv) code(offset int64) string { return TOTPCode(e.secret, TOTPStep(e.now())+offset) }

func (e *bgEnv) login(user, pass, code string, hdr map[string]string) *httptest.ResponseRecorder {
	b, _ := json.Marshal(map[string]string{"username": user, "password": pass, "totp": code})
	req := httptest.NewRequestWithContext(context.Background(), "POST", "/api/auth/breakglass/login", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "192.0.2.10:4711"
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	return rec
}

func TestBreakGlassLoginWithoutOIDC(t *testing.T) {
	e := newBGEnv(t, nil)
	rec := e.login(bgUser, bgPass, e.code(0), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("login: %d %s", rec.Code, rec.Body)
	}
	sc, cc := cookieNamed(rec, "__Host-acp_session"), cookieNamed(rec, "__Host-acp_csrf")
	if sc == nil || cc == nil || !sc.HttpOnly || !sc.Secure {
		t.Fatalf("session cookies missing or weak: %+v %+v", sc, cc)
	}
	req := httptest.NewRequestWithContext(context.Background(), "GET", "/api/v1/me", nil)
	req.AddCookie(sc)
	me := httptest.NewRecorder()
	e.h.ServeHTTP(me, req)
	if me.Code != http.StatusOK {
		t.Fatalf("me: %d", me.Code)
	}
	if got := e.store.actions(); len(got) != 1 || got[0] != "auth.breakglass_login" {
		t.Errorf("audit actions %v", got)
	}
	// OIDC routes must not exist when OIDC is not configured.
	if c := httptest.NewRecorder(); true {
		e.h.ServeHTTP(c, httptest.NewRequestWithContext(context.Background(), "GET", "/api/auth/oidc/login", nil))
		if c.Code != http.StatusNotFound && c.Code != http.StatusMethodNotAllowed {
			t.Errorf("oidc login route status %d", c.Code)
		}
	}
	// Logout works without an IdP.
	lo := httptest.NewRequestWithContext(context.Background(), "POST", "/api/auth/logout", nil)
	lo.AddCookie(sc)
	lo.Header.Set("X-CSRF-Token", cc.Value)
	lr := httptest.NewRecorder()
	e.h.ServeHTTP(lr, lo)
	if lr.Code != http.StatusOK || !strings.Contains(lr.Body.String(), "/login") {
		t.Errorf("logout: %d %s", lr.Code, lr.Body)
	}
}

func TestBreakGlassFailuresAreGeneric(t *testing.T) {
	e := newBGEnv(t, nil)
	cases := map[string]*httptest.ResponseRecorder{
		"unknown user": e.login("nobody", bgPass, e.code(0), nil),
		"bad password": e.login(bgUser, "wrong password!!", e.code(0), nil),
		"bad totp":     e.login(bgUser, bgPass, "000000", nil),
		"old totp":     e.login(bgUser, bgPass, e.code(-2), nil),
	}
	var first string
	for name, rec := range cases {
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s: status %d", name, rec.Code)
		}
		if cookieNamed(rec, "__Host-acp_session") != nil {
			t.Errorf("%s: session issued", name)
		}
		if first == "" {
			first = rec.Body.String()
		} else if rec.Body.String() != first {
			t.Errorf("%s: body differs, leaks failure reason: %s", name, rec.Body)
		}
	}
	for _, a := range e.store.audit {
		if a.Action != "auth.breakglass_failed" {
			t.Errorf("unexpected audit action %s", a.Action)
		}
		for _, secret := range []string{bgPass, "wrong password"} {
			if secret != "" && bytes.Contains(a.Details, []byte(secret)) {
				t.Errorf("audit details leak input: %s", a.Details)
			}
		}
	}
	if len(e.store.audit) != len(cases) {
		t.Errorf("audit rows %d", len(e.store.audit))
	}
}

func TestBreakGlassTOTPReplayRejected(t *testing.T) {
	e := newBGEnv(t, nil)
	code := e.code(0)
	if rec := e.login(bgUser, bgPass, code, nil); rec.Code != http.StatusOK {
		t.Fatalf("first login %d", rec.Code)
	}
	if rec := e.login(bgUser, bgPass, code, nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("replayed code accepted: %d", rec.Code)
	}
	// An older step than the accepted one is also a replay.
	if rec := e.login(bgUser, bgPass, e.code(-1), nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("older step accepted: %d", rec.Code)
	}
	// The next step is fine.
	if rec := e.login(bgUser, bgPass, e.code(1), nil); rec.Code != http.StatusOK {
		t.Fatalf("next step rejected: %d", rec.Code)
	}
}

func TestBreakGlassUserLockout(t *testing.T) {
	e := newBGEnv(t, func(o *Options) { o.MaxFailures = 3; o.LockDuration = 10 * time.Minute; o.IPMaxFailures = 1000 })
	for i := 0; i < 3; i++ {
		if rec := e.login(bgUser, "wrong password!!", e.code(0), nil); rec.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: %d", i, rec.Code)
		}
	}
	// Locked: even correct credentials are refused, with the same generic answer.
	rec := e.login(bgUser, bgPass, e.code(0), nil)
	if rec.Code != http.StatusUnauthorized || cookieNamed(rec, "__Host-acp_session") != nil {
		t.Fatalf("locked account logged in: %d", rec.Code)
	}
	e.clock.Add(int64(11 * time.Minute))
	if rec := e.login(bgUser, bgPass, e.code(0), nil); rec.Code != http.StatusOK {
		t.Fatalf("login after lock expiry: %d", rec.Code)
	}
}

func TestBreakGlassSuccessResetsFailureCounter(t *testing.T) {
	e := newBGEnv(t, func(o *Options) { o.MaxFailures = 3; o.IPMaxFailures = 1000 })
	for i := 0; i < 2; i++ {
		e.login(bgUser, "wrong password!!", e.code(0), nil)
	}
	if rec := e.login(bgUser, bgPass, e.code(0), nil); rec.Code != http.StatusOK {
		t.Fatalf("login: %d", rec.Code)
	}
	for i := 0; i < 2; i++ {
		e.login(bgUser, "wrong password!!", e.code(1), nil)
	}
	if rec := e.login(bgUser, bgPass, e.code(1), nil); rec.Code != http.StatusOK {
		t.Fatalf("counter was not reset by the successful login: %d", rec.Code)
	}
}

func TestBreakGlassIPRateLimit(t *testing.T) {
	e := newBGEnv(t, func(o *Options) { o.IPMaxFailures = 3; o.IPWindow = time.Minute })
	for i := 0; i < 3; i++ {
		e.login("nobody", "whatever whatever", "000000", nil)
	}
	rec := e.login(bgUser, bgPass, e.code(0), nil)
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("expected 429 with Retry-After, got %d", rec.Code)
	}
	e.clock.Add(int64(2 * time.Minute))
	if rec := e.login(bgUser, bgPass, e.code(0), nil); rec.Code != http.StatusOK {
		t.Fatalf("login after window: %d", rec.Code)
	}
}

func TestBreakGlassRequestHardening(t *testing.T) {
	e := newBGEnv(t, nil)
	if rec := e.login(bgUser, bgPass, e.code(0), map[string]string{"Content-Type": "text/plain"}); rec.Code != http.StatusForbidden {
		t.Errorf("non-JSON content type: %d", rec.Code)
	}
	if rec := e.login(bgUser, bgPass, e.code(0), map[string]string{"Origin": "https://evil.example.net"}); rec.Code != http.StatusForbidden {
		t.Errorf("foreign origin: %d", rec.Code)
	}
	if rec := e.login(bgUser, bgPass, e.code(0), map[string]string{"Origin": publicURL}); rec.Code != http.StatusOK {
		t.Errorf("same origin: %d", rec.Code)
	}
	req := httptest.NewRequestWithContext(context.Background(), "POST", "/api/auth/breakglass/login", strings.NewReader("{not json"))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("malformed body: %d", rec.Code)
	}
}

func TestBreakGlassRefusesUnusableAccounts(t *testing.T) {
	e := newBGEnv(t, nil)
	u := e.user
	u.Disabled = true
	e.store.addBreakGlass(u)
	if rec := e.login(bgUser, bgPass, e.code(0), nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("disabled user: %d", rec.Code)
	}
	u.Disabled, u.TotpEnvelope = false, nil
	e.store.addBreakGlass(u)
	if rec := e.login(bgUser, bgPass, e.code(0), nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("user without TOTP enrolled: %d", rec.Code)
	}
}

func TestBreakGlassTOTPEnvelopeBoundToUser(t *testing.T) {
	e := newBGEnv(t, nil)
	// A sealed secret copied to another user row must not decrypt (AAD binding).
	other := e.user
	other.ID = uuid.New()
	other.Username = pgtype.Text{String: "other", Valid: true}
	e.store.addBreakGlass(other)
	if rec := e.login("other", bgPass, e.code(0), nil); rec.Code == http.StatusOK {
		t.Fatal("envelope swap accepted")
	}
}

func TestNewRequiresALoginMethod(t *testing.T) {
	base := Options{PublicURL: publicURL, IdleTimeout: time.Hour, AbsoluteTimeout: time.Hour,
		StateKey: []byte("0123456789abcdef"), Store: newMemStore(time.Now), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if _, err := New(base); err == nil {
		t.Error("no login method must be rejected")
	}
	b := base
	b.BreakGlass = true
	if _, err := New(b); err == nil {
		t.Error("break-glass without secrets provider must be rejected")
	}
}
