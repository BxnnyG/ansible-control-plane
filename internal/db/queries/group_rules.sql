-- name: CreateGroupRule :one
INSERT INTO group_rules (workspace_id, name, kind, params, inventory_id, group_name, mode, enabled, created_by)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
RETURNING *;

-- name: GetGroupRule :one
SELECT * FROM group_rules WHERE id = $1 AND workspace_id = $2;

-- name: LockGroupRule :one
SELECT * FROM group_rules WHERE id = $1 AND workspace_id = $2 FOR UPDATE;

-- name: ListGroupRules :many
SELECT * FROM group_rules WHERE workspace_id = $1 ORDER BY name;

-- name: UpdateGroupRule :one
UPDATE group_rules
SET name = $3, kind = $4, params = $5, inventory_id = $6, group_name = $7, mode = $8, enabled = $9, updated_at = now()
WHERE id = $1 AND workspace_id = $2
RETURNING *;

-- name: DeleteGroupRule :execrows
DELETE FROM group_rules WHERE id = $1 AND workspace_id = $2;

-- name: MarkGroupRuleApplied :exec
UPDATE group_rules SET last_applied_at = now(), last_applied_by = $2 WHERE id = $1;

-- name: ListGroupRuleMembers :many
SELECT host_name FROM group_rule_members WHERE rule_id = $1 ORDER BY host_name;

-- name: AddGroupRuleMember :exec
INSERT INTO group_rule_members (rule_id, host_name) VALUES ($1, $2)
ON CONFLICT DO NOTHING;

-- name: DeleteGroupRuleMembers :exec
DELETE FROM group_rule_members WHERE rule_id = $1 AND host_name = ANY($2::text[]);

-- name: ClearGroupRuleMembers :exec
DELETE FROM group_rule_members WHERE rule_id = $1;
