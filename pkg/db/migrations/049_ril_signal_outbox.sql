-- Durable, tenant-scoped producer outbox for canonical RIL signals. Delivery
-- workers receive only a short-lived claim capability; plaintext claim tokens
-- and target credentials are never persisted.

SET LOCAL lock_timeout = '5s';
SELECT pg_catalog.set_config(
    'search_path',
    pg_catalog.quote_ident(pg_catalog.current_schema()) || ', pg_catalog, pg_temp',
    true
);

CREATE TABLE IF NOT EXISTS ril_signal_outbox (
    sequence_id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    tenant_id text NOT NULL,
    signal_id text NOT NULL,
    dedupe_key text NOT NULL,
    server_id text NOT NULL,
    source text NOT NULL,
    severity text NOT NULL,
    priority text NOT NULL,
    trace_id text NOT NULL,
    audit_id text NOT NULL,
    envelope_json jsonb NOT NULL,
    available_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    attempts integer NOT NULL DEFAULT 0,
    claim_generation bigint NOT NULL DEFAULT 0,
    claim_owner text,
    claim_token_digest bytea,
    claim_expires_at timestamptz,
    delivered_at timestamptz,
    failed_at timestamptz,
    last_error text,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT ril_signal_outbox_server_fk
        FOREIGN KEY (tenant_id, server_id)
        REFERENCES servers (tenant_id, id)
        ON DELETE CASCADE,
    CONSTRAINT ril_signal_outbox_signal_unique UNIQUE (tenant_id, signal_id),
    CONSTRAINT ril_signal_outbox_dedupe_unique UNIQUE (tenant_id, dedupe_key),
    CONSTRAINT ril_signal_outbox_source_valid CHECK (
        source IN ('health', 'drift', 'backup', 'cert', 'deploy', 'resource', 'connector')
    ),
    CONSTRAINT ril_signal_outbox_severity_valid CHECK (
        severity IN ('info', 'low', 'medium', 'high', 'critical')
    ),
    CONSTRAINT ril_signal_outbox_priority_valid CHECK (
        priority IN ('low', 'normal', 'high', 'urgent')
    ),
    CONSTRAINT ril_signal_outbox_identity_valid CHECK (
        NULLIF(BTRIM(signal_id), '') IS NOT NULL
        AND NULLIF(BTRIM(dedupe_key), '') IS NOT NULL
        AND NULLIF(BTRIM(trace_id), '') IS NOT NULL
        AND NULLIF(BTRIM(audit_id), '') IS NOT NULL
    ),
    CONSTRAINT ril_signal_outbox_claim_valid CHECK (
        attempts >= 0 AND claim_generation >= 0
        AND ((claim_owner IS NULL AND claim_token_digest IS NULL AND claim_expires_at IS NULL)
          OR (claim_owner IS NOT NULL AND octet_length(claim_token_digest) = 32 AND claim_expires_at IS NOT NULL))
    ),
    CONSTRAINT ril_signal_outbox_terminal_valid CHECK (
        delivered_at IS NULL OR failed_at IS NULL
    )
);

CREATE INDEX IF NOT EXISTS idx_ril_signal_outbox_due
    ON ril_signal_outbox (tenant_id, priority DESC, available_at, sequence_id)
    WHERE delivered_at IS NULL AND failed_at IS NULL;

ALTER TABLE ril_signal_outbox ENABLE ROW LEVEL SECURITY;
ALTER TABLE ril_signal_outbox FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation ON ril_signal_outbox;
CREATE POLICY tenant_isolation ON ril_signal_outbox
    USING (tenant_id = current_setting('app.tenant_id', true))
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true));
