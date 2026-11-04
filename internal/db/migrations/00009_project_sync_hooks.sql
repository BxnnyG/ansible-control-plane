-- +goose Up
-- +goose StatementBegin
-- Scheduled sync and push webhooks (ADR-0013). next_sync_at is a claim lease
-- set by the scheduler so that several app instances never sync one project
-- twice. The webhook secret lives in the secret store (type token).
ALTER TABLE projects
    ADD COLUMN next_sync_at      timestamptz,
    ADD COLUMN last_sync_trigger text NOT NULL DEFAULT 'manual'
        CHECK (last_sync_trigger IN ('manual', 'schedule', 'webhook')),
    ADD COLUMN webhook_enabled   boolean NOT NULL DEFAULT false,
    ADD COLUMN webhook_provider  text NOT NULL DEFAULT 'generic'
        CHECK (webhook_provider IN ('gitea', 'github', 'gitlab', 'generic')),
    ADD COLUMN webhook_secret_id uuid REFERENCES secrets(id) ON DELETE SET NULL;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE projects
    DROP COLUMN webhook_secret_id,
    DROP COLUMN webhook_provider,
    DROP COLUMN webhook_enabled,
    DROP COLUMN last_sync_trigger,
    DROP COLUMN next_sync_at;
-- +goose StatementEnd
