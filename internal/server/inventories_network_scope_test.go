package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"

	"github.com/google/uuid"

	"github.com/BxnnyG/ansible-control-plane/internal/auth"
	"github.com/BxnnyG/ansible-control-plane/internal/db/dbgen"
	"github.com/BxnnyG/ansible-control-plane/internal/inventory"
	"github.com/BxnnyG/ansible-control-plane/internal/policy"
)

// netSource resolves one network to a prefix and serves fixed fleet hosts.
type netSource struct {
	prefixes map[uuid.UUID]netip.Prefix
	hosts    []policy.FleetHost
}

func (n netSource) NetworkPrefixes(_ context.Context, _ uuid.UUID, ids []uuid.UUID) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, id := range ids {
		if p, ok := n.prefixes[id]; ok {
			out = append(out, p)
		}
	}
	return out, nil
}

func (n netSource) FleetHosts(context.Context, uuid.UUID) ([]policy.FleetHost, error) {
	return n.hosts, nil
}

// networkScopedRouter serves the inventories API for a membership scoped to
// one network. Fleet hosts h-leaf (in the network) and h-other (outside) are
// linked to the inventory hosts of the same name.
func networkScopedRouter(role string, svc *scopeInv, net uuid.UUID, cidr string, audits *[]string) http.Handler {
	uid := uuid.New()
	scopes := scopeOf(policy.KindNetwork, net.String())
	st := fakeAPIStore{ws: dbgen.Workspace{ID: uuid.New(), Slug: "default", Name: "Default"}, user: uid, role: role, scopes: scopes, audits: audits}
	authn := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(auth.WithPrincipal(r.Context(), auth.Principal{User: dbgen.User{ID: uid}})))
		})
	}
	ns := netSource{
		prefixes: map[uuid.UUID]netip.Prefix{net: netip.MustParsePrefix(cidr)},
		hosts: []policy.FleetHost{
			{ID: uuid.New(), Names: []string{"h-leaf"}, IPs: []string{"192.0.2.5"}},
			{ID: uuid.New(), Names: []string{"h-other"}, IPs: []string{"198.51.100.5"}},
		},
	}
	az := policy.New(st, policy.InventoryResolver{Source: svc, Networks: ns}, authn, quiet)
	return Router(quiet, fakePing{}, DefaultStatic(), WithPolicy(az, st), WithInventories(svc))
}

// A membership scoped to a network sees the inventories that contain hosts of
// that network, with hosts reduced to the granted ones, and cannot change or
// commit anything.
func TestInventoriesNetworkScopedReads(t *testing.T) {
	svc := newScopeInv()
	netID := uuid.New()
	var audits []string
	h := networkScopedRouter(policy.RoleEditor, svc, netID, "192.0.2.0/24", &audits)
	do := func(method, path string) *httptest.ResponseRecorder {
		req := httptest.NewRequestWithContext(context.Background(), method, "/api/v1/workspaces/default/inventories"+path, nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	base := "/" + svc.inv.ID.String()

	var invs []inventory.Inventory
	rec := do("GET", "")
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &invs) != nil {
		t.Fatalf("list: %d %s", rec.Code, rec.Body.String())
	}
	if len(invs) != 1 || invs[0].ID != svc.inv.ID || invs[0].Hosts != 1 || invs[0].Groups != 0 || invs[0].Path != "" || invs[0].BaseSHA != "" {
		t.Fatalf("list must hold only the inventory with granted hosts, narrowed: %+v", invs)
	}
	if rec := do("GET", base); rec.Code != 200 {
		t.Errorf("get inventory: %d", rec.Code)
	}

	var hosts []inventory.HostView
	rec = do("GET", base+"/hosts")
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &hosts) != nil {
		t.Fatalf("hosts: %d", rec.Code)
	}
	if len(hosts) != 1 || hosts[0].Name != "h-leaf" || len(hosts[0].GroupIDs) != 0 {
		t.Errorf("hosts must be filtered to granted hosts without group ids: %+v", hosts)
	}
	var groups []inventory.GroupView
	rec = do("GET", base+"/groups")
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &groups) != nil || len(groups) != 0 {
		t.Errorf("groups must be empty: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do("GET", base+"/hosts/"+svc.hid("h-leaf")); rec.Code != 200 {
		t.Errorf("granted host: %d", rec.Code)
	}
	if rec := do("GET", base+"/hosts/"+svc.hid("h-other")); rec.Code != 403 {
		t.Errorf("ungranted host: %d", rec.Code)
	}
	if rec := do("GET", base+"/render"); rec.Code != 200 || svc.visible == nil || len(svc.visible.Hosts) != 1 || len(svc.visible.Groups) != 0 {
		t.Errorf("render must be restricted: %d %+v", rec.Code, svc.visible)
	}

	for _, c := range []struct{ method, path string }{
		{"PUT", base}, {"POST", base + "/commit"}, {"GET", base + "/diff"}, {"POST", base + "/import"}, {"DELETE", base},
	} {
		if rec := do(c.method, c.path); rec.Code != 403 {
			t.Errorf("%s %s = %d, want 403", c.method, c.path, rec.Code)
		}
	}
	if len(svc.calls) != 0 {
		t.Errorf("service was reached: %v", svc.calls)
	}
}

func TestInventoriesNetworkScopeWithoutMatchingHosts(t *testing.T) {
	svc := newScopeInv()
	var audits []string
	// A network no fleet host lies in: the inventory stays invisible.
	h2 := networkScopedRouter(policy.RoleViewer, svc, uuid.New(), "203.0.113.0/24", &audits)
	req := httptest.NewRequestWithContext(context.Background(), "GET", "/api/v1/workspaces/default/inventories", nil)
	rec := httptest.NewRecorder()
	h2.ServeHTTP(rec, req)
	var invs []inventory.Inventory
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &invs) != nil || len(invs) != 0 {
		t.Fatalf("list: %d %s", rec.Code, rec.Body.String())
	}
}
