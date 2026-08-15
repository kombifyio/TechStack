-- Provider discovery is collected by an application host while the insert
-- fence is evaluated by PostgreSQL. Permit only a small, explicit clock-skew
-- window; all immutable custody fields and the lower guarded_at bound remain
-- enforced by the existing trigger function.
DO $$
DECLARE
    function_definition text;
    current_predicate constant text := 'OR NEW.collected_at > clock_timestamp() THEN';
    skew_tolerant_predicate constant text :=
        'OR NEW.collected_at > clock_timestamp() + interval ''30 seconds'' THEN';
BEGIN
    SELECT pg_get_functiondef(
        'provider_provision_discovery_validate_insert()'::regprocedure
    )
    INTO function_definition;

    IF function_definition IS NULL
       OR function_definition NOT LIKE '%' || current_predicate || '%'
       OR function_definition LIKE '%' || skew_tolerant_predicate || '%' THEN
        RAISE EXCEPTION
            'provider provision discovery clock-skew predicate is not at the expected version';
    END IF;

    function_definition := replace(
        function_definition,
        current_predicate,
        skew_tolerant_predicate
    );
    EXECUTE function_definition;
END;
$$;

COMMENT ON FUNCTION provider_provision_discovery_validate_insert() IS
    'Validates append-only AMO provision discovery against immutable custody, allowing at most 30 seconds of application/database clock skew.';
