# ansible-control-plane – Project Plan

Status: **approved** (M1, 2026-11-03). Working title `ansible-control-plane` (final name: keep working title).

---

## 1. Purpose

A self-hosted web platform to run, organize and monitor Ansible. It replaces an existing Semaphore UI installation and adds a **network-aware fleet view** fed by connectors (OPNsense, Proxmox VE, NetBox, monitoring).

> Semaphore's simplicity, AWX's structure, a modern UI – plus a fleet view that knows the network.

## 2. Problem statement

- Semaphore runs playbooks well but has no idea which hosts *exist* on the network, which are online, or which ones Ansible has never touched.
- AWX has structure (RBAC, surveys, inventories) but is heavy (Kubernetes operator, many components) and unpleasant for a single admin or a small team.
- Facts about a host are spread across OPNsense (leases, ARP), Proxmox (VMs, LXC, tags), NetBox (intended state) and monitoring (problems). Nobody joins them up, and nothing links a monitoring problem to the playbook that fixes it.
- Inventories are maintained by hand and drift away from reality ("not well maintained" is the expected starting point, not the exception).

## 3. Goals (v1.0)

1. Full functional replacement of Semaphore for the operator: projects, repositories, inventories, environments, keys, templates, schedules, run history.
2. Lossless migration from Semaphore (dry run, import, comparison, cutover with rollback).
3. Fleet view: per network, which hosts are online, Ansible-reachable, last run result, drift, open problems; unmanaged devices.
4. One-click remediation: run a playbook for a host, group or network, optionally as a check run first.
5. OIDC-first auth (Zitadel) with **scoped** permissions (e.g. "operator for the server network only"), not all-or-nothing.
6. Guided first-run setup: break-glass admin, OIDC, first workspace, users/roles, connectors with "test connection".
7. `git clone` + `.env` + `docker compose up -d` on an empty VM is enough.

## 4. Non-goals (v1.0)

- Kubernetes / Helm deployment, horizontal scaling of the app, HA.
- Write access to OPNsense, Proxmox, NetBox (connectors are read-only; exception: monitoring acknowledge, opt-in).
- Being a CMDB or IPAM (NetBox stays the source of truth for intended state).
- Execution environments per template (one bundled Ansible image in v1.0; multiple images are a v1.x candidate).
- Windows hosts beyond an optional WinRM reachability check.
- Running a PKI; TLS certificates are provided by the operator.
- Multi-tenancy for untrusted tenants (workspaces separate teams, not hostile customers).

## 5. Competitive landscape

| Tool | Strengths | Gaps for this use case |
|---|---|---|
| Semaphore UI | Simple, single binary, cheap to run | No fleet/network awareness, flat RBAC, limited inventory management, no vault tooling |
| AWX / AAP | RBAC, surveys, workflows, inventory sources | Kubernetes-only, heavy, slow UI, complex to operate |
| Rundeck | Generic job runner, ACLs | Not Ansible-native, Java stack, no network view |
| ARA | Excellent run recording/reporting | Reporting only, no execution, no inventory |
| NetBox + AWX | Source of truth + execution | Two heavy systems, no live state (leases/ARP/alerts), no remediation mapping |

Positioning: Semaphore-level operating cost, AWX-level structure where it matters (RBAC, surveys, inventories), plus live network state and remediation.

## 6. Domain model

```
Workspace ─┬─ Membership (User, Role, Scopes[])
           ├─ Project (Git repo, branch, credentials) ─┬─ Playbook (detected)
           │                                          ├─ Role / Collection requirements (detected)
           │                                          └─ VarsFile (group_vars/host_vars, vault-aware)
           ├─ Inventory (mode: db | git-synced) ─┬─ Group (tree, vars)
           │                                     └─ InventoryHost (→ Host, vars)
           ├─ Secret (type: ssh_key | password | token | vault_password | file)
           ├─ VaultIdentity (vault-id label → Secret | external provider)
           ├─ Environment (extra vars, env vars, secret refs)
           ├─ Template (project, playbook, inventory, environment, credentials, survey, limit, tags, check/diff defaults)
           ├─ Schedule (cron, template, enabled)
           ├─ Run (template, trigger, user, params) ─┬─ RunHostResult (ok/changed/failed/unreachable/skipped)
           │                                         └─ RunLog (chunked)
           ├─ Webhook (trigger → template)
           ├─ RemediationRule (match: alert type/label/service → template, scope, check-first)
           └─ GroupRule (match: subnet | Proxmox tag | NetBox role/site/tag → inventory group)

Global (shared by workspaces, filtered by scope):
Connector (type, config, secret ref, interval, status) → ConnectorSync (result, errors)
Network (subnet/VLAN/interface, source) 
Host (unified identity) ─┬─ HostObservation (source, field, value, seen_at)   ← per-field provenance
                         ├─ HostState (online, ansible_reachable, last_run, drift, open_alerts)
                         └─ HostConflict (type, details, first_seen, resolved)
Alert (source, host, severity, type/labels, since)
User (OIDC subject, email) · LocalAdmin (break-glass) · AuditEvent
```

Scopes: a membership grants a role either workspace-wide or restricted to a set of **networks**, **inventories** and/or **inventory groups**. Every host-touching action (view, run with `--limit`, remediation) is checked against the effective host set.

## 7. Functional requirements

### Setup (SETUP)
- **SETUP-1** First-run wizard (only while no admin exists): create break-glass admin, configure OIDC (with discovery test), create first workspace.
- **SETUP-2** Wizard steps for connectors with "test connection" (read-only probe) before saving.
- **SETUP-3** Wizard step for role mapping (OIDC role/group → workspace role + scope) with a preview of the resulting permissions.
- **SETUP-4** Wizard can be resumed and re-opened later from settings; every step is also available as a normal settings page.

### Authentication & authorization (AUTH)
- **AUTH-1** OIDC Authorization Code flow with PKCE (public client, no client secret); discovery via issuer URL.
- **AUTH-2** Roles/groups mapped from configurable claims (default for Zitadel: `urn:zitadel:iam:org:project:roles`). Default IdP role names: `acp-admin`, `acp-editor`, `acp-operator`, `acp-viewer`; workspace and scope are assigned by mapping rules.
- **AUTH-3** Users are provisioned just-in-time on first login; identity key is the OIDC `sub`, email used for matching migrated users.
- **AUTH-4** Break-glass local admin: password (argon2id) + mandatory TOTP; can be disabled via env; every use is audited prominently.
- **AUTH-5** Built-in roles per workspace: `owner`, `admin`, `operator` (run templates, remediate), `editor` (edit inventories/templates, no secrets read), `viewer`.
- **AUTH-6** Scoped memberships: role restricted to networks, inventories or inventory groups (see §6).
- **AUTH-7** Server-side sessions (HttpOnly, Secure, SameSite=Lax cookie), CSRF protection, RP-initiated logout.
- **AUTH-8** API tokens per user (scoped, expiring, hashed at rest) for CLI/automation.
- **AUTH-9** Audit log of all security-relevant actions (login, permission change, secret access, run start, cutover).

### Workspaces & organization (ORG)
- **ORG-1** Multiple workspaces from day one; all objects belong to exactly one workspace, including connectors and fleet data (ADR-0018).
- **ORG-2** Folders, tags and owners for hosts, playbooks, templates, inventories.
- **ORG-3** Command palette (Ctrl+K) searching all objects the user may see; dark/light mode.

### Git projects (GIT)
- **GIT-1** Project = Git repository (SSH or HTTPS, credentials from secret store), branch/ref selectable.
- **GIT-2** Detection of playbooks, roles, `requirements.yml` (roles + collections), `ansible.cfg`, `group_vars`/`host_vars`, inventories in the repo.
- **GIT-3** Sync on demand, on schedule and via webhook (GitHub, GitLab, Gitea/Forgejo, generic HMAC).
- **GIT-4** Collections/roles from `requirements.yml` installed per project revision and cached.
- **GIT-5** Survey forms generated from role `meta/argument_specs.yml` (types, defaults, choices, required).

### Inventory (INV)
- **INV-1** Inventories fully manageable in the UI: groups (nested), hosts, vars (YAML editor with validation).
- **INV-2** Mode *DB-only*: inventory lives in the database.
- **INV-3** Mode *Git-synced*: inventory file(s) in a project; UI changes are committed back (signed-off author = acting user) on a configurable branch; conflicts are surfaced, never force-pushed.
- **INV-4** Import of existing INI/YAML inventories from Git.
- **INV-5** Group rules: automatic group membership from connector data (subnet, Proxmox tag, NetBox role/site/tag); rule preview before applying.
- **INV-6** Rendered inventory preview (what `ansible-inventory --list` would see), with secrets masked.

### Ansible Vault (VAULT)
- **VAULT-1** Multiple vault IDs per workspace, each bound to a secret or an external password provider.
- **VAULT-2** Detection of vault-encrypted files and inline `!vault` variables in projects.
- **VAULT-3** Encrypt / decrypt (view, permission-gated and audited) / rekey in the UI; changes committed back to Git like INV-3.
- **VAULT-4** External vault password providers via provider interface (script/client mode, later OpenBao).

### Secrets (SEC)
- **SEC-1** Envelope encryption: per-secret data key (AES-256-GCM), wrapped by a key-encryption key derived from a master key that lives outside the DB (file or env).
- **SEC-2** Provider interface (`local` in v1.0; OpenBao/Vault later) for storage and unwrap.
- **SEC-3** Secrets are write-only in the UI/API (no read-back), only metadata visible; masked everywhere in logs.
- **SEC-4** Master key rotation (re-wrap data keys) via CLI subcommand.
- **SEC-5** Secrets reach the worker only for the duration of a run, in memory/tmpfs, never on persistent disk.

### Runs (RUN)
- **RUN-1** Templates: playbook, inventory, environment, credentials, vault IDs, default limit/tags/skip-tags, verbosity, check/diff defaults, survey.
- **RUN-2** Start with overrides: `--limit`, extra vars, check mode, diff mode, tags.
- **RUN-3** Live log streaming (SSE), ANSI colors, search, download; cancel (SIGTERM, then SIGKILL after timeout).
- **RUN-4** Per-host result per run (ok/changed/failed/unreachable/skipped/rescued) via a bundled callback plugin; changed tasks per host.
- **RUN-5** Run history with filters (template, host, result, user, trigger); retention policy for logs.
- **RUN-6** Cron schedules (timezone-aware), enable/disable, next-run preview, missed-run policy.
- **RUN-7** Concurrency limits per worker and per template; queueing.
- **RUN-8** Notifications on run failure (webhook / email; pluggable).

### Connectors (CON)
- **CON-1** Common Go interface; each connector returns normalized `Network`, `HostObservation`, `Alert` records.
- **CON-2** Sync on interval plus manual trigger; per-connector status, last success, last error, duration, counts in the UI.
- **CON-3** Multiple instances per connector type (e.g. two OPNsense firewalls).
- **CON-4** OPNsense: interfaces/VLANs/subnets, DHCP leases (Kea, dnsmasq, ISC legacy – auto-detected per instance), static mappings, ARP and NDP tables.
- **CON-5** Proxmox VE: nodes, VMs, LXC, status, tags, notes, IPs via guest agent (VM) and container interfaces (LXC).
- **CON-6** NetBox: prefixes, IP addresses, devices, VMs, sites, tags, roles.
- **CON-7** Monitoring: adapters for Checkmk REST API, Zabbix API, Prometheus Alertmanager; generic webhook input for alerts.
- **CON-8** Read-only toward external systems; the only write is monitoring acknowledge, disabled by default.
- **CON-9** Recorded fixtures per connector and API version for tests; API endpoints verified against the installed versions.

### Fleet view (FLEET)
- **FLEET-1** Network overview: per network counts (hosts, online, Ansible ok, failed, never managed, open alerts).
- **FLEET-2** Host identity merge: MAC > hostname/FQDN > IP; per-field source visible; configurable source priority (default NetBox > Proxmox > OPNsense).
- **FLEET-3** Conflicts (same IP different MAC, duplicate hostname, host disappeared) shown as hints, never silently overwritten; can be acknowledged.
- **FLEET-4** Per host: online (lease/ARP/ping), Ansible reachability (SSH check, optional WinRM), last run + result, drift (last check run), open alerts.
- **FLEET-5** "Unmanaged" list: devices seen on the network but not in any inventory; one-click "add to inventory/group".
- **FLEET-6** Remediation rules: alert type/label/service → template; fix button runs with `--limit` host, group or network; optional check run first.
- **FLEET-7** Bulk actions on a network/selection (check run, fact gathering, reachability check), permission- and scope-checked.

### Migration (MIG)
- ✅ **MIG-1** CLI `acp migrate semaphore` reading via API and/or directly from the Semaphore DB (MySQL/MariaDB, Postgres, BoltDB) read-only.
- ✅ **MIG-2** Mandatory backup check before reading (refuses to run without a backup path).
- ✅ **MIG-3** Scope: projects → workspaces, repositories, inventories, environments → variables, key store → secrets, templates, schedules, users (email → OIDC identity).
- ✅ **MIG-4** Key store decryption with `access_key_encryption` from `config.json` in-process; immediate re-encryption; secrets never printed.
- ✅ **MIG-5** Dry-run report: what would be migrated, what cannot be mapped and why.
- ✅ **MIG-6** Comparison report: object counts old/new; one check-mode run per migrated template.
- ✅ **MIG-7** Schedules imported disabled.
- ✅ **MIG-9** (finding from M4) The Ansible repo references `~/.ssh/ansible_infra` (`private_key_file` in ansible.cfg or `ansible_ssh_private_key_file` in vars). Migrated templates must override it; inventory/group vars beat `--private-key`.
- **MIG-8** (runbook in `docs/migration-from-semaphore.md`; cutover is a manual step after user confirmation) Cutover command + documented rollback; source system never modified (only stopped/disabled after explicit confirmation).

## 8. Non-functional requirements

- **Security:** OWASP ASVS L2 as guideline; CSP, secure headers; least privilege containers; no Docker socket; dependency and secret scanning in CI.
- **Performance:** fleet view for 2,000 hosts / 100 networks renders < 1 s; log streaming latency < 1 s.
- **Footprint:** whole stack ≤ 1.5 GiB RAM at idle on a 4 GiB host; app image < 60 MB; worker image as small as Ansible allows.
- **Operability:** healthchecks, structured JSON logs, Prometheus `/metrics`, backup/restore commands, versioned DB migrations.
- **Reliability:** runs survive app restarts (worker keeps running, reattaches log stream); scheduler is idempotent (no double runs).
- **Usability:** few menus, keyboard-first (Ctrl+K), dark/light, responsive; NetBird-dashboard-like look.
- **Testability:** unit tests for merge/secrets/migration, integration tests against Postgres (testcontainers), connector tests with sanitized fixtures.
- **Licensing:** AGPL-3.0 with attribution term (see `LICENSE`).

## 9. Architecture

```
            browser ──HTTPS──▶ app (Go) ───────────────▶ PostgreSQL
                                │  ▲  REST + SSE           (state, queue, logs)
                                │  │
              internal network  │  │ worker API (token-authenticated, pull model)
                                ▼  │
                              worker (Go supervisor + ansible-core, unprivileged)
                                │ SSH / WinRM
                                ▼
                         managed hosts

  app ──read-only HTTPS──▶ OPNsense ×N · Proxmox VE · NetBox · Checkmk/Zabbix/Alertmanager
```

- **app** (single Go binary): REST API, SSE log streaming, OIDC, scheduler, connector syncs, Git operations for project scanning and Git-synced inventories, serves the SvelteKit SPA (embedded static assets), `/metrics`, `/healthz`. Terminates TLS itself (operator-provided cert) or runs behind a reverse proxy.
- **worker**: separate container. A small Go supervisor pulls jobs from the app over an internal authenticated API, receives a run bundle (repo revision, rendered inventory, decrypted credentials just in time), materializes it on tmpfs, runs `ansible-playbook` as an unprivileged subprocess, streams events/log chunks back. **No DB access, no master key, no Docker socket.** Read-only rootfs, all capabilities dropped, `no-new-privileges`.
- **PostgreSQL**: single source of state; job queue via `SELECT … FOR UPDATE SKIP LOCKED`; `LISTEN/NOTIFY` for log fan-out.
- **Deployment**: one `docker-compose.yml` (`app`, `worker`, `postgres`), configuration via `.env`, secrets via files mounted as Docker secrets.

Stack decisions are recorded in `docs/adr/`.

## 10. Security model

- **Trust boundaries:** browser ↔ app (OIDC session), app ↔ worker (shared token + internal network only), app ↔ external APIs (read-only tokens), worker ↔ managed hosts (SSH keys from secret store).
- **Master key** in a file (Docker secret) or env; never in DB, never in backups of the DB (backups document separately how to back up the key).
- **Secrets at rest:** envelope encryption (SEC-1); at runtime only in app memory and worker tmpfs for the run's lifetime.
- **Log masking:** known secret values and vault passwords are redacted from run logs before persistence; `no_log` respected.
- **Authorization:** every API handler checks role + scope; host-level actions are resolved to the effective host set and rejected if any host is out of scope.
- **Audit:** append-only audit table, exported in structured logs.
- **Supply chain:** pinned base images and dependencies, `gitleaks` pre-commit + CI, `govulncheck`, `npm audit`, container image scan.
- **Connectors:** read-only API users documented per system (least privilege), TLS verification on by default with custom CA support.

## 11. Roadmap

| Milestone | Content |
|---|---|
| M0 ✅ | Git init, orchestrator setup, discovery |
| M1 ✅ | This plan, ADRs, license, repo skeleton, `.gitignore` + gitleaks hook, compose stack (hello-world app/worker/postgres), CI (lint, test, secret scan) |
| M2 ✅ | Auth (OIDC + break-glass), setup wizard (SETUP-1/3/4), workspaces, roles + scopes, secrets store, audit |
| M3 ✅ | Git projects, inventories (DB + Git-synced), vault integration, surveys from `argument_specs` (GIT-4 requirements install moved to M4) |
| M4 ✅ | Worker, templates, `requirements.yml` install (GIT-4), live logs, run history, per-host results, schedules, webhooks |
| M7 ✅ | Semaphore migration: dry run → import → comparison → cutover |
| M5 ✅ | Connector framework, OPNsense (Kea/dnsmasq/ISC), Proxmox VE, host merge, conflicts, group rules; decisions in ADR-0018, operator guide `docs/connectors-and-fleet.md` |
| M6 ✅ | NetBox, monitoring (Checkmk, alert webhook incl. Alertmanager format), host status, network counts, unmanaged list, remediation rules, bulk runs; decisions in ADR-0019 |
| M8 ✅ | Hardening: security review, job process isolation (ADR-0020), worker egress allowlist, backup/restore, OpenAPI spec, docs, release `v1.0.0` |

Execution order (decided 2026-11-05): M4 → M7 → M5 → M6 → M8. Migration comes first because real runs need the migrated SSH keys and ACP should replace Semaphore in daily use before connectors add value. Milestone numbers stay unchanged so branches and tags remain stable.

### Known limitations and v1.x candidates

- Per-worker claim scoping: the worker token claims any queued run (ADR-0020 follow-up); matters only with several workers.
- OpenAPI spec does not describe SSE event payloads.
- Survey fields lack min/max/regex validation.
- Worker egress allowlist is IPv4 only (no IPv6 rules) and requires IP-based inventories (no DNS).
- No browser (UI) end-to-end tests; the UI is covered by unit and type checks only.
- NetBox connector is not verified against a live instance; Checkmk and Alertmanager formats are verified with fixtures only.
- No secret rotation for alert webhook connectors; group rules are not auto-applied after a sync; resolved conflicts are never pruned.
- Concurrent mutations: scope check and write are separate steps (ADR-0007); clients should send `If-Match`.
- Session login behind a reverse proxy shares one rate limit budget (no trusted-proxy setting).
- Notifications (ADR-0021): one template filter per rule, no test-before-save, no per-user subscriptions or quiet hours, imported already-finished runs emit no events.

## 12. Risks

| Risk | Impact | Mitigation |
|---|---|---|
| Scope is large for v1.0 | Delays | Strict milestone gates, each milestone usable on its own |
| Connector API differences between versions | Wrong/missing data | Verify against installed versions, recorded fixtures, auto-detection (e.g. DHCP backend) |
| Poorly maintained source data (DNS, NetBox) | Noisy merge, false conflicts | Conflicts as hints, acknowledge workflow, source priority configurable |
| Semaphore key store format changes | Migration fails | Read version from source, dry run first, test against a copy of the real DB |
| Ansible version jump vs. Semaphore's bundled Ansible | Templates behave differently | Check-mode comparison run per template (MIG-6); version pinned in worker image |
| Secrets leaking into logs/Git | Security incident | Masking, write-only secrets, gitleaks, tmpfs-only materialization |
| Small host (4 GiB RAM) | OOM during builds/runs | Multi-stage builds, memory limits in compose, worker concurrency default 2 |
| Single maintainer | Bus factor | Docs, ADRs, `HANDOFF.md`, tests |

## 13. Open questions

1. Ansible version for the worker image: answered, full community `ansible` package 14.6.0 (ansible-core 2.21.6), pinned via build arg.
2. Is the second Semaphore repository (local path) still needed, or is the Git repository the only source?
3. Notification channels for failed runs: answered, implemented in v1.1 (webhook, Matrix, SMTP, Gotify, ntfy; [ADR-0021](docs/adr/0021-run-notifications.md), [guide](docs/notifications.md)).
