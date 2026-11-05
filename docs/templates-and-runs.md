# Templates and runs (REST API)

Implements the user-facing part of ADR-0016 (RUN-1, RUN-2, RUN-5, RUN-7). All routes live under
`/api/v1/workspaces/{ws}/` and declare their permission through the central policy package.

| Route | Permission |
|-------|------------|
| `GET templates`, `GET templates/{id}` | `template.read` |
| `POST templates`, `PUT/DELETE templates/{id}` | `template.write` (unscoped memberships only) |
| `POST templates/{id}/runs` | `run.execute` |
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

Scoped memberships only see runs whose whole host set lies in their scope (others are 404).

## Run execution (operator view)

Runs are executed by the separate `worker` container (ADR-0004, ADR-0016). The worker holds no database
credentials and no master key; it talks to the app over the internal compose network only.

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

### End-to-end smoke test

`scripts/test-run-e2e.sh` starts a throwaway compose project (`scripts/e2e/compose.yml`, project name
`acp-e2e-<pid>`, own network and volumes, no published ports, images `acp-e2e-*:test`) with postgres, a
dumb-HTTP git server serving `scripts/e2e/fixture`, app and worker. It seeds a workspace, project,
inventory and templates via SQL, syncs the project with `acp admin project sync`, then asserts a
successful run (status, host summary, log, changed tasks, run directory removed) and a cancelled run. It
does not exercise Galaxy installs (needs internet) or credentials; those are covered by unit tests.
`KEEP=1` leaves the stack up for debugging. The live stack is never touched.
