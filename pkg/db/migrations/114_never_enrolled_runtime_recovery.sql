-- Never-enrolled managed runtime recovery.
--
-- Managed provisioning can succeed and then never enroll or roll out: the
-- auto-deploy gate waits at most 20 minutes for canonical Guard evidence and
-- then fails the job without decommissioning, so a billed VM stays behind.
-- StaleCapacityRecovery only sees leases that already request provider
-- absence, and a restart often terminalizes the running provision job as
-- failed, so nothing re-enters cleanup.
--
-- This directory lists unreleased provider-control capacity whose enrollment
-- window has passed and whose exact canonical server (when one exists at all)
-- has never enrolled: no worker, no heartbeat, never active. Leases that
-- already request absence stay with StaleCapacityRecovery, and any
-- non-terminal job for the stack suppresses the candidate so the reaper never
-- races the boot re-enqueue or an in-flight rollout. The directory is
-- read-only; provider-control must still prove exact provider absence before
-- it writes a release fact.

SET LOCAL lock_timeout = '5s';

CREATE OR REPLACE FUNCTION provider_control_list_never_enrolled_runtime_candidates(
    after_tenant_id text,
    after_lease_id text,
    enrollment_window_seconds integer,
    requested_limit integer
)
RETURNS TABLE (
    tenant_id text,
    owner_subject_id text,
    lease_id text,
    stack_id text,
    reserved_at timestamptz
)
LANGUAGE plpgsql
SECURITY DEFINER
STABLE
SET search_path FROM CURRENT
AS $$
BEGIN
    IF requested_limit IS NULL OR requested_limit < 1 OR requested_limit > 101 THEN
        RAISE EXCEPTION 'provider-control never-enrolled runtime limit must be between 1 and 101'
            USING ERRCODE = '22023';
    END IF;
    IF enrollment_window_seconds IS NULL OR enrollment_window_seconds < 60 OR enrollment_window_seconds > 86400 THEN
        RAISE EXCEPTION 'provider-control never-enrolled enrollment window must be between 60 and 86400 seconds'
            USING ERRCODE = '22023';
    END IF;

    RETURN QUERY
    WITH candidates AS (
        SELECT
            reservation.tenant_id AS tenant_id,
            reservation.owner_subject_id AS owner_subject_id,
            reservation.lease_id AS lease_id,
            reservation.reserved_at AS reserved_at,
            COALESCE(lease.lease_json->'metadata'->>'stack_id', '') AS stack_id
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
          AND lease.cancelled_at IS NULL
          AND lease.desired_state <> 'absent'
          AND NOT EXISTS (
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
          AND reservation.reserved_at < clock_timestamp() - make_interval(secs => enrollment_window_seconds)
          AND NOT EXISTS (
                  SELECT 1
                  FROM servers AS server
                  WHERE server.tenant_id = lease.tenant_id
                    AND server.id = lease.server_id
                    AND server.lease_id = lease.id
                    AND (
                        server.worker_id IS NOT NULL
                        OR server.last_heartbeat_at IS NOT NULL
                        OR server.lifecycle_state = 'active'
                        OR server.connection_state IN ('connected', 'degraded')
                    )
              )
    )
    SELECT
        candidate.tenant_id,
        candidate.owner_subject_id,
        candidate.lease_id,
        candidate.stack_id,
        candidate.reserved_at
    FROM candidates AS candidate
    WHERE candidate.stack_id <> ''
      AND NOT EXISTS (
              SELECT 1
              FROM jobs AS job
              WHERE job.tenant_id = candidate.tenant_id
                AND job.stack_id = candidate.stack_id
                AND job.state IN ('pending', 'running', 'waiting')
          )
      AND (candidate.tenant_id, candidate.lease_id) > (
          COALESCE(BTRIM(after_tenant_id), ''),
          COALESCE(BTRIM(after_lease_id), '')
      )
    ORDER BY candidate.tenant_id, candidate.lease_id
    LIMIT requested_limit;
END;
$$;

ALTER FUNCTION provider_control_list_never_enrolled_runtime_candidates(text, text, integer, integer)
    SET search_path TO pg_catalog, public, pg_temp;
REVOKE ALL ON FUNCTION provider_control_list_never_enrolled_runtime_candidates(text, text, integer, integer)
    FROM PUBLIC;

COMMENT ON FUNCTION provider_control_list_never_enrolled_runtime_candidates(text, text, integer, integer) IS
    'Hard-limited read-only directory of unreleased provider-control capacity past its enrollment window whose canonical server never enrolled and which no non-terminal stack job is working on.';
