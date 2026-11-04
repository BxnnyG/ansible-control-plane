-- +goose Up
-- +goose StatementBegin
-- Inventories (ADR-0014): one relational model for DB-only and Git-synced
-- inventories. Vars are JSON objects; vault-encrypted values are stored as the
-- opaque object {"__ansible_vault": "<envelope>"} and never decrypted.
-- Groups form a DAG through inventory_group_links (Ansible allows a group to
-- have several parents); the API exposes the links as parent ids.
-- version is bumped on every content change (optimistic locking, If-Match).
CREATE TABLE inventories (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id  uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    name          text NOT NULL,
    slug          text NOT NULL CHECK (slug ~ '^[a-z0-9][a-z0-9-]{0,62}$'),
    mode          text NOT NULL CHECK (mode IN ('db', 'git')),
    project_id    uuid REFERENCES projects(id) ON DELETE SET NULL,
    path          text NOT NULL DEFAULT '',
    write_branch  text NOT NULL DEFAULT '',
    source_format text NOT NULL DEFAULT 'yaml' CHECK (source_format IN ('yaml', 'ini')),
    base_sha      text NOT NULL DEFAULT '',
    version       integer NOT NULL DEFAULT 1,
    last_import_at timestamptz,
    created_by    uuid REFERENCES users(id) ON DELETE SET NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    UNIQUE (workspace_id, slug),
    UNIQUE (workspace_id, name)
);

CREATE TABLE inventory_groups (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    inventory_id uuid NOT NULL REFERENCES inventories(id) ON DELETE CASCADE,
    name         text NOT NULL CHECK (name <> ''),
    vars         jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(vars) = 'object'),
    UNIQUE (inventory_id, name)
);

CREATE TABLE inventory_group_links (
    parent_id uuid NOT NULL REFERENCES inventory_groups(id) ON DELETE CASCADE,
    child_id  uuid NOT NULL REFERENCES inventory_groups(id) ON DELETE CASCADE,
    PRIMARY KEY (parent_id, child_id),
    CHECK (parent_id <> child_id)
);
CREATE INDEX inventory_group_links_child_idx ON inventory_group_links (child_id);

CREATE TABLE inventory_hosts (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    inventory_id uuid NOT NULL REFERENCES inventories(id) ON DELETE CASCADE,
    name         text NOT NULL CHECK (name <> ''),
    vars         jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(vars) = 'object'),
    UNIQUE (inventory_id, name)
);

CREATE TABLE inventory_group_hosts (
    group_id uuid NOT NULL REFERENCES inventory_groups(id) ON DELETE CASCADE,
    host_id  uuid NOT NULL REFERENCES inventory_hosts(id) ON DELETE CASCADE,
    PRIMARY KEY (group_id, host_id)
);
CREATE INDEX inventory_group_hosts_host_idx ON inventory_group_hosts (host_id);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE inventory_group_hosts;
DROP TABLE inventory_hosts;
DROP TABLE inventory_group_links;
DROP TABLE inventory_groups;
DROP TABLE inventories;
-- +goose StatementEnd
