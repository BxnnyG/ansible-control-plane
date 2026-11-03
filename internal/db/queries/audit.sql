-- name: InsertAudit :one
INSERT INTO audit_log (actor_id, actor_label, action, target_type, target_id, workspace_id, details)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING id, ts;

-- name: ListAudit :many
SELECT * FROM audit_log
WHERE (sqlc.narg('workspace_id')::uuid IS NULL OR workspace_id = sqlc.narg('workspace_id'))
  AND (sqlc.narg('before_id')::bigint IS NULL OR id < sqlc.narg('before_id'))
ORDER BY id DESC
LIMIT $1;
