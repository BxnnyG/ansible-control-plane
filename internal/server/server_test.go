package server

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/go-chi/chi/v5"
)

type fakePing struct{ err error }

func (f fakePing) Ping(context.Context) error { return f.err }

func get(h http.Handler, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), http.MethodGet, path, nil))
	return rec
}

func TestRoutes(t *testing.T) {
	log := slog.New(slog.NewJSONHandler(io.Discard, nil))
	ok := Router(log, fakePing{}, DefaultStatic())
	bad := Router(log, fakePing{errors.New("down")}, DefaultStatic())
	if c := get(ok, "/healthz").Code; c != 200 {
		t.Errorf("healthz %d", c)
	}
	if c := get(ok, "/readyz").Code; c != 200 {
		t.Errorf("readyz %d", c)
	}
	if c := get(bad, "/readyz").Code; c != 503 {
		t.Errorf("readyz down %d", c)
	}
	if rec := get(ok, "/metrics"); rec.Code != http.StatusNotFound || strings.Contains(rec.Body.String(), "go_goroutines") {
		t.Errorf("metrics without a token: want 404, got %d", rec.Code)
	}
	if c := get(ok, "/some/spa/route").Code; c != 200 {
		t.Errorf("spa fallback %d", c)
	}
}

func TestRedirectUsesConfiguredHostOnly(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	h := redirectExceptOps(ok, "https://acp.example.com:8443")

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/runs?x=1", nil)
	req.Host = "evil.example.net"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if got := rec.Header().Get("Location"); got != "https://acp.example.com:8443/runs?x=1" {
		t.Fatalf("unexpected redirect target %q", got)
	}

	req = httptest.NewRequestWithContext(context.Background(), http.MethodGet, "//evil.example.net/x", nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if got := rec.Header().Get("Location"); got != "https://acp.example.com:8443/evil.example.net/x" {
		t.Fatalf("protocol-relative path escaped: %q", got)
	}

	rec = get(h, "/healthz")
	if rec.Code != http.StatusOK {
		t.Fatalf("healthz must not redirect, got %d", rec.Code)
	}
}

func TestWorkerAPINotOnPublicRouter(t *testing.T) {
	log := slog.New(slog.NewJSONHandler(io.Discard, nil))
	h := Router(log, fakePing{}, DefaultStatic())
	for _, p := range []string{"/api/internal/worker/claim", "/api/internal/worker/runs/x/bundle"} {
		if c := get(h, p).Code; c != http.StatusNotFound {
			t.Errorf("%s on public router: %d", p, c)
		}
		// Even behind the HTTPS redirect the path is redirected, never served.
		if c := get(redirectExceptOps(h, "https://acp.example.com:8443"), p).Code; c != http.StatusMovedPermanently {
			t.Errorf("%s not redirected: %d", p, c)
		}
	}
}

func TestWorkerAPIListener(t *testing.T) {
	log := slog.New(slog.NewJSONHandler(io.Discard, nil))
	inner := chi.NewRouter()
	inner.Get("/claim", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) })
	h := WorkerAPIHandler(inner, log)
	if c := get(h, "/api/internal/worker/claim").Code; c != http.StatusTeapot {
		t.Errorf("worker listener: %d", c)
	}
	// Nothing but the worker API is served on the internal listener.
	for _, p := range []string{"/", "/api/v1/me", "/healthz", "/api/internal/other"} {
		if c := get(h, p).Code; c != http.StatusNotFound {
			t.Errorf("%s on worker listener: %d", p, c)
		}
	}
}

func TestMetricsBearerToken(t *testing.T) {
	log := slog.New(slog.NewJSONHandler(io.Discard, nil))
	h := Router(log, fakePing{}, DefaultStatic(), WithMetricsToken("s3cret-token"))
	req := func(auth string) *httptest.ResponseRecorder {
		r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/metrics", nil)
		if auth != "" {
			r.Header.Set("Authorization", auth)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec
	}
	if c := req("").Code; c != http.StatusUnauthorized {
		t.Errorf("no token: %d", c)
	}
	if c := req("Bearer wrong").Code; c != http.StatusUnauthorized {
		t.Errorf("wrong token: %d", c)
	}
	if rec := req("Bearer s3cret-token"); rec.Code != 200 || !strings.Contains(rec.Body.String(), "go_goroutines") {
		t.Errorf("valid token: %d", rec.Code)
	}
}

func TestWorkerListenerServesMetrics(t *testing.T) {
	log := slog.New(slog.NewJSONHandler(io.Discard, nil))
	h := WorkerAPIHandler(http.NotFoundHandler(), log)
	r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "go_goroutines") {
		t.Errorf("worker listener metrics: %d", rec.Code)
	}
}

func TestSecurityHeaders(t *testing.T) {
	log := slog.New(slog.NewJSONHandler(io.Discard, nil))
	static := fstest.MapFS{"index.html": {Data: []byte(`<html><head><script>var a=1;</script><script src="/x.js"></script></head>` +
		`<body><script>{ boot("x") }</script></body></html>`)}}
	h := Router(log, fakePing{}, static)
	rec := get(h, "/")
	hd := rec.Header()
	csp := hd.Get("Content-Security-Policy")
	for _, want := range []string{"default-src 'self'", "frame-ancestors 'none'", "object-src 'none'", "'sha256-"} {
		if !strings.Contains(csp, want) {
			t.Errorf("CSP lacks %q: %s", want, csp)
		}
	}
	if strings.Count(csp, "'sha256-") != 2 {
		t.Errorf("want 2 inline script hashes: %s", csp)
	}
	if strings.Contains(csp, "script-src 'self' 'unsafe-inline'") || strings.Contains(csp, "unsafe-eval") {
		t.Errorf("script-src too loose: %s", csp)
	}
	if hd.Get("X-Content-Type-Options") != "nosniff" || hd.Get("X-Frame-Options") != "DENY" || hd.Get("Referrer-Policy") != "same-origin" {
		t.Errorf("headers: %v", hd)
	}
	if hd.Get("Strict-Transport-Security") != "" {
		t.Error("HSTS must not be sent without TLS")
	}
	r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/healthz", nil)
	r.TLS = &tls.ConnectionState{}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Header().Get("Strict-Transport-Security") == "" {
		t.Error("HSTS missing on TLS")
	}
}
