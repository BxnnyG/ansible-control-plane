-- name: CreateSecret :one
INSERT INTO secrets (id, workspace_id, name, type, description, env_version, env_key_id,
    env_wrap_nonce, env_wrapped_dek, env_nonce, env_ciphertext, created_by)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
RETURNING *;

-- name: GetSecret :one
SELECT * FROM secrets WHERE id = $1 AND workspace_id = $2;

-- name: ListSecretsMeta :many
SELECT id, workspace_id, name, type, description, created_by, created_at, updated_at
FROM secrets WHERE workspace_id = $1 ORDER BY name;

-- name: ReplaceSecretEnvelope :exec
UPDATE secrets
SET env_version = $3, env_key_id = $4, env_wrap_nonce = $5, env_wrapped_dek = $6,
    env_nonce = $7, env_ciphertext = $8, updated_at = now()
WHERE id = $1 AND workspace_id = $2;

-- name: DeleteSecret :exec
DELETE FROM secrets WHERE id = $1 AND workspace_id = $2;

-- name: ListAllSecrets :many
SELECT * FROM secrets ORDER BY id;
