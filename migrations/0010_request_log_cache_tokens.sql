-- Prompt-cache accounting on the request log.
--
-- Every protocol already reports these and every codec already converts them;
-- they were being dropped at the gateway, so an operator could see that a
-- prompt cost 5000 input tokens but not that 4800 of them were served from
-- cache at a fraction of the price.
--
-- Both columns are parts of input_tokens, never additions to it — the same
-- rule canonical.Usage states. A hit rate is cached_input_tokens over
-- input_tokens, which is why the denominator had to be made consistent across
-- protocols before these columns were worth writing.
--
-- Existing rows keep 0. That is honest for them: the counts were never
-- recorded, and no backfill can invent them.
ALTER TABLE request_logs ADD COLUMN cached_input_tokens INTEGER NOT NULL DEFAULT 0;
ALTER TABLE request_logs ADD COLUMN cache_write_tokens INTEGER NOT NULL DEFAULT 0;
