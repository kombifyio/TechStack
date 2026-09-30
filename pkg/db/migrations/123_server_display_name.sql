-- Owner-chosen server name (ai-platform-t2w6.6).
--
-- `servers.name` is a runtime projection: every registry, worker, RIL and
-- server-runtime upsert overwrites it. The owner's rename therefore lives in a
-- separate nullable column that only the inventory rename path writes; no
-- projection upsert lists it, so a later observation cannot undo a rename.
-- NULL means "not renamed" and readers fall back to the projected name.
--
-- The runner wraps each migration in one transaction, so the check is added
-- NOT VALID here and validated by migration 124 without holding this
-- migration's ACCESS EXCLUSIVE lock during the scan.

SET LOCAL lock_timeout = '5s';

ALTER TABLE servers ADD COLUMN IF NOT EXISTS display_name text;

ALTER TABLE servers DROP CONSTRAINT IF EXISTS servers_display_name_check;
ALTER TABLE servers ADD CONSTRAINT servers_display_name_check
    CHECK (display_name IS NULL OR char_length(display_name) BETWEEN 1 AND 100) NOT VALID;
