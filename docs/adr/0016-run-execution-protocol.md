# ADR-0016: Run execution protocol (queue, worker API, bundle, events)

- Status: accepted
- Date: 2026-11-05

## Context
M4 implements RUN-1..7 and GIT-4 on top of ADR-0003 (Postgres as queue), ADR-0004 (isolated pull worker) and ADR-0008 (SSE logs). The worker must stay free of DB credentials and the master key; the host has 4 GiB RAM.

## Decision
- **Worker image:** the full community `ansible` package (pinned, current release) on the pinned Python, so existing playbooks find the same collections they had under Semaphore. Project `requirements.yml` (GIT-4) is installed by the worker on top, into a cache volume keyed by `sha256(requirements file)`; the cache holds no secrets.
- **Queue:** `runs` rows move `queued → running → {succeeded, failed, cancelled, lost}`. Claiming uses `SELECT … FOR UPDATE SKIP LOCKED` and respects the per-template limit `max_concurrent` (default 1, RUN-7). Worker concurrency comes from `ACP_WORKER_CONCURRENCY` (default 2).
- **Worker API** (`/api/internal/worker/*`, served on a separate plain-HTTP listener `ACP_WORKER_API_ADDR` (default `:8082`) that is never published and exists only on the internal compose network; absent from the public app listeners; bearer token from a Docker secret file `worker_token`, constant-time compare, never accepted on public routes):
  - `POST claim` (long poll ≤ 25 s) → run id + lease;
  - `GET runs/{id}/bundle` → tar.gz with the project tree at the run's revision, rendered inventory (unmasked JSON), `extra_vars.json`, the command line and the credentials (SSH key, become password, vault passwords) decrypted just in time; served once per lease;
  - `POST runs/{id}/events` → batched log chunks (monotonic `seq`) and callback events;
  - `POST runs/{id}/heartbeat` (every 5 s) → response carries `cancel: true` when cancellation was requested;
  - `POST runs/{id}/finish` → exit code and final status.
- **Execution:** the bundle is unpacked on tmpfs (`/tmp/runs/<id>`, mode 0700), deleted after the run. `ansible-playbook` runs as uid 10001 with a bundled callback plugin (`acp`) that emits JSON events (task start, per-host result, stats) next to the normal stdout. Cancel = SIGTERM to the process group, SIGKILL after 10 s (RUN-3).
- **Logs & masking:** the worker masks known secret values before sending; the app masks again before persisting (`run_log_chunks`, ordered by `seq`) and publishes via `LISTEN/NOTIFY` for SSE fan-out with `Last-Event-ID` = `seq`. Per-host results land in `run_host_results` (RUN-4).
- **Liveness:** a run with no heartbeat for 60 s is marked `lost` by the app.
- **Authorization:** `run.execute` requires the effective host set (inventory ∩ `--limit`) to be inside the caller's scope (`Authorizer.AuthorizeHosts`); schedules and webhooks run as the template's owning workspace with the creator recorded as trigger user.
- **Schedules (RUN-6):** 5-field cron + IANA timezone; missed-run policy `skip` (default) or `run_once`; the app scheduler claims due schedules with SKIP LOCKED.
- **Retention (RUN-5):** log chunks older than `ACP_RUN_LOG_RETENTION_DAYS` (default 90) are deleted; run rows and host results are kept.

## Consequences
- The worker only needs network access to the app and managed hosts; a compromised worker sees secrets only for runs it currently holds.
- The full `ansible` package makes the worker image large (~1 GB); builds run with memory limits.
- Notifications (RUN-8) are out of scope for M4.
