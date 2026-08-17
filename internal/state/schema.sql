-- Schema for the local vpncli state database.
--
-- This replaces Terraform state. It is deliberately small: the provider API is
-- the source of truth, this table is a fast local cache plus the bits the API
-- does not store for us (REALITY keys, our own short ids). `vpncli sync`
-- reconciles the two.

CREATE TABLE IF NOT EXISTS servers (
    -- Short local id. This is what the user types: `vpncli connect 3`.
    id          INTEGER PRIMARY KEY AUTOINCREMENT,

    provider    TEXT    NOT NULL,
    -- Provider-side id (droplet id, etc). String because providers disagree.
    provider_id TEXT    NOT NULL,

    name        TEXT    NOT NULL,
    region      TEXT    NOT NULL,
    size        TEXT    NOT NULL,
    image       TEXT    NOT NULL,
    ipv4        TEXT    NOT NULL DEFAULT '',
    status      TEXT    NOT NULL DEFAULT 'unknown',

    created_at  TIMESTAMP NOT NULL,
    updated_at  TIMESTAMP NOT NULL,

    UNIQUE (provider, provider_id)
);

CREATE INDEX IF NOT EXISTS idx_servers_provider ON servers (provider);
CREATE INDEX IF NOT EXISTS idx_servers_status ON servers (status);
