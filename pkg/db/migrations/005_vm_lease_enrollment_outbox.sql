-- 005_vm_lease_enrollment_outbox.sql
--
-- Durable bridge between TechStack's Monthly Runtime product lease and
-- Simulate's provider execution boundary. This table is intentionally worker
-- scoped, not tenant-query scoped: the outbox worker must claim due jobs across
-- tenants, while every lease update still goes through techstack_vm_leases RLS.

CREATE TABLE IF NOT EXISTS techstack_vm_lease_enrollment_outbox (
    tenant_id text NOT NULL,
    lease_id text NOT NULL,
    request_json jsonb NOT NULL,
    idempotency_key text,
    status text NOT NULL DEFAULT 'pending',
    attempts integer NOT NULL DEFAULT 0,
    next_attempt_at timestamptz NOT NULL DEFAULT now(),
    last_error text,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, lease_id),
    CHECK (status IN ('pending', 'retrying', 'enrolled', 'failed')),
    CHECK (attempts >= 0)
);

CREATE INDEX IF NOT EXISTS idx_techstack_vm_lease_enrollment_outbox_due
    ON techstack_vm_lease_enrollment_outbox (status, next_attempt_at, updated_at);

CREATE INDEX IF NOT EXISTS idx_techstack_vm_lease_enrollment_outbox_idempotency
    ON techstack_vm_lease_enrollment_outbox (idempotency_key)
    WHERE idempotency_key IS NOT NULL;

CREATE TABLE IF NOT EXISTS techstack_vm_lease_operation_journal (
    id bigserial PRIMARY KEY,
    tenant_id text NOT NULL,
    lease_id text NOT NULL,
    event_type text NOT NULL,
    status text NOT NULL,
    actor text,
    error text,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_techstack_vm_lease_operation_journal_lease
    ON techstack_vm_lease_operation_journal (tenant_id, lease_id, created_at DESC);
