package policy

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"sort"
	"testing"

	"github.com/google/uuid"

	"github.com/BxnnyG/ansible-control-plane/internal/auth"
	"github.com/BxnnyG/ansible-control-plane/internal/db/dbgen"
)

// testGraph builds an inventory graph from names:
//
//	root -> mid -> leaf, root -> other, side -> leaf (leaf has two parents),
//	cyc1 <-> cyc2 (cycle), solo (no links)
//
// Hosts: h-root(root) h-mid(mid) h-leaf(leaf) h-other(other) h-side(side)
// h-cyc(cyc2) h-solo(solo) h-none(no group), h-both(leaf and other).
type testGraph struct {
	g      Graph
	groups map[string]uuid.UUID
	hosts  map[string]uuid.UUID
}

func buildGraph() testGraph {
	t := testGraph{g: Graph{Groups: map[uuid.UUID]string{}, Hosts: map[uuid.UUID]string{}}, groups: map[string]uuid.UUID{}, hosts: map[string]uuid.UUID{}}
	for _, n := range []string{"root", "mid", "leaf", "other", "side", "cyc1", "cyc2", "solo"} {
		id := uuid.New()
		t.groups[n] = id
		t.g.Groups[id] = n
	}
	link := func(p, c string) {
		t.g.Links = append(t.g.Links, GraphLink{Parent: t.groups[p], Child: t.groups[c]})
	}
	link("root", "mid")
	link("mid", "leaf")
	link("root", "other")
	link("side", "leaf")
	link("cyc1", "cyc2")
	link("cyc2", "cyc1")
	host := func(name string, groups ...string) {
		id := uuid.New()
		t.hosts[name] = id
		t.g.Hosts[id] = name
		for _, g := range groups {
			t.g.Members = append(t.g.Members, GraphMember{Group: t.groups[g], Host: id})
		}
	}
	host("h-root", "root")
	host("h-mid", "mid")
	host("h-leaf", "leaf")
	host("h-other", "other")
	host("h-side", "side")
	host("h-cyc", "cyc2")
	host("h-solo", "solo")
	host("h-none")
	host("h-both", "leaf", "other")
	return t
}

func (tg testGraph) scopes(inv uuid.UUID, specs ...string) []Scope {
	var out []Scope
	for _, s := range specs {
		switch s {
		case "inv":
			out = append(out, Scope{KindInventory, inv.String()})
		case "net":
			out = append(out, Scope{KindNetwork, "net-1"})
		case "foreign":
			out = append(out, Scope{KindInventoryGroup, uuid.NewString()})
		case "garbage":
			out = append(out, Scope{KindInventoryGroup, "not-a-uuid"})
		default:
			out = append(out, Scope{KindInventoryGroup, tg.groups[s].String()})
		}
	}
	return out
}

func (tg testGraph) hostNames(a Access) []string {
	var out []string
	for id := range a.Hosts {
		out = append(out, tg.g.Hosts[id])
	}
	sort.Strings(out)
	return out
}

func (tg testGraph) groupNames(a Access) []string {
	var out []string
	for id := range a.Groups {
		out = append(out, tg.g.Groups[id])
	}
	sort.Strings(out)
	return out
}

func TestComputeAccess(t *testing.T) {
	tg := buildGraph()
	inv := uuid.New()
	tests := []struct {
		name       string
		scopes     []string
		all        bool
		wantGroups []string
		wantHosts  []string
	}{
		{"no scopes grants nothing", nil, false, nil, nil},
		{"inventory scope is everything", []string{"inv"}, true, nil, nil},
		{"inventory plus group is still everything", []string{"inv", "leaf"}, true, nil, nil},
		{"network scope without a network source matches nothing", []string{"net"}, false, nil, nil},
		{"leaf group", []string{"leaf"}, false, []string{"leaf"}, []string{"h-both", "h-leaf"}},
		{"nested: mid includes leaf", []string{"mid"}, false, []string{"leaf", "mid"}, []string{"h-both", "h-leaf", "h-mid"}},
		{"root includes all descendants", []string{"root"}, false, []string{"leaf", "mid", "other", "root"},
			[]string{"h-both", "h-leaf", "h-mid", "h-other", "h-root"}},
		{"multi-parent: side reaches leaf", []string{"side"}, false, []string{"leaf", "side"}, []string{"h-both", "h-leaf", "h-side"}},
		{"two scopes union", []string{"mid", "other"}, false, []string{"leaf", "mid", "other"},
			[]string{"h-both", "h-leaf", "h-mid", "h-other"}},
		{"cycle terminates", []string{"cyc1"}, false, []string{"cyc1", "cyc2"}, []string{"h-cyc"}},
		{"mixed group and network", []string{"net", "solo"}, false, []string{"solo"}, []string{"h-solo"}},
		{"group of another inventory ignored", []string{"foreign"}, false, nil, nil},
		{"unparseable group ref ignored", []string{"garbage"}, false, nil, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a := ComputeAccess(tg.scopes(inv, tc.scopes...), inv, tg.g)
			if a.All != tc.all {
				t.Fatalf("All=%v want %v", a.All, tc.all)
			}
			if a.None() != (!tc.all && tc.wantGroups == nil) {
				t.Errorf("None()=%v", a.None())
			}
			if got := tg.groupNames(a); !slices.Equal(got, tc.wantGroups) {
				t.Errorf("groups %v want %v", got, tc.wantGroups)
			}
			if got := tg.hostNames(a); !slices.Equal(got, tc.wantHosts) {
				t.Errorf("hosts %v want %v", got, tc.wantHosts)
			}
		})
	}
}

func TestAccessChecks(t *testing.T) {
	tg := buildGraph()
	inv := uuid.New()
	a := ComputeAccess(tg.scopes(inv, "mid"), inv, tg.g)
	if !a.Group(tg.groups["leaf"]) || a.Group(tg.groups["root"]) {
		t.Error("group check")
	}
	if !a.Host(tg.hosts["h-mid"]) || a.Host(tg.hosts["h-none"]) {
		t.Error("host check")
	}
	if !a.AllHostsIn([]string{"h-mid", "h-leaf"}) || a.AllHostsIn([]string{"h-mid", "h-other"}) {
		t.Error("AllHostsIn")
	}
	if a.AllHostsIn(nil) || a.AllHostsIn([]string{"h-unknown"}) {
		t.Error("empty and unknown host lists must fail closed")
	}
	if !a.AllGroupsIn([]uuid.UUID{tg.groups["mid"]}) || a.AllGroupsIn([]uuid.UUID{tg.groups["mid"], tg.groups["other"]}) {
		t.Error("AllGroupsIn")
	}
	if !(Access{All: true}).AllHostsIn([]string{"anything"}) {
		t.Error("whole-inventory access covers any host name")
	}
}

type fakeSource struct {
	inv   uuid.UUID
	g     Graph
	err   error
	loads int
}

func (f *fakeSource) InventoryGraph(_ context.Context, _, inv uuid.UUID) (Graph, error) {
	f.loads++
	if f.err != nil {
		return Graph{}, f.err
	}
	if inv != f.inv {
		return Graph{}, ErrNoInventory
	}
	return f.g, nil
}

func TestInventoryResolver(t *testing.T) {
	tg := buildGraph()
	inv := uuid.New()
	ws := uuid.NewString()
	hosts := func(n ...string) Target { return Target{InventoryID: inv.String(), Hosts: n} }
	group := func(n string) Target {
		return Target{InventoryID: inv.String(), Refs: []Ref{{KindInventoryGroup, tg.groups[n].String()}}}
	}
	tests := []struct {
		name   string
		scopes []string
		target Target
		want   bool
	}{
		{"empty target", []string{"inv"}, Target{}, false},
		{"inventory scope, inventory ref", []string{"inv"}, Target{Refs: []Ref{{KindInventory, inv.String()}}}, true},
		{"group scope cannot act on whole inventory", []string{"root"}, Target{Refs: []Ref{{KindInventory, inv.String()}}}, false},
		{"other inventory ref", []string{"inv"}, Target{Refs: []Ref{{KindInventory, uuid.NewString()}}}, false},
		{"group ref in scope", []string{"mid"}, group("leaf"), true},
		{"group ref outside", []string{"mid"}, group("root"), false},
		{"group ref without inventory id", []string{"mid"}, Target{Refs: []Ref{{KindInventoryGroup, tg.groups["leaf"].String()}}}, false},
		{"group ref derived from inventory ref only with inventory scope", []string{"mid"},
			Target{Refs: []Ref{{KindInventory, inv.String()}, {KindInventoryGroup, tg.groups["leaf"].String()}}}, false},
		{"hosts in scope", []string{"mid"}, hosts("h-mid", "h-leaf", "h-both"), true},
		{"one host outside", []string{"mid"}, hosts("h-mid", "h-root"), false},
		{"ungrouped host outside", []string{"mid"}, hosts("h-none"), false},
		{"unknown host", []string{"mid"}, hosts("h-ghost"), false},
		{"hosts need an inventory", []string{"mid"}, Target{Hosts: []string{"h-mid"}}, false},
		{"hosts with inventory scope", []string{"inv"}, hosts("h-root", "h-none"), true},
		{"hosts with network scope only", []string{"net"}, hosts("h-root"), false},
		{"inventory ref names inventory for hosts", []string{"inv"}, Target{Refs: []Ref{{KindInventory, inv.String()}}, Hosts: []string{"h-x"}}, true},
		{"network ref never matches", []string{"inv"}, Target{Refs: []Ref{{KindNetwork, "net-1"}}}, false},
		{"bad ref id", []string{"mid"}, Target{InventoryID: inv.String(), Refs: []Ref{{KindInventoryGroup, "x"}}}, false},
		{"bad inventory id", []string{"mid"}, Target{InventoryID: "x", Hosts: []string{"h-mid"}}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := InventoryResolver{Source: &fakeSource{inv: inv, g: tg.g}}
			got, err := r.InScope(context.Background(), ws, tg.scopes(inv, tc.scopes...), tc.target)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Errorf("InScope=%v want %v", got, tc.want)
			}
		})
	}
	t.Run("unknown inventory yields no access", func(t *testing.T) {
		r := InventoryResolver{Source: &fakeSource{inv: uuid.New(), g: tg.g}}
		a, err := r.Access(context.Background(), ws, tg.scopes(inv, "mid"), inv)
		if err != nil || !a.None() {
			t.Errorf("access %+v err %v", a, err)
		}
	})
	t.Run("source errors propagate", func(t *testing.T) {
		boom := errors.New("boom")
		r := InventoryResolver{Source: &fakeSource{err: boom}}
		if _, err := r.InScope(context.Background(), ws, tg.scopes(inv, "mid"), hosts("h-mid")); !errors.Is(err, boom) {
			t.Errorf("err %v", err)
		}
	})
	t.Run("graph is only loaded when needed", func(t *testing.T) {
		src := &fakeSource{inv: inv, g: tg.g}
		r := InventoryResolver{Source: src}
		for _, sc := range [][]string{{"inv"}, {"net"}} {
			if _, err := r.Access(context.Background(), ws, tg.scopes(inv, sc...), inv); err != nil {
				t.Fatal(err)
			}
		}
		if src.loads != 0 {
			t.Errorf("graph loaded %d times", src.loads)
		}
		if _, err := r.InScope(context.Background(), ws, tg.scopes(inv, "mid"), hosts("h-mid", "h-leaf")); err != nil || src.loads != 1 {
			t.Errorf("loads=%d err=%v, want one load per call", src.loads, err)
		}
	})
}

func TestAuthorizerHostsAndAccess(t *testing.T) {
	tg := buildGraph()
	inv := uuid.New()
	src := &fakeSource{inv: inv, g: tg.g}
	f := &fakeStore{ws: dbgen.Workspace{ID: uuid.New(), Slug: "default"}, member: map[uuid.UUID]dbgen.Membership{}}
	users := map[string]auth.Principal{}
	add := func(name, role string, scopes ...Scope) {
		u := dbgen.User{ID: uuid.New(), DisplayName: name}
		users[name] = auth.Principal{User: u}
		m := dbgen.Membership{ID: uuid.New(), WorkspaceID: f.ws.ID, UserID: u.ID, Role: role}
		f.member[u.ID] = m
		for _, s := range scopes {
			f.scopes = append(f.scopes, dbgen.MembershipScope{MembershipID: m.ID, Kind: s.Kind, RefID: s.RefID})
		}
	}
	add("unscoped", RoleOperator)
	add("viewer", RoleViewer)
	add("mid-op", RoleOperator, tg.scopes(inv, "mid")...)
	add("inv-op", RoleOperator, tg.scopes(inv, "inv")...)
	add("net-op", RoleOperator, tg.scopes(inv, "net")...)
	authn := func(next http.Handler) http.Handler { return next }
	a := New(f, InventoryResolver{Source: src}, authn, slog.New(slog.NewTextHandler(io.Discard, nil)))

	tests := []struct {
		name  string
		user  string
		perm  Permission
		hosts []string
		want  error
	}{
		{"unscoped runs anything", "unscoped", RunExecute, []string{"x", "y"}, nil},
		{"unscoped viewer cannot execute", "viewer", RunExecute, []string{"x"}, ErrForbidden},
		{"group scope, hosts inside", "mid-op", RunExecute, []string{"h-mid", "h-leaf"}, nil},
		{"group scope, one host outside", "mid-op", RunExecute, []string{"h-mid", "h-root"}, ErrForbidden},
		{"group scope, empty limit", "mid-op", RunExecute, nil, ErrForbidden},
		{"inventory scope, any host", "inv-op", RunExecute, []string{"h-root", "h-none"}, nil},
		{"network scope without a network source matches nothing", "net-op", RunExecute, []string{"h-mid"}, ErrForbidden},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := a.AuthorizeHosts(context.Background(), users[tc.user], f.ws.ID, tc.perm, inv, tc.hosts)
			if !errors.Is(err, tc.want) {
				t.Errorf("got %v want %v", err, tc.want)
			}
		})
	}

	d, err := a.load(context.Background(), f.ws, users["mid-op"].User.ID)
	if err != nil {
		t.Fatal(err)
	}
	acc, err := a.InventoryAccess(context.Background(), d, inv)
	if err != nil || acc.All || !slices.Equal(tg.hostNames(acc), []string{"h-both", "h-leaf", "h-mid"}) {
		t.Errorf("InventoryAccess: %+v %v", acc, err)
	}
	du, _ := a.load(context.Background(), f.ws, users["unscoped"].User.ID)
	if acc, err := a.InventoryAccess(context.Background(), du, inv); err != nil || !acc.All {
		t.Errorf("unscoped access: %+v %v", acc, err)
	}
	// A resolver without AccessResolver falls back to inventory scopes.
	plain := New(f, plainResolver{}, authn, slog.New(slog.NewTextHandler(io.Discard, nil)))
	di, _ := plain.load(context.Background(), f.ws, users["inv-op"].User.ID)
	if acc, _ := plain.InventoryAccess(context.Background(), di, inv); !acc.All {
		t.Error("fallback must honour inventory scopes")
	}
	if acc, _ := plain.InventoryAccess(context.Background(), d, inv); !acc.None() {
		t.Error("fallback must not grant group scopes")
	}
}

type plainResolver struct{}

func (plainResolver) InScope(context.Context, string, []Scope, Target) (bool, error) {
	return false, nil
}
