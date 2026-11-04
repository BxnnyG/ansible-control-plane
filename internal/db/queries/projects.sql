-- name: CreateProject :one
INSERT INTO projects (workspace_id, name, slug, url, default_ref, credential_secret_id,
    https_username, sync_interval_seconds, created_by)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
RETURNING *;

-- name: GetProject :one
SELECT * FROM projects WHERE id = $1 AND workspace_id = $2;

-- name: ListProjects :many
SELECT * FROM projects WHERE workspace_id = $1 ORDER BY name;

-- name: UpdateProject :one
UPDATE projects
SET name = $3, slug = $4, url = $5, default_ref = $6, credential_secret_id = $7,
    https_username = $8, sync_interval_seconds = $9, known_hosts = $10, updated_at = now()
WHERE id = $1 AND workspace_id = $2
RETURNING *;

-- name: SetProjectKnownHosts :one
UPDATE projects SET known_hosts = $3, updated_at = now()
WHERE id = $1 AND workspace_id = $2
RETURNING *;

-- name: SetProjectSyncResult :exec
UPDATE projects
SET last_sync_status = $2, last_sync_error = $3, last_sync_sha = $4, last_sync_trigger = $5,
    last_sync_at = now()
WHERE id = $1;

-- name: DeleteProject :execrows
DELETE FROM projects WHERE id = $1 AND workspace_id = $2;

-- name: UpsertProjectRevision :one
INSERT INTO project_revisions (project_id, sha, scan, warnings)
VALUES ($1, $2, $3, $4)
ON CONFLICT (project_id, sha) DO UPDATE
SET synced_at = now(), scan = EXCLUDED.scan, warnings = EXCLUDED.warnings
RETURNING *;

-- name: ListProjectRevisions :many
SELECT * FROM project_revisions WHERE project_id = $1 ORDER BY synced_at DESC LIMIT $2;

-- name: GetLatestProjectRevision :one
SELECT * FROM project_revisions WHERE project_id = $1 ORDER BY synced_at DESC LIMIT 1;

-- name: GetProjectByID :one
SELECT * FROM projects WHERE id = $1;

-- name: SetProjectWebhook :one
UPDATE projects
SET webhook_enabled = $3, webhook_provider = $4, webhook_secret_id = $5, updated_at = now()
WHERE id = $1 AND workspace_id = $2
RETURNING *;

-- ClaimDueProjects atomically leases projects whose interval elapsed. The lease
-- (next_sync_at) keeps other app instances and later ticks away until the sync
-- has finished or the interval has passed again.
-- name: ClaimDueProjects :many
UPDATE projects p
SET next_sync_at = now() + make_interval(secs => p.sync_interval_seconds)
WHERE p.id IN (
    SELECT c.id FROM projects c
    WHERE c.sync_interval_seconds IS NOT NULL
      AND (c.last_sync_at IS NULL OR c.last_sync_at + make_interval(secs => c.sync_interval_seconds) <= now())
      AND (c.next_sync_at IS NULL OR c.next_sync_at <= now())
    ORDER BY c.last_sync_at NULLS FIRST
    LIMIT $1
    FOR UPDATE SKIP LOCKED
)
RETURNING p.*;
