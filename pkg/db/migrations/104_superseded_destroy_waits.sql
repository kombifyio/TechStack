-- Reconcile legacy destroy offers that lost the per-stack execution race and
-- remained pending after a later destroy attempt became terminal. Runtime
-- admission now supersedes these offers before admitting the replacement.

SET LOCAL lock_timeout = '5s';
SELECT pg_catalog.set_config(
    'search_path',
    pg_catalog.quote_ident(pg_catalog.current_schema()) || ', pg_catalog, pg_temp',
    true
);

-- Tenant enumeration is the only cross-tenant read. Every job read and update
-- below remains under the exact tenant GUC and the normal FORCE RLS policy.
ALTER TABLE techstack_tenants NO FORCE ROW LEVEL SECURITY;

DO $superseded_destroy_waits$
DECLARE
    scoped_tenant_id text;
BEGIN
    FOR scoped_tenant_id IN
        SELECT tenant.id
        FROM techstack_tenants AS tenant
        ORDER BY tenant.id
    LOOP
        PERFORM pg_catalog.set_config('app.tenant_id', scoped_tenant_id, true);

        WITH superseded AS (
            SELECT waiting.id AS waiting_job_id,
                   terminal.id AS terminal_job_id,
                   terminal.completed_at AS terminal_at
            FROM jobs AS waiting
            JOIN LATERAL (
                SELECT newer.id, newer.completed_at
                FROM jobs AS newer
                WHERE newer.tenant_id = waiting.tenant_id
                  AND newer.stack_id = waiting.stack_id
                  AND newer.type = 'destroy'
                  AND newer.state IN ('completed', 'failed', 'cancelled')
                  AND newer.completed_at IS NOT NULL
                  AND newer.created_at > waiting.created_at
                ORDER BY newer.created_at DESC, newer.id DESC
                LIMIT 1
            ) AS terminal ON true
            WHERE waiting.tenant_id = scoped_tenant_id
              AND waiting.type = 'destroy'
              AND waiting.state = 'pending'
              AND waiting.execution_owner_id IS NULL
              AND waiting.execution_lease_expires_at IS NULL
              AND waiting.result_json->'job_wait'->>'state' = 'waiting'
              AND waiting.result_json->'job_wait'->>'reason' = 'waiting_stack_execution'
        )
        UPDATE jobs AS job
        SET state = 'cancelled',
            message = 'Superseded by newer stack destroy',
            error = 'Job superseded by newer stack destroy',
            error_details = NULL,
            result_json = job.result_json || pg_catalog.jsonb_build_object(
                'destroy_supersession', pg_catalog.jsonb_build_object(
                    'schema', 'techstack.stack-destroy-supersession/v1',
                    'stack_id', job.stack_id,
                    'superseded_at', pg_catalog.to_char(
                        superseded.terminal_at AT TIME ZONE 'UTC',
                        'YYYY-MM-DD"T"HH24:MI:SS.US"Z"'
                    ),
                    'authority', 'later_terminal_destroy',
                    'superseded_by_job_id', superseded.terminal_job_id
                )
            ),
            completed_at = superseded.terminal_at,
            updated_at = superseded.terminal_at,
            execution_owner_id = NULL,
            execution_lease_expires_at = NULL
        FROM superseded
        WHERE job.tenant_id = scoped_tenant_id
          AND job.id = superseded.waiting_job_id
          AND job.state = 'pending';
    END LOOP;
END
$superseded_destroy_waits$;

ALTER TABLE techstack_tenants FORCE ROW LEVEL SECURITY;
