-- name: CreateVaultIdentity :one
INSERT INTO vault_identities (workspace_id, label, description, provider, secret_id, script_path, script_args, created_by)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING *;

-- name: GetVaultIdentity :one
SELECT * FROM vault_identities WHERE id = $1 AND workspace_id = $2;

-- name: GetVaultIdentityByLabel :one
SELECT * FROM vault_identities WHERE workspace_id = $1 AND label = $2;

-- name: ListVaultIdentities :many
SELECT * FROM vault_identities WHERE workspace_id = $1 ORDER BY label;

-- name: UpdateVaultIdentity :one
UPDATE vault_identities
SET label = $3, description = $4, provider = $5, secret_id = $6, script_path = $7, script_args = $8, updated_at = now()
WHERE id = $1 AND workspace_id = $2
RETURNING *;

-- name: DeleteVaultIdentity :execrows
DELETE FROM vault_identities WHERE id = $1 AND workspace_id = $2;
