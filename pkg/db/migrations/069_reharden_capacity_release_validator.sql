-- Migration 068 replaces this trigger function to support resource-free
-- teardown. CREATE OR REPLACE preserves the function identity but resets its
-- configured search_path to the migration session posture. Restore the exact
-- SECURITY DEFINER boundary required by provider-control startup.
DO $provider_control_reharden_capacity_release_validator$
DECLARE
    active_schema text := current_schema();
BEGIN
    EXECUTE pg_catalog.format(
        'ALTER FUNCTION %I.managed_runtime_capacity_release_validate_insert() SECURITY DEFINER',
        active_schema
    );
    EXECUTE pg_catalog.format(
        'ALTER FUNCTION %I.managed_runtime_capacity_release_validate_insert() SET search_path TO pg_catalog, %I, pg_temp',
        active_schema,
        active_schema
    );
    EXECUTE pg_catalog.format(
        'REVOKE ALL ON FUNCTION %I.managed_runtime_capacity_release_validate_insert() FROM PUBLIC',
        active_schema
    );
END;
$provider_control_reharden_capacity_release_validator$;
