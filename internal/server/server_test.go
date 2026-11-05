package server

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

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
	if c := get(ok, "/metrics").Code; c != 200 {
		t.Errorf("metrics %d", c)
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
