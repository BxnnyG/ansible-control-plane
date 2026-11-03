# ansible-control-plane

A self-hosted web platform to run, organize and monitor Ansible – Semaphore's simplicity, AWX's structure, a modern UI, plus a network-aware fleet view fed by connectors (OPNsense, Proxmox VE, NetBox, monitoring).

> **Status:** early development (milestone M1). Not usable yet. See [`PLAN.md`](PLAN.md) for scope and roadmap.

## Features (planned for v1.0)

- Git-native projects, inventories (DB or Git-synced), deep Ansible Vault integration
- Templates, surveys from `argument_specs`, live logs, per-host results, cron schedules
- OIDC login (e.g. Zitadel) with workspace roles and scoped permissions (per network, inventory or group)
- Fleet view per network: online, Ansible-reachable, last run, drift, open monitoring problems, unmanaged devices
- One-click remediation runs mapped from monitoring alerts
- Migration tool from Semaphore UI

## Quickstart

```sh
git clone https://github.com/BxnnyG/ansible-control-plane.git
cd ansible-control-plane
make init              # or: cp .env.example .env and set ACP_DB_PASSWORD; create secrets/master.key
docker compose up -d --build
```

The UI is then available on `http://<host>:8080` (configurable in `.env`).

## Documentation

- [`PLAN.md`](PLAN.md) – project plan and requirements
- [`docs/adr/`](docs/adr/) – architecture decision records

## License

[AGPL-3.0](LICENSE) with an additional attribution term (section 7(b)).

Created by bxnny
