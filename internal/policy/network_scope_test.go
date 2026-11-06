package policy

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/netip"
	"slices"
	"testing"

	"github.com/google/uuid"

	"github.com/BxnnyG/ansible-control-plane/internal/auth"
	"github.com/BxnnyG/ansible-control-plane/internal/db/dbgen"
)

type fakeNetworks struct {
	prefixes map[uuid.UUID]netip.Prefix
	hosts    []FleetHost
	err      error
}

func (f fakeNetworks) NetworkPrefixes(_ context.Context, _ uuid.UUID, ids []uuid.UUID) ([]netip.Prefix, error) {
	if f.err != nil {
		return nil, f.err
	}
	var out []netip.Prefix
	for _, id := range ids {
		if p, ok := f.prefixes[id]; ok {
			out = append(out, p)
		}
	}
	return out, nil
}

func (f fakeNetworks) FleetHosts(context.Context, uuid.UUID) ([]FleetHost, error) { return f.hosts, nil }

func TestNetworkScopeInventoryAccess(t *testing.T) {
	tg := buildGraph()
	inv := uuid.New()
	netID := uuid.New()
	// h-mid is linked by name (case-insensitive), h-solo by ansible_host IP,
	// h-leaf by ansible_host name. h-other belongs to a fleet host outside the network.
	tg.g.HostAddrs = map[uuid.UUID]string{tg.hosts["h-solo"]: "10.1.0.7", tg.hosts["h-leaf"]: "web-leaf.example.test"}
	fleetHosts := []FleetHost{
		{ID: uuid.New(), Names: []string{"H-MID"}, IPs: []string{"10.1.0.5"}},
		{ID: uuid.New(), Names: []string{"other-name"}, IPs: []string{"10.1.0.7"}},
		{ID: uuid.New(), Names: []string{"web-leaf.example.test"}, IPs: []string{"10.1.0.9", "::ffff:10.1.0.9"}},
		{ID: uuid.New(), Names: []string{"h-other"}, IPs: []string{"10.2.0.5"}},
	}
	nets := fakeNetworks{prefixes: map[uuid.UUID]netip.Prefix{netID: netip.MustParsePrefix("10.1.0.0/24")}, hosts: fleetHosts}
	r := InventoryResolver{Source: &fakeSource{inv: inv, g: tg.g}, Networks: nets}
	ws := uuid.NewString()
	netScope := Scope{KindNetwork, netID.String()}
	ctx := context.Background()

	a, err := r.Access(ctx, ws, []Scope{netScope}, inv)
	if err != nil {
		t.Fatal(err)
	}
	if got := tg.hostNames(a); !slices.Equal(got, []string{"h-leaf", "h-mid", "h-solo"}) {
		t.Errorf("hosts %v", got)
	}
	if a.All || len(a.Groups) != 0 || a.None() {
		t.Errorf("access %+v", a)
	}
	// Union with a group scope keeps both.
	a, _ = r.Access(ctx, ws, append(tg.scopes(inv, "other"), netScope), inv)
	if got := tg.hostNames(a); !slices.Equal(got, []string{"h-both", "h-leaf", "h-mid", "h-other", "h-solo"}) {
		t.Errorf("union hosts %v", got)
	}
	// An inventory scope still wins.
	if a, _ = r.Access(ctx, ws, []Scope{netScope, {KindInventory, inv.String()}}, inv); !a.All {
		t.Error("inventory scope lost")
	}
	// Unknown network, malformed id and failing source grant nothing / fail closed.
	for name, sc := range map[string]Scope{"unknown": {KindNetwork, uuid.NewString()}, "garbage": {KindNetwork, "net"}} {
		if a, err = r.Access(ctx, ws, []Scope{sc}, inv); err != nil || !a.None() {
			t.Errorf("%s: %+v %v", name, a, err)
		}
	}
	bad := InventoryResolver{Source: &fakeSource{inv: inv, g: tg.g}, Networks: fakeNetworks{err: errors.New("boom")}}
	if _, err = bad.Access(ctx, ws, []Scope{netScope}, inv); err == nil {
		t.Error("source error must propagate")
	}

	ok, err := r.InScope(ctx, ws, []Scope{netScope}, Target{InventoryID: inv.String(), Hosts: []string{"h-mid", "h-solo"}})
	if err != nil || !ok {
		t.Errorf("InScope inside: %v %v", ok, err)
	}
	ok, _ = r.InScope(ctx, ws, []Scope{netScope}, Target{InventoryID: inv.String(), Hosts: []string{"h-mid", "h-other"}})
	if ok {
		t.Error("host outside the network must be denied")
	}
	// A network scope never grants the whole inventory.
	ok, _ = r.InScope(ctx, ws, []Scope{netScope}, Target{Refs: []Ref{{KindInventory, inv.String()}}})
	if ok {
		t.Error("inventory ref must be denied for network scope")
	}
}

func TestNetworkScopeAuthorizeAndFleetAccess(t *testing.T) {
	tg := buildGraph()
	inv := uuid.New()
	netID := uuid.New()
	nets := fakeNetworks{prefixes: map[uuid.UUID]netip.Prefix{netID: netip.MustParsePrefix("10.1.0.0/24")},
		hosts: []FleetHost{{ID: uuid.New(), Names: []string{"h-mid"}, IPs: []string{"10.1.0.5"}}, {ID: uuid.New(), Names: []string{"h-root"}, IPs: []string{"10.9.0.5"}}}}
	f := &fakeStore{ws: dbgen.Workspace{ID: uuid.New(), Slug: "default"}, member: map[uuid.UUID]dbgen.Membership{}}
	u := dbgen.User{ID: uuid.New(), DisplayName: "net-op"}
	m := dbgen.Membership{ID: uuid.New(), WorkspaceID: f.ws.ID, UserID: u.ID, Role: RoleOperator}
	f.member[u.ID] = m
	f.scopes = append(f.scopes, dbgen.MembershipScope{MembershipID: m.ID, Kind: KindNetwork, RefID: netID.String()})
	authn := func(next http.Handler) http.Handler { return next }
	a := New(f, InventoryResolver{Source: &fakeSource{inv: inv, g: tg.g}, Networks: nets}, authn, slog.New(slog.NewTextHandler(io.Discard, nil)))
	p := auth.Principal{User: u}
	ctx := context.Background()

	if err := a.AuthorizeHosts(ctx, p, f.ws.ID, RunExecute, inv, []string{"h-mid"}); err != nil {
		t.Errorf("host in network: %v", err)
	}
	if err := a.AuthorizeHosts(ctx, p, f.ws.ID, RunExecute, inv, []string{"h-mid", "h-root"}); !errors.Is(err, ErrForbidden) {
		t.Errorf("host outside network: %v", err)
	}
	d, err := a.load(ctx, f.ws, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	fa, err := a.FleetAccess(ctx, d)
	if err != nil || fa.All || fa.None() {
		t.Fatalf("fleet access %+v %v", fa, err)
	}
	if !fa.Host([]string{"192.0.2.1", "10.1.0.200"}) || fa.Host([]string{"10.9.0.5"}) || fa.Host(nil) || fa.Host([]string{"bogus"}) {
		t.Error("fleet host filter wrong")
	}
	// Unscoped sees everything; resolvers without network support see nothing.
	f.scopes = nil
	du, _ := a.load(ctx, f.ws, u.ID)
	if fa, _ = a.FleetAccess(ctx, du); !fa.All || !fa.Host(nil) {
		t.Error("unscoped must see all")
	}
	plain := New(f, plainResolver{}, authn, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if fa, _ = plain.FleetAccess(ctx, d); !fa.None() {
		t.Error("plain resolver must grant no fleet access")
	}
	if fa, _ = (RefResolver{}).FleetAccess(ctx, "", d.Scopes); !fa.None() {
		t.Error("RefResolver must grant no fleet access")
	}
}

type fakeNetQueries struct {
	nets  []dbgen.Network
	hosts []dbgen.Host
	keys  []dbgen.HostKey
}

func (f fakeNetQueries) ListNetworks(context.Context, uuid.UUID) ([]dbgen.Network, error) {
	return f.nets, nil
}
func (f fakeNetQueries) ListHosts(context.Context, uuid.UUID) ([]dbgen.Host, error) {
	return f.hosts, nil
}
func (f fakeNetQueries) ListHostKeys(context.Context, uuid.UUID) ([]dbgen.HostKey, error) {
	return f.keys, nil
}

func TestDBNetworkSource(t *testing.T) {
	n1, n2, h := uuid.New(), uuid.New(), uuid.New()
	src := DBNetworkSource{Q: fakeNetQueries{
		nets:  []dbgen.Network{{ID: n1, Cidr: netip.MustParsePrefix("10.1.0.0/24")}, {ID: n2, Cidr: netip.MustParsePrefix("10.2.0.0/24")}},
		hosts: []dbgen.Host{{ID: h, Name: "web1", Fields: []byte(`{"hostname":"web1","fqdn":"web1.example.test","ips":["10.1.0.5"]}`)}},
		keys:  []dbgen.HostKey{{HostID: h, Kind: "name", Value: "alias"}, {HostID: h, Kind: "mac", Value: "aa:bb:cc:dd:ee:ff"}},
	}}
	pfx, err := src.NetworkPrefixes(context.Background(), uuid.New(), []uuid.UUID{n2})
	if err != nil || len(pfx) != 1 || pfx[0].String() != "10.2.0.0/24" {
		t.Errorf("prefixes %v %v", pfx, err)
	}
	hosts, err := src.FleetHosts(context.Background(), uuid.New())
	if err != nil || len(hosts) != 1 || hosts[0].ID != h || !slices.Equal(hosts[0].IPs, []string{"10.1.0.5"}) {
		t.Fatalf("hosts %+v %v", hosts, err)
	}
	for _, want := range []string{"alias", "web1", "web1.example.test"} {
		if !slices.Contains(hosts[0].Names, want) {
			t.Errorf("name %q missing in %v", want, hosts[0].Names)
		}
	}
	if slices.Contains(hosts[0].Names, "aa:bb:cc:dd:ee:ff") {
		t.Error("MAC must not be a name")
	}
}
