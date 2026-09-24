-- The provider-control runtime role is intentionally NOBYPASSRLS. A
-- SECURITY DEFINER function which scans FORCE-RLS tenant tables therefore
-- cannot discover cross-tenant stale capacity. Materialize the exact,
-- secret-free recovery candidates while each source mutation still carries
-- its tenant scope, then expose only a bounded keyset read to the runtime.

SET LOCAL lock_timeout = '5s';
SELECT pg_catalog.set_config(
    'search_path',
    pg_catalog.quote_ident(pg_catalog.current_schema()) || ', pg_catalog, pg_temp',
    true
);

CREATE TABLE IF NOT EXISTS provider_stale_capacity_recovery_candidates (
    tenant_id text NOT NULL CHECK (BTRIM(tenant_id) <> ''),
    lease_id text NOT NULL CHECK (BTRIM(lease_id) <> ''),
    owner_subject_id text NOT NULL CHECK (BTRIM(owner_subject_id) <> ''),
    stack_id text NOT NULL CHECK (BTRIM(stack_id) <> ''),
    refreshed_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (tenant_id, lease_id),
    FOREIGN KEY (tenant_id)
        REFERENCES techstack_tenants (id)
        ON UPDATE RESTRICT ON DELETE RESTRICT
);

REVOKE ALL ON TABLE provider_stale_capacity_recovery_candidates FROM PUBLIC;

CREATE OR REPLACE FUNCTION provider_control_refresh_stale_capacity_recovery_tenant()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path FROM CURRENT
AS $$
DECLARE
    recovery_tenant_id text := COALESCE(NEW.tenant_id, OLD.tenant_id);
    scoped_tenant_id text := NULLIF(current_setting('app.tenant_id', true), '');
BEGIN
    IF recovery_tenant_id IS NULL OR BTRIM(recovery_tenant_id) = '' THEN
        RAISE EXCEPTION 'stale capacity recovery refresh requires an exact tenant'
            USING ERRCODE = '55000';
    END IF;
    IF scoped_tenant_id IS DISTINCT FROM recovery_tenant_id THEN
        RAISE EXCEPTION 'stale capacity recovery refresh requires the exact tenant scope'
            USING ERRCODE = '42501';
    END IF;

    PERFORM pg_catalog.pg_advisory_xact_lock(
        pg_catalog.hashtextextended(
            'providercontrol.stale-capacity-recovery/v1:' || recovery_tenant_id,
            0
        )
    );

    DELETE FROM provider_stale_capacity_recovery_candidates
    WHERE tenant_id = recovery_tenant_id;

    INSERT INTO provider_stale_capacity_recovery_candidates (
        tenant_id,
        lease_id,
        owner_subject_id,
        stack_id,
        refreshed_at
    )
    SELECT
        reservation.tenant_id,
        reservation.lease_id,
        reservation.owner_subject_id,
        lease.lease_json->'metadata'->>'stack_id',
        clock_timestamp()
    FROM managed_runtime_capacity_reservations AS reservation
    JOIN techstack_vm_leases AS lease
      ON lease.tenant_id = reservation.tenant_id
     AND lease.id = reservation.lease_id
     AND lease.resource_generation_id = reservation.resource_generation_id
    JOIN runtime_lease_execution_authorities AS authority
      ON authority.tenant_id = lease.tenant_id
     AND authority.lease_id = lease.id
     AND authority.execution_authority = 'techstack_provider_control'
    WHERE reservation.tenant_id = recovery_tenant_id
      AND NOT EXISTS (
          SELECT 1
          FROM managed_runtime_capacity_release_facts AS release
          WHERE release.tenant_id = reservation.tenant_id
            AND release.lease_id = reservation.lease_id
            AND release.resource_generation_id = reservation.resource_generation_id
      )
      AND (
          lease.cancelled_at IS NOT NULL
          OR lease.desired_state = 'absent'
          OR EXISTS (
              SELECT 1
              FROM servers AS server
              WHERE server.tenant_id = lease.tenant_id
                AND server.id = lease.server_id
                AND server.lease_id = lease.id
                AND server.desired_state = 'absent'
                AND (
                    server.lifecycle_state IN ('decommissioning', 'decommissioned')
                    OR server.decommissioned_at IS NOT NULL
                )
          )
      )
      AND COALESCE(lease.lease_json->'metadata'->>'stack_id', '') <> '';

    RETURN COALESCE(NEW, OLD);
END;
$$;

-- Build the initial directory with migration-only cross-tenant authority.
-- Runtime paths regain FORCE RLS before this transaction commits.
LOCK TABLE managed_runtime_capacity_reservations,
    managed_runtime_capacity_release_facts,
    techstack_vm_leases,
    runtime_lease_execution_authorities,
    servers
    IN SHARE ROW EXCLUSIVE MODE;

ALTER TABLE managed_runtime_capacity_reservations NO FORCE ROW LEVEL SECURITY;
ALTER TABLE managed_runtime_capacity_reservations DISABLE ROW LEVEL SECURITY;
ALTER TABLE managed_runtime_capacity_release_facts NO FORCE ROW LEVEL SECURITY;
ALTER TABLE managed_runtime_capacity_release_facts DISABLE ROW LEVEL SECURITY;
ALTER TABLE techstack_vm_leases NO FORCE ROW LEVEL SECURITY;
ALTER TABLE techstack_vm_leases DISABLE ROW LEVEL SECURITY;
ALTER TABLE runtime_lease_execution_authorities NO FORCE ROW LEVEL SECURITY;
ALTER TABLE runtime_lease_execution_authorities DISABLE ROW LEVEL SECURITY;
ALTER TABLE servers NO FORCE ROW LEVEL SECURITY;
ALTER TABLE servers DISABLE ROW LEVEL SECURITY;

TRUNCATE TABLE provider_stale_capacity_recovery_candidates;
INSERT INTO provider_stale_capacity_recovery_candidates (
    tenant_id,
    lease_id,
    owner_subject_id,
    stack_id,
    refreshed_at
)
SELECT
    reservation.tenant_id,
    reservation.lease_id,
    reservation.owner_subject_id,
    lease.lease_json->'metadata'->>'stack_id',
    clock_timestamp()
FROM managed_runtime_capacity_reservations AS reservation
JOIN techstack_vm_leases AS lease
  ON lease.tenant_id = reservation.tenant_id
 AND lease.id = reservation.lease_id
 AND lease.resource_generation_id = reservation.resource_generation_id
JOIN runtime_lease_execution_authorities AS authority
  ON authority.tenant_id = lease.tenant_id
 AND authority.lease_id = lease.id
 AND authority.execution_authority = 'techstack_provider_control'
WHERE NOT EXISTS (
          SELECT 1
          FROM managed_runtime_capacity_release_facts AS release
          WHERE release.tenant_id = reservation.tenant_id
            AND release.lease_id = reservation.lease_id
            AND release.resource_generation_id = reservation.resource_generation_id
      )
  AND (
      lease.cancelled_at IS NOT NULL
      OR lease.desired_state = 'absent'
      OR EXISTS (
          SELECT 1
          FROM servers AS server
          WHERE server.tenant_id = lease.tenant_id
            AND server.id = lease.server_id
            AND server.lease_id = lease.id
            AND server.desired_state = 'absent'
            AND (
                server.lifecycle_state IN ('decommissioning', 'decommissioned')
                OR server.decommissioned_at IS NOT NULL
            )
      )
  )
  AND COALESCE(lease.lease_json->'metadata'->>'stack_id', '') <> '';

ALTER TABLE servers ENABLE ROW LEVEL SECURITY;
ALTER TABLE servers FORCE ROW LEVEL SECURITY;
ALTER TABLE runtime_lease_execution_authorities ENABLE ROW LEVEL SECURITY;
ALTER TABLE runtime_lease_execution_authorities FORCE ROW LEVEL SECURITY;
ALTER TABLE techstack_vm_leases ENABLE ROW LEVEL SECURITY;
ALTER TABLE techstack_vm_leases FORCE ROW LEVEL SECURITY;
ALTER TABLE managed_runtime_capacity_release_facts ENABLE ROW LEVEL SECURITY;
ALTER TABLE managed_runtime_capacity_release_facts FORCE ROW LEVEL SECURITY;
ALTER TABLE managed_runtime_capacity_reservations ENABLE ROW LEVEL SECURITY;
ALTER TABLE managed_runtime_capacity_reservations FORCE ROW LEVEL SECURITY;

DROP TRIGGER IF EXISTS managed_capacity_reservations_refresh_stale_recovery
    ON managed_runtime_capacity_reservations;
CREATE TRIGGER managed_capacity_reservations_refresh_stale_recovery
AFTER INSERT OR UPDATE OR DELETE ON managed_runtime_capacity_reservations
FOR EACH ROW EXECUTE FUNCTION provider_control_refresh_stale_capacity_recovery_tenant();

DROP TRIGGER IF EXISTS managed_capacity_releases_refresh_stale_recovery
    ON managed_runtime_capacity_release_facts;
CREATE TRIGGER managed_capacity_releases_refresh_stale_recovery
AFTER INSERT OR UPDATE OR DELETE ON managed_runtime_capacity_release_facts
FOR EACH ROW EXECUTE FUNCTION provider_control_refresh_stale_capacity_recovery_tenant();

DROP TRIGGER IF EXISTS runtime_leases_refresh_stale_capacity_recovery
    ON techstack_vm_leases;
CREATE TRIGGER runtime_leases_refresh_stale_capacity_recovery
AFTER INSERT OR UPDATE OR DELETE ON techstack_vm_leases
FOR EACH ROW EXECUTE FUNCTION provider_control_refresh_stale_capacity_recovery_tenant();

DROP TRIGGER IF EXISTS lease_authorities_refresh_stale_capacity_recovery
    ON runtime_lease_execution_authorities;
CREATE TRIGGER lease_authorities_refresh_stale_capacity_recovery
AFTER INSERT OR UPDATE OR DELETE ON runtime_lease_execution_authorities
FOR EACH ROW EXECUTE FUNCTION provider_control_refresh_stale_capacity_recovery_tenant();

DROP TRIGGER IF EXISTS servers_refresh_stale_capacity_recovery
    ON servers;
CREATE TRIGGER servers_refresh_stale_capacity_recovery
AFTER INSERT OR UPDATE OR DELETE ON servers
FOR EACH ROW EXECUTE FUNCTION provider_control_refresh_stale_capacity_recovery_tenant();

CREATE OR REPLACE FUNCTION provider_control_list_stale_capacity_recovery_candidates(
    after_tenant_id text,
    after_lease_id text,
    requested_limit integer
)
RETURNS TABLE (
    tenant_id text,
    owner_subject_id text,
    lease_id text,
    stack_id text
)
LANGUAGE plpgsql
SECURITY DEFINER
STABLE
SET search_path FROM CURRENT
AS $$
BEGIN
    IF requested_limit IS NULL OR requested_limit < 1 OR requested_limit > 101 THEN
        RAISE EXCEPTION 'provider-control stale capacity recovery limit must be between 1 and 101'
            USING ERRCODE = '22023';
    END IF;

    RETURN QUERY
    SELECT
        candidate.tenant_id,
        candidate.owner_subject_id,
        candidate.lease_id,
        candidate.stack_id
    FROM provider_stale_capacity_recovery_candidates AS candidate
    WHERE (candidate.tenant_id, candidate.lease_id) > (
        COALESCE(BTRIM(after_tenant_id), ''),
        COALESCE(BTRIM(after_lease_id), '')
    )
    ORDER BY candidate.tenant_id, candidate.lease_id
    LIMIT requested_limit;
END;
$$;

DO $provider_stale_capacity_recovery_posture$
DECLARE
    active_schema text := current_schema();
    migration_role text := current_user;
    boundary_function text;
BEGIN
    FOREACH boundary_function IN ARRAY ARRAY[
        'provider_control_refresh_stale_capacity_recovery_tenant',
        'provider_control_list_stale_capacity_recovery_candidates'
    ] LOOP
        EXECUTE pg_catalog.format(
            'ALTER FUNCTION %I.%I%s OWNER TO %I',
            active_schema,
            boundary_function,
            CASE boundary_function
                WHEN 'provider_control_list_stale_capacity_recovery_candidates' THEN '(text, text, integer)'
                ELSE '()'
            END,
            migration_role
        );
        EXECUTE pg_catalog.format(
            'ALTER FUNCTION %I.%I%s SECURITY DEFINER',
            active_schema,
            boundary_function,
            CASE boundary_function
                WHEN 'provider_control_list_stale_capacity_recovery_candidates' THEN '(text, text, integer)'
                ELSE '()'
            END
        );
        EXECUTE pg_catalog.format(
            'ALTER FUNCTION %I.%I%s SET search_path TO pg_catalog, %I, pg_temp',
            active_schema,
            boundary_function,
            CASE boundary_function
                WHEN 'provider_control_list_stale_capacity_recovery_candidates' THEN '(text, text, integer)'
                ELSE '()'
            END,
            active_schema
        );
        EXECUTE pg_catalog.format(
            'REVOKE ALL ON FUNCTION %I.%I%s FROM PUBLIC',
            active_schema,
            boundary_function,
            CASE boundary_function
                WHEN 'provider_control_list_stale_capacity_recovery_candidates' THEN '(text, text, integer)'
                ELSE '()'
            END
        );
    END LOOP;
END
$provider_stale_capacity_recovery_posture$;

COMMENT ON TABLE provider_stale_capacity_recovery_candidates IS
    'Secret-free migration-owned directory of exact unreleased native capacity recovery candidates.';
COMMENT ON FUNCTION provider_control_refresh_stale_capacity_recovery_tenant() IS
    'Refreshes stale native-capacity recovery candidates for one exact tenant under its FORCE-RLS scope.';
COMMENT ON FUNCTION provider_control_list_stale_capacity_recovery_candidates(text, text, integer) IS
    'Bounded keyset recovery directory for the dedicated NOBYPASSRLS provider-control runtime role.';
