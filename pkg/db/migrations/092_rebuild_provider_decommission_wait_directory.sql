-- Rebuild the secret-free tenant directory from durable recovery receipts.
-- Migration 071 established the trigger and authority boundary, but production
-- truth showed marker-bound July destroy waits with an empty directory. This
-- bounded rebuild restores discovery without granting the runtime direct job
-- access or changing any provider resource.

SET LOCAL lock_timeout = '5s';
SELECT pg_catalog.set_config(
    'search_path',
    pg_catalog.quote_ident(pg_catalog.current_schema()) || ', pg_catalog, pg_temp',
    true
);

LOCK TABLE jobs IN SHARE ROW EXCLUSIVE MODE;
ALTER TABLE jobs NO FORCE ROW LEVEL SECURITY;
ALTER TABLE jobs DISABLE ROW LEVEL SECURITY;

TRUNCATE TABLE provider_decommission_wait_tenants;
INSERT INTO provider_decommission_wait_tenants (
    tenant_id,
    recovery_destroy_count,
    earliest_resume_at,
    refreshed_at
)
SELECT
    job.tenant_id,
    COUNT(*)::integer,
    MIN(
        CASE job.state
            WHEN 'pending' THEN job.scheduled_for
            WHEN 'running' THEN job.updated_at + interval '3 seconds'
        END
    ),
    clock_timestamp()
FROM jobs AS job
WHERE job.state IN ('pending', 'running')
  AND job.type = 'destroy'
  AND job.result_json->'managed_provider_decommission_recovery'->>'schema' =
      'techstack.managed-provider-decommission-recovery/v1'
  AND job.result_json->'managed_provider_decommission_recovery'->>'tenant_id' = job.tenant_id
  AND job.result_json->'managed_provider_decommission_recovery'->>'stack_id' = job.stack_id
GROUP BY job.tenant_id;

ALTER TABLE jobs ENABLE ROW LEVEL SECURITY;
ALTER TABLE jobs FORCE ROW LEVEL SECURITY;
