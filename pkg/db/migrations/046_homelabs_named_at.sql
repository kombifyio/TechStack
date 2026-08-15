-- 046_homelabs_named_at.sql
--
-- "Did the owner name this homelab?" is a fact about the row, not something a
-- reader can infer from the string. Every homelab starts with the generated
-- name "homelab" (migration 044's backfill and the wizard default), and the
-- dashboard has to know whether that name was chosen or merely generated: a
-- chosen name outranks the kombify Cloud Stack Identity in the title, a
-- generated one does not. Comparing against the literal would silently discard
-- a rename to exactly "homelab" and would break the moment the generator or a
-- localized default changes.
--
-- NULL means "still the generated name". PATCH /api/v1/homelab stamps it.
ALTER TABLE homelabs ADD COLUMN IF NOT EXISTS named_at timestamptz;

-- Existing rows keep NULL: nothing renamed them, because until this release
-- there was no rename operation at all.
