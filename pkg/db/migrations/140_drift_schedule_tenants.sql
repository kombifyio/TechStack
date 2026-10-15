-- Tenant directory for the scheduled Advanced drift detection.
--
-- stacks is FORCE RLS, so the drift scheduler cannot scan it across tenants.
-- Like 125's server_maintenance_tenants, this secret-free directory lists
-- tenant IDs that may hold a live stack; the scheduler reads the stacks
-- afterwards inside the tenant's RLS boundary and retires an entry once the
-- tenant has no live stack left.
--
-- The directory is fed only by stack writes made inside the tenant's own RLS
-- boundary: a stack joins on insert and on every later update (rollout,
-- status and drift writes). No cross-tenant backfill runs here, so a stack
-- that predates this migration joins on its next write.

SET LOCAL lock_timeout = '5s';
SELECT pg_catalog.set_config(
    'search_path',
    pg_catalog.quote_ident(pg_catalog.current_schema()) || ', pg_catalog, pg_temp',
    true
);

CREATE TABLE IF NOT EXISTS drift_schedule_tenants (
    tenant_id text PRIMARY KEY CHECK (BTRIM(tenant_id) <> ''),
    refreshed_at timestamptz NOT NULL DEFAULT clock_timestamp()
);

REVOKE ALL ON TABLE drift_schedule_tenants FROM PUBLIC;

CREATE OR REPLACE FUNCTION drift_schedule_wake_tenant()
RETURNS trigger
LANGUAGE plpgsql
SECURITY INVOKER
SET search_path FROM CURRENT
AS $$
BEGIN
    IF NEW.deleted_at IS NULL THEN
        INSERT INTO drift_schedule_tenants (tenant_id, refreshed_at)
        VALUES (NEW.tenant_id, clock_timestamp())
        ON CONFLICT (tenant_id) DO UPDATE SET refreshed_at = EXCLUDED.refreshed_at;
    END IF;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS drift_schedule_wake ON stacks;
CREATE TRIGGER drift_schedule_wake
AFTER INSERT OR UPDATE ON stacks
FOR EACH ROW
EXECUTE FUNCTION drift_schedule_wake_tenant();

COMMENT ON TABLE drift_schedule_tenants IS
    'Secret-free tenant directory for the scheduled Advanced drift detection; tenant IDs and refresh time only.';
