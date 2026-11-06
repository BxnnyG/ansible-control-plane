package server

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/BxnnyG/ansible-control-plane/internal/db/dbgen"
	"github.com/BxnnyG/ansible-control-plane/internal/fleet"
	"github.com/BxnnyG/ansible-control-plane/internal/inventory"
	"github.com/BxnnyG/ansible-control-plane/internal/policy"
)

func invScope(id string) dbgen.MembershipScope {
	return dbgen.MembershipScope{Kind: policy.KindInventory, RefID: id}
}

type fakeAdder struct {
	calls []fleet.AddInput
	hosts []uuid.UUID
	err   error
	vis   []bool // whether a visibility filter was passed
}

func (f *fakeAdder) AddToInventory(_ context.Context, _, hostID uuid.UUID, in fleet.AddInput, v fleet.Visible) (fleet.AddResult, error) {
	f.calls = append(f.calls, in)
	f.hosts = append(f.hosts, hostID)
	f.vis = append(f.vis, v != nil)
	if f.err != nil {
		return fleet.AddResult{}, f.err
	}
	name := in.Name
	if name == "" {
		name = "host-a"
	}
	return fleet.AddResult{Host: name, AnsibleHost: "192.0.2.10", Result: inventory.GroupChangeResult{Added: []string{name}, Version: 3}}, nil
}

func TestFleetAddToInventory(t *testing.T) {
	inv := uuid.NewString()
	body := `{"inventory_id":"` + inv + `","group":"web","name":"custom"}`
	path := "/hosts/" + uuid.NewString() + "/add-to-inventory"

	// Roles: fleet.read is not enough, inventory.write decides.
	for role, want := range map[string]int{policy.RoleViewer: http.StatusForbidden, policy.RoleOperator: http.StatusForbidden,
		policy.RoleEditor: http.StatusOK, policy.RoleAdmin: http.StatusOK} {
		e := newConnEnv(t, role)
		if rec := e.do("POST", path, body); rec.Code != want {
			t.Errorf("%s: status %d, want %d (%s)", role, rec.Code, want, rec.Body.String())
		}
	}

	// A network-scoped editor lacks inventory.write on the target inventory.
	e := newConnEnv(t, policy.RoleEditor, netScope(uuid.NewString()))
	if rec := e.do("POST", path, body); rec.Code != http.StatusForbidden || len(e.adder.calls) != 0 {
		t.Errorf("scoped editor: %d, calls %d", rec.Code, len(e.adder.calls))
	}
	// With an inventory scope on the target the action goes through, still limited to the visible hosts.
	e = newConnEnv(t, policy.RoleEditor, netScope(uuid.NewString()), invScope(inv))
	if rec := e.do("POST", path, body); rec.Code != http.StatusOK || len(e.adder.vis) != 1 || !e.adder.vis[0] {
		t.Errorf("scoped editor with inventory scope: %d vis %v", rec.Code, e.adder.vis)
	}

	e = newConnEnv(t, policy.RoleEditor)
	rec := e.do("POST", path, body)
	if rec.Code != http.StatusOK || len(e.adder.calls) != 1 {
		t.Fatalf("add: %d %s", rec.Code, rec.Body.String())
	}
	if c := e.adder.calls[0]; c.Group != "web" || c.Name != "custom" || c.InventoryID.String() != inv {
		t.Errorf("input: %+v", c)
	}
	if !strings.Contains(rec.Body.String(), `"ansible_host":"192.0.2.10"`) || rec.Header().Get("ETag") != `"3"` {
		t.Errorf("response: %s etag %s", rec.Body.String(), rec.Header().Get("ETag"))
	}
	if len(*e.audits) != 1 || !strings.Contains((*e.audits)[0], "fleet.host.add_to_inventory") {
		t.Errorf("audits: %v", *e.audits)
	}
	if e.adder.vis[0] {
		t.Error("unscoped caller got a visibility filter")
	}

	// Input and error mapping.
	for _, tt := range []struct {
		name, body string
		err        error
		want       int
	}{
		{"malformed", `{"group":`, nil, http.StatusBadRequest},
		{"missing inventory", `{"group":"web"}`, nil, http.StatusBadRequest},
		{"unknown field", `{"inventory_id":"` + inv + `","group":"web","x":1}`, nil, http.StatusBadRequest},
		{"not found", body, fleet.ErrNotFound, http.StatusNotFound},
		{"managed", body, fleet.ErrManaged, http.StatusConflict},
		{"name taken", body, fleet.ErrNameTaken, http.StatusConflict},
		{"invalid", body, fleet.ErrInvalid, http.StatusUnprocessableEntity},
		{"inventory missing", body, inventory.ErrNotFound, http.StatusNotFound},
		{"inventory invalid", body, inventory.ErrInvalid, http.StatusUnprocessableEntity},
	} {
		e := newConnEnv(t, policy.RoleEditor)
		e.adder.err = tt.err
		if rec := e.do("POST", path, tt.body); rec.Code != tt.want {
			t.Errorf("%s: status %d, want %d (%s)", tt.name, rec.Code, tt.want, rec.Body.String())
		}
	}

	// The managed filter is validated.
	e = newConnEnv(t, policy.RoleViewer)
	if rec := e.do("GET", "/hosts?managed=maybe", ""); rec.Code != http.StatusBadRequest {
		t.Errorf("managed=maybe: %d", rec.Code)
	}
	if rec := e.do("GET", "/hosts?managed=false", ""); rec.Code != http.StatusOK {
		t.Errorf("managed=false: %d", rec.Code)
	}
}
