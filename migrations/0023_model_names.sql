-- The name the maker gives each model on models.dev ("DeepSeek V4 Flash" for
-- deepseek-v4-flash), so the WebUI can show it instead of the id. Taken only
-- from the maker's own listing: resellers spell the same model several ways.
ALTER TABLE model_makers ADD COLUMN name TEXT NOT NULL DEFAULT '';
