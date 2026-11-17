-- +goose Up
-- +goose StatementBegin
-- Reachability matrix (ADR-0022, NET-1). One config row per workspace names the
-- inventory the probe sources live in, the SSH credential, and the probe
-- interval (0 = scheduler off). Sources are inventory hosts, targets are
-- address + check definitions; results hold the latest status per pair.
CREATE TABLE reachability_config (
    workspace_id     uuid PRIMARY KEY REFERENCES workspaces(id) ON DELETE CASCADE,
    inventory_id     uuid REFERENCES inventories(id) ON DELETE SET NULL,
    ssh_secret_id    uuid REFERENCES secrets(id) ON DELETE SET NULL,
    interval_seconds integer NOT NULL DEFAULT 0 CHECK (interval_seconds = 0 OR interval_seconds BETWEEN 60 AND 86400),
    next_run_at      timestamptz,
    last_run_at      timestamptz,
    updated_at       timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX reachability_config_due_idx ON reachability_config (next_run_at) WHERE interval_seconds > 0;

CREATE TABLE reachability_sources (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    host_id      uuid NOT NULL REFERENCES inventory_hosts(id) ON DELETE CASCADE,
    created_at   timestamptz NOT NULL DEFAULT now(),
    UNIQUE (workspace_id, host_id)
);

CREATE TABLE reachability_targets (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    name         text NOT NULL CHECK (name <> '' AND length(name) <= 120),
    address      text NOT NULL CHECK (address <> '' AND length(address) <= 253),
    check_type   text NOT NULL CHECK (check_type IN ('icmp', 'tcp', 'dns')),
    port         integer CHECK (port IS NULL OR port BETWEEN 1 AND 65535),
    dns_name     text NOT NULL DEFAULT '' CHECK (length(dns_name) <= 253),
    enabled      boolean NOT NULL DEFAULT true,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    UNIQUE (workspace_id, name),
    CHECK (check_type <> 'tcp' OR port IS NOT NULL),
    CHECK (check_type <> 'dns' OR dns_name <> '')
);

CREATE TABLE reachability_results (
    source_id  uuid NOT NULL REFERENCES reachability_sources(id) ON DELETE CASCADE,
    target_id  uuid NOT NULL REFERENCES reachability_targets(id) ON DELETE CASCADE,
    status     text NOT NULL CHECK (status IN ('ok', 'failed', 'probe_unreachable')),
    latency_ms integer CHECK (latency_ms IS NULL OR latency_ms >= 0),
    detail     text NOT NULL DEFAULT '' CHECK (length(detail) <= 200),
    run_id     uuid REFERENCES runs(id) ON DELETE SET NULL,
    checked_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (source_id, target_id)
);
CREATE INDEX reachability_results_target_idx ON reachability_results (target_id);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE reachability_results;
DROP TABLE reachability_targets;
DROP TABLE reachability_sources;
DROP TABLE reachability_config;
-- +goose StatementEnd
