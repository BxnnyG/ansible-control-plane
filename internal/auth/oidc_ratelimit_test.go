package auth

import (
	"net/http"
	"testing"
	"time"
)

func TestOIDCRateLimit(t *testing.T) {
	e := newEnv(t)
	e.svc.oidcLimiter = newIPLimiter(2, time.Minute)
	for i := 0; i < 2; i++ {
		if rec := e.do("GET", "/api/auth/oidc/login", map[string]string{"Accept": "text/html"}); rec.Code != http.StatusSeeOther {
			t.Fatalf("request %d: %d", i, rec.Code)
		}
	}
	rec := e.do("GET", "/api/auth/oidc/login", map[string]string{"Accept": "text/html"})
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != loginPath+"?error=rate_limited" {
		t.Fatalf("browser: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Error("missing Retry-After")
	}
	// The callback shares the budget; non-browser clients get 429.
	rec = e.do("GET", callbackPath+"?code=x&state=y", nil)
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("api client: %d", rec.Code)
	}
	// After the window the budget is restored.
	e.clock.Add(int64(2 * time.Minute))
	if rec := e.do("GET", "/api/auth/oidc/login", map[string]string{"Accept": "text/html"}); rec.Code != http.StatusSeeOther ||
		rec.Header().Get("Location") == loginPath+"?error=rate_limited" {
		t.Fatalf("after window: %d %s", rec.Code, rec.Header().Get("Location"))
	}
}

func TestIPLimiterEvictsSoonestExpiring(t *testing.T) {
	l := newIPLimiter(1, time.Minute)
	now := time.Now()
	l.fail("old", now)
	for i := 0; i < maxLimiterEntries; i++ {
		l.fail(string(rune('a'+i%26))+time.Duration(i).String(), now.Add(time.Second))
	}
	if len(l.m) > maxLimiterEntries {
		t.Fatalf("limiter grew to %d", len(l.m))
	}
	if _, ok := l.m["old"]; ok {
		t.Error("soonest-expiring entry should have been evicted")
	}
}
