-- +goose Up
-- +goose StatementBegin
CREATE TABLE users (
    id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    oidc_issuer       text,
    oidc_subject      text,
    email             text NOT NULL DEFAULT '',
    display_name      text NOT NULL DEFAULT '',
    is_break_glass    boolean NOT NULL DEFAULT false,
    username          text,
    password_hash     text,
    totp_envelope     jsonb,
    disabled          boolean NOT NULL DEFAULT false,
    last_login_at     timestamptz,
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now(),
    -- OIDC identity is (issuer, subject); break-glass users have a local username instead.
    CONSTRAINT users_identity_chk CHECK (
        (is_break_glass AND username IS NOT NULL AND password_hash IS NOT NULL AND oidc_issuer IS NULL AND oidc_subject IS NULL)
        OR (NOT is_break_glass AND oidc_issuer IS NOT NULL AND oidc_subject IS NOT NULL AND password_hash IS NULL AND totp_envelope IS NULL)
    ),
    CONSTRAINT users_oidc_uniq UNIQUE (oidc_issuer, oidc_subject),
    CONSTRAINT users_username_uniq UNIQUE (username)
);

CREATE TABLE workspaces (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    slug        text NOT NULL UNIQUE CHECK (slug ~ '^[a-z0-9][a-z0-9-]{0,62}$'),
    name        text NOT NULL,
    description text NOT NULL DEFAULT '',
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE memberships (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    user_id      uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role         text NOT NULL CHECK (role IN ('owner', 'admin', 'editor', 'operator', 'viewer')),
    created_at   timestamptz NOT NULL DEFAULT now(),
    UNIQUE (workspace_id, user_id)
);
CREATE INDEX memberships_user_idx ON memberships (user_id);

-- No rows for a membership = whole workspace (ADR-0007). ref_id is a soft reference
-- because networks/inventories/groups are introduced in later milestones.
CREATE TABLE membership_scopes (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    membership_id uuid NOT NULL REFERENCES memberships(id) ON DELETE CASCADE,
    kind          text NOT NULL CHECK (kind IN ('network', 'inventory', 'inventory_group')),
    ref_id        text NOT NULL,
    UNIQUE (membership_id, kind, ref_id)
);

CREATE TABLE sessions (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    token_hash      bytea NOT NULL UNIQUE,
    csrf_hash       bytea NOT NULL,
    user_id         uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    id_token_hint   text,
    created_at      timestamptz NOT NULL DEFAULT now(),
    last_seen_at    timestamptz NOT NULL DEFAULT now(),
    idle_expires_at timestamptz NOT NULL,
    expires_at      timestamptz NOT NULL
);
CREATE INDEX sessions_user_idx ON sessions (user_id);
CREATE INDEX sessions_expires_idx ON sessions (expires_at);

-- Envelope columns mirror secrets.Envelope (ADR-0005); plaintext is never stored.
CREATE TABLE secrets (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id  uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    name          text NOT NULL,
    type          text NOT NULL CHECK (type IN ('ssh_key', 'vault_password', 'api_token', 'password', 'generic')),
    description   text NOT NULL DEFAULT '',
    env_version   integer NOT NULL,
    env_key_id    text NOT NULL,
    env_wrap_nonce bytea NOT NULL,
    env_wrapped_dek bytea NOT NULL,
    env_nonce     bytea NOT NULL,
    env_ciphertext bytea NOT NULL,
    created_by    uuid REFERENCES users(id) ON DELETE SET NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    UNIQUE (workspace_id, name)
);

-- Append-only: no FKs (rows must outlive users/workspaces), UPDATE/DELETE/TRUNCATE rejected.
CREATE TABLE audit_log (
    id           bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    ts           timestamptz NOT NULL DEFAULT now(),
    actor_id     uuid,
    actor_label  text NOT NULL DEFAULT '',
    action       text NOT NULL,
    target_type  text NOT NULL DEFAULT '',
    target_id    text NOT NULL DEFAULT '',
    workspace_id uuid,
    details      jsonb NOT NULL DEFAULT '{}'::jsonb
);
CREATE INDEX audit_log_ts_idx ON audit_log (ts DESC);
CREATE INDEX audit_log_ws_idx ON audit_log (workspace_id, ts DESC);

CREATE FUNCTION audit_log_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'audit_log is append-only';
END;
$$;
CREATE TRIGGER audit_log_no_update BEFORE UPDATE OR DELETE ON audit_log
    FOR EACH ROW EXECUTE FUNCTION audit_log_immutable();
CREATE TRIGGER audit_log_no_truncate BEFORE TRUNCATE ON audit_log
    FOR EACH STATEMENT EXECUTE FUNCTION audit_log_immutable();
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE audit_log;
DROP FUNCTION audit_log_immutable();
DROP TABLE secrets;
DROP TABLE sessions;
DROP TABLE membership_scopes;
DROP TABLE memberships;
DROP TABLE workspaces;
DROP TABLE users;
-- +goose StatementEnd
