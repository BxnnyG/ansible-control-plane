package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/BxnnyG/ansible-control-plane/internal/auth"
	"github.com/BxnnyG/ansible-control-plane/internal/db/dbgen"
	"github.com/BxnnyG/ansible-control-plane/internal/inventory"
	"github.com/BxnnyG/ansible-control-plane/internal/policy"
)

// scopeInv is a small in-memory inventory:
//
//	top -> mid -> leaf        other
//
// hosts: h-leaf (leaf), h-other (other), h-both (leaf and other), h-mid (mid).
// It records the mutations that reach the service.
type scopeInv struct {
	*fakeInventories
	inv     inventory.Inventory
	groups  map[string]inventory.GroupView
	hosts   map[string]inventory.HostView
	visible *inventory.Visible

	createdGroup []inventory.GroupInput
	createdHost  []inventory.HostInput
	updatedGroup []inventory.GroupUpdate
	updatedHost  []inventory.HostUpdate
	calls        []string
}

func newScopeInv() *scopeInv {
	s := &scopeInv{fakeInventories: &fakeInventories{}, groups: map[string]inventory.GroupView{}, hosts: map[string]inventory.HostView{}}
	s.inv = inventory.Inventory{ID: uuid.New(), Name: "lab", Slug: "lab", Mode: "git", Path: "inv/hosts.ini", WriteBranch: "acp/lab", Hosts: 4, Groups: 4, Version: 3}
	for _, n := range []string{"top", "mid", "leaf", "other"} {
		s.groups[n] = inventory.GroupView{ID: uuid.New(), Name: n, ParentIDs: []uuid.UUID{}, VarsYAML: "secret_of: " + n + "\n"}
	}
	setParent := func(child, parent string) {
		g := s.groups[child]
		g.ParentIDs = append(g.ParentIDs, s.groups[parent].ID)
		s.groups[child] = g
	}
	setParent("mid", "top")
	setParent("leaf", "mid")
	host := func(n string, groups ...string) {
		h := inventory.HostView{ID: uuid.New(), Name: n, GroupIDs: []uuid.UUID{}}
		for _, g := range groups {
			h.GroupIDs = append(h.GroupIDs, s.groups[g].ID)
		}
		s.hosts[n] = h
	}
	host("h-leaf", "leaf")
	host("h-other", "other")
	host("h-both", "leaf", "other")
	host("h-mid", "mid")
	return s
}

func (s *scopeInv) gid(n string) string { return s.groups[n].ID.String() }
func (s *scopeInv) hid(n string) string { return s.hosts[n].ID.String() }

func (s *scopeInv) InventoryGraph(_ context.Context, _, inv uuid.UUID) (policy.Graph, error) {
	if inv != s.inv.ID {
		return policy.Graph{}, policy.ErrNoInventory
	}
	g := policy.Graph{Groups: map[uuid.UUID]string{}, Hosts: map[uuid.UUID]string{}}
	for _, v := range s.groups {
		g.Groups[v.ID] = v.Name
		for _, p := range v.ParentIDs {
			g.Links = append(g.Links, policy.GraphLink{Parent: p, Child: v.ID})
		}
	}
	for _, v := range s.hosts {
		g.Hosts[v.ID] = v.Name
		for _, gr := range v.GroupIDs {
			g.Members = append(g.Members, policy.GraphMember{Group: gr, Host: v.ID})
		}
	}
	return g, nil
}

func (s *scopeInv) List(context.Context, uuid.UUID) ([]inventory.Inventory, error) {
	other := s.inv
	other.ID = uuid.New()
	return []inventory.Inventory{s.inv, other}, nil
}

func (s *scopeInv) Get(context.Context, uuid.UUID, uuid.UUID) (inventory.Inventory, error) {
	return s.inv, nil
}

func (s *scopeInv) ListGroups(context.Context, uuid.UUID, uuid.UUID) ([]inventory.GroupView, error) {
	var out []inventory.GroupView
	for _, g := range s.groups {
		out = append(out, g)
	}
	return out, nil
}

func (s *scopeInv) GetGroup(_ context.Context, _, _, id uuid.UUID) (inventory.GroupView, error) {
	for _, g := range s.groups {
		if g.ID == id {
			return g, nil
		}
	}
	return inventory.GroupView{}, inventory.ErrNotFound
}

func (s *scopeInv) ListHosts(context.Context, uuid.UUID, uuid.UUID) ([]inventory.HostView, error) {
	var out []inventory.HostView
	for _, h := range s.hosts {
		out = append(out, h)
	}
	return out, nil
}

func (s *scopeInv) GetHost(_ context.Context, _, _, id uuid.UUID) (inventory.HostView, error) {
	for _, h := range s.hosts {
		if h.ID == id {
			return h, nil
		}
	}
	return inventory.HostView{}, inventory.ErrNotFound
}

func (s *scopeInv) CreateGroup(_ context.Context, _, _ uuid.UUID, in inventory.GroupInput, _ *int) (inventory.GroupView, int, error) {
	s.createdGroup = append(s.createdGroup, in)
	return inventory.GroupView{ID: uuid.New(), Name: in.Name, ParentIDs: in.ParentIDs}, 4, nil
}

func (s *scopeInv) UpdateGroup(_ context.Context, _, _, id uuid.UUID, in inventory.GroupUpdate, _ *int) (inventory.GroupView, int, error) {
	s.updatedGroup = append(s.updatedGroup, in)
	g, _ := s.GetGroup(context.Background(), uuid.Nil, uuid.Nil, id)
	if in.ParentIDs != nil {
		g.ParentIDs = *in.ParentIDs
	}
	return g, 4, nil
}

func (s *scopeInv) CreateHost(_ context.Context, _, _ uuid.UUID, in inventory.HostInput, _ *int) (inventory.HostView, int, error) {
	s.createdHost = append(s.createdHost, in)
	return inventory.HostView{ID: uuid.New(), Name: in.Name, GroupIDs: in.GroupIDs}, 4, nil
}

func (s *scopeInv) UpdateHost(_ context.Context, _, _, id uuid.UUID, in inventory.HostUpdate, _ *int) (inventory.HostView, int, error) {
	s.updatedHost = append(s.updatedHost, in)
	h, _ := s.GetHost(context.Background(), uuid.Nil, uuid.Nil, id)
	if in.GroupIDs != nil {
		h.GroupIDs = *in.GroupIDs
	}
	return h, 4, nil
}

func (s *scopeInv) DeleteGroup(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, *int) (int, error) {
	s.calls = append(s.calls, "delete-group")
	return 4, nil
}

func (s *scopeInv) DeleteHost(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, *int) (int, error) {
	s.calls = append(s.calls, "delete-host")
	return 4, nil
}

func (s *scopeInv) AddMember(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, uuid.UUID, *int) (int, error) {
	s.calls = append(s.calls, "add-member")
	return 4, nil
}

func (s *scopeInv) RemoveMember(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, uuid.UUID, *int) (int, error) {
	s.calls = append(s.calls, "remove-member")
	return 4, nil
}

func (s *scopeInv) Update(context.Context, uuid.UUID, uuid.UUID, inventory.UpdateInput, *int) (inventory.Inventory, error) {
	s.calls = append(s.calls, "update-inventory")
	return s.inv, nil
}

func (s *scopeInv) Import(context.Context, uuid.UUID, uuid.UUID, inventory.ImportInput, *int) (inventory.ImportResult, error) {
	s.calls = append(s.calls, "import")
	return inventory.ImportResult{Inventory: s.inv, Warnings: []string{}}, nil
}

func (s *scopeInv) RenderVisible(_ context.Context, _, _ uuid.UUID, _ bool, v inventory.Visible) (map[string]any, error) {
	s.visible = &v
	return map[string]any{"visible": true}, nil
}

// scopedRouter serves the inventories API for an operator-level editor whose
// membership carries the given scopes.
func scopedRouter(role string, scopes []dbgen.MembershipScope, svc *scopeInv, audits *[]string) http.Handler {
	uid := uuid.New()
	st := fakeAPIStore{ws: dbgen.Workspace{ID: uuid.New(), Slug: "default", Name: "Default"}, user: uid, role: role, scopes: scopes, audits: audits}
	authn := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(auth.WithPrincipal(r.Context(), auth.Principal{User: dbgen.User{ID: uid}})))
		})
	}
	az := policy.New(st, policy.InventoryResolver{Source: svc}, authn, quiet)
	return Router(quiet, fakePing{}, DefaultStatic(), WithPolicy(az, st), WithInventories(svc))
}

func scopeOf(kind, ref string) []dbgen.MembershipScope {
	return []dbgen.MembershipScope{{Kind: kind, RefID: ref}}
}

type scopeCase struct {
	name, method, path, body string
	want                     int
	deniedAudit              bool
}

func runScopeCases(t *testing.T, role string, scopes func(*scopeInv) []dbgen.MembershipScope, cases func(*scopeInv) []scopeCase) {
	t.Helper()
	probe := newScopeInv()
	for _, tc := range cases(probe) {
		t.Run(tc.name, func(t *testing.T) {
			svc := newScopeInv()
			// Rebuild the cases against this fixture so ids match.
			var c scopeCase
			for _, x := range cases(svc) {
				if x.name == tc.name {
					c = x
				}
			}
			var audits []string
			h := scopedRouter(role, scopes(svc), svc, &audits)
			req := httptest.NewRequestWithContext(context.Background(), c.method, "/api/v1/workspaces/default/inventories/"+svc.inv.ID.String()+c.path, strings.NewReader(c.body))
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != c.want {
				t.Fatalf("status %d, want %d: %s", rec.Code, c.want, rec.Body.String())
			}
			denied := slices.Contains(audits, "authz.denied")
			if c.deniedAudit != denied {
				t.Errorf("authz.denied audited=%v, want %v (%v)", denied, c.deniedAudit, audits)
			}
			if c.want == http.StatusForbidden && len(svc.calls)+len(svc.createdGroup)+len(svc.createdHost)+len(svc.updatedGroup)+len(svc.updatedHost) != 0 {
				t.Errorf("denied request reached the service: %+v", svc)
			}
		})
	}
}

func TestInventoriesScopedGroupMutations(t *testing.T) {
	midScope := func(s *scopeInv) []dbgen.MembershipScope { return scopeOf(policy.KindInventoryGroup, s.gid("mid")) }
	runScopeCases(t, policy.RoleEditor, midScope, func(s *scopeInv) []scopeCase {
		return []scopeCase{
			{"get group in scope", "GET", "/groups/" + s.gid("leaf"), "", 200, false},
			{"get scope root", "GET", "/groups/" + s.gid("mid"), "", 200, false},
			{"get group above scope", "GET", "/groups/" + s.gid("top"), "", 403, true},
			{"get unrelated group", "GET", "/groups/" + s.gid("other"), "", 403, true},
			{"get host in scope", "GET", "/hosts/" + s.hid("h-leaf"), "", 200, false},
			{"get host outside", "GET", "/hosts/" + s.hid("h-other"), "", 403, true},
			{"create group below scope", "POST", "/groups", `{"name":"n","parent_ids":["` + s.gid("leaf") + `"]}`, 201, false},
			{"create top-level group", "POST", "/groups", `{"name":"n"}`, 403, true},
			{"create group under outside parent", "POST", "/groups", `{"name":"n","parent_ids":["` + s.gid("other") + `"]}`, 403, true},
			{"create group under mixed parents", "POST", "/groups", `{"name":"n","parent_ids":["` + s.gid("leaf") + `","` + s.gid("top") + `"]}`, 403, true},
			{"update vars of group in scope", "PUT", "/groups/" + s.gid("leaf"), `{"vars_yaml":"a: 1"}`, 200, false},
			{"update vars of outside group", "PUT", "/groups/" + s.gid("top"), `{"vars_yaml":"a: 1"}`, 403, true},
			{"move group under outside parent", "PUT", "/groups/" + s.gid("leaf"), `{"parent_ids":["` + s.gid("other") + `"]}`, 403, true},
			{"move group within scope", "PUT", "/groups/" + s.gid("leaf"), `{"parent_ids":["` + s.gid("mid") + `"]}`, 200, false},
			{"delete group in scope", "DELETE", "/groups/" + s.gid("leaf"), "", 204, false},
			{"delete scope root detaches outside parent", "DELETE", "/groups/" + s.gid("mid"), "", 403, true},
			{"delete outside group", "DELETE", "/groups/" + s.gid("other"), "", 403, true},
			{"create host in scope", "POST", "/hosts", `{"name":"n1","group_ids":["` + s.gid("leaf") + `"]}`, 201, false},
			{"create ungrouped host", "POST", "/hosts", `{"name":"n1"}`, 403, true},
			{"create host in outside group", "POST", "/hosts", `{"name":"n1","group_ids":["` + s.gid("other") + `"]}`, 403, true},
			{"update host vars", "PUT", "/hosts/" + s.hid("h-both"), `{"vars_yaml":"a: 1"}`, 200, false},
			{"update outside host", "PUT", "/hosts/" + s.hid("h-other"), `{"vars_yaml":"a: 1"}`, 403, true},
			{"move host into outside group", "PUT", "/hosts/" + s.hid("h-leaf"), `{"group_ids":["` + s.gid("other") + `"]}`, 403, true},
			{"move host within scope", "PUT", "/hosts/" + s.hid("h-leaf"), `{"group_ids":["` + s.gid("mid") + `"]}`, 200, false},
			{"rename host shared with outside group", "PUT", "/hosts/" + s.hid("h-both"), `{"name":"x"}`, 403, true},
			{"rename host fully in scope", "PUT", "/hosts/" + s.hid("h-leaf"), `{"name":"x"}`, 200, false},
			{"delete host fully in scope", "DELETE", "/hosts/" + s.hid("h-leaf"), "", 204, false},
			{"delete host also in outside group", "DELETE", "/hosts/" + s.hid("h-both"), "", 403, true},
			{"delete outside host", "DELETE", "/hosts/" + s.hid("h-other"), "", 403, true},
			{"add in-scope host to in-scope group", "PUT", "/groups/" + s.gid("mid") + "/hosts/" + s.hid("h-leaf"), "", 204, false},
			{"pull outside host into scope", "PUT", "/groups/" + s.gid("mid") + "/hosts/" + s.hid("h-other"), "", 403, true},
			{"add host to outside group", "PUT", "/groups/" + s.gid("other") + "/hosts/" + s.hid("h-leaf"), "", 403, true},
			{"remove member in scope", "DELETE", "/groups/" + s.gid("leaf") + "/hosts/" + s.hid("h-leaf"), "", 204, false},
			{"remove member of outside group", "DELETE", "/groups/" + s.gid("other") + "/hosts/" + s.hid("h-both"), "", 403, true},
			{"update inventory settings", "PUT", "", `{"name":"y"}`, 403, true},
			{"delete inventory", "DELETE", "", "", 403, true},
			{"import inventory", "POST", "/import", `{}`, 403, true},
			{"render", "GET", "/render", "", 200, false},
			{"get inventory", "GET", "", "", 200, false},
		}
	})
}

func TestInventoriesScopeKinds(t *testing.T) {
	cases := func(s *scopeInv) []scopeCase {
		return []scopeCase{
			{"get inventory", "GET", "", "", 0, false},
			{"list groups", "GET", "/groups", "", 0, false},
			{"render", "GET", "/render", "", 0, false},
		}
	}
	tests := []struct {
		name   string
		scopes func(*scopeInv) []dbgen.MembershipScope
		want   int
		audit  bool
	}{
		{"network scope only sees nothing", func(*scopeInv) []dbgen.MembershipScope { return scopeOf(policy.KindNetwork, "net-1") }, 403, true},
		{"other inventory scope", func(*scopeInv) []dbgen.MembershipScope { return scopeOf(policy.KindInventory, uuid.NewString()) }, 403, true},
		{"deleted group scope", func(*scopeInv) []dbgen.MembershipScope {
			return scopeOf(policy.KindInventoryGroup, uuid.NewString())
		}, 403, true},
		{"inventory scope", func(s *scopeInv) []dbgen.MembershipScope { return scopeOf(policy.KindInventory, s.inv.ID.String()) }, 200, false},
		{"group scope", func(s *scopeInv) []dbgen.MembershipScope { return scopeOf(policy.KindInventoryGroup, s.gid("leaf")) }, 200, false},
	}
	for _, tt := range tests {
		for _, c := range cases(newScopeInv()) {
			t.Run(tt.name+"/"+c.name, func(t *testing.T) {
				svc := newScopeInv()
				var audits []string
				h := scopedRouter(policy.RoleViewer, tt.scopes(svc), svc, &audits)
				req := httptest.NewRequestWithContext(context.Background(), c.method, "/api/v1/workspaces/default/inventories/"+svc.inv.ID.String()+c.path, nil)
				rec := httptest.NewRecorder()
				h.ServeHTTP(rec, req)
				if rec.Code != tt.want || slices.Contains(audits, "authz.denied") != tt.audit {
					t.Errorf("status %d audits %v, want %d denied=%v", rec.Code, audits, tt.want, tt.audit)
				}
			})
		}
	}
}

func TestInventoriesScopedReadsAreFiltered(t *testing.T) {
	svc := newScopeInv()
	h := scopedRouter(policy.RoleViewer, scopeOf(policy.KindInventoryGroup, svc.gid("mid")), svc, new([]string))
	get := func(path string, v any) {
		t.Helper()
		req := httptest.NewRequestWithContext(context.Background(), "GET", "/api/v1/workspaces/default/inventories"+path, nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("%s: status %d: %s", path, rec.Code, rec.Body.String())
		}
		if err := json.Unmarshal(rec.Body.Bytes(), v); err != nil {
			t.Fatal(err)
		}
	}
	base := "/" + svc.inv.ID.String()

	var invs []inventory.Inventory
	get("", &invs)
	if len(invs) != 1 || invs[0].ID != svc.inv.ID {
		t.Fatalf("list must contain only the scoped inventory: %+v", invs)
	}
	if invs[0].Hosts != 3 || invs[0].Groups != 2 || invs[0].Path != "" || invs[0].WriteBranch != "" {
		t.Errorf("inventory must be narrowed: %+v", invs[0])
	}

	var groups []inventory.GroupView
	get(base+"/groups", &groups)
	names := []string{}
	for _, g := range groups {
		names = append(names, g.Name)
		for _, p := range g.ParentIDs {
			if p == svc.groups["top"].ID {
				t.Errorf("group %s reveals its out-of-scope parent", g.Name)
			}
		}
	}
	slices.Sort(names)
	if !slices.Equal(names, []string{"leaf", "mid"}) {
		t.Errorf("groups %v", names)
	}

	var hosts []inventory.HostView
	get(base+"/hosts", &hosts)
	hn := []string{}
	for _, x := range hosts {
		hn = append(hn, x.Name)
		if x.Name == "h-both" && (len(x.GroupIDs) != 1 || x.GroupIDs[0] != svc.groups["leaf"].ID) {
			t.Errorf("h-both must only list in-scope groups: %v", x.GroupIDs)
		}
	}
	slices.Sort(hn)
	if !slices.Equal(hn, []string{"h-both", "h-leaf", "h-mid"}) {
		t.Errorf("hosts %v", hn)
	}

	var out map[string]any
	get(base+"/render", &out)
	if svc.visible == nil || len(svc.visible.Groups) != 2 || len(svc.visible.Hosts) != 3 || svc.visible.Groups[svc.groups["top"].ID] {
		t.Errorf("render must be restricted to the visible set: %+v", svc.visible)
	}
}

func TestInventoriesScopedMergesOutsideMemberships(t *testing.T) {
	svc := newScopeInv()
	h := scopedRouter(policy.RoleEditor, scopeOf(policy.KindInventoryGroup, svc.gid("mid")), svc, new([]string))
	do := func(method, path, body string) {
		t.Helper()
		req := httptest.NewRequestWithContext(context.Background(), method, "/api/v1/workspaces/default/inventories/"+svc.inv.ID.String()+path, strings.NewReader(body))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("%s %s: %d %s", method, path, rec.Code, rec.Body.String())
		}
	}
	// h-both is in leaf (in scope) and other (outside). The client sees only
	// leaf and moves the host to mid: the outside membership must survive.
	do("PUT", "/hosts/"+svc.hid("h-both"), `{"group_ids":["`+svc.gid("mid")+`"]}`)
	if len(svc.updatedHost) != 1 || svc.updatedHost[0].GroupIDs == nil {
		t.Fatalf("update not forwarded: %+v", svc.updatedHost)
	}
	got := *svc.updatedHost[0].GroupIDs
	if len(got) != 2 || !slices.Contains(got, svc.groups["other"].ID) || !slices.Contains(got, svc.groups["mid"].ID) {
		t.Errorf("merged groups %v, want other + mid", got)
	}
	// The scope root keeps its out-of-scope parent when the client sends an
	// empty (filtered) parent list.
	do("PUT", "/groups/"+svc.gid("mid"), `{"parent_ids":[]}`)
	if len(svc.updatedGroup) != 1 || svc.updatedGroup[0].ParentIDs == nil || !slices.Equal(*svc.updatedGroup[0].ParentIDs, []uuid.UUID{svc.groups["top"].ID}) {
		t.Errorf("scope root lost its parent: %+v", svc.updatedGroup)
	}
}

// Commit and diff write or reveal the whole inventory file, so a membership
// scoped to a group must be refused while a workspace-wide editor succeeds.
func TestInventoryCommitAndDiffRequireWholeInventory(t *testing.T) {
	tests := []struct {
		name       string
		scopes     func(*scopeInv) []dbgen.MembershipScope
		want       int
		wantDenied bool
	}{
		{"group-scoped editor", func(s *scopeInv) []dbgen.MembershipScope { return scopeOf(policy.KindInventoryGroup, s.gid("mid")) }, 403, true},
		{"workspace-wide editor", func(*scopeInv) []dbgen.MembershipScope { return nil }, 200, false},
	}
	calls := []struct{ method, path, body string }{
		{"POST", "/commit", `{"message":"update"}`},
		{"GET", "/diff", ""},
	}
	for _, tc := range tests {
		for _, c := range calls {
			t.Run(tc.name+" "+c.method+c.path, func(t *testing.T) {
				svc := newScopeInv()
				var audits []string
				h := scopedRouter(policy.RoleEditor, tc.scopes(svc), svc, &audits)
				req := httptest.NewRequestWithContext(context.Background(), c.method,
					"/api/v1/workspaces/default/inventories/"+svc.inv.ID.String()+c.path, strings.NewReader(c.body))
				rec := httptest.NewRecorder()
				h.ServeHTTP(rec, req)
				if rec.Code != tc.want {
					t.Fatalf("status %d, want %d: %s", rec.Code, tc.want, rec.Body.String())
				}
				if denied := slices.Contains(audits, "authz.denied"); denied != tc.wantDenied {
					t.Errorf("authz.denied audited=%v, want %v (%v)", denied, tc.wantDenied, audits)
				}
				if tc.want == http.StatusForbidden && len(svc.commits) != 0 {
					t.Errorf("denied commit reached the service")
				}
				if tc.want == http.StatusOK && c.method == "POST" && len(svc.commits) != 1 {
					t.Errorf("commit did not reach the service")
				}
			})
		}
	}
}
