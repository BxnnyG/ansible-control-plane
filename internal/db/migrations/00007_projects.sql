-- +goose Up
-- +goose StatementBegin
-- Git projects (ADR-0013). known_hosts holds the pinned host key line(s) of
-- the Git server after explicit operator confirmation. The credential is a
-- soft dependency: deleting the secret clears the reference.
CREATE TABLE projects (
    id                    uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id          uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    name                  text NOT NULL,
    slug                  text NOT NULL CHECK (slug ~ '^[a-z0-9][a-z0-9-]{0,62}$'),
    url                   text NOT NULL,
    default_ref           text NOT NULL DEFAULT 'main',
    credential_secret_id  uuid REFERENCES secrets(id) ON DELETE SET NULL,
    https_username        text NOT NULL DEFAULT '',
    known_hosts           text NOT NULL DEFAULT '',
    sync_interval_seconds integer CHECK (sync_interval_seconds IS NULL OR sync_interval_seconds >= 60),
    last_sync_status      text NOT NULL DEFAULT 'never' CHECK (last_sync_status IN ('never', 'ok', 'error')),
    last_sync_error       text NOT NULL DEFAULT '',
    last_sync_sha         text NOT NULL DEFAULT '',
    last_sync_at          timestamptz,
    created_by            uuid REFERENCES users(id) ON DELETE SET NULL,
    created_at            timestamptz NOT NULL DEFAULT now(),
    updated_at            timestamptz NOT NULL DEFAULT now(),
    UNIQUE (workspace_id, slug),
    UNIQUE (workspace_id, name)
);

CREATE TABLE project_revisions (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id uuid NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    sha        text NOT NULL,
    synced_at  timestamptz NOT NULL DEFAULT now(),
    scan       jsonb NOT NULL DEFAULT '{}'::jsonb,
    warnings   jsonb NOT NULL DEFAULT '[]'::jsonb,
    UNIQUE (project_id, sha)
);
CREATE INDEX project_revisions_latest_idx ON project_revisions (project_id, synced_at DESC);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE project_revisions;
DROP TABLE projects;
-- +goose StatementEnd
