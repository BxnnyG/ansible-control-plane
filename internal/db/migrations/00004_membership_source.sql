-- +goose Up
-- +goose StatementBegin
-- source: who owns the membership. 'idp' rows are managed by the IdP role sync
-- and may be created, changed and removed by it; 'local' rows are never touched.
ALTER TABLE memberships
    ADD COLUMN source text NOT NULL DEFAULT 'local' CHECK (source IN ('local', 'idp'));
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE memberships DROP COLUMN source;
-- +goose StatementEnd
