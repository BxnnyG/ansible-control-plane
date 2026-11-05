package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/BxnnyG/ansible-control-plane/internal/auth"
	"github.com/BxnnyG/ansible-control-plane/internal/db/dbgen"
	"github.com/BxnnyG/ansible-control-plane/internal/policy"
	"github.com/BxnnyG/ansible-control-plane/internal/runs"
)

type fakeRuns struct {
	err error
	// run returned by GetRun/Enqueue/CancelRun
	run runs.Run

	previews  []runs.Overrides
	enqueued  []runs.Overrides
	actors    []runs.Actor
	filters   []runs.Filter
	tplInputs []runs.TemplateInput
	cancelled []uuid.UUID
	deleted   []uuid.UUID
}

func (f *fakeRuns) tpl() runs.Template {
	return runs.Template{ID: uuid.New(), Name: "deploy", Playbook: "site.yml", InventoryID: f.run.Params.InventoryID}
}

func (f *fakeRuns) ListTemplates(context.Context, uuid.UUID) ([]runs.Template, error) {
	return []runs.Template{f.tpl()}, f.err
}
func (f *fakeRuns) GetTemplate(context.Context, uuid.UUID, uuid.UUID) (runs.Template, error) {
	return f.tpl(), f.err
}
func (f *fakeRuns) CreateTemplate(_ context.Context, _ uuid.UUID, in runs.TemplateInput) (runs.Template, error) {
	f.tplInputs = append(f.tplInputs, in)
	return f.tpl(), f.err
}
func (f *fakeRuns) UpdateTemplate(_ context.Context, _, _ uuid.UUID, in runs.TemplateInput) (runs.Template, error) {
	f.tplInputs = append(f.tplInputs, in)
	return f.tpl(), f.err
}
func (f *fakeRuns) DeleteTemplate(_ context.Context, _, id uuid.UUID) (int, error) {
	f.deleted = append(f.deleted, id)
	return 2, f.err
}
func (f *fakeRuns) Enqueue(_ context.Context, _, _ uuid.UUID, ov runs.Overrides, trigger string, a runs.Actor, _ *uuid.UUID) (runs.Run, error) {
	f.enqueued = append(f.enqueued, ov)
	f.actors = append(f.actors, a)
	r := f.run
	r.Trigger = trigger
	return r, f.err
}
func (f *fakeRuns) Preview(_ context.Context, _, _ uuid.UUID, ov runs.Overrides, _ runs.Actor) (runs.Preview, error) {
	f.previews = append(f.previews, ov)
	return runs.Preview{HostCount: 2, Hosts: []string{"a", "b"}, AllInScope: true}, f.err
}
func (f *fakeRuns) GetRun(context.Context, uuid.UUID, uuid.UUID) (runs.RunDetail, error) {
	return runs.RunDetail{Run: f.run, HostResults: []runs.HostResult{}}, f.err
}
func (f *fakeRuns) ListRuns(_ context.Context, _ uuid.UUID, fl runs.Filter) (runs.Page, error) {
	f.filters = append(f.filters, fl)
	items := []runs.Run{f.run}
	if fl.Visible != nil {
		items = items[:0]
		if fl.Visible(f.run) {
			items = append(items, f.run)
		}
	}
	return runs.Page{Items: items}, f.err
}
func (f *fakeRuns) CancelRun(_ context.Context, _, id uuid.UUID) (runs.Run, error) {
	f.cancelled = append(f.cancelled, id)
	return f.run, f.err
}

func newFakeRuns() *fakeRuns {
	return &fakeRuns{run: runs.Run{
		ID: uuid.New(), TemplateName: "deploy", Status: runs.StatusQueued,
		Params: runs.Params{InventoryID: uuid.New(), Hosts: []string{"h1.example.test"}},
	}}
}

func runsRouter(role string, fr *fakeRuns, audits *[]string, scopes ...dbgen.MembershipScope) http.Handler {
	uid := uuid.New()
	st := fakeAPIStore{ws: dbgen.Workspace{ID: uuid.New(), Slug: "default", Name: "Default"}, user: uid, role: role, audits: audits, scopes: scopes}
	authn := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(auth.WithPrincipal(r.Context(), auth.Principal{User: dbgen.User{ID: uid}})))
		})
	}
	return Router(quiet, fakePing{}, DefaultStatic(), WithPolicy(policy.New(st, nil, authn, quiet), st), WithRuns(fr))
}

func doRuns(h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequestWithContext(context.Background(), method, "/api/v1/workspaces/default"+path, strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestRunsAPIPermissions(t *testing.T) {
	tid, rid := uuid.NewString(), uuid.NewString()
	create := `{"name":"deploy","project_id":"` + uuid.NewString() + `","inventory_id":"` + uuid.NewString() + `","playbook":"site.yml"}`
	tests := []struct {
		name, role, method, path, body string
		want                           int
		audit                          string
	}{
		{"viewer lists templates", policy.RoleViewer, "GET", "/templates", "", 200, ""},
		{"viewer gets template", policy.RoleViewer, "GET", "/templates/" + tid, "", 200, ""},
		{"viewer lists runs", policy.RoleViewer, "GET", "/runs", "", 200, ""},
		{"viewer gets run", policy.RoleViewer, "GET", "/runs/" + rid, "", 200, ""},
		{"viewer cannot start", policy.RoleViewer, "POST", "/templates/" + tid + "/runs", "", 403, ""},
		{"viewer cannot cancel", policy.RoleViewer, "POST", "/runs/" + rid + "/cancel", "", 403, ""},
		{"viewer cannot create template", policy.RoleViewer, "POST", "/templates", create, 403, ""},
		{"operator cannot create template", policy.RoleOperator, "POST", "/templates", create, 403, ""},
		{"operator cannot update template", policy.RoleOperator, "PUT", "/templates/" + tid, `{"name":"x"}`, 403, ""},
		{"operator cannot delete template", policy.RoleOperator, "DELETE", "/templates/" + tid, "", 403, ""},
		{"operator starts", policy.RoleOperator, "POST", "/templates/" + tid + "/runs", "", 201, "run.start"},
		{"operator starts with overrides", policy.RoleOperator, "POST", "/templates/" + tid + "/runs", `{"limit":"web","check":true}`, 201, "run.start"},
		{"unknown override field", policy.RoleOperator, "POST", "/templates/" + tid + "/runs", `{"bogus":1}`, 400, ""},
		{"operator cancels", policy.RoleOperator, "POST", "/runs/" + rid + "/cancel", "", 200, "run.cancel"},
		{"editor creates template", policy.RoleEditor, "POST", "/templates", create, 201, "template.create"},
		{"unknown template field", policy.RoleEditor, "POST", "/templates", `{"name":"x","bogus":1}`, 400, ""},
		{"editor updates template", policy.RoleEditor, "PUT", "/templates/" + tid, `{"ssh_secret_id":null}`, 200, "template.update"},
		{"empty update", policy.RoleEditor, "PUT", "/templates/" + tid, `{}`, 422, ""},
		{"editor deletes template", policy.RoleEditor, "DELETE", "/templates/" + tid, "", 204, "template.delete"},
		{"bad template id", policy.RoleViewer, "GET", "/templates/nope", "", 404, ""},
		{"bad run id", policy.RoleViewer, "GET", "/runs/nope", "", 404, ""},
		{"viewer cannot preview", policy.RoleViewer, "POST", "/templates/" + tid + "/runs/preview", "", 403, ""},
		{"operator previews without audit", policy.RoleOperator, "POST", "/templates/" + tid + "/runs/preview", `{"limit":"web"}`, 200, ""},
		{"preview unknown field", policy.RoleOperator, "POST", "/templates/" + tid + "/runs/preview", `{"bogus":1}`, 400, ""},
		{"bad preview id", policy.RoleOperator, "POST", "/templates/nope/runs/preview", "", 404, ""},
		{"bad start id", policy.RoleOperator, "POST", "/templates/nope/runs", "", 404, ""},
		{"bad template filter", policy.RoleViewer, "GET", "/runs?template=nope", "", 400, ""},
		{"bad limit", policy.RoleViewer, "GET", "/runs?limit=0", "", 400, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var audits []string
			rec := doRuns(runsRouter(tc.role, newFakeRuns(), &audits), tc.method, tc.path, tc.body)
			if rec.Code != tc.want {
				t.Fatalf("status %d, want %d: %s", rec.Code, tc.want, rec.Body.String())
			}
			if tc.audit != "" && (len(audits) != 1 || audits[0] != tc.audit) {
				t.Errorf("audits %v, want %s", audits, tc.audit)
			}
			if tc.audit == "" && tc.want < 400 && len(audits) != 0 {
				t.Errorf("unexpected audits %v", audits)
			}
		})
	}
}

func TestRunsAPIErrorMapping(t *testing.T) {
	tid := uuid.NewString()
	tests := []struct {
		name string
		err  error
		want int
		code string
	}{
		{"out of scope limit", policy.ErrForbidden, 403, ""},
		{"override not allowed", runs.ErrInvalid, 422, "invalid"},
		{"survey violation", runs.ErrInvalid, 422, "invalid"},
		{"project not synced", runs.ErrNotSynced, 409, "project_not_synced"},
		{"already finished", runs.ErrConflict, 409, "conflict"},
		{"not found", runs.ErrNotFound, 404, "not_found"},
		{"unexpected", context.DeadlineExceeded, 500, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fr := newFakeRuns()
			fr.err = tc.err
			for _, path := range []string{"/runs", "/runs/preview"} { // preview shares start's error mapping
				rec := doRuns(runsRouter(policy.RoleOperator, fr, nil), "POST", "/templates/"+tid+path, `{"limit":"web"}`)
				if rec.Code != tc.want {
					t.Fatalf("%s: status %d, want %d: %s", path, rec.Code, tc.want, rec.Body.String())
				}
				checkErrBody(t, rec, tc.want, tc.code)
			}
		})
	}
}

func checkErrBody(t *testing.T, rec *httptest.ResponseRecorder, want int, code string) {
	t.Helper()
	var e apiError
	_ = json.Unmarshal(rec.Body.Bytes(), &e)
	if e.Code != code {
		t.Errorf("code %q, want %q", e.Code, code)
	}
	if want == 500 && strings.Contains(rec.Body.String(), "deadline") {
		t.Error("internal error leaked")
	}
}

func TestRunsAPIStartPassesOverridesAndActor(t *testing.T) {
	fr := newFakeRuns()
	var audits []string
	body := `{"limit":"web:!db","tags":"a","skip_tags":"b","extra_vars":{"v":"1","pw":"s3cr3t"},"check":true,"diff":false,"verbosity":2}`
	rec := doRuns(runsRouter(policy.RoleOperator, fr, &audits), "POST", "/templates/"+uuid.NewString()+"/runs", body)
	if rec.Code != 201 || len(fr.enqueued) != 1 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	ov := fr.enqueued[0]
	if ov.Limit == nil || *ov.Limit != "web:!db" || ov.Tags == nil || ov.SkipTags == nil || ov.Check == nil || !*ov.Check ||
		ov.Diff == nil || *ov.Diff || ov.Verbosity == nil || *ov.Verbosity != 2 || ov.ExtraVars["v"] != "1" {
		t.Errorf("overrides %+v", ov)
	}
	a := fr.actors[0]
	if a.Principal == nil || a.UserID != a.Principal.User.ID || a.UserID == uuid.Nil {
		t.Errorf("actor %+v", a)
	}
	var out struct {
		Trigger string `json:"trigger"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out.Trigger != runs.TriggerManual {
		t.Errorf("trigger %q", out.Trigger)
	}
}

func TestRunsAPIListFilters(t *testing.T) {
	fr := newFakeRuns()
	tid, uid := uuid.New(), uuid.New()
	rec := doRuns(runsRouter(policy.RoleViewer, fr, nil), "GET",
		"/runs?template="+tid.String()+"&user="+uid.String()+"&status=failed&trigger=schedule&host=h1.example.test&cursor=abc&limit=7", "")
	if rec.Code != 200 || len(fr.filters) != 1 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	f := fr.filters[0]
	if f.TemplateID == nil || *f.TemplateID != tid || f.UserID == nil || *f.UserID != uid || f.Status != "failed" ||
		f.Trigger != "schedule" || f.Host != "h1.example.test" || f.Cursor != "abc" || f.Limit != 7 || f.Visible != nil {
		t.Errorf("filter %+v", f)
	}
}

func TestRunsAPIScoped(t *testing.T) {
	fr := newFakeRuns()
	inScope := dbgen.MembershipScope{Kind: policy.KindInventory, RefID: fr.run.Params.InventoryID.String()}
	outScope := dbgen.MembershipScope{Kind: policy.KindInventory, RefID: uuid.NewString()}
	tid, rid := uuid.NewString(), fr.run.ID.String()
	create := `{"name":"x","project_id":"` + uuid.NewString() + `","inventory_id":"` + uuid.NewString() + `","playbook":"p.yml"}`

	t.Run("scoped editor cannot manage templates", func(t *testing.T) {
		var audits []string
		h := runsRouter(policy.RoleEditor, newFakeRuns(), &audits, inScope)
		for _, c := range []struct{ m, p, b string }{{"POST", "/templates", create}, {"PUT", "/templates/" + tid, `{"name":"y"}`}, {"DELETE", "/templates/" + tid, ""}} {
			if rec := doRuns(h, c.m, c.p, c.b); rec.Code != 403 {
				t.Errorf("%s %s: status %d", c.m, c.p, rec.Code)
			}
		}
	})
	t.Run("in-scope member sees and cancels the run", func(t *testing.T) {
		fr := newFakeRuns()
		sc := dbgen.MembershipScope{Kind: policy.KindInventory, RefID: fr.run.Params.InventoryID.String()}
		h := runsRouter(policy.RoleOperator, fr, nil, sc)
		if rec := doRuns(h, "GET", "/runs/"+fr.run.ID.String(), ""); rec.Code != 200 {
			t.Errorf("get: %d", rec.Code)
		}
		if rec := doRuns(h, "POST", "/runs/"+fr.run.ID.String()+"/cancel", ""); rec.Code != 200 || len(fr.cancelled) != 1 {
			t.Errorf("cancel: %d", rec.Code)
		}
		if rec := doRuns(h, "GET", "/runs", ""); !strings.Contains(rec.Body.String(), fr.run.ID.String()) {
			t.Errorf("list misses run: %s", rec.Body.String())
		}
		if rec := doRuns(h, "GET", "/templates", ""); !strings.Contains(rec.Body.String(), "deploy") {
			t.Errorf("template list: %s", rec.Body.String())
		}
	})
	t.Run("out-of-scope member sees nothing and cannot cancel", func(t *testing.T) {
		fr := newFakeRuns()
		h := runsRouter(policy.RoleOperator, fr, nil, outScope)
		if rec := doRuns(h, "GET", "/runs/"+rid, ""); rec.Code != 404 {
			t.Errorf("get: %d", rec.Code)
		}
		if rec := doRuns(h, "POST", "/runs/"+rid+"/cancel", ""); rec.Code != 404 || len(fr.cancelled) != 0 {
			t.Errorf("cancel: %d cancelled=%v", rec.Code, fr.cancelled)
		}
		rec := doRuns(h, "GET", "/runs", "")
		var page runs.Page
		_ = json.Unmarshal(rec.Body.Bytes(), &page)
		if rec.Code != 200 || len(page.Items) != 0 {
			t.Errorf("list: %d %s", rec.Code, rec.Body.String())
		}
		if rec := doRuns(h, "GET", "/templates/"+tid, ""); rec.Code != 404 {
			t.Errorf("template get: %d", rec.Code)
		}
		if rec := doRuns(h, "GET", "/templates", ""); strings.Contains(rec.Body.String(), "deploy") {
			t.Errorf("template list leaks: %s", rec.Body.String())
		}
	})
}

func TestRunRoutesDeclarePermissions(t *testing.T) {
	uid := uuid.New()
	st := fakeAPIStore{ws: dbgen.Workspace{ID: uuid.New(), Slug: "default"}, user: uid, role: policy.RoleViewer}
	az := policy.New(st, nil, func(next http.Handler) http.Handler { return next }, quiet)
	h := Router(quiet, fakePing{}, DefaultStatic(), WithPolicy(az, st), WithRuns(newFakeRuns()))
	root, ok := h.(chi.Routes)
	if !ok {
		t.Fatal("router does not expose its routes")
	}
	missing, err := az.UndeclaredRoutes(root, "GET /api/v1/me")
	if err != nil || len(missing) != 0 {
		t.Fatalf("undeclared routes: %v %v", missing, err)
	}
	want := map[string]policy.Permission{
		"GET /api/v1/workspaces/{workspace}/templates":                    policy.TemplateRead,
		"POST /api/v1/workspaces/{workspace}/templates":                   policy.TemplateWrite,
		"PUT /api/v1/workspaces/{workspace}/templates/{id}":               policy.TemplateWrite,
		"DELETE /api/v1/workspaces/{workspace}/templates/{id}":            policy.TemplateWrite,
		"POST /api/v1/workspaces/{workspace}/templates/{id}/runs":         policy.RunExecute,
		"POST /api/v1/workspaces/{workspace}/templates/{id}/runs/preview": policy.RunExecute,
		"GET /api/v1/workspaces/{workspace}/runs":                         policy.RunRead,
		"GET /api/v1/workspaces/{workspace}/runs/{id}":                    policy.RunRead,
		"POST /api/v1/workspaces/{workspace}/runs/{id}/cancel":            policy.RunCancel,
	}
	for route, perm := range want {
		method, pattern, _ := strings.Cut(route, " ")
		if got, ok := az.Declared(method, pattern); !ok || got != string(perm) {
			t.Errorf("%s declares %q, want %q", route, got, perm)
		}
	}
}

func TestRunsRoleMatrix(t *testing.T) {
	tests := []struct {
		role  string
		perm  policy.Permission
		allow bool
	}{
		{policy.RoleViewer, policy.TemplateRead, true}, {policy.RoleViewer, policy.RunRead, true},
		{policy.RoleViewer, policy.RunExecute, false}, {policy.RoleViewer, policy.RunCancel, false},
		{policy.RoleViewer, policy.TemplateWrite, false},
		{policy.RoleOperator, policy.RunExecute, true}, {policy.RoleOperator, policy.RunCancel, true},
		{policy.RoleOperator, policy.TemplateWrite, false},
		{policy.RoleEditor, policy.TemplateWrite, true}, {policy.RoleEditor, policy.RunExecute, true},
	}
	for _, tc := range tests {
		if got := policy.Allowed(tc.role, tc.perm); got != tc.allow {
			t.Errorf("%s %s = %v, want %v", tc.role, tc.perm, got, tc.allow)
		}
	}
}
