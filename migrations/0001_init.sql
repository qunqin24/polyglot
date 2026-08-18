-- Polyglot initial schema.
-- Six tables, no more: everything here is required by "protocol gateway".

CREATE TABLE settings (
    key        TEXT PRIMARY KEY,
    value      TEXT NOT NULL,
    updated_at INTEGER NOT NULL
);

-- A single local administrator. No registration, no org, no RBAC.
CREATE TABLE admins (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    username      TEXT NOT NULL UNIQUE,
    password_hash TEXT NOT NULL,
    created_at    INTEGER NOT NULL,
    updated_at    INTEGER NOT NULL
);

-- Admin browser sessions. Only the hash of the token is stored.
CREATE TABLE sessions (
    token_hash TEXT PRIMARY KEY,
    admin_id   INTEGER NOT NULL REFERENCES admins(id) ON DELETE CASCADE,
    created_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL,
    user_agent TEXT NOT NULL DEFAULT ''
);
CREATE INDEX idx_sessions_expires ON sessions(expires_at);

-- An upstream service. protocol says how to speak to it; the vendor name is
-- just a label. OpenRouter/DeepSeek/SiliconFlow are all protocol='openai'.
CREATE TABLE providers (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    name         TEXT NOT NULL UNIQUE,
    protocol     TEXT NOT NULL,
    base_url     TEXT NOT NULL,
    api_key_enc  BLOB,
    headers      TEXT NOT NULL DEFAULT '{}',
    timeout_secs INTEGER NOT NULL DEFAULT 0,
    enabled      INTEGER NOT NULL DEFAULT 1,
    created_at   INTEGER NOT NULL,
    updated_at   INTEGER NOT NULL
);

-- Model mapping: client-facing alias -> (provider, upstream model).
-- Several rows may share an alias; the lowest priority wins and the rest are
-- available as fallbacks.
CREATE TABLE model_routes (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    alias          TEXT NOT NULL,
    provider_id    INTEGER NOT NULL REFERENCES providers(id) ON DELETE CASCADE,
    upstream_model TEXT NOT NULL,
    priority       INTEGER NOT NULL DEFAULT 0,
    enabled        INTEGER NOT NULL DEFAULT 1,
    created_at     INTEGER NOT NULL,
    updated_at     INTEGER NOT NULL,
    UNIQUE (alias, provider_id, upstream_model)
);
CREATE INDEX idx_model_routes_alias ON model_routes(alias, priority);

-- Polyglot's own outward-facing keys. Only a hash is stored; the plaintext is
-- shown once at creation.
CREATE TABLE api_keys (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    name         TEXT NOT NULL,
    prefix       TEXT NOT NULL,
    secret_hash  TEXT NOT NULL UNIQUE,
    enabled      INTEGER NOT NULL DEFAULT 1,
    created_at   INTEGER NOT NULL,
    last_used_at INTEGER
);
CREATE INDEX idx_api_keys_hash ON api_keys(secret_hash);

-- One row per completed request. Never one row per streamed token.
CREATE TABLE request_logs (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    started_at        INTEGER NOT NULL,
    finished_at       INTEGER NOT NULL,
    latency_ms        INTEGER NOT NULL,
    ttfb_ms           INTEGER,
    status            TEXT NOT NULL,
    status_code       INTEGER NOT NULL,
    client_protocol   TEXT NOT NULL,
    upstream_protocol TEXT NOT NULL DEFAULT '',
    provider_id       INTEGER,
    provider_name     TEXT NOT NULL DEFAULT '',
    model_alias       TEXT NOT NULL DEFAULT '',
    upstream_model    TEXT NOT NULL DEFAULT '',
    api_key_id        INTEGER,
    api_key_name      TEXT NOT NULL DEFAULT '',
    stream            INTEGER NOT NULL DEFAULT 0,
    input_tokens      INTEGER NOT NULL DEFAULT 0,
    output_tokens     INTEGER NOT NULL DEFAULT 0,
    reasoning_tokens  INTEGER NOT NULL DEFAULT 0,
    error_type        TEXT NOT NULL DEFAULT '',
    error_message     TEXT NOT NULL DEFAULT '',
    fidelity_notes    TEXT NOT NULL DEFAULT ''
);
CREATE INDEX idx_request_logs_started ON request_logs(started_at DESC);
CREATE INDEX idx_request_logs_provider ON request_logs(provider_id, started_at DESC);
