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

    -- What a client needs to reach this server, generated during the bootstrap
    -- and stored nowhere else. A rotation makes a fresh set, which is the
    -- point: the old keys die with the old IP.
    uuid                TEXT NOT NULL DEFAULT '',
    reality_private_key TEXT NOT NULL DEFAULT '',
    reality_public_key  TEXT NOT NULL DEFAULT '',
    reality_short_id    TEXT NOT NULL DEFAULT '',
    -- The camouflage this server was actually configured with. Kept per server
    -- rather than read from config.yaml, which may have moved on since.
    reality_dest        TEXT NOT NULL DEFAULT '',
    reality_server_name TEXT NOT NULL DEFAULT '',

    -- The host key seen the first time we connected, in authorized_keys form.
    -- A fresh server has no key anyone could have known in advance, so the
    -- first connection trusts and records; every later one has to match.
    ssh_host_key TEXT NOT NULL DEFAULT '',

    -- When the bootstrap finished. NULL means a server that exists but is not
    -- yet configured, which is what `vpncli bootstrap` is for.
    bootstrapped_at TIMESTAMP,

    UNIQUE (provider, provider_id)
);

CREATE INDEX IF NOT EXISTS idx_servers_provider ON servers (provider);
CREATE INDEX IF NOT EXISTS idx_servers_status ON servers (status);
