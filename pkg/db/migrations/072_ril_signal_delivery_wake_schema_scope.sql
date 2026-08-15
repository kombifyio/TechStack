-- Migration 050 created the delivery wake trigger with a hardcoded
-- public.ril_signal_delivery_tenants target while pinning the function to
-- search_path = pg_catalog, pg_temp. Every outbox write therefore fails with
-- SQLSTATE 42P01 whenever the migration chain owns a schema other than public,
-- which is the posture of the isolated integration suites. Rebind the function
-- to the schema that owns this migration, matching the provider-control
-- functions from migration 034.
SET LOCAL lock_timeout = '5s';
SELECT pg_catalog.set_config(
    'search_path',
    pg_catalog.quote_ident(pg_catalog.current_schema()) || ', pg_catalog, pg_temp',
    true
);

CREATE OR REPLACE FUNCTION wake_ril_signal_delivery_tenant()
RETURNS trigger
LANGUAGE plpgsql
SECURITY INVOKER
SET search_path FROM CURRENT
AS $$
BEGIN
    INSERT INTO ril_signal_delivery_tenants (tenant_id, pending_at)
    VALUES (NEW.tenant_id, clock_timestamp())
    ON CONFLICT (tenant_id) DO UPDATE
      SET pending_at = LEAST(ril_signal_delivery_tenants.pending_at, EXCLUDED.pending_at);
    RETURN NEW;
END;
$$;

-- Databases migrated before this fix could not run the trigger, so the
-- cross-tenant wake index can be missing rows for still-pending work.
INSERT INTO ril_signal_delivery_tenants (tenant_id, pending_at)
SELECT tenant_id, MIN(available_at)
FROM ril_signal_outbox
WHERE delivered_at IS NULL AND failed_at IS NULL
GROUP BY tenant_id
ON CONFLICT (tenant_id) DO UPDATE
SET pending_at = LEAST(ril_signal_delivery_tenants.pending_at, EXCLUDED.pending_at);
