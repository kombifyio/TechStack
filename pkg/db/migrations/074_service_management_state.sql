-- Service management state: who owns the definition, persisted once instead of
-- re-derived at read time.
--
-- Migration 073 separated the three measured/intended dimensions of a service
-- (desired, observed, health). It left the weakest-modeled axis untouched: the
-- managed-vs-observed distinction existed only as a read-time derivation in
-- internal/routes/registry.go, at three call sites with three slightly
-- different rules, and `services.source` was free text (NOT NULL DEFAULT
-- 'observed') with no vocabulary at all.
--
-- This migration adds the fourth aggregate dimension and closes the source
-- vocabulary. The two columns stay SEPARATE axes on purpose:
--
--   management_state - is there a declared target configuration for this row?
--     managed  : rolled out by us through StackKits/PaaS. The desired state
--                comes from the kit contract, so drift comparison is defined.
--     observed : discovered on a server we watch. No declared target exists, so
--                desired state is not applicable and drift is undefined.
--
--   source - which pipeline last reported the row (provenance):
--     observed                 : discovered without a declared contract, and
--                                the fail-closed default for unknown provenance
--     stackkits-inventory      : authenticated Guard runtime observation
--     stackkit_outputs         : StackKits apply/job output projection
--     legacy-registry-backfill : bounded read-through of pre-aggregate rows
--
-- Neither column is the StackKits evidence-provenance vocabulary
-- (local-runtime | standard-process | verified-apply-evidence). That axis
-- describes how strong an apply-evidence record is, is owned by StackKits, and
-- is deliberately not merged into either column here - conflating axes is
-- exactly how `status` ended up carrying the health value before 073.
--
-- BACKFILL MAPPING (reproduces the historical read-time derivation exactly, so
-- no existing row changes meaning). A row becomes `observed` when ANY of:
--   * lower(btrim(source))  = 'observed'   (registry.go:862 and registry.go:1596)
--   * lower(btrim(status))  = 'observed'   (registry.go:815, legacy import path)
--   * metadata_json->>'type' = 'custom'    (registry.go:815, hand-imported service)
-- Every other row becomes `managed`, which is what all three derivations
-- returned for it. management_state is computed BEFORE `source` is normalized,
-- so folding an unrecognized provenance cannot change a row's ownership.
--
-- desired_state stays on the table and stays NOT NULL: existing readers and the
-- 073 CHECK depend on it. For an `observed` row the stored value carries no
-- contract and is NOT authoritative - the aggregate write boundary
-- (pkg/controlplane/service_events.go) rejects a control-plane desired-state
-- write against an observed service with reason code
-- `desired_state_not_applicable_for_observed_service`, ignores a Guard-carried
-- desired seed for it, and never appends a `desired` transition for it. Drift
-- comparison is defined for managed services only.

SET LOCAL lock_timeout = '5s';
SELECT pg_catalog.set_config(
    'search_path',
    pg_catalog.quote_ident(pg_catalog.current_schema()) || ', pg_catalog, pg_temp',
    true
);

-- 1) The persisted ownership dimension. The column default is fail-closed:
-- a row that never states its ownership must not claim we manage it.
ALTER TABLE services ADD COLUMN IF NOT EXISTS management_state text NOT NULL DEFAULT 'observed';

ALTER TABLE services DROP CONSTRAINT IF EXISTS services_management_state_check;
ALTER TABLE services DROP CONSTRAINT IF EXISTS services_source_check;

-- 2) Backfill ownership from the historical derivation, before `source` is
-- normalized so that no fold can flip a row.
UPDATE services
SET management_state = CASE
    WHEN lower(btrim(COALESCE(source, ''))) = 'observed' THEN 'observed'
    WHEN lower(btrim(COALESCE(status, ''))) = 'observed' THEN 'observed'
    WHEN lower(btrim(COALESCE(metadata_json->>'type', ''))) = 'custom' THEN 'observed'
    ELSE 'managed'
END;

-- 3) Normalize the provenance column onto the closed vocabulary. Case and
-- padding are folded; an unrecognized or empty provenance becomes 'observed',
-- which is what it actually is: a row whose reporting pipeline is unknown.
UPDATE services
SET source = CASE
    WHEN lower(btrim(COALESCE(source, ''))) IN (
        'observed', 'stackkits-inventory', 'stackkit_outputs', 'legacy-registry-backfill'
    ) THEN lower(btrim(source))
    ELSE 'observed'
END
WHERE source IS NULL OR source NOT IN (
    'observed', 'stackkits-inventory', 'stackkit_outputs', 'legacy-registry-backfill'
);

ALTER TABLE services ADD CONSTRAINT services_management_state_check
    CHECK (management_state IN ('managed', 'observed'));
ALTER TABLE services ADD CONSTRAINT services_source_check
    CHECK (source IN ('observed', 'stackkits-inventory', 'stackkit_outputs', 'legacy-registry-backfill'));

-- 4) The transition timeline gains the management dimension. Rows stay
-- append-only and change-only: the write boundary emits a management row only
-- when ownership actually changed.
ALTER TABLE service_state_transitions DROP CONSTRAINT IF EXISTS service_state_transitions_dimension_check;
ALTER TABLE service_state_transitions ADD CONSTRAINT service_state_transitions_dimension_check
    CHECK (dimension IN ('desired', 'observed', 'health', 'management'));

-- The 073 dimension CHECK was created anonymously, so it carries a generated
-- name. Drop whichever unnamed CHECK still restricts `dimension` to the old
-- three-value vocabulary; the named constraint above is now authoritative.
DO $$
DECLARE
    stale_constraint text;
BEGIN
    FOR stale_constraint IN
        SELECT conname
        FROM pg_constraint
        WHERE conrelid = 'service_state_transitions'::regclass
            AND contype = 'c'
            AND conname <> 'service_state_transitions_dimension_check'
            AND pg_get_constraintdef(oid) LIKE '%dimension%'
            AND pg_get_constraintdef(oid) NOT LIKE '%management%'
    LOOP
        EXECUTE format(
            'ALTER TABLE service_state_transitions DROP CONSTRAINT %I',
            stale_constraint
        );
    END LOOP;
END
$$;

CREATE INDEX IF NOT EXISTS idx_services_management_state
    ON services (tenant_id, management_state);
