-- name: GetReachabilityConfig :one
SELECT * FROM reachability_config WHERE workspace_id = $1;

-- name: UpsertReachabilityConfig :one
-- Enabling the interval makes the first probe due immediately; disabling
-- clears the schedule.
INSERT INTO reachability_config (workspace_id, inventory_id, ssh_secret_id, interval_seconds, next_run_at)
VALUES ($1, $2, $3, $4, CASE WHEN $4::int > 0 THEN now() END)
ON CONFLICT (workspace_id) DO UPDATE SET
    inventory_id = EXCLUDED.inventory_id,
    ssh_secret_id = EXCLUDED.ssh_secret_id,
    interval_seconds = EXCLUDED.interval_seconds,
    next_run_at = CASE
        WHEN EXCLUDED.interval_seconds = 0 THEN NULL
        WHEN reachability_config.interval_seconds = EXCLUDED.interval_seconds AND reachability_config.next_run_at IS NOT NULL
            THEN reachability_config.next_run_at
        ELSE now() END,
    updated_at = now()
RETURNING *;

-- name: ClaimDueReachability :many
-- Must run inside a transaction together with AdvanceReachability; the row
-- locks keep other app instances from claiming the same workspace.
SELECT * FROM reachability_config
WHERE interval_seconds > 0 AND inventory_id IS NOT NULL AND next_run_at IS NOT NULL AND next_run_at <= now()
ORDER BY next_run_at, workspace_id
LIMIT $1
FOR NO KEY UPDATE SKIP LOCKED;

-- name: AdvanceReachability :exec
UPDATE reachability_config
SET next_run_at = now() + make_interval(secs => interval_seconds), last_run_at = COALESCE(sqlc.narg(last_run_at), last_run_at)
WHERE workspace_id = $1;

-- name: CountActiveProbeRuns :one
SELECT count(*) FROM runs
WHERE workspace_id = $1 AND params->>'builtin' = 'probe' AND status IN ('queued', 'running');

-- name: EnqueueBuiltinRun :one
INSERT INTO runs (workspace_id, template_name, trigger, triggered_by, overrides, params)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING *;

-- name: ListReachabilitySources :many
SELECT s.id, s.workspace_id, s.host_id, s.created_at, h.name AS host_name, h.inventory_id
FROM reachability_sources s JOIN inventory_hosts h ON h.id = s.host_id
WHERE s.workspace_id = $1
ORDER BY h.name;

-- name: CreateReachabilitySource :one
INSERT INTO reachability_sources (workspace_id, host_id) VALUES ($1, $2) RETURNING *;

-- name: DeleteReachabilitySource :execrows
DELETE FROM reachability_sources WHERE id = $1 AND workspace_id = $2;

-- name: ListReachabilityTargets :many
SELECT * FROM reachability_targets WHERE workspace_id = $1 ORDER BY name;

-- name: GetReachabilityTarget :one
SELECT * FROM reachability_targets WHERE id = $1 AND workspace_id = $2;

-- name: CreateReachabilityTarget :one
INSERT INTO reachability_targets (workspace_id, name, address, check_type, port, dns_name, enabled)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING *;

-- name: UpdateReachabilityTarget :one
UPDATE reachability_targets
SET name = $3, address = $4, check_type = $5, port = $6, dns_name = $7, enabled = $8, updated_at = now()
WHERE id = $1 AND workspace_id = $2
RETURNING *;

-- name: DeleteReachabilityTarget :execrows
DELETE FROM reachability_targets WHERE id = $1 AND workspace_id = $2;

-- name: ListReachabilityResults :many
SELECT r.* FROM reachability_results r
JOIN reachability_sources s ON s.id = r.source_id
WHERE s.workspace_id = $1;

-- name: ProbeSourceByHost :one
-- Resolves a probe source of the run's workspace by inventory host name.
SELECT s.id FROM reachability_sources s JOIN inventory_hosts h ON h.id = s.host_id
WHERE s.workspace_id = $1 AND h.inventory_id = $2 AND h.name = $3;

-- name: UpsertReachabilityResult :exec
-- Only targets of the same workspace are stored.
INSERT INTO reachability_results (source_id, target_id, status, latency_ms, detail, run_id, checked_at)
SELECT $1, t.id, $3, $4, $5, $6, now()
FROM reachability_targets t
WHERE t.id = $2 AND t.workspace_id = $7
ON CONFLICT (source_id, target_id) DO UPDATE SET
    status = EXCLUDED.status, latency_ms = EXCLUDED.latency_ms, detail = EXCLUDED.detail,
    run_id = EXCLUDED.run_id, checked_at = EXCLUDED.checked_at;

-- name: MarkSourceUnreachable :exec
-- A dead source marks every enabled target probe_unreachable.
INSERT INTO reachability_results (source_id, target_id, status, latency_ms, detail, run_id, checked_at)
SELECT sqlc.arg(source_id), t.id, 'probe_unreachable', NULL, '', sqlc.arg(run_id), now()
FROM reachability_targets t
WHERE t.workspace_id = sqlc.arg(workspace_id) AND t.enabled
ON CONFLICT (source_id, target_id) DO UPDATE SET
    status = 'probe_unreachable', latency_ms = NULL, detail = '', run_id = EXCLUDED.run_id, checked_at = EXCLUDED.checked_at;

-- name: GetRunHostResult :one
SELECT * FROM run_host_results WHERE run_id = $1 AND host = $2;
