-- name: ListMigrationMappings :many
SELECT * FROM migration_mappings WHERE source = $1 AND workspace_id = $2 ORDER BY kind, source_id;

-- name: CreateMigrationMapping :one
INSERT INTO migration_mappings (source, kind, source_id, acp_id, workspace_id)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (source, kind, source_id) DO UPDATE SET source = EXCLUDED.source
RETURNING *;

-- name: ListWorkspaceInventoryHostIdentifiers :many
SELECT i.slug AS inventory_slug, i.mode AS inventory_mode, h.name AS host_name,
       COALESCE(h.vars ->> 'ansible_host', '')::text AS ansible_host
FROM inventories i
JOIN inventory_hosts h ON h.inventory_id = i.id
WHERE i.workspace_id = $1
ORDER BY i.slug, h.name;
