-- +goose Up
-- +goose StatementBegin
-- Brute-force and replay guards for the break-glass login (ADR-0006).
-- totp_last_step: highest accepted RFC 6238 time step (replay protection).
-- failed_logins/locked_until: per-user lockout, independent of the in-memory per-IP limiter.
ALTER TABLE users
    ADD COLUMN totp_last_step bigint NOT NULL DEFAULT 0,
    ADD COLUMN failed_logins  integer NOT NULL DEFAULT 0,
    ADD COLUMN locked_until   timestamptz;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE users
    DROP COLUMN locked_until,
    DROP COLUMN failed_logins,
    DROP COLUMN totp_last_step;
-- +goose StatementEnd
