package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/BxnnyG/ansible-control-plane/internal/auth"
	"github.com/BxnnyG/ansible-control-plane/internal/db/dbgen"
	"github.com/BxnnyG/ansible-control-plane/internal/notify"
	"github.com/BxnnyG/ansible-control-plane/internal/policy"
)

type fakeNotify struct {
	ch        notify.ChannelView
	rule      notify.RuleView
	err       error
	testErr   error
	gotChan   []notify.ChannelInput
	gotRule   []notify.RuleInput
	delivs    []notify.DeliveryView
	delivLim  int
	deleted   int
	testCalls int
}

func (f *fakeNotify) CreateChannel(_ context.Context, _ uuid.UUID, in notify.ChannelInput, _ uuid.UUID) (notify.ChannelView, error) {
	f.gotChan = append(f.gotChan, in)
	return f.ch, f.err
}
func (f *fakeNotify) GetChannel(_ context.Context, _, id uuid.UUID) (notify.ChannelView, error) {
	if f.err == nil && id != f.ch.ID {
		return notify.ChannelView{}, notify.ErrNotFound
	}
	return f.ch, f.err
}
func (f *fakeNotify) ListChannels(context.Context, uuid.UUID) ([]notify.ChannelView, error) {
	return []notify.ChannelView{f.ch}, f.err
}
func (f *fakeNotify) UpdateChannel(_ context.Context, _, _ uuid.UUID, in notify.ChannelInput, _ uuid.UUID) (notify.ChannelView, error) {
	f.gotChan = append(f.gotChan, in)
	return f.ch, f.err
}
func (f *fakeNotify) DeleteChannel(context.Context, uuid.UUID, uuid.UUID) error {
	f.deleted++
	return f.err
}
func (f *fakeNotify) CreateRule(_ context.Context, _ uuid.UUID, in notify.RuleInput, _ uuid.UUID) (notify.RuleView, error) {
	f.gotRule = append(f.gotRule, in)
	return f.rule, f.err
}
func (f *fakeNotify) GetRule(_ context.Context, _, id uuid.UUID) (notify.RuleView, error) {
	if f.err == nil && id != f.rule.ID {
		return notify.RuleView{}, notify.ErrNotFound
	}
	return f.rule, f.err
}
func (f *fakeNotify) ListRules(context.Context, uuid.UUID) ([]notify.RuleView, error) {
	return []notify.RuleView{f.rule}, f.err
}
func (f *fakeNotify) UpdateRule(_ context.Context, _, _ uuid.UUID, in notify.RuleInput) (notify.RuleView, error) {
	f.gotRule = append(f.gotRule, in)
	return f.rule, f.err
}
func (f *fakeNotify) DeleteRule(context.Context, uuid.UUID, uuid.UUID) error {
	f.deleted++
	return f.err
}
func (f *fakeNotify) ListDeliveries(_ context.Context, _, _ uuid.UUID, limit int) ([]notify.DeliveryView, error) {
	f.delivLim = limit
	return f.delivs, f.err
}
func (f *fakeNotify) SendTest(context.Context, uuid.UUID, uuid.UUID) error {
	f.testCalls++
	return f.testErr
}

type notifyEnv struct {
	h      http.Handler
	svc    *fakeNotify
	audits *[]string
}

func newNotifyEnv(t *testing.T, role string) *notifyEnv {
	t.Helper()
	uid := uuid.New()
	audits := []string{}
	st := fakeAPIStore{ws: dbgen.Workspace{ID: uuid.New(), Slug: "default", Name: "Default"}, user: uid, role: role, audits: &audits}
	authn := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(auth.WithPrincipal(r.Context(), auth.Principal{User: dbgen.User{ID: uid, DisplayName: "Ann"}})))
		})
	}
	az := policy.New(st, nil, authn, quiet)
	sec := uuid.New()
	svc := &fakeNotify{
		ch:   notify.ChannelView{ID: uuid.New(), Name: "ops", Type: "webhook", Config: json.RawMessage(`{"url":"https://hooks.example.test/x"}`), SecretID: &sec, Enabled: true},
		rule: notify.RuleView{ID: uuid.New(), ChannelID: uuid.New(), Events: []string{"run.failed"}, Enabled: true},
	}
	svc.rule.ChannelID = svc.ch.ID
	h := Router(quiet, fakePing{}, DefaultStatic(), WithPolicy(az, st), WithNotifications(svc))
	return &notifyEnv{h: h, svc: svc, audits: &audits}
}

func (e *notifyEnv) do(method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequestWithContext(context.Background(), method, "/api/v1/workspaces/default/notifications"+path, strings.NewReader(body))
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	return rec
}

func TestNotificationsAuthorization(t *testing.T) {
	chBody := `{"name":"ops","type":"webhook","config":{"url":"https://hooks.example.test/x"}}`
	tests := []struct {
		name         string
		role         string
		method, path string // {ch} and {rule} are replaced
		body         string
		want         int
	}{
		{"viewer cannot list channels", policy.RoleViewer, "GET", "/channels", "", 403},
		{"operator cannot list rules", policy.RoleOperator, "GET", "/rules", "", 403},
		{"editor lists channels", policy.RoleEditor, "GET", "/channels", "", 200},
		{"editor gets channel", policy.RoleEditor, "GET", "/channels/{ch}", "", 200},
		{"editor lists rules", policy.RoleEditor, "GET", "/rules", "", 200},
		{"editor lists deliveries", policy.RoleEditor, "GET", "/deliveries?channel_id={ch}", "", 200},
		{"editor cannot create channel", policy.RoleEditor, "POST", "/channels", chBody, 403},
		{"editor cannot test", policy.RoleEditor, "POST", "/channels/{ch}/test", "", 403},
		{"editor cannot delete rule", policy.RoleEditor, "DELETE", "/rules/{rule}", "", 403},
		{"admin creates channel", policy.RoleAdmin, "POST", "/channels", chBody, 201},
		{"admin patches channel", policy.RoleAdmin, "PATCH", "/channels/{ch}", `{"enabled":false}`, 200},
		{"admin deletes channel", policy.RoleAdmin, "DELETE", "/channels/{ch}", "", 204},
		{"admin tests channel", policy.RoleAdmin, "POST", "/channels/{ch}/test", "", 200},
		{"owner creates rule", policy.RoleOwner, "POST", "/rules", `{"channel_id":"` + uuid.NewString() + `","events":["run.failed"]}`, 201},
		{"admin patches rule", policy.RoleAdmin, "PATCH", "/rules/{rule}", `{"enabled":false}`, 200},
		{"admin deletes rule", policy.RoleAdmin, "DELETE", "/rules/{rule}", "", 204},
		{"unknown channel", policy.RoleAdmin, "GET", "/channels/" + uuid.NewString(), "", 404},
		{"bad id", policy.RoleAdmin, "GET", "/channels/nope", "", 404},
		{"empty patch", policy.RoleAdmin, "PATCH", "/channels/{ch}", `{}`, 422},
		{"unknown field", policy.RoleAdmin, "POST", "/channels", `{"name":"x","bogus":1}`, 400},
		{"deliveries need channel", policy.RoleEditor, "GET", "/deliveries", "", 422},
		{"deliveries bad status", policy.RoleEditor, "GET", "/deliveries?channel_id={ch}&status=x", "", 422},
		{"deliveries bad limit", policy.RoleEditor, "GET", "/deliveries?channel_id={ch}&limit=0", "", 422},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := newNotifyEnv(t, tc.role)
			path := strings.NewReplacer("{ch}", e.svc.ch.ID.String(), "{rule}", e.svc.rule.ID.String()).Replace(tc.path)
			if rec := e.do(tc.method, path, tc.body); rec.Code != tc.want {
				t.Fatalf("status %d, want %d: %.200s", rec.Code, tc.want, rec.Body.String())
			}
		})
	}
}

func TestNotificationsBehaviour(t *testing.T) {
	e := newNotifyEnv(t, policy.RoleAdmin)

	// Create defaults to enabled, passes input through, audits and never leaks secret values.
	rec := e.do("POST", "/channels", `{"name":"ops","type":"webhook","config":{"url":"https://hooks.example.test/x"},"secret_id":"`+uuid.NewString()+`"}`)
	if rec.Code != 201 || len(e.svc.gotChan) != 1 || !e.svc.gotChan[0].Enabled || e.svc.gotChan[0].SecretID == nil {
		t.Fatalf("create: %d %+v", rec.Code, e.svc.gotChan)
	}
	if !strings.Contains(rec.Body.String(), `"secret_id"`) || strings.Contains(rec.Body.String(), "password") {
		t.Errorf("unexpected body %s", rec.Body.String())
	}

	// PATCH merges into the stored channel; null clears the secret.
	e.do("PATCH", "/channels/"+e.svc.ch.ID.String(), `{"name":"new","secret_id":null}`)
	got := e.svc.gotChan[1]
	if got.Name != "new" || got.SecretID != nil || !got.Enabled || string(got.Config) != string(e.svc.ch.Config) {
		t.Errorf("patch merge: %+v", got)
	}
	e.do("PATCH", "/rules/"+e.svc.rule.ID.String(), `{"events":["run.drift","run.failed"]}`)
	if r := e.svc.gotRule[0]; r.ChannelID != e.svc.ch.ID || !slices.Equal(r.Events, []string{"run.drift", "run.failed"}) {
		t.Errorf("rule merge: %+v", r)
	}

	// A failed test send is a 200 with ok=false and a sanitized message.
	e.svc.testErr = errors.New("smtp: connection refused")
	rec = e.do("POST", "/channels/"+e.svc.ch.ID.String()+"/test", "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"ok":false`) || !strings.Contains(rec.Body.String(), "connection refused") {
		t.Errorf("test send: %d %s", rec.Code, rec.Body.String())
	}

	// Deliveries filter by status and honour the limit.
	e.svc.delivs = []notify.DeliveryView{{Status: "failed"}, {Status: "sent"}, {Status: "failed"}, {Status: "failed"}}
	rec = e.do("GET", "/deliveries?channel_id="+e.svc.ch.ID.String()+"&status=failed&limit=2", "")
	var out []notify.DeliveryView
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || len(out) != 2 || e.svc.delivLim != 200 {
		t.Errorf("deliveries: %v %d %+v", err, e.svc.delivLim, out)
	}

	// Validation errors map to 422, conflicts to 409.
	e.svc.err = errors.Join(notify.ErrInvalid, errors.New("boom"))
	if rec = e.do("POST", "/rules", `{"channel_id":"`+uuid.NewString()+`","events":["x"]}`); rec.Code != 422 {
		t.Errorf("invalid: %d", rec.Code)
	}
	e.svc.err = notify.ErrConflict
	if rec = e.do("POST", "/channels", `{"name":"ops","type":"webhook"}`); rec.Code != 409 {
		t.Errorf("conflict: %d", rec.Code)
	}

	want := []string{"notification.channel.create", "notification.channel.update", "notification.rule.update", "notification.channel.test"}
	if !slices.Equal(*e.audits, want) {
		t.Errorf("audits %v, want %v", *e.audits, want)
	}
}
