-- Failure knowledge class A outcomes (knowledge-search plan section 5.8 step
-- 7): one row per resolution an agent applied through a product procedure.
-- The procedure ran as a job of the tenant; the outcome is that job's terminal
-- state, never a claim of the caller. A row names the resolution (an AI
-- Platform resolution id), the failure signature it was applied to, the
-- procedure reference and the coarse context of that failure; never raw
-- error text. Rows are tenant-isolated and never leave the tenant.
--
-- Additive and backward compatible: a binary that predates this migration
-- neither writes nor reads the table.

CREATE TABLE IF NOT EXISTS failure_resolution_outcomes (
    tenant_id text NOT NULL,
    resolution_id text NOT NULL,
    receipt_kind text NOT NULL,
    receipt_ref text NOT NULL,
    target_kind text NOT NULL DEFAULT '',
    target_ref text NOT NULL DEFAULT '',
    signature text NOT NULL,
    procedure_ref text NOT NULL,
    stackkit_module text NOT NULL DEFAULT '',
    stackkit_version text NOT NULL DEFAULT '',
    app_version text NOT NULL DEFAULT '',
    os text NOT NULL DEFAULT '',
    os_version text NOT NULL DEFAULT '',
    arch text NOT NULL DEFAULT '',
    container_runtime text NOT NULL DEFAULT '',
    provider text NOT NULL DEFAULT '',
    device_class text NOT NULL DEFAULT '',
    outcome text NOT NULL,
    recorded_by text NOT NULL DEFAULT '',
    completed_at timestamptz NOT NULL,
    recorded_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, resolution_id, receipt_kind, receipt_ref),
    CHECK (resolution_id ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'),
    CHECK (receipt_kind IN ('job')),
    CHECK (BTRIM(receipt_ref) <> ''),
    CHECK (target_kind IN ('', 'stack')),
    CHECK (signature ~ '^[0-9a-f]{64}$'),
    CHECK (procedure_ref ~ '^(stackkits|techstack):[a-z0-9][a-z0-9._:/@#-]{1,199}$'),
    CHECK (outcome IN ('succeeded', 'failed'))
);

CREATE INDEX IF NOT EXISTS idx_failure_resolution_outcomes_resolution
    ON failure_resolution_outcomes (tenant_id, resolution_id, completed_at DESC);

ALTER TABLE failure_resolution_outcomes ENABLE ROW LEVEL SECURITY;
ALTER TABLE failure_resolution_outcomes FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation ON failure_resolution_outcomes;
CREATE POLICY tenant_isolation ON failure_resolution_outcomes
    USING (tenant_id = current_setting('app.tenant_id', true))
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true));
