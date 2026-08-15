-- Bounded cross-tenant directory for native recovery of capacity reservations
-- whose leases already request absence. The runtime role receives EXECUTE on
-- this function only; it never receives a cross-tenant table grant.

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
        reservation.tenant_id,
        reservation.owner_subject_id,
        reservation.lease_id,
        COALESCE(lease.lease_json->'metadata'->>'stack_id', '')
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
      )
      AND COALESCE(lease.lease_json->'metadata'->>'stack_id', '') <> ''
      AND (reservation.tenant_id, reservation.lease_id) > (
          COALESCE(BTRIM(after_tenant_id), ''),
          COALESCE(BTRIM(after_lease_id), '')
      )
    ORDER BY reservation.tenant_id, reservation.lease_id
    LIMIT requested_limit;
END;
$$;

ALTER FUNCTION provider_control_list_stale_capacity_recovery_candidates(text, text, integer)
    SET search_path TO pg_catalog, public, pg_temp;
REVOKE ALL ON FUNCTION provider_control_list_stale_capacity_recovery_candidates(text, text, integer)
    FROM PUBLIC;

COMMENT ON FUNCTION provider_control_list_stale_capacity_recovery_candidates(text, text, integer) IS
    'Hard-limited cross-tenant directory of unreleased native capacity whose lease already requests provider absence.';
