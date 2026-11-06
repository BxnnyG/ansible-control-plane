-- +goose Up
-- +goose StatementBegin
-- Stored monitoring alerts (ADR-0019). Pull connectors replace their open
-- set on every sync; webhook connectors report firing and resolved alerts
-- individually. host_id is the merged host the alert was linked to (name or
-- address match) and is NULL when there is no unambiguous match.
CREATE TABLE alerts (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    connector_id uuid NOT NULL REFERENCES connectors(id) ON DELETE CASCADE,
    external_key text NOT NULL CHECK (external_key <> ''),
    host_name    text NOT NULL DEFAULT '',
    host_ip      text NOT NULL DEFAULT '',
    host_id      uuid REFERENCES hosts(id) ON DELETE SET NULL,
    service      text NOT NULL DEFAULT '',
    severity     text NOT NULL CHECK (severity IN ('critical', 'warning', 'unknown', 'info')),
    summary      text NOT NULL DEFAULT '',
    labels       jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(labels) = 'object'),
    started_at   timestamptz NOT NULL DEFAULT now(),
    last_seen_at timestamptz NOT NULL DEFAULT now(),
    resolved_at  timestamptz,
    UNIQUE (connector_id, external_key)
);
CREATE INDEX alerts_workspace_idx ON alerts (workspace_id, resolved_at);
CREATE INDEX alerts_host_idx ON alerts (host_id) WHERE host_id IS NOT NULL;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE alerts;
-- +goose StatementEnd
