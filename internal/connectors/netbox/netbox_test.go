package netbox

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/BxnnyG/ansible-control-plane/internal/connectors"
	"github.com/BxnnyG/ansible-control-plane/internal/secrets"
)

const testToken = "fake-netbox-token-for-tests"

type route struct {
	file   string
	status int
	body   func(offset int) string
}

func base() map[string]route {
	return map[string]route{
		"/api/status/":                          {file: "status.json"},
		"/api/ipam/prefixes/":                   {file: "prefixes.json"},
		"/api/dcim/devices/":                    {file: "devices.json"},
		"/api/virtualization/virtual-machines/": {file: "vms.json"},
		"/api/dcim/interfaces/":                 {file: "interfaces_dcim.json"},
		"/api/virtualization/interfaces/":       {file: "interfaces_vm.json"},
		"/api/ipam/ip-addresses/":               {file: "ips.json"},
	}
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
		s.reqs = append(s.reqs, r.Method+" "+r.URL.RequestURI())
		s.auth = r.Header.Get("Authorization")
		s.mu.Unlock()
		rt, ok := routes[r.URL.Path]
		switch {
		case !ok:
			http.NotFound(w, r)
		case rt.status != 0:
			http.Error(w, "x", rt.status)
		case rt.body != nil:
			off, _ := strconv.Atoi(r.URL.Query().Get("offset"))
			_, _ = w.Write([]byte(rt.body(off)))
		default:
			b, err := os.ReadFile("testdata/" + rt.file)
			if err != nil {
				t.Errorf("fixture: %v", err)
			}
			_, _ = w.Write(b)
		}
	}))
	t.Cleanup(s.Close)
	return s
}

func build(t *testing.T, url string) connectors.Connector {
	t.Helper()
	c, err := New(connectors.Instance{Name: "nb", BaseURL: url, Config: map[string]any{connectors.CfgTimeoutSecs: 5}},
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

func TestTest(t *testing.T) {
	s := newServer(t, base())
	if err := build(t, s.URL).Test(context.Background()); err != nil {
		t.Fatal(err)
	}
	if want := "Token " + testToken; s.auth != want {
		t.Errorf("auth header = %q", s.auth)
	}
	bad := newServer(t, map[string]route{"/api/status/": {status: 401}})
	err := build(t, bad.URL).Test(context.Background())
	if err == nil || strings.Contains(err.Error(), testToken) {
		t.Errorf("want masked 401 error, got %v", err)
	}
	notNB := newServer(t, map[string]route{"/api/status/": {body: func(int) string { return `{"a":1}` }}})
	if err := build(t, notNB.URL).Test(context.Background()); err == nil {
		t.Error("want error for non-NetBox response")
	}
}

func TestSyncFull(t *testing.T) {
	s := newServer(t, base())
	res, err := build(t, s.URL).Sync(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range s.reqs {
		if !strings.HasPrefix(r, "GET ") {
			t.Errorf("non-GET request %q", r)
		}
	}
	if len(res.Warnings) != 0 {
		t.Errorf("warnings: %v", res.Warnings)
	}
	if len(res.Networks) != 2 || res.Networks[0].CIDR.String() != "192.0.2.0/24" ||
		res.Networks[0].VLAN == nil || *res.Networks[0].VLAN != 20 || res.Networks[0].Description != "Server network" ||
		res.Networks[1].VLAN != nil {
		t.Errorf("networks = %+v", res.Networks)
	}
	obs := byKey(res)
	if len(obs) != 3 {
		t.Fatalf("observations = %d (%v)", len(obs), obs)
	}
	web := obs["device:netbox:10"]
	if web.Kind != connectors.KindDevice || web.Fields.Hostname != "web-01.example.test" || web.Fields.FQDN != "web-01.example.test" ||
		web.Fields.Status != "active" {
		t.Errorf("web = %+v", web)
	}
	if want := []string{"192.0.2.10", "198.51.100.10"}; !slices.Equal(web.Fields.IPs, want) {
		t.Errorf("web ips = %v", web.Fields.IPs)
	}
	if want := []string{"02:00:00:00:00:10", "02:00:00:00:00:11", "02:00:00:00:00:12"}; !slices.Equal(web.Fields.MACs, want) {
		t.Errorf("web macs = %v", web.Fields.MACs)
	}
	if !slices.Equal(web.Fields.Tags, []string{"netbox:prod"}) {
		t.Errorf("web tags = %v", web.Fields.Tags)
	}
	wantAttrs := map[string]string{"site": "site-a", "role": "web", "tenant": "acme", "platform": "debian"}
	for k, v := range wantAttrs {
		if web.Fields.Attrs[k] != v {
			t.Errorf("web attr %s = %q, want %q", k, web.Fields.Attrs[k], v)
		}
	}
	sw := obs["device:netbox:11"]
	if sw.Fields.FQDN != "" || sw.Fields.Attrs["role"] != "switch" || sw.Fields.Status != "planned" || len(sw.Fields.IPs) != 0 {
		t.Errorf("switch = %+v", sw)
	}
	vm := obs["vm:netbox:20"]
	if vm.Kind != connectors.KindVM || vm.Fields.Attrs["cluster"] != "pve-cluster" || vm.Fields.Attrs["site"] != "" {
		t.Errorf("vm = %+v", vm)
	}
	if want := []string{"192.0.2.20", "2001:db8::20"}; !slices.Equal(vm.Fields.IPs, want) {
		t.Errorf("vm ips = %v", vm.Fields.IPs)
	}
	if !slices.Equal(vm.Fields.MACs, []string{"02:00:00:00:00:20"}) || !slices.Equal(vm.Fields.Tags, []string{"netbox:prod", "netbox:db"}) {
		t.Errorf("vm macs/tags = %v / %v", vm.Fields.MACs, vm.Fields.Tags)
	}
}

func TestSyncPagination(t *testing.T) {
	const total = 450
	r := base()
	r["/api/dcim/devices/"] = route{body: func(off int) string {
		var items []string
		for i := off; i < off+pageSize && i < total; i++ {
			items = append(items, fmt.Sprintf(`{"id":%d,"name":"host-%d","status":{"value":"active"}}`, i+1, i+1))
		}
		next := "null"
		if off+pageSize < total {
			next = `"http://nb.example.test/api/dcim/devices/?next"`
		}
		return fmt.Sprintf(`{"count":%d,"next":%s,"results":[%s]}`, total, next, strings.Join(items, ","))
	}}
	s := newServer(t, r)
	res, err := build(t, s.URL).Sync(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	devices := 0
	for _, o := range res.Observations {
		if o.Kind == connectors.KindDevice {
			devices++
		}
	}
	if devices != total {
		t.Errorf("devices = %d, want %d", devices, total)
	}
}

func TestSyncPartialPermissions(t *testing.T) {
	r := base()
	r["/api/ipam/ip-addresses/"] = route{status: 403}
	r["/api/ipam/prefixes/"] = route{status: 404}
	s := newServer(t, r)
	res, err := build(t, s.URL).Sync(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Warnings) != 2 || len(res.Networks) != 0 {
		t.Errorf("warnings = %v, networks = %d", res.Warnings, len(res.Networks))
	}
	// Primary IPs still come from the device object.
	if ips := byKey(res)["device:netbox:10"].Fields.IPs; !slices.Equal(ips, []string{"192.0.2.10"}) {
		t.Errorf("ips = %v", ips)
	}
}

func TestSyncUnauthorizedFails(t *testing.T) {
	r := base()
	r["/api/dcim/devices/"] = route{status: 401}
	s := newServer(t, r)
	if _, err := build(t, s.URL).Sync(context.Background()); err == nil {
		t.Error("want error on 401")
	}
}

func TestNewValidation(t *testing.T) {
	inst := connectors.Instance{BaseURL: "http://nb.example.test:8080"}
	if _, err := New(inst, connectors.Credential{}); err == nil {
		t.Error("want error without api_token")
	}
	if _, err := New(inst, connectors.Credential{Type: secrets.TypeAPIToken, Value: secrets.NewValue([]byte(" "))}); err == nil {
		t.Error("want error for empty token")
	}
	reg := connectors.NewRegistry()
	Register(reg)
	if err := reg.Validate(Type, "http://nb.example.test:8080", nil, secrets.TypeAPIToken, true); err != nil {
		t.Errorf("validate: %v", err)
	}
}

func TestNormMAC(t *testing.T) {
	for in, want := range map[string]string{
		"02:00:00:00:00:AB": "02:00:00:00:00:ab", "02-00-00-00-00-ab": "02:00:00:00:00:ab",
		"020000000001": "02:00:00:00:00:01", "": "", "zz:00:00:00:00:00": "", "02:00:00": "",
	} {
		if got := normMAC(in); got != want {
			t.Errorf("normMAC(%q) = %q, want %q", in, got, want)
		}
	}
}
