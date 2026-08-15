-- Immutable managed-runtime capacity custody.
--
-- Capacity is reserved in the same transaction as the native lease,
-- resource-generation UUID, and provider provision operation. Reservations
-- have no release mutation in this schema: a later release slice must require
-- terminal, generation-bound provider-absence proof before it can introduce a
-- separately auditable release fact.

SET LOCAL lock_timeout = '5s';
SELECT pg_catalog.set_config(
    'search_path',
    pg_catalog.quote_ident(pg_catalog.current_schema()) || ', pg_catalog, pg_temp',
    true
);

-- The provider-control runtime role deliberately has SELECT but no UPDATE on
-- immutable lease identity and credential authority. PostgreSQL row-locking
-- clauses require UPDATE even when no mutation is attempted. Keep the final
-- claim/dispatch triggers as narrow lock/revalidation capabilities instead:
-- they run as the migration owner, have a fixed trusted search path, cannot be
-- invoked directly, and remain tenant/operation/generation/credential bound.
-- Earlier Go lookups are typed diagnostics only; these triggers are the durable
-- authority immediately before dispatch custody or a side-effecting claim can
-- commit.
DO $managed_runtime_claim_lock_boundary$
DECLARE
    active_schema text := current_schema();
    boundary_function text;
BEGIN
    FOREACH boundary_function IN ARRAY ARRAY[
        'provider_provision_dispatch_guard_validate_insert',
        'provider_execution_claim_current_head',
        'provider_execution_claim_credential_guard'
    ] LOOP
        EXECUTE format(
            'ALTER FUNCTION %I.%I() SECURITY DEFINER',
            active_schema,
            boundary_function
        );
        EXECUTE format(
            'ALTER FUNCTION %I.%I() SET search_path TO pg_catalog, %I',
            active_schema,
            boundary_function,
            active_schema
        );
        EXECUTE format(
            'REVOKE ALL ON FUNCTION %I.%I() FROM PUBLIC',
            active_schema,
            boundary_function
        );
    END LOOP;
END;
$managed_runtime_claim_lock_boundary$;

-- Stop concurrent lease/authority/operation admissions while migration-owned
-- quarantine rows are derived. After this transaction commits, the deferred
-- provider-operation constraint below prevents an older binary from admitting
-- another native provision without an atomic reservation.
LOCK TABLE techstack_vm_leases,
    runtime_lease_execution_authorities,
    provider_operations,
    provider_operation_execution_claims
    IN SHARE ROW EXCLUSIVE MODE;

CREATE TABLE IF NOT EXISTS managed_runtime_capacity_reservations (
    tenant_id text NOT NULL CHECK (BTRIM(tenant_id) <> ''),
    owner_subject_id text NOT NULL CHECK (BTRIM(owner_subject_id) <> ''),
    provider_id text NOT NULL CHECK (provider_id IN ('ionos', 'centron')),
    lease_id text NOT NULL CHECK (BTRIM(lease_id) <> ''),
    resource_generation_id uuid NOT NULL,
    operation_id text,
    admission_idempotency_key text,
    reservation_mode text NOT NULL
        CHECK (reservation_mode IN ('limited', 'unlimited', 'quarantine')),
    capacity_limit integer,
    policy_source text NOT NULL CHECK (BTRIM(policy_source) <> ''),
    policy_digest text NOT NULL
        CHECK (policy_digest ~ '^sha256:[0-9a-f]{64}$'),
    reservation_origin text NOT NULL
        CHECK (reservation_origin IN ('native_admission', 'migration_quarantine')),
    reserved_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (tenant_id, lease_id, resource_generation_id),
    UNIQUE (tenant_id, operation_id),
    FOREIGN KEY (tenant_id, lease_id)
        REFERENCES techstack_vm_leases (tenant_id, id)
        ON UPDATE RESTRICT ON DELETE RESTRICT,
    FOREIGN KEY (tenant_id, operation_id, lease_id)
        REFERENCES provider_operations (tenant_id, operation_id, lease_id)
        ON UPDATE RESTRICT ON DELETE RESTRICT
        DEFERRABLE INITIALLY DEFERRED,
    CHECK (
        (
            reservation_origin = 'native_admission'
            AND reservation_mode IN ('limited', 'unlimited')
            AND policy_source IN (
                'edge_v2_entitlement+signed_budget:cloud.runtime.credits#managed_servers',
                'static_release_manifest:selfhost-oss'
            )
            AND operation_id IS NOT NULL
            AND BTRIM(operation_id) <> ''
            AND admission_idempotency_key IS NOT NULL
            AND BTRIM(admission_idempotency_key) <> ''
        )
        OR (
            reservation_origin = 'migration_quarantine'
            AND reservation_mode = 'quarantine'
            AND policy_source = 'migration_quarantine:managed-runtime-capacity/v1'
        )
    ),
    CHECK (
        (reservation_mode = 'limited' AND capacity_limit BETWEEN 1 AND 2147483647)
        OR (reservation_mode IN ('unlimited', 'quarantine') AND capacity_limit IS NULL)
    )
);

CREATE INDEX IF NOT EXISTS idx_managed_runtime_capacity_scope
    ON managed_runtime_capacity_reservations (tenant_id, owner_subject_id);

-- Migrations run without app.tenant_id. Suspend FORCE RLS only inside this
-- transaction so the cross-tenant cost-custody scan cannot silently see zero
-- rows. Any migration failure rolls these DDL changes back as one transaction.
ALTER TABLE techstack_vm_leases NO FORCE ROW LEVEL SECURITY;
ALTER TABLE techstack_vm_leases DISABLE ROW LEVEL SECURITY;
ALTER TABLE runtime_lease_execution_authorities NO FORCE ROW LEVEL SECURITY;
ALTER TABLE runtime_lease_execution_authorities DISABLE ROW LEVEL SECURITY;
ALTER TABLE provider_operations NO FORCE ROW LEVEL SECURITY;
ALTER TABLE provider_operations DISABLE ROW LEVEL SECURITY;
ALTER TABLE provider_operation_execution_claims NO FORCE ROW LEVEL SECURITY;
ALTER TABLE provider_operation_execution_claims DISABLE ROW LEVEL SECURITY;

-- Historical cost custody is deliberately recognized from durable evidence.
-- A provider handle or execution-authority binding is sufficient even when
-- optional legacy billing metadata is missing. Metadata-only rows require an
-- active desired state. Provider aliases are accepted here for inventory
-- quarantine only; they never become executable adapter identities.
DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM techstack_vm_leases AS lease
        LEFT JOIN runtime_lease_execution_authorities AS authority
          ON authority.tenant_id = lease.tenant_id
         AND authority.lease_id = lease.id
        WHERE (
                lease.desired_state IN ('running', 'stopped')
                OR NULLIF(BTRIM(lease.engine_vm_id), '') IS NOT NULL
                OR authority.execution_authority IN ('legacy_simulate', 'techstack_provider_control')
            )
          AND (
                LOWER(BTRIM(COALESCE(lease.provider_id, ''))) IN ('ionos', 'ionos-managed')
                OR LOWER(BTRIM(COALESCE(lease.lease_json->'resource'->>'provider_id', ''))) IN ('ionos', 'ionos-managed')
                OR LOWER(BTRIM(COALESCE(lease.lease_json->'metadata'->>'provider_id', ''))) IN ('ionos', 'ionos-managed')
                OR LOWER(BTRIM(COALESCE(lease.lease_json->'metadata'->>'lease_provider', ''))) IN ('ionos', 'ionos-managed')
                OR LOWER(BTRIM(COALESCE(lease.lease_json->'metadata'->>'simulate_provider_id', ''))) IN ('ionos', 'ionos-managed')
            )
          AND (
                LOWER(BTRIM(COALESCE(lease.provider_id, ''))) IN ('centron', 'centron-managed')
                OR LOWER(BTRIM(COALESCE(lease.lease_json->'resource'->>'provider_id', ''))) IN ('centron', 'centron-managed')
                OR LOWER(BTRIM(COALESCE(lease.lease_json->'metadata'->>'provider_id', ''))) IN ('centron', 'centron-managed')
                OR LOWER(BTRIM(COALESCE(lease.lease_json->'metadata'->>'lease_provider', ''))) IN ('centron', 'centron-managed')
                OR LOWER(BTRIM(COALESCE(lease.lease_json->'metadata'->>'simulate_provider_id', ''))) IN ('centron', 'centron-managed')
            )
    ) THEN
        RAISE EXCEPTION
            'managed runtime capacity backfill found conflicting IONOS and Centron custody identity'
            USING ERRCODE = '55000';
    END IF;

    IF EXISTS (
        SELECT 1
        FROM techstack_vm_leases AS lease
        LEFT JOIN runtime_lease_execution_authorities AS authority
          ON authority.tenant_id = lease.tenant_id
         AND authority.lease_id = lease.id
        WHERE (
                NULLIF(BTRIM(lease.engine_vm_id), '') IS NOT NULL
                OR authority.execution_authority IN ('legacy_simulate', 'techstack_provider_control')
                OR (
                    lease.desired_state IN ('running', 'stopped')
                    AND (
                        LOWER(BTRIM(COALESCE(lease.lease_json->>'billing_mode', ''))) = 'subscription'
                        OR LOWER(BTRIM(COALESCE(lease.lease_json->>'lifecycle_class', ''))) = 'subscription'
                        OR LOWER(BTRIM(COALESCE(lease.lease_json->'metadata'->>'runtime_lane', ''))) = 'monthly'
                        OR LOWER(BTRIM(COALESCE(lease.lease_json->'metadata'->>'server_mode', ''))) IN (
                            'monthly', 'managed-cloud'
                        )
                        OR LOWER(BTRIM(COALESCE(lease.lease_json->'metadata'->>'billing_mode', ''))) = 'subscription'
                    )
                )
            )
          AND NOT (
                LOWER(BTRIM(COALESCE(lease.provider_id, ''))) IN (
                    'centron', 'ionos', 'centron-managed', 'ionos-managed'
                )
                OR LOWER(BTRIM(COALESCE(lease.lease_json->'resource'->>'provider_id', ''))) IN (
                    'centron', 'ionos', 'centron-managed', 'ionos-managed'
                )
                OR LOWER(BTRIM(COALESCE(lease.lease_json->'metadata'->>'provider_id', ''))) IN (
                    'centron', 'ionos', 'centron-managed', 'ionos-managed'
                )
                OR LOWER(BTRIM(COALESCE(lease.lease_json->'metadata'->>'lease_provider', ''))) IN (
                    'centron', 'ionos', 'centron-managed', 'ionos-managed'
                )
                OR LOWER(BTRIM(COALESCE(lease.lease_json->'metadata'->>'simulate_provider_id', ''))) IN (
                    'centron', 'ionos', 'centron-managed', 'ionos-managed'
                )
            )
    ) THEN
        RAISE EXCEPTION
            'managed runtime capacity backfill found externally cost-bearing custody without exact IONOS or Centron identity'
            USING ERRCODE = '55000';
    END IF;

    IF EXISTS (
        SELECT 1
        FROM techstack_vm_leases AS lease
        LEFT JOIN runtime_lease_execution_authorities AS authority
          ON authority.tenant_id = lease.tenant_id
         AND authority.lease_id = lease.id
        WHERE (
                LOWER(BTRIM(COALESCE(lease.provider_id, ''))) IN (
                    'centron', 'ionos', 'centron-managed', 'ionos-managed'
                )
                OR LOWER(BTRIM(COALESCE(lease.lease_json->'resource'->>'provider_id', ''))) IN (
                    'centron', 'ionos', 'centron-managed', 'ionos-managed'
                )
                OR LOWER(BTRIM(COALESCE(lease.lease_json->'metadata'->>'provider_id', ''))) IN (
                    'centron', 'ionos', 'centron-managed', 'ionos-managed'
                )
                OR LOWER(BTRIM(COALESCE(lease.lease_json->'metadata'->>'lease_provider', ''))) IN (
                    'centron', 'ionos', 'centron-managed', 'ionos-managed'
                )
                OR LOWER(BTRIM(COALESCE(lease.lease_json->'metadata'->>'simulate_provider_id', ''))) IN (
                    'centron', 'ionos', 'centron-managed', 'ionos-managed'
                )
            )
          AND (
                lease.desired_state IN ('running', 'stopped')
                OR NULLIF(BTRIM(lease.engine_vm_id), '') IS NOT NULL
                OR authority.execution_authority IN ('legacy_simulate', 'techstack_provider_control')
            )
          AND (
                NULLIF(BTRIM(lease.tenant_id), '') IS NULL
                OR NULLIF(BTRIM(lease.owner_subject_id), '') IS NULL
                OR lease.resource_generation_id IS NULL
            )
    ) THEN
        RAISE EXCEPTION
            'managed runtime capacity backfill found cost custody without exact tenant, owner, or resource generation'
            USING ERRCODE = '55000';
    END IF;

    IF EXISTS (
        SELECT 1
        FROM techstack_vm_leases AS lease
        JOIN provider_operations AS operation
          ON operation.tenant_id = lease.tenant_id
         AND operation.lease_id = lease.id
         AND operation.operation = 'provision'
         AND operation.command_json->>'schema_version' = 'techstack.provider-control-operation/v1'
         AND operation.command_json->>'execution_authority' = 'techstack_provider_control'
         AND operation.command_json #>> '{command,resource_generation_id}'
                = lease.resource_generation_id::text
        WHERE (
                LOWER(BTRIM(COALESCE(lease.provider_id, ''))) IN (
                    'centron', 'ionos', 'centron-managed', 'ionos-managed'
                )
                OR LOWER(BTRIM(COALESCE(lease.lease_json->'resource'->>'provider_id', ''))) IN (
                    'centron', 'ionos', 'centron-managed', 'ionos-managed'
                )
                OR LOWER(BTRIM(COALESCE(lease.lease_json->'metadata'->>'provider_id', ''))) IN (
                    'centron', 'ionos', 'centron-managed', 'ionos-managed'
                )
                OR LOWER(BTRIM(COALESCE(lease.lease_json->'metadata'->>'lease_provider', ''))) IN (
                    'centron', 'ionos', 'centron-managed', 'ionos-managed'
                )
                OR LOWER(BTRIM(COALESCE(lease.lease_json->'metadata'->>'simulate_provider_id', ''))) IN (
                    'centron', 'ionos', 'centron-managed', 'ionos-managed'
                )
            )
        GROUP BY lease.tenant_id, lease.id, lease.resource_generation_id
        HAVING COUNT(*) > 1
    ) THEN
        RAISE EXCEPTION
            'managed runtime capacity backfill found multiple native provision operations for one resource generation'
            USING ERRCODE = '55000';
    END IF;
END;
$$;

-- This cutover is not rolling across an already issued adapter capability.
-- The table lock above prevents new claims while the guard is installed; an
-- unexpired historical provision/reconcile claim requires the deployment
-- egress kill-switch and claim drain to complete before this migration runs.
DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM provider_operation_execution_claims AS claim
        JOIN provider_operations AS operation
          ON operation.tenant_id = claim.tenant_id
         AND operation.operation_id = claim.operation_id
        JOIN techstack_vm_leases AS lease
          ON lease.tenant_id = operation.tenant_id
         AND lease.id = operation.lease_id
        WHERE claim.state = 'active'
          AND claim.lease_expires_at > clock_timestamp()
          AND operation.operation IN ('provision', 'reconcile')
          AND (
                LOWER(BTRIM(COALESCE(lease.provider_id, ''))) IN (
                    'centron', 'ionos', 'centron-managed', 'ionos-managed'
                )
                OR LOWER(BTRIM(COALESCE(lease.lease_json->'resource'->>'provider_id', ''))) IN (
                    'centron', 'ionos', 'centron-managed', 'ionos-managed'
                )
                OR LOWER(BTRIM(COALESCE(lease.lease_json->'metadata'->>'provider_id', ''))) IN (
                    'centron', 'ionos', 'centron-managed', 'ionos-managed'
                )
                OR LOWER(BTRIM(COALESCE(lease.lease_json->'metadata'->>'lease_provider', ''))) IN (
                    'centron', 'ionos', 'centron-managed', 'ionos-managed'
                )
                OR LOWER(BTRIM(COALESCE(lease.lease_json->'metadata'->>'simulate_provider_id', ''))) IN (
                    'centron', 'ionos', 'centron-managed', 'ionos-managed'
                )
            )
    ) THEN
        RAISE EXCEPTION
            'managed runtime capacity cutover requires all provider execution claims to be drained'
            USING ERRCODE = '55000';
    END IF;
END;
$$;

INSERT INTO managed_runtime_capacity_reservations (
    tenant_id,
    owner_subject_id,
    provider_id,
    lease_id,
    resource_generation_id,
    operation_id,
    admission_idempotency_key,
    reservation_mode,
    capacity_limit,
    policy_source,
    policy_digest,
    reservation_origin,
    reserved_at
)
SELECT
    lease.tenant_id,
    lease.owner_subject_id,
    CASE
        WHEN LOWER(BTRIM(COALESCE(lease.provider_id, ''))) IN ('ionos', 'ionos-managed')
          OR LOWER(BTRIM(COALESCE(lease.lease_json->'resource'->>'provider_id', ''))) IN ('ionos', 'ionos-managed')
          OR LOWER(BTRIM(COALESCE(lease.lease_json->'metadata'->>'provider_id', ''))) IN ('ionos', 'ionos-managed')
          OR LOWER(BTRIM(COALESCE(lease.lease_json->'metadata'->>'lease_provider', ''))) IN ('ionos', 'ionos-managed')
          OR LOWER(BTRIM(COALESCE(lease.lease_json->'metadata'->>'simulate_provider_id', ''))) IN ('ionos', 'ionos-managed')
        THEN 'ionos'
        ELSE 'centron'
    END,
    lease.id,
    lease.resource_generation_id,
    exact_operation.operation_id,
    COALESCE(exact_operation.idempotency_key, lease.idempotency_key),
    'quarantine',
    NULL,
    'migration_quarantine:managed-runtime-capacity/v1',
    'sha256:' || encode(
        sha256(convert_to(
            'managed-runtime-capacity-quarantine/v1' || E'\n'
            || lease.tenant_id || E'\n'
            || lease.owner_subject_id || E'\n'
            || lease.id || E'\n'
            || lease.resource_generation_id::text || E'\n'
            || COALESCE(exact_operation.operation_id, '') || E'\n'
            || LOWER(BTRIM(COALESCE(lease.provider_id, ''))) || E'\n'
            || LOWER(BTRIM(COALESCE(lease.lease_json->'resource'->>'provider_id', ''))) || E'\n'
            || LOWER(BTRIM(COALESCE(lease.lease_json->'metadata'->>'provider_id', ''))) || E'\n'
            || LOWER(BTRIM(COALESCE(lease.lease_json->'metadata'->>'lease_provider', ''))) || E'\n'
            || LOWER(BTRIM(COALESCE(lease.lease_json->'metadata'->>'simulate_provider_id', ''))) || E'\n'
            || BTRIM(COALESCE(lease.engine_vm_id, '')) || E'\n'
            || COALESCE(authority.execution_authority, '') || E'\n'
            || COALESCE(lease.desired_state, '') || E'\n'
            || LOWER(BTRIM(COALESCE(lease.lease_json->>'billing_mode', ''))) || E'\n'
            || LOWER(BTRIM(COALESCE(lease.lease_json->>'lifecycle_class', ''))),
            'UTF8'
        )),
        'hex'
    ),
    'migration_quarantine',
    COALESCE(exact_operation.created_at, lease.created_at)
FROM techstack_vm_leases AS lease
LEFT JOIN runtime_lease_execution_authorities AS authority
  ON authority.tenant_id = lease.tenant_id
 AND authority.lease_id = lease.id
LEFT JOIN LATERAL (
    SELECT operation.operation_id, operation.idempotency_key, operation.created_at
    FROM provider_operations AS operation
    WHERE operation.tenant_id = lease.tenant_id
      AND operation.lease_id = lease.id
      AND operation.operation = 'provision'
      AND operation.command_json->>'schema_version' = 'techstack.provider-control-operation/v1'
      AND operation.command_json->>'execution_authority' = 'techstack_provider_control'
      AND operation.command_json #>> '{command,resource_generation_id}'
            = lease.resource_generation_id::text
    ORDER BY operation.created_at, operation.operation_id
    LIMIT 1
) AS exact_operation ON true
WHERE (
        LOWER(BTRIM(COALESCE(lease.provider_id, ''))) IN (
            'centron', 'ionos', 'centron-managed', 'ionos-managed'
        )
        OR LOWER(BTRIM(COALESCE(lease.lease_json->'resource'->>'provider_id', ''))) IN (
            'centron', 'ionos', 'centron-managed', 'ionos-managed'
        )
        OR LOWER(BTRIM(COALESCE(lease.lease_json->'metadata'->>'provider_id', ''))) IN (
            'centron', 'ionos', 'centron-managed', 'ionos-managed'
        )
        OR LOWER(BTRIM(COALESCE(lease.lease_json->'metadata'->>'lease_provider', ''))) IN (
            'centron', 'ionos', 'centron-managed', 'ionos-managed'
        )
        OR LOWER(BTRIM(COALESCE(lease.lease_json->'metadata'->>'simulate_provider_id', ''))) IN (
            'centron', 'ionos', 'centron-managed', 'ionos-managed'
        )
    )
  AND (
        lease.desired_state IN ('running', 'stopped')
        OR NULLIF(BTRIM(lease.engine_vm_id), '') IS NOT NULL
        OR authority.execution_authority IN ('legacy_simulate', 'techstack_provider_control')
    )
  AND NULLIF(BTRIM(lease.tenant_id), '') IS NOT NULL
  AND NULLIF(BTRIM(lease.owner_subject_id), '') IS NOT NULL
  AND lease.resource_generation_id IS NOT NULL
ON CONFLICT (tenant_id, lease_id, resource_generation_id) DO NOTHING;

-- The quarantine insert queues the deferrable operation FK even when the
-- historical row has no operation. Drain those events before ALTER TABLE;
-- PostgreSQL otherwise rejects the RLS restoration below with SQLSTATE 55006.
SET CONSTRAINTS ALL IMMEDIATE;

ALTER TABLE provider_operation_execution_claims ENABLE ROW LEVEL SECURITY;
ALTER TABLE provider_operation_execution_claims FORCE ROW LEVEL SECURITY;
ALTER TABLE provider_operations ENABLE ROW LEVEL SECURITY;
ALTER TABLE provider_operations FORCE ROW LEVEL SECURITY;
ALTER TABLE runtime_lease_execution_authorities ENABLE ROW LEVEL SECURITY;
ALTER TABLE runtime_lease_execution_authorities FORCE ROW LEVEL SECURITY;
ALTER TABLE techstack_vm_leases ENABLE ROW LEVEL SECURITY;
ALTER TABLE techstack_vm_leases FORCE ROW LEVEL SECURITY;

-- Migration-owned quarantine rows were inserted before this trigger exists.
-- Every later insert is a fresh native admission and must bind the exact live
-- lease/owner/UUID, initial provision operation, idempotency key, and immutable
-- TechStack provider-control authority.
CREATE OR REPLACE FUNCTION managed_runtime_capacity_policy_digest(
    tenant_id text,
    provider_id text,
    scope_kind text,
    scope_id text,
    reservation_mode text,
    capacity_limit integer,
    policy_source text
)
RETURNS text
LANGUAGE sql
IMMUTABLE
STRICT
SET search_path = pg_catalog
AS $$
    SELECT 'sha256:' || encode(
        sha256(convert_to(
            octet_length('providercontrol/managed-runtime-capacity-policy/v2')::text
                || ':providercontrol/managed-runtime-capacity-policy/v2' || E'\n'
            || octet_length(tenant_id)::text || ':' || tenant_id || E'\n'
            || octet_length(provider_id)::text || ':' || provider_id || E'\n'
            || octet_length(scope_kind)::text || ':' || scope_kind || E'\n'
            || octet_length(scope_id)::text || ':' || scope_id || E'\n'
            || octet_length(reservation_mode)::text || ':' || reservation_mode || E'\n'
            || octet_length(capacity_limit::text)::text || ':' || capacity_limit::text || E'\n'
            || octet_length(policy_source)::text || ':' || policy_source || E'\n',
            'UTF8'
        )),
        'hex'
    )
$$;

CREATE OR REPLACE FUNCTION managed_runtime_capacity_reservation_validate_insert()
RETURNS trigger
LANGUAGE plpgsql
SET search_path FROM CURRENT
AS $$
DECLARE
    operation_kind text;
    operation_status text;
    operation_phase text;
    operation_head_sequence bigint;
    operation_idempotency_key text;
    operation_schema_version text;
    operation_execution_authority text;
    operation_generation_id text;
    operation_provider_id text;
    live_owner_subject_id text;
    live_provider_id text;
    live_resource_generation_id uuid;
    live_execution_authority text;
    held_capacity integer;
    expected_policy_digest text;
BEGIN
    IF NEW.reservation_origin IS DISTINCT FROM 'native_admission'
       OR NEW.reservation_mode NOT IN ('limited', 'unlimited')
       OR NEW.operation_id IS NULL THEN
        RAISE EXCEPTION 'new managed runtime capacity reservation must be native admission custody'
            USING ERRCODE = '55000';
    END IF;

    SELECT
        operation.operation,
        operation.status,
        operation.phase,
        operation.head_sequence,
        operation.idempotency_key,
        operation.command_json->>'schema_version',
        operation.command_json->>'execution_authority',
        operation.command_json #>> '{command,resource_generation_id}',
        operation.command_json #>> '{command,provider_id}'
    INTO
        operation_kind,
        operation_status,
        operation_phase,
        operation_head_sequence,
        operation_idempotency_key,
        operation_schema_version,
        operation_execution_authority,
        operation_generation_id,
        operation_provider_id
    FROM provider_operations AS operation
    WHERE operation.tenant_id = NEW.tenant_id
      AND operation.operation_id = NEW.operation_id
      AND operation.lease_id = NEW.lease_id;

    IF NOT FOUND
       OR operation_kind IS DISTINCT FROM 'provision'
       OR operation_status IS DISTINCT FROM 'pending'
       OR operation_phase IS DISTINCT FROM 'requested'
       OR operation_head_sequence IS DISTINCT FROM 1
       OR operation_idempotency_key IS DISTINCT FROM NEW.admission_idempotency_key
       OR operation_schema_version IS DISTINCT FROM 'techstack.provider-control-operation/v1'
       OR operation_execution_authority IS DISTINCT FROM 'techstack_provider_control'
       OR operation_generation_id IS DISTINCT FROM NEW.resource_generation_id::text
       OR operation_provider_id IS DISTINCT FROM NEW.provider_id THEN
        RAISE EXCEPTION 'capacity reservation must bind the initial native provision operation'
            USING ERRCODE = '55000';
    END IF;

    SELECT
        lease.owner_subject_id,
        COALESCE(
            NULLIF(LOWER(BTRIM(lease.provider_id)), ''),
            NULLIF(LOWER(BTRIM(lease.lease_json->'resource'->>'provider_id')), '')
        ),
        lease.resource_generation_id,
        authority.execution_authority
    INTO
        live_owner_subject_id,
        live_provider_id,
        live_resource_generation_id,
        live_execution_authority
    FROM techstack_vm_leases AS lease
    JOIN runtime_lease_execution_authorities AS authority
      ON authority.tenant_id = lease.tenant_id
     AND authority.lease_id = lease.id
    WHERE lease.tenant_id = NEW.tenant_id
      AND lease.id = NEW.lease_id;

    IF NOT FOUND
       OR live_owner_subject_id IS DISTINCT FROM NEW.owner_subject_id
       OR live_provider_id IS DISTINCT FROM NEW.provider_id
       OR live_resource_generation_id IS DISTINCT FROM NEW.resource_generation_id
       OR live_execution_authority IS DISTINCT FROM 'techstack_provider_control' THEN
        RAISE EXCEPTION 'capacity reservation must bind the live native lease owner and generation'
            USING ERRCODE = '55000';
    END IF;

    -- The application performs the same lock/count for early typed denial.
    -- Repeating it here makes the tenant+owner ceiling authoritative even for
    -- a direct INSERT-capable runtime writer.
    PERFORM pg_advisory_xact_lock(
        hashtext(NEW.tenant_id),
        hashtext('providercontrol.capacity:owner_subject:' || NEW.owner_subject_id)
    );
    IF NEW.reservation_mode = 'limited' THEN
        SELECT count(*)
        INTO held_capacity
        FROM managed_runtime_capacity_reservations AS reservation
        WHERE reservation.tenant_id = NEW.tenant_id
          AND reservation.owner_subject_id = NEW.owner_subject_id;
        IF held_capacity >= NEW.capacity_limit THEN
            RAISE EXCEPTION 'managed runtime capacity reservation exceeds the authoritative owner limit'
                USING ERRCODE = '55000';
        END IF;
    END IF;

    expected_policy_digest := managed_runtime_capacity_policy_digest(
        NEW.tenant_id,
        NEW.provider_id,
        'owner_subject',
        NEW.owner_subject_id,
        NEW.reservation_mode,
        COALESCE(NEW.capacity_limit, 0),
        NEW.policy_source
    );
    IF NEW.policy_digest IS DISTINCT FROM expected_policy_digest THEN
        RAISE EXCEPTION 'managed runtime capacity policy digest does not match its canonical projection'
            USING ERRCODE = '55000';
    END IF;
    RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION managed_runtime_capacity_reservation_reject_mutation()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog
AS $$
BEGIN
    RAISE EXCEPTION 'managed runtime capacity reservations are immutable'
        USING ERRCODE = '55000';
END;
$$;

DROP TRIGGER IF EXISTS managed_runtime_capacity_reservations_validate_insert
    ON managed_runtime_capacity_reservations;
CREATE TRIGGER managed_runtime_capacity_reservations_validate_insert
BEFORE INSERT ON managed_runtime_capacity_reservations
FOR EACH ROW EXECUTE FUNCTION managed_runtime_capacity_reservation_validate_insert();

DROP TRIGGER IF EXISTS managed_runtime_capacity_reservations_reject_mutation
    ON managed_runtime_capacity_reservations;
CREATE TRIGGER managed_runtime_capacity_reservations_reject_mutation
BEFORE UPDATE OR DELETE ON managed_runtime_capacity_reservations
FOR EACH ROW EXECUTE FUNCTION managed_runtime_capacity_reservation_reject_mutation();

ALTER TABLE managed_runtime_capacity_reservations ENABLE ROW LEVEL SECURITY;
ALTER TABLE managed_runtime_capacity_reservations FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation ON managed_runtime_capacity_reservations;
CREATE POLICY tenant_isolation ON managed_runtime_capacity_reservations
    USING (tenant_id = current_setting('app.tenant_id', true))
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true));

-- After the stopped-lane cutover, this deferred constraint closes the old
-- writer database gap. It is not a rolling-deployment fence and cannot revoke
-- a side effect already buffered outside PostgreSQL. An old binary cannot
-- commit a fresh native provision operation unless the same transaction also
-- inserted the exact reservation validated above.
CREATE OR REPLACE FUNCTION provider_operation_require_capacity_reservation()
RETURNS trigger
LANGUAGE plpgsql
SET search_path FROM CURRENT
AS $$
DECLARE
    operation_generation_id text;
BEGIN
    IF NEW.operation IS DISTINCT FROM 'provision'
       OR NEW.command_json->>'schema_version' IS DISTINCT FROM 'techstack.provider-control-operation/v1'
       OR NEW.command_json->>'execution_authority' IS DISTINCT FROM 'techstack_provider_control' THEN
        RETURN NEW;
    END IF;

    operation_generation_id := NEW.command_json #>> '{command,resource_generation_id}';
    PERFORM 1
    FROM managed_runtime_capacity_reservations AS reservation
    WHERE reservation.tenant_id = NEW.tenant_id
      AND reservation.lease_id = NEW.lease_id
      AND reservation.operation_id = NEW.operation_id
      AND reservation.resource_generation_id::text = operation_generation_id
      AND reservation.reservation_origin = 'native_admission'
      AND reservation.reservation_mode IN ('limited', 'unlimited')
      AND reservation.policy_source IN (
          'edge_v2_entitlement+signed_budget:cloud.runtime.credits#managed_servers',
          'static_release_manifest:selfhost-oss'
      );
    IF NOT FOUND THEN
        RAISE EXCEPTION 'native provision operation requires an atomic managed runtime capacity reservation'
            USING ERRCODE = '55000';
    END IF;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS provider_operations_require_capacity_reservation
    ON provider_operations;
CREATE CONSTRAINT TRIGGER provider_operations_require_capacity_reservation
AFTER INSERT ON provider_operations
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION provider_operation_require_capacity_reservation();

-- Historical quarantine is a counting hold and inventory fact, never an
-- execution grant. Revalidate this at the last durable capability boundary so
-- neither replay nor a direct runtime-role claim can activate a quarantined
-- provision generation. Reconcile is also side-effecting and therefore needs
-- the generation's original native reservation; decommission remains allowed
-- so exact-handle cleanup of quarantined resources is not prevented.
CREATE OR REPLACE FUNCTION managed_runtime_capacity_execution_claim_guard()
RETURNS trigger
LANGUAGE plpgsql
SET search_path FROM CURRENT
AS $$
DECLARE
    operation_kind text;
    operation_lease_id text;
    operation_schema_version text;
    operation_execution_authority text;
    operation_generation_id text;
    operation_provider_id text;
    operation_profile_provider_id text;
BEGIN
    IF NEW.state IS DISTINCT FROM 'active' THEN
        RETURN NEW;
    END IF;

    SELECT
        operation.operation,
        operation.lease_id,
        operation.command_json->>'schema_version',
        operation.command_json->>'execution_authority',
        operation.command_json #>> '{command,resource_generation_id}',
        operation.command_json #>> '{command,provider_id}',
        operation.command_json #>> '{execution_profile,provider_id}'
    INTO
        operation_kind,
        operation_lease_id,
        operation_schema_version,
        operation_execution_authority,
        operation_generation_id,
        operation_provider_id,
        operation_profile_provider_id
    FROM provider_operations AS operation
    WHERE operation.tenant_id = NEW.tenant_id
      AND operation.operation_id = NEW.operation_id;

    IF NOT FOUND THEN
        RAISE EXCEPTION 'provider execution claim has no durable operation'
            USING ERRCODE = '55000';
    END IF;
    IF operation_kind NOT IN ('provision', 'reconcile') THEN
        RETURN NEW;
    END IF;
    IF operation_schema_version IS DISTINCT FROM 'techstack.provider-control-operation/v1'
       OR operation_execution_authority IS DISTINCT FROM 'techstack_provider_control'
       OR operation_provider_id NOT IN ('ionos', 'centron')
       OR operation_profile_provider_id IS DISTINCT FROM operation_provider_id THEN
        RAISE EXCEPTION 'provider execution claim is blocked by missing native managed runtime capacity authority'
            USING ERRCODE = '55000';
    END IF;

    PERFORM 1
    FROM managed_runtime_capacity_reservations AS reservation
    WHERE reservation.tenant_id = NEW.tenant_id
      AND reservation.lease_id = operation_lease_id
      AND reservation.resource_generation_id::text = operation_generation_id
      AND reservation.provider_id = operation_provider_id
      AND reservation.reservation_origin = 'native_admission'
      AND reservation.reservation_mode IN ('limited', 'unlimited')
      AND reservation.policy_source IN (
          'edge_v2_entitlement+signed_budget:cloud.runtime.credits#managed_servers',
          'static_release_manifest:selfhost-oss'
      )
      AND (
          operation_kind <> 'provision'
          OR reservation.operation_id = NEW.operation_id
      );
    IF NOT FOUND THEN
        RAISE EXCEPTION 'provider execution claim is blocked by missing native managed runtime capacity authority'
            USING ERRCODE = '55000';
    END IF;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS managed_runtime_capacity_execution_claim_guard
    ON provider_operation_execution_claims;
CREATE TRIGGER managed_runtime_capacity_execution_claim_guard
BEFORE INSERT OR UPDATE ON provider_operation_execution_claims
FOR EACH ROW EXECUTE FUNCTION managed_runtime_capacity_execution_claim_guard();

REVOKE ALL ON FUNCTION managed_runtime_capacity_reservation_validate_insert() FROM PUBLIC;
REVOKE ALL ON FUNCTION managed_runtime_capacity_reservation_reject_mutation() FROM PUBLIC;
REVOKE ALL ON FUNCTION provider_operation_require_capacity_reservation() FROM PUBLIC;
REVOKE ALL ON FUNCTION managed_runtime_capacity_execution_claim_guard() FROM PUBLIC;

COMMENT ON TABLE managed_runtime_capacity_reservations IS
    'Immutable tenant/owner capacity custody bound to one managed runtime lease resource generation.';
COMMENT ON COLUMN managed_runtime_capacity_reservations.reservation_mode IS
    'limited and unlimited are native policy decisions; quarantine conservatively counts historical custody.';
COMMENT ON COLUMN managed_runtime_capacity_reservations.provider_id IS
    'Canonical managed provider identity bound into the capacity policy digest.';
COMMENT ON COLUMN managed_runtime_capacity_reservations.policy_source IS
    'Closed native policy authority or migration quarantine origin; quarantine never authorizes execution.';
COMMENT ON COLUMN managed_runtime_capacity_reservations.policy_digest IS
    'Immutable digest of the service-owned capacity policy, or deterministic migration quarantine identity.';
