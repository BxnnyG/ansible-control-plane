-- name: CreateWorkspace :one
INSERT INTO workspaces (slug, name, description) VALUES ($1, $2, $3) RETURNING *;

-- name: GetWorkspace :one
SELECT * FROM workspaces WHERE id = $1;

-- name: ListWorkspaces :many
SELECT * FROM workspaces ORDER BY name, id;

-- name: ListWorkspacesForUser :many
SELECT w.* FROM workspaces w
JOIN memberships m ON m.workspace_id = w.id
WHERE m.user_id = $1
ORDER BY w.name, w.id;

-- name: UpdateWorkspace :one
UPDATE workspaces SET name = $2, description = $3, updated_at = now()
WHERE id = $1 RETURNING *;

-- name: DeleteWorkspace :exec
DELETE FROM workspaces WHERE id = $1;

-- name: CreateMembership :one
INSERT INTO memberships (workspace_id, user_id, role) VALUES ($1, $2, $3) RETURNING *;

-- name: GetMembership :one
SELECT * FROM memberships WHERE workspace_id = $1 AND user_id = $2;

-- name: ListMembershipsForUser :many
SELECT * FROM memberships WHERE user_id = $1 ORDER BY created_at, id;

-- name: ListMembershipsForWorkspace :many
SELECT * FROM memberships WHERE workspace_id = $1 ORDER BY created_at, id;

-- name: UpdateMembershipRole :exec
UPDATE memberships SET role = $2 WHERE id = $1;

-- name: DeleteMembership :exec
DELETE FROM memberships WHERE id = $1;

-- name: AddMembershipScope :one
INSERT INTO membership_scopes (membership_id, kind, ref_id) VALUES ($1, $2, $3)
ON CONFLICT (membership_id, kind, ref_id) DO UPDATE SET kind = EXCLUDED.kind
RETURNING *;

-- name: ListMembershipScopes :many
SELECT * FROM membership_scopes WHERE membership_id = $1 ORDER BY kind, ref_id;

-- name: ClearMembershipScopes :exec
DELETE FROM membership_scopes WHERE membership_id = $1;

-- name: GetWorkspaceBySlug :one
SELECT * FROM workspaces WHERE slug = $1;

-- name: CreateIdPMembership :one
INSERT INTO memberships (workspace_id, user_id, role, source) VALUES ($1, $2, $3, 'idp')
ON CONFLICT (workspace_id, user_id) DO NOTHING
RETURNING *;

-- name: ListMembershipSummariesForUser :many
SELECT w.id AS workspace_id, w.slug, w.name, m.role,
       EXISTS (SELECT 1 FROM membership_scopes s WHERE s.membership_id = m.id) AS scoped
FROM memberships m
JOIN workspaces w ON w.id = m.workspace_id
WHERE m.user_id = $1
ORDER BY w.name, w.id;

-- name: UpdateIdPMembershipRole :execrows
UPDATE memberships SET role = $2 WHERE id = $1 AND source = 'idp';

-- name: DeleteIdPMembership :execrows
DELETE FROM memberships WHERE id = $1 AND source = 'idp';

-- name: RenameWorkspace :one
UPDATE workspaces SET name = $2, updated_at = now()
WHERE id = $1 RETURNING *;

-- name: CompleteWorkspaceSetup :one
UPDATE workspaces SET setup_completed_at = COALESCE(setup_completed_at, now()), updated_at = now()
WHERE id = $1 RETURNING *;
