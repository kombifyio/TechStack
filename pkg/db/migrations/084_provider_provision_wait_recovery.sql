-- Secret-free, provider-neutral recovery projection for provision jobs waiting
-- on a native provider operation. The jobs row and provider ledger remain the
-- two authorities; this table is only a restart scheduler index.

SET LOCAL lock_timeout = '5s';
SELECT pg_catalog.set_config(
    'search_path',
    pg_catalog.quote_ident(pg_catalog.current_schema()) || ', pg_catalog, pg_temp',
    true
);

CREATE TABLE IF NOT EXISTS provider_provision_wait_recovery (
    tenant_id text NOT NULL CHECK (BTRIM(tenant_id) <> ''),
    operation_id text NOT NULL CHECK (BTRIM(operation_id) <> ''),
    job_id text NOT NULL CHECK (BTRIM(job_id) <> ''),
    refreshed_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (tenant_id, operation_id, job_id),
    FOREIGN KEY (tenant_id)
        REFERENCES techstack_tenants (id)
        ON UPDATE RESTRICT ON DELETE RESTRICT
);

REVOKE ALL ON TABLE provider_provision_wait_recovery FROM PUBLIC;

CREATE INDEX IF NOT EXISTS idx_jobs_provider_provision_wait_operation
    ON jobs (tenant_id, (result_json->>'operation_id'), scheduled_for, id)
    WHERE type = 'provision'
      AND state = 'pending'
      AND result_json->'job_wait'->>'state' = 'waiting'
      AND result_json->'job_wait'->>'reason' = 'waiting_provider_provision'
      AND NULLIF(BTRIM(result_json->>'operation_id'), '') IS NOT NULL;

CREATE OR REPLACE FUNCTION provider_control_refresh_provider_provision_wait()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path FROM CURRENT
AS $$
DECLARE
    scoped_tenant_id text := NULLIF(current_setting('app.tenant_id', true), '');
    old_operation_id text;
    new_operation_id text;
BEGIN
    IF TG_OP IN ('UPDATE', 'DELETE') THEN
        IF OLD.tenant_id IS NULL OR BTRIM(OLD.tenant_id) = '' THEN
            RAISE EXCEPTION 'provider provision wait refresh requires an exact old tenant'
                USING ERRCODE = '55000';
        END IF;
        IF scoped_tenant_id IS DISTINCT FROM OLD.tenant_id THEN
            RAISE EXCEPTION 'provider provision wait refresh requires the exact old tenant scope'
                USING ERRCODE = '42501';
        END IF;
        old_operation_id := NULLIF(BTRIM(OLD.result_json->>'operation_id'), '');
        IF old_operation_id IS NOT NULL THEN
            DELETE FROM provider_provision_wait_recovery
            WHERE tenant_id = OLD.tenant_id
              AND operation_id = old_operation_id
              AND job_id = OLD.id;
        END IF;
    END IF;

    IF TG_OP IN ('INSERT', 'UPDATE') THEN
        IF NEW.tenant_id IS NULL OR BTRIM(NEW.tenant_id) = '' THEN
            RAISE EXCEPTION 'provider provision wait refresh requires an exact new tenant'
                USING ERRCODE = '55000';
        END IF;
        IF scoped_tenant_id IS DISTINCT FROM NEW.tenant_id THEN
            RAISE EXCEPTION 'provider provision wait refresh requires the exact new tenant scope'
                USING ERRCODE = '42501';
        END IF;
        new_operation_id := NULLIF(BTRIM(NEW.result_json->>'operation_id'), '');
        IF NEW.type = 'provision'
           AND NEW.state = 'pending'
           AND NEW.result_json->'job_wait'->>'state' = 'waiting'
           AND NEW.result_json->'job_wait'->>'reason' = 'waiting_provider_provision'
           AND new_operation_id IS NOT NULL THEN
            INSERT INTO provider_provision_wait_recovery (
                tenant_id,
                operation_id,
                job_id,
                refreshed_at
            ) VALUES (
                NEW.tenant_id,
                new_operation_id,
                NEW.id,
                clock_timestamp()
            )
            ON CONFLICT (tenant_id, operation_id, job_id) DO UPDATE
            SET refreshed_at = EXCLUDED.refreshed_at;
        END IF;
    END IF;

    RETURN COALESCE(NEW, OLD);
END;
$$;

-- Backfill runs only in the migration transaction. Runtime job access remains
-- tenant-scoped under FORCE RLS after this migration commits.
LOCK TABLE jobs IN SHARE ROW EXCLUSIVE MODE;
ALTER TABLE jobs NO FORCE ROW LEVEL SECURITY;
ALTER TABLE jobs DISABLE ROW LEVEL SECURITY;

TRUNCATE TABLE provider_provision_wait_recovery;
INSERT INTO provider_provision_wait_recovery (
    tenant_id,
    operation_id,
    job_id,
    refreshed_at
)
SELECT
    job.tenant_id,
    BTRIM(job.result_json->>'operation_id'),
    job.id,
    clock_timestamp()
FROM jobs AS job
WHERE job.type = 'provision'
  AND job.state = 'pending'
  AND job.result_json->'job_wait'->>'state' = 'waiting'
  AND job.result_json->'job_wait'->>'reason' = 'waiting_provider_provision'
  AND NULLIF(BTRIM(job.result_json->>'operation_id'), '') IS NOT NULL;

ALTER TABLE jobs ENABLE ROW LEVEL SECURITY;
ALTER TABLE jobs FORCE ROW LEVEL SECURITY;

DROP TRIGGER IF EXISTS jobs_refresh_provider_provision_wait_recovery
    ON jobs;
CREATE TRIGGER jobs_refresh_provider_provision_wait_recovery
AFTER INSERT OR UPDATE OR DELETE ON jobs
FOR EACH ROW EXECUTE FUNCTION provider_control_refresh_provider_provision_wait();

-- The only cross-tenant recovery read exposed to the provider-control runtime
-- returns operation references, never job payloads, owners, or credentials.
CREATE OR REPLACE FUNCTION provider_control_list_provider_provision_waits(
    after_tenant_id text,
    after_operation_id text,
    requested_limit integer
)
RETURNS TABLE (tenant_id text, operation_id text)
LANGUAGE plpgsql
SECURITY DEFINER
STABLE
SET search_path FROM CURRENT
AS $$
BEGIN
    IF requested_limit IS NULL OR requested_limit < 1 OR requested_limit > 101 THEN
        RAISE EXCEPTION 'provider-control provider provision wait limit must be between 1 and 101'
            USING ERRCODE = '22023';
    END IF;
    RETURN QUERY
    SELECT directory.tenant_id, directory.operation_id
    FROM provider_provision_wait_recovery AS directory
    WHERE (directory.tenant_id, directory.operation_id) > (
        COALESCE(NULLIF(BTRIM(after_tenant_id), ''), ''),
        COALESCE(NULLIF(BTRIM(after_operation_id), ''), '')
    )
    GROUP BY directory.tenant_id, directory.operation_id
    ORDER BY directory.tenant_id ASC, directory.operation_id ASC
    LIMIT requested_limit;
END;
$$;

DO $provider_provision_wait_recovery_posture$
DECLARE
    active_schema text := current_schema();
    migration_role text := current_user;
    boundary_function text;
BEGIN
    FOREACH boundary_function IN ARRAY ARRAY[
        'provider_control_refresh_provider_provision_wait',
        'provider_control_list_provider_provision_waits'
    ] LOOP
        EXECUTE pg_catalog.format(
            'ALTER FUNCTION %I.%I%s OWNER TO %I',
            active_schema,
            boundary_function,
            CASE boundary_function
                WHEN 'provider_control_list_provider_provision_waits' THEN '(text, text, integer)'
                ELSE '()'
            END,
            migration_role
        );
        EXECUTE pg_catalog.format(
            'ALTER FUNCTION %I.%I%s SECURITY DEFINER',
            active_schema,
            boundary_function,
            CASE boundary_function
                WHEN 'provider_control_list_provider_provision_waits' THEN '(text, text, integer)'
                ELSE '()'
            END
        );
        EXECUTE pg_catalog.format(
            'ALTER FUNCTION %I.%I%s SET search_path TO pg_catalog, %I, pg_temp',
            active_schema,
            boundary_function,
            CASE boundary_function
                WHEN 'provider_control_list_provider_provision_waits' THEN '(text, text, integer)'
                ELSE '()'
            END,
            active_schema
        );
        EXECUTE pg_catalog.format(
            'REVOKE ALL ON FUNCTION %I.%I%s FROM PUBLIC',
            active_schema,
            boundary_function,
            CASE boundary_function
                WHEN 'provider_control_list_provider_provision_waits' THEN '(text, text, integer)'
                ELSE '()'
            END
        );
    END LOOP;
END
$provider_provision_wait_recovery_posture$;

COMMENT ON TABLE provider_provision_wait_recovery IS
    'Secret-free provider-neutral restart index for pending provision waits; jobs and provider ledger remain authoritative.';
COMMENT ON INDEX idx_jobs_provider_provision_wait_operation IS
    'Exact tenant and provider operation lookup for durable provider-provision wait rehydration.';
COMMENT ON FUNCTION provider_control_refresh_provider_provision_wait() IS
    'Refreshes the provider-neutral provision wait scheduling projection under exact tenant scope.';
COMMENT ON FUNCTION provider_control_list_provider_provision_waits(text, text, integer) IS
    'Bounded secret-free operation-reference discovery for restart-safe provider-provision wait recovery.';
