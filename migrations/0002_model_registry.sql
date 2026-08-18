-- Models become first-class, and aliases become optional.
--
-- Until now the only way to reach an upstream model was to create a mapping
-- for it. That made a mapping a mandatory setup step. This migration adds a
-- registry of the real models each provider offers — discovered automatically
-- or added by hand — so clients can call an upstream model id directly.
--
-- The old model_routes table was always an alias table in everything but
-- name, so it is renamed rather than replaced. Existing rows keep working.

-- The real models a provider offers.
CREATE TABLE models (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    provider_id       INTEGER NOT NULL REFERENCES providers(id) ON DELETE CASCADE,
    -- The id the upstream itself uses, e.g. "anthropic/claude-sonnet-4".
    upstream_model_id TEXT NOT NULL,
    display_name      TEXT NOT NULL DEFAULT '',
    -- discovered | manual. Discovery never deletes manual entries.
    source            TEXT NOT NULL DEFAULT 'manual',
    enabled           INTEGER NOT NULL DEFAULT 1,
    -- Last time a sync saw this model upstream. NULL for manual entries that
    -- have never been confirmed.
    last_seen_at      INTEGER,
    created_at        INTEGER NOT NULL,
    updated_at        INTEGER NOT NULL,
    UNIQUE (provider_id, upstream_model_id)
);

-- Resolution looks models up by the id a client asked for.
CREATE INDEX idx_models_upstream ON models(upstream_model_id);
CREATE INDEX idx_models_provider ON models(provider_id);

-- model_routes was only ever a table of aliases; name it so.
DROP INDEX IF EXISTS idx_model_routes_alias;
ALTER TABLE model_routes RENAME TO model_aliases;
CREATE INDEX idx_model_aliases_alias ON model_aliases(alias, priority);

-- Provider priority breaks ties deterministically when the same upstream model
-- id exists on more than one provider. Lower wins.
ALTER TABLE providers ADD COLUMN priority INTEGER NOT NULL DEFAULT 0;

-- When models were last discovered for this provider; NULL means never.
ALTER TABLE providers ADD COLUMN models_synced_at INTEGER;
