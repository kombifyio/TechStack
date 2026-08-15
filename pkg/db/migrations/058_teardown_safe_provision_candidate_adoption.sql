-- A canceled AMO provision can still own a real provider server whose create
-- response was lost. Operator discovery/adoption must be able to recover that
-- exact handle so ordinary decommission can delete it. Cancellation continues
-- to fence every normal provision transition; only the transaction-bound
-- accepted -> resources_bound operator adoption exception is admitted.

SET LOCAL lock_timeout = '5s';
SELECT pg_catalog.set_config(
    'search_path',
    pg_catalog.quote_ident(pg_catalog.current_schema()) || ', pg_catalog, pg_temp',
    true
);

DO $$
DECLARE
    definition text;
    changed text;
    legacy_operator_assignment text := $fragment$
            operator_adoption :=
                OLD.operation = 'provision'
                AND OLD.provision_dispatch_mode = 'at_most_once_dispatch_manual_reconcile'
                AND OLD.status = 'pending'
                AND OLD.phase = 'accepted'
                AND NEW.status = 'pending'
                AND NEW.phase = 'resources_bound';
$fragment$;
BEGIN
    definition := pg_get_functiondef(
        'provider_provision_discovery_validate_insert()'::regprocedure
    );
    changed := regexp_replace(
        definition,
        E'\\s+OR \\(\\s+NOT terminal_failure\\s+AND \\(\\s+live_cancelled_at IS NOT NULL\\s+OR live_desired_state NOT IN \\(''running'', ''stopped''\\)\\s+OR server_desired_state = ''absent''\\s+OR server_lifecycle_state IN \\(''decommissioning'', ''decommissioned''\\)\\s+\\)\\s+\\)',
        '',
        'n'
    );
    IF changed IS NOT DISTINCT FROM definition THEN
        RAISE EXCEPTION 'provider discovery custody guard did not match its expected predecessor';
    END IF;
    EXECUTE changed;

    definition := pg_get_functiondef(
        'provider_provision_resolution_validate_insert()'::regprocedure
    );
    changed := regexp_replace(
        definition,
        E'\\s+OR \\(\\s+NOT terminal_failure\\s+AND \\(\\s+teardown_requested\\s+OR live_desired_state NOT IN \\(''running'', ''stopped''\\)\\s+OR server_lifecycle_state = ''decommissioned''\\s+\\)\\s+\\)',
        '',
        'n'
    );
    IF changed IS NOT DISTINCT FROM definition THEN
        RAISE EXCEPTION 'provider resolution custody guard did not match its expected predecessor';
    END IF;
    EXECUTE changed;

    definition := pg_get_functiondef(
        'provider_execution_immutable_update()'::regprocedure
    );
    changed := definition;
    IF position('AND NOT operator_adoption' IN changed) = 0 THEN
        changed := replace(
            changed,
            '            teardown_requested :=',
            legacy_operator_assignment || '            teardown_requested :='
        );
        changed := replace(
            changed,
            '            ELSIF teardown_requested THEN',
            '            ELSIF teardown_requested AND NOT operator_adoption THEN'
        );
    END IF;
    IF position('AND NOT operator_adoption' IN changed) = 0 THEN
        RAISE EXCEPTION 'provider operation teardown guard is not operator-adoption aware';
    END IF;
    IF changed IS DISTINCT FROM definition THEN
        EXECUTE changed;
    END IF;
END;
$$;

COMMENT ON FUNCTION provider_provision_discovery_validate_insert() IS
    'Validates append-only AMO discovery, including exact provider-handle recovery needed by an already requested teardown.';
COMMENT ON FUNCTION provider_provision_resolution_validate_insert() IS
    'Validates append-only AMO decisions, including exact-candidate adoption solely to restore cleanup custody after cancellation.';
