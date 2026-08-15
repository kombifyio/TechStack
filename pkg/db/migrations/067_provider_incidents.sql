-- Tenant-scoped, secret-free provider failure incidents.
ALTER TABLE ril_workflow_runs DROP CONSTRAINT IF EXISTS ril_workflow_runs_type_check;
ALTER TABLE ril_workflow_runs ADD CONSTRAINT ril_workflow_runs_type_check CHECK (type IN (
    'action_card_remediation', 'drift_correction', 'cert_rotation',
    'rolling_update', 'service_migration', 'provider_incident_advisory'
));

CREATE TABLE IF NOT EXISTS provider_incidents (
    tenant_id text NOT NULL REFERENCES techstack_tenants(id) ON DELETE RESTRICT,
    incident_key text NOT NULL,
    lease_id text NOT NULL,
    operation_id text NOT NULL,
    resource_generation_id text NOT NULL,
    provider_id text NOT NULL,
    adapter_id text NOT NULL,
    stage text NOT NULL,
    receipt_sequence bigint NOT NULL CHECK (receipt_sequence > 0),
    receipt_digest text NOT NULL,
    reason_code text NOT NULL,
    retryable boolean NOT NULL,
    correlation_id text NOT NULL,
    classification text NOT NULL CHECK (classification IN ('unknown')),
    advisor_state text NOT NULL CHECK (advisor_state IN ('pending','unavailable','rejected','completed')),
    evidence_json jsonb NOT NULL,
    advisory_json jsonb NOT NULL DEFAULT '{}'::jsonb,
    workflow_run_id text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    PRIMARY KEY (tenant_id, incident_key),
    UNIQUE (tenant_id, operation_id, receipt_sequence, receipt_digest)
);

CREATE INDEX IF NOT EXISTS idx_provider_incidents_tenant_updated
    ON provider_incidents (tenant_id, updated_at DESC);

CREATE TABLE IF NOT EXISTS provider_incident_advisory_attempts (
    tenant_id text NOT NULL,
    incident_key text NOT NULL,
    attempt bigint NOT NULL CHECK (attempt > 0),
    state text NOT NULL CHECK (state IN ('unavailable','rejected','completed')),
    advisory_json jsonb NOT NULL,
    error_classification text NOT NULL DEFAULT '',
    workflow_run_id text,
    created_at timestamptz NOT NULL,
    PRIMARY KEY (tenant_id, incident_key, attempt),
    FOREIGN KEY (tenant_id, incident_key)
        REFERENCES provider_incidents (tenant_id, incident_key) ON DELETE RESTRICT
);

CREATE INDEX IF NOT EXISTS idx_provider_incident_advisory_attempts_latest
    ON provider_incident_advisory_attempts (tenant_id, incident_key, attempt DESC);

ALTER TABLE provider_incidents ENABLE ROW LEVEL SECURITY;
ALTER TABLE provider_incidents FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation ON provider_incidents;
CREATE POLICY tenant_isolation ON provider_incidents
    USING (tenant_id = current_setting('app.tenant_id', true))
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true));

ALTER TABLE provider_incident_advisory_attempts ENABLE ROW LEVEL SECURITY;
ALTER TABLE provider_incident_advisory_attempts FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation ON provider_incident_advisory_attempts;
CREATE POLICY tenant_isolation ON provider_incident_advisory_attempts
    USING (tenant_id = current_setting('app.tenant_id', true))
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true));
