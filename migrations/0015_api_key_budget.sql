-- A spending cap per API key, in USD.
--
-- This is the one place where money can refuse a request, and it is deliberate:
-- a key handed to someone else needs a floor under how much it can cost, and
-- "watch the dashboard" is not one. Everything else about pricing stays what it
-- was — no balance, no top-up, no invoice, no deduction from anything.
--
-- NULL means no cap, so every key that existed before this migration keeps
-- exactly the behaviour it had.
--
-- The cap is approximate by construction, and the UI says so: a request is
-- priced after it finishes, so the one that crosses the line still completes,
-- and a model with no price contributes nothing to the total because an unknown
-- cost is not zero.
ALTER TABLE api_keys ADD COLUMN budget_usd REAL;

-- 'total' counts from budget_anchor and only an operator resets it; the others
-- roll over on their own in UTC, matching how RPD and TPD already count.
ALTER TABLE api_keys ADD COLUMN budget_period TEXT NOT NULL DEFAULT 'total';

-- When the current 'total' window began. NULL means the key's creation, which
-- is what an upgraded row gets.
ALTER TABLE api_keys ADD COLUMN budget_anchor INTEGER;

-- Summing a key's spend over a window is now a request-path question, asked
-- once per key per window rather than per request — but the old (api_key_id,
-- client_ip) index cannot answer it without reading every row for the key.
CREATE INDEX IF NOT EXISTS idx_request_logs_key_started ON request_logs(api_key_id, started_at);
