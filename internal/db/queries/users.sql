-- name: UpsertOIDCUser :one
INSERT INTO users (oidc_issuer, oidc_subject, email, display_name, last_login_at)
VALUES ($1, $2, $3, $4, now())
ON CONFLICT (oidc_issuer, oidc_subject) DO UPDATE
SET email = EXCLUDED.email, display_name = EXCLUDED.display_name,
    last_login_at = now(), updated_at = now()
RETURNING *;

-- name: CreateBreakGlassUser :one
INSERT INTO users (id, is_break_glass, username, password_hash, totp_envelope, display_name)
VALUES ($1, true, $2, $3, $4, $2)
RETURNING *;

-- name: GetUser :one
SELECT * FROM users WHERE id = $1;

-- name: GetBreakGlassUser :one
SELECT * FROM users WHERE is_break_glass AND username = $1;

-- name: ListUsers :many
SELECT * FROM users ORDER BY created_at, id;

-- name: SetUserDisabled :exec
UPDATE users SET disabled = $2, updated_at = now() WHERE id = $1;

-- name: TouchUserLogin :exec
UPDATE users SET last_login_at = now() WHERE id = $1;

-- name: SetBreakGlassTOTP :execrows
UPDATE users SET totp_envelope = $2, totp_last_step = 0, failed_logins = 0, locked_until = NULL, updated_at = now()
WHERE id = $1 AND is_break_glass;

-- name: RecordBreakGlassFailure :one
-- Counts a failed attempt; once the threshold is reached the account is locked
-- for the given number of seconds and the counter starts over.
UPDATE users SET
    failed_logins = CASE WHEN failed_logins + 1 >= @max_failures::int THEN 0 ELSE failed_logins + 1 END,
    locked_until = CASE WHEN failed_logins + 1 >= @max_failures::int
                        THEN now() + make_interval(secs => @lock_seconds::float8) ELSE locked_until END
WHERE id = @id AND is_break_glass
RETURNING failed_logins, locked_until;

-- name: AcceptBreakGlassLogin :one
-- Atomic replay protection: succeeds only for a TOTP step newer than the last accepted one.
UPDATE users SET totp_last_step = $2, failed_logins = 0, locked_until = NULL,
    last_login_at = now(), updated_at = now()
WHERE id = $1 AND is_break_glass AND totp_last_step < $2
RETURNING id;

-- name: SetBreakGlassPassword :execrows
-- Replaces the password hash and clears lockout state.
UPDATE users SET password_hash = $2, failed_logins = 0, locked_until = NULL, updated_at = now()
WHERE id = $1 AND is_break_glass;

-- name: ReplaceUserTOTPEnvelope :exec
-- Re-wraps an existing envelope (key rotation); replay and lockout state stay untouched.
UPDATE users SET totp_envelope = $2, updated_at = now() WHERE id = $1 AND totp_envelope IS NOT NULL;
