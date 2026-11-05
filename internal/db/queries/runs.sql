-- name: EnqueueRun :one
INSERT INTO runs (workspace_id, template_id, template_name, project_id, project_revision, trigger,
    triggered_by, schedule_id, overrides, params)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
RETURNING *;

-- name: GetRun :one
SELECT * FROM runs WHERE id = $1 AND workspace_id = $2;

-- name: GetRunByID :one
SELECT * FROM runs WHERE id = $1;

-- name: LockRunClaim :exec
-- Serializes claims so that the per-template concurrency check cannot race.
-- Transaction-scoped; call it first in the same transaction as ClaimRun.
SELECT pg_advisory_xact_lock(hashtext('acp.run.claim'));

-- name: ClaimRun :one
UPDATE runs
SET status = 'running', worker_id = $1, lease_id = gen_random_uuid(),
    claimed_at = now(), started_at = now(), last_heartbeat_at = now()
WHERE runs.id = (
    SELECT r.id FROM runs r
    JOIN templates t ON t.id = r.template_id
    WHERE r.status = 'queued'
      AND (SELECT count(*) FROM runs x WHERE x.template_id = r.template_id AND x.status = 'running') < t.max_concurrent
    ORDER BY r.created_at, r.id
    LIMIT 1
    FOR UPDATE OF r SKIP LOCKED
)
RETURNING *;

-- name: HeartbeatRun :one
UPDATE runs SET last_heartbeat_at = now()
WHERE id = $1 AND lease_id = $2 AND status = 'running'
RETURNING cancel_requested_at;

-- name: RequestRunCancel :one
UPDATE runs SET cancel_requested_at = COALESCE(cancel_requested_at, now())
WHERE id = $1 AND workspace_id = $2 AND status = 'running'
RETURNING *;

-- name: CancelQueuedRun :one
UPDATE runs SET status = 'cancelled', cancel_requested_at = now(), finished_at = now(),
    params = params - 'secret_vars_enc'
WHERE id = $1 AND workspace_id = $2 AND status = 'queued'
RETURNING *;

-- name: FinishRun :one
-- Host summary counts are derived from run_host_results.
UPDATE runs r
SET status = $3, exit_code = $4, finished_at = now(), params = r.params - 'secret_vars_enc',
    hosts_ok = s.ok, hosts_changed = s.changed, hosts_failed = s.failed, hosts_unreachable = s.unreachable
FROM (
    SELECT count(*) FILTER (WHERE failed = 0 AND unreachable = 0) AS ok,
           count(*) FILTER (WHERE changed > 0) AS changed,
           count(*) FILTER (WHERE failed > 0) AS failed,
           count(*) FILTER (WHERE unreachable > 0) AS unreachable
    FROM run_host_results WHERE run_id = $1
) s
WHERE r.id = $1 AND r.lease_id = $2 AND r.status = 'running'
RETURNING r.*;

-- name: MarkLostRuns :many
UPDATE runs SET status = 'lost', finished_at = now(), params = params - 'secret_vars_enc'
WHERE status = 'running' AND last_heartbeat_at < now() - make_interval(secs => sqlc.arg(stale_seconds)::float8)
RETURNING *;

-- name: AppendRunLogChunk :execrows
INSERT INTO run_log_chunks (run_id, seq, data) VALUES ($1, $2, $3)
ON CONFLICT (run_id, seq) DO NOTHING;

-- name: ListRunLogChunks :many
SELECT * FROM run_log_chunks WHERE run_id = $1 AND seq > $2 ORDER BY seq LIMIT $3;

-- name: LastRunLogSeq :one
SELECT COALESCE(max(seq), 0)::bigint FROM run_log_chunks WHERE run_id = $1;

-- name: DeleteOldRunLogChunks :execrows
DELETE FROM run_log_chunks c
USING runs r
WHERE r.id = c.run_id AND r.finished_at IS NOT NULL AND c.created_at < $1;

-- name: UpsertRunHostResult :exec
INSERT INTO run_host_results (run_id, host, ok, changed, failed, unreachable, skipped, rescued, ignored, changed_tasks)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
ON CONFLICT (run_id, host) DO UPDATE SET
    ok = EXCLUDED.ok, changed = EXCLUDED.changed, failed = EXCLUDED.failed,
    unreachable = EXCLUDED.unreachable, skipped = EXCLUDED.skipped, rescued = EXCLUDED.rescued,
    ignored = EXCLUDED.ignored, changed_tasks = EXCLUDED.changed_tasks;

-- name: ListRunHostResults :many
SELECT * FROM run_host_results WHERE run_id = $1 ORDER BY host;

-- name: ListRuns :many
-- Keyset pagination: pass the created_at and id of the last row as the cursor.
SELECT sqlc.embed(r),
       COALESCE(NULLIF(u.display_name, ''), NULLIF(u.email, ''), u.username, '')::text AS triggered_by_name
FROM runs r
LEFT JOIN users u ON u.id = r.triggered_by
WHERE r.workspace_id = sqlc.arg(workspace_id)
  AND (sqlc.narg(template_id)::uuid IS NULL OR r.template_id = sqlc.narg(template_id))
  AND (sqlc.narg(status)::text IS NULL OR r.status = sqlc.narg(status))
  AND (sqlc.narg(trigger)::text IS NULL OR r.trigger = sqlc.narg(trigger))
  AND (sqlc.narg(triggered_by)::uuid IS NULL OR r.triggered_by = sqlc.narg(triggered_by))
  AND (sqlc.narg(host)::text IS NULL OR EXISTS (
        SELECT 1 FROM run_host_results h WHERE h.run_id = r.id AND h.host = sqlc.narg(host)))
  AND (sqlc.narg(cursor_created_at)::timestamptz IS NULL
       OR (r.created_at, r.id) < (sqlc.narg(cursor_created_at)::timestamptz, sqlc.narg(cursor_id)::uuid))
ORDER BY r.created_at DESC, r.id DESC
LIMIT sqlc.arg(page_size);

-- name: CountRunningForTemplate :one
SELECT count(*) FROM runs WHERE template_id = $1 AND status = 'running';

-- name: CancelQueuedRunsForTemplate :many
-- Used when a template is deleted: queued runs would never become claimable
-- again (claiming joins the template), so they are cancelled in the same transaction.
UPDATE runs SET status = 'cancelled', cancel_requested_at = now(), finished_at = now(),
    params = params - 'secret_vars_enc'
WHERE template_id = $1 AND workspace_id = $2 AND status = 'queued'
RETURNING id;

-- name: MarkRunBundleServed :one
-- Succeeds once per lease: the bundle carries decrypted credentials.
UPDATE runs SET bundle_served_at = now()
WHERE id = $1 AND lease_id = $2 AND status = 'running' AND bundle_served_at IS NULL
RETURNING *;

-- name: GetRunForLease :one
SELECT * FROM runs WHERE id = $1 AND lease_id = $2 AND status = 'running';

-- name: RequestCancelOrphanRuns :many
-- Running runs whose template was deleted are stopped (the worker sees the
-- cancel flag with its next heartbeat).
UPDATE runs SET cancel_requested_at = now()
WHERE status = 'running' AND template_id IS NULL AND cancel_requested_at IS NULL
RETURNING id;

-- name: MarkRunHostEvent :exec
-- Idempotent progress marker from a callback event; the final counters come
-- from SetRunHostStats. kind is one of ok, changed, failed, unreachable.
INSERT INTO run_host_results (run_id, host, changed, failed, unreachable, changed_tasks)
VALUES (
    sqlc.arg(run_id), sqlc.arg(host),
    CASE WHEN sqlc.arg(kind)::text = 'changed' THEN 1 ELSE 0 END,
    CASE WHEN sqlc.arg(kind)::text = 'failed' THEN 1 ELSE 0 END,
    CASE WHEN sqlc.arg(kind)::text = 'unreachable' THEN 1 ELSE 0 END,
    CASE WHEN sqlc.arg(kind)::text = 'changed' THEN jsonb_build_array(sqlc.arg(task)::text) ELSE '[]'::jsonb END
)
ON CONFLICT (run_id, host) DO UPDATE SET
    changed = GREATEST(run_host_results.changed, EXCLUDED.changed),
    failed = GREATEST(run_host_results.failed, EXCLUDED.failed),
    unreachable = GREATEST(run_host_results.unreachable, EXCLUDED.unreachable),
    changed_tasks = CASE
        WHEN sqlc.arg(kind)::text = 'changed'
         AND jsonb_array_length(run_host_results.changed_tasks) < 200
         AND NOT run_host_results.changed_tasks ? sqlc.arg(task)::text
        THEN run_host_results.changed_tasks || jsonb_build_array(sqlc.arg(task)::text)
        ELSE run_host_results.changed_tasks END;

-- name: SetRunHostStats :exec
-- Final per-host counters from the stats event; changed_tasks is kept.
INSERT INTO run_host_results (run_id, host, ok, changed, failed, unreachable, skipped, rescued, ignored)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
ON CONFLICT (run_id, host) DO UPDATE SET
    ok = EXCLUDED.ok, changed = EXCLUDED.changed, failed = EXCLUDED.failed,
    unreachable = EXCLUDED.unreachable, skipped = EXCLUDED.skipped, rescued = EXCLUDED.rescued,
    ignored = EXCLUDED.ignored;

-- name: NotifyRun :exec
SELECT pg_notify(sqlc.arg(channel)::text, sqlc.arg(payload)::text);

-- name: GetUserLabel :one
-- Display label of a user (display name, else email, else username).
SELECT COALESCE(NULLIF(display_name, ''), NULLIF(email, ''), username, '')::text AS label
FROM users WHERE id = $1;
