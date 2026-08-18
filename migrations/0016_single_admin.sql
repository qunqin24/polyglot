-- First-run setup is allowed to create exactly one local administrator.
-- A unique index over a constant enforces that invariant for every writer,
-- including two setup requests that both observed an empty table.
CREATE UNIQUE INDEX idx_admins_singleton ON admins ((1));
