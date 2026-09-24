-- Reconcile legacy managed rollout waits whose exact provider generation was
-- already torn down. Runtime destroy admission now cancels pending rollout
-- offers through the JobStore; this bounded backfill repairs rows written
-- before that durable cross-replica cancellation existed.

SET LOCAL lock_timeout = '5s';
SELECT pg_catalog.set_config(
    'search_path',
    pg_catalog.quote_ident(pg_catalog.current_schema()) || ', pg_catalog, pg_temp',
    true
);

-- Tenant enumeration is the only cross-tenant read in this migration. Data
-- access below remains behind the normal FORCE RLS policies after setting the
-- exact tenant GUC for each iteration.
ALTER TABLE techstack_tenants NO FORCE ROW LEVEL SECURITY;

DO $terminal_managed_rollout_jobs$
DECLARE
    scoped_tenant_id text;
BEGIN
    FOR scoped_tenant_id IN
        SELECT tenant.id
        FROM techstack_tenants AS tenant
        ORDER BY tenant.id
    LOOP
        PERFORM pg_catalog.set_config('app.tenant_id', scoped_tenant_id, true);

        UPDATE jobs AS job
        SET state = 'cancelled',
            message = 'Superseded by completed managed runtime teardown',
            error = 'Job superseded by completed managed runtime teardown',
            error_details = NULL,
            result_json = job.result_json || pg_catalog.jsonb_build_object(
                'destroy_cancellation', pg_catalog.jsonb_build_object(
                    'schema', 'techstack.stack-rollout-destroy-cancellation/v1',
                    'stack_id', job.stack_id,
                    'cancelled_at', pg_catalog.to_char(
                        release.released_at AT TIME ZONE 'UTC',
                        'YYYY-MM-DD"T"HH24:MI:SS.US"Z"'
                    ),
                    'authority', 'terminal_managed_runtime_release'
                )
            ),
            completed_at = release.released_at,
            updated_at = release.released_at,
            execution_owner_id = NULL,
            execution_lease_expires_at = NULL
        FROM techstack_vm_leases AS lease
        JOIN servers AS server
          ON server.tenant_id = lease.tenant_id
         AND server.id = lease.server_id
         AND server.lease_id = lease.id
        JOIN managed_runtime_capacity_release_facts AS release
          ON release.tenant_id = lease.tenant_id
         AND release.lease_id = lease.id
         AND release.server_id = lease.server_id
        WHERE job.tenant_id = scoped_tenant_id
          AND lease.tenant_id = scoped_tenant_id
          AND job.stack_id = server.stack_id
          AND job.type IN ('provision', 'deploy')
          AND job.state = 'pending'
          AND job.execution_owner_id IS NULL
          AND job.execution_lease_expires_at IS NULL
          AND lease.cancelled_at IS NOT NULL
          AND lease.desired_state = 'absent'
          AND server.lifecycle_state = 'decommissioned'
          AND server.desired_state = 'absent'
          AND COALESCE(
              NULLIF(job.result_json->>'lease_id', ''),
              NULLIF(job.result_json->>'runtime_lease_id', ''),
              NULLIF(job.result_json->>'enrollment_resume_lease_id', '')
          ) = lease.id;
    END LOOP;
END
$terminal_managed_rollout_jobs$;

ALTER TABLE techstack_tenants FORCE ROW LEVEL SECURITY;
