-- name: CreateConnector :one
INSERT INTO connectors (workspace_id, type, name, base_url, config, secret_id, interval_seconds, enabled, created_by)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
RETURNING *;

-- name: GetConnector :one
SELECT * FROM connectors WHERE id = $1 AND workspace_id = $2;

-- name: GetConnectorByName :one
SELECT * FROM connectors WHERE workspace_id = $1 AND name = $2;

-- name: ListConnectors :many
SELECT * FROM connectors WHERE workspace_id = $1 ORDER BY name;

-- name: UpdateConnector :one
UPDATE connectors
SET name = $3, base_url = $4, config = $5, secret_id = $6, interval_seconds = $7, enabled = $8, updated_at = now()
WHERE id = $1 AND workspace_id = $2
RETURNING *;

-- name: DeleteConnector :execrows
DELETE FROM connectors WHERE id = $1 AND workspace_id = $2;

-- name: RequestConnectorSync :execrows
UPDATE connectors SET next_sync_at = now() WHERE id = $1 AND workspace_id = $2;

-- name: ClaimDueConnectors :many
UPDATE connectors c
SET next_sync_at = now() + make_interval(secs => $1::int)
WHERE c.id IN (
    SELECT d.id FROM connectors d
    WHERE d.enabled AND d.next_sync_at <= now()
    ORDER BY d.next_sync_at
    LIMIT $2::int
    FOR UPDATE SKIP LOCKED
)
RETURNING c.*;

-- name: FinishConnectorSyncOK :exec
UPDATE connectors
SET backoff_count = 0, last_sync_at = $2, last_success_at = $2, last_status = 'ok',
    last_error = '', last_duration_ms = $3, last_counts = $4,
    next_sync_at = $2::timestamptz + make_interval(secs => interval_seconds)
WHERE id = $1;

-- name: FinishConnectorSyncError :exec
UPDATE connectors
SET backoff_count = backoff_count + 1, last_sync_at = $2, last_status = 'error',
    last_error = $3, last_duration_ms = $4,
    next_sync_at = $2::timestamptz + make_interval(secs => $5::int)
WHERE id = $1;

-- name: InsertConnectorSync :one
INSERT INTO connector_syncs (connector_id, workspace_id, trigger, status, started_at, finished_at, duration_ms, counts, error)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
RETURNING *;

-- name: ListConnectorSyncs :many
SELECT * FROM connector_syncs WHERE connector_id = $1 AND workspace_id = $2 ORDER BY started_at DESC LIMIT $3;

-- name: PruneConnectorSyncs :execrows
DELETE FROM connector_syncs cs WHERE cs.connector_id = $1 AND cs.id NOT IN (
    SELECT s.id FROM connector_syncs s WHERE s.connector_id = $1 ORDER BY s.started_at DESC LIMIT $2
);

-- name: UpsertNetwork :exec
INSERT INTO networks (workspace_id, connector_id, external_key, cidr, vlan, interface, description, source, first_seen, last_seen)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $9)
ON CONFLICT (connector_id, external_key) DO UPDATE
SET cidr = EXCLUDED.cidr, vlan = EXCLUDED.vlan, interface = EXCLUDED.interface,
    description = EXCLUDED.description, source = EXCLUDED.source, last_seen = EXCLUDED.last_seen;

-- name: PruneNetworks :execrows
DELETE FROM networks WHERE connector_id = $1 AND last_seen < $2;

-- name: ListNetworks :many
SELECT * FROM networks WHERE workspace_id = $1 ORDER BY cidr, external_key;

-- name: UpsertObservation :exec
INSERT INTO host_observations (workspace_id, connector_id, external_key, kind, fields, first_seen, last_seen)
VALUES ($1, $2, $3, $4, $5, $6, $6)
ON CONFLICT (connector_id, external_key) DO UPDATE
SET kind = EXCLUDED.kind, fields = EXCLUDED.fields, last_seen = EXCLUDED.last_seen;

-- name: PruneObservations :execrows
DELETE FROM host_observations WHERE connector_id = $1 AND last_seen < $2;

-- name: ListObservations :many
SELECT * FROM host_observations WHERE workspace_id = $1 ORDER BY connector_id, external_key;

-- name: ListConnectorObservations :many
SELECT * FROM host_observations WHERE connector_id = $1 ORDER BY external_key;
