-- A provider resource may still exist after the RuntimeServer received its
-- terminal local tombstone. Admit only the already-dispatched provision's
-- resources_bound read-only poll and its consumed-claim result append. This
-- cannot authorize prepare/create, reconcile, or decommission mutations, and
-- the projection layer keeps teardown intent dominant over late presence.
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
        'IF[[:space:]]+live_server_lifecycle_state IS NOT DISTINCT FROM ''decommissioned''[[:space:]]+OR[[:space:]]+live_server_decommissioned_at IS NOT NULL THEN';
    continuation_predicate :=
        'IF (
            live_server_lifecycle_state IS NOT DISTINCT FROM ''decommissioned''
            OR live_server_decommissioned_at IS NOT NULL
       )
       AND NOT (
            command_operation = ''provision''
            AND command_phase = ''resources_bound''
            AND NEW.claim_access = ''read_only''
       ) THEN';
    updated_definition := regexp_replace(
        function_definition,
        current_pattern,
        continuation_predicate
    );
    IF function_definition IS NULL
       OR updated_definition IS NOT DISTINCT FROM function_definition THEN
        RAISE EXCEPTION 'credential tombstone fence is not at the expected version';
    END IF;
    EXECUTE updated_definition;

    SELECT pg_get_functiondef('provider_execution_claim_runtime_generation_guard()'::regprocedure)
    INTO function_definition;
    current_pattern :=
        'OR[[:space:]]+live_server_lifecycle_state = ''decommissioned''[[:space:]]+OR[[:space:]]+live_server_decommissioned_at IS NOT NULL THEN';
    continuation_predicate :=
        'OR (
            (
                live_server_lifecycle_state = ''decommissioned''
                OR live_server_decommissioned_at IS NOT NULL
            )
            AND NOT (
                operation_kind = ''provision''
                AND operation_phase = ''resources_bound''
                AND NEW.claim_access = ''read_only''
            )
       ) THEN';
    updated_definition := regexp_replace(
        function_definition,
        current_pattern,
        continuation_predicate
    );
    IF function_definition IS NULL
       OR updated_definition IS NOT DISTINCT FROM function_definition THEN
        RAISE EXCEPTION 'runtime-generation tombstone fence is not at the expected version';
    END IF;
    EXECUTE updated_definition;

    SELECT pg_get_functiondef('provider_operation_head_update_guard()'::regprocedure)
    INTO function_definition;
    current_pattern :=
        'OR[[:space:]]+live_server_lifecycle_state = ''decommissioned''[[:space:]]+OR[[:space:]]+live_server_decommissioned_at IS NOT NULL THEN';
    continuation_predicate :=
        'OR (
            (
                live_server_lifecycle_state = ''decommissioned''
                OR live_server_decommissioned_at IS NOT NULL
            )
            AND NOT (
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
       ) THEN';
    updated_definition := regexp_replace(
        function_definition,
        current_pattern,
        continuation_predicate
    );
    IF function_definition IS NULL
       OR updated_definition IS NOT DISTINCT FROM function_definition THEN
        RAISE EXCEPTION 'operation-head tombstone fence is not at the expected version';
    END IF;
    EXECUTE updated_definition;
END;
$$;

COMMENT ON FUNCTION provider_execution_claim_credential_guard() IS
    'Fences credential authority, allowing only tombstoned provision/resources_bound read-only continuation.';
COMMENT ON FUNCTION provider_execution_claim_runtime_generation_guard() IS
    'Fences runtime generation, allowing only tombstoned provision/resources_bound read-only continuation.';
COMMENT ON FUNCTION provider_operation_head_update_guard() IS
    'Fences immutable operation heads, allowing an exact consumed read-only provision result after teardown tombstone.';
