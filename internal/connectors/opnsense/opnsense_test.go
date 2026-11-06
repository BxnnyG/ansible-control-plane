package opnsense

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/BxnnyG/ansible-control-plane/internal/connectors"
	"github.com/BxnnyG/ansible-control-plane/internal/secrets"
)

// route maps an API endpoint to a fixture file or an HTTP status.
type route struct {
	file   string
	status int
}

func ok(f string) route { return route{file: f} }
func code(c int) route  { return route{status: c} }
func base() map[string]route {
	return map[string]route{
		"core/firmware/status":                ok("firmware_status.json"),
		"core/system/status":                  ok("system_status.json"),
		"interfaces/overview/interfacesInfo":  ok("interfaces_info.json"),
		"interfaces/vlan_settings/searchItem": ok("vlan_search.json"),
		"diagnostics/interface/getArp":        ok("arp.json"),
		"diagnostics/interface/getNdp":        ok("ndp.json"),
		"kea/service/status":                  ok("status_stopped.json"),
		"dnsmasq/service/status":              ok("status_disabled.json"),
		"dhcpv4/service/status":               ok("status_stopped.json"),
	}
}

func with(m map[string]route, kv ...any) map[string]route {
	for len(kv) >= 2 {
		m[kv[0].(string)] = kv[1].(route)
		kv = kv[2:]
	}
	return m
}

type server struct {
	*httptest.Server
	mu   sync.Mutex
	reqs []string
	auth string
}

func newServer(t *testing.T, routes map[string]route) *server {
	t.Helper()
	s := &server{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.reqs = append(s.reqs, r.Method+" "+r.URL.Path)
		s.auth = r.Header.Get("Authorization")
		s.mu.Unlock()
		rt, found := routes[strings.TrimPrefix(r.URL.Path, "/api/")]
		switch {
		case !found:
			http.NotFound(w, r)
		case rt.status != 0:
			http.Error(w, "x", rt.status)
		default:
			b, err := os.ReadFile("testdata/" + rt.file)
			if err != nil {
				t.Errorf("fixture: %v", err)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(b)
		}
	}))
	t.Cleanup(s.Close)
	return s
}

func build(t *testing.T, url string) connectors.Connector {
	t.Helper()
	c, err := New(connectors.Instance{Name: "fw", BaseURL: url, Config: map[string]any{connectors.CfgTimeoutSecs: 5}},
		connectors.Credential{Type: secrets.TypeAPIKeyPair, Value: secrets.NewValue([]byte(`{"key":"testkey123","secret":"testsecret456"}`))})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func keys(res connectors.Result) []string {
	var out []string
	for _, o := range res.Observations {
		out = append(out, o.ExternalKey)
	}
	sort.Strings(out)
	return out
}

func TestSyncBackends(t *testing.T) {
	cases := []struct {
		name     string
		routes   map[string]route
		wantObs  []string
		wantNets []string
		warnings int
		forbid   string // endpoint that must not be requested
	}{
		{
			name: "isc",
			routes: with(base(), "dhcpv4/service/status", ok("status_running.json"),
				"dhcpv4/leases/searchLease", ok("isc_leases.json")),
			wantObs: []string{"arp:neigh:02:00:00:00:01:01/192.0.2.10", "arp:neigh:02:00:00:00:01:02/198.51.100.10",
				"lease:isc:02:00:00:00:02:01/192.0.2.50", "ndp:neigh:02:00:00:00:01:01/2001:db8:0:1::10",
				"static:isc:02:00:00:00:02:02/192.0.2.51"},
			wantNets: []string{"if:lan:192.0.2.0/24", "if:lan:2001:db8:0:1::/64", "if:opt1:198.51.100.0/24"},
			forbid:   "kea/leases4/search",
		},
		{
			name:   "isc without status privilege",
			routes: with(base(), "dhcpv4/service/status", code(403), "dhcpv4/leases/searchLease", ok("isc_leases.json")),
			wantObs: []string{"arp:neigh:02:00:00:00:01:01/192.0.2.10", "arp:neigh:02:00:00:00:01:02/198.51.100.10",
				"lease:isc:02:00:00:00:02:01/192.0.2.50", "ndp:neigh:02:00:00:00:01:01/2001:db8:0:1::10",
				"static:isc:02:00:00:00:02:02/192.0.2.51"},
			wantNets: []string{"if:lan:192.0.2.0/24", "if:lan:2001:db8:0:1::/64", "if:opt1:198.51.100.0/24"},
			warnings: 1,
		},
		{
			name: "dnsmasq",
			routes: with(base(), "dnsmasq/service/status", ok("status_running.json"),
				"dnsmasq/leases/search", ok("dnsmasq_leases.json"), "dnsmasq/settings/searchHost", ok("dnsmasq_hosts.json")),
			wantObs: []string{"arp:neigh:02:00:00:00:01:01/192.0.2.10", "arp:neigh:02:00:00:00:01:02/198.51.100.10",
				"lease:dnsmasq:02:00:00:00:03:01/192.0.2.60", "ndp:neigh:02:00:00:00:01:01/2001:db8:0:1::10",
				"static:dnsmasq:00000000-0000-4000-8000-0000000000a1#0", "static:dnsmasq:00000000-0000-4000-8000-0000000000a1#1",
				"static:dnsmasq:00000000-0000-4000-8000-0000000000a2"},
			wantNets: []string{"if:lan:192.0.2.0/24", "if:lan:2001:db8:0:1::/64", "if:opt1:198.51.100.0/24"},
			forbid:   "dhcpv4/leases/searchLease",
		},
		{
			name: "kea wins over dnsmasq",
			routes: with(base(), "kea/service/status", ok("status_running.json"), "dnsmasq/service/status", ok("status_running.json"),
				"kea/leases4/search", ok("kea_leases.json"), "kea/dhcpv4/search_reservation", ok("kea_reservations.json"),
				"kea/dhcpv4/search_subnet", ok("kea_subnets.json")),
			wantObs: []string{"arp:neigh:02:00:00:00:01:01/192.0.2.10", "arp:neigh:02:00:00:00:01:02/198.51.100.10",
				"lease:kea:02:00:00:00:04:01/192.0.2.70", "ndp:neigh:02:00:00:00:01:01/2001:db8:0:1::10",
				"static:kea:00000000-0000-4000-8000-0000000000b1"},
			wantNets: []string{"if:lan:192.0.2.0/24", "if:lan:2001:db8:0:1::/64", "if:opt1:198.51.100.0/24", "kea:203.0.113.0/24"},
			forbid:   "dnsmasq/leases/search",
		},
		{
			name:   "no backend",
			routes: with(base()),
			wantObs: []string{"arp:neigh:02:00:00:00:01:01/192.0.2.10", "arp:neigh:02:00:00:00:01:02/198.51.100.10",
				"ndp:neigh:02:00:00:00:01:01/2001:db8:0:1::10"},
			wantNets: []string{"if:lan:192.0.2.0/24", "if:lan:2001:db8:0:1::/64", "if:opt1:198.51.100.0/24"},
			warnings: 1,
			forbid:   "dhcpv4/leases/searchLease",
		},
		{
			name: "partial sync on 403 and 404",
			routes: with(base(), "interfaces/overview/interfacesInfo", code(403), "interfaces/vlan_settings/searchItem", code(403),
				"diagnostics/interface/getArp", code(403), "diagnostics/interface/getNdp", code(404),
				"dhcpv4/service/status", code(403), "dhcpv4/leases/searchLease", ok("isc_leases.json")),
			wantObs:  []string{"lease:isc:02:00:00:00:02:01/192.0.2.50", "static:isc:02:00:00:00:02:02/192.0.2.51"},
			warnings: 5,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := newServer(t, tc.routes)
			res, err := build(t, srv.URL).Sync(context.Background())
			if err != nil {
				t.Fatalf("sync: %v", err)
			}
			if got := keys(res); strings.Join(got, "|") != strings.Join(tc.wantObs, "|") {
				t.Errorf("observations:\n got %v\nwant %v", got, tc.wantObs)
			}
			var nets []string
			for _, n := range res.Networks {
				nets = append(nets, n.ExternalKey)
			}
			sort.Strings(nets)
			if strings.Join(nets, "|") != strings.Join(tc.wantNets, "|") {
				t.Errorf("networks:\n got %v\nwant %v", nets, tc.wantNets)
			}
			if len(res.Warnings) != tc.warnings {
				t.Errorf("warnings = %v, want %d", res.Warnings, tc.warnings)
			}
			for _, r := range srv.reqs {
				if !strings.HasPrefix(r, "GET ") {
					t.Errorf("non-GET request %q", r)
				}
				if tc.forbid != "" && strings.HasSuffix(r, "/api/"+tc.forbid) {
					t.Errorf("unexpected request %q", r)
				}
			}
			if !strings.HasPrefix(srv.auth, "Basic ") {
				t.Errorf("auth header = %q", srv.auth)
			}
			if err := validateResult(res); err != nil {
				t.Error(err)
			}
		})
	}
}

// validateResult mirrors the framework's key checks (unique, valid kinds).
func validateResult(res connectors.Result) error {
	seen := map[string]bool{}
	for _, n := range res.Networks {
		if seen[n.ExternalKey] || !n.CIDR.IsValid() {
			return errString("bad network " + n.ExternalKey)
		}
		seen[n.ExternalKey] = true
	}
	return nil
}

type errString string

func (e errString) Error() string { return string(e) }

func TestNormalization(t *testing.T) {
	srv := newServer(t, with(base(), "dhcpv4/service/status", ok("status_running.json"), "dhcpv4/leases/searchLease", ok("isc_leases.json")))
	res, err := build(t, srv.URL).Sync(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var vlan *int
	for _, n := range res.Networks {
		if n.ExternalKey == "if:opt1:198.51.100.0/24" {
			vlan = n.VLAN
			if n.Description != "Servers" || n.Interface != "opt1" {
				t.Errorf("network = %+v", n)
			}
		}
		if n.CIDR.Addr().IsLoopback() || n.CIDR.Addr().IsLinkLocalUnicast() {
			t.Errorf("unexpected network %v", n.CIDR)
		}
	}
	if vlan == nil || *vlan != 20 {
		t.Errorf("vlan = %v", vlan)
	}
	for _, o := range res.Observations {
		if o.ExternalKey != "static:isc:02:00:00:00:02:02/192.0.2.51" {
			continue
		}
		f := o.Fields
		if o.Kind != connectors.KindStatic || f.Hostname != "isc-static" || f.Notes != "Printer" || len(f.Tags) != 1 || f.Tags[0] != "interface:lan" {
			t.Errorf("static = %+v", o)
		}
		return
	}
	t.Error("static observation missing")
}

func TestSyncFailures(t *testing.T) {
	cases := []struct {
		name   string
		routes map[string]route
	}{
		{"auth failure on version", with(base(), "core/firmware/status", code(401))},
		{"forbidden on version", with(base(), "core/firmware/status", code(403))},
		{"auth failure on optional endpoint", with(base(), "diagnostics/interface/getArp", code(401))},
		{"server error on optional endpoint", with(base(), "diagnostics/interface/getArp", code(500))},
		{"no product version", with(base(), "core/firmware/status", ok("status_running.json"))},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := newServer(t, tc.routes)
			_, err := build(t, srv.URL).Sync(context.Background())
			if err == nil {
				t.Fatal("expected error")
			}
			if strings.Contains(err.Error(), "testsecret456") || strings.Contains(err.Error(), "testkey123") {
				t.Errorf("error leaks credentials: %v", err)
			}
		})
	}
}

func TestTest(t *testing.T) {
	srv := newServer(t, base())
	if err := build(t, srv.URL).Test(context.Background()); err != nil {
		t.Fatal(err)
	}
	bad := newServer(t, with(base(), "core/firmware/status", code(401)))
	if err := build(t, bad.URL).Test(context.Background()); err == nil {
		t.Fatal("expected auth failure")
	}
}

func TestNewAndSpec(t *testing.T) {
	reg := connectors.NewRegistry()
	Register(reg)
	if err := reg.Validate(Type, "https://fw.example.test:8443", map[string]any{connectors.CfgTimeoutSecs: 60, connectors.CfgTLSSkipVerify: true}, secrets.TypeAPIKeyPair, true); err != nil {
		t.Errorf("valid config rejected: %v", err)
	}
	if err := reg.Validate(Type, "", nil, secrets.TypeAPIKeyPair, true); err == nil {
		t.Error("missing base URL accepted")
	}
	if err := reg.Validate(Type, "https://fw.example.test", nil, secrets.TypePassword, true); err == nil {
		t.Error("wrong secret type accepted")
	}
	if _, err := New(connectors.Instance{BaseURL: "https://fw.example.test"}, connectors.Credential{}); err == nil {
		t.Error("missing credential accepted")
	}
	if _, err := New(connectors.Instance{BaseURL: "https://fw.example.test"},
		connectors.Credential{Type: secrets.TypeAPIKeyPair, Value: secrets.NewValue([]byte(`{"key":"k"}`))}); err == nil {
		t.Error("incomplete key pair accepted")
	}
}
