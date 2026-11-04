# Projects, inventories and vault – operator guide

This guide covers the M3 features. Design rationale: ADR-0013 (projects),
ADR-0014 (inventories), ADR-0015 (vault), ADR-0007 (scopes).

## Permissions at a glance

| Action | Minimum role |
|---|---|
| View projects, inventories | viewer |
| Edit projects, inventories, host key pin, webhook | editor |
| Test connection, sync | operator |
| View secret / vault identity metadata | editor (workspace-wide membership) |
| Create/replace/delete secrets, manage vault identities, encrypt, rekey | admin |
| Decrypt vault content (audited per call) | admin |

Scoped memberships (inventory or group scope) only see their part of an
inventory. Commit and diff of a Git-synced inventory require a scope that
covers the whole inventory.

## 1. Credentials

Create a secret first (**Secrets → New secret**):

- `ssh_key` for SSH remotes: an unencrypted private key (passphrase-protected
  keys are rejected). Only the fingerprint is shown afterwards.
- `password` / `token` for HTTPS remotes (plus the HTTPS user name on the project).

Secret values are write-only; they are never returned by the API or logged.
For write-back (inventories, vault) the key needs **write** access on the Git
server (e.g. a deploy key with write permission).

## 2. Projects

1. **Projects → New project**: name, repository URL, default ref (branch/tag), credential.
2. **SSH host key**: *Scan host key*, compare the fingerprints with one you
   trust (e.g. `ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub` on the Git
   server), select exactly one and *Pin selected key*. Test and sync are
   blocked until a key is pinned; changing the SSH host or port clears the pin.
3. **Test connection**, then **Sync now**. Each sync records a revision (commit
   SHA) with a scan result: playbooks, roles (with survey preview from
   `meta/argument_specs.yml`), requirements, vars files, inventories, vault content.

Automatic sync:

- **Interval**: set *Sync interval* (minutes) on the project; the scheduler
  checks every 30 s and syncs at most two projects at a time.
- **Webhook**: choose the provider (`gitea` for Gitea/Forgejo, `github`,
  `gitlab`, `generic`) and *Enable*. The URL and secret are shown **once** –
  enter both in the Git server's webhook settings (push events, content type
  JSON). Only pushes to the project's ref trigger a sync. *Rotate* issues a
  new secret.

The checkout is a disposable cache under `ACP_DATA_DIR/projects/<id>`
(volume `acpdata`); it can be deleted and is rebuilt on the next sync.

## 3. Inventories

### Modes

- **db** – the inventory lives only in the database.
- **git** – bound to a project, a file path (INI or YAML) and a write branch
  (default `acp/<inventory-slug>`). Import reads the file from the latest
  synced revision; edits are committed back.

### Syncing from the host

Projects can be listed and synced without the UI (inside the app container):

```sh
docker compose exec app acp admin project list --workspace <ws>
docker compose exec app acp admin project sync --workspace <ws> --project <slug|id>
```

The sync uses the same service as the UI (per-project lock, revision, scan),
is recorded with trigger `manual`, audited as actor `cli`, and prints sha,
revision id and scan counts. Failures exit non-zero with a redacted error.

### Import

UI: inventory → *Import* (project + path from the latest scan). CLI (inside the
app container):

```sh
docker compose exec app acp admin inventory import --workspace <ws> \
  --project <slug|id> --path inventories/<name>/hosts.ini --name <name>
```

Import replaces the inventory content in one transaction. `group_vars/` and
`host_vars/` are merged **only from next to the inventory file** – the same
rule Ansible uses. Vars directories at the repository root are *not* applied
by Ansible to playbooks in subdirectories (e.g. `playbooks/day1/x.yml`) and are
not imported; place them next to the inventory file instead:

```
inventories/<name>/hosts.ini
inventories/<name>/group_vars/all.yml
inventories/<name>/host_vars/<host>.yml   # extension optional
```

### Editing and write-back

- Groups may have several parents; cycles are rejected. Vars are edited as YAML.
- Edits are optimistic: a stale version returns `412 version_conflict` – reload and re-apply.
- **Diff** shows the files that would be written; **Commit** pushes them to the
  write branch with the acting user as author and a `Signed-off-by` trailer.
  ACP never force-pushes and never merges: if the files changed on the branch
  since the imported revision, the commit fails with `409 git_conflict` and the
  affected paths. Merge the `acp/*` branch on the Git server yourself, then
  re-sync and re-import.
- Changed vars go back into the `group_vars`/`host_vars` file they came from.
  Rewritten files are regenerated: comments in touched files are lost.
- INI inventories cannot hold vault values on write-back (`422 vault_requires_yaml`) – use YAML.

### Preview

*Render* shows what `ansible-inventory --list` would return; vault values are
masked as `<vault>`.

## 4. Vault

1. Create a `vault_password` secret.
2. **Vault → New identity**: label (`default` = no vault-id, otherwise the
   vault-id used in `$ANSIBLE_VAULT;1.2;AES256;<id>`) bound to that secret.
   A `script` provider can be stored for runs (executed by the worker from M4);
   it cannot be used for UI decrypt/encrypt.
3. Project page → **Vault**: lists vault files and inline `!vault` values from
   the latest scan.
   - *Decrypt* (admin) shows the plaintext in a modal; every call is audited,
     the response is not cached.
   - *Encrypt* (whole file, or one YAML key path as inline `!vault`) and
     *Rekey* (from one identity to another) commit to a branch (default
     `acp/vault`), with the same conflict rules as inventories. Inline edits
     keep comments but may normalize indentation; multi-document YAML files
     are refused.

## Troubleshooting

| Error code | Meaning |
|---|---|
| `hostkey_not_pinned` (409) | Scan and pin the SSH host key first |
| `sync_in_progress` (409) | Another sync of this project is running |
| `remote_failed` / `remote_error` (502) | Git command failed (auth, network, missing ref); details are in the message, credentials redacted |
| `git_conflict` (409) | Target files changed on the write branch – merge/re-sync, then retry |
| `version_conflict` (412) | Inventory changed since it was loaded – reload |
| `vault_requires_yaml` (422) | INI inventory with vault values – convert to YAML |
| `provider_not_supported_in_app` (422) | Script vault identities only work in runs |
