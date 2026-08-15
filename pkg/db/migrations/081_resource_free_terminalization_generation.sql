-- Resource-free teardown may follow an owner-requested decommission intent
-- that has already advanced the RuntimeServer generation. Keep the sealed
-- provision generation as a lower bound, while requiring the terminalization
-- row to bind the exact live server generation and provision receipt head.
DO $$
DECLARE
    function_definition text;
    current_predicate constant text :=
        'OR operation_server_generation IS DISTINCT FROM NEW.server_generation';
    generation_fenced_predicate constant text :=
        'OR operation_server_generation IS NULL
       OR operation_server_generation < 1
       OR NEW.server_generation < operation_server_generation';
BEGIN
    SELECT pg_get_functiondef(
        'provider_resource_free_terminalization_validate_insert()'::regprocedure
    )
    INTO function_definition;

    IF function_definition IS NULL
       OR function_definition NOT LIKE '%' || current_predicate || '%'
       OR function_definition LIKE '%' || generation_fenced_predicate || '%' THEN
        RAISE EXCEPTION
            'resource-free terminalization generation validator is not at the expected version';
    END IF;

    function_definition := replace(
        function_definition,
        current_predicate,
        generation_fenced_predicate
    );
    EXECUTE function_definition;
END;
$$;

COMMENT ON FUNCTION provider_resource_free_terminalization_validate_insert() IS
    'Validates resource-free teardown against the exact provision head and live server generation, allowing only forward teardown generation advancement.';
