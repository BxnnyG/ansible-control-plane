// Package policy is the central authorization layer (ADR-0007): fixed
// workspace roles, a typed permission matrix and optional resource scopes.
//
// Every /api/v1 handler declares the permission it needs through
// Authorizer.Handle; authentication (auth.Service.Require) runs first.
package policy

import "sort"

// Permission is a single capability that a role may hold.
type Permission string

// Permissions known to the platform.
const (
	WorkspaceRead      Permission = "workspace.read"
	WorkspaceManage    Permission = "workspace.manage"
	WorkspaceDelete    Permission = "workspace.delete"
	MembersRead        Permission = "members.read"
	MembersManage      Permission = "members.manage"
	AuditRead          Permission = "audit.read"
	SecretsMetaRead    Permission = "secrets.meta.read"
	SecretsWrite       Permission = "secrets.write"
	ProjectRead        Permission = "project.read"
	ProjectWrite       Permission = "project.write"
	ProjectSync        Permission = "project.sync"
	InventoryRead      Permission = "inventory.read"
	InventoryWrite     Permission = "inventory.write"
	TemplateRead       Permission = "template.read"
	TemplateWrite      Permission = "template.write"
	RunRead            Permission = "run.read"
	RunExecute         Permission = "run.execute"
	RunCancel          Permission = "run.cancel"
	ScheduleRead       Permission = "schedule.read"
	ScheduleWrite      Permission = "schedule.write"
	ConnectorRead      Permission = "connector.read"
	ConnectorWrite     Permission = "connector.write"
	FleetRead          Permission = "fleet.read"
	FleetManage        Permission = "fleet.manage"
	FleetRemediate     Permission = "fleet.remediate"
	VaultRead          Permission = "vault.read"
	VaultManage        Permission = "vault.manage"
	VaultDecrypt       Permission = "vault.decrypt"
	NotificationRead   Permission = "notification.read"
	NotificationManage Permission = "notification.manage"
)

// Roles in ascending order of privilege.
const (
	RoleViewer   = "viewer"
	RoleOperator = "operator"
	RoleEditor   = "editor"
	RoleAdmin    = "admin"
	RoleOwner    = "owner"
)

// workspaceWide permissions can never be exercised through a scoped
// membership: they affect the workspace as a whole, not a subset of hosts.
var workspaceWide = map[Permission]bool{
	WorkspaceRead:   false, // reading the workspace itself is allowed when scoped
	WorkspaceManage: true, WorkspaceDelete: true,
	MembersRead: true, MembersManage: true,
	AuditRead: true, ConnectorRead: true, ConnectorWrite: true, SecretsWrite: true, SecretsMetaRead: true,
	VaultRead: true, VaultManage: true, VaultDecrypt: true,
	NotificationRead: true, NotificationManage: true,
}

// IsWorkspaceWide reports whether the permission requires an unscoped membership.
func IsWorkspaceWide(p Permission) bool { return workspaceWide[p] }

// Viewers read everything that is not administrative. members.read,
// connector.read, audit.read and secrets.meta.read are granted by higher roles.
var viewerPerms = []Permission{
	WorkspaceRead, ProjectRead, InventoryRead, TemplateRead, RunRead, ScheduleRead, FleetRead,
}

var matrix = buildMatrix()

func buildMatrix() map[string]map[Permission]bool {
	grant := func(base map[Permission]bool, add ...Permission) map[Permission]bool {
		m := make(map[Permission]bool, len(base)+len(add))
		for k := range base {
			m[k] = true
		}
		for _, p := range add {
			m[p] = true
		}
		return m
	}
	viewer := grant(nil, viewerPerms...)
	operator := grant(viewer, RunExecute, RunCancel, ProjectSync, FleetRemediate)
	editor := grant(operator, SecretsMetaRead, ProjectWrite, InventoryWrite, TemplateWrite, ScheduleWrite, VaultRead, FleetManage, NotificationRead)
	admin := grant(editor, SecretsWrite, WorkspaceManage, MembersRead, MembersManage, AuditRead, ConnectorRead, ConnectorWrite, VaultManage, VaultDecrypt, NotificationManage)
	owner := grant(admin, WorkspaceDelete)
	return map[string]map[Permission]bool{
		RoleViewer: viewer, RoleOperator: operator, RoleEditor: editor, RoleAdmin: admin, RoleOwner: owner,
	}
}

// ValidRole reports whether r is one of the fixed roles.
func ValidRole(r string) bool { _, ok := matrix[r]; return ok }

// Allowed reports whether the role holds the permission, ignoring scopes.
func Allowed(role string, p Permission) bool { return matrix[role][p] }

// Permissions returns the sorted permissions of a role. If scoped is true,
// workspace-wide permissions are omitted (they need an unscoped membership).
func Permissions(role string, scoped bool) []Permission {
	out := []Permission{}
	for p := range matrix[role] {
		if scoped && IsWorkspaceWide(p) {
			continue
		}
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// CanAssignRole reports whether an actor with role actor may change a
// membership from role from to role to. An empty from means a new membership,
// an empty to means removal. Only owners may grant or revoke the owner role.
func CanAssignRole(actor, from, to string) bool {
	if !Allowed(actor, MembersManage) {
		return false
	}
	if (from != "" && !ValidRole(from)) || (to != "" && !ValidRole(to)) || (from == "" && to == "") {
		return false
	}
	if (from == RoleOwner || to == RoleOwner) && actor != RoleOwner {
		return false
	}
	return true
}
