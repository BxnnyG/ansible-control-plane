-- name: CreateInventory :one
INSERT INTO inventories (workspace_id, name, slug, mode, project_id, path, write_branch, source_format, created_by)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
RETURNING *;

-- name: GetInventory :one
SELECT * FROM inventories WHERE id = $1 AND workspace_id = $2;

-- name: LockInventory :one
SELECT * FROM inventories WHERE id = $1 AND workspace_id = $2 FOR UPDATE;

-- name: ListInventories :many
SELECT * FROM inventories WHERE workspace_id = $1 ORDER BY name;

-- name: UpdateInventory :one
UPDATE inventories
SET name = $3, slug = $4, project_id = $5, path = $6, write_branch = $7,
    version = version + 1, updated_at = now()
WHERE id = $1 AND workspace_id = $2
RETURNING *;

-- name: BumpInventoryVersion :one
UPDATE inventories SET version = version + 1, updated_at = now()
WHERE id = $1
RETURNING version;

-- name: SetInventoryImported :one
UPDATE inventories
SET base_sha = $2, source_format = $3, last_import_at = now(), version = version + 1, updated_at = now()
WHERE id = $1
RETURNING *;

-- name: SetInventoryBaseSHA :one
UPDATE inventories SET base_sha = $2, updated_at = now() WHERE id = $1
RETURNING *;

-- name: DeleteInventory :execrows
DELETE FROM inventories WHERE id = $1 AND workspace_id = $2;

-- name: CountInventoryContent :one
SELECT
    (SELECT count(*) FROM inventory_hosts h WHERE h.inventory_id = $1)::bigint AS hosts,
    (SELECT count(*) FROM inventory_groups g WHERE g.inventory_id = $1)::bigint AS groups;

-- name: ClearInventoryHosts :exec
DELETE FROM inventory_hosts WHERE inventory_id = $1;

-- name: ClearInventoryGroups :exec
DELETE FROM inventory_groups WHERE inventory_id = $1;

-- name: CreateInventoryGroup :one
INSERT INTO inventory_groups (inventory_id, name, vars) VALUES ($1, $2, $3)
RETURNING *;

-- name: GetInventoryGroup :one
SELECT * FROM inventory_groups WHERE id = $1 AND inventory_id = $2;

-- name: ListInventoryGroups :many
SELECT * FROM inventory_groups WHERE inventory_id = $1 ORDER BY name;

-- name: UpdateInventoryGroup :one
UPDATE inventory_groups SET name = $3, vars = $4
WHERE id = $1 AND inventory_id = $2
RETURNING *;

-- name: DeleteInventoryGroup :execrows
DELETE FROM inventory_groups WHERE id = $1 AND inventory_id = $2;

-- name: ListInventoryGroupLinks :many
SELECT l.parent_id, l.child_id
FROM inventory_group_links l
JOIN inventory_groups g ON g.id = l.child_id
WHERE g.inventory_id = $1;

-- name: AddInventoryGroupLink :exec
INSERT INTO inventory_group_links (parent_id, child_id) VALUES ($1, $2)
ON CONFLICT DO NOTHING;

-- name: DeleteInventoryGroupParents :exec
DELETE FROM inventory_group_links WHERE child_id = $1;

-- name: CreateInventoryHost :one
INSERT INTO inventory_hosts (inventory_id, name, vars) VALUES ($1, $2, $3)
RETURNING *;

-- name: GetInventoryHost :one
SELECT * FROM inventory_hosts WHERE id = $1 AND inventory_id = $2;

-- name: ListInventoryHosts :many
SELECT * FROM inventory_hosts WHERE inventory_id = $1 ORDER BY name;

-- name: UpdateInventoryHost :one
UPDATE inventory_hosts SET name = $3, vars = $4
WHERE id = $1 AND inventory_id = $2
RETURNING *;

-- name: DeleteInventoryHost :execrows
DELETE FROM inventory_hosts WHERE id = $1 AND inventory_id = $2;

-- name: ListInventoryMemberships :many
SELECT gh.group_id, gh.host_id
FROM inventory_group_hosts gh
JOIN inventory_groups g ON g.id = gh.group_id
WHERE g.inventory_id = $1;

-- name: AddInventoryMembership :exec
INSERT INTO inventory_group_hosts (group_id, host_id) VALUES ($1, $2)
ON CONFLICT DO NOTHING;

-- name: DeleteInventoryMembership :execrows
DELETE FROM inventory_group_hosts WHERE group_id = $1 AND host_id = $2;

-- name: ListWorkspaceInventoryHosts :many
-- Every inventory host of the workspace with its ansible_host (fleet linking).
SELECT h.id, h.inventory_id, h.name, COALESCE(h.vars->>'ansible_host', '')::text AS ansible_host
FROM inventory_hosts h JOIN inventories i ON i.id = h.inventory_id
WHERE i.workspace_id = $1
ORDER BY h.inventory_id, h.name;
