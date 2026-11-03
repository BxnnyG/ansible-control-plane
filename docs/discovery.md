# Discovery Summary (sanitized)

Read-only discovery of the target host, 2026-11-03. Raw data with real addresses is kept in `docs/local/` (not in Git).

## Host
- Debian 13, running as an **LXC container** on Proxmox VE.
- 4 vCPU, **2 GiB RAM**, **8 GB disk (~3.9 GB free)**: tight for PostgreSQL + worker image + builds.
- Docker 29.x and Compose v5.x available; no Go/Node/Ansible on the host (builds must happen in containers).
- Port 80 is currently used by Semaphore.

## Semaphore (source system)
- Docker Compose deployment, Semaphore v2.17.x, backend **MySQL 8.0** (no BoltDB).
- Ansible 11.1.0 / ansible-core 2.18, Python 3.12, ~90 collections in the bundled venv.
- Content: 1 project, 2 repositories, 2 static inventories, 7 key store entries (SSH, login/password, none), 1 environment, 8 templates, 1 schedule (inactive), 1 user, ~2.8k historical tasks.
- A full backup exists outside the repo before any data access.

## Ansible content
- One Git repository (SSH remote on an internal Git server): `ansible.cfg`, multiple inventories, `group_vars`/`host_vars`, playbooks split into folders (day1, deploy, facts, scheduled, updates), templates.
- No Ansible Vault-encrypted files today; no vault settings in `ansible.cfg`.
- One local, non-Git playbook directory bind-mounted into Semaphore.

## External systems (ping / unauthenticated HTTPS only)
| System | Result |
|---|---|
| OPNsense | Host found (gateway), management UI not reachable from this host (filtered) |
| Proxmox VE | Not found via DNS |
| NetBox | Not found |
| Zabbix | DNS entry exists, host not reachable |
| Checkmk | Reachable, Checkmk agent installed on this host; site URL unknown |
| Prometheus/Alertmanager | Not found |
