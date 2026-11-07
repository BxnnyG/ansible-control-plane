-- name: CreateNotificationChannel :one
INSERT INTO notification_channels (workspace_id, name, type, config, secret_id, tls_ca_pem, tls_skip_verify, enabled, created_by, updated_by)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $9)
RETURNING *;

-- name: GetNotificationChannel :one
SELECT * FROM notification_channels WHERE id = $1 AND workspace_id = $2;

-- name: GetNotificationChannelByID :one
SELECT * FROM notification_channels WHERE id = $1;

-- name: ListNotificationChannels :many
SELECT * FROM notification_channels WHERE workspace_id = $1 ORDER BY name;

-- name: UpdateNotificationChannel :one
UPDATE notification_channels
SET name = $3, config = $4, secret_id = $5, tls_ca_pem = $6, tls_skip_verify = $7, enabled = $8,
    updated_by = $9, updated_at = now()
WHERE id = $1 AND workspace_id = $2
RETURNING *;

-- name: DeleteNotificationChannel :execrows
DELETE FROM notification_channels WHERE id = $1 AND workspace_id = $2;

-- name: CreateNotificationRule :one
INSERT INTO notification_rules (workspace_id, channel_id, events, template_id, enabled, created_by)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING *;

-- name: GetNotificationRule :one
SELECT * FROM notification_rules WHERE id = $1 AND workspace_id = $2;

-- name: ListNotificationRules :many
SELECT * FROM notification_rules WHERE workspace_id = $1 ORDER BY created_at, id;

-- name: UpdateNotificationRule :one
UPDATE notification_rules
SET channel_id = $3, events = $4, template_id = $5, enabled = $6, updated_at = now()
WHERE id = $1 AND workspace_id = $2
RETURNING *;

-- name: DeleteNotificationRule :execrows
DELETE FROM notification_rules WHERE id = $1 AND workspace_id = $2;

-- name: ListMatchingNotificationRules :many
SELECT r.channel_id, r.events
FROM notification_rules r JOIN notification_channels c ON c.id = r.channel_id
WHERE r.workspace_id = $1 AND r.enabled AND c.enabled
  AND (r.template_id IS NULL OR r.template_id = sqlc.narg(template_id)::uuid);

-- name: ClaimNotificationEvents :many
SELECT * FROM notification_events
WHERE processed_at IS NULL
ORDER BY id
LIMIT $1
FOR UPDATE SKIP LOCKED;

-- name: MarkNotificationEventProcessed :exec
UPDATE notification_events SET processed_at = now() WHERE id = $1;

-- name: GetRunForNotification :one
SELECT * FROM runs WHERE id = $1;

-- name: GetPreviousTerminalRunStatus :one
SELECT p.status FROM runs p, runs cur
WHERE cur.id = $1 AND p.id <> cur.id AND p.template_id = cur.template_id
  AND p.status IN ('succeeded', 'failed', 'lost')
  AND (COALESCE(p.finished_at, p.created_at), p.id) < (COALESCE(cur.finished_at, cur.created_at), cur.id)
ORDER BY COALESCE(p.finished_at, p.created_at) DESC, p.id DESC
LIMIT 1;

-- name: GetNotificationWorkspaceName :one
SELECT name FROM workspaces WHERE id = $1;

-- name: InsertNotificationDelivery :exec
INSERT INTO notification_deliveries (event_id, run_id, channel_id, kind)
VALUES ($1, $2, $3, $4)
ON CONFLICT (event_id, channel_id) DO NOTHING;

-- name: ClaimDueNotificationDeliveries :many
UPDATE notification_deliveries d
SET attempts = d.attempts + 1, next_attempt_at = $2
WHERE d.id IN (
    SELECT s.id FROM notification_deliveries s
    WHERE s.status = 'pending' AND s.next_attempt_at <= now()
    ORDER BY s.next_attempt_at
    LIMIT $1
    FOR UPDATE SKIP LOCKED
)
RETURNING *;

-- name: MarkNotificationDeliverySent :exec
UPDATE notification_deliveries SET status = 'sent', sent_at = now(), last_error = '' WHERE id = $1;

-- name: MarkNotificationDeliveryRetry :exec
UPDATE notification_deliveries SET next_attempt_at = $2, last_error = $3 WHERE id = $1;

-- name: MarkNotificationDeliveryFailed :exec
UPDATE notification_deliveries SET status = 'failed', last_error = $2 WHERE id = $1;

-- name: ListNotificationDeliveries :many
SELECT * FROM notification_deliveries d
WHERE d.channel_id = $1
ORDER BY d.created_at DESC, d.id DESC
LIMIT $2;

-- name: DeleteOldNotificationEvents :execrows
DELETE FROM notification_events e
WHERE e.processed_at IS NOT NULL AND e.processed_at < $1
  AND NOT EXISTS (SELECT 1 FROM notification_deliveries d WHERE d.event_id = e.id AND d.status = 'pending');
