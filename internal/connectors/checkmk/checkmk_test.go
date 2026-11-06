package checkmk

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/BxnnyG/ansible-control-plane/internal/connectors"
	"github.com/BxnnyG/ansible-control-plane/internal/secrets"
)

const (
	testUser   = "automation"
	testSecret = "fake-automation-secret-for-tests"
)

type server struct {
	*httptest.Server
	mu    sync.Mutex
	reqs  []string
	auths []string
}

func newServer(t *testing.T, status map[string]int) *server {
	t.Helper()
	files := map[string]string{
		"/prod/check_mk/api/1.0/version":                              "version.json",
		"/prod/check_mk/api/1.0/domain-types/host/collections/all":    "hosts.json",
		"/prod/check_mk/api/1.0/domain-types/service/collections/all": "services.json",
	}
	s := &server{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.reqs = append(s.reqs, r.Method+" "+r.URL.Path+"?"+r.URL.RawQuery)
		s.auths = append(s.auths, r.Header.Get("Authorization"))
		s.mu.Unlock()
		if code := status[r.URL.Path]; code != 0 {
			http.Error(w, "x", code)
			return
		}
		f, ok := files[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		b, err := os.ReadFile("testdata/" + f)
		if err != nil {
			t.Errorf("fixture: %v", err)
		}
		_, _ = w.Write(b)
	}))
	t.Cleanup(s.Close)
	return s
}

func build(t *testing.T, url string) connectors.Connector {
	t.Helper()
	pair, _ := json.Marshal(map[string]string{"key": testUser, "secret": testSecret})
	c, err := New(connectors.Instance{Name: "cmk", BaseURL: url, Config: map[string]any{connectors.CfgTimeoutSecs: 5}},
		connectors.Credential{Type: secrets.TypeAPIKeyPair, Value: secrets.NewValue(pair)})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestSyncMapsProblems(t *testing.T) {
	srv := newServer(t, nil)
	res, err := build(t, srv.URL+"/prod").Sync(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !res.ReplaceAlerts || len(res.Observations) != 0 || len(res.Networks) != 0 {
		t.Errorf("only alerts expected, replace set: %+v", res)
	}
	by := map[string]connectors.Alert{}
	for _, a := range res.Alerts {
		by[a.ExternalKey] = a
	}
	if len(by) != 6 {
		t.Fatalf("alerts: %d (%v)", len(by), by)
	}
	tests := []struct {
		key, sev, host, ip, svc, ack, down string
	}{
		{"host:web1.example.test", "critical", "web1.example.test", "192.0.2.10", "", "false", "false"},
		{"host:web2", "critical", "web2", "192.0.2.11", "", "true", "true"},
		{"host:odd", "unknown", "odd", "192.0.2.12", "", "false", "false"},
		{"svc:web1.example.test/Filesystem /", "critical", "web1.example.test", "192.0.2.10", "Filesystem /", "false", "false"},
		{"svc:web1.example.test/CPU load", "warning", "web1.example.test", "192.0.2.10", "CPU load", "true", "false"},
		{"svc:web2/Memory", "unknown", "web2", "192.0.2.11", "Memory", "false", "true"},
	}
	for _, tc := range tests {
		a, ok := by[tc.key]
		if !ok {
			t.Errorf("missing %s", tc.key)
			continue
		}
		if a.Severity != tc.sev || a.Host != tc.host || a.IP != tc.ip || a.Service != tc.svc ||
			a.Labels["acknowledged"] != tc.ack || a.Labels["in_downtime"] != tc.down || a.Summary == "" {
			t.Errorf("%s: %+v", tc.key, a)
		}
	}
	if a := by["host:web1.example.test"]; a.StartedAt.Unix() != 1767323045 || a.Labels["state"] != "DOWN" ||
		!strings.HasPrefix(a.Summary, "DOWN: CRIT") {
		t.Errorf("host alert: %+v", a)
	}
	if by["host:odd"].StartedAt.IsZero() != true {
		t.Error("zero last_state_change must stay zero")
	}
}

func TestRequestsAreGetOnlyAuthenticatedAndFiltered(t *testing.T) {
	srv := newServer(t, nil)
	if _, err := build(t, srv.URL+"/prod").Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(srv.reqs) != 2 {
		t.Fatalf("requests: %v", srv.reqs)
	}
	for i, r := range srv.reqs {
		if !strings.HasPrefix(r, "GET ") {
			t.Errorf("non-GET request %s", r)
		}
		if srv.auths[i] != "Bearer "+testUser+" "+testSecret {
			t.Errorf("auth header %q", srv.auths[i])
		}
		for _, want := range []string{"columns=name", "columns=state", "columns=plugin_output", "columns=last_state_change", "query=%7B%22op%22"} {
			if !strings.Contains(r, want) && !strings.Contains(r, strings.Replace(want, "columns=name", "columns=host_name", 1)) {
				t.Errorf("%s lacks %s", r, want)
			}
		}
	}
	if !strings.Contains(srv.reqs[1], "columns=description") || !strings.Contains(srv.reqs[1], "columns=host_address") {
		t.Errorf("service columns: %s", srv.reqs[1])
	}
}

func TestSiteAndAPIBaseURLsAreEquivalent(t *testing.T) {
	srv := newServer(t, nil)
	for _, base := range []string{srv.URL + "/prod", srv.URL + "/prod/", srv.URL + "/prod/check_mk/api/1.0", srv.URL + "/prod/check_mk/api/1.0/"} {
		if err := build(t, base).Test(context.Background()); err != nil {
			t.Errorf("%s: %v", base, err)
		}
	}
}

func TestSyncFailsWhenAnyRequestFails(t *testing.T) {
	for _, path := range []string{"/prod/check_mk/api/1.0/domain-types/host/collections/all", "/prod/check_mk/api/1.0/domain-types/service/collections/all"} {
		for _, code := range []int{401, 403, 500} {
			srv := newServer(t, map[string]int{path: code})
			res, err := build(t, srv.URL+"/prod").Sync(context.Background())
			if err == nil || len(res.Alerts) != 0 || res.ReplaceAlerts {
				t.Errorf("%s %d: must fail without result (err=%v)", path, code, err)
			}
			if err != nil && strings.Contains(err.Error(), testSecret) {
				t.Error("secret leaked in error")
			}
		}
	}
}

func TestTest(t *testing.T) {
	srv := newServer(t, nil)
	if err := build(t, srv.URL+"/prod").Test(context.Background()); err != nil {
		t.Fatal(err)
	}
	srv = newServer(t, map[string]int{"/prod/check_mk/api/1.0/version": 401})
	if err := build(t, srv.URL+"/prod").Test(context.Background()); err == nil || !strings.Contains(err.Error(), "401") {
		t.Errorf("want 401, got %v", err)
	}
	if err := build(t, srv.URL+"/nosuchsite").Test(context.Background()); err == nil {
		t.Error("unknown site must fail")
	}
}

func TestNewValidatesCredential(t *testing.T) {
	inst := connectors.Instance{BaseURL: "https://cmk.example.test/prod"}
	pair := func(k, s string) connectors.Credential {
		b, _ := json.Marshal(map[string]string{"key": k, "secret": s})
		return connectors.Credential{Type: secrets.TypeAPIKeyPair, Value: secrets.NewValue(b)}
	}
	bad := []connectors.Credential{
		{Type: secrets.TypeToken, Value: secrets.NewValue([]byte("x"))},
		{Type: secrets.TypeAPIKeyPair, Value: secrets.NewValue([]byte("nope"))},
		pair("", "s"), pair("u", ""), pair("u u", "s"), pair("u", "s\ns"),
	}
	for i, c := range bad {
		if _, err := New(inst, c); err == nil {
			t.Errorf("case %d must be rejected", i)
		}
	}
	if _, err := New(inst, pair("u", "s")); err != nil {
		t.Error(err)
	}
}

func TestSpecRegisters(t *testing.T) {
	reg := connectors.NewRegistry()
	Register(reg)
	spec, ok := reg.Spec(Type)
	if !ok || !spec.BaseURL || !spec.SecretRequired || len(spec.SecretTypes) != 1 || spec.SecretTypes[0] != secrets.TypeAPIKeyPair {
		t.Errorf("spec %+v", spec)
	}
	if err := reg.Validate(Type, "https://cmk.example.test/prod", nil, secrets.TypeAPIKeyPair, true); err != nil {
		t.Error(err)
	}
}
