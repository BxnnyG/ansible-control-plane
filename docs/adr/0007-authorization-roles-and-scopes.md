# ADR-0007: Authorization with workspace roles and resource scopes

- Status: accepted
- Date: 2026-11-03

## Context
Access must be grantable for parts of the infrastructure (e.g. only the server network), not all-or-nothing.

## Decision
- Fixed roles per workspace: owner, admin, editor, operator, viewer.
- A membership can be restricted by scopes: networks, inventories, inventory groups. No scope = whole workspace.
- Host-touching actions resolve the target to an effective host set; the action is denied if any host is out of scope (no silent filtering for runs).
- Central policy package with table-driven tests; every handler declares its required permission.

## Permission matrix
Permissions are typed constants (`internal/policy`). Roles are cumulative:

- viewer: workspace.read, project.read, inventory.read, template.read, run.read, schedule.read
- operator: viewer + run.execute, run.cancel
- editor: operator + secrets.meta.read, secrets.write, project.write, inventory.write, template.write, schedule.write
- admin: editor + workspace.manage, members.read, members.manage, audit.read, connector.read, connector.write
- owner: admin + workspace.delete

members.read and connector.read are not viewer permissions; they are granted from admin upwards together with their write counterparts.
Admins may not grant, change or revoke the owner role (`policy.CanAssignRole`).

## Scope evaluation
- A membership without scopes covers the whole workspace.
- Workspace-wide permissions (workspace.*, members.*, audit.read, connector.write, secrets.write) require an unscoped membership; scoped memberships are denied.
- `policy.Require(perm)` is the workspace-level gate: non-members get 404 (no existence leak), missing permission 403 (audited as `authz.denied`).
- Handlers touching resources call `Authorizer.Authorize` with a `Target` (refs and/or hosts). A `ScopeResolver` decides; any target part outside the scopes denies the whole action. An empty target is denied for scoped memberships (fail closed). The shipped `RefResolver` matches kind and id and cannot expand hosts; the inventory-backed resolver arrives in M3.
- Routes are registered through `Authorizer.Handle` (workspace permission) or `HandleAuthenticated` (caller's own data). A test walks the router and fails for any `/api/v1` route without a declaration; `/api/v1/me` is allowlisted.

## IdP role sync
- Zitadel roles acp-admin, acp-editor, acp-operator, acp-viewer map to the same-named role in workspace `default`; the highest wins. The roles claim (`ACP_OIDC_ROLES_CLAIM`) may be an object keyed by role name (Zitadel) or an array.
- `memberships.source` is `local` (default) or `idp`. The sync creates, updates and deletes only `idp` memberships and never touches `local` ones, so owners and manually managed users are unaffected. The IdP never grants owner.
- Changes are written to the audit log as `membership.idp_sync`.
- If a login leaves the user without any membership, it is rejected (`/login?error=no_access`, audited as `auth.login_denied`).

## Consequences
Clear, testable model; custom roles are a later extension.

## Alternatives
Free-form ACLs (Rundeck style – powerful but hard to reason about); AWX-style object roles on every object (complex UI).
