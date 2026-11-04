package inventory

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/google/uuid"

	"github.com/BxnnyG/ansible-control-plane/internal/policy"
)

func TestScopeResolutionOnStoredInventory(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	inv := e.newInv(t, "scoped")

	mk := func(name string, parents ...uuid.UUID) GroupView {
		t.Helper()
		g, _, err := e.svc.CreateGroup(ctx, e.ws, inv.ID, GroupInput{Name: name, ParentIDs: parents, VarsYAML: "owner: " + name + "\n"}, nil)
		if err != nil {
			t.Fatal(err)
		}
		return g
	}
	top := mk("top")
	mid := mk("mid", top.ID)
	leaf := mk("leaf", mid.ID)
	side := mk("side")
	// leaf has a second parent: side.
	if _, _, err := e.svc.UpdateGroup(ctx, e.ws, inv.ID, leaf.ID, GroupUpdate{ParentIDs: &[]uuid.UUID{mid.ID, side.ID}}, nil); err != nil {
		t.Fatal(err)
	}
	host := func(name string, groups ...uuid.UUID) HostView {
		t.Helper()
		h, _, err := e.svc.CreateHost(ctx, e.ws, inv.ID, HostInput{Name: name, GroupIDs: groups, VarsYAML: "own: " + name + "\n"}, nil)
		if err != nil {
			t.Fatal(err)
		}
		return h
	}
	host("h-top.example.test", top.ID)
	host("h-mid.example.test", mid.ID)
	host("h-leaf.example.test", leaf.ID)
	host("h-side.example.test", side.ID)
	host("h-none.example.test")

	g, err := e.svc.InventoryGraph(ctx, e.ws, inv.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(g.Groups) != 4 || len(g.Hosts) != 5 || len(g.Links) != 3 || len(g.Members) != 4 {
		t.Fatalf("graph: %d groups, %d hosts, %d links, %d members", len(g.Groups), len(g.Hosts), len(g.Links), len(g.Members))
	}
	if _, err := e.svc.InventoryGraph(ctx, e.ws, uuid.New()); !errors.Is(err, policy.ErrNoInventory) {
		t.Errorf("unknown inventory: %v", err)
	}
	if _, err := e.svc.InventoryGraph(ctx, uuid.New(), inv.ID); !errors.Is(err, policy.ErrNoInventory) {
		t.Errorf("inventory of another workspace: %v", err)
	}

	scope := func(g GroupView) []policy.Scope {
		return []policy.Scope{{Kind: policy.KindInventoryGroup, RefID: g.ID.String()}}
	}
	res := policy.InventoryResolver{Source: e.svc}
	wsID := e.ws.String()
	tests := []struct {
		name   string
		scopes []policy.Scope
		hosts  []string
		want   bool
	}{
		{"mid scope covers leaf host", scope(mid), []string{"h-mid.example.test", "h-leaf.example.test"}, true},
		{"mid scope excludes top host", scope(mid), []string{"h-top.example.test"}, false},
		{"mid scope excludes the second parent's own host", scope(mid), []string{"h-side.example.test"}, false},
		{"side scope reaches leaf via second parent", scope(side), []string{"h-side.example.test", "h-leaf.example.test"}, true},
		{"side scope excludes mid host", scope(side), []string{"h-mid.example.test"}, false},
		{"ungrouped host is outside group scopes", scope(top), []string{"h-none.example.test"}, false},
		{"top scope covers the subtree", scope(top), []string{"h-top.example.test", "h-mid.example.test", "h-leaf.example.test"}, true},
		{"inventory scope covers ungrouped", []policy.Scope{{Kind: policy.KindInventory, RefID: inv.ID.String()}}, []string{"h-none.example.test"}, true},
		{"network scope covers none", []policy.Scope{{Kind: policy.KindNetwork, RefID: "n"}}, []string{"h-mid.example.test"}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ok, err := res.InScope(ctx, wsID, tc.scopes, policy.Target{InventoryID: inv.ID.String(), Hosts: tc.hosts})
			if err != nil || ok != tc.want {
				t.Errorf("InScope=%v err=%v, want %v", ok, err, tc.want)
			}
		})
	}

	// Rendering the visible part never discloses variables of groups outside
	// the scope, nor the implicit "all" group.
	if _, _, err := e.svc.UpdateGroup(ctx, e.ws, inv.ID, mustAll(t, e, inv.ID), GroupUpdate{VarsYAML: ptr("everyone: yes\n")}, nil); err != nil {
		t.Fatal(err)
	}
	acc, err := res.Access(ctx, wsID, scope(mid), inv.ID)
	if err != nil {
		t.Fatal(err)
	}
	out, err := e.svc.RenderVisible(ctx, e.ws, inv.ID, false, Visible{Groups: acc.Groups, Hosts: acc.Hosts})
	if err != nil {
		t.Fatal(err)
	}
	hv := out["_meta"].(map[string]any)["hostvars"].(map[string]any)
	if len(hv) != 2 || hv["h-mid.example.test"] == nil || hv["h-leaf.example.test"] == nil {
		t.Fatalf("hostvars hosts: %v", hv)
	}
	leafVars := hv["h-leaf.example.test"].(map[string]any)
	if leafVars["owner"] != "leaf" || leafVars["own"] != "h-leaf.example.test" {
		t.Errorf("leaf vars: %v", leafVars)
	}
	if _, ok := leafVars["everyone"]; ok {
		t.Error("vars of the implicit all group leaked")
	}
	for _, name := range []string{"top", "side"} {
		if _, ok := out[name]; ok {
			t.Errorf("group %s rendered", name)
		}
	}
	if m := out["mid"].(map[string]any); !slices.Equal(m["children"].([]string), []string{"leaf"}) {
		t.Errorf("mid entry: %v", m)
	}
	exp, err := e.svc.RenderVisible(ctx, e.ws, inv.ID, true, Visible{Groups: acc.Groups, Hosts: acc.Hosts})
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := exp["all"].(map[string]any)["vars"]; v != nil {
		t.Errorf("export leaked all vars: %v", v)
	}
	if _, ok := exp["top"]; ok {
		t.Error("export rendered an out-of-scope group")
	}
}

// mustAll stores the implicit "all" group (only its vars are kept) and returns its id.
func mustAll(t *testing.T, e *env, inv uuid.UUID) uuid.UUID {
	t.Helper()
	g, _, err := e.svc.CreateGroup(context.Background(), e.ws, inv, GroupInput{Name: GroupAll}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return g.ID
}
