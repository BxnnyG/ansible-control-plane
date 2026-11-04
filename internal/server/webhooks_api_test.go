package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/BxnnyG/ansible-control-plane/internal/policy"
	"github.com/BxnnyG/ansible-control-plane/internal/projects"
)

const testHookSecret = "plain-test-secret-value" //nolint:gosec // fake value

// Webhook methods of the shared fake; hookOut drives the public endpoint.
func (f *fakeProjects) wh(secret string) projects.WebhookSecret {
	return projects.WebhookSecret{
		Webhook: projects.Webhook{Enabled: true, Provider: "github", HasSecret: true, Path: projects.HookPath(uuid.New())},
		Secret:  secret,
	}
}

func (f *fakeProjects) GetWebhook(context.Context, uuid.UUID, uuid.UUID) (projects.Webhook, error) {
	return f.wh("").Webhook, f.err
}

func (f *fakeProjects) SetWebhook(context.Context, uuid.UUID, uuid.UUID, bool, string) (projects.WebhookSecret, error) {
	return f.wh(testHookSecret), f.err
}

func (f *fakeProjects) RotateWebhook(context.Context, uuid.UUID, uuid.UUID) (projects.WebhookSecret, error) {
	return f.wh(testHookSecret), f.err
}

func (f *fakeProjects) DeleteWebhook(context.Context, uuid.UUID, uuid.UUID) error { return f.err }

func (f *fakeProjects) HandleWebhook(_ context.Context, id uuid.UUID, _ func(string) string, body []byte) projects.HookOutcome {
	f.hookCalls++
	f.hookBodyLen = len(body)
	if id == f.hookKnown {
		return f.hookOut
	}
	return projects.HookRejected
}

func TestWebhookManagementAPI(t *testing.T) {
	id := uuid.NewString()
	base := "/projects/" + id + "/webhook"
	tests := []struct {
		name, role, method, path, body string
		want                           int
		audit                          string
		secret                         bool
	}{
		{"editor reads", policy.RoleEditor, "GET", base, "", 200, "", false},
		{"viewer cannot read", policy.RoleViewer, "GET", base, "", 403, "", false},
		{"operator cannot read", policy.RoleOperator, "GET", base, "", 403, "", false},
		{"editor enables", policy.RoleEditor, "PUT", base, `{"enabled":true,"provider":"github"}`, 200, "project.webhook_set", true},
		{"viewer cannot enable", policy.RoleViewer, "PUT", base, `{"enabled":true,"provider":"github"}`, 403, "", false},
		{"unknown field", policy.RoleEditor, "PUT", base, `{"enabled":true,"provider":"github","x":1}`, 400, "", false},
		{"editor rotates", policy.RoleEditor, "POST", base + "/rotate", "", 200, "project.webhook_rotate", true},
		{"operator cannot rotate", policy.RoleOperator, "POST", base + "/rotate", "", 403, "", false},
		{"editor deletes", policy.RoleEditor, "DELETE", base, "", 204, "project.webhook_delete", false},
		{"viewer cannot delete", policy.RoleViewer, "DELETE", base, "", 403, "", false},
		{"bad id", policy.RoleEditor, "GET", "/projects/nope/webhook", "", 404, "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var audits []string
			h := projectsRouter(tc.role, &fakeProjects{}, &audits)
			req := httptest.NewRequestWithContext(context.Background(), tc.method, "/api/v1/workspaces/default"+tc.path, strings.NewReader(tc.body))
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("status %d, want %d: %s", rec.Code, tc.want, rec.Body.String())
			}
			if got := strings.Contains(rec.Body.String(), testHookSecret); got != tc.secret {
				t.Errorf("secret in response = %v, want %v", got, tc.secret)
			}
			if tc.audit != "" && (len(audits) != 1 || audits[0] != tc.audit) {
				t.Errorf("audits %v, want %s", audits, tc.audit)
			}
			for _, a := range audits {
				if strings.Contains(a, testHookSecret) {
					t.Error("secret leaked into audit")
				}
			}
		})
	}
}

func TestWebhookManagementErrors(t *testing.T) {
	id := uuid.NewString()
	for err, want := range map[error]int{projects.ErrNotFound: 404, projects.ErrInvalid: 422, context.DeadlineExceeded: 500} {
		var audits []string
		h := projectsRouter(policy.RoleAdmin, &fakeProjects{err: err}, &audits)
		req := httptest.NewRequestWithContext(context.Background(), "POST", "/api/v1/workspaces/default/projects/"+id+"/webhook/rotate", nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != want || len(audits) != 0 {
			t.Errorf("%v: status %d audits %v, want %d", err, rec.Code, audits, want)
		}
	}
}

func TestPublicHookEndpoint(t *testing.T) {
	known := uuid.New()
	tests := []struct {
		name, path string
		out        projects.HookOutcome
		want       int
		wantBody   string
	}{
		{"accepted", known.String(), projects.HookAccepted, 202, "accepted"},
		{"ignored ref", known.String(), projects.HookIgnored, 202, "ignored"},
		{"bad signature", known.String(), projects.HookRejected, 404, "not found"},
		{"unknown project", uuid.NewString(), projects.HookAccepted, 404, "not found"},
		{"malformed id", "nope", projects.HookAccepted, 404, "not found"},
	}
	var bodies []string
	for _, tc := range tests {
		fp := &fakeProjects{hookKnown: known, hookOut: tc.out}
		h := projectsRouter(policy.RoleViewer, fp, nil) // no session, no CSRF header
		req := httptest.NewRequestWithContext(context.Background(), "POST", "/api/hooks/projects/"+tc.path, strings.NewReader(`{"ref":"refs/heads/main"}`))
		req.RemoteAddr = "192.0.2.10:4000"
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != tc.want || !strings.Contains(rec.Body.String(), tc.wantBody) {
			t.Errorf("%s: status %d body %q", tc.name, rec.Code, rec.Body.String())
		}
		if tc.want == 404 {
			bodies = append(bodies, rec.Body.String())
		}
	}
	for _, b := range bodies[1:] {
		if b != bodies[0] {
			t.Errorf("404 responses differ: %q vs %q", b, bodies[0])
		}
	}
}

func TestPublicHookBodyLimit(t *testing.T) {
	known := uuid.New()
	fp := &fakeProjects{hookKnown: known, hookOut: projects.HookAccepted}
	h := projectsRouter(policy.RoleViewer, fp, nil)
	big := strings.Repeat("a", projects.MaxWebhookBody+1)
	req := httptest.NewRequestWithContext(context.Background(), "POST", "/api/hooks/projects/"+known.String(), strings.NewReader(big))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge || fp.hookCalls != 0 {
		t.Errorf("status %d calls %d", rec.Code, fp.hookCalls)
	}
}

func TestPublicHookRateLimit(t *testing.T) {
	known := uuid.New()
	fp := &fakeProjects{hookKnown: known, hookOut: projects.HookAccepted}
	h := projectsRouter(policy.RoleViewer, fp, nil)
	var last int
	for range 61 {
		req := httptest.NewRequestWithContext(context.Background(), "POST", "/api/hooks/projects/"+known.String(), strings.NewReader("{}"))
		req.RemoteAddr = "192.0.2.20:1"
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		last = rec.Code
	}
	if last != http.StatusTooManyRequests {
		t.Errorf("61st request status %d, want 429", last)
	}
	req := httptest.NewRequestWithContext(context.Background(), "POST", "/api/hooks/projects/"+known.String(), strings.NewReader("{}"))
	req.RemoteAddr = "192.0.2.21:1" // another source is unaffected
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Errorf("other source status %d", rec.Code)
	}
}

func TestHookLimiterWindow(t *testing.T) {
	l := newHookLimiter(2, time.Minute)
	now := time.Now()
	first, second, third := l.allow("a", now), l.allow("a", now), l.allow("a", now)
	if !first || !second || third {
		t.Error("expected two allowed then blocked")
	}
	if !l.allow("a", now.Add(2*time.Minute)) {
		t.Error("window should reset")
	}
}
