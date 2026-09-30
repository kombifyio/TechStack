-- Validate the servers.display_name check added NOT VALID by migration 123.
-- VALIDATE CONSTRAINT takes only a SHARE UPDATE EXCLUSIVE lock, so concurrent
-- reads and writes continue while existing rows are checked.

SET LOCAL lock_timeout = '5s';

ALTER TABLE servers VALIDATE CONSTRAINT servers_display_name_check;
