package auth

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/BxnnyG/ansible-control-plane/internal/db/dbgen"
)

const publicURL = "https://acp.example.com"

type env struct {
	idp   *fakeIdP
	store *memStore
	svc   *Service
	h     http.Handler
	clock *atomic.Int64
	roles *[][]string
}

type roleRec struct{ got *[][]string }

func (r roleRec) SyncRoles(_ context.Context, _ dbgen.User, roles []string) error {
	*r.got = append(*r.got, roles)
	return nil
}

func newEnv(t *testing.T) *env {
	t.Helper()
	idp := newFakeIdP(t)
	clock := &atomic.Int64{}
	clock.Store(time.Now().UnixNano())
	now := func() time.Time { return time.Unix(0, clock.Load()) }
	st := newMemStore(now)
	var got [][]string
	svc, err := New(Options{
		Issuer: idp.srv.URL, ClientID: "client-1", RolesClaim: "urn:zitadel:iam:org:project:roles",
		PublicURL: publicURL, IdleTimeout: time.Hour, AbsoluteTimeout: 4 * time.Hour,
		StateKey: []byte("0123456789abcdef0123456789abcdef"), Store: st,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)), RoleSync: roleRec{&got}, Now: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	r := chi.NewRouter()
	svc.Routes(r)
	r.With(svc.Require).Post("/api/v1/echo", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	return &env{idp: idp, store: st, svc: svc, h: r, clock: clock, roles: &got}
}

func (e *env) do(method, path string, hdr map[string]string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	req := httptest.NewRequestWithContext(context.Background(), method, path, nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	return rec
}

func cookieNamed(rec *httptest.ResponseRecorder, name string) *http.Cookie {
	for _, c := range rec.Result().Cookies() {
		if c.Name == name {
			return c
		}
	}
	return nil
}

// startLogin runs /login and returns the flow cookie and the IdP authorize URL.
func (e *env) startLogin(t *testing.T, returnTo string) (*http.Cookie, *url.URL) {
	t.Helper()
	rec := e.do("GET", "/api/auth/oidc/login?return_to="+url.QueryEscape(returnTo), nil)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("login status %d", rec.Code)
	}
	loc, err := url.Parse(rec.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	return cookieNamed(rec, "__Host-acp_oidc_flow"), loc
}

// login performs the full flow and returns the callback response.
func (e *env) login(t *testing.T, returnTo string) *httptest.ResponseRecorder {
	t.Helper()
	flow, loc := e.startLogin(t, returnTo)
	q := loc.Query()
	code := e.idp.authorize(q.Get("code_challenge"), q.Get("nonce"))
	return e.do("GET", callbackPath+"?code="+code+"&state="+url.QueryEscape(q.Get("state")), nil, flow)
}

func TestLoginRedirectParams(t *testing.T) {
	e := newEnv(t)
	flow, loc := e.startLogin(t, "/runs")
	q := loc.Query()
	if !strings.HasPrefix(loc.String(), e.idp.srv.URL+"/authorize") {
		t.Fatalf("unexpected target %s", loc)
	}
	for k, want := range map[string]string{"response_type": "code", "client_id": "client-1", "code_challenge_method": "S256",
		"redirect_uri": publicURL + callbackPath, "scope": "openid profile email"} {
		if q.Get(k) != want {
			t.Errorf("%s = %q, want %q", k, q.Get(k), want)
		}
	}
	if q.Get("state") == "" || q.Get("nonce") == "" || q.Get("code_challenge") == "" {
		t.Error("missing state/nonce/challenge")
	}
	if flow == nil || !flow.HttpOnly || !flow.Secure || flow.SameSite != http.SameSiteLaxMode || flow.Path != "/" {
		t.Errorf("flow cookie attributes: %+v", flow)
	}
	if strings.Contains(flow.Value, q.Get("state")) {
		t.Error("flow cookie must be sealed")
	}
}

func TestFullFlow(t *testing.T) {
	e := newEnv(t)
	rec := e.login(t, "/runs?x=1")
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != publicURL+"/runs?x=1" {
		t.Fatalf("callback: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	sc, cc := cookieNamed(rec, "__Host-acp_session"), cookieNamed(rec, "__Host-acp_csrf")
	if sc == nil || !sc.HttpOnly || !sc.Secure || sc.SameSite != http.SameSiteLaxMode || sc.Path != "/" || sc.Domain != "" {
		t.Fatalf("session cookie: %+v", sc)
	}
	if cc == nil || cc.HttpOnly || !cc.Secure {
		t.Fatalf("csrf cookie: %+v", cc)
	}
	// Only hashes are stored.
	for _, s := range e.store.sessions {
		if string(s.TokenHash) == sc.Value || len(s.TokenHash) != 32 || len(s.CsrfHash) != 32 {
			t.Error("session token must be stored hashed")
		}
	}
	if len(*e.roles) != 1 || len((*e.roles)[0]) != 1 || (*e.roles)[0][0] != "acp-admin" {
		t.Errorf("role sync: %v", *e.roles)
	}

	me := e.do("GET", "/api/v1/me", nil, sc)
	if me.Code != 200 || !strings.Contains(me.Body.String(), `"email":"a@example.com"`) {
		t.Fatalf("me: %d %s", me.Code, me.Body.String())
	}
	if strings.Contains(me.Body.String(), sc.Value) || strings.Contains(me.Body.String(), "at-secret") {
		t.Error("me leaks secrets")
	}
	// Second login reuses the user (JIT provisioning is idempotent).
	e.login(t, "/")
	if len(e.store.users) != 1 {
		t.Errorf("users = %d", len(e.store.users))
	}

	// Logout needs CSRF.
	if r := e.do("POST", "/api/auth/logout", nil, sc); r.Code != 403 {
		t.Errorf("logout without csrf = %d", r.Code)
	}
	out := e.do("POST", "/api/auth/logout", map[string]string{"X-CSRF-Token": cc.Value}, sc, cc)
	if out.Code != 200 {
		t.Fatalf("logout %d", out.Code)
	}
	if !strings.HasPrefix(out.Body.String(), `{"redirect":"`+e.idp.srv.URL+`/logout?`) ||
		!strings.Contains(out.Body.String(), "post_logout_redirect_uri=https%3A%2F%2Facp.example.com%2Flogin") {
		t.Errorf("logout body: %s", out.Body.String())
	}
	if r := e.do("GET", "/api/v1/me", nil, sc); r.Code != 401 {
		t.Errorf("session still valid after logout: %d", r.Code)
	}
	acts := strings.Join(e.store.actions(), ",")
	if !strings.Contains(acts, "auth.login") || !strings.Contains(acts, "auth.logout") {
		t.Errorf("audit actions: %s", acts)
	}
	for _, a := range e.store.audit {
		if strings.Contains(string(a.Details), "at-secret") || strings.Contains(string(a.Details), "eyJ") {
			t.Error("audit contains token material")
		}
	}
}

func TestCallbackFailures(t *testing.T) {
	tests := []struct {
		name string
		prep func(*env)
		mod  func(e *env, flow *http.Cookie, state, code string) (string, *http.Cookie)
		want string
	}{
		{"state mismatch", nil, func(_ *env, f *http.Cookie, _, code string) (string, *http.Cookie) {
			return callbackPath + "?code=" + code + "&state=wrong", f
		}, "login_failed"},
		{"no flow cookie", nil, func(_ *env, _ *http.Cookie, s, code string) (string, *http.Cookie) {
			return callbackPath + "?code=" + code + "&state=" + s, nil
		}, "login_failed"},
		{"tampered flow cookie", nil, func(_ *env, f *http.Cookie, s, code string) (string, *http.Cookie) {
			c := *f //nolint:gosec // test copy of a cookie
			c.Value = c.Value[:len(c.Value)-2] + "AA"
			return callbackPath + "?code=" + code + "&state=" + s, &c
		}, "login_failed"},
		{"idp error", nil, func(_ *env, f *http.Cookie, s, _ string) (string, *http.Cookie) {
			return callbackPath + "?error=access_denied&state=" + s, f
		}, "login_failed"},
		{"bad code", nil, func(_ *env, f *http.Cookie, s, _ string) (string, *http.Cookie) {
			return callbackPath + "?code=bogus&state=" + s, f
		}, "login_failed"},
		{"nonce mismatch", func(e *env) { e.idp.nonceOverride = "other" }, nil, "login_failed"},
		{"wrong audience", func(e *env) { e.idp.audience = "someone-else" }, nil, "login_failed"},
		{"expired token", func(e *env) { e.idp.expired = true }, nil, "login_failed"},
		{"bad signature", func(e *env) { e.idp.otherKey = newFakeIdP(t).key }, nil, "login_failed"},
		{"token endpoint fails", func(e *env) { e.idp.tokenFails = true }, nil, "login_failed"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			if tc.prep != nil {
				tc.prep(e)
			}
			flow, loc := e.startLogin(t, "/")
			q := loc.Query()
			code := e.idp.authorize(q.Get("code_challenge"), q.Get("nonce"))
			path := callbackPath + "?code=" + code + "&state=" + url.QueryEscape(q.Get("state"))
			if tc.mod != nil {
				path, flow = tc.mod(e, flow, url.QueryEscape(q.Get("state")), code)
			}
			var cs []*http.Cookie
			if flow != nil {
				cs = append(cs, flow)
			}
			rec := e.do("GET", path, nil, cs...)
			if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/login?error="+tc.want {
				t.Fatalf("got %d %s", rec.Code, rec.Header().Get("Location"))
			}
			if cookieNamed(rec, "__Host-acp_session") != nil || len(e.store.sessions) != 0 {
				t.Error("session must not be created")
			}
			if acts := e.store.actions(); len(acts) != 1 || acts[0] != "auth.login_failed" {
				t.Errorf("audit: %v", acts)
			}
		})
	}
}

func TestStateReplayRejected(t *testing.T) {
	e := newEnv(t)
	flow, loc := e.startLogin(t, "/")
	q := loc.Query()
	code := e.idp.authorize(q.Get("code_challenge"), q.Get("nonce"))
	path := callbackPath + "?code=" + code + "&state=" + url.QueryEscape(q.Get("state"))
	if r := e.do("GET", path, nil, flow); r.Header().Get("Location") != publicURL+"/" {
		t.Fatalf("first use failed: %s", r.Header().Get("Location"))
	}
	if r := e.do("GET", path, nil, flow); r.Header().Get("Location") != "/login?error=login_failed" {
		t.Errorf("replayed code accepted: %s", r.Header().Get("Location"))
	}
}

func TestFlowCookieExpires(t *testing.T) {
	e := newEnv(t)
	flow, loc := e.startLogin(t, "/")
	q := loc.Query()
	code := e.idp.authorize(q.Get("code_challenge"), q.Get("nonce"))
	e.clock.Add(int64(flowTTL + time.Minute))
	rec := e.do("GET", callbackPath+"?code="+code+"&state="+url.QueryEscape(q.Get("state")), nil, flow)
	if rec.Header().Get("Location") != "/login?error=login_failed" {
		t.Errorf("expired flow accepted: %s", rec.Header().Get("Location"))
	}
}

func TestDisabledUserRejected(t *testing.T) {
	e := newEnv(t)
	e.login(t, "/")
	for id, u := range e.store.users {
		u.Disabled = true
		e.store.users[id] = u
	}
	rec := e.login(t, "/")
	if rec.Header().Get("Location") != "/login?error=account_disabled" {
		t.Errorf("got %s", rec.Header().Get("Location"))
	}
}

func TestCSRFAndOrigin(t *testing.T) {
	e := newEnv(t)
	rec := e.login(t, "/")
	sc, cc := cookieNamed(rec, "__Host-acp_session"), cookieNamed(rec, "__Host-acp_csrf")
	tests := []struct {
		name string
		hdr  map[string]string
		want int
	}{
		{"no token", nil, 403},
		{"wrong token", map[string]string{"X-CSRF-Token": "nope"}, 403},
		{"cookie value only is not enough", map[string]string{"X-CSRF-Token": sc.Value}, 403},
		{"valid", map[string]string{"X-CSRF-Token": cc.Value}, 204},
		{"valid same origin", map[string]string{"X-CSRF-Token": cc.Value, "Origin": publicURL}, 204},
		{"cross origin", map[string]string{"X-CSRF-Token": cc.Value, "Origin": "https://evil.example.org"}, 403},
		{"null origin", map[string]string{"X-CSRF-Token": cc.Value, "Origin": "null"}, 403},
	}
	for _, tc := range tests {
		if got := e.do("POST", "/api/v1/echo", tc.hdr, sc).Code; got != tc.want {
			t.Errorf("%s: %d, want %d", tc.name, got, tc.want)
		}
	}
	if got := e.do("POST", "/api/v1/echo", map[string]string{"X-CSRF-Token": cc.Value}).Code; got != 401 {
		t.Errorf("anonymous = %d", got)
	}
	if got := e.do("GET", "/api/v1/me", nil).Code; got != 401 {
		t.Errorf("anonymous me = %d", got)
	}
}

func TestSessionExpiry(t *testing.T) {
	e := newEnv(t)
	sc := cookieNamed(e.login(t, "/"), "__Host-acp_session")

	// Activity within the idle window extends the session past the idle timeout...
	for i := 0; i < 3; i++ {
		e.clock.Add(int64(50 * time.Minute))
		if c := e.do("GET", "/api/v1/me", nil, sc).Code; c != 200 {
			t.Fatalf("step %d: %d", i, c)
		}
	}
	// ...but never beyond the absolute timeout (4h; 150min elapsed).
	e.clock.Add(int64(50 * time.Minute))
	e.do("GET", "/api/v1/me", nil, sc)
	e.clock.Add(int64(50 * time.Minute)) // 250min > 4h
	if c := e.do("GET", "/api/v1/me", nil, sc).Code; c != 401 {
		t.Errorf("absolute expiry not enforced: %d", c)
	}

	// Idle expiry.
	sc2 := cookieNamed(e.login(t, "/"), "__Host-acp_session")
	e.clock.Add(int64(70 * time.Minute))
	if c := e.do("GET", "/api/v1/me", nil, sc2).Code; c != 401 {
		t.Errorf("idle expiry not enforced: %d", c)
	}
}

func TestSafeReturnTo(t *testing.T) {
	for in, want := range map[string]string{
		"": "/", "/": "/", "/a/b?c=d": "/a/b?c=d", "//evil.com": "/", "/\\evil.com": "/",
		"https://evil.com": "/", "evil": "/", "/a\r\nSet-Cookie: x": "/",
	} {
		if got := safeReturnTo(in); got != want {
			t.Errorf("safeReturnTo(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestExtractRoles(t *testing.T) {
	c := map[string]any{"obj": map[string]any{"b": 1, "a": 2}, "arr": []any{"x", 3, "y"}, "str": "z"}
	for claim, want := range map[string]string{"obj": "a,b", "arr": "x,y", "str": "", "none": ""} {
		if got := strings.Join(extractRoles(c, claim), ","); got != want {
			t.Errorf("%s: %q want %q", claim, got, want)
		}
	}
}

func TestExtractRolesZitadel(t *testing.T) {
	const def = "urn:zitadel:iam:org:project:roles"
	obj := func(names ...string) map[string]any {
		m := map[string]any{}
		for _, n := range names {
			m[n] = map[string]any{"org1": "example.com"}
		}
		return m
	}
	tests := []struct {
		name   string
		claims map[string]any
		claim  string
		want   string
	}{
		{"default map shape", map[string]any{def: obj("acp-admin")}, def, "acp-admin"},
		{"array shape", map[string]any{def: []any{"b", "a"}}, def, "a,b"},
		{"project-specific fallback", map[string]any{"urn:zitadel:iam:org:project:123:roles": obj("acp-admin")}, def, "acp-admin"},
		{"project-specific union", map[string]any{
			"urn:zitadel:iam:org:project:1:roles": obj("a", "b"), "urn:zitadel:iam:org:project:2:roles": obj("b", "c")}, def, "a,b,c"},
		{"default wins over project-specific", map[string]any{def: obj("x"), "urn:zitadel:iam:org:project:1:roles": obj("y")}, def, "x"},
		{"custom claim has no fallback", map[string]any{"urn:zitadel:iam:org:project:1:roles": obj("y")}, "custom", ""},
		{"empty", map[string]any{}, def, ""},
		{"unrelated claims", map[string]any{"urn:zitadel:iam:org:project:1:other": obj("y")}, def, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := strings.Join(extractRoles(tc.claims, tc.claim), ","); got != tc.want {
				t.Errorf("got %q want %q", got, tc.want)
			}
		})
	}
}

func TestCallbackRolesFromUserInfo(t *testing.T) {
	e := newEnv(t)
	e.idp.idTokenClaims = map[string]any{}
	e.idp.userInfoClaims = map[string]any{"urn:zitadel:iam:org:project:roles": map[string]any{"acp-admin": map[string]any{"o": "d"}}}
	if rec := e.login(t, "/"); rec.Code != http.StatusSeeOther {
		t.Fatalf("status %d", rec.Code)
	}
	if len(*e.roles) != 1 || strings.Join((*e.roles)[0], ",") != "acp-admin" {
		t.Fatalf("roles synced: %v", *e.roles)
	}
}

func TestCallbackUserInfoFailureNotFatal(t *testing.T) {
	e := newEnv(t)
	e.idp.idTokenClaims = map[string]any{}
	e.idp.userInfoFails = true
	e.store.noAccess = true
	rec := e.login(t, "/")
	if loc := rec.Header().Get("Location"); !strings.Contains(loc, "error=no_access") {
		t.Fatalf("location %q", loc)
	}
	for _, a := range e.store.audit {
		if a.Action != "auth.login_denied" {
			continue
		}
		var d map[string]any
		_ = json.Unmarshal(a.Details, &d)
		if d["role_source"] != "none" || d["roles_received"] != float64(0) {
			t.Errorf("details: %v", d)
		}
		return
	}
	t.Fatal("no login_denied audit")
}

func TestPlainHTTPCookieNames(t *testing.T) {
	e := newEnv(t)
	svc, err := New(Options{Issuer: e.idp.srv.URL, ClientID: "client-1", PublicURL: "http://localhost:8080",
		IdleTimeout: time.Hour, AbsoluteTimeout: time.Hour, StateKey: []byte("0123456789abcdef"), Store: e.store,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	if svc.names.session != "acp_session" || svc.secure {
		t.Errorf("names=%+v secure=%v", svc.names, svc.secure)
	}
}

func TestIdPUnavailable(t *testing.T) {
	e := newEnv(t)
	e.idp.srv.Close()
	rec := e.do("GET", "/api/auth/oidc/login", nil)
	if rec.Header().Get("Location") != "/login?error=idp_unavailable" {
		t.Errorf("got %d %s", rec.Code, rec.Header().Get("Location"))
	}
}

func TestLoginRejectedWithoutMembership(t *testing.T) {
	e := newEnv(t)
	e.store.noAccess = true
	rec := e.login(t, "/")
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != loginPath+"?error=no_access" {
		t.Fatalf("callback: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	if cookieNamed(rec, "__Host-acp_session") != nil || len(e.store.sessions) != 0 {
		t.Error("no session may be created without access")
	}
	found := false
	for _, a := range e.store.actions() {
		found = found || a == "auth.login_denied"
	}
	if !found {
		t.Errorf("denial not audited: %v", e.store.actions())
	}
}
