-- A zero-resource terminal provision may settle after its exact RuntimeServer
-- was independently decommissioned. Keep every other terminalization custody
-- fence intact while admitting that complete, teardown-bound tombstone.
DO $resource_free_terminalization_tombstone$
DECLARE
    function_definition text;
    updated_definition text;
    decommissioned_at_fence text := $predicate$OR (
            live_server_decommissioned_at IS NOT NULL
            AND NOT (
                operation_status = 'failed'
                AND operation_phase = 'failed'
                AND NEW.authority = 'no_candidate_observed'
                AND live_server_lifecycle_state = 'decommissioned'
                AND live_server_desired_state = 'absent'
                AND live_cancelled_at IS NOT NULL
                AND live_lease_desired_state = 'absent'
            )
       )$predicate$;
BEGIN
    SELECT pg_get_functiondef(
        'provider_resource_free_terminalization_validate_insert()'::regprocedure
    ) INTO function_definition;

    updated_definition := replace(
        function_definition,
        'OR live_server_decommissioned_at IS NOT NULL',
        decommissioned_at_fence
    );
    IF function_definition IS NULL
       OR updated_definition IS NOT DISTINCT FROM function_definition THEN
        RAISE EXCEPTION 'resource-free terminalization decommissioned-at fence is not at the expected version';
    END IF;

    function_definition := updated_definition;
    updated_definition := replace(
        function_definition,
        '''planned'', ''provisioning'', ''failed'', ''decommissioning''',
        '''planned'', ''provisioning'', ''failed'', ''decommissioning'', ''decommissioned'''
    );
    IF updated_definition IS NOT DISTINCT FROM function_definition THEN
        RAISE EXCEPTION 'resource-free terminalization lifecycle fence is not at the expected version';
    END IF;
    EXECUTE updated_definition;
END;
$resource_free_terminalization_tombstone$;

COMMENT ON FUNCTION provider_resource_free_terminalization_validate_insert() IS
    'Validates exact resource-free terminalization custody, including a fully tombstoned zero-resource terminal provision.';
