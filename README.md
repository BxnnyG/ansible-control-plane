# ansible-control-plane

A self-hosted web platform to run, organize and monitor Ansible – Semaphore's simplicity, AWX's structure, a modern UI, plus a network-aware fleet view fed by connectors (OPNsense, Proxmox VE, NetBox, monitoring).

> **Status:** v1.0.0 - all milestones M1-M8 are done. See [`CHANGELOG.md`](CHANGELOG.md) for the release notes and [`PLAN.md`](PLAN.md) for scope, roadmap and known limitations.

## Features

- OIDC login (PKCE, e.g. Zitadel) plus break-glass admin, first-run setup wizard, workspaces, roles and scoped memberships, audit log
- Write-only secrets store with envelope encryption
- Git projects (sync on demand, schedule or webhook; playbook, role, inventory and vault detection; surveys from `argument_specs`)
- Inventories in the database or synced with Git, Ansible Vault (identities, audited decrypt, encrypt, rekey)
- Templates with surveys, schedules, webhooks, unprivileged and process-isolated worker, live logs (SSE), run history with per-host results
- Read-only connectors (OPNsense, Proxmox VE, NetBox, Checkmk and alert webhooks) feeding a merged fleet view with host status, conflicts, unmanaged devices and group rules
- Manual remediation: alert rules map to a template and limit; never automatic
- Migration tool from Semaphore UI
- Operations: backup and restore, worker egress allowlist, Prometheus metrics, OpenAPI 3.1 spec

## Quickstart

```sh
git clone https://github.com/BxnnyG/ansible-control-plane.git
cd ansible-control-plane
make init              # or: cp .env.example .env and set ACP_DB_PASSWORD; create secrets/master.key
docker compose up -d --build
```

The UI is then available on the configured port (default 8080, or HTTPS on 8443 with TLS); the setup wizard guides the first run. Full install, upgrade and security guidance: [`docs/operations.md`](docs/operations.md).

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

- [`CHANGELOG.md`](CHANGELOG.md) – release notes
- [`SECURITY.md`](SECURITY.md) – vulnerability reporting, supported versions
- [`PLAN.md`](PLAN.md) – project plan, roadmap, known limitations
- [`docs/operations.md`](docs/operations.md) – operator guide: install, upgrade, backup/restore, security model, troubleshooting
- [`docs/templates-and-runs.md`](docs/templates-and-runs.md) – templates, runs, schedules, webhooks
- [`docs/migration-from-semaphore.md`](docs/migration-from-semaphore.md) – migration from Semaphore UI
- [`docs/projects-and-inventories.md`](docs/projects-and-inventories.md) – operator guide: projects, inventories, vault
- [`docs/connectors-and-fleet.md`](docs/connectors-and-fleet.md) – operator guide: connectors, fleet, conflicts, network scopes, group rules
- [`docs/adr/`](docs/adr/) – architecture decision records

## License

[AGPL-3.0](LICENSE) with an additional attribution term (section 7(b)).

Created by bxnny
