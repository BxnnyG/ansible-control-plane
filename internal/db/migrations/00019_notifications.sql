-- +goose Up
-- +goose StatementBegin
-- Run notifications (ADR-0021). Channel credentials live in the secrets store
-- (secret_id); deleting a secret detaches it (SET NULL). A trigger on runs feeds
-- the notification_events outbox when a run reaches a terminal status, so every
-- code path (worker finish, lost-run maintenance, cancel, CLI SQL) is covered.
CREATE TABLE notification_channels (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id    uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    name            text NOT NULL CHECK (name <> ''),
    type            text NOT NULL CHECK (type IN ('webhook', 'matrix', 'smtp', 'gotify', 'ntfy')),
    config          jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(config) = 'object'),
    secret_id       uuid REFERENCES secrets(id) ON DELETE SET NULL,
    tls_ca_pem      text NOT NULL DEFAULT '',
    tls_skip_verify boolean NOT NULL DEFAULT false,
    enabled         boolean NOT NULL DEFAULT true,
    created_by      uuid REFERENCES users(id) ON DELETE SET NULL,
    updated_by      uuid REFERENCES users(id) ON DELETE SET NULL,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    UNIQUE (workspace_id, name)
);

CREATE TABLE notification_rules (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    channel_id   uuid NOT NULL REFERENCES notification_channels(id) ON DELETE CASCADE,
    events       text[] NOT NULL CHECK (cardinality(events) > 0
        AND events <@ ARRAY['run.failed', 'run.recovered', 'run.drift', 'run.finished']),
    template_id  uuid REFERENCES templates(id) ON DELETE CASCADE,
    enabled      boolean NOT NULL DEFAULT true,
    created_by   uuid REFERENCES users(id) ON DELETE SET NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX notification_rules_ws_idx ON notification_rules (workspace_id) WHERE enabled;
CREATE INDEX notification_rules_channel_idx ON notification_rules (channel_id);
CREATE INDEX notification_rules_template_idx ON notification_rules (template_id);

CREATE TABLE notification_events (
    id           bigserial PRIMARY KEY,
    run_id       uuid NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    status       text NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    processed_at timestamptz
);
CREATE INDEX notification_events_pending_idx ON notification_events (id) WHERE processed_at IS NULL;
CREATE INDEX notification_events_processed_idx ON notification_events (processed_at) WHERE processed_at IS NOT NULL;

CREATE TABLE notification_deliveries (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    event_id        bigint NOT NULL REFERENCES notification_events(id) ON DELETE CASCADE,
    run_id          uuid NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    channel_id      uuid NOT NULL REFERENCES notification_channels(id) ON DELETE CASCADE,
    kind            text NOT NULL CHECK (kind IN ('run.failed', 'run.recovered', 'run.drift', 'run.finished')),
    status          text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'sent', 'failed')),
    attempts        integer NOT NULL DEFAULT 0,
    next_attempt_at timestamptz NOT NULL DEFAULT now(),
    last_error      text NOT NULL DEFAULT '',
    sent_at         timestamptz,
    created_at      timestamptz NOT NULL DEFAULT now(),
    UNIQUE (event_id, channel_id)
);
CREATE INDEX notification_deliveries_due_idx ON notification_deliveries (next_attempt_at) WHERE status = 'pending';
CREATE INDEX notification_deliveries_channel_idx ON notification_deliveries (channel_id, created_at DESC);
CREATE INDEX notification_deliveries_run_idx ON notification_deliveries (run_id);

CREATE FUNCTION notification_enqueue_run() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    INSERT INTO notification_events (run_id, status) VALUES (NEW.id, NEW.status);
    RETURN NEW;
END;
$$;

CREATE TRIGGER runs_notification_event
AFTER UPDATE OF status ON runs
FOR EACH ROW
WHEN (OLD.status IS DISTINCT FROM NEW.status
    AND OLD.status IN ('queued', 'running')
    AND NEW.status IN ('succeeded', 'failed', 'cancelled', 'lost'))
EXECUTE FUNCTION notification_enqueue_run();
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TRIGGER runs_notification_event ON runs;
DROP FUNCTION notification_enqueue_run();
DROP TABLE notification_deliveries;
DROP TABLE notification_events;
DROP TABLE notification_rules;
DROP TABLE notification_channels;
-- +goose StatementEnd
