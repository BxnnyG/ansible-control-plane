# Operations

Operator guide for ansible-control-plane v1.0. Contents: [Install](#install),
[Upgrade](#upgrade), [Backup](#backup), [Restore](#restore),
[Network exposure and hardening](#network-exposure-and-hardening),
[Metrics](#application-metrics), [Worker egress](#worker-egress-allowlist),
[API](#api), [Security model](#security-model), [Troubleshooting](#troubleshooting).

## Install

Requirements: a Linux host with Docker Engine and the Compose plugin (about
4 GiB RAM and 16 GB disk are enough for a small fleet), outbound access to your
Git remotes, and network reach from the worker to the managed hosts (SSH). Go and
Node are not needed on the host; everything builds in containers.

    git clone https://github.com/BxnnyG/ansible-control-plane.git
    cd ansible-control-plane
    make init                     # .env, secrets/master.key, secrets/worker.token, git hooks
    $EDITOR .env                  # set ACP_PUBLIC_URL, OIDC and TLS values
    docker compose up -d --build  # or: make up

Without `make`, copy `.env.example` to `.env`, set `ACP_DB_PASSWORD`, create
`secrets/master.key` (`openssl rand -base64 32`) and `secrets/worker.token`, and
make both owned by uid 10001 with mode 600.

### Configuration

Set in `.env` (see `.env.example`); compose passes them to the containers.

| Variable | Default | Meaning |
| --- | --- | --- |
| `ACP_HTTP_PORT` / `ACP_HTTPS_PORT` | 8080 / 8443 | Published host ports (port 80 is not used) |
| `ACP_DB_PASSWORD` | none (required) | Password of the postgres user `acp` |
| `ACP_LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |
| `ACP_PUBLIC_URL` | none | External base URL (`scheme://host[:port]`, no path). Only source for redirects (HTTPS redirect, OIDC). Required with OIDC |
| `ACP_ALLOW_INSECURE_HTTP` | `false` | Allow a plain `http://` public URL on a non-loopback host |
| `ACP_METRICS_TOKEN` | empty | Also expose `/metrics` on public ports behind this bearer token |
| `ACP_OIDC_ISSUER`, `ACP_OIDC_CLIENT_ID` | empty | Both set enable OIDC (PKCE public client, no secret) |
| `ACP_OIDC_SCOPES` | `openid profile email` | Must include `openid` |
| `ACP_OIDC_ROLES_CLAIM` | `urn:zitadel:iam:org:project:roles` | Claim carrying role names |
| `ACP_SESSION_IDLE_TIMEOUT` / `ACP_SESSION_ABSOLUTE_TIMEOUT` | `8h` / `24h` | Session lifetimes |
| `ACP_BREAKGLASS_ENABLED` | `true` | Local break-glass login (`acp admin create-breakglass`) |
| `ACP_TLS_CERT_SECRET`, `ACP_TLS_KEY_SECRET` | `./deploy/empty` | Host paths of certificate and key, mounted as Docker secrets |
| `ACP_TLS_CERT_FILE`, `ACP_TLS_KEY_FILE` | empty | In-container paths (`/run/secrets/tls_cert`, `/run/secrets/tls_key`); both or none |
| `ACP_WORKER_CONCURRENCY` | 2 | Parallel runs per worker container |
| `ACP_WORKER_JOB_UID_BASE` | 10100 (image) | First job uid; 0 disables isolation (not recommended), otherwise at least 1000 |
| `ACP_WORKER_POLL_INTERVAL` | `15s` | How often the worker polls for jobs |
| `ACP_WORKER_API_ADDR` | `:8082` | Internal worker API listener, never published |
| `ACP_RUN_LOG_RETENTION_DAYS` | 90 | Delete log chunks of finished runs after N days (0 = keep) |
| `ACP_SHUTDOWN_GRACE` | `15s` | Graceful shutdown period |
| `ACP_WORKER_EGRESS_CIDRS`, `ACP_WORKER_EGRESS_PORTS` | unset | Read by `scripts/worker-egress.sh` only |

Set by compose, normally not changed: `ACP_HTTP_ADDR`, `ACP_HTTPS_ADDR`,
`ACP_DATABASE_URL`, `ACP_DATA_DIR`, `ACP_WORKER_TOKEN_FILE`, `ACP_MASTER_KEY` /
the master key file secret, `ACP_APP_URL` and `ACP_WORKER_ADDR` (worker side).
`ACP_MIGRATE_SOURCE_DSN` is used only by the Semaphore migration tool.

### TLS

Put the certificate and key under `secrets/tls/`, set the four `ACP_TLS_*`
variables and an `https://` `ACP_PUBLIC_URL`. The files are bind mounts that keep
host ownership: the private key must be owned by uid 10001 (`acp`), mode 600
(`chown 10001 secrets/tls/*.key && chmod 600 secrets/tls/*.key`). Alternatively
terminate TLS at a proxy and use an `https://` public URL (see the rate limit
note below). HTTP requests then redirect to the public URL.

### First run

Open the public URL. While no admin exists the setup wizard (`/setup`) guides
you: create the break-glass admin (password and TOTP, or `docker compose exec app
acp admin create-breakglass`), configure OIDC with a discovery test, create the
first workspace, map IdP roles to workspace roles and scopes, and optionally add
connectors with a read-only test connection. The wizard can be reopened from the
settings; every step is also a normal settings page. Then add secrets, a Git
project, an inventory and a template (see [`templates-and-runs.md`](templates-and-runs.md)
and [`projects-and-inventories.md`](projects-and-inventories.md)).

### OIDC setup (generic)

Register a native/SPA application at your identity provider with PKCE and
authentication method "none" (no client secret).

- Redirect URI: `<ACP_PUBLIC_URL>/api/auth/oidc/callback`
- Post-logout redirect URI: `<ACP_PUBLIC_URL>/login`
- Set `ACP_OIDC_ISSUER` and `ACP_OIDC_CLIENT_ID`; scopes must include `openid`.
- Roles come from a configurable claim (`ACP_OIDC_ROLES_CLAIM`), falling back to
  UserInfo. Define roles named `acp-owner`, `acp-admin`, `acp-editor`,
  `acp-operator` and `acp-viewer` (plus `acp-connector` and `acp-sync` for service
  roles) and assign them to users. A user without any matching role is rejected
  with `/login?error=no_access`.
- Zitadel: enable "assert roles on authentication" on the project, "roles inside
  ID token" on the application, and add the scope `urn:zitadel:iam:org:project:roles`.

Keep the break-glass admin for emergencies; set `ACP_BREAKGLASS_ENABLED=false`
only when the IdP is reliable and you have another recovery path.

## Upgrade

1. Back up: `scripts/backup-acp.sh` (and keep `secrets/master.key` stored separately).
2. Fetch the release: `git fetch --tags && git checkout v1.0.0` (or `git pull` on `main`).
3. Review `.env.example` for new variables and merge them into `.env`.
4. `make up` (rebuilds images; the app applies database migrations on start).
5. Verify: `curl -fsS <public-url>/readyz` succeeds; `docker compose logs worker | grep "job isolation enabled"`;
   `docker compose exec app acp version` reports the new version.
6. If the egress network was recreated, `systemctl restart acp-worker-egress`.

Rollback: restore the database backup (see [Restore](#restore)) and check out the previous tag.

## Backup

    scripts/backup-acp.sh [-p <compose-project>] [-o <backup-root>]

Writes `/opt/backups/acp-<YYYY-MM-DD-HHMM>/` (mode 700/600) containing:

- `acp.dump` - `pg_dump -Fc` of the `acp` database (the only state store).
- `manifest.txt` - creation time, app version, goose migration version, sha256 of the dump.

The dump is validated with `pg_restore -l` before the manifest is written; a failed
run leaves no partial directory. The script runs `pg_dump` inside the postgres
container (consistent snapshot, no downtime).

### Not included

- **`secrets/master.key`** - deliberately never part of the DB backup. Without it the
  stored secrets (SSH keys, vault passwords, connector tokens) cannot be decrypted.
  Back it up separately, offline, and keep it apart from the dumps. Restoring a dump
  with a different key leaves secrets unreadable (everything else works).
- `secrets/worker.token`, `.env`, TLS keys - re-creatable or configuration; keep them in
  your regular configuration backup.
- `acpdata` volume (project Git clones) and the worker cache - re-created by the next
  project sync.

## Restore

    scripts/restore-acp.sh -p <compose-project> -b <backup-dir> --yes

Replaces the database of the given project. The project name is mandatory (there is no
default) and `--yes` is required. Steps: verify the dump checksum from the manifest,
start postgres, stop app and worker, drop and recreate the database, `pg_restore`,
start app and worker (the app applies pending migrations on start, so older dumps
are upgraded), wait for `/readyz`.

Restoring onto a fresh host: run `make init`, put the original `secrets/master.key`
in place (owner uid 10001, mode 600), then run the restore with the compose project
name (normally `acp`). Sync the projects afterwards to repopulate the clone cache.

## Testing a restore

Use a throwaway project with different ports, never the live one:

    ACP_DB_PASSWORD=x ACP_HTTP_PORT=18080 ACP_HTTPS_PORT=18443 \
      scripts/restore-acp.sh -p acptest -b <backup-dir> --yes
    docker compose -p acptest down -v

## Network exposure and hardening

- **Plain HTTP:** `acp serve` refuses to start when `ACP_PUBLIC_URL` is an
  `http://` URL on a non-loopback host, because session cookies and
  credentials would be unencrypted. Use TLS (`ACP_TLS_*`) or a TLS-terminating
  proxy with an `https://` public URL. `ACP_ALLOW_INSECURE_HTTP=true` overrides the check.
- **Security headers:** every response carries a Content-Security-Policy
  (same-origin; inline scripts of the SPA entry page are allowed by hash,
  computed at startup), `X-Content-Type-Options: nosniff`, `X-Frame-Options: DENY`
  with CSP `frame-ancestors 'none'`, `Referrer-Policy: same-origin` and a minimal
  `Permissions-Policy`. HSTS is sent only on connections the app terminated TLS for.
- **Metrics:** `/metrics` is served on the internal listener (`ACP_WORKER_API_ADDR`,
  `:8082`, not published). Scrape it from a container on the `internal` network.
  Setting `ACP_METRICS_TOKEN` additionally exposes it on the public ports behind
  `Authorization: Bearer <token>`.
- **Login rate limits:** the OIDC endpoints `/api/auth/oidc/login` and
  `/api/auth/oidc/callback` share a per-source-IP budget of 20 requests per
  minute (fixed window, in memory, reset on restart; `auth.Options.OIDCRateMax` /
  `OIDCRateWindow`). Over the limit, browser navigations (`Accept: text/html`)
  are redirected to `/login?error=rate_limited`, other clients get `429` with
  `Retry-After`. Break-glass login keeps its own failure limiter and lockout
  (ADR-0006). The app never trusts `X-Forwarded-For` (no trusted-proxy setting
  exists), so behind a reverse proxy all clients share the proxy's IP and thus
  one budget; terminate TLS in the app (ADR-0011) or rate limit at the proxy.
- **Timeouts:** the public listeners use read-header 10 s, read 60 s, write 120 s
  and idle 120 s timeouts. Live-log streams and log downloads lift the read and
  write deadlines for their own response only.
- **Version:** `make build`/`make up` pass `ACP_VERSION` (default `git describe`)
  and `ACP_COMMIT` as build args, so `acp version` reports the real build.
  Override with `ACP_VERSION=... docker compose build`.
- **Process limits:** compose sets `pids_limit` on postgres, app and worker.
- **Job isolation:** ansible runs under per-slot job uids, separate from the worker user that holds the
  worker token (ADR-0020). The worker service therefore needs `cap_add: [SETUID, SETGID, CHOWN, KILL]`
  next to `cap_drop: [ALL]` and keeps `read_only` and `no-new-privileges`. Check after a deploy:
  `docker compose exec worker grep Cap /proc/1/status` shows CapEff `00000000000000e1` (CHOWN, KILL, SETGID,
  SETUID) and the log line "job isolation enabled". Any other base image or runtime must keep the file
  capabilities (`getcap /usr/libexec/acp/acp-worker`).

## Application metrics

Namespace `acp_`. Label values come from small fixed sets; HTTP metrics use the
chi route pattern (unmatched paths collapse into `static`, `api_not_found` or
`unmatched`), never the raw path. No hostnames, workspace names or user input
appear as labels. Go runtime and process metrics are exported as well.

| Metric | Type | Labels |
| --- | --- | --- |
| `acp_runs_started_total` | counter | none (runs claimed by a worker) |
| `acp_runs_finished_total` | counter | `status` (succeeded, failed, cancelled, lost) |
| `acp_run_duration_seconds` | histogram | `status` (claim to finish; not observed for lost runs) |
| `acp_runs_queued` | gauge | none (computed on scrape; -1 if the query fails) |
| `acp_worker_claims_total` | counter | `result` (claimed, empty, error) |
| `acp_worker_heartbeats_total` | counter | `result` (ok, lease_lost, error) |
| `acp_connector_syncs_total` | counter | `type`, `result` (ok, error) |
| `acp_connector_last_success_timestamp_seconds` | gauge | `type` |
| `acp_project_syncs_total` | counter | `result` (ok, error) |
| `acp_webhook_requests_total` | counter | `kind` (project, template, alert), `result` (accepted, ignored, rejected, rate_limited, too_large, bad_payload, error) |
| `acp_login_attempts_total` | counter | `method` (oidc, breakglass), `result` (success, failed, denied, rate_limited) |
| `acp_http_requests_total` | counter | `route`, `method`, `status` |
| `acp_http_request_duration_seconds` | histogram | `route` |

Counters reset when the app restarts. Alert on
`time() - acp_connector_last_success_timestamp_seconds` for stale connectors
and on a growing `acp_runs_queued` with a flat `acp_runs_started_total` for a
stuck worker.

## Audit trail for CLI actions

`acp admin` commands that change state write audit entries with actor label
`cli` (and `"source": "cli"` in the details), using the same action names as the
REST API: `group_rule.apply`, `remediation.rule.create`, `remediation.run`
(`alert remediate`), `fleet.host.add_to_inventory`, `connector.create`,
`connector.update`, `connector.delete`. Audit write failures are reported on
stderr and do not undo the action.

## Worker egress allowlist

The worker sits on the `egress` network, which would otherwise allow any
outbound traffic. `scripts/worker-egress.sh apply|remove|status` restricts it
with iptables rules in `DOCKER-USER` (forwarded traffic) and `INPUT` (traffic to
the Docker host itself). Only traffic entering from the egress bridge is
matched; the bridge name is resolved from `docker network inspect`. Every rule
carries the comment `acp-egress`, so `apply` replaces and `remove` deletes
exactly those rules.

- `.env` variables (read by grep, never sourced; if either is missing the script
  changes nothing and exits non-zero):
  - `ACP_WORKER_EGRESS_CIDRS`: space-separated destination CIDRs the worker may reach.
  - `ACP_WORKER_EGRESS_PORTS`: comma-separated allowed TCP ports (e.g. `22,2222`).
- Everything else from the egress bridge is dropped (internet, other ports, DNS).
  Inventories must therefore use IP addresses.
- Persistence: install `deploy/systemd/acp-worker-egress.service` to
  `/etc/systemd/system/`, then `systemctl daemon-reload && systemctl enable --now acp-worker-egress`.
  The unit runs after and with `docker.service`.
- After the egress network was recreated (e.g. `docker compose down` / `up`) or
  the allowlist variables changed: `systemctl restart acp-worker-egress`.
- Rollback: `scripts/worker-egress.sh remove` and `systemctl disable --now acp-worker-egress`.
- Pre-existing risk: `systemctl restart nftables` (or `nft -f /etc/nftables.conf`,
  which starts with `flush ruleset`) flushes Docker's iptables-nft rules,
  including these. Restart Docker afterwards and then `acp-worker-egress`.

## API

The REST API of the app listener is described by `api/openapi.yaml` (OpenAPI 3.1, hand-maintained). The running app serves it to any signed-in user at `GET /api/v1/openapi.yaml`. Every operation carries `x-acp-permission` (a policy permission, `authenticated` or `public`). The internal worker API is not part of the document.

- Drift guard: `TestOpenAPIMatchesRoutes` (`internal/server/openapi_test.go`, part of `make test`) fails when a registered route has no operation, an operation has no route, or the permission differs from the one the route declares.
- Frontend types: `web/src/lib/api/schema.d.ts` is generated and committed. After editing the spec run `npm run gen:api` in `web/` (Docker: `docker run --rm -v $PWD:/repo -w /repo/web node:24-alpine npm run gen:api`). CI runs `npm run check:api`, which fails when the committed file differs from a fresh generation.
- `web/src/lib/api.ts` re-exports the generated schemas under short names; request/response shapes are changed in the spec, not in `api.ts`.

## Security model

Summary; decisions are in the ADRs.

- **Trust boundaries:** the app holds the database access and the master key; the
  worker has neither (ADR-0004), no Docker socket, and talks only to the internal
  worker API with a bearer token (ADR-0016). The worker API listener is never published.
- **Secrets:** envelope encryption with a master key file, write-only API, never
  logged or returned, masked in logs and (for viewers) in run parameters (ADR-0005).
- **Authentication:** OIDC with PKCE plus break-glass admin with TOTP and lockout (ADR-0006).
- **Authorization:** one policy package with role and scope checks on every handler (ADR-0007).
- **Job isolation:** each run executes under its own unprivileged job uid without
  capabilities, separate from the worker user holding the token (ADR-0020).
- **Connectors:** read-only, GET-only HTTP client, TLS verification on by default.

Threat assumptions: the host, Docker daemon and the operator with access to
`secrets/master.key` are trusted. Authors of playbooks in connected Git projects
are semi-trusted: their code runs in the worker, which is why isolation, the
egress allowlist, environment and variable denylists and masking exist. Network
attackers are expected to reach only the public ports.

Hardening checklist:

- TLS enabled (or a TLS proxy) and `ACP_ALLOW_INSECURE_HTTP` unset.
- `secrets/` owned by uid 10001, mode 600; `master.key` backed up offline and separately.
- OIDC enabled, roles least-privileged, break-glass password in a vault.
- Worker egress allowlist applied and persisted (`acp-worker-egress` unit).
- Worker log shows "job isolation enabled"; `ACP_WORKER_JOB_UID_BASE` not set to 0.
- Backups scheduled and a restore tested on a throwaway project.
- Metrics scraped from the internal network, or `ACP_METRICS_TOKEN` set.
- Ports 8082 and the database never published.

Vulnerabilities: see [`../SECURITY.md`](../SECURITY.md).

## Notifications

Run notifications (webhook, Matrix, SMTP, Gotify, ntfy) are configured per workspace; the app container needs outbound access to the targets. See [`notifications.md`](notifications.md). Delivery rows and events are pruned with `ACP_RUN_LOG_RETENTION_DAYS`.

## Troubleshooting

| Symptom | Cause and fix |
| --- | --- |
| `serve` exits: "plain http on a non-loopback host" | Use an `https://` public URL with TLS, or `ACP_ALLOW_INSECURE_HTTP=true` for lab use |
| App cannot read TLS key or master key ("permission denied") | Bind-mounted secrets keep host ownership: `chown 10001`, mode 600 |
| Runs stay queued | Worker down or token mismatch: check `docker compose logs worker`, that `secrets/worker.token` exists for app and worker, and `acp_runs_queued` |
| Worker exits: capabilities missing | Compose must keep `cap_add: [SETUID, SETGID, CHOWN, KILL]`; see Job isolation above |
| Playbook cannot reach hosts or resolve names | Egress allowlist: check `ACP_WORKER_EGRESS_*`, use IP addresses in inventories, `scripts/worker-egress.sh status` |
| Egress rules vanished | `nftables` reload flushed them: restart Docker, then `acp-worker-egress` |
| Secrets unreadable after restore | The restored dump was made with another master key; put the original `master.key` in place |
| Login redirects to `/login?error=no_access` | The user has no `acp-*` role in the roles claim; check `ACP_OIDC_ROLES_CLAIM`, scopes and IdP role assertion |
| `/login?error=rate_limited` or HTTP 429 | OIDC login budget exceeded (20 per minute per IP); behind a proxy all clients share one IP |
| OIDC `idp_unavailable` | Issuer unreachable from the app container or wrong `ACP_OIDC_ISSUER` |
| Project sync fails with host key error | Confirm the host key (trust on first use with confirmation) in the project settings |
| Postgres log: "relation goose_db_version does not exist" on first start | Expected before the first migration |
| `/readyz` fails | Database unreachable or migrations failing: `docker compose logs app postgres` |
