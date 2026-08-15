-- Control-plane-only tenant-ID enumeration for provider incident retry.
--
-- techstack_tenants FORCE RLS intentionally returns zero rows without an exact
-- app.tenant_id scope. Keep a separate migration-owned, secret-free directory
-- of tenants that currently have pending incident dispatch work. The sweeper
-- reads only tenant IDs from this directory and then switches to tenant-scoped
-- transactions before touching provider_incidents payload rows.

SET LOCAL lock_timeout = '5s';
SELECT pg_catalog.set_config(
    'search_path',
    pg_catalog.quote_ident(pg_catalog.current_schema()) || ', pg_catalog, pg_temp',
    true
);

CREATE TABLE IF NOT EXISTS provider_incident_pending_dispatch_tenants (
    tenant_id text PRIMARY KEY CHECK (BTRIM(tenant_id) <> ''),
    pending_incident_count integer NOT NULL CHECK (pending_incident_count > 0),
    refreshed_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    FOREIGN KEY (tenant_id)
        REFERENCES techstack_tenants (id)
        ON UPDATE RESTRICT ON DELETE RESTRICT
);

REVOKE ALL ON TABLE provider_incident_pending_dispatch_tenants FROM PUBLIC;

CREATE OR REPLACE FUNCTION provider_incident_refresh_pending_dispatch_tenant()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path FROM CURRENT
AS $$
DECLARE
    incident_tenant_id text := COALESCE(NEW.tenant_id, OLD.tenant_id);
    scoped_tenant_id text := NULLIF(current_setting('app.tenant_id', true), '');
    pending_count integer;
BEGIN
    IF incident_tenant_id IS NULL OR BTRIM(incident_tenant_id) = '' THEN
        RAISE EXCEPTION 'provider incident pending dispatch refresh requires an exact tenant'
            USING ERRCODE = '55000';
    END IF;
    IF scoped_tenant_id IS DISTINCT FROM incident_tenant_id THEN
        RAISE EXCEPTION 'provider incident pending dispatch refresh requires the exact tenant scope'
            USING ERRCODE = '42501';
    END IF;

    PERFORM pg_catalog.pg_advisory_xact_lock(
        pg_catalog.hashtextextended(
            'providerincidents.pending-dispatch-tenant/v1:' || incident_tenant_id,
            0
        )
    );

    SELECT COUNT(*)::integer
    INTO pending_count
    FROM provider_incidents AS incident
    WHERE incident.tenant_id = incident_tenant_id
      AND incident.advisor_state = 'pending'
      AND incident.workflow_run_id = '';

    IF pending_count > 0 THEN
        INSERT INTO provider_incident_pending_dispatch_tenants (
            tenant_id,
            pending_incident_count,
            refreshed_at
        ) VALUES (
            incident_tenant_id,
            pending_count,
            clock_timestamp()
        )
        ON CONFLICT (tenant_id) DO UPDATE
        SET pending_incident_count = EXCLUDED.pending_incident_count,
            refreshed_at = EXCLUDED.refreshed_at;
    ELSE
        DELETE FROM provider_incident_pending_dispatch_tenants
        WHERE tenant_id = incident_tenant_id;
    END IF;

    RETURN COALESCE(NEW, OLD);
END;
$$;

-- Backfill uses the migration transaction only; runtime payload access remains
-- FORCE RLS tenant-scoped after this migration commits.
LOCK TABLE provider_incidents IN SHARE ROW EXCLUSIVE MODE;
ALTER TABLE provider_incidents NO FORCE ROW LEVEL SECURITY;
ALTER TABLE provider_incidents DISABLE ROW LEVEL SECURITY;

TRUNCATE TABLE provider_incident_pending_dispatch_tenants;
INSERT INTO provider_incident_pending_dispatch_tenants (
    tenant_id,
    pending_incident_count,
    refreshed_at
)
SELECT
    incident.tenant_id,
    COUNT(*)::integer,
    clock_timestamp()
FROM provider_incidents AS incident
WHERE incident.advisor_state = 'pending'
  AND incident.workflow_run_id = ''
GROUP BY incident.tenant_id;

ALTER TABLE provider_incidents ENABLE ROW LEVEL SECURITY;
ALTER TABLE provider_incidents FORCE ROW LEVEL SECURITY;

DROP TRIGGER IF EXISTS provider_incidents_refresh_pending_dispatch_tenant
    ON provider_incidents;
CREATE TRIGGER provider_incidents_refresh_pending_dispatch_tenant
AFTER INSERT OR UPDATE OR DELETE ON provider_incidents
FOR EACH ROW EXECUTE FUNCTION provider_incident_refresh_pending_dispatch_tenant();

CREATE OR REPLACE FUNCTION provider_incident_list_tenant_ids()
RETURNS TABLE (tenant_id text)
LANGUAGE sql
SECURITY DEFINER
STABLE
SET search_path FROM CURRENT
AS $$
    SELECT directory.tenant_id
    FROM provider_incident_pending_dispatch_tenants AS directory
    ORDER BY directory.tenant_id ASC
$$;

DO $provider_incident_tenant_enumerator_posture$
DECLARE
    active_schema text := current_schema();
    migration_role text := current_user;
    boundary_function text;
BEGIN
    FOREACH boundary_function IN ARRAY ARRAY[
        'provider_incident_refresh_pending_dispatch_tenant',
        'provider_incident_list_tenant_ids'
    ] LOOP
        EXECUTE pg_catalog.format(
            'ALTER FUNCTION %I.%I() OWNER TO %I',
            active_schema,
            boundary_function,
            migration_role
        );
        EXECUTE pg_catalog.format(
            'ALTER FUNCTION %I.%I() SECURITY DEFINER',
            active_schema,
            boundary_function
        );
        EXECUTE pg_catalog.format(
            'ALTER FUNCTION %I.%I() SET search_path TO pg_catalog, %I, pg_temp',
            active_schema,
            boundary_function,
            active_schema
        );
        EXECUTE pg_catalog.format(
            'REVOKE ALL ON FUNCTION %I.%I() FROM PUBLIC',
            active_schema,
            boundary_function
        );
    END LOOP;
    EXECUTE pg_catalog.format(
        'GRANT EXECUTE ON FUNCTION %I.provider_incident_list_tenant_ids() TO %I',
        active_schema,
        migration_role
    );
END
$provider_incident_tenant_enumerator_posture$;

COMMENT ON TABLE provider_incident_pending_dispatch_tenants IS
    'Secret-free migration-owned tenant directory for pending provider incident dispatch retries.';
COMMENT ON FUNCTION provider_incident_refresh_pending_dispatch_tenant() IS
    'Refreshes the secret-free pending-dispatch tenant directory under exact tenant scope.';
COMMENT ON FUNCTION provider_incident_list_tenant_ids() IS
    'Control-plane-only, secret-free tenant directory for provider incident retry; runtime role must not execute.';
