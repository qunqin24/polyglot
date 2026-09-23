-- The tenant dimension. Every row that can be owned now names the team that
-- owns it.
--
-- The invariant this migration exists to establish is that there is always
-- exactly one team. A single-operator deployment never sees the concept: it
-- gets team 1, called 'Default', and everything it already had folds into it.
-- What changes is that nothing is reachable any more without naming a team.
-- Scoping is not something that switches on when a second team appears — it is
-- how every query is written from here on, and it stays invisible only because
-- the answer is always the same one.
--
-- Behaviour after this migration is unchanged by construction: one team, every
-- existing row inside it, every existing setting carried across.

CREATE TABLE teams (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    name       TEXT NOT NULL UNIQUE,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);

-- Team 1 is the one every deployment has. It is seeded here and not by Go at
-- first run, because the backfills below need it to exist already, and because
-- "the database always holds at least one team" should be a property of the
-- schema rather than of a code path somebody can forget to call.
INSERT INTO teams (id, name, created_at, updated_at)
VALUES (1, 'Default', strftime('%s','now'), strftime('%s','now'));

-- ---------------------------------------------------------------------------
-- The three ownership roots.
--
-- providers, api_keys and log_keys carry team_id. models, model_aliases and
-- api_key_models deliberately do not: each already hangs off a root through a
-- NOT NULL … ON DELETE CASCADE foreign key, so their team is a fact the schema
-- knows. A second copy of it would be free to disagree with providers.team_id
-- the first time a provider moved between teams, and two answers with no rule
-- for choosing between them is a worse leak surface than the join that answers
-- it today. Every query over those tables already joins providers; the team
-- predicate is one more term on a join that is already there.
--
-- DEFAULT 0, not DEFAULT 1, and that is the whole point of the column's shape.
-- An insert that forgets its team has to land somewhere findable
-- (WHERE team_id = 0) rather than be filed silently under the default team
-- where nobody will look for it again. The failure direction is the safe one.
-- The sentinel is permanent — removing the default would need a table rebuild —
-- and that is fine, because the sentinel is exactly what the triggers watch for.
--
-- The guard is a BEFORE INSERT trigger rather than a CHECK constraint. Two
-- reasons, and the second one is the stronger:
--
--   * ADD COLUMN … CHECK (team_id > 0) is rejected outright on a table that
--     already holds rows: SQLite re-validates them and at that instant every
--     one of them is 0.
--   * On an *empty* table the very same statement succeeds. A CHECK would
--     therefore be accepted on a database created from scratch and refused on
--     one being upgraded — the schema would fork on the database's history, and
--     what a developer sees locally would not be what an operator gets. The
--     trigger behaves identically on both.
--
-- RAISE(ABORT, …) puts its own text in the driver's error, so an insert that
-- forgot its team says so in the message instead of failing as an anonymous
-- constraint violation.
--
-- None of these columns gets REFERENCES teams(id). SQLite refuses to ADD COLUMN
-- a REFERENCES column that has a non-NULL default, and a nullable or
-- zero-defaulted tenant column is precisely what this migration is avoiding.
-- State the limit precisely, because it is easy to mis-remember: it is the
-- non-NULL default that is refused, not the foreign key. `… INTEGER REFERENCES
-- teams(id)` is accepted, and on an empty table even the NOT NULL DEFAULT 1
-- form is accepted — so a fresh database will happily tell you the constraint
-- can be added when it cannot be added to any database that holds data. Do not
-- trust that answer. Phase 2 adds the real foreign key as part of the table
-- rebuild described below.
-- ---------------------------------------------------------------------------

ALTER TABLE providers ADD COLUMN team_id INTEGER NOT NULL DEFAULT 0;
UPDATE providers SET team_id = 1;
CREATE TRIGGER providers_team_required BEFORE INSERT ON providers
WHEN NEW.team_id = 0
BEGIN SELECT RAISE(ABORT, 'providers.team_id: this insert did not carry a team'); END;

-- providers.name stays globally UNIQUE in this phase, and that is a deferral,
-- not an oversight. It should end up UNIQUE(team_id, name) — two teams each
-- naming their own upstream "openrouter" is not a collision — but it is an
-- inline table constraint living in the implicit index
-- sqlite_autoindex_providers_1, which cannot be dropped. Changing it means
-- rebuilding the table, and rebuilding providers *here* would destroy data:
--
--   * migrate() runs each file as one transaction on a connection with
--     foreign_keys ON, and PRAGMA foreign_keys cannot be changed inside a
--     transaction.
--   * models and model_aliases both hold REFERENCES providers(id) ON DELETE
--     CASCADE. DROP TABLE providers fires the implicit delete and takes every
--     model and every alias with it.
--   * ALTER TABLE providers RENAME TO providers_old does not dodge it: modern
--     SQLite rewrites the children's REFERENCES to point at providers_old, so
--     dropping it afterwards cascades exactly the same.
--
-- A safe rebuild means rebuilding the children in the same migration, or
-- teaching migrate() to run outside the foreign-key pragma. Both are real work
-- and neither belongs in a skeleton. While there is one team, UNIQUE(name) and
-- UNIQUE(team_id, name) are the same constraint, so waiting costs nothing.
-- Phase 2 does the rebuild once, for the name constraint and the real
-- REFERENCES teams(id) together. Do not undo the deferral by landing half of it
-- here: half of it is the half that cascades.

ALTER TABLE api_keys ADD COLUMN team_id INTEGER NOT NULL DEFAULT 0;
UPDATE api_keys SET team_id = 1;
CREATE TRIGGER api_keys_team_required BEFORE INSERT ON api_keys
WHEN NEW.team_id = 0
BEGIN SELECT RAISE(ABORT, 'api_keys.team_id: this insert did not carry a team'); END;

ALTER TABLE log_keys ADD COLUMN team_id INTEGER NOT NULL DEFAULT 0;
UPDATE log_keys SET team_id = 1;
CREATE TRIGGER log_keys_team_required BEFORE INSERT ON log_keys
WHEN NEW.team_id = 0
BEGIN SELECT RAISE(ABORT, 'log_keys.team_id: this insert did not carry a team'); END;

-- ---------------------------------------------------------------------------
-- request_logs.
--
-- It gets the column but no trigger. It has exactly one writer,
-- InsertRequestLogs, and that writer sits on the buffered flush path, which is
-- the one path the rules say must not grow per-row work.
--
-- team_id here is deliberately not a foreign key, and should not become one in
-- a later phase either. A log row is an immutable fact about a request that
-- happened; deleting a team does not un-happen it. Every foreign-key action
-- gets that wrong. CASCADE destroys the audit trail at the exact moment it is
-- most wanted. SET NULL forces the column nullable, and a nullable tenant
-- column is the real leak surface — it either matches nobody or tempts somebody
-- into `team_id IS NULL OR team_id = ?`. RESTRICT turns the retention policy
-- into a precondition for an administrative action. api_key_id already answered
-- this same question the same way; this is not a second mechanism standing
-- beside an existing one.
--
-- team_name is the denormalised snapshot that keeps the orphan readable, for
-- the reason api_key_name exists: once a team is gone, an integer pointing at
-- nothing tells nobody whose request that was. Deleting a team deletes its live
-- configuration, not its history, and the history ages out on the ordinary
-- retention window like every other row.
-- ---------------------------------------------------------------------------

ALTER TABLE request_logs ADD COLUMN team_id INTEGER NOT NULL DEFAULT 0;
ALTER TABLE request_logs ADD COLUMN team_name TEXT NOT NULL DEFAULT '';
UPDATE request_logs SET team_id = 1, team_name = 'Default';

-- A key's name tells one key from another inside its team, not across the whole
-- gateway. 0018 declared that with a named index, so this is a drop and a
-- create rather than a table rebuild.
--
-- secret_hash stays globally UNIQUE, and so does log_keys.token_hash: those two
-- lookups are how a presented secret is resolved to its owner, which is how a
-- request discovers its team in the first place. Scoping them would be circular.
DROP INDEX idx_api_keys_name;
CREATE UNIQUE INDEX idx_api_keys_team_name ON api_keys(team_id, name);

-- ---------------------------------------------------------------------------
-- Content logging, per team: the switch and the retention window both.
--
-- This is the setting that records prompts and completions. Which team's
-- conversations are recorded, and for how long they are kept, is not a decision
-- one team gets to make on another team's behalf, so it moves out of the
-- deployment-wide settings table and onto a row per team.
--
-- Team 1's row is seeded from the existing settings['content_logging'] JSON, so
-- a deployment that had content logging on comes up with it on and with its
-- retention window intact, and one that never enabled it comes up off. Both
-- directions matter: a silent enable starts recording prompts nobody asked to
-- record, and a silent disable quietly drops logs an operator is relying on.
--
-- The old settings row is left where it is. Nothing reads it after this, and
-- deleting it would only make a database harder to explain if this migration
-- ever has to be reasoned about after the fact.
-- ---------------------------------------------------------------------------

CREATE TABLE team_content_logging (
    team_id        INTEGER PRIMARY KEY,
    enabled        INTEGER NOT NULL DEFAULT 0,
    retention_days INTEGER NOT NULL DEFAULT 7,
    updated_at     INTEGER NOT NULL
);

INSERT INTO team_content_logging (team_id, enabled, retention_days, updated_at)
SELECT 1,
       COALESCE(json_extract(value, '$.enabled'), 0),
       COALESCE(json_extract(value, '$.retention_days'), 7),
       strftime('%s','now')
FROM settings WHERE key = 'content_logging';

-- A database that never enabled content logging has no such settings row, so
-- the SELECT above inserted nothing. OR IGNORE means this is the fallback for
-- exactly that case and a no-op otherwise.
INSERT OR IGNORE INTO team_content_logging (team_id, enabled, retention_days, updated_at)
VALUES (1, 0, 7, strftime('%s','now'));

-- Every request-log question is now "this team, this window", and every
-- provider listing is "this team".
CREATE INDEX idx_request_logs_team_started ON request_logs(team_id, started_at);
CREATE INDEX idx_providers_team ON providers(team_id);
