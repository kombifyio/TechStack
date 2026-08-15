-- A provision candidate adopted after an ambiguous at-most-once create is the
-- exact provider graph ordinary decommission must delete. The Go-side target
-- and absence checks already bind that authority to the current resolution
-- receipt; apply the identical predicate to the database execution-claim
-- guard so delete polling cannot lose custody after the first provider call.

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
    predicate_pattern text := E'OR \\(\\s*source_operation\\.status = ''failed''\\s*AND source_operation\\.phase = ''failed''\\s*\\)';
    adopted_predicate text := $fragment$
                              OR EXISTS (
                                  SELECT 1
                                  FROM provider_provision_resolution_decisions AS decision
                                  WHERE decision.tenant_id = source_operation.tenant_id
                                    AND decision.operation_id = source_operation.operation_id
                                    AND decision.outcome = 'adopted_exact_candidate'
                                    AND decision.selected_candidate_digest IS NOT NULL
                                    AND decision.result_receipt_sequence = source_operation.head_sequence
                                    AND decision.result_receipt_digest = source_operation.head_receipt_digest
                              )
$fragment$;
BEGIN
    definition := pg_get_functiondef(
        'provider_execution_claim_runtime_generation_guard()'::regprocedure
    );
    IF regexp_count(definition, predicate_pattern, 1, 'n') <> 2 THEN
        RAISE EXCEPTION 'provider execution claim custody guard does not contain its two expected decommission predicates';
    END IF;
    changed := regexp_replace(
        definition,
        predicate_pattern,
        E'\\&' || adopted_predicate,
        'gn'
    );
    IF changed IS NOT DISTINCT FROM definition
       OR regexp_count(changed, E'decision\\.outcome = ''adopted_exact_candidate''', 1, 'n') < 2 THEN
        RAISE EXCEPTION 'provider execution claim custody guard is not operator-adoption aware';
    END IF;
    EXECUTE changed;
END;
$$;

COMMENT ON FUNCTION provider_execution_claim_runtime_generation_guard() IS
    'Fences provider claims to the exact live generation and accepts only a current, digest-bound adopted candidate as decommission custody.';
