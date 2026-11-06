# Migrating from Semaphore UI

Operator runbook for `acp migrate semaphore` (ADR-0017, PLAN.md MIG-1..9). The
migration never modifies Semaphore: it reads a restored copy of a backup. The
only change to the source system is the optional cutover (stop and disable),
which you perform yourself after the checks below.

## What is migrated

| Semaphore | ACP | Notes |
|---|---|---|
| Access keys (`ssh`, `login_password`) | Secrets named `semaphore: <name>` | Decrypted in memory, re-encrypted by the ACP secrets store. A passphrase on an SSH key is removed; the key is stored unprotected inside the ACP envelope. Keys of type `none` are skipped. |
| Keys used as vault passwords | Secret (`vault_password`) plus vault identity `semaphore-<slug>` | If your vault files use a vault-id label, rename the identity afterwards. |
| Static inventories | Archived DB inventory `semaphore-<slug>` (every one, always) | Lossless copy of hosts, groups and variables. |
| Templates | Templates | Project, inventory, SSH/become secret, vault identities, environment (extra vars and env vars), survey and parsed arguments (`--limit`, `--tags`, `--skip-tags`, `-v`, `--check`, `--diff`). Other arguments are listed in the report. The SSH key login becomes the `ansible_user` extra var. |
| Schedules | Schedules | Always created **disabled**. |
| Repositories | Matched to existing ACP projects by normalized Git URL | Add the project with `acp admin project` first. Local-path repositories cannot be mapped. |
| Users, project members | Reported only | Users log in once through OIDC; roles are assigned in ACP. |
| Task history | Not migrated | Stays in the backup and in Semaphore. |
| Secret environment values | Not migrated | Listed in the report; recreate them by hand. |

## Runbook

All commands run on the host from the repository directory. The helper script
restores the backup into a throwaway MySQL container and runs the migration
inside the ACP compose project; the live Semaphore database is never queried.

1. **Backup** (read-only on Semaphore, writes outside the repo):

   ```sh
   scripts/backup-semaphore.sh
   ```

   It prints the backup directory `/opt/backups/semaphore-<timestamp>/`. Use a
   fresh backup right before the real import so that nothing changed in between.

2. **Dry run** (default, writes nothing):

   ```sh
   scripts/migrate-semaphore.sh /opt/backups/semaphore-<timestamp> --workspace default
   ```

   Read the report: `new` objects will be created, `exists` are already imported,
   `unmappable` objects are listed with the reason. The `Inventory suggestions`
   section proposes existing ACP inventories by host overlap.

3. **Inventory map** (optional): re-point templates from the archived Semaphore
   inventory to an ACP (Git-synced) inventory with one flag per inventory:

   ```sh
   ... --inventory-map <semaphore-id>=<acp-inventory-slug>
   ```

   The static inventory is archived as `semaphore-<slug>` anyway, only the
   template uses the mapped inventory.

4. **Apply**:

   ```sh
   scripts/migrate-semaphore.sh /opt/backups/semaphore-<timestamp> --apply --inventory-map 1=prod
   ```

   Objects are created in dependency order (secrets, vault identities,
   inventories, templates, schedules). Every created object is recorded in
   `migration_mappings` and in the audit log (actor `cli`, no secret values).
   Results per object: `created`, `exists`, `conflict`, `failed`, `skipped`.
   - The command is idempotent: run it again and nothing new is created;
     existing objects are reported as `exists`.
   - If an ACP object with the same name exists and is not mapped, it is
     reported as `conflict`; the object and everything depending on it
     (for example the templates using a conflicting secret) are skipped. Nothing
     is ever overwritten. Rename or remove the ACP object (or the source object)
     and re-run.
   - Exit code is non-zero when there are conflicts or failures.
   - Use `--json` for machine-readable output.

5. **Compare**:

   ```sh
   scripts/migrate-semaphore.sh /opt/backups/semaphore-<timestamp> --compare
   ```

   Prints source count, mapped count and present ACP count per kind (secrets,
   vault identities, inventories, templates, schedules) and lists missing
   objects with the reason. The exit code is non-zero when the counts do not
   match, so it can gate the cutover.

6. **Check-mode runs**:

   ```sh
   scripts/migrate-semaphore.sh /opt/backups/semaphore-<timestamp> --compare --check-runs
   ```

   Queues one `--check` run per migrated template and prints the run ids
   without waiting. Review the results in the ACP UI (or with
   `acp admin run show`) and compare with Semaphore's behavior. The project must
   be synced and the template must allow check overrides. Templates that cannot
   be queued are listed with the error.

7. **Enable schedules** in ACP only when Semaphore no longer runs the same
   schedules, otherwise jobs run twice.

## Cutover

Only after you have confirmed the comparison and the check runs:

```sh
docker compose stop            # in the Semaphore compose directory
docker update --restart=no <semaphore containers>
```

Semaphore is stopped and its restart policy set to `no`, so it does not return
after a reboot. Data, containers, volumes and images stay in place. Note the
previous restart policy (`docker inspect --format '{{.HostConfig.RestartPolicy.Name}}'`)
before changing it.

If Semaphore published port 80, ACP can take it over afterwards so old links
land on ACP: set `ACP_HTTP_PORT=80` in `.env` and run `docker compose up -d`
(plain HTTP only redirects to `ACP_PUBLIC_URL`).

Deploy the ACP version that contains the migration (app **and** worker) before
queuing check runs; an older worker does not force the template SSH key
(MIG-9) and the runs fail with misleading SSH errors.

## Rollback

If ACP took over port 80, set `ACP_HTTP_PORT` back to its previous value and
run `docker compose up -d` for ACP first. Then start Semaphore again
(`docker compose start` in its directory) and restore the previous restart
policy with `docker update --restart=<policy> <containers>`. ACP keeps working in parallel; the
migration did not change Semaphore. To redo the import, take a new backup and
run the steps again: already mapped objects are kept, new ones are added.

## Notes

- **Encryption key precedence:** Semaphore lets the environment variable
  `SEMAPHORE_ACCESS_KEY_ENCRYPTION` override `access_key_encryption` from
  `config.json`. The backup script records the container's effective value in
  `semaphore-effective-config.json`; when that file is present its value wins
  over `config.json`. If decryption fails, the key is reported as `unmappable`
  with the reason; keys and values are never printed.
- Names are trimmed of surrounding whitespace before they are used in ACP.
- The Ansible repository may set `private_key_file` or
  `ansible_ssh_private_key_file`; the worker passes the template's SSH key as an
  extra var so that it wins (MIG-9).
