-- Record what a request was for, not just that it happened.
--
-- "Which of my projects made this call" is a question the existing columns
-- cannot answer: model and key tell you what was used, never what for. Three
-- sources fill that in, in decreasing order of how sure they are:
--
--   client_app       X-Title, else the HTTP-Referer host, else the User-Agent
--                    product token. The first two are OpenRouter's attribution
--                    convention, so tools already written for it work with no
--                    change; the third costs the caller nothing at all and is
--                    what makes this useful retroactively.
--
--   request_user     the `user` field OpenAI and Responses carry, and
--                    Anthropic's metadata.user_id. Already decoded into the
--                    canonical request today and thrown away at the end of it.
--
--   request_metadata the client's own key/value labels, verbatim. This is the
--                    precise answer when you control the caller:
--                    {"project": "docs-site", "task": "summarise"}.
--
-- Only named headers are read. A snapshot of all headers filtered afterwards
-- would be one forgotten case away from writing a credential into the
-- database; reading three by name cannot reach one.
ALTER TABLE request_logs ADD COLUMN client_app TEXT NOT NULL DEFAULT '';
ALTER TABLE request_logs ADD COLUMN request_user TEXT NOT NULL DEFAULT '';
ALTER TABLE request_logs ADD COLUMN request_metadata TEXT NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS idx_request_logs_client_app ON request_logs(client_app);
