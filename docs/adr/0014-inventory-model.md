# ADR-0014: Inventory model – DB-only and Git-synced

- Status: accepted
- Date: 2026-11-04

## Context
INV-1..4/6 require inventories managed in the UI, stored either only in the DB or synchronized with files in a project, plus import of existing INI/YAML inventories and a rendered preview.

## Decision
- One relational model for both modes: `inventories`, `inventory_groups` (tree via parent id, vars as JSONB), `inventory_hosts` (name, vars JSONB), `inventory_group_hosts`. Vars are validated YAML in the API and stored as JSON; vault-encrypted values are kept as opaque `!vault` strings and never decrypted for storage.
- Mode `git`: the inventory is bound to a project, a path (INI or YAML file, optionally `group_vars/`/`host_vars/` next to it) and a write branch. Sync parses the file at the project revision into the DB model. UI edits are serialized back to YAML (format of the source is kept where possible: INI stays INI) and committed with the acting user as author and a `Signed-off-by` trailer, then pushed with a normal (non-force) push.
- Write branch is configurable per inventory; default `acp/<inventory-slug>`. If the remote branch has moved, the app rebases nothing automatically: it re-syncs, shows a conflict and requires the user to re-apply. Every inventory row carries the base revision it was edited against (optimistic locking).
- Rendering produces the `ansible-inventory --list` JSON shape in Go; previews mask vault strings and secret references.
- Inventory groups and hosts back the policy `ScopeResolver` (ADR-0007) for inventory and group scopes.

## Consequences
One code path for editing, rendering and authorization regardless of mode. Dynamic inventory plugins are out of scope for v1.0 (connectors cover that in M5).

## Alternatives
Storing raw files only (no structured editing, no scope checks); auto-merging remote changes (risk of silent data loss).

## Implementation notes (M3 step 5)
- Group hierarchy is stored as links (`inventory_group_links`) instead of a single `parent_id`, because Ansible allows a group to be a child of several groups and imports must not lose that. The API exposes `parent_ids`; cycles are rejected.
- Vault values are stored as the object `{"__ansible_vault": "<envelope>"}` (the same marker `ansible-inventory --list` uses) and written back as `!vault` scalars; INI output cannot represent them and fails explicitly.
- `group_vars/` and `host_vars/` next to the inventory file are merged into the stored group and host vars on import (files win over inline vars, like Ansible); entries without a matching group or host and whole-file vault files are reported as warnings.
- `ungrouped` is implicit and never stored; `all` is stored only when it carries vars. Host ranges (`web[01:03]`) are expanded on import.
- Edits use `If-Match: "<version>"` (optional, `*` skips the check); every content change bumps `inventories.version` and the new value is returned as `ETag`. A mismatch yields 412 `version_conflict`.
- Previews (`GET .../render`) always mask vault values as `<vault>`; `?export=true` keeps variables on their defining host or group.
- `Service.HostsInGroups`, `GroupsOfHost` and `HostNames` are the queries for the later `ScopeResolver`.

## Implementation notes (M3 write-back)
- `projects.Service.WriteBack` is the generic write path (reused by the vault feature): under the project lock it fetches the write branch, checks that the files it is about to write (plus caller-declared watch paths) are unchanged on the branch since the base revision the user edited, writes confined to the checkout (no symlinks, no `.git`), commits as the acting user with `Signed-off-by`, and pushes without force. A moved branch or a lost push race is a `ConflictError` (HTTP 409 `git_conflict` with the changed paths); nothing is merged. A missing branch is created from the base revision. Afterwards the cache clone is reset to the last synced revision without network access.
- `POST .../inventories/{id}/commit` (permission `inventory.write`, `If-Match`) serializes the stored model while the inventory row is locked and records the new commit as `base_sha`. It is a 200 no-op when nothing differs. `GET .../diff` previews the same plan as a unified diff against the base revision (vault values stay encrypted envelopes). The audit entry `inventory.commit` holds branch, sha and paths, never content.
- The plan re-reads the files at the base revision: the inventory file is rewritten only if its inline content changed (format kept; INI with vault values yields 422 `vault_requires_yaml`), and `group_vars/`/`host_vars/` files are rewritten only if their variables changed. Variables go back to the file that defined them (last file wins), new variables of a group or host with vars files go to its last file, all others stay inline. No schema change was needed: origins are derived from the base revision instead of being stored at import.
- Rewriting regenerates formatting and drops comments in the touched files.
