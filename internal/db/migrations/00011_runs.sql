-- +goose Up
-- +goose StatementBegin
-- Templates, runs, logs, per-host results and schedules (ADR-0016, RUN-1..7).
-- Credentials are references to the secret store; deleting a secret detaches
-- it (SET NULL). Project and inventory are RESTRICT: a template in use blocks
-- their deletion. Runs keep their history when a template is deleted
-- (template_id SET NULL, template_name is a snapshot).
CREATE TABLE templates (
    id                   uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id         uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    project_id           uuid NOT NULL REFERENCES projects(id) ON DELETE RESTRICT,
    inventory_id         uuid NOT NULL REFERENCES inventories(id) ON DELETE RESTRICT,
    name                 text NOT NULL CHECK (name <> ''),
    slug                 text NOT NULL CHECK (slug ~ '^[a-z0-9][a-z0-9-]{0,62}$'),
    description          text NOT NULL DEFAULT '',
    playbook             text NOT NULL CHECK (playbook <> ''),
    extra_vars           jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(extra_vars) = 'object'),
    env_vars             jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(env_vars) = 'object'),
    ssh_secret_id        uuid REFERENCES secrets(id) ON DELETE SET NULL,
    become_secret_id     uuid REFERENCES secrets(id) ON DELETE SET NULL,
    default_limit        text NOT NULL DEFAULT '',
    default_tags         text NOT NULL DEFAULT '',
    default_skip_tags    text NOT NULL DEFAULT '',
    verbosity            smallint NOT NULL DEFAULT 0 CHECK (verbosity BETWEEN 0 AND 4),
    check_mode           boolean NOT NULL DEFAULT false,
    diff_mode            boolean NOT NULL DEFAULT false,
    survey               jsonb CHECK (survey IS NULL OR jsonb_typeof(survey) IN ('array', 'object')),
    max_concurrent       integer NOT NULL DEFAULT 1 CHECK (max_concurrent >= 1),
    allow_limit_override boolean NOT NULL DEFAULT true,
    allow_tags_override  boolean NOT NULL DEFAULT true,
    allow_vars_override  boolean NOT NULL DEFAULT false,
    allow_check_override boolean NOT NULL DEFAULT true,
    allow_diff_override  boolean NOT NULL DEFAULT true,
    allow_verbosity_override boolean NOT NULL DEFAULT true,
    webhook_enabled      boolean NOT NULL DEFAULT false,
    webhook_secret_id    uuid REFERENCES secrets(id) ON DELETE SET NULL,
    created_by           uuid REFERENCES users(id) ON DELETE SET NULL,
    created_at           timestamptz NOT NULL DEFAULT now(),
    updated_at           timestamptz NOT NULL DEFAULT now(),
    UNIQUE (workspace_id, slug),
    UNIQUE (workspace_id, name)
);
CREATE INDEX templates_project_idx ON templates (project_id);
CREATE INDEX templates_inventory_idx ON templates (inventory_id);

CREATE TABLE template_vault_identities (
    template_id       uuid NOT NULL REFERENCES templates(id) ON DELETE CASCADE,
    vault_identity_id uuid NOT NULL REFERENCES vault_identities(id) ON DELETE CASCADE,
    position          integer NOT NULL DEFAULT 0,
    PRIMARY KEY (template_id, vault_identity_id)
);
CREATE INDEX template_vault_identities_vi_idx ON template_vault_identities (vault_identity_id);

CREATE TABLE schedules (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    template_id  uuid NOT NULL REFERENCES templates(id) ON DELETE CASCADE,
    name         text NOT NULL DEFAULT '',
    cron_expr    text NOT NULL CHECK (cron_expr <> ''),
    timezone     text NOT NULL DEFAULT 'UTC' CHECK (timezone <> ''),
    enabled      boolean NOT NULL DEFAULT true,
    missed_policy text NOT NULL DEFAULT 'skip' CHECK (missed_policy IN ('skip', 'run_once')),
    overrides    jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(overrides) = 'object'),
    next_run_at  timestamptz,
    last_run_at  timestamptz,
    created_by   uuid REFERENCES users(id) ON DELETE SET NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX schedules_template_idx ON schedules (template_id);
CREATE INDEX schedules_due_idx ON schedules (next_run_at) WHERE enabled;

CREATE TABLE runs (
    id                  uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id        uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    template_id         uuid REFERENCES templates(id) ON DELETE SET NULL,
    template_name       text NOT NULL DEFAULT '',
    project_id          uuid REFERENCES projects(id) ON DELETE SET NULL,
    project_revision    text NOT NULL DEFAULT '',
    status              text NOT NULL DEFAULT 'queued'
        CHECK (status IN ('queued', 'running', 'succeeded', 'failed', 'cancelled', 'lost')),
    trigger             text NOT NULL CHECK (trigger IN ('manual', 'schedule', 'webhook', 'api')),
    triggered_by        uuid REFERENCES users(id) ON DELETE SET NULL,
    schedule_id         uuid REFERENCES schedules(id) ON DELETE SET NULL,
    overrides           jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(overrides) = 'object'),
    params              jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(params) = 'object'),
    worker_id           text NOT NULL DEFAULT '',
    lease_id            uuid,
    claimed_at          timestamptz,
    started_at          timestamptz,
    finished_at         timestamptz,
    last_heartbeat_at   timestamptz,
    cancel_requested_at timestamptz,
    exit_code           integer,
    hosts_ok            integer NOT NULL DEFAULT 0,
    hosts_changed       integer NOT NULL DEFAULT 0,
    hosts_failed        integer NOT NULL DEFAULT 0,
    hosts_unreachable   integer NOT NULL DEFAULT 0,
    created_at          timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX runs_queue_idx ON runs (created_at, id) WHERE status = 'queued';
CREATE INDEX runs_running_idx ON runs (template_id) WHERE status = 'running';
CREATE INDEX runs_workspace_created_idx ON runs (workspace_id, created_at DESC, id DESC);
CREATE INDEX runs_template_created_idx ON runs (template_id, created_at DESC, id DESC);
CREATE INDEX runs_status_created_idx ON runs (status, created_at DESC);
CREATE INDEX runs_trigger_created_idx ON runs (trigger, created_at DESC);
CREATE INDEX runs_user_created_idx ON runs (triggered_by, created_at DESC);

CREATE TABLE run_log_chunks (
    run_id     uuid NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    seq        bigint NOT NULL,
    data       text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (run_id, seq)
);
CREATE INDEX run_log_chunks_created_idx ON run_log_chunks (created_at);

CREATE TABLE run_host_results (
    run_id        uuid NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    host          text NOT NULL,
    ok            integer NOT NULL DEFAULT 0,
    changed       integer NOT NULL DEFAULT 0,
    failed        integer NOT NULL DEFAULT 0,
    unreachable   integer NOT NULL DEFAULT 0,
    skipped       integer NOT NULL DEFAULT 0,
    rescued       integer NOT NULL DEFAULT 0,
    ignored       integer NOT NULL DEFAULT 0,
    changed_tasks jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(changed_tasks) = 'array'),
    PRIMARY KEY (run_id, host)
);
CREATE INDEX run_host_results_host_idx ON run_host_results (host);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE run_host_results;
DROP TABLE run_log_chunks;
DROP TABLE runs;
DROP TABLE schedules;
DROP TABLE template_vault_identities;
DROP TABLE templates;
-- +goose StatementEnd
