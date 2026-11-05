# Templates and runs (REST API)

Implements the user-facing part of ADR-0016 (RUN-1, RUN-2, RUN-5, RUN-7). All routes live under
`/api/v1/workspaces/{ws}/` and declare their permission through the central policy package.

| Route | Permission |
|-------|------------|
| `GET templates`, `GET templates/{id}` | `template.read` |
| `POST templates`, `PUT/DELETE templates/{id}` | `template.write` (unscoped memberships only) |
| `POST templates/{id}/runs`, `POST templates/{id}/runs/preview` | `run.execute` |
| `GET runs` (filters: `template`, `status`, `trigger`, `user`, `host`, `cursor`, `limit`), `GET runs/{id}` | `run.read` |
| `POST runs/{id}/cancel` | `run.cancel` |

## Templates

A template references a project (with a playbook from the project's last scan), an inventory, optional
SSH/become secrets (by id, never by value) and an ordered list of vault identities
(`vault_identity_ids`). Create validates that everything belongs to the workspace and that the playbook
exists in the latest scanned revision. Update is a partial `PUT`; `null` clears nullable references.
Deleting a template cancels its queued runs in the same transaction; run history is kept.

`allow_*_override` flags decide which per-run overrides are accepted (limit, tags/skip_tags, check, diff,
verbosity, extra vars). Without `allow_vars_override`, only variables defined by the template survey may
be set at run time. The survey is a list of fields (shape of `scan.Field`) or an object with `fields`;
without a template survey the `argument_specs` of the roles used by the playbook type-check supplied
variables (required fields are not enforced there).

## Starting a run

`POST templates/{id}/runs` takes optional overrides (`limit`, `tags`, `skip_tags`, `extra_vars`, `check`,
`diff`, `verbosity`). The service merges them with the template defaults, validates the survey, pins the
project's latest synced revision, resolves the limit against the inventory (Ansible pattern subset: names,
groups, wildcards, `:`/`,` union, `:&` intersection, `:!` exclusion) and authorizes the effective host set
with `Authorizer.AuthorizeHosts` (403 when outside the caller's scope, 422 when the limit matches no
host). The run row stores the full parameter snapshot (`runs.params`) so later template edits do not
affect queued runs. Values of survey fields flagged `secret` are masked in every API response.

Every run response (list, detail, start, cancel, the `runs/events` payload) carries `triggered_by_name`: the
display name of the user (email or username as fallback), or `null` for schedule and webhook runs without a
user. The `user` filter still takes the user id.

### Preview

`POST templates/{id}/runs/preview` takes the same body as start and runs the same validation (allowed
overrides, survey, limit pattern, host scope authorization) but creates no run and writes no audit entry.
The response has `host_count`, `hosts` (the first 50 names), `truncated` and `all_in_scope` (always `true`:
a host set outside the caller's scope is rejected with 403, a limit matching nothing with 422, as in start).

Scoped memberships only see runs whose whole host set lies in their scope (others are 404).

## Run execution (operator view)

Runs are executed by the separate `worker` container (ADR-0004, ADR-0016). The worker holds no database
credentials and no master key; it talks to the app over the internal compose network only. For SSH to managed hosts it additionally joins the dedicated `egress` network (the only service that does).

### Worker token

App and worker share a bearer token, mounted as the Docker secret `worker_token` from
`./secrets/worker.token` (`make init` creates it with uid 10001, mode 600; the app reads it through
`ACP_WORKER_TOKEN_FILE`, the worker through the same variable). Without the token file the app does not
start the worker API at all, and queued runs stay queued. The routes are not part of the user permission
model (`/api/v1/...`); they accept only this token, compared in constant time.

The worker API has its own plain-HTTP listener (`ACP_WORKER_API_ADDR`, default `:8082`) that serves nothing
but `/api/internal/worker/*`. The compose file does not publish this port and the worker reaches it as
`http://app:8082` over the internal network, so the worker needs no certificates. The public listeners
(`:8080` redirect, `:8443`) do not know these paths and answer 404 or redirect. Never publish port 8082 or
route `/api/internal/` through a reverse proxy.

### Lifecycle

1. `ACP_WORKER_CONCURRENCY` (default 2) claim loops in the worker long-poll `POST claim` (up to 25 s). The
   app claims through `db.ClaimRun` (advisory lock, per-template `max_concurrent`).
2. The worker fetches the bundle once per lease: project tree at the pinned revision (`git archive` from
   the app's cache clone), the unmasked inventory, `extra_vars.json` (including the decrypted secret survey
   values), the SSH key, the become password and one password file per vault identity. It is unpacked to
   `/tmp/runs/<run id>` (tmpfs, mode 0700) and deleted afterwards.
3. If the project has `requirements.yml`, `collections/requirements.yml` or `roles/requirements.yml`, the
   worker runs `ansible-galaxy` into `$ACP_WORKER_CACHE_DIR/galaxy/<sha256 of the files>` and reuses the
   result for later runs with identical files (file lock; no secrets in that cache).
4. `ansible-playbook` runs without a shell, as uid 10001, in its own process group, from the project
   directory (so the project's `ansible.cfg` applies). The inventory is a tiny script printing the
   rendered `--list` JSON. Environment order: passthrough of `PATH`, `HOME`, `LC_*`, proxy and `ANSIBLE_*`
   image settings, then the template's `env_vars`, then worker-enforced variables last (event callback
   `acp`, `ANSIBLE_FORCE_COLOR`, events file, temp directories). Templates cannot set `ACP_*`,
   `ANSIBLE_CALLBACK*` or `ANSIBLE_STDOUT_CALLBACK`. Host key checking is not changed: set
   `ANSIBLE_HOST_KEY_CHECKING` in the template's `env_vars` or in the project's `ansible.cfg` if needed.
5. stdout and stderr are streamed in chunks (at most 64 KiB, flushed every 500 ms, numbered by `seq`);
   callback events become per-host results with the names of changed tasks. A heartbeat every 5 s carries
   the cancel flag.
6. Cancel sends SIGTERM to the process group and SIGKILL after 10 s; the run ends as `cancelled`. Exit code 0
   is `succeeded`, any other exit `failed`. Failures before the playbook starts (bundle, Galaxy) end as
   `failed` with the reason in the log.

### Liveness and cleanup

- The app marks a running run as `lost` after 60 s without heartbeat (checked every 15 s) and requests
  cancellation of running runs whose template was deleted.
- A worker that loses its lease (HTTP 409) stops the process and does not report a result.
- Values of secret survey fields are stored envelope-encrypted inside `runs.params` (`secret_vars_enc`),
  only the bundle builder decrypts them, and the envelope is removed when the run finishes, is cancelled or
  is marked lost. The API keeps showing `***`.

### Log masking

Known secret values (SSH key and its lines, become password, vault passwords, secret survey values) are
replaced by `***` in the worker before sending and again in the app before persisting
(`run_log_chunks`). Values shorter than 4 characters are not masked. Anything a playbook prints that is not
one of these values (for example a password a task derives) is not recognized: use `no_log` for such tasks.
After each batch the app issues `NOTIFY acp_run_<run id without dashes>` (payload `log:<last seq>` or
`status:<status>`) for the SSE stream. Status transitions also notify the channel `acp_runs` (payload: run id)
for the run list stream.

### Live logs and run status (SSE, ADR-0008)

All endpoints need `run.read` and apply the same scope check as `GET runs/{id}` (out-of-scope runs: 404).

- `GET /api/v1/workspaces/{ws}/runs/{id}/log` (`text/event-stream`): replays stored chunks after
  `Last-Event-ID` (or `?after=<seq>`), then follows the run live. Events: `log` (id = chunk `seq`, data = the
  chunk as a JSON string, so CR/LF and ANSI escapes are preserved), `status` (JSON: status, timestamps, exit
  code, host counters and per-host results; sent when it changes), `end` (run finished and all chunks sent,
  then the stream closes). A `: ping` comment is sent every 15 s. Browsers resume automatically with
  `Last-Event-ID`. At most 10 concurrent streams per user (HTTP 429 beyond that).
- `GET /api/v1/workspaces/{ws}/runs/events`: `run` events (JSON run object) when a run visible to the caller
  changes status (queued, running, finished) among the 50 newest runs. No initial snapshot: load the list
  through REST first and again after a reconnect.
- `GET /api/v1/workspaces/{ws}/runs/{id}/log.txt`: the stored log as a plain-text download (ANSI kept).

All streams share one PostgreSQL `LISTEN` connection (one subscription per run, not per browser); streams
also re-read the database every 5 s, so a missed notification only delays updates. The handler lifts the write
deadline for its own response only. Reverse proxies must not buffer these paths (`X-Accel-Buffering: no` is
set; for nginx also `proxy_buffering off`, long `proxy_read_timeout`). HTTP/2 works as is.

### Log retention

The app deletes log chunks of finished runs older than `ACP_RUN_LOG_RETENTION_DAYS` (default 90, `0` keeps
them forever) once at start and then daily. Run rows, host results and the audit log are kept; an expired
log is simply empty.

### End-to-end smoke test

`scripts/test-run-e2e.sh` starts a throwaway compose project (`scripts/e2e/compose.yml`, project name
`acp-e2e-<pid>`, own network and volumes, no published ports, images `acp-e2e-*:test`) with postgres, a
dumb-HTTP git server serving `scripts/e2e/fixture`, app and worker. It seeds a workspace, project,
inventory and templates via SQL, syncs the project with `acp admin project sync`, then asserts a
successful run (status, host summary, log, changed tasks, run directory removed) and a cancelled run. It
does not exercise Galaxy installs (needs internet) or credentials; those are covered by unit tests.
`KEEP=1` leaves the stack up for debugging. The live stack is never touched.

## Schedules (RUN-6)

A template can have any number of cron schedules. Endpoints under `/api/v1/workspaces/{ws}/`:

- `GET|POST templates/{id}/schedules` – list / create (`schedule.read` / `schedule.write`).
- `GET|PUT|DELETE schedules/{id}` – read / change / delete. PUT is partial: omitted fields keep their value;
  every change recomputes `next_run_at` from now.
- `GET schedules/preview?cron=&tz=&n=5` – the next `n` (1-20) run times of an expression without saving it.

Fields: `name`, `cron` (five fields `minute hour day-of-month month day-of-week`, lists, ranges, `*/n`,
month and weekday names, `@hourly`/`@daily`/`@weekly`/`@monthly`/`@yearly`), `timezone` (IANA name, default
`UTC`; `Local` is rejected; zone data is embedded in the binary), `enabled`, `missed_policy` and `overrides`
(same shape and rules as a manual start; checked on save, so overrides the template does not allow are 422).
Like Vixie cron, a day matches when either restricted day field (day-of-month or day-of-week) matches.

Daylight saving: wall-clock times that do not exist (spring-forward gap) are skipped for that day; a time that
occurs twice (fall-back) fires once, at its first occurrence. Pick a time outside 02:00-03:00 if a daily job
must never be skipped.

Scheduler: the app polls every 30 seconds, claims due schedules with `FOR UPDATE SKIP LOCKED` in one
transaction, enqueues the run with `trigger=schedule`, `triggered_by` = the schedule's creator (the creator
being deleted leaves it empty) and advances `next_run_at`, all before commit, so several app instances never
start the same slot. Scheduled runs use workspace authority (no per-host scope check, as the creator needed
`schedule.write`, which scoped memberships do not get). Missed runs (the app was down; a due time more than
90 seconds in the past): `skip` (default) advances to the next future slot without running, `run_once`
starts one run and then advances, so any number of missed slots collapse into one run. A schedule whose
enqueue fails (playbook gone, project unsynced, limit matches nothing) stays enabled, is advanced and audited
as `schedule.run_failed`; if the template no longer exists it is disabled (`schedule.disabled`). Disabled
schedules have no `next_run_at`. Audit actions: `schedule.create|update|delete` (user), `schedule.run`,
`schedule.run_skipped`, `schedule.run_failed`, `schedule.disabled` (actor `schedule`).

## Template webhooks

`POST templates/{id}/webhook` (`template.write`, workspace-level memberships only) enables the webhook and
generates a new secret; calling it again rotates the secret. The secret is stored through the secrets
package and shown **once** in the response (`secret`), together with `path` and, when `ACP_PUBLIC_URL` is
set, the absolute `url`. `GET` reports `enabled`/`has_secret` only; `DELETE` disables the hook and deletes
the secret. Audit: `template.webhook_set`, `template.webhook_delete`.

The public endpoint is `POST /api/hooks/templates/{id}`; there is no session or CSRF. It accepts any
signature scheme of the project hooks, all HMAC-SHA256 over the raw body with the secret (GitLab: the
token compared as is): `X-ACP-Signature: sha256=<hex>`, `X-Gitea-Signature`, `X-Hub-Signature-256`,
`X-Gitlab-Token`. Unknown template, disabled hook and bad signature are indistinguishable (404 `not found`),
bodies are limited to 1 MiB and calls are limited to 60 per minute per peer IP, one bucket shared with the
project hooks (behind a reverse proxy all callers share one bucket). On success the response is 202 with
the `run_id`; the run has `trigger=webhook` and no `triggered_by`, and runs with workspace authority.
Only a body signed with `X-ACP-Signature` may carry JSON run overrides (the fields of a manual start); they
are subject to the template's `allow_*_override` flags. Bodies of Git providers are push payloads and are
ignored. An authenticated call that cannot start a run answers 422 (invalid overrides, empty limit) or 409
(project not synced). Each call is audited as `template.webhook_run` or `template.webhook_failed` (actor
`webhook:<scheme>`). Rejected calls are not audited.

Example (generic scheme):

    body='{"limit":"web"}'
    sig=$(printf '%s' "$body" | openssl dgst -sha256 -hmac "$SECRET" -hex | sed 's/^.* //')
    curl -X POST -H "X-ACP-Signature: sha256=$sig" -d "$body" "$ACP_PUBLIC_URL/api/hooks/templates/$TEMPLATE_ID"

## Web UI

- `/templates`: list and create; `/templates/{id}`: details, edit, delete, run dialog, recent runs, schedules
  and webhook (secret shown once). The run dialog offers only the overrides the template allows and renders the
  template survey (secret fields as password inputs). The dialog calls the preview endpoint
  (debounced on limit changes) and shows the effective host count and the first host names.
- `/runs`: history with filters (template, status, trigger, user id with a "Me" shortcut, host) and the name of the user who started a run, cursor pagination and live
  updates through `runs/events` (reload after a reconnect). `/runs/{id}`: status header, cancel, live log
  (SSE, ANSI rendered as escaped text segments, search, auto-scroll, download) and per-host results.
- Schedule overrides are not editable in the UI yet; existing overrides are kept on edit.

## Admin CLI (templates and runs)

For operators and live ops, run via `docker compose exec app acp admin ...`. All commands take
`--workspace SLUG` (default `default`), print plain text and never print secret values (secret
survey variables are masked, extra variable values are not echoed). Create and start are audited
with actor `cli`.

- `template list`
- `template create --name N --project SLUG|ID --playbook PATH --inventory NAME|ID [--ssh-key-secret NAME]
  [--become-secret NAME] [--vault-identity LABEL]... [--limit PATTERN] [--max-concurrent N]`: same
  validation as the API (playbook must exist in the last project scan, secret types, workspace scope).
- `run start --template NAME|ID [--limit P] [--tags T] [--check] [--diff] [-e key=value]... [--wait]`:
  prints `run_id`. The run has trigger `api` and no `triggered_by` user (the CLI has workspace authority, no
  per-host scope check). With `--wait` the masked, ANSI-stripped log is followed until the run
  finishes; the exit code is non-zero unless the status is `succeeded`.
- `run show RUN_ID [--log] [--tail N]`: status, timestamps, host summary and per-host results; `--log`
  prints the stored log, `--tail N` only the last N lines.
- `run list [--template NAME|ID] [--status S] [--limit N]`
