package policy

import (
	"context"
	"testing"
)

func TestMatrix(t *testing.T) {
	all := []Permission{WorkspaceRead, WorkspaceManage, WorkspaceDelete, MembersRead, MembersManage, AuditRead,
		SecretsMetaRead, SecretsWrite, ProjectRead, ProjectWrite, ProjectSync, InventoryRead, InventoryWrite, TemplateRead,
		TemplateWrite, RunRead, RunExecute, RunCancel, ScheduleRead, ScheduleWrite, ConnectorRead, ConnectorWrite,
		VaultRead, VaultManage, VaultDecrypt, FleetRead, FleetManage, FleetRemediate}
	want := map[string][]Permission{
		RoleViewer:   {WorkspaceRead, ProjectRead, InventoryRead, TemplateRead, RunRead, ScheduleRead, FleetRead},
		RoleOperator: {RunExecute, RunCancel, ProjectSync, FleetRemediate},
		RoleEditor:   {SecretsMetaRead, ProjectWrite, InventoryWrite, TemplateWrite, ScheduleWrite, VaultRead, FleetManage},
		RoleAdmin:    {SecretsWrite, WorkspaceManage, MembersRead, MembersManage, AuditRead, ConnectorRead, ConnectorWrite, VaultManage, VaultDecrypt},
		RoleOwner:    {WorkspaceDelete},
	}
	order := []string{RoleViewer, RoleOperator, RoleEditor, RoleAdmin, RoleOwner}
	cum := map[Permission]bool{}
	for _, role := range order {
		for _, p := range want[role] {
			cum[p] = true
		}
		for _, p := range all {
			if got := Allowed(role, p); got != cum[p] {
				t.Errorf("role %s perm %s: got %v want %v", role, p, got, cum[p])
			}
		}
	}
	if Allowed("nobody", WorkspaceRead) || Allowed("", WorkspaceRead) {
		t.Error("unknown role must have no permissions")
	}
}

func TestPermissionsScoped(t *testing.T) {
	for _, p := range Permissions(RoleOwner, true) {
		if IsWorkspaceWide(p) {
			t.Errorf("scoped list contains workspace-wide %s", p)
		}
	}
	if len(Permissions(RoleOwner, false)) != 28 {
		t.Errorf("owner has %d permissions", len(Permissions(RoleOwner, false)))
	}
}

func TestCanAssignRole(t *testing.T) {
	tests := []struct {
		name            string
		actor, from, to string
		want            bool
	}{
		{"admin grants editor", RoleAdmin, "", RoleEditor, true},
		{"admin grants admin", RoleAdmin, "", RoleAdmin, true},
		{"admin grants owner", RoleAdmin, "", RoleOwner, false},
		{"admin demotes owner", RoleAdmin, RoleOwner, RoleViewer, false},
		{"admin removes owner", RoleAdmin, RoleOwner, "", false},
		{"owner grants owner", RoleOwner, "", RoleOwner, true},
		{"owner demotes owner", RoleOwner, RoleOwner, RoleAdmin, true},
		{"editor cannot manage", RoleEditor, "", RoleViewer, false},
		{"invalid target", RoleOwner, "", "root", false},
		{"nothing to do", RoleOwner, "", "", false},
		{"admin removes viewer", RoleAdmin, RoleViewer, "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := CanAssignRole(tc.actor, tc.from, tc.to); got != tc.want {
				t.Errorf("got %v want %v", got, tc.want)
			}
		})
	}
}

func TestRefResolver(t *testing.T) {
	scopes := []Scope{{KindNetwork, "net-1"}, {KindInventory, "inv-1"}}
	tests := []struct {
		name string
		t    Target
		want bool
	}{
		{"all in scope", Target{Refs: []Ref{{KindNetwork, "net-1"}, {KindInventory, "inv-1"}}}, true},
		{"one out of scope", Target{Refs: []Ref{{KindNetwork, "net-1"}, {KindNetwork, "net-2"}}}, false},
		{"kind mismatch", Target{Refs: []Ref{{KindInventory, "net-1"}}}, false},
		{"hosts unresolvable", Target{Refs: []Ref{{KindNetwork, "net-1"}}, Hosts: []string{"h1"}}, false},
		{"empty target fails closed", Target{}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := RefResolver{}.InScope(context.Background(), "ws", scopes, tc.t)
			if err != nil || got != tc.want {
				t.Errorf("got %v err %v want %v", got, err, tc.want)
			}
		})
	}
}
