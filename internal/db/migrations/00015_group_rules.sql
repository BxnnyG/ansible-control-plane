-- +goose Up
-- +goose StatementBegin
-- Group rules (INV-5, ADR-0018): the match kind is open (subnet, proxmox_tag,
-- later NetBox role/site/tag) and its parameters are JSON, so new kinds need
-- no schema change. mode "sync" removes the hosts a rule added earlier;
-- group_rule_members remembers exactly those memberships so manual ones are
-- never touched.
ALTER TABLE group_rules DROP CONSTRAINT group_rules_kind_check;
ALTER TABLE group_rules ADD CONSTRAINT group_rules_kind_check CHECK (kind ~ '^[a-z][a-z0-9_]{0,31}$');
ALTER TABLE group_rules ADD COLUMN mode text NOT NULL DEFAULT 'add-only' CHECK (mode IN ('add-only', 'sync'));
ALTER TABLE group_rules ADD COLUMN last_applied_at timestamptz;
ALTER TABLE group_rules ADD COLUMN last_applied_by uuid REFERENCES users(id) ON DELETE SET NULL;
CREATE INDEX group_rules_inventory_idx ON group_rules (inventory_id);

CREATE TABLE group_rule_members (
    rule_id   uuid NOT NULL REFERENCES group_rules(id) ON DELETE CASCADE,
    host_name text NOT NULL CHECK (host_name <> ''),
    added_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (rule_id, host_name)
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE group_rule_members;
DROP INDEX group_rules_inventory_idx;
ALTER TABLE group_rules DROP COLUMN last_applied_by;
ALTER TABLE group_rules DROP COLUMN last_applied_at;
ALTER TABLE group_rules DROP COLUMN mode;
DELETE FROM group_rules WHERE kind NOT IN ('subnet', 'proxmox_tag');
ALTER TABLE group_rules DROP CONSTRAINT group_rules_kind_check;
ALTER TABLE group_rules ADD CONSTRAINT group_rules_kind_check CHECK (kind IN ('subnet', 'proxmox_tag'));
-- +goose StatementEnd
