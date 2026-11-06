# ADR-0017: Semaphore migration (source, mapping, credentials, cutover)

- Status: accepted
- Date: 2026-11-05

## Context
M7 implements MIG-1..9. The source installation is Semaphore v2.17 on MySQL 8.0. Hard rules: Semaphore is never modified (only stopped/disabled at cutover after explicit confirmation), and a full backup must exist before any access to its data. The source data set is small (one project, a handful of keys, inventories, templates and schedules) but contains encrypted credentials. ACP already holds the Git project and Git-synced inventories (M3), which are newer than Semaphore's static inline inventories.

## Decision
- **Source = restored backup copy.** `scripts/backup-semaphore.sh` writes `/opt/backups/semaphore-<timestamp>/` (consistent `mysqldump --single-transaction`, `config.json`, data dirs, `SHA256SUMS`). `scripts/migrate-semaphore.sh` restores the dump into a throwaway MySQL container (tmpfs, removed on exit) and runs `acp migrate semaphore` against it. The live Semaphore DB is never queried by the tool. The CLI requires `--backup-dir`, verifies `SHA256SUMS` and reads `config.json` from there (MIG-2).
- **Reader:** `internal/migrate/semaphore` reads the source with `database/sql` + `go-sql-driver/mysql` (read-only queries only). Other Semaphore dialects (Postgres, BoltDB) are out of scope for v1.0; the reader is behind an interface.
- **Modes:** `--dry-run` (default) prints the mapping report (MIG-5); `--apply` imports; `--compare` prints old/new counts and optionally queues one check-mode run per migrated template (MIG-6). Mapping results are stored in `migration_mappings (source, kind, source_id → acp_id)`, which makes `--apply` idempotent: already mapped objects are skipped, never duplicated.
- **Mapping:**
  - project → existing workspace (`--workspace`, default `default`);
  - repository → existing ACP project with the same normalized Git URL; local-path repositories are reported as unmappable;
  - access keys → secrets: `ssh` → `ssh_key` (a passphrase-protected key is decrypted in process and stored unencrypted inside the ACP envelope; the SSH login is reported and set as `ansible_user` extra var on templates that use the key), `login_password` → `password`, `none` → skipped; names keep their source names, prefixed `semaphore: `;
  - static inventories → archived DB inventories `semaphore-<name>` (lossless); templates are re-pointed to existing Git inventories via `--inventory-map <semaphore-id>=<acp-inventory>`; the dry run suggests targets by host overlap. Inventory SSH/become keys become the template's `ssh_secret_id` / `become_secret_id`;
  - environments → template `extra_vars` (JSON) and `env_vars` (ENV); secret environment values → reported;
  - templates → templates (`arguments` parsed for `--limit`, `--tags`, `--skip-tags`, `-v…`, `--check`, `--diff`; anything else reported), survey vars → survey, vault refs → vault identities;
  - schedules → schedules, always `enabled = false` (MIG-7);
  - users → reported only (ACP users are created just in time via OIDC);
  - task history → not imported (stays in the backup and in Semaphore).
- **Key decryption (MIG-4):** secrets are decrypted with `access_key_encryption` from the backup's `config.json` (AES-256-GCM), held in memory only, and immediately re-encrypted through `secrets.Service`. Neither plaintext nor the source encryption key is ever logged or printed; reports show only names, types and fingerprints.
- **Template SSH key wins (MIG-9):** when a template has an SSH secret, the worker passes `-e ansible_ssh_private_key_file=<bundle key path>` in addition to `--private-key`, because inventory/group vars (e.g. `~/.ssh/ansible_infra` in the Ansible repo) beat `--private-key`. Extra vars beat every other variable source.
- **Cutover (MIG-8):** documented runbook in `docs/migration-from-semaphore.md`: final backup → dry run → apply → compare → check-mode runs → user confirmation → `docker compose stop` + restart policy `no` for Semaphore (data, containers, volumes and images stay). Rollback = start Semaphore again; ACP keeps working in parallel.

## Consequences
- One new dependency (MySQL driver), used only by the migration subcommand.
- The migration can be repeated safely; a fresh backup is the only prerequisite.
- Users must log in once via OIDC; their Semaphore account is not carried over.
- Semaphore data that has no ACP equivalent is listed in the report instead of being silently dropped.
