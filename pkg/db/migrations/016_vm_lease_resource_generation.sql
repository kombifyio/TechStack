-- Bind confirmed provider teardown evidence to one exact lease resource
-- generation. Existing operation rows remain readable with a NULL digest;
-- destructive flows reject missing digests before provider execution.

-- Existing leases represent already-created resource generations. Assign each
-- one a server-side random generation exactly once so the first post-upgrade
-- decommission can produce generation-bound proof without recreating the VM.
-- Migration 003 FORCEs tenant RLS, while migrations intentionally run without
-- an app.tenant_id. The migrator owns this table, so take an ACCESS EXCLUSIVE
-- DDL lock and suspend RLS only inside the migration transaction; otherwise an
-- upgrade can silently update zero tenant rows and still record this migration.
-- Keep startup bounded when another session holds a conflicting table lock.
SET LOCAL lock_timeout = '5s';

ALTER TABLE techstack_vm_leases NO FORCE ROW LEVEL SECURITY;
ALTER TABLE techstack_vm_leases DISABLE ROW LEVEL SECURITY;

UPDATE techstack_vm_leases
SET lease_json = jsonb_set(
    lease_json,
    '{metadata}',
    (
        CASE
            WHEN jsonb_typeof(lease_json->'metadata') = 'object'
                THEN lease_json->'metadata'
            ELSE '{}'::jsonb
        END
    ) || jsonb_build_object('resource_generation_id', gen_random_uuid()::text),
    true
);

-- Do not trust any pre-migration resource_generation_id, even when it already
-- resembles a UUID: legacy JSON was caller-selectable. The unconditional
-- UPDATE above replaces every value. These validated postconditions keep all
-- future rows canonical and make accidental generation reuse fail closed.
ALTER TABLE techstack_vm_leases
    DROP CONSTRAINT IF EXISTS techstack_vm_leases_resource_generation_uuid_check;

ALTER TABLE techstack_vm_leases
    ADD CONSTRAINT techstack_vm_leases_resource_generation_uuid_check
    CHECK (
        jsonb_typeof(lease_json->'metadata') = 'object'
        AND NULLIF(BTRIM(lease_json->'metadata'->>'resource_generation_id'), '') IS NOT NULL
        AND lease_json->'metadata'->>'resource_generation_id'
            ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'
    );

CREATE UNIQUE INDEX IF NOT EXISTS idx_techstack_vm_leases_resource_generation
    ON techstack_vm_leases ((lease_json->'metadata'->>'resource_generation_id'));

-- A composite unique key lets the journal reference the tenant and lease as
-- one authority boundary instead of relying on globally unique lease IDs.
CREATE UNIQUE INDEX IF NOT EXISTS idx_techstack_vm_leases_tenant_id
    ON techstack_vm_leases (tenant_id, id);

ALTER TABLE techstack_vm_leases ENABLE ROW LEVEL SECURITY;
ALTER TABLE techstack_vm_leases FORCE ROW LEVEL SECURITY;

ALTER TABLE techstack_vm_lease_operation_journal
    ADD COLUMN IF NOT EXISTS resource_generation_digest text;

ALTER TABLE techstack_vm_lease_operation_journal
    DROP CONSTRAINT IF EXISTS techstack_vm_lease_operation_journal_generation_digest_check;

ALTER TABLE techstack_vm_lease_operation_journal
    ADD CONSTRAINT techstack_vm_lease_operation_journal_generation_digest_check
    CHECK (
        resource_generation_digest IS NULL
        OR resource_generation_digest ~ '^[0-9a-f]{64}$'
    );

-- Preserve legacy confirmed rows with NULL proof while rejecting every new
-- confirmed decommission insert that lacks an exact generation binding.
ALTER TABLE techstack_vm_lease_operation_journal
    DROP CONSTRAINT IF EXISTS techstack_vm_lease_confirmed_generation_digest_check;

ALTER TABLE techstack_vm_lease_operation_journal
    ADD CONSTRAINT techstack_vm_lease_confirmed_generation_digest_check
    CHECK (
        event_type <> 'decommission'
        OR status <> 'decommissioned'
        OR resource_generation_digest IS NOT NULL
    ) NOT VALID;

-- Keep legacy journal rows readable even if old data predates a lease row,
-- while enforcing the exact tenant+lease authority on every new append.
ALTER TABLE techstack_vm_lease_operation_journal
    DROP CONSTRAINT IF EXISTS techstack_vm_lease_operation_journal_tenant_lease_fkey;

ALTER TABLE techstack_vm_lease_operation_journal
    ADD CONSTRAINT techstack_vm_lease_operation_journal_tenant_lease_fkey
    FOREIGN KEY (tenant_id, lease_id)
    REFERENCES techstack_vm_leases (tenant_id, id)
    ON UPDATE RESTRICT
    ON DELETE RESTRICT
    NOT VALID;

ALTER TABLE techstack_vm_lease_operation_journal ENABLE ROW LEVEL SECURITY;
ALTER TABLE techstack_vm_lease_operation_journal FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation ON techstack_vm_lease_operation_journal;
CREATE POLICY tenant_isolation ON techstack_vm_lease_operation_journal
    USING (tenant_id = current_setting('app.tenant_id', true))
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true));

-- Operation evidence is an append-only custody record. Run the trigger with a
-- fixed search path and no dynamic SQL so a tenant cannot redirect object
-- resolution through a writable schema.
CREATE OR REPLACE FUNCTION techstack_vm_lease_operation_journal_reject_mutation()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog
AS $$
BEGIN
    RAISE EXCEPTION 'VM lease operation journal rows are append-only and cannot be %',
        CASE TG_OP WHEN 'UPDATE' THEN 'updated' ELSE 'deleted' END
        USING ERRCODE = '55000';
END;
$$;

DROP TRIGGER IF EXISTS techstack_vm_lease_operation_journal_reject_mutation
    ON techstack_vm_lease_operation_journal;
CREATE TRIGGER techstack_vm_lease_operation_journal_reject_mutation
BEFORE UPDATE OR DELETE ON techstack_vm_lease_operation_journal
FOR EACH ROW EXECUTE FUNCTION techstack_vm_lease_operation_journal_reject_mutation();
