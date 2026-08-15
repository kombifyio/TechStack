-- ClientPairingEnvelope v1 persistence.
--
-- This table is intentionally separate from pairing_tokens, which belongs to
-- worker enrollment. Only SHA-256 hashes are persisted; successful redemption
-- is a single conditional UPDATE so replay can never produce a second success.

CREATE TABLE IF NOT EXISTS client_pairing_codes (
    id text PRIMARY KEY,
    tenant_id text NOT NULL REFERENCES techstack_tenants(id) ON DELETE CASCADE,
    instance_id text NOT NULL,
    code_hash text NOT NULL,
    tls_fingerprint_sha256 text NOT NULL,
    issued_by_subject_id text NOT NULL,
    issued_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL,
    consumed_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, code_hash),
    CHECK (code_hash ~ '^[a-f0-9]{64}$'),
    CHECK (tls_fingerprint_sha256 ~ '^[a-f0-9]{64}$'),
    CHECK (expires_at > issued_at),
    CHECK (expires_at <= issued_at + interval '10 minutes'),
    CHECK (consumed_at IS NULL OR consumed_at >= issued_at)
);

CREATE INDEX IF NOT EXISTS idx_client_pairing_codes_expiry
    ON client_pairing_codes (tenant_id, expires_at)
    WHERE consumed_at IS NULL;

ALTER TABLE client_pairing_codes ENABLE ROW LEVEL SECURITY;
ALTER TABLE client_pairing_codes FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation ON client_pairing_codes;
CREATE POLICY tenant_isolation ON client_pairing_codes
    USING (tenant_id = current_setting('app.tenant_id', true))
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true));

DROP TRIGGER IF EXISTS set_client_pairing_codes_updated_at ON client_pairing_codes;
CREATE TRIGGER set_client_pairing_codes_updated_at
    BEFORE UPDATE ON client_pairing_codes
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();
