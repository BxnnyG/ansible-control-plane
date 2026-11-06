-- name: LockFleetMerge :exec
SELECT pg_advisory_xact_lock(hashtextextended('fleet-merge:' || $1::text, 0));

-- name: ListHosts :many
SELECT * FROM hosts WHERE workspace_id = $1 ORDER BY first_seen, id;

-- name: ListHostKeys :many
SELECT * FROM host_keys WHERE workspace_id = $1 ORDER BY host_id, kind, value;

-- name: InsertHost :exec
INSERT INTO hosts (id, workspace_id, name, fields, first_seen, last_seen)
VALUES ($1, $2, $3, $4, $5, $6);

-- name: UpdateHost :exec
UPDATE hosts SET name = $3, fields = $4, first_seen = LEAST(first_seen, $5), last_seen = $6, updated_at = now()
WHERE id = $1 AND workspace_id = $2;

-- name: DeleteHost :exec
DELETE FROM hosts WHERE id = $1 AND workspace_id = $2;

-- name: DeleteHostKeys :exec
DELETE FROM host_keys WHERE host_id = $1;

-- name: InsertHostKeys :exec
INSERT INTO host_keys (host_id, workspace_id, kind, value)
-- $3 holds "kind:value" entries; the kind never contains a colon.
SELECT $1, $2, split_part(k, ':', 1), substr(k, position(':' in k) + 1) FROM unnest($3::text[]) AS k
ON CONFLICT DO NOTHING;

-- name: ListHostConflicts :many
SELECT * FROM host_conflicts WHERE workspace_id = $1 ORDER BY type, fingerprint;

-- name: InsertHostConflict :exec
INSERT INTO host_conflicts (workspace_id, type, fingerprint, host_ids, details, first_seen, last_seen)
VALUES ($1, $2, $3, $4, $5, $6, $6);

-- name: RefreshHostConflict :exec
UPDATE host_conflicts SET host_ids = $2, details = $3, last_seen = $4 WHERE id = $1;

-- name: ReopenHostConflict :exec
UPDATE host_conflicts
SET state = 'open', host_ids = $2, details = $3, first_seen = $4, last_seen = $4, resolved_at = NULL,
    acknowledged_by = NULL, acknowledged_at = NULL
WHERE id = $1;

-- name: ResolveHostConflict :exec
UPDATE host_conflicts SET state = 'resolved', resolved_at = $2 WHERE id = $1;

-- name: AcknowledgeHostConflict :execrows
UPDATE host_conflicts SET acknowledged_by = $3, acknowledged_at = $4
WHERE id = $1 AND workspace_id = $2 AND state = 'open';
