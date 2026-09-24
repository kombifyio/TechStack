-- Customer-owned hypervisors reuse native lease/operation authority. Their
-- guests are never inserted into commercial managed-runtime capacity.
SET LOCAL lock_timeout = '5s';
SELECT pg_catalog.set_config('search_path', pg_catalog.quote_ident(pg_catalog.current_schema()) || ', pg_catalog, pg_temp', true);
CREATE TABLE substrate_bindings (
 tenant_id text NOT NULL REFERENCES techstack_tenants(id),
 server_id text NOT NULL,
 worker_id text NOT NULL,
 node text NOT NULL CHECK (node ~ '^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$'),
 revision bigint NOT NULL CHECK (revision > 0),
 config_json jsonb NOT NULL CHECK (jsonb_typeof(config_json)='object'),
 enabled boolean NOT NULL DEFAULT false,
 PRIMARY KEY (tenant_id,server_id),
 FOREIGN KEY (tenant_id,server_id) REFERENCES servers(tenant_id,id),
 FOREIGN KEY (tenant_id,worker_id) REFERENCES workers(tenant_id,id)
);
CREATE TABLE substrate_guest_leases (
 tenant_id text NOT NULL,
 lease_id text NOT NULL,
 server_id text NOT NULL,
 substrate_server_id text NOT NULL,
 guest_id integer NOT NULL CHECK (guest_id BETWEEN 100 AND 999999999),
 slot_id text NOT NULL,
 resource_generation_id uuid NOT NULL,
 operation_id text NOT NULL,
 request_digest text NOT NULL CHECK (request_digest ~ '^sha256:[0-9a-f]{64}$'),
 PRIMARY KEY (tenant_id,lease_id),
 UNIQUE (tenant_id,substrate_server_id,guest_id),
 UNIQUE (tenant_id,slot_id),
 FOREIGN KEY (tenant_id,server_id) REFERENCES servers(tenant_id,id),
 FOREIGN KEY (tenant_id,lease_id) REFERENCES techstack_vm_leases(tenant_id,id),
 FOREIGN KEY (tenant_id,substrate_server_id) REFERENCES substrate_bindings(tenant_id,server_id),
 FOREIGN KEY (tenant_id,operation_id,lease_id) REFERENCES provider_operations(tenant_id,operation_id,lease_id)
);
ALTER TABLE substrate_bindings ENABLE ROW LEVEL SECURITY;
ALTER TABLE substrate_bindings FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON substrate_bindings
 USING (tenant_id=current_setting('app.tenant_id',true)) WITH CHECK (tenant_id=current_setting('app.tenant_id',true));
ALTER TABLE substrate_guest_leases ENABLE ROW LEVEL SECURITY;
ALTER TABLE substrate_guest_leases FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON substrate_guest_leases
 USING (tenant_id=current_setting('app.tenant_id',true)) WITH CHECK (tenant_id=current_setting('app.tenant_id',true));

ALTER TABLE provider_credential_handles DROP CONSTRAINT provider_credential_handles_credential_mode_check;
ALTER TABLE provider_credential_handles ADD CONSTRAINT provider_credential_handles_credential_mode_check
 CHECK (credential_mode IN ('managed','byok') OR (credential_mode='worker_held' AND provider_id='proxmox'));

ALTER TABLE servers DROP CONSTRAINT servers_runtime_target_shape;
ALTER TABLE servers ADD CONSTRAINT servers_runtime_target_shape CHECK (
 (environment_class='unknown' AND offering IS NULL AND provider_id IS NULL AND provider_target_ref IS NULL
  AND availability_owner IS NULL AND operations_owner IS NULL AND runtime_target_evidence_ref IS NULL AND runtime_target_observed_at IS NULL)
 OR (environment_class='local' AND offering='self_owned_device' AND provider_id IS NULL AND provider_target_ref IS NULL
  AND availability_owner='customer' AND operations_owner='customer' AND runtime_target_evidence_ref IS NOT NULL AND runtime_target_observed_at IS NOT NULL)
 OR (environment_class='local' AND offering='substrate_vm' AND provider_id='proxmox' AND provider_target_ref IS NOT NULL
  AND lease_id IS NOT NULL AND runtime_target_evidence_ref='runtime-lease:' || lease_id AND runtime_target_observed_at IS NOT NULL
  AND availability_owner='customer' AND operations_owner='customer')
 OR (environment_class='cloud' AND offering='external_vps' AND provider_id IS NOT NULL AND provider_target_ref IS NOT NULL
  AND availability_owner='provider' AND operations_owner='customer' AND runtime_target_evidence_ref IS NOT NULL AND runtime_target_observed_at IS NOT NULL)
 OR (environment_class='cloud' AND offering='managed_vps' AND provider_id IS NOT NULL AND provider_target_ref IS NOT NULL
  AND availability_owner='provider' AND operations_owner='kombify' AND lease_id IS NOT NULL
  AND runtime_target_evidence_ref IS NOT NULL AND runtime_target_observed_at IS NOT NULL)
);

-- Runtime can lock its exact binding but cannot update the owner's grant.
CREATE FUNCTION substrate_lock_binding(p_tenant text,p_server text,p_revision bigint)
RETURNS boolean LANGUAGE plpgsql SECURITY DEFINER SET search_path FROM CURRENT AS $$
BEGIN
 IF p_tenant IS DISTINCT FROM current_setting('app.tenant_id',true) THEN
  RETURN false;
 END IF;
 PERFORM 1 FROM substrate_bindings b WHERE b.tenant_id=p_tenant AND b.server_id=p_server
  AND b.revision=p_revision AND b.enabled FOR SHARE OF b;
 RETURN FOUND;
END; $$;
REVOKE EXECUTE ON FUNCTION substrate_lock_binding(text,text,bigint) FROM PUBLIC;

CREATE FUNCTION substrate_guest_custody_valid(p_tenant text,p_lease text,p_generation text,p_operation text)
RETURNS boolean LANGUAGE sql STABLE SECURITY DEFINER SET search_path FROM CURRENT AS $$
 SELECT EXISTS (SELECT 1 FROM substrate_guest_leases g
 JOIN techstack_vm_leases l ON l.tenant_id=g.tenant_id AND l.id=g.lease_id
 JOIN servers s ON s.tenant_id=g.tenant_id AND s.id=g.server_id AND s.lease_id=g.lease_id
 JOIN provider_operations p ON p.tenant_id=g.tenant_id AND p.operation_id=p_operation AND p.lease_id=g.lease_id
 WHERE p_tenant=current_setting('app.tenant_id',true)
 AND g.tenant_id=p_tenant AND g.lease_id=p_lease AND g.resource_generation_id::text=p_generation
 AND l.resource_generation_id=g.resource_generation_id AND l.server_id=g.server_id AND l.provider_id='proxmox'
 AND l.lease_json->>'custody_class'='customer_substrate'
 AND l.lease_json->>'billing_mode'='local'
 AND l.lease_json->>'lifecycle_class'='one_time'
 AND l.lease_json->>'recreate_policy'='never'
 AND s.owner_subject_id=l.owner_subject_id
 AND s.provider_id='proxmox' AND s.offering='substrate_vm'
 AND s.provider_target_ref=g.substrate_server_id || '/' || g.guest_id::text
 AND l.lease_json #>> '{metadata,substrate_server_id}'=g.substrate_server_id
 AND l.lease_json #>> '{metadata,substrate_guest_id}'=g.guest_id::text
 AND l.lease_json #>> '{resource,engine_vm_id}'=g.substrate_server_id || '/' || g.guest_id::text
 AND p.command_json->>'schema_version'='techstack.provider-control-operation/v1'
 AND p.command_json->>'execution_authority'='techstack_provider_control'
 AND p.command_json #>> '{command,provider_id}'='proxmox'
 AND p.command_json #>> '{command,resource_generation_id}'=g.resource_generation_id::text
 AND p.command_json #>> '{command,runtime_server_id}'=g.server_id
 AND p.command_json #>> '{execution_profile,credential_mode}'='worker_held'
 AND (p.operation<>'provision' OR g.operation_id=p.operation_id));
$$;
REVOKE EXECUTE ON FUNCTION substrate_guest_custody_valid(text,text,text,text) FROM PUBLIC;

CREATE FUNCTION substrate_guest_custody_insert_guard() RETURNS trigger LANGUAGE plpgsql SET search_path FROM CURRENT AS $$
BEGIN
 IF NOT substrate_guest_custody_valid(NEW.tenant_id,NEW.lease_id,NEW.resource_generation_id::text,NEW.operation_id) THEN
  RAISE EXCEPTION 'substrate guest has no exact native creation custody' USING ERRCODE='55000';
 END IF;
 RETURN NEW;
END; $$;
CREATE CONSTRAINT TRIGGER substrate_guest_custody_guard AFTER INSERT ON substrate_guest_leases
 DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION substrate_guest_custody_insert_guard();
CREATE TRIGGER substrate_guest_custody_immutable BEFORE UPDATE OR DELETE ON substrate_guest_leases
 FOR EACH ROW EXECUTE FUNCTION managed_runtime_capacity_reservation_reject_mutation();

-- Independently fence the live executor on every new/renewed claim. An offline
-- Guard never opens an inbound fallback and never lends custody to another VM.
CREATE FUNCTION substrate_execution_claim_guard() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER SET search_path FROM CURRENT AS $$
DECLARE p provider_operations%ROWTYPE;
BEGIN
 IF NEW.state<>'active' THEN RETURN NEW; END IF;
 SELECT * INTO p FROM provider_operations WHERE tenant_id=NEW.tenant_id AND operation_id=NEW.operation_id;
 IF p.command_json #>> '{command,provider_id}' IS DISTINCT FROM 'proxmox' THEN RETURN NEW; END IF;
 PERFORM 1 FROM techstack_vm_leases l
 JOIN substrate_bindings b ON b.tenant_id=l.tenant_id AND b.server_id=l.lease_json #>> '{metadata,substrate_server_id}'
 JOIN servers s ON s.tenant_id=b.tenant_id AND s.id=b.server_id
 WHERE l.tenant_id=NEW.tenant_id AND l.id=p.lease_id AND b.enabled
 AND b.revision::text=l.lease_json #>> '{metadata,substrate_binding_revision}'
 AND s.owner_subject_id=l.owner_subject_id AND s.worker_id=b.worker_id
 AND s.metadata_json->>'server_node_role'='substrate' AND s.lifecycle_state='active' AND s.connection_state='connected'
 AND s.last_heartbeat_at>clock_timestamp()-interval '2 minutes'
 AND p.command_json #>> '{command,connection_ref}'='provider-connection://substrate/' || b.worker_id || '/' || l.id
 FOR SHARE OF b,s;
 IF NOT FOUND THEN RAISE EXCEPTION 'substrate Guard custody is unavailable' USING ERRCODE='55000'; END IF;
 RETURN NEW;
END; $$;
CREATE TRIGGER substrate_execution_claim_guard BEFORE INSERT OR UPDATE ON provider_operation_execution_claims
 FOR EACH ROW EXECUTE FUNCTION substrate_execution_claim_guard();
REVOKE EXECUTE ON FUNCTION substrate_execution_claim_guard() FROM PUBLIC;

CREATE OR REPLACE FUNCTION provider_operation_require_capacity_reservation()
RETURNS trigger
LANGUAGE plpgsql
SET search_path FROM CURRENT
AS $$
DECLARE
    operation_generation_id text;
BEGIN
    IF NEW.command_json #>> '{command,provider_id}' = 'proxmox' THEN
        IF NOT substrate_guest_custody_valid(NEW.tenant_id,NEW.lease_id,NEW.command_json #>> '{command,resource_generation_id}',NEW.operation_id) THEN
            RAISE EXCEPTION 'native substrate operation requires atomic guest custody' USING ERRCODE='55000';
        END IF;
        RETURN NEW;
    END IF;
    IF NEW.operation IS DISTINCT FROM 'provision'
       OR NEW.command_json->>'schema_version' IS DISTINCT FROM 'techstack.provider-control-operation/v1'
       OR NEW.command_json->>'execution_authority' IS DISTINCT FROM 'techstack_provider_control' THEN
        RETURN NEW;
    END IF;

    operation_generation_id := NEW.command_json #>> '{command,resource_generation_id}';
    PERFORM 1
    FROM managed_runtime_capacity_reservations AS reservation
    WHERE reservation.tenant_id = NEW.tenant_id
      AND reservation.lease_id = NEW.lease_id
      AND reservation.operation_id = NEW.operation_id
      AND reservation.resource_generation_id::text = operation_generation_id
      AND reservation.reservation_origin = 'native_admission'
      AND reservation.reservation_mode IN ('limited', 'unlimited')
      AND reservation.policy_source IN (
          'edge_v2_entitlement+signed_budget:cloud.runtime.credits#managed_servers',
          'static_release_manifest:selfhost-oss'
      );
    IF NOT FOUND THEN
        RAISE EXCEPTION 'native provision operation requires an atomic managed runtime capacity reservation'
            USING ERRCODE = '55000';
    END IF;
    RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION managed_runtime_capacity_execution_claim_guard()
RETURNS trigger
LANGUAGE plpgsql
SET search_path FROM CURRENT
AS $$
DECLARE
    operation_kind text;
    operation_lease_id text;
    operation_schema_version text;
    operation_execution_authority text;
    operation_generation_id text;
    operation_provider_id text;
    operation_profile_provider_id text;
BEGIN
    IF NEW.state IS DISTINCT FROM 'active' THEN
        RETURN NEW;
    END IF;

    SELECT
        operation.operation,
        operation.lease_id,
        operation.command_json->>'schema_version',
        operation.command_json->>'execution_authority',
        operation.command_json #>> '{command,resource_generation_id}',
        operation.command_json #>> '{command,provider_id}',
        operation.command_json #>> '{execution_profile,provider_id}'
    INTO
        operation_kind,
        operation_lease_id,
        operation_schema_version,
        operation_execution_authority,
        operation_generation_id,
        operation_provider_id,
        operation_profile_provider_id
    FROM provider_operations AS operation
    WHERE operation.tenant_id = NEW.tenant_id
      AND operation.operation_id = NEW.operation_id;

    IF NOT FOUND THEN
        RAISE EXCEPTION 'provider execution claim has no durable operation'
            USING ERRCODE = '55000';
    END IF;
    IF operation_provider_id='proxmox' THEN
        IF NOT substrate_guest_custody_valid(NEW.tenant_id,operation_lease_id,operation_generation_id,NEW.operation_id) THEN
            RAISE EXCEPTION 'substrate execution requires exact guest custody' USING ERRCODE='55000';
        END IF;
        RETURN NEW;
    END IF;
    IF operation_kind NOT IN ('provision', 'reconcile') THEN
        RETURN NEW;
    END IF;
    IF operation_schema_version IS DISTINCT FROM 'techstack.provider-control-operation/v1'
       OR operation_execution_authority IS DISTINCT FROM 'techstack_provider_control'
       OR operation_provider_id NOT IN ('ionos', 'centron')
       OR operation_profile_provider_id IS DISTINCT FROM operation_provider_id THEN
        RAISE EXCEPTION 'provider execution claim is blocked by missing native managed runtime capacity authority'
            USING ERRCODE = '55000';
    END IF;

    PERFORM 1
    FROM managed_runtime_capacity_reservations AS reservation
    WHERE reservation.tenant_id = NEW.tenant_id
      AND reservation.lease_id = operation_lease_id
      AND reservation.resource_generation_id::text = operation_generation_id
      AND reservation.provider_id = operation_provider_id
      AND reservation.reservation_origin = 'native_admission'
      AND reservation.reservation_mode IN ('limited', 'unlimited')
      AND reservation.policy_source IN (
          'edge_v2_entitlement+signed_budget:cloud.runtime.credits#managed_servers',
          'static_release_manifest:selfhost-oss'
      )
      AND (
          operation_kind <> 'provision'
          OR reservation.operation_id = NEW.operation_id
      );
    IF NOT FOUND THEN
        RAISE EXCEPTION 'provider execution claim is blocked by missing native managed runtime capacity authority'
            USING ERRCODE = '55000';
    END IF;
    RETURN NEW;
END;
$$;
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
           OR COALESCE(NEW.command_json #>> '{execution_profile,offering_id}', '') NOT IN ('ubuntu-24.04','haos')
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
