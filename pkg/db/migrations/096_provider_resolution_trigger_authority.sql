-- Provision-resolution validation locks immutable operation custody while the
-- restricted runtime role appends an observation or decision. Keep those row
-- locks inside the migration-owned trigger boundary; granting UPDATE on the
-- immutable receipt, dispatch-guard, or observation tables would broaden the
-- runtime role beyond its append-only authority.
DO $provider_resolution_trigger_authority$
DECLARE
    active_schema text := current_schema();
    boundary_function text;
BEGIN
    FOREACH boundary_function IN ARRAY ARRAY[
        'provider_provision_discovery_validate_insert',
        'provider_provision_resolution_validate_insert'
    ] LOOP
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
END;
$provider_resolution_trigger_authority$;

COMMENT ON FUNCTION provider_provision_discovery_validate_insert() IS
    'Migration-owned append-only discovery validator; row-lock authority is available only through its table trigger.';
COMMENT ON FUNCTION provider_provision_resolution_validate_insert() IS
    'Migration-owned append-only resolution validator; row-lock authority is available only through its table trigger.';
