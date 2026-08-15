-- 004_agent_enrollments.sql
--
-- Phase 7.1 mTLS Identity Binding: per-agent enrollment registry.
-- One row per (tenant, agent) pair. The gRPC Register() handler looks
-- this row up after extracting the peer's TLS client cert and:
--   1. asserts cert.Subject.CN == req.AgentId
--   2. asserts the row exists, is not revoked, and matches cert_serial
--   3. pins ConnectedAgent.Tenant + AllowedCommandClasses from this row
--
-- AllowedCommandClasses is JSONB-encoded (e.g. ["health_check","get_logs"])
-- so callers can extend the catalogue without a migration. RLS scopes all
-- SELECTs to current_setting('app.tenant_id', true) — set per-transaction
-- by pkg/db.WithTenant.

CREATE TABLE IF NOT EXISTS techstack_agent_enrollments (
    agent_id                TEXT        NOT NULL,
    tenant_id               TEXT        NOT NULL,
    cert_serial             TEXT        NOT NULL,
    cert_fingerprint        TEXT        NOT NULL,
    allowed_command_classes JSONB       NOT NULL DEFAULT '[]'::jsonb,
    enrolled_at             TIMESTAMPTZ NOT NULL DEFAULT now(),
    revoked_at              TIMESTAMPTZ NULL,
    enrolled_by             TEXT        NOT NULL DEFAULT '',
    PRIMARY KEY (tenant_id, agent_id)
);

CREATE INDEX IF NOT EXISTS techstack_agent_enrollments_agent_id_idx
    ON techstack_agent_enrollments (agent_id);

CREATE INDEX IF NOT EXISTS techstack_agent_enrollments_cert_serial_idx
    ON techstack_agent_enrollments (cert_serial);

ALTER TABLE techstack_agent_enrollments ENABLE ROW LEVEL SECURITY;
ALTER TABLE techstack_agent_enrollments FORCE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS techstack_agent_enrollments_tenant_isolation
    ON techstack_agent_enrollments;
CREATE POLICY techstack_agent_enrollments_tenant_isolation
    ON techstack_agent_enrollments
    USING (tenant_id = current_setting('app.tenant_id', true))
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true));
