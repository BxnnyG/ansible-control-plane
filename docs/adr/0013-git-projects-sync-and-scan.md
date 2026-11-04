# ADR-0013: Git projects – sync, cache and scan

- Status: accepted
- Date: 2026-11-04

## Context
Projects are Git repositories (GIT-1..3, GIT-5). The app must read them for detection (playbooks, roles, requirements, vars, inventories, vault content, `argument_specs`) and write back Git-synced inventories and vault changes (INV-3, VAULT-3). ADR-0012 mandates the git CLI.

## Decision
- The app keeps one working clone per project in a cache volume (`ACP_DATA_DIR/projects/<project-id>`), owned by uid 10001. Sync = `fetch` + hard reset of the cache clone to the remote ref; the cache is disposable and never the source of truth.
- Git credentials are secrets (ADR-0005) of type `ssh_key` or `password`/`token` (HTTPS). Keys are materialized per operation into a 0600 temp file on tmpfs and removed afterwards. `known_hosts` is stored per project (pinned on first successful connection after explicit operator confirmation of the fingerprint, TOFU with confirmation); `StrictHostKeyChecking=yes` afterwards. No credential helpers, `GIT_TERMINAL_PROMPT=0`, timeouts on every command.
- Every sync records a project revision (commit SHA, timestamp, scan result as JSONB). Scanning is pure Go over the checked-out tree, bounded in file size and count.
- Sync triggers: on demand, on schedule (per project interval), and webhook (Gitea/Forgejo HMAC `X-Gitea-Signature`, GitHub `X-Hub-Signature-256`, GitLab token header, generic HMAC). Webhook secrets are stored in the secret store.
- Installing `requirements.yml` (GIT-4) needs `ansible-galaxy` and runs in the worker; it moves to M4. M3 only detects requirements.

## Consequences
Scans are fast and need no Python in the app image. The cache volume must be backed up only optionally (it can be rebuilt).

## Alternatives
Scanning inside the worker (adds a round trip and a job type for a read-only task); bare mirrors plus worktrees (more moving parts, no benefit at this scale).

## Implementation notes (M3 step 3a)
- API under `/api/v1/workspaces/{workspace}/projects`: CRUD, `hostkey/scan` (nothing stored), `hostkey/confirm` (pins the key with the confirmed `SHA256:` fingerprint; the server is scanned again so only a currently presented key can be pinned), `test` (ls-remote), `sync`, `revisions`, `revisions/latest`.
- Permissions: `project.read` (viewer), `project.sync` (operator: test and sync), `project.write` (editor: create, update, delete, host key pinning).
- SSH URLs require a credential of type `ssh_key` and a pinned host key; sync and test fail with HTTP 409 `hostkey_not_pinned` otherwise. Changing the SSH host or port of a project clears the pin.
- Syncs of one project never overlap (HTTP 409 `sync_in_progress`). A revision is unique per (project, SHA); syncing an unchanged SHA refreshes its timestamp and scan.
- The scan is behind a `Scanner` interface (no-op until `internal/scan` is wired). Deleting a credential secret clears the project reference (`ON DELETE SET NULL`); the next sync then fails with an authentication error.
