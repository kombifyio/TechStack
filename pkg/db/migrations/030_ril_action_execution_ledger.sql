CREATE TABLE IF NOT EXISTS ril_action_execution_ledger (
    tenant_id text NOT NULL,
    idempotency_key text NOT NULL,
    execution_id text NOT NULL,
    request_digest text NOT NULL,
    requested_at timestamptz NOT NULL,
    valid_until timestamptz NOT NULL,
    reservation_token text NOT NULL,
    status text NOT NULL DEFAULT 'in-progress',
    evidence_json jsonb,
    completed_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, idempotency_key),
    UNIQUE (tenant_id, execution_id),
    CHECK (status IN ('in-progress', 'completed')),
    CHECK (request_digest ~ '^sha256:[a-f0-9]{64}$'),
    CHECK (valid_until > requested_at),
    CHECK (
        (status = 'in-progress' AND evidence_json IS NULL AND completed_at IS NULL)
        OR (status = 'completed' AND evidence_json IS NOT NULL AND completed_at IS NOT NULL)
    )
);

CREATE INDEX IF NOT EXISTS idx_ril_action_execution_ledger_status
    ON ril_action_execution_ledger (tenant_id, status, valid_until);

ALTER TABLE ril_action_execution_ledger ENABLE ROW LEVEL SECURITY;
ALTER TABLE ril_action_execution_ledger FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation ON ril_action_execution_ledger;
CREATE POLICY tenant_isolation ON ril_action_execution_ledger
    USING (tenant_id = current_setting('app.tenant_id', true))
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true));
