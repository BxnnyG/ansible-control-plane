-- +goose Up
-- +goose StatementBegin
-- Vault identities (ADR-0015): a vault-id label per workspace bound to a
-- password source. Provider "secret" points to a secret of type
-- vault_password; provider "script" only stores the script configuration, it
-- is executed by the worker at run time. The label "default" stands for the
-- unlabeled default vault. Deleting the referenced secret leaves the identity
-- without a source (secret_id NULL) instead of silently removing it.
CREATE TABLE vault_identities (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    label        text NOT NULL CHECK (label ~ '^[A-Za-z0-9][A-Za-z0-9_.-]{0,62}$'),
    description  text NOT NULL DEFAULT '',
    provider     text NOT NULL CHECK (provider IN ('secret', 'script')),
    secret_id    uuid REFERENCES secrets(id) ON DELETE SET NULL,
    script_path  text NOT NULL DEFAULT '',
    script_args  jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(script_args) = 'array'),
    created_by   uuid REFERENCES users(id) ON DELETE SET NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    UNIQUE (workspace_id, label),
    CHECK (provider = 'script' OR script_path = ''),
    CHECK (provider = 'secret' OR secret_id IS NULL)
);
CREATE INDEX vault_identities_secret_idx ON vault_identities (secret_id);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE vault_identities;
-- +goose StatementEnd
