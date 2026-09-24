-- Migration 094 admits exact decommission claims after a local tombstone.
-- A newly opened operation still needs its coordinator-owned requested to
-- accepted receipt before any claim or provider call exists. Admit only that
-- immutable no-side-effect transition; every later continuation still needs
-- the consumed exact execution claim established by Migration 094.
DO $$
DECLARE
    function_definition text;
    current_pattern text;
    continuation_predicate text;
    updated_definition text;
BEGIN
    SELECT pg_get_functiondef('provider_operation_head_update_guard()'::regprocedure)
    INTO function_definition;
    current_pattern :=
        'OR \([[:space:]]+OLD.operation = ''decommission''[[:space:]]+AND claimed_result_append[[:space:]]+\)[[:space:]]+\)[[:space:]]+\) THEN';
    continuation_predicate :=
        'OR (
                    OLD.operation = ''decommission''
                    AND (
                        claimed_result_append
                        OR (
                            OLD.status = ''pending''
                            AND OLD.phase = ''requested''
                            AND NEW.status = ''pending''
                            AND NEW.phase = ''accepted''
                        )
                    )
                )
            )
       ) THEN';
    updated_definition := regexp_replace(
        function_definition,
        current_pattern,
        continuation_predicate
    );
    IF function_definition IS NULL
       OR updated_definition IS NOT DISTINCT FROM function_definition THEN
        RAISE EXCEPTION 'operation-head terminal decommission acceptance fence is not at the expected version';
    END IF;
    EXECUTE updated_definition;
END;
$$;

COMMENT ON FUNCTION provider_operation_head_update_guard() IS
    'Fences immutable operation heads, allowing exact decommission acceptance and claimed continuation or bounded provision continuation after a tombstone.';
