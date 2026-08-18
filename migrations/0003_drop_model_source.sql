-- Drop models.source.
--
-- It recorded whether a model id came from an upstream listing or was typed in.
-- That mattered while discovery ran on its own: deleting a discovered model
-- meant the next sync would put it back, while a manual one stayed gone. Now
-- that every model is registered because an operator picked it, and nothing
-- syncs by itself, the column records only who typed the string — which
-- last_seen_at already tells you, and which nothing in the runtime reads.

ALTER TABLE models DROP COLUMN source;
