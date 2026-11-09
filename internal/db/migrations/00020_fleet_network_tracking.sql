-- +goose Up
-- +goose StatementBegin
-- Choose which discovered networks the fleet tracks. Networks are keyed on
-- (connector_id, external_key) and upserted on every sync, so the flag
-- survives syncs; a network that vanished and is pruned comes back with the
-- workspace default. fleet_new_networks_tracked is that default for newly
-- discovered networks.
ALTER TABLE networks ADD COLUMN tracked boolean NOT NULL DEFAULT true;
ALTER TABLE workspaces ADD COLUMN fleet_new_networks_tracked boolean NOT NULL DEFAULT true;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE workspaces DROP COLUMN fleet_new_networks_tracked;
ALTER TABLE networks DROP COLUMN tracked;
-- +goose StatementEnd
