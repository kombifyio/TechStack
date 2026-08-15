-- Idempotency is scoped to the authenticated tenant. A globally unique client
-- key lets one tenant block another tenant's otherwise valid create request
-- while FORCE RLS hides the conflicting row and makes recovery impossible.

SET LOCAL lock_timeout = '10s';

ALTER TABLE techstack_vm_leases
    DROP CONSTRAINT IF EXISTS techstack_vm_leases_idempotency_key_key;

ALTER TABLE techstack_vm_leases
    DROP CONSTRAINT IF EXISTS techstack_vm_leases_tenant_idempotency_key_key;

ALTER TABLE techstack_vm_leases
    ADD CONSTRAINT techstack_vm_leases_tenant_idempotency_key_key
    UNIQUE (tenant_id, idempotency_key);
