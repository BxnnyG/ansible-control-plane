package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/BxnnyG/ansible-control-plane/internal/auth"
	"github.com/BxnnyG/ansible-control-plane/internal/db/dbgen"
	"github.com/BxnnyG/ansible-control-plane/internal/policy"
	"github.com/BxnnyG/ansible-control-plane/internal/runs"
	"github.com/BxnnyG/ansible-control-plane/internal/schedules"
)

type fakeSchedules struct {
	err     error
	known   uuid.UUID // template id accepted by HandleWebhook
	hookErr error
	inputs  []schedules.Input
	calls   int
}

func (f *fakeSchedules) sched() schedules.Schedule {
	return schedules.Schedule{ID: uuid.New(), TemplateID: uuid.New(), Cron: "0 3 * * *", Timezone: "UTC", MissedPolicy: "skip"}
}
func (f *fakeSchedules) List(context.Context, uuid.UUID, uuid.UUID) ([]schedules.Schedule, error) {
	return []schedules.Schedule{f.sched()}, f.err
}
func (f *fakeSchedules) ListAll(context.Context, uuid.UUID) ([]schedules.Schedule, error) {
	return []schedules.Schedule{f.sched()}, f.err
}
func (f *fakeSchedules) Create(_ context.Context, _, _, _ uuid.UUID, in schedules.Input) (schedules.Schedule, error) {
	f.inputs = append(f.inputs, in)
	return f.sched(), f.err
}
func (f *fakeSchedules) Get(context.Context, uuid.UUID, uuid.UUID) (schedules.Schedule, error) {
	return f.sched(), f.err
}
func (f *fakeSchedules) Update(_ context.Context, _, _ uuid.UUID, in schedules.Input) (schedules.Schedule, error) {
	f.inputs = append(f.inputs, in)
	return f.sched(), f.err
}
func (f *fakeSchedules) Delete(context.Context, uuid.UUID, uuid.UUID) error { return f.err }
func (f *fakeSchedules) Preview(string, string, int) ([]time.Time, error) {
	return []time.Time{time.Date(2026, 1, 1, 3, 0, 0, 0, time.UTC)}, f.err
}
func (f *fakeSchedules) GetWebhook(context.Context, uuid.UUID, uuid.UUID) (schedules.Webhook, error) {
	return schedules.Webhook{Enabled: true, HasSecret: true, Path: "/api/hooks/templates/x"}, f.err
}
func (f *fakeSchedules) RotateWebhook(context.Context, uuid.UUID, uuid.UUID) (schedules.Webhook, error) {
	return schedules.Webhook{Enabled: true, HasSecret: true, Path: "/api/hooks/templates/x", Secret: "fresh-secret"}, f.err
}
func (f *fakeSchedules) DisableWebhook(context.Context, uuid.UUID, uuid.UUID) error { return f.err }
func (f *fakeSchedules) HandleWebhook(_ context.Context, id uuid.UUID, _ func(string) string, _ []byte) (runs.Run, error) {
	f.calls++
	if id != f.known {
		return runs.Run{}, schedules.ErrRejected
	}
	return runs.Run{ID: uuid.New()}, f.hookErr
}

func schedulesRouter(role string, fs *fakeSchedules, audits *[]string, scopes ...dbgen.MembershipScope) http.Handler {
	uid := uuid.New()
	st := fakeAPIStore{ws: dbgen.Workspace{ID: uuid.New(), Slug: "default", Name: "Default"}, user: uid, role: role, audits: audits, scopes: scopes}
	authn := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(auth.WithPrincipal(r.Context(), auth.Principal{User: dbgen.User{ID: uid}})))
		})
	}
	return Router(quiet, fakePing{}, DefaultStatic(), WithPolicy(policy.New(st, nil, authn, quiet), st), WithSchedules(fs))
}

func TestSchedulesAPIPermissions(t *testing.T) {
	tpl, sid := uuid.NewString(), uuid.NewString()
	const body = `{"cron":"0 3 * * *","timezone":"Europe/Berlin"}`
	tests := []struct {
		name, role, method, path, body string
		want                           int
		audit                          string
	}{
		{"viewer lists", policy.RoleViewer, "GET", "/templates/" + tpl + "/schedules", "", 200, ""},
		{"viewer lists workspace", policy.RoleViewer, "GET", "/schedules", "", 200, ""},
		{"viewer previews", policy.RoleViewer, "GET", "/schedules/preview?cron=*+*+*+*+*&tz=UTC", "", 200, ""},
		{"viewer gets", policy.RoleViewer, "GET", "/schedules/" + sid, "", 200, ""},
		{"viewer cannot create", policy.RoleViewer, "POST", "/templates/" + tpl + "/schedules", body, 403, ""},
		{"operator cannot create", policy.RoleOperator, "POST", "/templates/" + tpl + "/schedules", body, 403, ""},
		{"editor creates", policy.RoleEditor, "POST", "/templates/" + tpl + "/schedules", body, 201, "schedule.create"},
		{"unknown field", policy.RoleEditor, "POST", "/templates/" + tpl + "/schedules", `{"cron":"* * * * *","x":1}`, 400, ""},
		{"editor updates", policy.RoleEditor, "PUT", "/schedules/" + sid, `{"enabled":false}`, 200, "schedule.update"},
		{"viewer cannot update", policy.RoleViewer, "PUT", "/schedules/" + sid, `{"enabled":false}`, 403, ""},
		{"editor deletes", policy.RoleEditor, "DELETE", "/schedules/" + sid, "", 204, "schedule.delete"},
		{"operator cannot delete", policy.RoleOperator, "DELETE", "/schedules/" + sid, "", 403, ""},
		{"bad id", policy.RoleEditor, "GET", "/schedules/nope", "", 404, ""},
		{"bad n", policy.RoleViewer, "GET", "/schedules/preview?cron=*+*+*+*+*&n=x", "", 422, ""},
		{"editor enables webhook", policy.RoleEditor, "POST", "/templates/" + tpl + "/webhook", "", 200, "template.webhook_set"},
		{"viewer cannot enable webhook", policy.RoleViewer, "POST", "/templates/" + tpl + "/webhook", "", 403, ""},
		{"viewer cannot read webhook", policy.RoleViewer, "GET", "/templates/" + tpl + "/webhook", "", 403, ""},
		{"editor disables webhook", policy.RoleEditor, "DELETE", "/templates/" + tpl + "/webhook", "", 204, "template.webhook_delete"},
		{"admin creates", policy.RoleAdmin, "POST", "/templates/" + tpl + "/schedules", body, 201, "schedule.create"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var audits []string
			h := schedulesRouter(tc.role, &fakeSchedules{}, &audits)
			rec := doRuns(h, tc.method, tc.path, tc.body)
			if rec.Code != tc.want {
				t.Fatalf("status %d, want %d: %s", rec.Code, tc.want, rec.Body.String())
			}
			if tc.audit == "" && len(audits) > 0 && tc.want < 300 || tc.audit != "" && !containsStr(audits, tc.audit) {
				t.Errorf("audits = %v, want %q", audits, tc.audit)
			}
		})
	}
}

func containsStr(l []string, s string) bool {
	for _, v := range l {
		if v == s {
			return true
		}
	}
	return false
}

func TestSchedulesAPIScopedDenied(t *testing.T) {
	scope := dbgen.MembershipScope{}
	h := schedulesRouter(policy.RoleEditor, &fakeSchedules{}, nil, scope)
	for _, p := range []string{"/schedules", "/templates/" + uuid.NewString() + "/schedules", "/schedules/" + uuid.NewString(), "/templates/" + uuid.NewString() + "/webhook"} {
		if rec := doRuns(h, "GET", p, ""); rec.Code != 403 {
			t.Errorf("scoped GET %s = %d, want 403", p, rec.Code)
		}
	}
}

func TestSchedulesAPIErrorsAndSecretOnce(t *testing.T) {
	h := schedulesRouter(policy.RoleEditor, &fakeSchedules{err: schedules.ErrInvalid}, nil)
	if rec := doRuns(h, "POST", "/templates/"+uuid.NewString()+"/schedules", `{"cron":"x"}`); rec.Code != 422 {
		t.Errorf("invalid = %d", rec.Code)
	}
	h = schedulesRouter(policy.RoleEditor, &fakeSchedules{err: schedules.ErrNotFound}, nil)
	if rec := doRuns(h, "GET", "/schedules/"+uuid.NewString(), ""); rec.Code != 404 {
		t.Errorf("not found = %d", rec.Code)
	}
	h = schedulesRouter(policy.RoleEditor, &fakeSchedules{}, nil)
	post := doRuns(h, "POST", "/templates/"+uuid.NewString()+"/webhook", "")
	if !strings.Contains(post.Body.String(), "fresh-secret") || post.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("rotate must show the secret once: %s", post.Body.String())
	}
	get := doRuns(h, "GET", "/templates/"+uuid.NewString()+"/webhook", "")
	if strings.Contains(get.Body.String(), `"secret":`) || strings.Contains(get.Body.String(), "fresh-secret") {
		t.Errorf("GET leaks the secret: %s", get.Body.String())
	}
}

func TestTemplateHookEndpoint(t *testing.T) {
	known := uuid.New()
	tests := []struct {
		name, path string
		hookErr    error
		want       int
	}{
		{"accepted", known.String(), nil, 202},
		{"unknown or bad signature", uuid.NewString(), nil, 404},
		{"malformed id", "nope", nil, 404},
		{"valid signature, invalid overrides", known.String(), runs.ErrInvalid, 422},
		{"valid signature, project unsynced", known.String(), runs.ErrNotSynced, 409},
	}
	var notFoundBodies []string
	for _, tc := range tests {
		h := schedulesRouter(policy.RoleViewer, &fakeSchedules{known: known, hookErr: tc.hookErr}, nil) // no session, no CSRF header
		req := httptest.NewRequestWithContext(context.Background(), "POST", "/api/hooks/templates/"+tc.path, strings.NewReader(`{}`))
		req.RemoteAddr = "192.0.2.10:4000"
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != tc.want {
			t.Errorf("%s: status %d body %q", tc.name, rec.Code, rec.Body.String())
		}
		if tc.want == 404 {
			notFoundBodies = append(notFoundBodies, rec.Body.String())
		}
	}
	if len(notFoundBodies) == 2 && notFoundBodies[0] != notFoundBodies[1] {
		t.Errorf("404 bodies differ: %q vs %q", notFoundBodies[0], notFoundBodies[1])
	}
}

func TestTemplateHookRateLimitSharedWithProjectHooks(t *testing.T) {
	fs := &fakeSchedules{}
	h := schedulesRouter(policy.RoleViewer, fs, nil)
	var last int
	for range 61 {
		req := httptest.NewRequestWithContext(context.Background(), "POST", "/api/hooks/templates/"+uuid.NewString(), strings.NewReader(`{}`))
		req.RemoteAddr = "192.0.2.77:1"
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		last = rec.Code
	}
	if last != http.StatusTooManyRequests {
		t.Errorf("61st call = %d, want 429", last)
	}
}
