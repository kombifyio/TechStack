-- Admit new Ubuntu 26.04 guests; existing explicit 24.04 leases remain unchanged.
CREATE OR REPLACE FUNCTION provider_operation_dispatch_mode_insert_guard()
RETURNS trigger
LANGUAGE plpgsql
SET search_path FROM CURRENT
AS $$
DECLARE
    catalog_dispatch_mode text;
    snapshot_dispatch_mode text;
    catalog_adapter_manifest_hash text;
    snapshot_adapter_manifest_hash text;
BEGIN
    IF NEW.command_json->>'schema_version' IS DISTINCT FROM 'techstack.provider-control-operation/v1'
       OR NEW.command_json->>'execution_authority' IS DISTINCT FROM 'techstack_provider_control' THEN
        RAISE EXCEPTION 'new provider operation requires the native provider-control envelope'
            USING ERRCODE = '55000';
    END IF;

    snapshot_dispatch_mode := NEW.command_json #>> '{execution_profile,provision_dispatch_mode}';
    snapshot_adapter_manifest_hash := NEW.command_json #>> '{execution_profile,adapter_manifest_hash}';
    IF NEW.provision_dispatch_mode = 'blocked'
       OR snapshot_dispatch_mode IS DISTINCT FROM NEW.provision_dispatch_mode
       OR snapshot_adapter_manifest_hash IS NULL
       OR snapshot_adapter_manifest_hash !~ '^sha256:[0-9a-f]{64}$' THEN
        RAISE EXCEPTION 'provider operation must copy an executable catalog dispatch-mode pin'
            USING ERRCODE = '55000';
    END IF;

    -- Worker-held substrate profiles are pinned by the native product module.
    -- Deferred guest custody validates the reservation in this transaction.
    IF NEW.command_json #>> '{execution_profile,provider_id}' = 'proxmox' THEN
        IF NEW.command_json #>> '{command,provider_id}' IS DISTINCT FROM 'proxmox'
           OR NEW.command_json #>> '{execution_profile,catalog_version}' IS DISTINCT FROM 'substrate-v1'
           OR NEW.command_json #>> '{execution_profile,adapter_id}' IS DISTINCT FROM 'proxmox-substrate-v1'
           OR NEW.command_json #>> '{execution_profile,credential_mode}' IS DISTINCT FROM 'worker_held'
           OR NEW.command_json #>> '{execution_profile,runtime_profile_id}' IS DISTINCT FROM 'customer-substrate-v1'
           OR COALESCE(NEW.command_json #>> '{execution_profile,offering_id}', '') NOT IN ('ubuntu-26.04','ubuntu-24.04','haos')
           OR NEW.provision_dispatch_mode IS DISTINCT FROM 'provider_correlation'
           OR snapshot_adapter_manifest_hash IS DISTINCT FROM 'sha256:d0d36d0ce8eca8c262240fb4e3d08603acdb9fd2ebd0182ef1584b9a4c701876' THEN
            RAISE EXCEPTION 'substrate dispatch requires the pinned worker-held profile' USING ERRCODE='55000';
        END IF;
        RETURN NEW;
    END IF;
    SELECT profile.provision_dispatch_mode, profile.adapter_manifest_hash
    INTO catalog_dispatch_mode, catalog_adapter_manifest_hash
    FROM provider_catalog_profiles AS profile
    WHERE profile.catalog_version = NEW.command_json #>> '{execution_profile,catalog_version}'
      AND profile.provider_id = NEW.command_json #>> '{execution_profile,provider_id}'
      AND profile.adapter_id = NEW.command_json #>> '{execution_profile,adapter_id}'
      AND profile.credential_mode = NEW.command_json #>> '{execution_profile,credential_mode}'
      AND profile.runtime_profile_id = NEW.command_json #>> '{execution_profile,runtime_profile_id}'
      AND profile.offering_id = NEW.command_json #>> '{execution_profile,offering_id}';
    IF catalog_dispatch_mode IS NULL
       OR catalog_dispatch_mode IS DISTINCT FROM NEW.provision_dispatch_mode
       OR catalog_adapter_manifest_hash IS DISTINCT FROM snapshot_adapter_manifest_hash THEN
        RAISE EXCEPTION 'provider operation dispatch mode does not match its immutable catalog profile'
            USING ERRCODE = '55000';
    END IF;
    RETURN NEW;
END;
$$;



-- Match the runtime boot posture: system names precede the immutable owning
-- schema, and caller-owned temporary objects are resolved last.
DO $substrate_definer_paths$
DECLARE active_schema text := current_schema();
BEGIN
 EXECUTE pg_catalog.format('ALTER FUNCTION %I.substrate_lock_binding(text,text,bigint) SET search_path TO pg_catalog, %I, pg_temp',active_schema,active_schema);
 EXECUTE pg_catalog.format('ALTER FUNCTION %I.substrate_guest_custody_valid(text,text,text,text) SET search_path TO pg_catalog, %I, pg_temp',active_schema,active_schema);
 EXECUTE pg_catalog.format('ALTER FUNCTION %I.substrate_execution_claim_guard() SET search_path TO pg_catalog, %I, pg_temp',active_schema,active_schema);
END;
$substrate_definer_paths$;
