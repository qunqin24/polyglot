-- Take a provider out of rotation when its credential stops working.
--
-- A cooldown is the right answer to a provider that is briefly unwell — a 429,
-- a 500, a timeout all heal on their own, and skipping the provider for half a
-- minute is enough. An expired key does not heal. Cooling it down means trying
-- again every thirty seconds forever, which is a steady trickle of failures to
-- an upstream that will keep refusing until someone edits the configuration.
--
-- So authentication failures can disable the provider instead. That is a
-- destructive-looking thing to do on the operator's behalf — some upstreams
-- answer 401 or 403 for a region restriction or an exhausted quota, not only
-- for a bad key — so it is off unless the operator asked for it, per provider.
--
-- disabled_reason records why, because a provider that switched itself off
-- with no explanation is worse than one that kept failing loudly. It is
-- cleared whenever the operator enables the provider again.
ALTER TABLE providers ADD COLUMN auto_disable_on_auth_error INTEGER NOT NULL DEFAULT 0;
ALTER TABLE providers ADD COLUMN disabled_reason TEXT NOT NULL DEFAULT '';
ALTER TABLE providers ADD COLUMN disabled_at INTEGER;
