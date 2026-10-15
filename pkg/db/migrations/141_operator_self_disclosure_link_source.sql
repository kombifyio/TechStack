-- 141_operator_self_disclosure_link_source.sql
-- Provenance for self-disclosure answers that arrive through a link
-- (CREATION-EXPERIENCE-STANDARD §4).
--
-- A link into the creation wizard only prefills. The wizard stores the
-- answers after the operator confirms them, with source 'link', so they stay
-- distinguishable from answers typed in Techstack ('techstack'). Rows the
-- wizard stored straight from a link before confirmation was required keep
-- source 'cloud': they stay readable and are never written again, and they are
-- not relabelled, because no operator confirmed them.

SET LOCAL lock_timeout = '5s';
SELECT pg_catalog.set_config(
    'search_path',
    pg_catalog.quote_ident(pg_catalog.current_schema()) || ', pg_catalog, pg_temp',
    true
);

-- 128 declared the source check inline, so its name is generated. Drop every
-- check on the table that reads the source column, whatever it is called.
DO $$
DECLARE
    constraint_name text;
BEGIN
    FOR constraint_name IN
        SELECT c.conname
        FROM pg_catalog.pg_constraint c
        JOIN pg_catalog.pg_attribute a
            ON a.attrelid = c.conrelid AND a.attnum = ANY (c.conkey)
        WHERE c.conrelid = 'operator_self_disclosures'::regclass
          AND c.contype = 'c'
          AND a.attname = 'source'
    LOOP
        EXECUTE format('ALTER TABLE operator_self_disclosures DROP CONSTRAINT %I', constraint_name);
    END LOOP;
END
$$;

ALTER TABLE operator_self_disclosures
    ADD CONSTRAINT operator_self_disclosures_source_check
        CHECK (source IN ('link', 'techstack', 'cloud'));
