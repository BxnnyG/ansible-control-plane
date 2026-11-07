package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

func scrape(t *testing.T) string {
	t.Helper()
	rec := httptest.NewRecorder()
	promhttp.Handler().ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/metrics", nil))
	return rec.Body.String()
}

func TestHTTPMetricsUseRoutePattern(t *testing.T) {
	r := chi.NewRouter()
	r.Use(httpMetrics)
	r.Get("/api/v1/things/{id}", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) })
	r.NotFound(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotFound) })
	for _, p := range []string{"/api/v1/things/aaa", "/api/v1/things/bbb", "/random/secret-host.example"} {
		r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequestWithContext(t.Context(), http.MethodGet, p, nil))
	}
	out := scrape(t)
	if !strings.Contains(out, `acp_http_requests_total{method="GET",route="/api/v1/things/{id}",status="418"} 2`) {
		t.Errorf("route pattern counter missing:\n%s", grep(out, "acp_http_requests_total"))
	}
	if strings.Contains(out, "secret-host") || strings.Contains(out, "things/aaa") {
		t.Error("raw path leaked into labels")
	}
	if !strings.Contains(out, "acp_runs_queued") {
		t.Error("acp_runs_queued missing")
	}
}

func grep(s, sub string) string {
	var b strings.Builder
	for _, l := range strings.Split(s, "\n") {
		if strings.Contains(l, sub) {
			b.WriteString(l + "\n")
		}
	}
	return b.String()
}
