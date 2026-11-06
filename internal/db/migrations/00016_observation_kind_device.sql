-- +goose Up
-- +goose StatementBegin
-- NetBox devices (ADR-0019) are observed as kind "device".
ALTER TABLE host_observations DROP CONSTRAINT host_observations_kind_check;
ALTER TABLE host_observations ADD CONSTRAINT host_observations_kind_check
    CHECK (kind IN ('lease', 'arp', 'ndp', 'static', 'vm', 'lxc', 'node', 'device'));
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DELETE FROM host_observations WHERE kind = 'device';
ALTER TABLE host_observations DROP CONSTRAINT host_observations_kind_check;
ALTER TABLE host_observations ADD CONSTRAINT host_observations_kind_check
    CHECK (kind IN ('lease', 'arp', 'ndp', 'static', 'vm', 'lxc', 'node'));
-- +goose StatementEnd
