-- A zero-resource terminal provision can be discovered after teardown has
-- already tombstoned its exact RuntimeServer. Admit only that fully terminal
-- lease/server custody; active and ambiguous provision heads retain the
-- original stale-custody fence.
DO $terminal_provision_resolution_tombstone$
DECLARE
    function_name text;
    function_definition text;
    updated_definition text;
    decommissioned_at_fence text := $predicate$OR (
            server_decommissioned_at IS NOT NULL
            AND NOT (
                terminal_failure
                AND server_lifecycle_state = 'decommissioned'
                AND server_desired_state = 'absent'
                AND live_cancelled_at IS NOT NULL
                AND live_desired_state = 'absent'
            )
       )$predicate$;
BEGIN
    FOREACH function_name IN ARRAY ARRAY[
        'provider_provision_discovery_validate_insert',
        'provider_provision_resolution_validate_insert'
    ] LOOP
        SELECT pg_get_functiondef(
            pg_catalog.to_regprocedure(function_name || '()')
        ) INTO function_definition;

        updated_definition := replace(
            function_definition,
            'OR server_decommissioned_at IS NOT NULL',
            decommissioned_at_fence
        );
        IF function_definition IS NULL
           OR updated_definition IS NOT DISTINCT FROM function_definition THEN
            RAISE EXCEPTION '% decommissioned-at fence is not at the expected version',
                function_name;
        END IF;

        function_definition := updated_definition;
        updated_definition := replace(
            function_definition,
            '''planned'', ''provisioning'', ''failed'', ''decommissioning''',
            '''planned'', ''provisioning'', ''failed'', ''decommissioning'', ''decommissioned'''
        );
        IF updated_definition IS NOT DISTINCT FROM function_definition THEN
            RAISE EXCEPTION '% terminal lifecycle fence is not at the expected version',
                function_name;
        END IF;
        EXECUTE updated_definition;
    END LOOP;
END;
$terminal_provision_resolution_tombstone$;

COMMENT ON FUNCTION provider_provision_discovery_validate_insert() IS
    'Validates exact AMO discovery custody, including a fully tombstoned zero-resource terminal provision.';
COMMENT ON FUNCTION provider_provision_resolution_validate_insert() IS
    'Validates exact AMO resolution custody, including a fully tombstoned zero-resource terminal provision.';
