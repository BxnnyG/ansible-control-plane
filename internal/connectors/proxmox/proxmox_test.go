package proxmox

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

const (
	testToken  = "svc@pve!acp=00000000-0000-4000-8000-000000000001"
	testSecret = "00000000-0000-4000-8000-000000000001"
)

type route struct {
	file   string
	status int
}

func ok(f string) route { return route{file: f} }
func code(c int) route  { return route{status: c} }

func base() map[string]route {
	return map[string]route{
		"version":                                          ok("version.json"),
		"nodes":                                            ok("nodes.json"),
		"nodes/pve1/network":                               ok("network_pve1.json"),
		"cluster/resources":                                ok("resources.json"),
		"nodes/pve1/qemu/100/config":                       ok("config_100.json"),
		"nodes/pve1/qemu/101/config":                       ok("config_101.json"),
		"nodes/pve1/qemu/102/config":                       ok("config_102.json"),
		"nodes/pve1/qemu/900/config":                       ok("config_900.json"),
		"nodes/pve1/lxc/200/config":                        ok("config_200.json"),
		"nodes/pve1/lxc/201/config":                        ok("config_201.json"),
		"nodes/pve1/qemu/100/agent/network-get-interfaces": ok("agent_100.json"),
		"nodes/pve1/qemu/101/agent/network-get-interfaces": code(500),
		"nodes/pve1/lxc/200/interfaces":                    ok("lxc_ifaces_200.json"),
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

func (s *server) requested(p string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.reqs {
		if strings.HasSuffix(r, "/api2/json/"+p) {
			return true
		}
	}
	return false
}

func newServer(t *testing.T, routes map[string]route) *server {
	t.Helper()
	s := &server{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.reqs = append(s.reqs, r.Method+" "+r.URL.Path)
		s.auth = r.Header.Get("Authorization")
		s.mu.Unlock()
		rt, found := routes[strings.TrimPrefix(r.URL.Path, "/api2/json/")]
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
	c, err := New(connectors.Instance{Name: "pve", BaseURL: url, Config: map[string]any{connectors.CfgTimeoutSecs: 5}},
		connectors.Credential{Type: secrets.TypeAPIToken, Value: secrets.NewValue([]byte(testToken))})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func byKey(res connectors.Result) map[string]connectors.HostObservation {
	m := map[string]connectors.HostObservation{}
	for _, o := range res.Observations {
		m[o.ExternalKey] = o
	}
	return m
}

func TestSyncFull(t *testing.T) {
	s := newServer(t, base())
	res, err := build(t, s.URL).Sync(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if want := "PVEAPIToken=" + testToken; s.auth != want {
		t.Errorf("auth header = %q", s.auth)
	}
	for _, r := range s.reqs {
		if !strings.HasPrefix(r, "GET ") {
			t.Errorf("non-GET request %q", r)
		}
	}
	obs := byKey(res)
	var keys []string
	for k := range obs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	want := []string{"lxc:proxmox:200", "lxc:proxmox:201", "node:proxmox:pve1", "vm:proxmox:100", "vm:proxmox:101", "vm:proxmox:102"}
	if strings.Join(keys, " ") != strings.Join(want, " ") {
		t.Fatalf("observations = %v", keys)
	}
	cases := []struct {
		key     string
		macs    string
		host    string
		ips     string
		tags    string
		status  string
		notes   string
		noAgent string // endpoint that must not be requested
	}{
		{"node:proxmox:pve1", "", "pve1", "192.0.2.2 2001:db8:0:1::2 198.51.100.2", "", "online", "", ""},
		{"vm:proxmox:100", "02:00:00:00:10:01 02:00:00:00:10:02", "vm-agent", "192.0.2.20 2001:db8:0:1::20 198.51.100.20",
			"proxmox:web proxmox:prod node:pve1", "running", "Web frontend\nowner: team-a", ""},
		{"vm:proxmox:101", "02:00:00:00:10:03", "vm-noagent", "", "node:pve1", "running", "", ""},
		{"vm:proxmox:102", "02:00:00:00:10:04", "vm-stopped", "", "proxmox:lab proxmox:test node:pve1", "stopped", "powered off", "nodes/pve1/qemu/102/agent/network-get-interfaces"},
		{"lxc:proxmox:200", "02:00:00:00:20:01", "ct-running", "192.0.2.30 2001:db8:0:1::30", "proxmox:dns node:pve1", "running", "DNS container", ""},
		{"lxc:proxmox:201", "02:00:00:00:20:02", "ct-stopped", "", "node:pve1", "stopped", "", "nodes/pve1/lxc/201/interfaces"},
	}
	for _, tc := range cases {
		t.Run(tc.key, func(t *testing.T) {
			o, f := obs[tc.key], obs[tc.key].Fields
			if strings.Join(f.MACs, " ") != tc.macs || f.Hostname != tc.host || strings.Join(f.IPs, " ") != tc.ips ||
				strings.Join(f.Tags, " ") != tc.tags ||
				f.Status != tc.status || f.Notes != tc.notes {
				t.Errorf("%s = %+v", o.ExternalKey, f)
			}
			if tc.noAgent != "" && s.requested(tc.noAgent) {
				t.Errorf("unexpected request %s", tc.noAgent)
			}
		})
	}
	// Templates are skipped without any request.
	if s.requested("nodes/pve1/qemu/900/config") {
		t.Error("template config requested")
	}
	if obs["vm:proxmox:100"].Kind != connectors.KindVM || obs["lxc:proxmox:200"].Kind != connectors.KindLXC || obs["node:proxmox:pve1"].Kind != connectors.KindNode {
		t.Error("wrong kinds")
	}
	// Only VM 101 has an agent warning.
	if len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "qemu/101/agent") {
		t.Errorf("warnings = %v", res.Warnings)
	}
	if len(res.Networks) != 3 {
		t.Fatalf("networks = %+v", res.Networks)
	}
	for _, n := range res.Networks {
		if n.Interface == "vmbr0.20" && (n.VLAN == nil || *n.VLAN != 20 || n.CIDR.String() != "198.51.100.0/24" || n.Description != "Lab VLAN") {
			t.Errorf("vlan network = %+v", n)
		}
		if n.Interface == "vmbr0" && n.VLAN != nil {
			t.Errorf("bridge has vlan: %+v", n)
		}
	}
}

func TestSyncFailures(t *testing.T) {
	cases := []struct {
		name     string
		routes   map[string]route
		wantErr  string
		warnings int
		obs      int
	}{
		{"401 on version", with(base(), "version", code(401)), "401", 0, 0},
		{"401 on guest config", with(base(), "nodes/pve1/qemu/100/config", code(401)), "401", 0, 0},
		{"403 on guest config", with(base(), "nodes/pve1/qemu/100/config", code(403)), "", 2, 6},
		{"403 on network", with(base(), "nodes/pve1/network", code(403)), "", 2, 6},
		{"403 on resources", with(base(), "cluster/resources", code(403)), "", 1, 1},
		{"agent 401", with(base(), "nodes/pve1/qemu/100/agent/network-get-interfaces", code(401)), "401", 0, 0},
		{"lxc interfaces fail", with(base(), "nodes/pve1/lxc/200/interfaces", code(500)), "", 2, 6},
		{"not proxmox", with(base(), "version", route{file: "nodes.json"}), "version", 0, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newServer(t, tc.routes)
			res, err := build(t, s.URL).Sync(context.Background())
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v", err)
				}
				if strings.Contains(err.Error(), testSecret) {
					t.Errorf("error leaks secret: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(res.Warnings) != tc.warnings {
				t.Errorf("warnings = %v", res.Warnings)
			}
			if len(res.Observations) != tc.obs {
				t.Errorf("observations = %d", len(res.Observations))
			}
		})
	}
}

func TestOfflineNodeSkipsNetwork(t *testing.T) {
	s := newServer(t, with(base(), "nodes", ok("nodes_offline.json")))
	res, err := build(t, s.URL).Sync(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if s.requested("nodes/pve2/network") {
		t.Error("offline node network requested")
	}
	if o := byKey(res)["node:proxmox:pve2"]; o.Fields.Status != "offline" {
		t.Errorf("pve2 = %+v", o)
	}
}

func TestTest(t *testing.T) {
	if err := build(t, newServer(t, base()).URL).Test(context.Background()); err != nil {
		t.Fatal(err)
	}
	err := build(t, newServer(t, with(base(), "version", code(401))).URL).Test(context.Background())
	if err == nil || strings.Contains(err.Error(), testSecret) {
		t.Fatalf("err = %v", err)
	}
}

func TestNewValidation(t *testing.T) {
	for _, tc := range []struct{ typ, val string }{
		{secrets.TypeAPIToken, ""}, {secrets.TypeAPIToken, "svc@pve!acp"}, {secrets.TypeAPIToken, "svc!acp=secret"},
		{secrets.TypeAPIToken, "svc@pve=secret"}, {secrets.TypePassword, testToken},
	} {
		_, err := New(connectors.Instance{BaseURL: "https://pve.example.test:8006"},
			connectors.Credential{Type: tc.typ, Value: secrets.NewValue([]byte(tc.val))})
		if err == nil {
			t.Errorf("%s %q accepted", tc.typ, tc.val)
		} else if tc.val != "" && strings.Contains(err.Error(), tc.val) {
			t.Errorf("error leaks value: %v", err)
		}
	}
}

func TestRegistryValidate(t *testing.T) {
	reg := connectors.NewRegistry()
	Register(reg)
	if err := reg.Validate(Type, "https://pve.example.test:8006", nil, secrets.TypeAPIToken, true); err != nil {
		t.Fatal(err)
	}
	if reg.Validate(Type, "https://pve.example.test:8006", nil, secrets.TypeAPIKeyPair, true) == nil {
		t.Error("wrong secret type accepted")
	}
	if sp, _ := reg.Spec(Type); sp.Help == "" {
		t.Error("spec has no help")
	}
}

func TestSplitTags(t *testing.T) {
	for in, want := range map[string]string{"": "", "a;b": "proxmox:a proxmox:b", "a,b c": "proxmox:a proxmox:b proxmox:c"} {
		if got := strings.Join(splitTags(in), " "); got != want {
			t.Errorf("%q -> %q", in, got)
		}
	}
}

func TestVirtualIface(t *testing.T) {
	for name, want := range map[string]bool{
		"lo": true, "docker0": true, "br-0a1b2c": true, "veth9f": true, "cni0": true, "flannel.1": true, "virbr0": true,
		"eth0": false, "ens18": false, "local0": false, "wg0": false, "tailscale0": false, "vmbr0": false,
	} {
		if virtualIface(name) != want {
			t.Errorf("virtualIface(%q) = %v", name, !want)
		}
	}
}
