-- Background loops on the separated application role claim the notification
-- outbox and page the secret-free wake directories. Those tables stay owned
-- by the migration role and keep their existing RLS posture (the directories
-- are intentionally not tenant-scoped). SECURITY DEFINER triggers already
-- write the registry directories; this migration does not grant that insert
-- path and does not grant to PUBLIC.
--
-- The named role is absent on databases that install capabilities later.
-- Skip the grant there. Refuse a superuser or BYPASSRLS role rather than
-- widening one.

SET LOCAL lock_timeout = '5s';
SELECT pg_catalog.set_config(
    'search_path',
    pg_catalog.quote_ident(pg_catalog.current_schema()) || ', pg_catalog, pg_temp',
    true
);

DO $application_loop_table_grants$
DECLARE
    active_schema text := pg_catalog.current_schema();
    application_role text := 'techstack_application';
    grant_target record;
    privilege text;
BEGIN
    IF pg_catalog.to_regrole(application_role) IS NULL THEN
        RETURN;
    END IF;
    IF EXISTS (
        SELECT 1
        FROM pg_catalog.pg_roles
        WHERE rolname = application_role
          AND (rolsuper OR rolbypassrls)
    ) THEN
        RAISE EXCEPTION 'application loop grants refuse a superuser or BYPASSRLS role'
            USING ERRCODE = '42501';
    END IF;

    FOR grant_target IN
        SELECT *
        FROM (
            VALUES
                -- Outbox worker claims (SELECT FOR UPDATE) and records delivery.
                -- The same role enqueues rows; nothing deletes them.
                ('techstack_notification_outbox', ARRAY['SELECT', 'INSERT', 'UPDATE']::text[]),
                -- Signal worker lists and retires wake rows. Retry updates
                -- available_at, so the invoker wake trigger also inserts.
                ('ril_signal_delivery_tenants', ARRAY['SELECT', 'INSERT', 'UPDATE', 'DELETE']::text[]),
                -- Maintenance sweep lists and compacts. Job insert still runs
                -- the invoker wake trigger as this role.
                ('server_maintenance_tenants', ARRAY['SELECT', 'INSERT', 'UPDATE', 'DELETE']::text[]),
                -- Drive-health sweep lists tenants. Evidence refresh upserts
                -- the same directory; it never deletes a tenant row.
                ('guard_drive_health_tenants', ARRAY['SELECT', 'INSERT', 'UPDATE']::text[]),
                -- Observation sweep lists and compacts. Migration 083's
                -- SECURITY DEFINER trigger remains the only insert path.
                ('server_registry_sweep_tenants', ARRAY['SELECT', 'UPDATE', 'DELETE']::text[]),
                ('server_registry_outbox_prune_tenants', ARRAY['SELECT', 'UPDATE', 'DELETE']::text[])
        ) AS grant_row(table_name, privileges)
    LOOP
        EXECUTE pg_catalog.format(
            'REVOKE ALL ON TABLE %I.%I FROM PUBLIC',
            active_schema,
            grant_target.table_name
        );
        FOREACH privilege IN ARRAY grant_target.privileges
        LOOP
            EXECUTE pg_catalog.format(
                'GRANT %s ON TABLE %I.%I TO %I',
                privilege,
                active_schema,
                grant_target.table_name,
                application_role
            );
        END LOOP;
    END LOOP;
END
$application_loop_table_grants$;
