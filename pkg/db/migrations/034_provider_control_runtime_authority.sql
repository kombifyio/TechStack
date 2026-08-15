-- Bounded provider-control runtime authority.
--
-- The provider scheduler must discover tenant work without granting its login
-- role BYPASSRLS or direct cross-tenant access to provider_operations. Keep a
-- secret-free, migration-owned directory instead. A transaction-bound trigger
-- refreshes the exact runnable count while the caller is still scoped to the
-- operation tenant; the runtime can read only a bounded keyset projection via
-- the SECURITY DEFINER function below.

SET LOCAL lock_timeout = '5s';
SELECT pg_catalog.set_config(
    'search_path',
    pg_catalog.quote_ident(pg_catalog.current_schema()) || ', pg_catalog, pg_temp',
    true
);

CREATE TABLE IF NOT EXISTS provider_control_runnable_tenants (
    tenant_id text PRIMARY KEY CHECK (BTRIM(tenant_id) <> ''),
    runnable_operation_count integer NOT NULL
        CHECK (runnable_operation_count > 0),
    refreshed_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    FOREIGN KEY (tenant_id)
        REFERENCES techstack_tenants (id)
        ON UPDATE RESTRICT ON DELETE RESTRICT
);

REVOKE ALL ON TABLE provider_control_runnable_tenants FROM PUBLIC;

-- Serialize refreshes per tenant so two concurrent terminal transitions cannot
-- each observe the other transaction's old runnable row and leave a stale
-- directory entry behind.
CREATE OR REPLACE FUNCTION provider_control_refresh_runnable_tenant()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path FROM CURRENT
AS $$
DECLARE
    operation_tenant_id text := COALESCE(NEW.tenant_id, OLD.tenant_id);
    scoped_tenant_id text := NULLIF(current_setting('app.tenant_id', true), '');
    runnable_count integer;
BEGIN
    IF operation_tenant_id IS NULL OR BTRIM(operation_tenant_id) = '' THEN
        RAISE EXCEPTION 'provider-control runnable tenant refresh requires an exact tenant'
            USING ERRCODE = '55000';
    END IF;
    IF scoped_tenant_id IS DISTINCT FROM operation_tenant_id THEN
        RAISE EXCEPTION 'provider-control runnable tenant refresh requires the exact tenant scope'
            USING ERRCODE = '42501';
    END IF;

    PERFORM pg_catalog.pg_advisory_xact_lock(
        pg_catalog.hashtextextended(
            'providercontrol.runnable-tenant/v1:' || operation_tenant_id,
            0
        )
    );

    SELECT COUNT(*)::integer
    INTO runnable_count
    FROM provider_operations AS operation
    WHERE operation.tenant_id = operation_tenant_id
      AND operation.command_json->>'schema_version' = 'techstack.provider-control-operation/v1'
      AND operation.command_json->>'execution_authority' = 'techstack_provider_control'
      AND operation.provision_dispatch_mode <> 'blocked'
      AND operation.status = 'pending'
      AND operation.phase NOT IN ('planned', 'present', 'absent', 'failed', 'denied')
      AND NOT (
          operation.operation = 'provision'
          AND operation.phase = 'accepted'
          AND EXISTS (
              SELECT 1
              FROM provider_provision_dispatch_guards AS dispatch_guard
              WHERE dispatch_guard.tenant_id = operation.tenant_id
                AND dispatch_guard.lease_id = operation.lease_id
                AND dispatch_guard.resource_generation_id =
                    (operation.command_json #>> '{command,resource_generation_id}')::uuid
                AND (
                    dispatch_guard.operation_id <> operation.operation_id
                    OR dispatch_guard.dispatch_mode = 'at_most_once_dispatch_manual_reconcile'
                    OR dispatch_guard.guard_origin = 'migration_quarantine'
                )
          )
      );

    IF runnable_count > 0 THEN
        INSERT INTO provider_control_runnable_tenants (
            tenant_id,
            runnable_operation_count,
            refreshed_at
        ) VALUES (
            operation_tenant_id,
            runnable_count,
            clock_timestamp()
        )
        ON CONFLICT (tenant_id) DO UPDATE
        SET runnable_operation_count = EXCLUDED.runnable_operation_count,
            refreshed_at = EXCLUDED.refreshed_at;
    ELSE
        DELETE FROM provider_control_runnable_tenants
        WHERE tenant_id = operation_tenant_id;
    END IF;
    RETURN COALESCE(NEW, OLD);
END;
$$;

-- Build the initial directory under the migration transaction. Runtime paths
-- never receive this temporary cross-tenant authority.
LOCK TABLE provider_operations,
    provider_provision_dispatch_guards
    IN SHARE ROW EXCLUSIVE MODE;
ALTER TABLE provider_operations NO FORCE ROW LEVEL SECURITY;
ALTER TABLE provider_operations DISABLE ROW LEVEL SECURITY;
ALTER TABLE provider_provision_dispatch_guards NO FORCE ROW LEVEL SECURITY;
ALTER TABLE provider_provision_dispatch_guards DISABLE ROW LEVEL SECURITY;

TRUNCATE TABLE provider_control_runnable_tenants;
INSERT INTO provider_control_runnable_tenants (
    tenant_id,
    runnable_operation_count,
    refreshed_at
)
SELECT
    operation.tenant_id,
    COUNT(*)::integer,
    clock_timestamp()
FROM provider_operations AS operation
WHERE operation.command_json->>'schema_version' = 'techstack.provider-control-operation/v1'
  AND operation.command_json->>'execution_authority' = 'techstack_provider_control'
  AND operation.provision_dispatch_mode <> 'blocked'
  AND operation.status = 'pending'
  AND operation.phase NOT IN ('planned', 'present', 'absent', 'failed', 'denied')
  AND NOT (
      operation.operation = 'provision'
      AND operation.phase = 'accepted'
      AND EXISTS (
          SELECT 1
          FROM provider_provision_dispatch_guards AS dispatch_guard
          WHERE dispatch_guard.tenant_id = operation.tenant_id
            AND dispatch_guard.lease_id = operation.lease_id
            AND dispatch_guard.resource_generation_id =
                (operation.command_json #>> '{command,resource_generation_id}')::uuid
            AND (
                dispatch_guard.operation_id <> operation.operation_id
                OR dispatch_guard.dispatch_mode = 'at_most_once_dispatch_manual_reconcile'
                OR dispatch_guard.guard_origin = 'migration_quarantine'
            )
      )
  )
GROUP BY operation.tenant_id;

ALTER TABLE provider_provision_dispatch_guards ENABLE ROW LEVEL SECURITY;
ALTER TABLE provider_provision_dispatch_guards FORCE ROW LEVEL SECURITY;
ALTER TABLE provider_operations ENABLE ROW LEVEL SECURITY;
ALTER TABLE provider_operations FORCE ROW LEVEL SECURITY;

DROP TRIGGER IF EXISTS provider_operations_refresh_runnable_tenant
    ON provider_operations;
CREATE TRIGGER provider_operations_refresh_runnable_tenant
AFTER INSERT OR UPDATE OR DELETE ON provider_operations
FOR EACH ROW EXECUTE FUNCTION provider_control_refresh_runnable_tenant();

-- AMO first-claim custody changes the same runnable predicate without changing
-- the operation head. Refresh from the guard table in the same transaction so
-- an ambiguous provision cannot leave a permanent scheduler-directory entry.
DROP TRIGGER IF EXISTS provider_dispatch_guards_refresh_runnable_tenant
    ON provider_provision_dispatch_guards;
CREATE TRIGGER provider_dispatch_guards_refresh_runnable_tenant
AFTER INSERT OR UPDATE OR DELETE ON provider_provision_dispatch_guards
FOR EACH ROW EXECUTE FUNCTION provider_control_refresh_runnable_tenant();

-- This is the only cross-tenant scheduler read exposed to the runtime role.
-- Its output is secret-free, ordered and hard-limited. The runtime role never
-- receives direct table rights on the directory.
CREATE OR REPLACE FUNCTION provider_control_list_runnable_tenants(
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
        RAISE EXCEPTION 'provider-control runnable tenant limit must be between 1 and 101'
            USING ERRCODE = '22023';
    END IF;
    RETURN QUERY
    SELECT directory.tenant_id
    FROM provider_control_runnable_tenants AS directory
    WHERE directory.tenant_id > COALESCE(BTRIM(after_tenant_id), '')
    ORDER BY directory.tenant_id ASC
    LIMIT requested_limit;
END;
$$;

-- Bind the migration and runtime DSNs to the same physical PostgreSQL cluster,
-- database and trusted schema. Database/schema OIDs alone can collide across
-- restored clones; system_identifier cannot.
CREATE OR REPLACE FUNCTION provider_control_runtime_authority()
RETURNS TABLE (
    system_identifier text,
    database_oid bigint,
    database_name text,
    schema_oid bigint,
    schema_name text
)
LANGUAGE sql
SECURITY DEFINER
STABLE
SET search_path FROM CURRENT
AS $$
    SELECT
        control.system_identifier::text,
        database.oid::bigint,
        database.datname,
        namespace.oid::bigint,
        namespace.nspname
    FROM pg_catalog.pg_control_system() AS control
    JOIN pg_catalog.pg_database AS database
      ON database.datname = pg_catalog.current_database()
    JOIN pg_catalog.pg_proc AS authority_function
      ON authority_function.oid = pg_catalog.to_regprocedure(
          'provider_control_runtime_authority()'
      )
    JOIN pg_catalog.pg_namespace AS namespace
      ON namespace.oid = authority_function.pronamespace
$$;

-- Harden every existing migration-owned function which can execute with more
-- authority than the runtime login. Trigger execution does not require a
-- caller EXECUTE grant; direct invocation remains denied.
DO $provider_control_secure_functions$
DECLARE
    active_schema text := current_schema();
    boundary_function text;
BEGIN
    FOREACH boundary_function IN ARRAY ARRAY[
        'provider_execution_immutable_update',
        'provider_provision_dispatch_guard_validate_insert',
        'provider_execution_claim_current_head',
        'provider_execution_claim_credential_guard',
        'provider_control_refresh_runnable_tenant',
        'provider_control_list_runnable_tenants',
        'provider_control_runtime_authority'
    ] LOOP
        EXECUTE pg_catalog.format(
            'ALTER FUNCTION %I.%I%s SECURITY DEFINER',
            active_schema,
            boundary_function,
            CASE boundary_function
                WHEN 'provider_control_list_runnable_tenants' THEN '(text, integer)'
                ELSE '()'
            END
        );
        EXECUTE pg_catalog.format(
            'ALTER FUNCTION %I.%I%s SET search_path TO pg_catalog, %I, pg_temp',
            active_schema,
            boundary_function,
            CASE boundary_function
                WHEN 'provider_control_list_runnable_tenants' THEN '(text, integer)'
                ELSE '()'
            END,
            active_schema
        );
        EXECUTE pg_catalog.format(
            'REVOKE ALL ON FUNCTION %I.%I%s FROM PUBLIC',
            active_schema,
            boundary_function,
            CASE boundary_function
                WHEN 'provider_control_list_runnable_tenants' THEN '(text, integer)'
                ELSE '()'
            END
        );
    END LOOP;
END;
$provider_control_secure_functions$;

COMMENT ON TABLE provider_control_runnable_tenants IS
    'Secret-free migration-owned scheduler directory; direct runtime access is forbidden.';
COMMENT ON FUNCTION provider_control_list_runnable_tenants(text, integer) IS
    'Bounded keyset tenant discovery authority for the dedicated NOBYPASSRLS provider-control runtime role.';
COMMENT ON FUNCTION provider_control_runtime_authority() IS
    'Physical PostgreSQL cluster/database/schema identity used to bind migration and runtime DSNs.';
