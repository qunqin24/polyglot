-- Per-key usage policies. NULL means unlimited, so keys created by older
-- versions keep exactly the behaviour they had before this migration.
ALTER TABLE api_keys ADD COLUMN rpm INTEGER;
ALTER TABLE api_keys ADD COLUMN rph INTEGER;
ALTER TABLE api_keys ADD COLUMN rpd INTEGER;
ALTER TABLE api_keys ADD COLUMN tpm INTEGER;
ALTER TABLE api_keys ADD COLUMN tpd INTEGER;
ALTER TABLE api_keys ADD COLUMN max_concurrent INTEGER;
ALTER TABLE api_keys ADD COLUMN max_output_tokens INTEGER;
ALTER TABLE api_keys ADD COLUMN expires_at INTEGER;

-- An empty set permits every model. Model names are the exact client-facing
-- ids or aliases supplied in requests, before routing resolves them.
CREATE TABLE api_key_models (
    api_key_id INTEGER NOT NULL REFERENCES api_keys(id) ON DELETE CASCADE,
    model      TEXT NOT NULL,
    PRIMARY KEY (api_key_id, model)
);
CREATE INDEX idx_api_key_models_key ON api_key_models(api_key_id);
