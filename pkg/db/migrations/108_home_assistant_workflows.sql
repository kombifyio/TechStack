-- Dedicated Home Assistant migrations retain the original source and archives.
-- Registration remains disabled until all native activity adapters are bound.
ALTER TABLE ril_workflow_runs DROP CONSTRAINT IF EXISTS ril_workflow_runs_type_check;
ALTER TABLE ril_workflow_runs ADD CONSTRAINT ril_workflow_runs_type_check CHECK (type IN (
    'action_card_remediation', 'drift_correction', 'cert_rotation', 'rolling_update',
    'service_migration', 'provider_incident_advisory',
    'home_assistant_migration', 'home_assistant_rollback'
));

-- Explicit tenant predicates are used by the node-local journal. Neither table
-- contains endpoint tokens, archive data, or source/target deletion authority.
CREATE TABLE home_assistant_operations (
    tenant_id text NOT NULL,
    operation_key text NOT NULL,
    receipt jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, operation_key)
);
CREATE TABLE home_assistant_recovery_keys (
    tenant_id text NOT NULL,
    operation_key text NOT NULL,
    password_enc text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, operation_key)
);
