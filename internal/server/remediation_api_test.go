package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/BxnnyG/ansible-control-plane/internal/auth"
	"github.com/BxnnyG/ansible-control-plane/internal/db/dbgen"
	"github.com/BxnnyG/ansible-control-plane/internal/fleet"
	"github.com/BxnnyG/ansible-control-plane/internal/policy"
	"github.com/BxnnyG/ansible-control-plane/internal/remediation"
	"github.com/BxnnyG/ansible-control-plane/internal/runs"
)

type fakeRemediation struct {
	err      error
	remCalls []uuid.UUID
	skip     []bool
	vis      []bool
	bulk     []remediation.BulkInput
}

func (f *fakeRemediation) List(context.Context, uuid.UUID) ([]remediation.Rule, error) {
	return []remediation.Rule{}, f.err
}
func (f *fakeRemediation) Get(context.Context, uuid.UUID, uuid.UUID) (remediation.Rule, error) {
	return remediation.Rule{Name: "r"}, f.err
}
func (f *fakeRemediation) Create(_ context.Context, _ uuid.UUID, in remediation.Input, _ uuid.UUID) (remediation.Rule, error) {
	return remediation.Rule{ID: uuid.New(), Name: in.Name}, f.err
}
func (f *fakeRemediation) Update(_ context.Context, _, id uuid.UUID, in remediation.Input, _ uuid.UUID) (remediation.Rule, error) {
	return remediation.Rule{ID: id, Name: in.Name}, f.err
}
func (f *fakeRemediation) Delete(context.Context, uuid.UUID, uuid.UUID) error { return f.err }
func (f *fakeRemediation) Preview(context.Context, uuid.UUID, remediation.Criteria) ([]fleet.AlertView, error) {
	return []fleet.AlertView{}, f.err
}
func (f *fakeRemediation) Applicable(context.Context, uuid.UUID, []fleet.AlertView) (map[uuid.UUID][]remediation.RuleRef, error) {
	return map[uuid.UUID][]remediation.RuleRef{}, f.err
}
func (f *fakeRemediation) Remediate(_ context.Context, _, alertID, ruleID uuid.UUID, skip bool, _ runs.Actor, v fleet.Visible) (remediation.Result, error) {
	f.remCalls = append(f.remCalls, ruleID)
	f.skip = append(f.skip, skip)
	f.vis = append(f.vis, v != nil)
	return remediation.Result{Run: runs.Run{ID: uuid.New()}, Phase: remediation.PhaseApply}, f.err
}
func (f *fakeRemediation) BulkRun(_ context.Context, _ uuid.UUID, in remediation.BulkInput, _ runs.Actor, v fleet.Visible) (remediation.Result, error) {
	f.bulk = append(f.bulk, in)
	f.vis = append(f.vis, v != nil)
	return remediation.Result{Run: runs.Run{ID: uuid.New()}}, f.err
}

type remEnv struct {
	h      http.Handler
	az     *policy.Authorizer
	rem    *fakeRemediation
	audits *[]string
}

func newRemEnv(t *testing.T, role string, scopes ...dbgen.MembershipScope) *remEnv {
	t.Helper()
	uid := uuid.New()
	audits := []string{}
	st := fakeAPIStore{ws: dbgen.Workspace{ID: uuid.New(), Slug: "default", Name: "Default"}, user: uid, role: role, scopes: scopes, audits: &audits}
	authn := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(auth.WithPrincipal(r.Context(), auth.Principal{User: dbgen.User{ID: uid}})))
		})
	}
	alertID := uuid.MustParse("00000000-0000-0000-0000-0000000000a1")
	hostID := uuid.New()
	ff := &fakeFleet{hosts: []fleet.HostView{{ID: hostID, Name: "host-a", IPs: []string{"192.0.2.10"}}},
		alerts: []fleet.AlertView{{ID: alertID, HostID: &hostID, Severity: "critical"}}}
	rem := &fakeRemediation{}
	az := policy.New(st, policy.InventoryResolver{Networks: policy.NetworkSource(fakeNetSource{})}, authn, quiet)
	h := Router(quiet, fakePing{}, DefaultStatic(), WithPolicy(az, st), WithFleet(ff), WithRemediation(rem))
	return &remEnv{h: h, az: az, rem: rem, audits: &audits}
}

func (e *remEnv) do(method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequestWithContext(context.Background(), method, "/api/v1/workspaces/default"+path, strings.NewReader(body))
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	return rec
}

func TestRemediationRoutesDeclarePermissions(t *testing.T) {
	e := newRemEnv(t, policy.RoleOwner)
	root, ok := e.h.(chi.Routes)
	if !ok {
		t.Fatal("router does not expose its routes")
	}
	missing, err := e.az.UndeclaredRoutes(root, "GET /api/v1/me")
	if err != nil {
		t.Fatal(err)
	}
	if len(missing) != 0 {
		t.Errorf("routes without a declared permission: %v", missing)
	}
}

func TestRemediationAuthorization(t *testing.T) {
	alert := "/alerts/00000000-0000-0000-0000-0000000000a1"
	rule := uuid.NewString()
	const in = `{"name":"r","target":"host","template_id":"` + "00000000-0000-0000-0000-000000000001" + `"}`
	type call struct{ method, path, body string }
	reads := []call{{"GET", "/remediation-rules", ""}, {"GET", "/remediation-rules/" + rule, ""}}
	manage := []call{{"POST", "/remediation-rules", in}, {"PUT", "/remediation-rules/" + rule, in}, {"DELETE", "/remediation-rules/" + rule, ""},
		{"POST", "/remediation-rules/preview", `{"service_regex":"x"}`}}
	fix := call{"POST", alert + "/remediate", `{"rule_id":"` + rule + `"}`}
	bulk := call{"POST", "/fleet/bulk-run", `{"host_ids":["` + uuid.NewString() + `"],"template_id":"` + rule + `"}`}
	open := []call{{"GET", "/remediation-rules/applicable", ""}, {"GET", alert + "/remediations", ""}}

	for _, role := range []string{policy.RoleViewer, policy.RoleOperator, policy.RoleEditor, policy.RoleAdmin, policy.RoleOwner} {
		editor := role == policy.RoleEditor || role == policy.RoleAdmin || role == policy.RoleOwner
		operator := editor || role == policy.RoleOperator
		for _, c := range append(open, reads...) {
			if rec := newRemEnv(t, role).do(c.method, c.path, c.body); rec.Code != http.StatusOK {
				t.Errorf("%s %s %s: %d", role, c.method, c.path, rec.Code)
			}
		}
		for _, c := range manage {
			if denied := newRemEnv(t, role).do(c.method, c.path, c.body).Code == http.StatusForbidden; denied == editor {
				t.Errorf("%s %s %s: denied=%v", role, c.method, c.path, denied)
			}
		}
		for _, c := range []call{fix, bulk} {
			if denied := newRemEnv(t, role).do(c.method, c.path, c.body).Code == http.StatusForbidden; denied == operator {
				t.Errorf("%s %s %s: denied=%v", role, c.method, c.path, denied)
			}
		}
	}

	// A scoped editor may not manage rules (they act on the whole workspace) and
	// cannot even read them; it can still fix visible alerts, with a visibility filter.
	sc := netScope("00000000-0000-0000-0000-00000000000a")
	for _, c := range append(manage, reads...) {
		e := newRemEnv(t, policy.RoleEditor, sc)
		if rec := e.do(c.method, c.path, c.body); rec.Code != http.StatusForbidden {
			t.Errorf("scoped %s %s: %d", c.method, c.path, rec.Code)
		}
	}
	e := newRemEnv(t, policy.RoleOperator, sc)
	if rec := e.do(fix.method, fix.path, fix.body); rec.Code != http.StatusCreated || len(e.rem.vis) != 1 || !e.rem.vis[0] {
		t.Errorf("scoped fix: %d vis %v", rec.Code, e.rem.vis)
	}
	if rec := e.do(bulk.method, bulk.path, bulk.body); rec.Code != http.StatusCreated || !e.rem.vis[1] {
		t.Errorf("scoped bulk: %d vis %v", rec.Code, e.rem.vis)
	}
}

func TestRemediationActionsAndErrors(t *testing.T) {
	alert := "/alerts/00000000-0000-0000-0000-0000000000a1/remediate"
	rule := uuid.NewString()
	e := newRemEnv(t, policy.RoleOperator)
	rec := e.do("POST", alert, `{"rule_id":"`+rule+`","skip_check":true}`)
	if rec.Code != http.StatusCreated || len(e.rem.remCalls) != 1 || e.rem.remCalls[0].String() != rule || !e.rem.skip[0] || e.rem.vis[0] {
		t.Fatalf("fix: %d %s", rec.Code, rec.Body.String())
	}
	if len(*e.audits) != 1 || !strings.Contains((*e.audits)[0], "remediation.run") {
		t.Errorf("audits: %v", *e.audits)
	}
	for _, tt := range []struct {
		name, path, body string
		err              error
		want             int
	}{
		{"malformed", alert, `{"rule_id":`, nil, http.StatusBadRequest},
		{"no rule", alert, `{}`, nil, http.StatusBadRequest},
		{"unknown field", alert, `{"rule_id":"` + rule + `","x":1}`, nil, http.StatusBadRequest},
		{"bad alert id", "/alerts/nope/remediate", `{"rule_id":"` + rule + `"}`, nil, http.StatusNotFound},
		{"forbidden", alert, `{"rule_id":"` + rule + `"}`, policy.ErrForbidden, http.StatusForbidden},
		{"disabled", alert, `{"rule_id":"` + rule + `"}`, remediation.ErrDisabled, http.StatusConflict},
		{"not applicable", alert, `{"rule_id":"` + rule + `"}`, remediation.ErrNotApplicable, http.StatusConflict},
		{"alert gone", alert, `{"rule_id":"` + rule + `"}`, remediation.ErrAlertNotFound, http.StatusNotFound},
		{"invalid", alert, `{"rule_id":"` + rule + `"}`, remediation.ErrInvalid, http.StatusUnprocessableEntity},
		{"bulk no template", "/fleet/bulk-run", `{"host_ids":[]}`, nil, http.StatusBadRequest},
		{"bulk not in inventory", "/fleet/bulk-run", `{"host_ids":["` + rule + `"],"template_id":"` + rule + `"}`, &remediation.NotInInventoryError{Hosts: []string{"h1"}}, http.StatusUnprocessableEntity},
		{"bulk forbidden", "/fleet/bulk-run", `{"host_ids":["` + rule + `"],"template_id":"` + rule + `"}`, errors.Join(policy.ErrForbidden), http.StatusForbidden},
	} {
		e := newRemEnv(t, policy.RoleOperator)
		e.rem.err = tt.err
		rec := e.do("POST", tt.path, tt.body)
		if rec.Code != tt.want {
			t.Errorf("%s: status %d, want %d (%s)", tt.name, rec.Code, tt.want, rec.Body.String())
		}
		if tt.name == "bulk not in inventory" && !strings.Contains(rec.Body.String(), `"hosts":["h1"]`) {
			t.Errorf("422 body lacks the host list: %s", rec.Body.String())
		}
		if tt.want >= 400 && len(*e.audits) != 0 {
			t.Errorf("%s: failed action was audited: %v", tt.name, *e.audits)
		}
	}
}
