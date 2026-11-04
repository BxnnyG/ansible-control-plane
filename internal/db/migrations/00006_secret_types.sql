-- +goose Up
-- Align secret types with the API (ssh_key, password, token, vault_password, file)
-- and add non-secret metadata (e.g. the public key fingerprint of an ssh_key).
ALTER TABLE secrets DROP CONSTRAINT secrets_type_check;
UPDATE secrets SET type = 'token' WHERE type = 'api_token';
UPDATE secrets SET type = 'file' WHERE type = 'generic';
ALTER TABLE secrets ADD CONSTRAINT secrets_type_check
    CHECK (type IN ('ssh_key', 'password', 'token', 'vault_password', 'file'));
ALTER TABLE secrets ADD COLUMN metadata jsonb NOT NULL DEFAULT '{}'::jsonb;

-- +goose Down
ALTER TABLE secrets DROP COLUMN metadata;
ALTER TABLE secrets DROP CONSTRAINT secrets_type_check;
UPDATE secrets SET type = 'api_token' WHERE type = 'token';
UPDATE secrets SET type = 'generic' WHERE type = 'file';
ALTER TABLE secrets ADD CONSTRAINT secrets_type_check
    CHECK (type IN ('ssh_key', 'vault_password', 'api_token', 'password', 'generic'));
