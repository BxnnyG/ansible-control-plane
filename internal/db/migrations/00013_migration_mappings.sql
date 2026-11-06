-- +goose Up
-- +goose StatementBegin
-- Maps objects of a migration source (e.g. Semaphore) to ACP objects so that
-- re-running an import never duplicates anything (ADR-0017).
CREATE TABLE migration_mappings (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    source       text NOT NULL CHECK (source <> ''),
    kind         text NOT NULL CHECK (kind <> ''),
    source_id    text NOT NULL CHECK (source_id <> ''),
    acp_id       uuid NOT NULL,
    workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    created_at   timestamptz NOT NULL DEFAULT now(),
    UNIQUE (source, kind, source_id)
);
CREATE INDEX migration_mappings_workspace_idx ON migration_mappings (workspace_id);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE migration_mappings;
-- +goose StatementEnd
