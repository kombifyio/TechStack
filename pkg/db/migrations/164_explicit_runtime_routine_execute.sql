-- The initial chain left these exact Techstack routines with PostgreSQL's
-- implicit PUBLIC EXECUTE. Both bounded application preparation and signing
-- custody reject that ambient authority. Retire only the observed routines;
-- unrelated routines and explicit purpose grants stay intact.
--
-- Trigger execution itself needs no caller EXECUTE. The catalog validators run
-- only during owner catalog writes; the claim-access helper runs inside existing
-- SECURITY DEFINER provider guards. The invoker capacity trigger also calls the
-- pure policy digest: InstallApplicationCapabilities grants that helper after
-- this migration, and provider preparation already grants it explicitly.
SET LOCAL lock_timeout = '5s';

DO $explicit_runtime_execute$
DECLARE
    active_schema text := current_schema();
    signature text;
    routine oid;
BEGIN
    FOREACH signature IN ARRAY ARRAY[
        'enforce_managed_runtime_generation_transition()',
        'managed_runtime_capacity_policy_digest(text,text,text,text,text,integer,text)',
        'project_server_provider_resource_bindings()',
        'provider_absence_observation_reject_mutation()',
        'provider_catalog_capability_json_within_limits(jsonb)',
        'provider_catalog_capability_snapshot_is_valid(jsonb)',
        'provider_catalog_capability_string_array_is_valid(jsonb)',
        'provider_catalog_managed_bootstrap_network_is_valid(jsonb)',
        'provider_catalog_reject_mutation()',
        'provider_catalog_version_transition_guard()',
        'provider_credential_handle_reject_delete()',
        'provider_credential_handle_require_claim_authority()',
        'provider_credential_handle_update_guard()',
        'provider_execution_claim_access_immutable()',
        'provider_execution_claim_reject_delete()',
        'provider_execution_claim_update()',
        'provider_execution_reject_delete()',
        'provider_expected_execution_claim_access(text,text,text)',
        'reject_managed_runtime_slot_mutation()',
        'reject_ril_audit_mutation()',
        'runtime_lease_execution_authority_reject_legacy_insert()',
        'runtime_lease_execution_authority_reject_mutation()',
        'runtime_lease_generation_projection_guard()',
        'runtime_lease_idempotency_reject_mutation()',
        'server_maintenance_wake_tenant()',
        'server_provider_resource_binding_guard()',
        'server_registry_wake_outbox_prune_tenant()',
        'server_registry_wake_sweep_tenant()',
        'set_updated_at()',
        'substrate_guest_custody_insert_guard()',
        'techstack_vm_lease_operation_journal_reject_mutation()',
        'techstack_vm_lease_reject_legacy_execution_metadata()',
        'typed_agent_commands_drop_sealed_delivery()',
        'wake_ril_signal_delivery_tenant()'
    ] LOOP
        routine := to_regprocedure(format('%I.%s', active_schema, signature));
        IF routine IS NULL OR NOT EXISTS (
            SELECT 1 FROM pg_catalog.pg_proc p
            JOIN pg_catalog.pg_roles owner ON owner.oid=p.proowner
            WHERE p.oid=routine AND owner.rolname=current_user
        ) THEN
            RAISE EXCEPTION 'runtime routine is absent or not owned by the migration authority: %', signature
                USING ERRCODE = '42501';
        END IF;
        EXECUTE format('REVOKE EXECUTE ON FUNCTION %I.%s FROM PUBLIC', active_schema, signature);
    END LOOP;

    -- PostgreSQL grants PUBLIC EXECUTE to future functions unless the creator's
    -- database-global default is changed. An IN SCHEMA revoke cannot cancel it.
    -- Only this migration owner in this database changes; no foreign defaults
    -- or existing routines outside the exact list above are touched.
    EXECUTE format('ALTER DEFAULT PRIVILEGES FOR ROLE %I REVOKE EXECUTE ON FUNCTIONS FROM PUBLIC', current_user);
END;
$explicit_runtime_execute$;
