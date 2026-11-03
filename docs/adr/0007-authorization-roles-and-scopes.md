# ADR-0007: Authorization with workspace roles and resource scopes

- Status: proposed
- Date: 2026-11-03

## Context
Access must be grantable for parts of the infrastructure (e.g. only the server network), not all-or-nothing.

## Decision
- Fixed roles per workspace: owner, admin, editor, operator, viewer.
- A membership can be restricted by scopes: networks, inventories, inventory groups. No scope = whole workspace.
- Host-touching actions resolve the target to an effective host set; the action is denied if any host is out of scope (no silent filtering for runs).
- Central policy package with table-driven tests; every handler declares its required permission.

## Consequences
Clear, testable model; custom roles are a later extension.

## Alternatives
Free-form ACLs (Rundeck style – powerful but hard to reason about); AWX-style object roles on every object (complex UI).
