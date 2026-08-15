-- Stable managed-runtime product slots.
--
-- A browser retry key is transport metadata, not resource identity. This
-- expand-only schema gives each (tenant, stack, slot_key) one immutable slot
-- identity and each provider create one exact generation binding. Existing
-- provider resources are intentionally not backfilled or adopted: rows are
-- created only by native admission after a fresh, authoritative intent.
-- Slot identity is deliberately provider-neutral. provider_id belongs to the
-- generation. A provider transition requires an append-only capacity release
-- fact for the exact old generation, backed by definitive absence evidence.

CREATE TABLE IF NOT EXISTS managed_runtime_server_slots (
    tenant_id text NOT NULL REFERENCES techstack_tenants(id) ON DELETE RESTRICT,
    stack_id text NOT NULL,
    slot_key text NOT NULL,
    slot_id text NOT NULL,
    owner_subject_id text NOT NULL,
    created_at timestamptz NOT NULL,
    PRIMARY KEY (tenant_id, stack_id, slot_key),
    UNIQUE (tenant_id, slot_id),
    FOREIGN KEY (tenant_id, stack_id)
        REFERENCES stacks (tenant_id, id) ON DELETE RESTRICT,
    CHECK (slot_key ~ '^[a-z0-9][a-z0-9._-]{0,63}$'),
    CHECK (slot_id <> ''),
    CHECK (owner_subject_id <> '')
);

CREATE TABLE IF NOT EXISTS managed_runtime_server_slot_generations (
    tenant_id text NOT NULL,
    slot_id text NOT NULL,
    provider_id text NOT NULL,
    lease_id text NOT NULL,
    runtime_server_id text NOT NULL,
    resource_generation_id uuid NOT NULL,
    generation_ordinal bigint NOT NULL CHECK (generation_ordinal > 0),
    provision_operation_id text NOT NULL,
    intent_digest text NOT NULL,
    state text NOT NULL,
    created_at timestamptz NOT NULL,
    PRIMARY KEY (tenant_id, slot_id, resource_generation_id),
    UNIQUE (tenant_id, slot_id, generation_ordinal),
    UNIQUE (tenant_id, lease_id),
    UNIQUE (tenant_id, runtime_server_id),
    UNIQUE (tenant_id, provision_operation_id),
    FOREIGN KEY (tenant_id, slot_id)
        REFERENCES managed_runtime_server_slots (tenant_id, slot_id) ON DELETE RESTRICT,
    FOREIGN KEY (tenant_id, lease_id)
        REFERENCES techstack_vm_leases (tenant_id, id) ON DELETE RESTRICT,
    FOREIGN KEY (tenant_id, runtime_server_id)
        REFERENCES servers (tenant_id, id) ON DELETE RESTRICT,
    FOREIGN KEY (tenant_id, provision_operation_id)
        REFERENCES provider_operations (tenant_id, operation_id) ON DELETE RESTRICT,
    CHECK (provider_id ~ '^[a-z0-9][a-z0-9._-]{0,127}$'),
    CHECK (intent_digest ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (state IN ('active', 'quarantined'))
);

CREATE INDEX IF NOT EXISTS idx_managed_runtime_slots_stack
    ON managed_runtime_server_slots (tenant_id, stack_id, slot_key);

CREATE OR REPLACE FUNCTION reject_managed_runtime_slot_mutation()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'managed runtime slot identity is immutable';
END;
$$;

DROP TRIGGER IF EXISTS managed_runtime_slots_reject_update_delete ON managed_runtime_server_slots;
CREATE TRIGGER managed_runtime_slots_reject_update_delete
    BEFORE UPDATE OR DELETE ON managed_runtime_server_slots
    FOR EACH ROW EXECUTE FUNCTION reject_managed_runtime_slot_mutation();

CREATE OR REPLACE FUNCTION enforce_managed_runtime_generation_transition()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'managed runtime generation custody is immutable';
    END IF;
    IF OLD.tenant_id <> NEW.tenant_id
       OR OLD.slot_id <> NEW.slot_id
       OR OLD.provider_id <> NEW.provider_id
       OR OLD.lease_id <> NEW.lease_id
       OR OLD.runtime_server_id <> NEW.runtime_server_id
       OR OLD.resource_generation_id <> NEW.resource_generation_id
       OR OLD.generation_ordinal <> NEW.generation_ordinal
       OR OLD.provision_operation_id <> NEW.provision_operation_id
       OR OLD.intent_digest <> NEW.intent_digest
       OR OLD.created_at <> NEW.created_at THEN
        RAISE EXCEPTION 'managed runtime generation identity is immutable';
    END IF;
    IF OLD.state <> 'active' OR NEW.state <> 'quarantined' THEN
        RAISE EXCEPTION 'managed runtime generation permits only active to quarantined';
    END IF;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS managed_runtime_slot_generations_guard_update_delete
    ON managed_runtime_server_slot_generations;
CREATE TRIGGER managed_runtime_slot_generations_guard_update_delete
    BEFORE UPDATE OR DELETE ON managed_runtime_server_slot_generations
    FOR EACH ROW EXECUTE FUNCTION enforce_managed_runtime_generation_transition();

ALTER TABLE managed_runtime_server_slots ENABLE ROW LEVEL SECURITY;
ALTER TABLE managed_runtime_server_slots FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation ON managed_runtime_server_slots;
CREATE POLICY tenant_isolation ON managed_runtime_server_slots
    USING (tenant_id = current_setting('app.tenant_id', true))
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true));

ALTER TABLE managed_runtime_server_slot_generations ENABLE ROW LEVEL SECURITY;
ALTER TABLE managed_runtime_server_slot_generations FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation ON managed_runtime_server_slot_generations;
CREATE POLICY tenant_isolation ON managed_runtime_server_slot_generations
    USING (tenant_id = current_setting('app.tenant_id', true))
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true));

COMMENT ON TABLE managed_runtime_server_slots IS
    'Immutable logical managed-runtime slot identities; never inferred from provider inventory.';
COMMENT ON TABLE managed_runtime_server_slot_generations IS
    'Exact native provider generations; occupancy ends only through the matching append-only capacity release fact.';
