package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/BxnnyG/ansible-control-plane/internal/auth"
	"github.com/BxnnyG/ansible-control-plane/internal/db/dbgen"
	"github.com/BxnnyG/ansible-control-plane/internal/grouprules"
	"github.com/BxnnyG/ansible-control-plane/internal/inventory"
	"github.com/BxnnyG/ansible-control-plane/internal/policy"
	"github.com/BxnnyG/ansible-control-plane/internal/projects"
)

type fakeGroupRules struct {
	rule    grouprules.Rule
	err     error
	created []grouprules.Input
	updated []grouprules.Update
	applied []grouprules.ApplyInput
	deleted int
	result  inventory.GroupChangeResult
}

func (f *fakeGroupRules) List(context.Context, uuid.UUID) ([]grouprules.Rule, error) {
	other := f.rule
	other.ID, other.Name, other.InventoryID = uuid.New(), "other", uuid.New()
	return []grouprules.Rule{f.rule, other}, f.err
}
func (f *fakeGroupRules) Get(_ context.Context, _, id uuid.UUID) (grouprules.Rule, error) {
	if f.err != nil {
		return grouprules.Rule{}, f.err
	}
	if id != f.rule.ID {
		return grouprules.Rule{}, grouprules.ErrNotFound
	}
	return f.rule, nil
}
func (f *fakeGroupRules) Create(_ context.Context, _ uuid.UUID, in grouprules.Input, _ uuid.UUID) (grouprules.Rule, error) {
	f.created = append(f.created, in)
	return f.rule, f.err
}
func (f *fakeGroupRules) Update(_ context.Context, _, _ uuid.UUID, in grouprules.Update) (grouprules.Rule, error) {
	f.updated = append(f.updated, in)
	return f.rule, f.err
}
func (f *fakeGroupRules) Delete(context.Context, uuid.UUID, uuid.UUID) error {
	f.deleted++
	return f.err
}
func (f *fakeGroupRules) Preview(context.Context, uuid.UUID, uuid.UUID) (grouprules.Preview, error) {
	return grouprules.Preview{Rule: f.rule, InventoryMode: "db", Plan: grouprules.Plan{Add: []grouprules.AddItem{{Name: "a.lan", CreateHost: true}}, Remove: []string{}, Untrack: []string{}, Skipped: []grouprules.Skipped{}}}, f.err
}
func (f *fakeGroupRules) Apply(_ context.Context, _, _ uuid.UUID, in grouprules.ApplyInput) (grouprules.Applied, error) {
	f.applied = append(f.applied, in)
	return grouprules.Applied{Rule: f.rule, InventoryMode: "db", Result: f.result}, f.err
}

type ruleEnv struct {
	h      http.Handler
	rules  *fakeGroupRules
	inv    *scopeInv
	audits *[]string
}

// newRuleEnv builds the group rule API for a membership with the given role
// and scopes. Network scopes resolve to 192.0.2.0/24 with one fleet host that
// is linked to the inventory host h-leaf.
func newRuleEnv(t *testing.T, role string, scopes ...dbgen.MembershipScope) *ruleEnv {
	t.Helper()
	uid := uuid.New()
	audits := []string{}
	st := fakeAPIStore{ws: dbgen.Workspace{ID: uuid.New(), Slug: "default", Name: "Default"}, user: uid, role: role, scopes: scopes, audits: &audits}
	authn := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(auth.WithPrincipal(r.Context(), auth.Principal{User: dbgen.User{ID: uid, DisplayName: "Ann", Email: "ann@example.com"}})))
		})
	}
	inv := newScopeInv()
	netID := uuid.New()
	for i := range scopes {
		if scopes[i].Kind == policy.KindNetwork {
			scopes[i].RefID = netID.String()
		}
	}
	ns := netSource{prefixes: map[uuid.UUID]netip.Prefix{netID: netip.MustParsePrefix("192.0.2.0/24")},
		hosts: []policy.FleetHost{{ID: uuid.New(), Names: []string{"h-leaf"}, IPs: []string{"192.0.2.5"}}}}
	az := policy.New(st, policy.InventoryResolver{Source: inv, Networks: ns}, authn, quiet)
	rules := &fakeGroupRules{rule: grouprules.Rule{ID: uuid.New(), Name: "lan", Kind: "subnet", Params: json.RawMessage(`{"cidr":"192.0.2.0/24"}`),
		InventoryID: inv.inv.ID, Group: "lan", Mode: "add-only", Enabled: true}}
	rules.result = inventory.GroupChangeResult{Added: []string{"a.lan"}, HostsCreated: []string{"a.lan"}, Removed: []string{}, Version: 7}
	h := Router(quiet, fakePing{}, DefaultStatic(), WithPolicy(az, st), WithInventories(inv), WithGroupRules(rules))
	return &ruleEnv{h: h, rules: rules, inv: inv, audits: &audits}
}

func (e *ruleEnv) do(method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequestWithContext(context.Background(), method, "/api/v1/workspaces/default/group-rules"+path, strings.NewReader(body))
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	return rec
}

func TestGroupRulesAuthorization(t *testing.T) {
	invScope := func() []dbgen.MembershipScope { return scopeOf(policy.KindInventory, "") }
	tests := []struct {
		name   string
		role   string
		scopes func() []dbgen.MembershipScope
		method string
		path   string // {id} is replaced
		body   string
		want   int
	}{
		{"viewer lists", policy.RoleViewer, nil, "GET", "", "", 200},
		{"viewer gets", policy.RoleViewer, nil, "GET", "/{id}", "", 200},
		{"viewer previews", policy.RoleViewer, nil, "GET", "/{id}/preview", "", 200},
		{"viewer previews via POST", policy.RoleViewer, nil, "POST", "/{id}/preview", "", 200},
		{"viewer cannot create", policy.RoleViewer, nil, "POST", "", `{"name":"x"}`, 403},
		{"viewer cannot update", policy.RoleViewer, nil, "PUT", "/{id}", `{"enabled":false}`, 403},
		{"viewer cannot delete", policy.RoleViewer, nil, "DELETE", "/{id}", "", 403},
		{"viewer cannot apply", policy.RoleViewer, nil, "POST", "/{id}/apply", "", 403},
		{"operator cannot apply", policy.RoleOperator, nil, "POST", "/{id}/apply", "", 403},
		{"editor creates", policy.RoleEditor, nil, "POST", "", `{"name":"x","kind":"subnet","params":{"cidr":"192.0.2.0/24"},"inventory_id":"` + uuid.Nil.String() + `","group":"g"}`, 201},
		{"editor updates", policy.RoleEditor, nil, "PUT", "/{id}", `{"enabled":false}`, 200},
		{"editor empty update", policy.RoleEditor, nil, "PUT", "/{id}", `{}`, 422},
		{"editor deletes", policy.RoleEditor, nil, "DELETE", "/{id}", "", 204},
		{"editor applies", policy.RoleEditor, nil, "POST", "/{id}/apply", "", 200},
		{"editor unknown rule", policy.RoleEditor, nil, "POST", "/" + uuid.NewString() + "/apply", "", 404},
		{"malformed body", policy.RoleEditor, nil, "POST", "", `{"nope":1}`, 400},
		{"inventory-scoped editor reads", policy.RoleEditor, invScope, "GET", "/{id}", "", 200},
		{"inventory-scoped editor cannot preview", policy.RoleEditor, invScope, "GET", "/{id}/preview", "", 403},
		{"inventory-scoped editor cannot apply", policy.RoleEditor, invScope, "POST", "/{id}/apply", "", 403},
		{"inventory-scoped editor cannot create", policy.RoleEditor, invScope, "POST", "", `{"name":"x"}`, 403},
		{"inventory-scoped editor cannot update", policy.RoleEditor, invScope, "PUT", "/{id}", `{"enabled":false}`, 403},
		{"inventory-scoped editor cannot delete", policy.RoleEditor, invScope, "DELETE", "/{id}", "", 403},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var scopes []dbgen.MembershipScope
			if tt.scopes != nil {
				scopes = tt.scopes()
			}
			e := newRuleEnv(t, tt.role, scopes...)
			for i := range scopes { // inventory scope names the rule's inventory
				if scopes[i].Kind == policy.KindInventory {
					scopes[i].RefID = e.inv.inv.ID.String()
				}
			}
			body := tt.body
			if strings.Contains(body, uuid.Nil.String()) {
				body = strings.ReplaceAll(body, uuid.Nil.String(), e.inv.inv.ID.String())
			}
			rec := e.do(tt.method, strings.ReplaceAll(tt.path, "{id}", e.rules.rule.ID.String()), body)
			if rec.Code != tt.want {
				t.Fatalf("status %d, want %d: %s", rec.Code, tt.want, rec.Body.String())
			}
			if tt.want == 403 && (len(e.rules.created)+len(e.rules.updated)+len(e.rules.applied)+e.rules.deleted) != 0 {
				t.Error("service reached despite 403")
			}
		})
	}
}

func TestGroupRulesAuditAndApplyResponse(t *testing.T) {
	e := newRuleEnv(t, policy.RoleEditor)
	id := e.rules.rule.ID.String()
	rec := e.do("POST", "/"+id+"/apply", "")
	if rec.Code != 200 || rec.Header().Get("ETag") != `"7"` {
		t.Fatalf("apply: %d etag %q: %s", rec.Code, rec.Header().Get("ETag"), rec.Body.String())
	}
	var out struct {
		Result struct {
			Added   []string `json:"added"`
			Removed []string `json:"removed"`
		} `json:"result"`
	}
	if json.Unmarshal(rec.Body.Bytes(), &out) != nil || !slices.Equal(out.Result.Added, []string{"a.lan"}) || out.Result.Removed == nil {
		t.Errorf("apply body: %s", rec.Body.String())
	}
	if len(e.rules.applied) != 1 || e.rules.applied[0].Author != (projects.Author{Name: "Ann", Email: "ann@example.com"}) || e.rules.applied[0].IfMatch != nil {
		t.Errorf("apply input: %+v", e.rules.applied)
	}
	e.do("POST", "", `{"name":"x","kind":"subnet","params":{"cidr":"192.0.2.0/24"},"inventory_id":"`+e.inv.inv.ID.String()+`","group":"g"}`)
	e.do("PUT", "/"+id, `{"enabled":false}`)
	e.do("DELETE", "/"+id, "")
	e.do("GET", "/"+id+"/preview", "")
	want := []string{"group_rule.apply", "group_rule.create", "group_rule.update", "group_rule.delete"}
	for _, w := range want {
		if !slices.Contains(*e.audits, w) {
			t.Errorf("audit %s missing: %v", w, *e.audits)
		}
	}
	if slices.Contains(*e.audits, "group_rule.preview") {
		t.Error("preview must not be audited")
	}
}

func TestGroupRulesIfMatchAndErrors(t *testing.T) {
	e := newRuleEnv(t, policy.RoleEditor)
	id := e.rules.rule.ID.String()
	req := httptest.NewRequestWithContext(context.Background(), "POST", "/api/v1/workspaces/default/group-rules/"+id+"/apply", nil)
	req.Header.Set("If-Match", `"4"`)
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	if rec.Code != 200 || e.rules.applied[0].IfMatch == nil || *e.rules.applied[0].IfMatch != 4 {
		t.Fatalf("if-match: %d %+v", rec.Code, e.rules.applied)
	}
	tests := []struct {
		err  error
		want int
		code string
	}{
		{grouprules.ErrDisabled, 409, "rule_disabled"},
		{grouprules.ErrRuleChanged, 409, "rule_changed"},
		{grouprules.ErrConflict, 409, "conflict"},
		{inventory.ErrPrecondition, 412, "version_conflict"},
		{&projects.ConflictError{Paths: []string{"inv/hosts.yml"}}, 409, "git_conflict"},
		{inventory.ErrNotGitInventory, 422, "invalid"},
	}
	for _, tt := range tests {
		e.rules.err = tt.err
		rec := e.do("POST", "/"+id+"/apply", "")
		if rec.Code != tt.want || !strings.Contains(rec.Body.String(), `"`+tt.code+`"`) {
			t.Errorf("%v: %d %s", tt.err, rec.Code, rec.Body.String())
		}
		if tt.code == "git_conflict" && !strings.Contains(rec.Body.String(), "inv/hosts.yml") {
			t.Errorf("conflict paths missing: %s", rec.Body.String())
		}
	}
	e.rules.err = nil
	if rec := e.do("POST", "/"+id+"/apply", ""); rec.Code != 200 {
		t.Errorf("recovery: %d", rec.Code)
	}
}

// Rules of inventories a scoped member cannot see stay hidden.
func TestGroupRulesListIsFilteredByInventoryAccess(t *testing.T) {
	e := newRuleEnv(t, policy.RoleViewer, dbgen.MembershipScope{Kind: policy.KindNetwork})
	rec := e.do("GET", "", "")
	var list []grouprules.Rule
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &list) != nil {
		t.Fatalf("list: %d %s", rec.Code, rec.Body.String())
	}
	if len(list) != 1 || list[0].ID != e.rules.rule.ID {
		t.Errorf("a network-scoped member must see only rules of inventories holding granted hosts: %+v", list)
	}
	if rec := e.do("GET", "/"+e.rules.rule.ID.String()+"/preview", ""); rec.Code != 403 {
		t.Errorf("scoped preview: %d", rec.Code)
	}
}
