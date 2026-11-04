package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/BxnnyG/ansible-control-plane/internal/auth"
	"github.com/BxnnyG/ansible-control-plane/internal/db/dbgen"
	"github.com/BxnnyG/ansible-control-plane/internal/inventory"
	"github.com/BxnnyG/ansible-control-plane/internal/policy"
	"github.com/BxnnyG/ansible-control-plane/internal/projects"
)

type fakeInventories struct {
	err     error
	created []inventory.CreateInput
	matches []*int
	imports []inventory.ImportInput
	commits []inventory.CommitInput
	noop    bool
	groups  []inventory.GroupUpdate
}

func (f *fakeInventories) inv() inventory.Inventory {
	return inventory.Inventory{ID: uuid.New(), Name: "lab", Slug: "lab", Mode: "db", Version: 4}
}

func (f *fakeInventories) Create(_ context.Context, _ uuid.UUID, in inventory.CreateInput) (inventory.Inventory, error) {
	f.created = append(f.created, in)
	return f.inv(), f.err
}

func (f *fakeInventories) List(context.Context, uuid.UUID) ([]inventory.Inventory, error) {
	return []inventory.Inventory{f.inv()}, f.err
}

func (f *fakeInventories) Get(context.Context, uuid.UUID, uuid.UUID) (inventory.Inventory, error) {
	return f.inv(), f.err
}

func (f *fakeInventories) Update(_ context.Context, _, _ uuid.UUID, _ inventory.UpdateInput, m *int) (inventory.Inventory, error) {
	f.matches = append(f.matches, m)
	return f.inv(), f.err
}

func (f *fakeInventories) Delete(context.Context, uuid.UUID, uuid.UUID) error { return f.err }

func (f *fakeInventories) Import(_ context.Context, _, _ uuid.UUID, in inventory.ImportInput, m *int) (inventory.ImportResult, error) {
	f.imports = append(f.imports, in)
	f.matches = append(f.matches, m)
	return inventory.ImportResult{Inventory: f.inv(), SHA: "abc", Warnings: []string{}}, f.err
}

func (f *fakeInventories) Commit(_ context.Context, _, _ uuid.UUID, in inventory.CommitInput, m *int) (inventory.CommitResult, error) {
	f.commits = append(f.commits, in)
	f.matches = append(f.matches, m)
	return inventory.CommitResult{Inventory: f.inv(), Branch: "acp/lab", SHA: "abc", Paths: []string{"hosts.yml"}, NoOp: f.noop}, f.err
}

func (f *fakeInventories) Diff(context.Context, uuid.UUID, uuid.UUID) (inventory.DiffResult, error) {
	return inventory.DiffResult{Paths: []string{}}, f.err
}

func (f *fakeInventories) Render(_ context.Context, _, _ uuid.UUID, export bool) (map[string]any, error) {
	return map[string]any{"_meta": map[string]any{"hostvars": map[string]any{}}, "export": export}, f.err
}

func (f *fakeInventories) RenderVisible(_ context.Context, _, _ uuid.UUID, _ bool, _ inventory.Visible) (map[string]any, error) {
	return map[string]any{"visible": true}, f.err
}

func (f *fakeInventories) ListGroups(context.Context, uuid.UUID, uuid.UUID) ([]inventory.GroupView, error) {
	return []inventory.GroupView{{ID: uuid.New(), Name: "web"}}, f.err
}

func (f *fakeInventories) GetGroup(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (inventory.GroupView, error) {
	return inventory.GroupView{ID: uuid.New(), Name: "web"}, f.err
}

func (f *fakeInventories) CreateGroup(_ context.Context, _, _ uuid.UUID, in inventory.GroupInput, m *int) (inventory.GroupView, int, error) {
	f.matches = append(f.matches, m)
	return inventory.GroupView{ID: uuid.New(), Name: in.Name}, 5, f.err
}

func (f *fakeInventories) UpdateGroup(_ context.Context, _, _, _ uuid.UUID, in inventory.GroupUpdate, _ *int) (inventory.GroupView, int, error) {
	f.groups = append(f.groups, in)
	return inventory.GroupView{ID: uuid.New(), Name: "web"}, 5, f.err
}

func (f *fakeInventories) DeleteGroup(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, *int) (int, error) {
	return 5, f.err
}

func (f *fakeInventories) ListHosts(context.Context, uuid.UUID, uuid.UUID) ([]inventory.HostView, error) {
	return []inventory.HostView{{ID: uuid.New(), Name: "h1.example.test"}}, f.err
}

func (f *fakeInventories) GetHost(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (inventory.HostView, error) {
	return inventory.HostView{ID: uuid.New(), Name: "h1.example.test"}, f.err
}

func (f *fakeInventories) CreateHost(_ context.Context, _, _ uuid.UUID, in inventory.HostInput, _ *int) (inventory.HostView, int, error) {
	return inventory.HostView{ID: uuid.New(), Name: in.Name}, 5, f.err
}

func (f *fakeInventories) UpdateHost(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, inventory.HostUpdate, *int) (inventory.HostView, int, error) {
	return inventory.HostView{ID: uuid.New(), Name: "h1.example.test"}, 5, f.err
}

func (f *fakeInventories) DeleteHost(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, *int) (int, error) {
	return 5, f.err
}

func (f *fakeInventories) AddMember(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, uuid.UUID, *int) (int, error) {
	return 5, f.err
}

func (f *fakeInventories) RemoveMember(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, uuid.UUID, *int) (int, error) {
	return 5, f.err
}

func inventoriesRouter(role string, fi *fakeInventories, audits *[]string) http.Handler {
	uid := uuid.New()
	st := fakeAPIStore{ws: dbgen.Workspace{ID: uuid.New(), Slug: "default", Name: "Default"}, user: uid, role: role, audits: audits}
	authn := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(auth.WithPrincipal(r.Context(), auth.Principal{User: dbgen.User{ID: uid}})))
		})
	}
	return Router(quiet, fakePing{}, DefaultStatic(), WithPolicy(policy.New(st, nil, authn, quiet), st), WithInventories(fi))
}

func TestInventoriesAPIPermissions(t *testing.T) {
	id, gid, hid := uuid.NewString(), uuid.NewString(), uuid.NewString()
	inv := "/inventories/" + id
	tests := []struct {
		name, role, method, path, body string
		want                           int
		audit                          string
	}{
		{"viewer lists", policy.RoleViewer, "GET", "/inventories", "", 200, ""},
		{"viewer gets", policy.RoleViewer, "GET", inv, "", 200, ""},
		{"viewer renders", policy.RoleViewer, "GET", inv + "/render", "", 200, ""},
		{"viewer lists groups", policy.RoleViewer, "GET", inv + "/groups", "", 200, ""},
		{"viewer gets group", policy.RoleViewer, "GET", inv + "/groups/" + gid, "", 200, ""},
		{"viewer lists hosts", policy.RoleViewer, "GET", inv + "/hosts", "", 200, ""},
		{"viewer gets host", policy.RoleViewer, "GET", inv + "/hosts/" + hid, "", 200, ""},
		{"viewer cannot create", policy.RoleViewer, "POST", "/inventories", `{"name":"x","mode":"db"}`, 403, ""},
		{"operator cannot create", policy.RoleOperator, "POST", "/inventories", `{"name":"x","mode":"db"}`, 403, ""},
		{"editor creates", policy.RoleEditor, "POST", "/inventories", `{"name":"x","mode":"db"}`, 201, "inventory.create"},
		{"unknown field", policy.RoleEditor, "POST", "/inventories", `{"name":"x","bogus":1}`, 400, ""},
		{"editor updates", policy.RoleEditor, "PUT", inv, `{"name":"y"}`, 200, "inventory.update"},
		{"empty update", policy.RoleEditor, "PUT", inv, `{}`, 422, ""},
		{"operator cannot update", policy.RoleOperator, "PUT", inv, `{"name":"y"}`, 403, ""},
		{"editor deletes", policy.RoleEditor, "DELETE", inv, "", 204, "inventory.delete"},
		{"viewer cannot delete", policy.RoleViewer, "DELETE", inv, "", 403, ""},
		{"editor imports", policy.RoleEditor, "POST", inv + "/import", `{"path":"hosts.ini"}`, 200, "inventory.import"},
		{"editor imports without body", policy.RoleEditor, "POST", inv + "/import", ``, 200, "inventory.import"},
		{"operator cannot import", policy.RoleOperator, "POST", inv + "/import", `{}`, 403, ""},
		{"editor creates group", policy.RoleEditor, "POST", inv + "/groups", `{"name":"web"}`, 201, "inventory.group_create"},
		{"viewer cannot create group", policy.RoleViewer, "POST", inv + "/groups", `{"name":"web"}`, 403, ""},
		{"editor updates group", policy.RoleEditor, "PUT", inv + "/groups/" + gid, `{"vars_yaml":"a: 1"}`, 200, "inventory.group_update"},
		{"empty group update", policy.RoleEditor, "PUT", inv + "/groups/" + gid, `{}`, 422, ""},
		{"editor deletes group", policy.RoleEditor, "DELETE", inv + "/groups/" + gid, "", 204, "inventory.group_delete"},
		{"editor creates host", policy.RoleEditor, "POST", inv + "/hosts", `{"name":"h1.example.test"}`, 201, "inventory.host_create"},
		{"operator cannot create host", policy.RoleOperator, "POST", inv + "/hosts", `{"name":"h"}`, 403, ""},
		{"editor updates host", policy.RoleEditor, "PUT", inv + "/hosts/" + hid, `{"vars_yaml":"a: 1"}`, 200, "inventory.host_update"},
		{"editor deletes host", policy.RoleEditor, "DELETE", inv + "/hosts/" + hid, "", 204, "inventory.host_delete"},
		{"editor adds member", policy.RoleEditor, "PUT", inv + "/groups/" + gid + "/hosts/" + hid, "", 204, "inventory.member_add"},
		{"editor commits", policy.RoleEditor, "POST", inv + "/commit", `{"message":"m"}`, 200, "inventory.commit"},
		{"editor commits without body", policy.RoleEditor, "POST", inv + "/commit", ``, 200, "inventory.commit"},
		{"operator cannot commit", policy.RoleOperator, "POST", inv + "/commit", `{}`, 403, ""},
		{"viewer diffs", policy.RoleViewer, "GET", inv + "/diff", "", 200, ""},
		{"editor removes member", policy.RoleEditor, "DELETE", inv + "/groups/" + gid + "/hosts/" + hid, "", 204, "inventory.member_remove"},
		{"viewer cannot add member", policy.RoleViewer, "PUT", inv + "/groups/" + gid + "/hosts/" + hid, "", 403, ""},
		{"bad inventory id", policy.RoleViewer, "GET", "/inventories/nope", "", 404, ""},
		{"bad group id", policy.RoleViewer, "GET", inv + "/groups/nope", "", 404, ""},
		{"bad host id", policy.RoleEditor, "DELETE", inv + "/hosts/nope", "", 404, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var audits []string
			h := inventoriesRouter(tc.role, &fakeInventories{}, &audits)
			req := httptest.NewRequestWithContext(context.Background(), tc.method, "/api/v1/workspaces/default"+tc.path, strings.NewReader(tc.body))
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
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

func TestInventoriesAPIErrorMapping(t *testing.T) {
	id := uuid.NewString()
	tests := []struct {
		err  error
		want int
		code string
	}{
		{inventory.ErrNotFound, 404, "not_found"},
		{inventory.ErrConflict, 409, "conflict"},
		{inventory.ErrPrecondition, 412, "version_conflict"},
		{projects.ErrNotFound, 409, "project_not_synced"},
		{inventory.ErrVaultInINI, 422, "vault_requires_yaml"},
		{&projects.ConflictError{Paths: []string{"hosts.yml"}, Reason: "x"}, 409, "git_conflict"},
		{&projects.RemoteError{Msg: "boom"}, 502, "remote_error"},
		{context.DeadlineExceeded, 500, ""},
	}
	for _, tc := range tests {
		var audits []string
		h := inventoriesRouter(policy.RoleAdmin, &fakeInventories{err: tc.err}, &audits)
		req := httptest.NewRequestWithContext(context.Background(), "POST", "/api/v1/workspaces/default/inventories/"+id+"/import", strings.NewReader("{}"))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != tc.want || (tc.code != "" && !strings.Contains(rec.Body.String(), `"code":"`+tc.code+`"`)) {
			t.Errorf("%v: status %d body %s, want %d %s", tc.err, rec.Code, rec.Body.String(), tc.want, tc.code)
		}
		if len(audits) != 0 {
			t.Errorf("%v: failed request wrote audit %v", tc.err, audits)
		}
	}
}

func TestInventoriesAPIIfMatchAndETag(t *testing.T) {
	id := uuid.NewString()
	for _, tc := range []struct {
		header  string
		want    int
		version *int
	}{
		{`"7"`, 200, ptrInt(7)}, {`W/"7"`, 200, ptrInt(7)}, {`7`, 200, ptrInt(7)},
		{"*", 200, nil}, {"", 200, nil}, {`"abc"`, 400, nil}, {`"-1"`, 400, nil},
	} {
		fi := &fakeInventories{}
		h := inventoriesRouter(policy.RoleEditor, fi, new([]string))
		req := httptest.NewRequestWithContext(context.Background(), "PUT", "/api/v1/workspaces/default/inventories/"+id, strings.NewReader(`{"name":"n"}`))
		if tc.header != "" {
			req.Header.Set("If-Match", tc.header)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != tc.want {
			t.Errorf("If-Match %q: status %d, want %d", tc.header, rec.Code, tc.want)
			continue
		}
		if tc.want == 200 {
			if len(fi.matches) != 1 || (fi.matches[0] == nil) != (tc.version == nil) || (tc.version != nil && *fi.matches[0] != *tc.version) {
				t.Errorf("If-Match %q: service got %v", tc.header, fi.matches)
			}
			if rec.Header().Get("ETag") != `"4"` {
				t.Errorf("ETag=%q", rec.Header().Get("ETag"))
			}
		}
	}
}

func ptrInt(i int) *int { return &i }

func TestInventoriesAPIRenderAndImportBodies(t *testing.T) {
	id := uuid.NewString()
	fi := &fakeInventories{}
	h := inventoriesRouter(policy.RoleEditor, fi, new([]string))
	do := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequestWithContext(context.Background(), method, "/api/v1/workspaces/default"+path, strings.NewReader(body))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	if rec := do("GET", "/inventories/"+id+"/render?export=true", ""); !strings.Contains(rec.Body.String(), `"export":true`) {
		t.Errorf("export flag not forwarded: %s", rec.Body.String())
	}
	if rec := do("GET", "/inventories/"+id+"/render", ""); !strings.Contains(rec.Body.String(), `"export":false`) {
		t.Errorf("render default: %s", rec.Body.String())
	}
	pid := uuid.New()
	do("POST", "/inventories/"+id+"/import", `{"project_id":"`+pid.String()+`","path":"a/hosts.ini","format":"ini"}`)
	if len(fi.imports) != 1 || fi.imports[0].ProjectID == nil || *fi.imports[0].ProjectID != pid || fi.imports[0].Path != "a/hosts.ini" || fi.imports[0].Format != "ini" {
		t.Errorf("import input: %+v", fi.imports)
	}
	do("PUT", "/inventories/"+id+"/groups/"+uuid.NewString(), `{"parent_ids":[]}`)
	if len(fi.groups) != 1 || fi.groups[0].ParentIDs == nil || len(*fi.groups[0].ParentIDs) != 0 {
		t.Errorf("empty parent_ids must clear the parents, got %+v", fi.groups)
	}
}

func TestInventoryRoutesDeclarePermissions(t *testing.T) {
	uid := uuid.New()
	st := fakeAPIStore{ws: dbgen.Workspace{ID: uuid.New(), Slug: "default"}, user: uid, role: policy.RoleViewer}
	az := policy.New(st, nil, func(next http.Handler) http.Handler { return next }, quiet)
	h := Router(quiet, fakePing{}, DefaultStatic(), WithPolicy(az, st), WithInventories(&fakeInventories{}))
	root, ok := h.(chi.Routes)
	if !ok {
		t.Fatal("router does not expose its routes")
	}
	missing, err := az.UndeclaredRoutes(root, "GET /api/v1/me")
	if err != nil {
		t.Fatal(err)
	}
	if len(missing) != 0 {
		t.Errorf("routes without a declared permission: %v", missing)
	}
	count := 0
	_ = chi.Walk(root, func(_, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if strings.HasPrefix(route, "/api/v1/workspaces/{workspace}/inventories") {
			count++
		}
		return nil
	})
	if count != 21 {
		t.Errorf("inventory routes registered: %d, want 21", count)
	}
}

func TestInventoryCommitNoOpIsNotAudited(t *testing.T) {
	var audits []string
	h := inventoriesRouter(policy.RoleEditor, &fakeInventories{noop: true}, &audits)
	req := httptest.NewRequestWithContext(context.Background(), "POST", "/api/v1/workspaces/default/inventories/"+uuid.NewString()+"/commit", strings.NewReader("{}"))
	req.Header.Set("If-Match", `"4"`)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"no_op":true`) || len(audits) != 0 {
		t.Fatalf("status %d audits %v body %s", rec.Code, audits, rec.Body.String())
	}
}
