-- A terminal RuntimeServer tombstone is local state, not provider-absence
-- evidence. Keep only an exact decommission operation runnable so its sealed
-- target graph can reach a definitive provider absence receipt and atomically
-- release capacity. Provision keeps its existing read-only exception;
-- reconcile and every unclaimed head mutation remain fenced.
DO $$
DECLARE
    function_definition text;
    current_pattern text;
    continuation_predicate text;
    updated_definition text;
BEGIN
    SELECT pg_get_functiondef('provider_execution_claim_credential_guard()'::regprocedure)
    INTO function_definition;
    current_pattern :=
        'AND NOT \([[:space:]]+command_operation = ''provision''[[:space:]]+AND command_phase = ''resources_bound''[[:space:]]+AND NEW.claim_access = ''read_only''[[:space:]]+\) THEN';
    continuation_predicate :=
        'AND NOT (
            (
                command_operation = ''provision''
                AND command_phase = ''resources_bound''
                AND NEW.claim_access = ''read_only''
            )
            OR command_operation = ''decommission''
       ) THEN';
    updated_definition := regexp_replace(
        function_definition,
        current_pattern,
        continuation_predicate
    );
    IF function_definition IS NULL
       OR updated_definition IS NOT DISTINCT FROM function_definition THEN
        RAISE EXCEPTION 'credential terminal-continuation fence is not at the expected version';
    END IF;
    EXECUTE updated_definition;

    SELECT pg_get_functiondef('provider_execution_claim_runtime_generation_guard()'::regprocedure)
    INTO function_definition;
    current_pattern :=
        'AND NOT \([[:space:]]+operation_kind = ''provision''[[:space:]]+AND operation_phase = ''resources_bound''[[:space:]]+AND NEW.claim_access = ''read_only''[[:space:]]+\)[[:space:]]+\) THEN';
    continuation_predicate :=
        'AND NOT (
                (
                    operation_kind = ''provision''
                    AND operation_phase = ''resources_bound''
                    AND NEW.claim_access = ''read_only''
                )
                OR operation_kind = ''decommission''
            )
       ) THEN';
    updated_definition := regexp_replace(
        function_definition,
        current_pattern,
        continuation_predicate
    );
    IF function_definition IS NULL
       OR updated_definition IS NOT DISTINCT FROM function_definition THEN
        RAISE EXCEPTION 'runtime-generation terminal-continuation fence is not at the expected version';
    END IF;
    EXECUTE updated_definition;

    SELECT pg_get_functiondef('provider_operation_head_update_guard()'::regprocedure)
    INTO function_definition;
    current_pattern :=
        'AND NOT \([[:space:]]+OLD.operation = ''provision''[[:space:]]+AND OLD.phase = ''resources_bound''[[:space:]]+AND claimed_result_append[[:space:]]+AND \([[:space:]]+live_cancelled_at IS NOT NULL[[:space:]]+OR live_lease_desired_state = ''absent''[[:space:]]+OR live_server_desired_state = ''absent''[[:space:]]+OR live_server_lifecycle_state IN \(''decommissioning'', ''decommissioned''\)[[:space:]]+\)[[:space:]]+\)[[:space:]]+\) THEN';
    continuation_predicate :=
        'AND NOT (
                (
                    OLD.operation = ''provision''
                    AND OLD.phase = ''resources_bound''
                    AND claimed_result_append
                    AND (
                        live_cancelled_at IS NOT NULL
                        OR live_lease_desired_state = ''absent''
                        OR live_server_desired_state = ''absent''
                        OR live_server_lifecycle_state IN (''decommissioning'', ''decommissioned'')
                    )
                )
                OR (
                    OLD.operation = ''decommission''
                    AND claimed_result_append
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
        RAISE EXCEPTION 'operation-head terminal-continuation fence is not at the expected version';
    END IF;
    EXECUTE updated_definition;
END;
$$;

COMMENT ON FUNCTION provider_execution_claim_credential_guard() IS
    'Fences credential authority, allowing only tombstoned exact decommission or provision/resources_bound read-only continuation.';
COMMENT ON FUNCTION provider_execution_claim_runtime_generation_guard() IS
    'Fences runtime generation, allowing only tombstoned exact decommission or provision/resources_bound read-only continuation.';
COMMENT ON FUNCTION provider_operation_head_update_guard() IS
    'Fences immutable operation heads, allowing claimed exact decommission or bounded provision continuation after a tombstone.';
