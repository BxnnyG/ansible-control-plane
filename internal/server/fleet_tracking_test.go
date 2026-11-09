package server

import (
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/BxnnyG/ansible-control-plane/internal/policy"
)

func TestFleetIncludeUntrackedParam(t *testing.T) {
	tests := []struct {
		query       string
		code        int
		wantExclude bool
	}{
		{"", 200, true},
		{"?include_untracked=false", 200, true},
		{"?include_untracked=true", 200, false},
		{"?include_untracked=1", 200, false},
		{"?include_untracked=maybe", 400, false},
	}
	for _, tc := range tests {
		for _, path := range []string{"/hosts", "/alerts"} {
			e := newConnEnv(t, policy.RoleViewer)
			rec := e.do("GET", path+tc.query, "")
			if rec.Code != tc.code {
				t.Errorf("%s%s: status %d", path, tc.query, rec.Code)
				continue
			}
			if tc.code != 200 {
				continue
			}
			got := e.fleet.lastHosts.ExcludeUntracked
			if path == "/alerts" {
				got = e.fleet.lastAlert.ExcludeUntracked
			}
			if got != tc.wantExclude {
				t.Errorf("%s%s: ExcludeUntracked = %v", path, tc.query, got)
			}
		}
	}
}

func TestFleetTrackingAPI(t *testing.T) {
	id1, id2 := uuid.NewString(), uuid.NewString()
	tests := []struct {
		name         string
		role         string
		method, path string
		body         string
		code         int
	}{
		{"single", policy.RoleEditor, "PATCH", "/networks/" + id1, `{"tracked":false}`, 200},
		{"single missing flag", policy.RoleEditor, "PATCH", "/networks/" + id1, `{}`, 400},
		{"single bad id", policy.RoleEditor, "PATCH", "/networks/nope", `{"tracked":true}`, 404},
		{"single viewer", policy.RoleViewer, "PATCH", "/networks/" + id1, `{"tracked":true}`, 403},
		{"bulk set", policy.RoleEditor, "PATCH", "/networks", `{"ids":["` + id1 + `","` + id2 + `"],"tracked":true}`, 200},
		{"bulk only", policy.RoleEditor, "PATCH", "/networks", `{"track_only":["` + id1 + `"]}`, 200},
		{"bulk only empty", policy.RoleEditor, "PATCH", "/networks", `{"track_only":[]}`, 200},
		{"bulk both forms", policy.RoleEditor, "PATCH", "/networks", `{"ids":["` + id1 + `"],"tracked":true,"track_only":[]}`, 400},
		{"bulk neither", policy.RoleEditor, "PATCH", "/networks", `{"ids":["` + id1 + `"]}`, 400},
		{"bulk unknown field", policy.RoleEditor, "PATCH", "/networks", `{"tracked":true,"x":1}`, 400},
		{"bulk operator", policy.RoleOperator, "PATCH", "/networks", `{"ids":[],"tracked":true}`, 403},
		{"settings get", policy.RoleViewer, "GET", "/fleet/settings", "", 200},
		{"settings put", policy.RoleEditor, "PUT", "/fleet/settings", `{"new_networks_tracked":false}`, 200},
		{"settings put empty", policy.RoleEditor, "PUT", "/fleet/settings", `{}`, 400},
		{"settings put viewer", policy.RoleViewer, "PUT", "/fleet/settings", `{"new_networks_tracked":false}`, 403},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := newConnEnv(t, tc.role)
			if rec := e.do(tc.method, tc.path, tc.body); rec.Code != tc.code {
				t.Fatalf("status %d, want %d: %s", rec.Code, tc.code, rec.Body.String())
			}
		})
	}

	e := newConnEnv(t, policy.RoleEditor)
	e.do("PATCH", "/networks/"+id1, `{"tracked":false}`)
	if len(e.fleet.tracking.setIDs) != 1 || e.fleet.tracking.setTracked {
		t.Errorf("single call: %+v", e.fleet.tracking)
	}
	if len(*e.audits) != 1 || !strings.Contains((*e.audits)[0], "fleet.network.track") {
		t.Errorf("audits: %v", *e.audits)
	}
	e.fleet.tracking.unknown = true
	if rec := e.do("PATCH", "/networks/"+id2, `{"tracked":true}`); rec.Code != http.StatusNotFound {
		t.Errorf("unknown network: %d", rec.Code)
	}
	e = newConnEnv(t, policy.RoleEditor)
	e.do("PATCH", "/networks", `{"track_only":[]}`)
	if !e.fleet.tracking.onlyCalled {
		t.Error("track_only with an empty list must still untrack everything")
	}
	// A network-scoped membership may not change workspace-wide tracking.
	sc := newConnEnv(t, policy.RoleEditor, netScope(uuid.NewString()))
	if rec := sc.do("PATCH", "/networks/"+id1, `{"tracked":false}`); rec.Code != http.StatusForbidden {
		t.Errorf("scoped patch: %d", rec.Code)
	}
}
