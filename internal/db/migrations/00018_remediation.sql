-- +goose Up
-- +goose StatementBegin
-- Remediation rules (FLEET-6, ADR-0019): an open alert that matches a rule can
-- be fixed manually by starting the rule's template with a limit derived from
-- the alert's host. Nothing here triggers runs automatically. A NULL
-- connector_type matches alerts of every connector type; empty regexes match
-- everything. group_name is only used with target = 'group'.
CREATE TABLE remediation_rules (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id   uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    name           text NOT NULL CHECK (name <> ''),
    enabled        boolean NOT NULL DEFAULT true,
    connector_type text CHECK (connector_type IS NULL OR connector_type ~ '^[a-z][a-z0-9_]{0,31}$'),
    service_regex  text NOT NULL DEFAULT '',
    summary_regex  text NOT NULL DEFAULT '',
    min_severity   text NOT NULL DEFAULT 'info' CHECK (min_severity IN ('critical', 'warning', 'unknown', 'info')),
    template_id    uuid NOT NULL REFERENCES templates(id) ON DELETE CASCADE,
    target         text NOT NULL CHECK (target IN ('host', 'group', 'network')),
    group_name     text NOT NULL DEFAULT '',
    check_first    boolean NOT NULL DEFAULT false,
    created_by     uuid REFERENCES users(id) ON DELETE SET NULL,
    updated_by     uuid REFERENCES users(id) ON DELETE SET NULL,
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now(),
    CHECK ((target = 'group') = (group_name <> '')),
    UNIQUE (workspace_id, name)
);
CREATE INDEX remediation_rules_template_idx ON remediation_rules (template_id);

-- Provenance of a run started from a remediation rule or a fleet bulk action,
-- e.g. {"source":"remediation","rule_id":...,"alert_id":...,"phase":"check"}.
ALTER TABLE runs ADD COLUMN remediation jsonb;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE runs DROP COLUMN remediation;
DROP TABLE remediation_rules;
-- +goose StatementEnd
