-- +goose Up
-- +goose StatementBegin
-- The worker bundle (credentials) is served once per lease (ADR-0016).
ALTER TABLE runs ADD COLUMN bundle_served_at timestamptz;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE runs DROP COLUMN bundle_served_at;
-- +goose StatementEnd
