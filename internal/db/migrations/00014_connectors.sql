-- +goose Up
-- +goose StatementBegin
-- Connector runtime and fleet schema (ADR-0018). Connectors, their networks
-- and observations belong to a workspace. hosts, host_keys, host_conflicts and
-- group_rules are filled by the merge and the group-rule steps.
ALTER TABLE secrets DROP CONSTRAINT secrets_type_check;
ALTER TABLE secrets ADD CONSTRAINT secrets_type_check
    CHECK (type IN ('ssh_key', 'password', 'token', 'vault_password', 'file', 'api_key_pair', 'api_token'));

CREATE TABLE connectors (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id     uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    type             text NOT NULL CHECK (type ~ '^[a-z][a-z0-9_]{0,31}$'),
    name             text NOT NULL CHECK (name <> ''),
    base_url         text NOT NULL DEFAULT '',
    config           jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(config) = 'object'),
    secret_id        uuid REFERENCES secrets(id) ON DELETE SET NULL,
    interval_seconds integer NOT NULL DEFAULT 300 CHECK (interval_seconds >= 30),
    enabled          boolean NOT NULL DEFAULT true,
    next_sync_at     timestamptz NOT NULL DEFAULT now(),
    backoff_count    integer NOT NULL DEFAULT 0 CHECK (backoff_count >= 0),
    last_sync_at     timestamptz,
    last_success_at  timestamptz,
    last_status      text NOT NULL DEFAULT '' CHECK (last_status IN ('', 'ok', 'error')),
    last_error       text NOT NULL DEFAULT '',
    last_duration_ms integer NOT NULL DEFAULT 0,
    last_counts      jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_by       uuid REFERENCES users(id) ON DELETE SET NULL,
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    UNIQUE (workspace_id, name)
);
CREATE INDEX connectors_due_idx ON connectors (next_sync_at) WHERE enabled;
CREATE INDEX connectors_secret_idx ON connectors (secret_id);

CREATE TABLE connector_syncs (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    connector_id uuid NOT NULL REFERENCES connectors(id) ON DELETE CASCADE,
    workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    trigger      text NOT NULL DEFAULT 'schedule',
    status       text NOT NULL CHECK (status IN ('ok', 'error')),
    started_at   timestamptz NOT NULL,
    finished_at  timestamptz NOT NULL,
    duration_ms  integer NOT NULL DEFAULT 0,
    counts       jsonb NOT NULL DEFAULT '{}'::jsonb,
    error        text NOT NULL DEFAULT ''
);
CREATE INDEX connector_syncs_connector_idx ON connector_syncs (connector_id, started_at DESC);

CREATE TABLE networks (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    connector_id uuid NOT NULL REFERENCES connectors(id) ON DELETE CASCADE,
    external_key text NOT NULL CHECK (external_key <> ''),
    cidr         cidr NOT NULL,
    vlan         integer CHECK (vlan IS NULL OR vlan BETWEEN 0 AND 4094),
    interface    text NOT NULL DEFAULT '',
    description  text NOT NULL DEFAULT '',
    source       text NOT NULL DEFAULT '',
    first_seen   timestamptz NOT NULL DEFAULT now(),
    last_seen    timestamptz NOT NULL DEFAULT now(),
    UNIQUE (connector_id, external_key)
);
CREATE INDEX networks_workspace_idx ON networks (workspace_id);

CREATE TABLE host_observations (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    connector_id uuid NOT NULL REFERENCES connectors(id) ON DELETE CASCADE,
    external_key text NOT NULL CHECK (external_key <> ''),
    kind         text NOT NULL CHECK (kind IN ('lease', 'arp', 'ndp', 'static', 'vm', 'lxc', 'node')),
    fields       jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(fields) = 'object'),
    first_seen   timestamptz NOT NULL DEFAULT now(),
    last_seen    timestamptz NOT NULL DEFAULT now(),
    UNIQUE (connector_id, external_key)
);
CREATE INDEX host_observations_workspace_idx ON host_observations (workspace_id, last_seen);

CREATE TABLE hosts (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    name         text NOT NULL DEFAULT '',
    fields       jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(fields) = 'object'),
    first_seen   timestamptz NOT NULL DEFAULT now(),
    last_seen    timestamptz NOT NULL DEFAULT now(),
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX hosts_workspace_idx ON hosts (workspace_id);

CREATE TABLE host_keys (
    host_id      uuid NOT NULL REFERENCES hosts(id) ON DELETE CASCADE,
    workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    kind         text NOT NULL CHECK (kind IN ('mac', 'name', 'ip')),
    value        text NOT NULL CHECK (value <> ''),
    PRIMARY KEY (host_id, kind, value)
);
CREATE INDEX host_keys_lookup_idx ON host_keys (workspace_id, kind, value);

CREATE TABLE host_conflicts (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id    uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    type            text NOT NULL CHECK (type <> ''),
    fingerprint     text NOT NULL CHECK (fingerprint <> ''),
    host_ids        uuid[] NOT NULL DEFAULT '{}',
    details         jsonb NOT NULL DEFAULT '{}'::jsonb,
    state           text NOT NULL DEFAULT 'open' CHECK (state IN ('open', 'resolved')),
    acknowledged_by uuid REFERENCES users(id) ON DELETE SET NULL,
    acknowledged_at timestamptz,
    first_seen      timestamptz NOT NULL DEFAULT now(),
    last_seen       timestamptz NOT NULL DEFAULT now(),
    resolved_at     timestamptz,
    UNIQUE (workspace_id, fingerprint)
);

CREATE TABLE group_rules (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    name         text NOT NULL CHECK (name <> ''),
    kind         text NOT NULL CHECK (kind IN ('subnet', 'proxmox_tag')),
    params       jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(params) = 'object'),
    inventory_id uuid NOT NULL REFERENCES inventories(id) ON DELETE CASCADE,
    group_name   text NOT NULL CHECK (group_name <> ''),
    enabled      boolean NOT NULL DEFAULT true,
    created_by   uuid REFERENCES users(id) ON DELETE SET NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    UNIQUE (workspace_id, name)
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE group_rules;
DROP TABLE host_conflicts;
DROP TABLE host_keys;
DROP TABLE hosts;
DROP TABLE host_observations;
DROP TABLE networks;
DROP TABLE connector_syncs;
DROP TABLE connectors;
DELETE FROM secrets WHERE type IN ('api_key_pair', 'api_token');
ALTER TABLE secrets DROP CONSTRAINT secrets_type_check;
ALTER TABLE secrets ADD CONSTRAINT secrets_type_check
    CHECK (type IN ('ssh_key', 'password', 'token', 'vault_password', 'file'));
-- +goose StatementEnd
