-- Failure knowledge class A (knowledge-search plan section 5.8): one row per
-- failed operation, normalized by pkg/failureknowledge. A row holds only the
-- allowlisted structured fields, the template in which every variable token
-- is replaced, the signature and coarse context dimensions; never raw error
-- text. Rows are tenant-isolated and never leave the tenant; an empty context
-- dimension means Techstack did not know it.
--
-- Additive and backward compatible: a binary that predates this migration
-- neither writes nor reads the table.

CREATE TABLE IF NOT EXISTS failure_occurrences (
    tenant_id text NOT NULL,
    receipt_kind text NOT NULL,
    receipt_ref text NOT NULL,
    target_kind text NOT NULL DEFAULT '',
    target_ref text NOT NULL DEFAULT '',
    signature text NOT NULL,
    normalizer_version text NOT NULL,
    component text NOT NULL DEFAULT '',
    phase text NOT NULL DEFAULT '',
    error_code text NOT NULL DEFAULT '',
    exit_status integer,
    template text NOT NULL,
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
    occurred_at timestamptz NOT NULL,
    PRIMARY KEY (tenant_id, receipt_kind, receipt_ref),
    CHECK (receipt_kind IN ('job')),
    CHECK (BTRIM(receipt_ref) <> ''),
    CHECK (target_kind IN ('', 'stack')),
    CHECK (signature ~ '^[0-9a-f]{64}$'),
    CHECK (normalizer_version ~ '^fk-normalizer/v[0-9]+$'),
    CHECK (char_length(template) <= 512),
    CHECK (outcome IN ('failed'))
);

CREATE INDEX IF NOT EXISTS idx_failure_occurrences_signature
    ON failure_occurrences (tenant_id, signature, occurred_at DESC);

ALTER TABLE failure_occurrences ENABLE ROW LEVEL SECURITY;
ALTER TABLE failure_occurrences FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation ON failure_occurrences;
CREATE POLICY tenant_isolation ON failure_occurrences
    USING (tenant_id = current_setting('app.tenant_id', true))
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true));
