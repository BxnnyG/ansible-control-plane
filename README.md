# ansible-control-plane

A self-hosted web platform to run, organize and monitor Ansible – Semaphore's simplicity, AWX's structure, a modern UI, plus a network-aware fleet view fed by connectors (OPNsense, Proxmox VE, NetBox, monitoring).

> **Status:** early development – milestones M0–M4 and M7 done (auth, workspaces, secrets, Git projects, inventories, vault, templates and runs, Semaphore migration with dry run, apply, compare and cutover), see [`docs/migration-from-semaphore.md`](docs/migration-from-semaphore.md). M5 (read-only OPNsense and Proxmox connectors, merged fleet view, conflicts, network scopes, group rules) is implemented and pending live verification, see [`docs/connectors-and-fleet.md`](docs/connectors-and-fleet.md). See [`PLAN.md`](PLAN.md) for scope and roadmap.

## Features

Available now:

- OIDC login (PKCE, e.g. Zitadel) plus break-glass admin, workspaces, roles and scoped memberships
- Write-only secrets store with envelope encryption and audit log
- Git projects: sync on demand, on schedule or via webhook; detection of playbooks, roles, requirements, vars files, inventories and vault content; surveys from `argument_specs`
- Inventories in the database or synced with Git (INI/YAML import, rendered preview, write-back to `acp/*` branches)
- Ansible Vault: vault identities, audited decrypt, encrypt and rekey via Git

- Templates with surveys, run dialog with host preview, unprivileged worker, live logs (SSE), run history, per-host results
- Cron schedules and template webhooks, requirements cache (`requirements.yml`), admin CLI for templates and runs

Planned for v1.0:

- Network scopes for permissions (fed by connectors)
- Fleet view per network: online, Ansible-reachable, last run, drift, open monitoring problems, unmanaged devices
- One-click remediation runs mapped from monitoring alerts
- Read-only connectors (OPNsense, Proxmox VE) feeding a merged fleet view with conflict hints and group rules (see [`docs/connectors-and-fleet.md`](docs/connectors-and-fleet.md))
- Migration tool from Semaphore UI (see [`docs/migration-from-semaphore.md`](docs/migration-from-semaphore.md))

## Quickstart

```sh
git clone https://github.com/BxnnyG/ansible-control-plane.git
cd ansible-control-plane
make init              # or: cp .env.example .env and set ACP_DB_PASSWORD; create secrets/master.key
docker compose up -d --build
```

The UI is then available on `http://<host>:8080` (configurable in `.env`).

## Admin CLI

Operator tasks run inside the app container (it has the data volume and master key):

```sh
docker compose exec app acp admin project list --workspace default
docker compose exec app acp admin project sync --workspace default --project <slug|id>
docker compose exec app acp admin inventory list --workspace default
docker compose exec app acp admin inventory import --workspace default \
  --project <slug|id> --path inventories/hosts.yml --name "Lab" [--mode git|db] [--write-branch acp/lab]
```

`project list` prints id, slug, URL, ref, last sync status/sha/time and interval.
`project sync` runs the same sync as the UI (per-project lock, revision
recording, scan; recorded with trigger `manual`, audited as actor `cli`) and
prints slug, status, sha, revision id and scan counts (playbooks, roles,
inventories, vars files, vault files, warnings). On failure it exits non-zero
with the redacted error. Credentials are never printed.

`import` creates the inventory if no inventory with that name or slug exists
(otherwise it re-imports into it when mode, project and path match), imports
from the latest synced project revision and prints the inventory id, slug and
group/host counts (never variable values). The project must be synced first.

## Documentation

- [`PLAN.md`](PLAN.md) – project plan and requirements
- [`docs/projects-and-inventories.md`](docs/projects-and-inventories.md) – operator guide: projects, inventories, vault
- [`docs/connectors-and-fleet.md`](docs/connectors-and-fleet.md) – operator guide: connectors, fleet, conflicts, network scopes, group rules
- [`docs/adr/`](docs/adr/) – architecture decision records

## License

[AGPL-3.0](LICENSE) with an additional attribution term (section 7(b)).

Created by bxnny
