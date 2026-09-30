-- Wake-up directory for the server maintenance runner.
--
-- server_maintenance_jobs is FORCE RLS, so the runner cannot scan it across
-- tenants. Like 075's job_execution_reclaim_tenants, this secret-free
-- directory lists tenant IDs that may hold an active maintenance job; the
-- runner reads the jobs afterwards inside the tenant's RLS boundary and
-- retires an entry once the tenant has none left.

SET LOCAL lock_timeout = '5s';
SELECT pg_catalog.set_config(
    'search_path',
    pg_catalog.quote_ident(pg_catalog.current_schema()) || ', pg_catalog, pg_temp',
    true
);

CREATE TABLE IF NOT EXISTS server_maintenance_tenants (
    tenant_id text PRIMARY KEY CHECK (BTRIM(tenant_id) <> ''),
    refreshed_at timestamptz NOT NULL DEFAULT clock_timestamp()
);

REVOKE ALL ON TABLE server_maintenance_tenants FROM PUBLIC;

LOCK TABLE server_maintenance_jobs IN SHARE ROW EXCLUSIVE MODE;
ALTER TABLE server_maintenance_jobs NO FORCE ROW LEVEL SECURITY;
ALTER TABLE server_maintenance_jobs DISABLE ROW LEVEL SECURITY;

INSERT INTO server_maintenance_tenants (tenant_id, refreshed_at)
SELECT DISTINCT job.tenant_id, clock_timestamp()
FROM server_maintenance_jobs AS job
WHERE job.state IN ('queued', 'running', 'waiting', 'awaiting_node_return')
ON CONFLICT (tenant_id) DO NOTHING;

ALTER TABLE server_maintenance_jobs ENABLE ROW LEVEL SECURITY;
ALTER TABLE server_maintenance_jobs FORCE ROW LEVEL SECURITY;

CREATE OR REPLACE FUNCTION server_maintenance_wake_tenant()
RETURNS trigger
LANGUAGE plpgsql
SECURITY INVOKER
SET search_path FROM CURRENT
AS $$
BEGIN
    INSERT INTO server_maintenance_tenants (tenant_id, refreshed_at)
    VALUES (NEW.tenant_id, clock_timestamp())
    ON CONFLICT (tenant_id) DO UPDATE SET refreshed_at = EXCLUDED.refreshed_at;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS server_maintenance_wake ON server_maintenance_jobs;
CREATE TRIGGER server_maintenance_wake
AFTER INSERT ON server_maintenance_jobs
FOR EACH ROW
EXECUTE FUNCTION server_maintenance_wake_tenant();

COMMENT ON TABLE server_maintenance_tenants IS
    'Secret-free tenant wake-up directory for the server maintenance runner; tenant IDs and refresh time only.';
