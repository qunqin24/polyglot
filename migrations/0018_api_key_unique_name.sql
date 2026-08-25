-- A key's name is how the operator tells one from another, in the list and in
-- the request log's api_key_name column. Two keys called the same thing make
-- both of those unreadable, so names are unique from here on.
--
-- A database written before this may already hold duplicates, and the index
-- cannot be created over them. Rename the later ones rather than deleting a
-- key somebody is still using: a renamed key keeps working, a deleted one
-- starts returning 401s. The suffix is the row id, which is unique, so the
-- only way the result still collides is an operator who had already named
-- another key exactly that. The statement runs twice to settle that case too;
-- each pass makes the string longer, so it converges.
UPDATE api_keys SET name = name || ' #' || id
WHERE id NOT IN (SELECT MIN(id) FROM api_keys GROUP BY name);

UPDATE api_keys SET name = name || ' #' || id
WHERE id NOT IN (SELECT MIN(id) FROM api_keys GROUP BY name);

CREATE UNIQUE INDEX idx_api_keys_name ON api_keys(name);
