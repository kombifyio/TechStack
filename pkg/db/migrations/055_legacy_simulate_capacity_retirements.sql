-- Immutable retirement facts for capacity quarantines created from the legacy
-- fake-only Simulate provider path.
--
-- These facts do not claim provider absence. They prove the narrower statement
-- that the quarantined generation could not have dispatched a real provider
-- mutation: the lease is explicitly marked as a PVM simulation and has no
-- provider-control authority, operation, resource binding, or provider handle.
-- Ambiguous quarantine rows remain held.

SET LOCAL lock_timeout = '5s';
SELECT pg_catalog.set_config(
    'search_path',
    pg_catalog.quote_ident(pg_catalog.current_schema()) || ', pg_catalog, pg_temp',
    true
);

LOCK TABLE techstack_vm_leases,
    runtime_lease_execution_authorities,
    managed_runtime_capacity_reservations,
    provider_operations,
    provider_operation_resources,
    servers,
    server_provider_resource_bindings
    IN SHARE ROW EXCLUSIVE MODE;

CREATE TABLE IF NOT EXISTS managed_runtime_capacity_quarantine_retirements (
    tenant_id text NOT NULL CHECK (BTRIM(tenant_id) <> ''),
    lease_id text NOT NULL CHECK (BTRIM(lease_id) <> ''),
    resource_generation_id uuid NOT NULL,
    provider_id text NOT NULL CHECK (provider_id IN ('ionos', 'centron')),
    retirement_authority text NOT NULL
        CHECK (retirement_authority = 'legacy_simulate_no_provider_dispatch'),
    simulate_provider_id text NOT NULL
        CHECK (simulate_provider_id IN ('ionos-managed', 'centron-managed')),
    simulate_node_lifecycle text NOT NULL
        CHECK (simulate_node_lifecycle = 'pvm'),
    scenario_id text NOT NULL CHECK (BTRIM(scenario_id) <> ''),
    evidence_digest text NOT NULL
        CHECK (evidence_digest ~ '^sha256:[0-9a-f]{64}$'),
    retired_at timestamptz NOT NULL,
    PRIMARY KEY (tenant_id, lease_id, resource_generation_id),
    FOREIGN KEY (tenant_id, lease_id, resource_generation_id)
        REFERENCES managed_runtime_capacity_reservations (
            tenant_id,
            lease_id,
            resource_generation_id
        )
        ON UPDATE RESTRICT ON DELETE RESTRICT
);

CREATE OR REPLACE FUNCTION managed_runtime_capacity_quarantine_retirement_digest(
    tenant_id text,
    lease_id text,
    resource_generation_id uuid,
    provider_id text,
    simulate_provider_id text,
    simulate_node_lifecycle text,
    scenario_id text
)
RETURNS text
LANGUAGE sql
IMMUTABLE
STRICT
SET search_path = pg_catalog
AS $$
    SELECT 'sha256:' || encode(
        sha256(convert_to(
            octet_length('providercontrol/legacy-simulate-capacity-retirement/v1')::text
                || ':providercontrol/legacy-simulate-capacity-retirement/v1' || E'\n'
            || octet_length(tenant_id)::text || ':' || tenant_id || E'\n'
            || octet_length(lease_id)::text || ':' || lease_id || E'\n'
            || octet_length(resource_generation_id::text)::text || ':'
                || resource_generation_id::text || E'\n'
            || octet_length(provider_id)::text || ':' || provider_id || E'\n'
            || octet_length(simulate_provider_id)::text || ':' || simulate_provider_id || E'\n'
            || octet_length(simulate_node_lifecycle)::text || ':'
                || simulate_node_lifecycle || E'\n'
            || octet_length(scenario_id)::text || ':' || scenario_id || E'\n',
            'UTF8'
        )),
        'hex'
    )
$$;

CREATE OR REPLACE FUNCTION managed_runtime_capacity_quarantine_retirement_validate_insert()
RETURNS trigger
LANGUAGE plpgsql
SET search_path FROM CURRENT
AS $$
DECLARE
    reservation_provider_id text;
    reservation_origin text;
    reservation_mode text;
    reservation_operation_id text;
    lease_engine_vm_id text;
    lease_simulate_provider_id text;
    lease_simulate_node_lifecycle text;
    lease_scenario_id text;
    expected_digest text;
BEGIN
    SELECT
        reservation.provider_id,
        reservation.reservation_origin,
        reservation.reservation_mode,
        reservation.operation_id,
        lease.engine_vm_id,
        lease.lease_json->'metadata'->>'simulate_provider_id',
        lease.lease_json->'metadata'->>'simulate_node_lifecycle',
        lease.lease_json->'metadata'->>'scenario_id'
    INTO
        reservation_provider_id,
        reservation_origin,
        reservation_mode,
        reservation_operation_id,
        lease_engine_vm_id,
        lease_simulate_provider_id,
        lease_simulate_node_lifecycle,
        lease_scenario_id
    FROM managed_runtime_capacity_reservations AS reservation
    JOIN techstack_vm_leases AS lease
      ON lease.tenant_id = reservation.tenant_id
     AND lease.id = reservation.lease_id
     AND lease.resource_generation_id = reservation.resource_generation_id
    WHERE reservation.tenant_id = NEW.tenant_id
      AND reservation.lease_id = NEW.lease_id
      AND reservation.resource_generation_id = NEW.resource_generation_id;

    IF NOT FOUND
       OR reservation_origin IS DISTINCT FROM 'migration_quarantine'
       OR reservation_mode IS DISTINCT FROM 'quarantine'
       OR reservation_operation_id IS NOT NULL
       OR reservation_provider_id IS DISTINCT FROM NEW.provider_id
       OR NEW.retirement_authority IS DISTINCT FROM 'legacy_simulate_no_provider_dispatch'
       OR NULLIF(BTRIM(lease_engine_vm_id), '') IS NOT NULL
       OR lease_simulate_provider_id IS DISTINCT FROM NEW.simulate_provider_id
       OR lease_simulate_provider_id IS DISTINCT FROM NEW.provider_id || '-managed'
       OR lease_simulate_node_lifecycle IS DISTINCT FROM NEW.simulate_node_lifecycle
       OR lease_simulate_node_lifecycle IS DISTINCT FROM 'pvm'
       OR lease_scenario_id IS DISTINCT FROM NEW.scenario_id
       OR lease_scenario_id NOT LIKE '%:' || lease_simulate_provider_id
       OR EXISTS (
            SELECT 1
            FROM runtime_lease_execution_authorities AS authority
            WHERE authority.tenant_id = NEW.tenant_id
              AND authority.lease_id = NEW.lease_id
       )
       OR EXISTS (
            SELECT 1
            FROM provider_operations AS operation
            WHERE operation.tenant_id = NEW.tenant_id
              AND operation.lease_id = NEW.lease_id
       )
       OR EXISTS (
            SELECT 1
            FROM servers AS server
            JOIN server_provider_resource_bindings AS binding
              ON binding.tenant_id = server.tenant_id
             AND binding.server_id = server.id
            WHERE server.tenant_id = NEW.tenant_id
              AND server.lease_id = NEW.lease_id
       ) THEN
        RAISE EXCEPTION 'capacity quarantine retirement lacks exact legacy Simulate custody'
            USING ERRCODE = '55000';
    END IF;

    expected_digest := managed_runtime_capacity_quarantine_retirement_digest(
        NEW.tenant_id,
        NEW.lease_id,
        NEW.resource_generation_id,
        NEW.provider_id,
        NEW.simulate_provider_id,
        NEW.simulate_node_lifecycle,
        NEW.scenario_id
    );
    IF NEW.evidence_digest IS DISTINCT FROM expected_digest THEN
        RAISE EXCEPTION 'capacity quarantine retirement evidence digest mismatch'
            USING ERRCODE = '55000';
    END IF;
    RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION managed_runtime_capacity_quarantine_retirement_reject_mutation()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog
AS $$
BEGIN
    RAISE EXCEPTION 'managed runtime capacity quarantine retirements are immutable'
        USING ERRCODE = '55000';
END;
$$;

DROP TRIGGER IF EXISTS managed_runtime_capacity_quarantine_retirements_validate_insert
    ON managed_runtime_capacity_quarantine_retirements;
CREATE TRIGGER managed_runtime_capacity_quarantine_retirements_validate_insert
BEFORE INSERT ON managed_runtime_capacity_quarantine_retirements
FOR EACH ROW EXECUTE FUNCTION managed_runtime_capacity_quarantine_retirement_validate_insert();

DROP TRIGGER IF EXISTS managed_runtime_capacity_quarantine_retirements_reject_mutation
    ON managed_runtime_capacity_quarantine_retirements;
CREATE TRIGGER managed_runtime_capacity_quarantine_retirements_reject_mutation
BEFORE UPDATE OR DELETE ON managed_runtime_capacity_quarantine_retirements
FOR EACH ROW EXECUTE FUNCTION managed_runtime_capacity_quarantine_retirement_reject_mutation();

-- Migrations execute without app.tenant_id. Open the source tables only for
-- this locked transaction; a rollback restores every RLS posture atomically.
ALTER TABLE techstack_vm_leases NO FORCE ROW LEVEL SECURITY;
ALTER TABLE techstack_vm_leases DISABLE ROW LEVEL SECURITY;
ALTER TABLE runtime_lease_execution_authorities NO FORCE ROW LEVEL SECURITY;
ALTER TABLE runtime_lease_execution_authorities DISABLE ROW LEVEL SECURITY;
ALTER TABLE managed_runtime_capacity_reservations NO FORCE ROW LEVEL SECURITY;
ALTER TABLE managed_runtime_capacity_reservations DISABLE ROW LEVEL SECURITY;
ALTER TABLE provider_operations NO FORCE ROW LEVEL SECURITY;
ALTER TABLE provider_operations DISABLE ROW LEVEL SECURITY;
ALTER TABLE servers NO FORCE ROW LEVEL SECURITY;
ALTER TABLE servers DISABLE ROW LEVEL SECURITY;
ALTER TABLE server_provider_resource_bindings NO FORCE ROW LEVEL SECURITY;
ALTER TABLE server_provider_resource_bindings DISABLE ROW LEVEL SECURITY;

INSERT INTO managed_runtime_capacity_quarantine_retirements (
    tenant_id,
    lease_id,
    resource_generation_id,
    provider_id,
    retirement_authority,
    simulate_provider_id,
    simulate_node_lifecycle,
    scenario_id,
    evidence_digest,
    retired_at
)
SELECT
    reservation.tenant_id,
    reservation.lease_id,
    reservation.resource_generation_id,
    reservation.provider_id,
    'legacy_simulate_no_provider_dispatch',
    lease.lease_json->'metadata'->>'simulate_provider_id',
    lease.lease_json->'metadata'->>'simulate_node_lifecycle',
    lease.lease_json->'metadata'->>'scenario_id',
    managed_runtime_capacity_quarantine_retirement_digest(
        reservation.tenant_id,
        reservation.lease_id,
        reservation.resource_generation_id,
        reservation.provider_id,
        lease.lease_json->'metadata'->>'simulate_provider_id',
        lease.lease_json->'metadata'->>'simulate_node_lifecycle',
        lease.lease_json->'metadata'->>'scenario_id'
    ),
    clock_timestamp()
FROM managed_runtime_capacity_reservations AS reservation
JOIN techstack_vm_leases AS lease
  ON lease.tenant_id = reservation.tenant_id
 AND lease.id = reservation.lease_id
 AND lease.resource_generation_id = reservation.resource_generation_id
WHERE reservation.reservation_origin = 'migration_quarantine'
  AND reservation.reservation_mode = 'quarantine'
  AND reservation.operation_id IS NULL
  AND NULLIF(BTRIM(lease.engine_vm_id), '') IS NULL
  AND lease.lease_json->'metadata'->>'simulate_provider_id' =
        reservation.provider_id || '-managed'
  AND lease.lease_json->'metadata'->>'simulate_node_lifecycle' = 'pvm'
  AND lease.lease_json->'metadata'->>'scenario_id' LIKE
        '%:' || (lease.lease_json->'metadata'->>'simulate_provider_id')
  AND NOT EXISTS (
      SELECT 1
      FROM runtime_lease_execution_authorities AS authority
      WHERE authority.tenant_id = reservation.tenant_id
        AND authority.lease_id = reservation.lease_id
  )
  AND NOT EXISTS (
      SELECT 1
      FROM provider_operations AS operation
      WHERE operation.tenant_id = reservation.tenant_id
        AND operation.lease_id = reservation.lease_id
  )
  AND NOT EXISTS (
      SELECT 1
      FROM servers AS server
      JOIN server_provider_resource_bindings AS binding
        ON binding.tenant_id = server.tenant_id
       AND binding.server_id = server.id
      WHERE server.tenant_id = reservation.tenant_id
        AND server.lease_id = reservation.lease_id
  )
ON CONFLICT DO NOTHING;

ALTER TABLE server_provider_resource_bindings ENABLE ROW LEVEL SECURITY;
ALTER TABLE server_provider_resource_bindings FORCE ROW LEVEL SECURITY;
ALTER TABLE servers ENABLE ROW LEVEL SECURITY;
ALTER TABLE servers FORCE ROW LEVEL SECURITY;
ALTER TABLE provider_operations ENABLE ROW LEVEL SECURITY;
ALTER TABLE provider_operations FORCE ROW LEVEL SECURITY;
ALTER TABLE managed_runtime_capacity_reservations ENABLE ROW LEVEL SECURITY;
ALTER TABLE managed_runtime_capacity_reservations FORCE ROW LEVEL SECURITY;
ALTER TABLE runtime_lease_execution_authorities ENABLE ROW LEVEL SECURITY;
ALTER TABLE runtime_lease_execution_authorities FORCE ROW LEVEL SECURITY;
ALTER TABLE techstack_vm_leases ENABLE ROW LEVEL SECURITY;
ALTER TABLE techstack_vm_leases FORCE ROW LEVEL SECURITY;

ALTER TABLE managed_runtime_capacity_quarantine_retirements ENABLE ROW LEVEL SECURITY;
ALTER TABLE managed_runtime_capacity_quarantine_retirements FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation ON managed_runtime_capacity_quarantine_retirements;
CREATE POLICY tenant_isolation ON managed_runtime_capacity_quarantine_retirements
    USING (tenant_id = current_setting('app.tenant_id', true))
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true));

-- Keep the insert-time database ceiling aligned with the application count.
-- Provider-absence releases and fake-only quarantine retirements are distinct
-- evidence classes and are intentionally checked through separate tables.
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

    PERFORM pg_advisory_xact_lock(
        hashtext(NEW.tenant_id),
        hashtext('providercontrol.capacity:owner_subject:' || NEW.owner_subject_id)
    );
    IF NEW.reservation_mode = 'limited' THEN
        SELECT count(*)
        INTO held_capacity
        FROM managed_runtime_capacity_reservations AS reservation
        WHERE reservation.tenant_id = NEW.tenant_id
          AND reservation.owner_subject_id = NEW.owner_subject_id
          AND NOT EXISTS (
              SELECT 1
              FROM managed_runtime_capacity_release_facts AS release
              WHERE release.tenant_id = reservation.tenant_id
                AND release.lease_id = reservation.lease_id
                AND release.resource_generation_id = reservation.resource_generation_id
          )
          AND NOT EXISTS (
              SELECT 1
              FROM managed_runtime_capacity_quarantine_retirements AS retirement
              WHERE retirement.tenant_id = reservation.tenant_id
                AND retirement.lease_id = reservation.lease_id
                AND retirement.resource_generation_id = reservation.resource_generation_id
          );
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

REVOKE ALL ON FUNCTION managed_runtime_capacity_quarantine_retirement_digest(
    text, text, uuid, text, text, text, text
) FROM PUBLIC;
REVOKE ALL ON FUNCTION managed_runtime_capacity_quarantine_retirement_validate_insert()
    FROM PUBLIC;
REVOKE ALL ON FUNCTION managed_runtime_capacity_quarantine_retirement_reject_mutation()
    FROM PUBLIC;

COMMENT ON TABLE managed_runtime_capacity_quarantine_retirements IS
    'Immutable evidence that a migration quarantine belongs to the legacy fake-only Simulate path and never held real provider-dispatch custody.';
