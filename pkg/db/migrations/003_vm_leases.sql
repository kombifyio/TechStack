-- 003_vm_leases.sql
--
-- TechStack is the authority for long-running managed VM leases. Simulate
-- consumes these rows over the servicecall-protected internal API and keeps
-- provider execution state out of this database.

CREATE TABLE IF NOT EXISTS techstack_vm_leases (
    id text PRIMARY KEY,
    tenant_id text NOT NULL,
    subject_id text NOT NULL,
    org_id text,
    provider_id text NOT NULL,
    engine_vm_id text,
    desired_state text NOT NULL,
    idempotency_key text UNIQUE,
    lease_json jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CHECK (desired_state IN ('running', 'stopped', 'archived'))
);

CREATE INDEX IF NOT EXISTS idx_techstack_vm_leases_tenant ON techstack_vm_leases (tenant_id);
CREATE INDEX IF NOT EXISTS idx_techstack_vm_leases_provider ON techstack_vm_leases (provider_id);
CREATE INDEX IF NOT EXISTS idx_techstack_vm_leases_engine_vm ON techstack_vm_leases (provider_id, engine_vm_id);

ALTER TABLE techstack_vm_leases ENABLE ROW LEVEL SECURITY;
ALTER TABLE techstack_vm_leases FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation ON techstack_vm_leases;
CREATE POLICY tenant_isolation ON techstack_vm_leases
    USING (tenant_id = current_setting('app.tenant_id', true))
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true));
