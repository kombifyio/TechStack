-- Bind every durable enrollment request to the exact provider-resource
-- generation that produced it. This prevents an in-flight result for Gen A
-- from completing, retrying, or failing the replacement Gen B request.

SET LOCAL lock_timeout = '5s';

ALTER TABLE techstack_vm_lease_enrollment_outbox
    ADD COLUMN IF NOT EXISTS resource_generation_id text;

-- Migration 016 restores FORCE RLS on the lease table, while migrations run
-- without an app.tenant_id. Suspend lease RLS only inside this migration's
-- transaction so the trusted owner can perform a complete legacy backfill.
-- A rollback restores the original RLS state if any postcondition fails.
ALTER TABLE techstack_vm_leases NO FORCE ROW LEVEL SECURITY;
ALTER TABLE techstack_vm_leases DISABLE ROW LEVEL SECURITY;

UPDATE techstack_vm_lease_enrollment_outbox AS outbox
SET resource_generation_id =
    leases.lease_json->'metadata'->>'resource_generation_id'
FROM techstack_vm_leases AS leases
WHERE leases.tenant_id = outbox.tenant_id
  AND leases.id = outbox.lease_id
  AND NULLIF(BTRIM(outbox.resource_generation_id), '') IS NULL;

ALTER TABLE techstack_vm_leases ENABLE ROW LEVEL SECURITY;
ALTER TABLE techstack_vm_leases FORCE ROW LEVEL SECURITY;

-- Orphaned or otherwise unbound legacy requests cannot be executed safely,
-- but must not prevent the control plane from starting. Quarantine them as
-- terminal failures with an operator-readable reason. A nullable generation
-- remains valid only for this terminal legacy state; every executable or
-- successfully completed row must carry a canonical generation UUID.
UPDATE techstack_vm_lease_enrollment_outbox
SET resource_generation_id = NULL,
    status = 'failed',
    last_error =
        'legacy enrollment request cannot be bound to an authoritative resource generation',
    updated_at = now()
WHERE NULLIF(BTRIM(resource_generation_id), '') IS NULL;

ALTER TABLE techstack_vm_lease_enrollment_outbox
    DROP CONSTRAINT IF EXISTS techstack_vm_lease_enrollment_generation_uuid_check;

ALTER TABLE techstack_vm_lease_enrollment_outbox
    ADD CONSTRAINT techstack_vm_lease_enrollment_generation_uuid_check
    CHECK (
        (
            status = 'failed'
            AND resource_generation_id IS NULL
        )
        OR (
            resource_generation_id IS NOT NULL
            AND resource_generation_id
                ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'
        )
    );

CREATE INDEX IF NOT EXISTS idx_techstack_vm_lease_enrollment_generation
    ON techstack_vm_lease_enrollment_outbox
        (tenant_id, lease_id, resource_generation_id, status);
