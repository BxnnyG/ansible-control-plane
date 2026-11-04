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
- Handlers touching resources call `Authorizer.Authorize` with a `Target` (refs and/or hosts). A `ScopeResolver` decides; any target part outside the scopes denies the whole action. An empty target is denied for scoped memberships (fail closed). `RefResolver` matches kind and id and cannot expand hosts; it is the fallback. Production uses `InventoryResolver` (see "Inventory scopes" below).
- Routes are registered through `Authorizer.Handle` (workspace permission) or `HandleAuthenticated` (caller's own data). A test walks the router and fails for any `/api/v1` route without a declaration; `/api/v1/me` is allowlisted.

## Inventory scopes (implementation notes, M3)
- `policy.InventoryResolver` is wired in `cmd/acp` with the inventory service as `GraphSource`. It expands scopes per inventory into a `policy.Access`: `All` (inventory scope) or the scoped groups plus all descendant groups and their hosts. Group links are a multi-parent graph and may contain cycles; the traversal is visited-set based.
- Group scopes are mapped to an inventory through the loaded graph (scopes naming groups of other inventories, deleted groups or unparseable ids simply match nothing). The graph is loaded only when group scopes exist and no inventory scope already grants everything.
- Network scopes match no inventory content until the connectors (M5) can resolve networks to hosts. A membership with only network scopes therefore sees no inventories; this is deliberate and fail closed.
- Targets: group refs and host names are only meaningful inside one inventory, so `Target.InventoryID` (or exactly one inventory ref) is required for them; hosts are explicit names, never patterns. `Authorizer.AuthorizeHosts(ctx, principal, workspace, perm, inventory, hosts)` checks a limit set (used by M4 runs): every host must be in scope, unknown hosts and empty lists are denied, and a whole-inventory run needs an inventory ref and therefore an inventory scope.
- Reads: inventory list and get are filtered (partial access shows visible counts only and hides the source project, path and branch); groups, hosts and the render preview contain only accessible groups and hosts. References to groups outside the scope (`parent_ids`, `group_ids`) are removed from responses. The preview of a scoped member merges host variables from the visible groups only, so variables of out-of-scope groups, including the implicit `all` group, are never disclosed; it can differ from what Ansible computes on the full inventory.
- Writes: inventory-level changes (create, settings, delete, import) need an inventory scope or no scope. Group and host changes need the touched group/host in scope. Creating a group or host requires at least one parent/group and all of them in scope. Moving or re-parenting is denied when a target is out of scope; memberships outside the scope are preserved (the client manages only the in-scope part). Deleting a group requires all its parents in scope (the scope root cannot be deleted); deleting or renaming a host requires all its groups in scope; adding a membership requires both the group and the host in scope, so an out-of-scope host cannot be pulled in.
- Denials are 403 and audited as `authz.denied`; an unknown inventory is indistinguishable from an out-of-scope one for scoped members.
- Limitation: scope checks and the mutation are separate steps; a concurrent change in between is possible. Clients should send `If-Match` for conflict detection.

## IdP role sync
- Zitadel roles acp-admin, acp-editor, acp-operator, acp-viewer map to the same-named role in workspace `default`; the highest wins. The roles claim (`ACP_OIDC_ROLES_CLAIM`) may be an object keyed by role name (Zitadel) or an array.
- `memberships.source` is `local` (default) or `idp`. The sync creates, updates and deletes only `idp` memberships and never touches `local` ones, so owners and manually managed users are unaffected. The IdP never grants owner.
- Changes are written to the audit log as `membership.idp_sync`.
- If a login leaves the user without any membership, it is rejected (`/login?error=no_access`, audited as `auth.login_denied`).

## Consequences
Clear, testable model; custom roles are a later extension.

## Alternatives
Free-form ACLs (Rundeck style – powerful but hard to reason about); AWX-style object roles on every object (complex UI).
