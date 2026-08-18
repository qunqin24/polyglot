-- Give a request log row the fields the telemetry lifecycle now measures.
--
-- request_id is the identifier that ties this row to the structured log lines
-- and, when tracing is on, to a trace. It is a plain string with no meaning of
-- its own, so it is safe to store and safe to show.
--
-- retry_count and fallback_count record what the router's candidate list
-- actually did: how many attempts followed the first, and how many of those
-- moved to a different provider. Before this, a request that succeeded on its
-- second provider was indistinguishable from one that succeeded outright.
--
-- generation_ms and output_tps are streaming-only, and null when they could
-- not be measured — an upstream that reports no token usage, or a reply short
-- enough that the first and last token share a millisecond. Null means "not
-- measurable", never zero: an invented number is worse than a gap.
ALTER TABLE request_logs ADD COLUMN request_id TEXT NOT NULL DEFAULT '';
ALTER TABLE request_logs ADD COLUMN retry_count INTEGER NOT NULL DEFAULT 0;
ALTER TABLE request_logs ADD COLUMN fallback_count INTEGER NOT NULL DEFAULT 0;
ALTER TABLE request_logs ADD COLUMN generation_ms INTEGER;
ALTER TABLE request_logs ADD COLUMN output_tps REAL;

-- ttfb_ms measured from the moment the upstream's response headers arrived to
-- the first canonical event, which excluded connection setup, provider queueing
-- and prompt processing — in other words most of the wait, and the part that
-- differs between providers. It also counted message.start, which carries no
-- text at all.
--
-- ttft_ms replaces it with the number everyone means by "time to first token":
-- from Polyglot receiving the request to the first chunk that carries content.
-- The old column goes rather than being redefined in place, so no stored value
-- ever means two different things.
ALTER TABLE request_logs DROP COLUMN ttfb_ms;
ALTER TABLE request_logs ADD COLUMN ttft_ms INTEGER;

CREATE INDEX IF NOT EXISTS idx_request_logs_request_id ON request_logs(request_id);
