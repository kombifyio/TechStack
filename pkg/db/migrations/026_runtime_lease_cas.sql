-- Normalize native runtime-lease authority fields while preserving historical
-- quarantine JSON for forensics. resource_generation_id is a typed projection
-- of migration 016's UUID; it is not a second generation authority.

SET LOCAL lock_timeout = '5s';
SELECT pg_catalog.set_config(
    'search_path',
    pg_catalog.quote_ident(pg_catalog.current_schema()) || ', pg_catalog, pg_temp',
    true
);

ALTER TABLE techstack_vm_leases
    ADD COLUMN IF NOT EXISTS lease_revision bigint,
    ADD COLUMN IF NOT EXISTS owner_subject_id text,
    ADD COLUMN IF NOT EXISTS server_id text,
    ADD COLUMN IF NOT EXISTS resource_generation_id uuid,
    ADD COLUMN IF NOT EXISTS valid_from timestamptz,
    ADD COLUMN IF NOT EXISTS valid_until timestamptz,
    ADD COLUMN IF NOT EXISTS renewed_at timestamptz,
    ADD COLUMN IF NOT EXISTS cancelled_at timestamptz;

-- Migration 016 replaced every caller-controlled generation with a canonical
-- UUID. Mirror that exact value into the typed column.
ALTER TABLE techstack_vm_leases NO FORCE ROW LEVEL SECURITY;
ALTER TABLE techstack_vm_leases DISABLE ROW LEVEL SECURITY;

UPDATE techstack_vm_leases
SET resource_generation_id = (lease_json->'metadata'->>'resource_generation_id')::uuid
WHERE resource_generation_id IS NULL;

-- Historical rows remain quarantine/inventory records. Values which are safe
-- to project are backfilled, but their absence never activates native work.
UPDATE techstack_vm_leases
SET lease_revision = COALESCE(lease_revision, 1),
    owner_subject_id = COALESCE(NULLIF(BTRIM(owner_subject_id), ''), NULLIF(BTRIM(subject_id), '')),
    valid_from = COALESCE(valid_from, created_at),
    valid_until = COALESCE(valid_until, created_at + INTERVAL '100 years'),
    renewed_at = COALESCE(renewed_at, updated_at)
WHERE lease_revision IS NULL
   OR owner_subject_id IS NULL
   OR valid_from IS NULL
   OR valid_until IS NULL;

UPDATE techstack_vm_leases
SET desired_state = 'absent'
WHERE desired_state = 'archived';

ALTER TABLE techstack_vm_leases ENABLE ROW LEVEL SECURITY;
ALTER TABLE techstack_vm_leases FORCE ROW LEVEL SECURITY;

ALTER TABLE techstack_vm_leases ALTER COLUMN provider_id DROP NOT NULL;
ALTER TABLE techstack_vm_leases ALTER COLUMN subject_id DROP NOT NULL;
ALTER TABLE techstack_vm_leases DROP CONSTRAINT IF EXISTS techstack_vm_leases_desired_state_check;
ALTER TABLE techstack_vm_leases
    ADD CONSTRAINT techstack_vm_leases_desired_state_check
        CHECK (desired_state IN ('running', 'stopped', 'absent')),
    ADD CONSTRAINT techstack_vm_leases_revision_valid
        CHECK (lease_revision IS NULL OR lease_revision BETWEEN 1 AND 9007199254740991),
    ADD CONSTRAINT techstack_vm_leases_validity_window
        CHECK (
            (valid_from IS NULL AND valid_until IS NULL)
            OR (valid_from IS NOT NULL AND valid_until IS NOT NULL AND valid_until > valid_from)
        ),
    ADD CONSTRAINT techstack_vm_leases_cancellation_window
        CHECK (cancelled_at IS NULL OR valid_until IS NULL OR cancelled_at <= valid_until),
    ADD CONSTRAINT runtime_lease_native_binding_unique
        UNIQUE (tenant_id, id, lease_revision, server_id, resource_generation_id);

CREATE UNIQUE INDEX IF NOT EXISTS uq_runtime_lease_resource_generation
    ON techstack_vm_leases (resource_generation_id)
    WHERE resource_generation_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_runtime_lease_tenant_server
    ON techstack_vm_leases (tenant_id, server_id)
    WHERE server_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_runtime_lease_owner
    ON techstack_vm_leases (tenant_id, owner_subject_id, id);

ALTER TABLE techstack_vm_leases
    ADD CONSTRAINT techstack_vm_leases_server_tenant_fk
        FOREIGN KEY (tenant_id, server_id)
        REFERENCES servers (tenant_id, id)
        ON UPDATE RESTRICT ON DELETE RESTRICT NOT VALID;

CREATE OR REPLACE FUNCTION runtime_lease_generation_projection_guard()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog
AS $$
DECLARE
    json_generation text;
    top_level_generation text;
    metadata_generation text;
BEGIN
    top_level_generation := NULLIF(BTRIM(NEW.lease_json->>'resource_generation_id'), '');
    metadata_generation := NULLIF(BTRIM(NEW.lease_json->'metadata'->>'resource_generation_id'), '');
    IF top_level_generation IS NOT NULL
       AND metadata_generation IS NOT NULL
       AND top_level_generation IS DISTINCT FROM metadata_generation THEN
        RAISE EXCEPTION 'runtime lease contains conflicting generation projections'
            USING ERRCODE = '23514';
    END IF;
    json_generation := COALESCE(top_level_generation, metadata_generation);
    IF NEW.resource_generation_id IS NULL
       OR json_generation IS NULL
       OR NEW.resource_generation_id::text IS DISTINCT FROM json_generation THEN
        RAISE EXCEPTION 'runtime lease generation projection mismatch'
            USING ERRCODE = '23514';
    END IF;
    IF TG_OP = 'UPDATE'
       AND NEW.resource_generation_id IS DISTINCT FROM OLD.resource_generation_id THEN
        RAISE EXCEPTION 'runtime lease generation is immutable'
            USING ERRCODE = '55000';
    END IF;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS runtime_lease_generation_projection_guard ON techstack_vm_leases;
CREATE TRIGGER runtime_lease_generation_projection_guard
BEFORE INSERT OR UPDATE OF resource_generation_id, lease_json ON techstack_vm_leases
FOR EACH ROW EXECUTE FUNCTION runtime_lease_generation_projection_guard();

CREATE TABLE IF NOT EXISTS runtime_lease_idempotency_records (
    tenant_id text NOT NULL,
    operation_scope text NOT NULL,
    idempotency_key text NOT NULL,
    request_digest bytea NOT NULL,
    lease_id text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (tenant_id, operation_scope, idempotency_key),
    FOREIGN KEY (tenant_id, lease_id)
        REFERENCES techstack_vm_leases (tenant_id, id)
        ON UPDATE RESTRICT ON DELETE RESTRICT,
    CHECK (char_length(operation_scope) BETWEEN 1 AND 128),
    CHECK (char_length(idempotency_key) BETWEEN 1 AND 256),
    CHECK (octet_length(request_digest) = 32)
);

CREATE OR REPLACE FUNCTION runtime_lease_idempotency_reject_mutation()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog
AS $$
BEGIN
    RAISE EXCEPTION 'runtime lease idempotency records are immutable'
        USING ERRCODE = '55000';
END;
$$;

DROP TRIGGER IF EXISTS runtime_lease_idempotency_reject_mutation
    ON runtime_lease_idempotency_records;
CREATE TRIGGER runtime_lease_idempotency_reject_mutation
BEFORE UPDATE OR DELETE ON runtime_lease_idempotency_records
FOR EACH ROW EXECUTE FUNCTION runtime_lease_idempotency_reject_mutation();

ALTER TABLE runtime_lease_idempotency_records ENABLE ROW LEVEL SECURITY;
ALTER TABLE runtime_lease_idempotency_records FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation ON runtime_lease_idempotency_records;
CREATE POLICY tenant_isolation ON runtime_lease_idempotency_records
    USING (tenant_id = current_setting('app.tenant_id', true))
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true));
