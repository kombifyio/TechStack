-- Durable tenant discovery for destroy jobs waiting on native provider
-- decommission. Queue timers are process-local, so a restart must recover an
-- already-persisted wait even when the corresponding provider operation has
-- just become terminal and no longer appears in the runnable-operation index.
--
-- The restricted provider-control runtime role receives only the bounded,
-- secret-free tenant-ID function below. It cannot read jobs or this directory
-- directly; each returned tenant is revalidated through tenant-scoped control
-- plane stores before a durable StartJob fence permits provider work.

SET LOCAL lock_timeout = '5s';
SELECT pg_catalog.set_config(
    'search_path',
    pg_catalog.quote_ident(pg_catalog.current_schema()) || ', pg_catalog, pg_temp',
    true
);

CREATE TABLE IF NOT EXISTS provider_decommission_wait_tenants (
    tenant_id text PRIMARY KEY CHECK (BTRIM(tenant_id) <> ''),
    recovery_destroy_count integer NOT NULL CHECK (recovery_destroy_count > 0),
    earliest_resume_at timestamptz NOT NULL,
    refreshed_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    FOREIGN KEY (tenant_id)
        REFERENCES techstack_tenants (id)
        ON UPDATE RESTRICT ON DELETE RESTRICT
);

REVOKE ALL ON TABLE provider_decommission_wait_tenants FROM PUBLIC;

CREATE OR REPLACE FUNCTION provider_control_refresh_decommission_wait_tenant()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path FROM CURRENT
AS $$
DECLARE
    wait_tenant_id text := COALESCE(NEW.tenant_id, OLD.tenant_id);
    scoped_tenant_id text := NULLIF(current_setting('app.tenant_id', true), '');
    wait_count integer;
    first_resume_at timestamptz;
BEGIN
    IF wait_tenant_id IS NULL OR BTRIM(wait_tenant_id) = '' THEN
        RAISE EXCEPTION 'provider decommission wait refresh requires an exact tenant'
            USING ERRCODE = '55000';
    END IF;
    IF scoped_tenant_id IS DISTINCT FROM wait_tenant_id THEN
        RAISE EXCEPTION 'provider decommission wait refresh requires the exact tenant scope'
            USING ERRCODE = '42501';
    END IF;

    PERFORM pg_catalog.pg_advisory_xact_lock(
        pg_catalog.hashtextextended(
            'providercontrol.decommission-wait-tenant/v1:' || wait_tenant_id,
            0
        )
    );

    -- Keep both pending recovery waits and running executions in this durable
    -- directory. A fresh running row is intentionally *not* due until its
    -- 3s recovery grace passes; each 500ms queue heartbeat moves that due
    -- instant forward. Removing fresh runners here would strand a crash after
    -- StartJob because no later write exists to reinsert the tenant.
    SELECT COUNT(*)::integer,
        MIN(
            CASE job.state
                WHEN 'pending' THEN job.scheduled_for
                WHEN 'running' THEN job.updated_at + interval '3 seconds'
            END
        )
    INTO wait_count, first_resume_at
    FROM jobs AS job
    WHERE job.tenant_id = wait_tenant_id
      AND job.type = 'destroy'
      AND job.state IN ('pending', 'running')
      AND job.result_json->'managed_provider_decommission_recovery'->>'schema' =
          'techstack.managed-provider-decommission-recovery/v1'
      AND job.result_json->'managed_provider_decommission_recovery'->>'tenant_id' = job.tenant_id
      AND job.result_json->'managed_provider_decommission_recovery'->>'stack_id' = job.stack_id;

    IF wait_count > 0 AND first_resume_at IS NOT NULL THEN
        INSERT INTO provider_decommission_wait_tenants (
            tenant_id,
            recovery_destroy_count,
            earliest_resume_at,
            refreshed_at
        ) VALUES (
            wait_tenant_id,
            wait_count,
            first_resume_at,
            clock_timestamp()
        )
        ON CONFLICT (tenant_id) DO UPDATE
        SET recovery_destroy_count = EXCLUDED.recovery_destroy_count,
            earliest_resume_at = EXCLUDED.earliest_resume_at,
            refreshed_at = EXCLUDED.refreshed_at;
    ELSE
        DELETE FROM provider_decommission_wait_tenants
        WHERE tenant_id = wait_tenant_id;
    END IF;

    RETURN COALESCE(NEW, OLD);
END;
$$;

-- Backfill only during the migration transaction. Runtime job reads remain
-- tenant-scoped under FORCE RLS after this migration commits.
LOCK TABLE jobs IN SHARE ROW EXCLUSIVE MODE;
ALTER TABLE jobs NO FORCE ROW LEVEL SECURITY;
ALTER TABLE jobs DISABLE ROW LEVEL SECURITY;

-- Preserve exact legacy native-provider waits across this migration by adding
-- the server-shaped scheduling marker before the new marker-only directory is
-- built. This marker grants no execution authority: Go still reloads the
-- canonical tenant-scoped stack and reclassifies managed runtime custody.
UPDATE jobs
SET result_json = result_json || jsonb_build_object(
    'managed_provider_decommission_recovery',
    jsonb_build_object(
        'schema', 'techstack.managed-provider-decommission-recovery/v1',
        'tenant_id', tenant_id,
        'stack_id', stack_id
    )
)
WHERE state IN ('pending', 'running')
  AND type = 'destroy'
  AND result_json->'job_wait'->>'state' = 'waiting'
  AND result_json->'job_wait'->>'reason' = 'waiting_provider_decommission'
  AND stack_id IS NOT NULL;

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

DROP TRIGGER IF EXISTS jobs_refresh_provider_decommission_wait_tenant
    ON jobs;
CREATE TRIGGER jobs_refresh_provider_decommission_wait_tenant
AFTER INSERT OR UPDATE OR DELETE ON jobs
FOR EACH ROW EXECUTE FUNCTION provider_control_refresh_decommission_wait_tenant();

-- This is the only cross-tenant recovery read exposed to the provider-control
-- runtime role. The dynamic due predicate is held in the secret-free
-- directory, so the function never returns job IDs, owners, payloads, or
-- provider credentials.
CREATE OR REPLACE FUNCTION provider_control_list_due_decommission_wait_tenants(
    after_tenant_id text,
    requested_limit integer
)
RETURNS TABLE (tenant_id text)
LANGUAGE plpgsql
SECURITY DEFINER
STABLE
SET search_path FROM CURRENT
AS $$
BEGIN
    IF requested_limit IS NULL OR requested_limit < 1 OR requested_limit > 101 THEN
        RAISE EXCEPTION 'provider-control due decommission wait tenant limit must be between 1 and 101'
            USING ERRCODE = '22023';
    END IF;
    RETURN QUERY
    SELECT directory.tenant_id
    FROM provider_decommission_wait_tenants AS directory
    WHERE directory.tenant_id > COALESCE(BTRIM(after_tenant_id), '')
      AND directory.earliest_resume_at <= clock_timestamp()
    ORDER BY directory.tenant_id ASC
    LIMIT requested_limit;
END;
$$;

DO $provider_decommission_wait_recovery_posture$
DECLARE
    active_schema text := current_schema();
    migration_role text := current_user;
    boundary_function text;
BEGIN
    FOREACH boundary_function IN ARRAY ARRAY[
        'provider_control_refresh_decommission_wait_tenant',
        'provider_control_list_due_decommission_wait_tenants'
    ] LOOP
        EXECUTE pg_catalog.format(
            'ALTER FUNCTION %I.%I%s OWNER TO %I',
            active_schema,
            boundary_function,
            CASE boundary_function
                WHEN 'provider_control_list_due_decommission_wait_tenants' THEN '(text, integer)'
                ELSE '()'
            END,
            migration_role
        );
        EXECUTE pg_catalog.format(
            'ALTER FUNCTION %I.%I%s SECURITY DEFINER',
            active_schema,
            boundary_function,
            CASE boundary_function
                WHEN 'provider_control_list_due_decommission_wait_tenants' THEN '(text, integer)'
                ELSE '()'
            END
        );
        EXECUTE pg_catalog.format(
            'ALTER FUNCTION %I.%I%s SET search_path TO pg_catalog, %I, pg_temp',
            active_schema,
            boundary_function,
            CASE boundary_function
                WHEN 'provider_control_list_due_decommission_wait_tenants' THEN '(text, integer)'
                ELSE '()'
            END,
            active_schema
        );
        EXECUTE pg_catalog.format(
            'REVOKE ALL ON FUNCTION %I.%I%s FROM PUBLIC',
            active_schema,
            boundary_function,
            CASE boundary_function
                WHEN 'provider_control_list_due_decommission_wait_tenants' THEN '(text, integer)'
                ELSE '()'
            END
        );
    END LOOP;
END
$provider_decommission_wait_recovery_posture$;

COMMENT ON TABLE provider_decommission_wait_tenants IS
    'Secret-free tenant directory for restart-safe native provider decommission wait recovery.';
COMMENT ON FUNCTION provider_control_refresh_decommission_wait_tenant() IS
    'Refreshes one exact-tenant provider decommission wait directory entry from durable jobs.';
COMMENT ON FUNCTION provider_control_list_due_decommission_wait_tenants(text, integer) IS
    'Bounded secret-free tenant discovery for due marker-bound managed provider decommission recovery.';
