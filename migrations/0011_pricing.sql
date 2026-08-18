-- Cost visibility: what a request was worth, not a bill.
--
-- Polyglot is a protocol gateway, so nothing here deducts a balance, enforces
-- a quota or blocks anything. These columns exist to answer one question an
-- operator cannot answer today: where does the spend go.
--
-- Prices are US dollars per million tokens — the unit models.dev publishes,
-- kept as-is so no conversion can drift.

-- The operator's own price for one model on one provider. Every column is
-- nullable, and NULL means "follow the catalog" rather than "free": an
-- operator who corrects only the input price still tracks an official output
-- price cut. A deliberate 0 is a different statement and is stored as 0.
ALTER TABLE models ADD COLUMN price_input REAL;
ALTER TABLE models ADD COLUMN price_output REAL;
ALTER TABLE models ADD COLUMN price_cache_read REAL;
ALTER TABLE models ADD COLUMN price_cache_write REAL;

-- Official vendor prices, trimmed from models.dev. Keyed by the normalised
-- model id alone: the catalog answers what the vendor charges, and a
-- reseller's own margin is not something any catalog can state. Only
-- first-party vendors are ingested, so one id has one official price.
CREATE TABLE price_catalog (
    model_id    TEXT PRIMARY KEY,
    vendor      TEXT NOT NULL,
    input       REAL,
    output      REAL,
    cache_read  REAL,
    cache_write REAL
);

-- One row, tracking which snapshot is loaded. The version decides whether a
-- freshly built binary's embedded copy should replace what is already here:
-- upgrading brings newer prices, but never rolls back a refresh the operator
-- ran more recently.
CREATE TABLE price_catalog_meta (
    id         INTEGER PRIMARY KEY CHECK (id = 1),
    version    TEXT NOT NULL,
    source     TEXT NOT NULL,   -- embedded | models.dev
    fetched_at INTEGER NOT NULL
);

-- What one request cost, snapshotted when it completed.
--
-- Stored rather than computed at read time, so changing a price tomorrow does
-- not rewrite what yesterday's traffic reportedly cost. NULL is an unknown
-- cost — no price was in force — and is never rendered as zero, which would
-- claim the request was free.
--
-- Existing rows keep NULL, and a price added later does not backfill them.
-- That is honest for them: nobody knew what those requests cost at the time,
-- and no backfill can invent it.
ALTER TABLE request_logs ADD COLUMN cost_usd REAL;
-- Where the price came from: models.dev | custom. Empty for an unknown cost.
ALTER TABLE request_logs ADD COLUMN cost_source TEXT NOT NULL DEFAULT '';
-- What the number rests on when it is not exact, e.g. cache_price_assumed
-- when a cache price was missing and the plain input price stood in.
ALTER TABLE request_logs ADD COLUMN cost_note TEXT NOT NULL DEFAULT '';
