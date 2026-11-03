-- +goose Up
-- +goose StatementBegin
-- setup_completed_at: set when the first-run setup wizard of the workspace is finished.
ALTER TABLE workspaces ADD COLUMN setup_completed_at timestamptz;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE workspaces DROP COLUMN setup_completed_at;
-- +goose StatementEnd
