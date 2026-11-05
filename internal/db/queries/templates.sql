-- name: CreateTemplate :one
INSERT INTO templates (workspace_id, project_id, inventory_id, name, slug, description, playbook,
    extra_vars, env_vars, ssh_secret_id, become_secret_id, default_limit, default_tags, default_skip_tags,
    verbosity, check_mode, diff_mode, survey, max_concurrent,
    allow_limit_override, allow_tags_override, allow_vars_override, allow_check_override,
    allow_diff_override, allow_verbosity_override, created_by)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19,
    $20, $21, $22, $23, $24, $25, $26)
RETURNING *;

-- name: GetTemplate :one
SELECT * FROM templates WHERE id = $1 AND workspace_id = $2;

-- name: ListTemplates :many
SELECT * FROM templates WHERE workspace_id = $1 ORDER BY name;

-- name: UpdateTemplate :one
UPDATE templates
SET project_id = $3, inventory_id = $4, name = $5, slug = $6, description = $7, playbook = $8,
    extra_vars = $9, env_vars = $10, ssh_secret_id = $11, become_secret_id = $12,
    default_limit = $13, default_tags = $14, default_skip_tags = $15, verbosity = $16,
    check_mode = $17, diff_mode = $18, survey = $19, max_concurrent = $20,
    allow_limit_override = $21, allow_tags_override = $22, allow_vars_override = $23,
    allow_check_override = $24, allow_diff_override = $25, allow_verbosity_override = $26,
    updated_at = now()
WHERE id = $1 AND workspace_id = $2
RETURNING *;

-- name: SetTemplateWebhook :one
UPDATE templates SET webhook_enabled = $3, webhook_secret_id = $4, updated_at = now()
WHERE id = $1 AND workspace_id = $2
RETURNING *;

-- name: DeleteTemplate :execrows
DELETE FROM templates WHERE id = $1 AND workspace_id = $2;

-- name: ClearTemplateVaultIdentities :exec
DELETE FROM template_vault_identities WHERE template_id = $1;

-- name: AddTemplateVaultIdentity :exec
INSERT INTO template_vault_identities (template_id, vault_identity_id, position)
VALUES ($1, $2, $3)
ON CONFLICT (template_id, vault_identity_id) DO UPDATE SET position = EXCLUDED.position;

-- name: ListTemplateVaultIdentities :many
SELECT v.* FROM template_vault_identities tv
JOIN vault_identities v ON v.id = tv.vault_identity_id
WHERE tv.template_id = $1
ORDER BY tv.position, v.label;

-- name: CreateSchedule :one
INSERT INTO schedules (workspace_id, template_id, name, cron_expr, timezone, enabled, missed_policy,
    overrides, next_run_at, created_by)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
RETURNING *;

-- name: GetSchedule :one
SELECT * FROM schedules WHERE id = $1 AND workspace_id = $2;

-- name: ListSchedules :many
SELECT * FROM schedules WHERE workspace_id = $1 ORDER BY created_at, id;

-- name: ListTemplateSchedules :many
SELECT * FROM schedules WHERE template_id = $1 AND workspace_id = $2 ORDER BY created_at, id;

-- name: UpdateSchedule :one
UPDATE schedules
SET name = $3, cron_expr = $4, timezone = $5, enabled = $6, missed_policy = $7,
    overrides = $8, next_run_at = $9, updated_at = now()
WHERE id = $1 AND workspace_id = $2
RETURNING *;

-- name: DeleteSchedule :execrows
DELETE FROM schedules WHERE id = $1 AND workspace_id = $2;

-- name: ClaimDueSchedules :many
-- Must run inside a transaction together with AdvanceSchedule; the row locks
-- keep other scheduler instances from claiming the same schedules.
SELECT * FROM schedules
WHERE enabled AND next_run_at IS NOT NULL AND next_run_at <= now()
ORDER BY next_run_at, id
LIMIT $1
FOR UPDATE SKIP LOCKED;

-- name: AdvanceSchedule :exec
UPDATE schedules
SET next_run_at = $2, last_run_at = COALESCE(sqlc.narg(last_run_at), last_run_at), updated_at = now()
WHERE id = $1;
