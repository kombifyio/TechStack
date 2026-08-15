-- Registry writes run under the tenant-scoped application role, while the
-- secret-free wake directories are intentionally not writable by that role.
-- Keep the trigger as the only write path and execute it with the migration
-- owner's narrowly scoped authority and a fixed schema search path.
DO $server_registry_wake_trigger_authority$
DECLARE
    active_schema text := current_schema();
    boundary_function text;
BEGIN
    FOREACH boundary_function IN ARRAY ARRAY[
        'server_registry_wake_sweep_tenant',
        'server_registry_wake_outbox_prune_tenant'
    ] LOOP
        EXECUTE pg_catalog.format(
            'ALTER FUNCTION %I.%I() SECURITY DEFINER',
            active_schema,
            boundary_function
        );
        EXECUTE pg_catalog.format(
            'ALTER FUNCTION %I.%I() SET search_path TO pg_catalog, %I',
            active_schema,
            boundary_function,
            active_schema
        );
    END LOOP;
END;
$server_registry_wake_trigger_authority$;

COMMENT ON FUNCTION server_registry_wake_sweep_tenant() IS
    'Tenant-scoped server writes wake the secret-free observation directory through a hardened trigger authority.';
COMMENT ON FUNCTION server_registry_wake_outbox_prune_tenant() IS
    'Tenant-scoped outbox writes wake the secret-free prune directory through a hardened trigger authority.';
