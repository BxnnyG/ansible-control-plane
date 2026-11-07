# Changelog

All notable changes are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project uses
[Semantic Versioning](https://semver.org/).

## [Unreleased]

### Added

- **Notification channels in the UI:** per-type setup guides, field help and defaults, inline credential entry (creates the encrypted secret for you) and replacing a linked secret's value, and a hint for channels without rules.
- **Draft test:** `POST /api/v1/workspaces/{workspace}/notifications/channels/test` sends a test message through an unsaved channel definition (also for edits of existing channels); the credential is used in memory only, audited as `notification.channel.test_draft`, rate limited to 10 per minute and user.

## [1.1.0] - 2026-11-07

### Added

- **Run notifications (ADR-0021):** channels for webhook (versioned JSON schema, HMAC signature or bearer), Matrix, SMTP, Gotify and ntfy; rules for `run.failed`, `run.recovered`, `run.drift` and `run.finished` with optional template filter.
- Outbox fed by a database trigger on terminal run status, dispatcher in `serve` with at-least-once delivery, exponential backoff (max 5 attempts) and pruning with the run log retention.
- `notification.read` and `notification.manage` permissions, audited changes and test sends.
- `acp admin notification` CLI, OpenAPI operations, notifications page in the UI and operator guide `docs/notifications.md`.

## [1.0.0] - 2026-11-07

First stable release. Milestones M1 to M8 (execution order M4, M7, M5, M6, M8).

### Added

- **Foundation (M1):** Go single-binary app with embedded SvelteKit SPA, separate hardened worker container, PostgreSQL as only state store, Docker Compose stack, CI (lint, tests, gitleaks), AGPL-3.0 license with ADRs 0000-0020.
- **Auth and access (M2):** OIDC login (PKCE, Zitadel-style role claims) plus a local break-glass admin (argon2id and TOTP), first-run setup wizard, workspaces, roles with scoped memberships, central policy package, append-only audit log.
- **Secrets (M2):** write-only secrets store with envelope encryption (master key file), secret types and metadata, audited access.
- **Git projects and inventories (M3):** Git projects with sync on demand, schedule or webhook; scan for playbooks, roles, requirements, vars files, inventories and vault content; surveys derived from `argument_specs`; inventories in the database or synced with Git (INI/YAML import, rendered preview, write-back to `acp/*` branches); Ansible Vault support in Go (identities, audited decrypt, encrypt, rekey).
- **Runs (M4):** templates with surveys, run dialog with host preview, unprivileged worker pulling jobs from the app, live logs over SSE, run history with per-host results, cron schedules with overrides, template webhooks, `requirements.yml` install cache, log retention, admin CLI for templates and runs.
- **Semaphore migration (M7):** `acp migrate semaphore` with dry run, import, comparison and cutover; documented in `docs/migration-from-semaphore.md`.
- **Connectors and fleet (M5, M6):** read-only OPNsense (Kea, dnsmasq, ISC), Proxmox VE and NetBox connectors; Checkmk and generic alert webhook (Alertmanager format); host identity merge with conflict hints; merged fleet view with host status, network counts and unmanaged list; group rules; manual remediation rules and bulk runs (never automatic).
- **Operations (M8):** backup and restore scripts, worker egress allowlist script and systemd unit, `acp_` Prometheus metrics, OIDC login rate limit, audit entries for CLI actions, version info via build args.
- **API and UI (M8):** OpenAPI 3.1 spec with a route drift test, generated TypeScript API types checked in CI, schedule override editing and a survey form with raw JSON toggle in the UI.

### Changed

- Metrics moved to the internal listener (`ACP_WORKER_API_ADDR`); exposing them publicly now needs `ACP_METRICS_TOKEN`.
- The worker container needs `cap_add: [SETUID, SETGID, CHOWN, KILL]` (with `cap_drop: [ALL]`) for job process isolation.
- `acp serve` refuses a plain `http://` public URL on a non-loopback host unless `ACP_ALLOW_INSECURE_HTTP=true`.
- Public listeners use explicit read, write and idle timeouts; compose sets `pids_limit` on all services.
- Connector secrets are removed together with the connector.

### Security

- Job process isolation (ADR-0020): ansible runs under per-slot job uids without capabilities and cannot read the worker token; symlink-safe bundle extraction.
- Worker egress allowlist (`scripts/worker-egress.sh`) restricts outbound traffic of the worker network.
- Project credentials can no longer be rebound across projects; denylists for run variables and environment variables.
- Security headers (CSP, nosniff, frame denial, referrer and permissions policy, HSTS when TLS is terminated by the app).
- Secret values masked in run parameters for roles without `template.write`; known secrets and vault passwords redacted from run logs.
- Lost-update protection (`If-Match`) for secrets; rate limiter eviction; OIDC login rate limit.

### Upgrading from pre-release milestones

1. Run `scripts/backup-acp.sh` and store `secrets/master.key` separately (see `docs/operations.md`).
2. `git pull` (or check out `v1.0.0`) and rebuild with `docker compose up -d --build`; database migrations apply automatically on app start.
3. The worker service needs the new capabilities from the shipped `docker-compose.yml`; verify that the worker log shows "job isolation enabled".
4. Set `ACP_WORKER_EGRESS_CIDRS` and `ACP_WORKER_EGRESS_PORTS` and install the egress unit, or run without the unit (worker egress then stays unrestricted).
