-- Immutable execution-lane custody for managed runtime leases.
--
-- legacy_simulate is historical quarantine only. It never authorizes an
-- execution. New provider work must be admitted separately as
-- techstack_provider_control before its first provider side effect.

SET LOCAL lock_timeout = '5s';

-- This is an intentionally non-rolling cutover. Before applying this
-- migration, deployment automation must disable legacy Simulate egress and
-- drain the old worker for at least its maximum 15-minute claim lease plus
-- heartbeat grace. The database cannot revoke a provider call that an old
-- process has already buffered in memory.
--
-- ACCESS EXCLUSIVE prevents another old worker from claiming, enqueueing, or
-- completing enrollment work while the quiet-window check and quarantine are
-- installed. lock_timeout makes a still-active database writer fail the
-- migration instead of turning a longer wait into an unsafe deployment.
LOCK TABLE techstack_vm_lease_enrollment_outbox IN ACCESS EXCLUSIVE MODE;

DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM techstack_vm_lease_enrollment_outbox
        WHERE status IN ('pending', 'retrying')
          AND updated_at > clock_timestamp() - INTERVAL '15 minutes'
          AND next_attempt_at > clock_timestamp()
    ) THEN
        RAISE EXCEPTION
            'legacy enrollment cutover requires a quiet window: a pending/retrying claim is still inside the 15-minute claim lease'
            USING ERRCODE = '55000';
    END IF;
END;
$$;

CREATE TABLE IF NOT EXISTS runtime_lease_execution_authorities (
    tenant_id text NOT NULL,
    lease_id text NOT NULL,
    execution_authority text NOT NULL,
    bound_at timestamptz NOT NULL,
    evidence_resource_generation_id text,
    evidence_json jsonb,
    PRIMARY KEY (tenant_id, lease_id),
    FOREIGN KEY (tenant_id, lease_id)
        REFERENCES techstack_vm_leases (tenant_id, id)
        ON UPDATE RESTRICT
        ON DELETE RESTRICT,
    CHECK (execution_authority IN ('legacy_simulate', 'techstack_provider_control')),
    CHECK (
        (
            execution_authority = 'legacy_simulate'
            AND NULLIF(BTRIM(evidence_resource_generation_id), '') IS NOT NULL
            AND jsonb_typeof(evidence_json) = 'object'
        )
        OR
        (
            execution_authority = 'techstack_provider_control'
            AND evidence_resource_generation_id IS NULL
            AND evidence_json IS NULL
        )
    )
);

-- Migrations run without app.tenant_id. Suspend the lease table's FORCE RLS
-- only inside this transaction so the cross-tenant evidence scan cannot
-- silently see zero rows. A migration failure rolls this state back.
ALTER TABLE techstack_vm_leases NO FORCE ROW LEVEL SECURITY;
ALTER TABLE techstack_vm_leases DISABLE ROW LEVEL SECURITY;

-- This backfill accepts only exact generation-bound custody evidence. Readiness,
-- enrollment state, attempts, provider aliases, URLs, timestamps, plan, and
-- observe operations are intentionally insufficient. The lateral candidate
-- selection is also the sole source of the immutable receipt fields: stale
-- metadata from a non-qualifying branch must never describe qualifying custody.
WITH exact_evidence AS (
    SELECT
        lease.tenant_id,
        lease.id AS lease_id,
        lease.lease_json->'metadata'->>'resource_generation_id' AS resource_generation_id,
        outbox.resource_generation_id AS outbox_resource_generation_id,
        evidence.evidence_kind,
        evidence.operation,
        evidence.operation_id,
        evidence.claim_status,
        evidence.result_status,
        evidence.provider_resource_ref,
        evidence.lease_resource_ref,
        evidence.request_resource_ref
    FROM techstack_vm_leases AS lease
    JOIN techstack_vm_lease_enrollment_outbox AS outbox
      ON outbox.tenant_id = lease.tenant_id
     AND outbox.lease_id = lease.id
    CROSS JOIN LATERAL (
        SELECT candidate.*
        FROM (
            -- A terminal side-effect result is stronger than a leftover claim.
            SELECT
                1 AS evidence_priority,
                'succeeded_side_effect_result'::text AS evidence_kind,
                lease.lease_json->'metadata'->>'executor_last_operation' AS operation,
                lease.lease_json->'metadata'->>'executor_last_operation_id' AS operation_id,
                NULL::text AS claim_status,
                lease.lease_json->'metadata'->>'executor_last_status' AS result_status,
                NULLIF(BTRIM(lease.lease_json->'metadata'->>'executor_last_provider_resource_ref'), '') AS provider_resource_ref,
                NULL::text AS lease_resource_ref,
                NULL::text AS request_resource_ref
            WHERE lease.lease_json->'metadata'->>'executor_last_resource_generation_id'
                      = lease.lease_json->'metadata'->>'resource_generation_id'
              AND NULLIF(BTRIM(lease.lease_json->'metadata'->>'executor_last_operation_id'), '') IS NOT NULL
              AND lease.lease_json->'metadata'->>'executor_last_operation'
                      IN ('provision', 'reconcile', 'decommission')
              AND lease.lease_json->'metadata'->>'executor_last_status' = 'succeeded'

            UNION ALL

            SELECT
                2,
                'failed_side_effect_result_with_handle'::text,
                lease.lease_json->'metadata'->>'executor_last_operation',
                lease.lease_json->'metadata'->>'executor_last_operation_id',
                NULL::text,
                lease.lease_json->'metadata'->>'executor_last_status',
                NULLIF(BTRIM(lease.lease_json->'metadata'->>'executor_last_provider_resource_ref'), ''),
                NULLIF(BTRIM(lease.lease_json->'resource'->>'engine_vm_id'), ''),
                NULL::text
            WHERE lease.lease_json->'metadata'->>'executor_last_resource_generation_id'
                      = lease.lease_json->'metadata'->>'resource_generation_id'
              AND NULLIF(BTRIM(lease.lease_json->'metadata'->>'executor_last_operation_id'), '') IS NOT NULL
              AND lease.lease_json->'metadata'->>'executor_last_operation'
                      IN ('provision', 'reconcile', 'decommission')
              AND lease.lease_json->'metadata'->>'executor_last_status' = 'failed'
              AND NULLIF(BTRIM(lease.lease_json->'metadata'->>'executor_last_provider_resource_ref'), '') IS NOT NULL
              AND BTRIM(lease.lease_json->'metadata'->>'executor_last_provider_resource_ref')
                      = BTRIM(lease.engine_vm_id)
              AND BTRIM(lease.lease_json->'resource'->>'engine_vm_id')
                      = BTRIM(lease.engine_vm_id)

            UNION ALL

            SELECT
                3,
                'side_effect_claim'::text,
                lease.lease_json->'metadata'->>'executor_claim_operation',
                lease.lease_json->'metadata'->>'executor_claim_operation_id',
                lease.lease_json->'metadata'->>'executor_claim_status',
                NULL::text,
                NULL::text,
                NULL::text,
                NULL::text
            WHERE lease.lease_json->'metadata'->>'executor_claim_status' IN ('active', 'blocked')
              AND lease.lease_json->'metadata'->>'executor_claim_resource_generation_id'
                      = lease.lease_json->'metadata'->>'resource_generation_id'
              AND NULLIF(BTRIM(lease.lease_json->'metadata'->>'executor_claim_operation_id'), '') IS NOT NULL
              AND lease.lease_json->'metadata'->>'executor_claim_operation'
                      IN ('provision', 'reconcile', 'decommission')

            UNION ALL

            SELECT
                4,
                'provider_handle_triple_match'::text,
                NULL::text,
                NULL::text,
                NULL::text,
                NULL::text,
                NULLIF(BTRIM(lease.engine_vm_id), ''),
                NULLIF(BTRIM(lease.lease_json->'resource'->>'engine_vm_id'), ''),
                NULLIF(BTRIM(outbox.request_json->>'engine_vm_id'), '')
            WHERE NULLIF(BTRIM(lease.engine_vm_id), '') IS NOT NULL
              AND BTRIM(lease.lease_json->'resource'->>'engine_vm_id') = BTRIM(lease.engine_vm_id)
              AND BTRIM(outbox.request_json->>'engine_vm_id') = BTRIM(lease.engine_vm_id)
        ) AS candidate
        ORDER BY candidate.evidence_priority
        LIMIT 1
    ) AS evidence
    WHERE NULLIF(BTRIM(lease.lease_json->'metadata'->>'resource_generation_id'), '') IS NOT NULL
      AND outbox.resource_generation_id = lease.lease_json->'metadata'->>'resource_generation_id'
)
INSERT INTO runtime_lease_execution_authorities (
    tenant_id,
    lease_id,
    execution_authority,
    bound_at,
    evidence_resource_generation_id,
    evidence_json
)
SELECT
    evidence.tenant_id,
    evidence.lease_id,
    'legacy_simulate',
    now(),
    evidence.resource_generation_id,
    jsonb_strip_nulls(jsonb_build_object(
        'schema_version', 'techstack-legacy-custody-evidence/v1',
        'resource_generation_id', evidence.resource_generation_id,
        'outbox_resource_generation_id', evidence.outbox_resource_generation_id,
        'evidence_kind', evidence.evidence_kind,
        'operation', evidence.operation,
        'operation_id', evidence.operation_id,
        'claim_status', evidence.claim_status,
        'result_status', evidence.result_status,
        'provider_resource_ref', evidence.provider_resource_ref,
        'lease_resource_ref', evidence.lease_resource_ref,
        'request_resource_ref', evidence.request_resource_ref
    ))
FROM exact_evidence AS evidence
ON CONFLICT (tenant_id, lease_id) DO NOTHING;

-- Bind the immutable evidence carriers to the quarantined lease itself. This
-- marker is migration-owned and cannot be supplied by a later application
-- request. It lets the lease trigger retain generation and provider-handle
-- evidence even if application code changes independently of this table.
UPDATE techstack_vm_leases AS lease
SET lease_json = jsonb_set(
    lease.lease_json,
    '{metadata,runtime_execution_authority_provenance}',
    '"legacy_simulate"'::jsonb,
    true
)
FROM runtime_lease_execution_authorities AS authority
WHERE authority.tenant_id = lease.tenant_id
  AND authority.lease_id = lease.id
  AND authority.execution_authority = 'legacy_simulate';

ALTER TABLE techstack_vm_leases ENABLE ROW LEVEL SECURITY;
ALTER TABLE techstack_vm_leases FORCE ROW LEVEL SECURITY;

-- Every old enrollment row is non-executable after cutover. Exact evidence is
-- retained as quarantine above; rows without evidence remain unbound. Both
-- cases are terminal for automatic workers and are never reset to pending.
UPDATE techstack_vm_lease_enrollment_outbox AS outbox
SET status = 'failed',
    last_error = CASE
        WHEN EXISTS (
            SELECT 1
            FROM runtime_lease_execution_authorities AS authority
            WHERE authority.tenant_id = outbox.tenant_id
              AND authority.lease_id = outbox.lease_id
              AND authority.execution_authority = 'legacy_simulate'
        ) THEN 'legacy Simulate execution disabled: historical custody requires inventory and manual cleanup'
        ELSE 'execution authority unbound: exact generation-bound custody evidence required'
    END,
    updated_at = now()
WHERE outbox.status IN ('pending', 'retrying');

-- Quarantine is irreversible. These fences make every legacy enrollment row
-- read-only and prevent an old binary from inserting, re-claiming, completing,
-- or deleting work after the cutover transaction commits.
CREATE OR REPLACE FUNCTION techstack_vm_lease_enrollment_outbox_reject_mutation()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog
AS $$
BEGIN
    RAISE EXCEPTION 'legacy enrollment outbox is disabled: % rejected', TG_OP
        USING ERRCODE = '55000';
END;
$$;

DROP TRIGGER IF EXISTS techstack_vm_lease_enrollment_outbox_reject_mutation
    ON techstack_vm_lease_enrollment_outbox;
CREATE TRIGGER techstack_vm_lease_enrollment_outbox_reject_mutation
BEFORE INSERT OR UPDATE OR DELETE ON techstack_vm_lease_enrollment_outbox
FOR EACH ROW EXECUTE FUNCTION techstack_vm_lease_enrollment_outbox_reject_mutation();

-- Existing legacy metadata remains available for inventory and manual cleanup,
-- but it is immutable after cutover. Unrelated lease fields and unrelated
-- metadata may still change. New leases cannot introduce executor_*,
-- runtime_enrollment_*, or migration-owned provenance state that an old worker
-- could interpret as executable. Generation and provider-handle evidence is
-- frozen for every quarantined lease.
CREATE OR REPLACE FUNCTION techstack_vm_lease_reject_legacy_execution_metadata()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog
AS $$
DECLARE
    old_legacy_metadata jsonb := '{}'::jsonb;
    new_legacy_metadata jsonb := '{}'::jsonb;
    legacy_custody boolean := false;
BEGIN
    SELECT COALESCE(jsonb_object_agg(metadata.key, metadata.value), '{}'::jsonb)
    INTO new_legacy_metadata
    FROM jsonb_each(
        CASE
            WHEN jsonb_typeof(NEW.lease_json->'metadata') = 'object'
                THEN NEW.lease_json->'metadata'
            ELSE '{}'::jsonb
        END
    ) AS metadata
    WHERE metadata.key ~ '^(executor_|runtime_enrollment_)'
       OR metadata.key = 'runtime_execution_authority_provenance';

    IF TG_OP = 'INSERT' THEN
        IF new_legacy_metadata <> '{}'::jsonb THEN
            RAISE EXCEPTION 'legacy executor/enrollment metadata cannot be created after native cutover'
                USING ERRCODE = '55000';
        END IF;
        RETURN NEW;
    END IF;

    SELECT COALESCE(jsonb_object_agg(metadata.key, metadata.value), '{}'::jsonb)
    INTO old_legacy_metadata
    FROM jsonb_each(
        CASE
            WHEN jsonb_typeof(OLD.lease_json->'metadata') = 'object'
                THEN OLD.lease_json->'metadata'
            ELSE '{}'::jsonb
        END
    ) AS metadata
    WHERE metadata.key ~ '^(executor_|runtime_enrollment_)'
       OR metadata.key = 'runtime_execution_authority_provenance';

    IF new_legacy_metadata IS DISTINCT FROM old_legacy_metadata THEN
        RAISE EXCEPTION 'legacy executor/enrollment metadata cannot be changed or reactivated after native cutover'
            USING ERRCODE = '55000';
    END IF;

    legacy_custody := OLD.lease_json->'metadata'->>'runtime_execution_authority_provenance'
        = 'legacy_simulate';
    IF legacy_custody AND (
        NEW.engine_vm_id IS DISTINCT FROM OLD.engine_vm_id
        OR NEW.lease_json->'metadata'->>'resource_generation_id'
            IS DISTINCT FROM OLD.lease_json->'metadata'->>'resource_generation_id'
        OR NEW.lease_json->'resource'->>'engine_vm_id'
            IS DISTINCT FROM OLD.lease_json->'resource'->>'engine_vm_id'
    ) THEN
        RAISE EXCEPTION 'legacy custody generation/provider evidence cannot be changed or cleared'
            USING ERRCODE = '55000';
    END IF;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS techstack_vm_lease_reject_legacy_execution_metadata
    ON techstack_vm_leases;
CREATE TRIGGER techstack_vm_lease_reject_legacy_execution_metadata
BEFORE INSERT OR UPDATE OF lease_json, engine_vm_id ON techstack_vm_leases
FOR EACH ROW EXECUTE FUNCTION techstack_vm_lease_reject_legacy_execution_metadata();

CREATE OR REPLACE FUNCTION runtime_lease_execution_authority_reject_legacy_insert()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog
AS $$
BEGIN
    IF NEW.execution_authority = 'legacy_simulate' THEN
        RAISE EXCEPTION 'legacy Simulate custody can only be created by migration 019 evidence backfill'
            USING ERRCODE = '55000';
    END IF;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS runtime_lease_execution_authority_reject_legacy_insert
    ON runtime_lease_execution_authorities;
CREATE TRIGGER runtime_lease_execution_authority_reject_legacy_insert
BEFORE INSERT ON runtime_lease_execution_authorities
FOR EACH ROW EXECUTE FUNCTION runtime_lease_execution_authority_reject_legacy_insert();

CREATE OR REPLACE FUNCTION runtime_lease_execution_authority_reject_mutation()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog
AS $$
BEGIN
    RAISE EXCEPTION 'runtime lease execution authority cannot be %',
        CASE TG_OP WHEN 'UPDATE' THEN 'changed' ELSE 'deleted' END
        USING ERRCODE = '55000';
END;
$$;

DROP TRIGGER IF EXISTS runtime_lease_execution_authority_reject_mutation
    ON runtime_lease_execution_authorities;
CREATE TRIGGER runtime_lease_execution_authority_reject_mutation
BEFORE UPDATE OR DELETE ON runtime_lease_execution_authorities
FOR EACH ROW EXECUTE FUNCTION runtime_lease_execution_authority_reject_mutation();

ALTER TABLE runtime_lease_execution_authorities ENABLE ROW LEVEL SECURITY;
ALTER TABLE runtime_lease_execution_authorities FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation ON runtime_lease_execution_authorities;
CREATE POLICY tenant_isolation ON runtime_lease_execution_authorities
    USING (tenant_id = current_setting('app.tenant_id', true))
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true));
