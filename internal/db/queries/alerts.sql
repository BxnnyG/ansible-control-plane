-- name: UpsertAlert :exec
-- A re-firing resolved alert starts a new incident (started_at is replaced);
-- an open alert keeps its original start.
INSERT INTO alerts (workspace_id, connector_id, external_key, host_name, host_ip, service, severity, summary, labels, started_at, last_seen_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
ON CONFLICT (connector_id, external_key) DO UPDATE
SET host_name = EXCLUDED.host_name, host_ip = EXCLUDED.host_ip, service = EXCLUDED.service,
    severity = EXCLUDED.severity, summary = EXCLUDED.summary, labels = EXCLUDED.labels,
    started_at = CASE WHEN alerts.resolved_at IS NOT NULL THEN EXCLUDED.started_at ELSE alerts.started_at END,
    last_seen_at = EXCLUDED.last_seen_at, resolved_at = NULL;

-- name: ResolveAlertsNotIn :execrows
UPDATE alerts SET resolved_at = $3
WHERE connector_id = $1 AND resolved_at IS NULL AND NOT (external_key = ANY($2::text[]));

-- name: ResolveAlert :execrows
UPDATE alerts SET resolved_at = $3, last_seen_at = $3
WHERE connector_id = $1 AND external_key = $2 AND resolved_at IS NULL;

-- name: PruneAlerts :execrows
DELETE FROM alerts WHERE connector_id = $1 AND resolved_at IS NOT NULL AND resolved_at < $2;

-- name: ListAlertsForLink :many
SELECT id, host_name, host_ip, host_id FROM alerts WHERE workspace_id = $1;

-- name: SetAlertHost :exec
UPDATE alerts SET host_id = $2 WHERE id = $1;

-- name: ListAlerts :many
SELECT a.*, c.name AS connector_name, c.type AS connector_type
FROM alerts a JOIN connectors c ON c.id = a.connector_id
WHERE a.workspace_id = @workspace_id
  AND (@state::text = '' OR (@state = 'open' AND a.resolved_at IS NULL) OR (@state = 'resolved' AND a.resolved_at IS NOT NULL))
  AND (sqlc.narg('host_id')::uuid IS NULL OR a.host_id = sqlc.narg('host_id'))
  AND (sqlc.narg('connector_id')::uuid IS NULL OR a.connector_id = sqlc.narg('connector_id'))
  AND (sqlc.narg('id')::uuid IS NULL OR a.id = sqlc.narg('id'))
ORDER BY (a.resolved_at IS NOT NULL),
         CASE a.severity WHEN 'critical' THEN 0 WHEN 'warning' THEN 1 WHEN 'unknown' THEN 2 ELSE 3 END,
         a.last_seen_at DESC, a.id
LIMIT @max_rows;

-- name: ListOpenAlertSummaries :many
SELECT host_id, count(*)::int AS open_count,
       max(CASE severity WHEN 'critical' THEN 4 WHEN 'warning' THEN 3 WHEN 'unknown' THEN 2 ELSE 1 END)::int AS worst_rank
FROM alerts WHERE workspace_id = $1 AND resolved_at IS NULL AND host_id IS NOT NULL
GROUP BY host_id;

-- name: CountOpenAlerts :one
SELECT count(*)::int FROM alerts WHERE connector_id = $1 AND resolved_at IS NULL;

-- name: GetConnectorByID :one
SELECT * FROM connectors WHERE id = $1;

-- name: SetConnectorSecret :one
UPDATE connectors SET secret_id = $3, updated_at = now() WHERE id = $1 AND workspace_id = $2 RETURNING *;
