-- name: CreateSession :one
INSERT INTO sessions (token_hash, csrf_hash, user_id, id_token_hint, idle_expires_at, expires_at)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING *;

-- name: GetActiveSession :one
SELECT * FROM sessions
WHERE token_hash = $1 AND idle_expires_at > now() AND expires_at > now();

-- name: TouchSession :exec
UPDATE sessions
SET last_seen_at = now(), idle_expires_at = LEAST($2::timestamptz, expires_at)
WHERE id = $1;

-- name: DeleteSession :exec
DELETE FROM sessions WHERE id = $1;

-- name: DeleteUserSessions :exec
DELETE FROM sessions WHERE user_id = $1;

-- name: DeleteExpiredSessions :execrows
DELETE FROM sessions WHERE idle_expires_at <= now() OR expires_at <= now();
