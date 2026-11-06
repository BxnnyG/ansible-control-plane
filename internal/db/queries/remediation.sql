-- name: CreateRemediationRule :one
INSERT INTO remediation_rules (workspace_id, name, enabled, connector_type, service_regex, summary_regex,
    min_severity, template_id, target, group_name, check_first, created_by, updated_by)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $12)
RETURNING *;

-- name: GetRemediationRule :one
SELECT * FROM remediation_rules WHERE id = $1 AND workspace_id = $2;

-- name: ListRemediationRules :many
SELECT r.*, t.name AS template_name
FROM remediation_rules r JOIN templates t ON t.id = r.template_id
WHERE r.workspace_id = $1 ORDER BY r.name;

-- name: UpdateRemediationRule :one
UPDATE remediation_rules
SET name = $3, enabled = $4, connector_type = $5, service_regex = $6, summary_regex = $7, min_severity = $8,
    template_id = $9, target = $10, group_name = $11, check_first = $12, updated_by = $13, updated_at = now()
WHERE id = $1 AND workspace_id = $2
RETURNING *;

-- name: DeleteRemediationRule :execrows
DELETE FROM remediation_rules WHERE id = $1 AND workspace_id = $2;
