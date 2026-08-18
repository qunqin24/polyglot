-- Record which address each request came from.
--
-- The question this answers is "has one of my API keys leaked": a key that has
-- only ever been used from one machine suddenly appearing from three countries
-- is the signal, and without an address there is no way to see it.
--
-- It is stored whole rather than truncated. A masked address cannot answer the
-- question it exists for, and this is a personal gateway logging its owner's
-- own clients, not a public service profiling strangers.
--
-- Storing it is only worth anything if it cannot be forged, which is why the
-- same change stops X-Forwarded-For being honoured unless the operator has
-- said there is a proxy in front. See TRUST_PROXY_HEADERS.
ALTER TABLE request_logs ADD COLUMN client_ip TEXT NOT NULL DEFAULT '';

-- Both of these serve the leak question directly: every request from one
-- address, and every address one key has been used from.
CREATE INDEX IF NOT EXISTS idx_request_logs_client_ip ON request_logs(client_ip);
CREATE INDEX IF NOT EXISTS idx_request_logs_key_ip ON request_logs(api_key_id, client_ip);
