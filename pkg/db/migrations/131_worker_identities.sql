-- 131_worker_identities.sql
-- kombify Guard host-bound identity. A worker may carry one host-key binding:
-- the public half of an Ed25519 key generated on the server. The binding is
-- created at pairing redemption (issued_unconfirmed), becomes bound with the
-- first request authenticated by a valid proof of possession, and ends as
-- revoked on detach, retire or revoke. Only public key material is stored.
-- worker_proof_jtis is the replay ledger for those proofs: every accepted
-- proof identifier is kept until its iat window has passed.

CREATE TABLE IF NOT EXISTS worker_identities (
    tenant_id text NOT NULL,
    worker_id text NOT NULL,
    state text NOT NULL,
    key_thumbprint text NOT NULL,
    public_key_jwk jsonb NOT NULL,
    attempt_id text,
    pairing_token_hash text,
    credential_epoch bigint NOT NULL DEFAULT 1,
    previous_key_thumbprint text,
    previous_public_key_jwk jsonb,
    previous_valid_until timestamptz,
    issued_at timestamptz NOT NULL DEFAULT now(),
    confirmed_at timestamptz,
    revoked_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, worker_id),
    FOREIGN KEY (tenant_id, worker_id) REFERENCES workers (tenant_id, id) ON DELETE CASCADE,
    CHECK (state IN ('issued_unconfirmed', 'bound', 'revoked')),
    CHECK (BTRIM(key_thumbprint) <> ''),
    CHECK (credential_epoch >= 1),
    CHECK (jsonb_typeof(public_key_jwk) = 'object'),
    CHECK (state <> 'bound' OR confirmed_at IS NOT NULL),
    CHECK (state <> 'revoked' OR revoked_at IS NOT NULL),
    CHECK ((previous_key_thumbprint IS NULL) = (previous_valid_until IS NULL))
);

-- One redemption per pairing capability: a repeated capability resolves the
-- same binding instead of creating a second one.
CREATE UNIQUE INDEX IF NOT EXISTS uq_worker_identities_pairing
    ON worker_identities (tenant_id, pairing_token_hash)
    WHERE pairing_token_hash IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_worker_identities_thumbprint
    ON worker_identities (key_thumbprint);

CREATE TABLE IF NOT EXISTS worker_proof_jtis (
    tenant_id text NOT NULL,
    worker_id text NOT NULL,
    jti text NOT NULL,
    expires_at timestamptz NOT NULL,
    PRIMARY KEY (tenant_id, worker_id, jti),
    FOREIGN KEY (tenant_id, worker_id) REFERENCES workers (tenant_id, id) ON DELETE CASCADE,
    CHECK (length(jti) BETWEEN 16 AND 128)
);

CREATE INDEX IF NOT EXISTS idx_worker_proof_jtis_expiry
    ON worker_proof_jtis (tenant_id, worker_id, expires_at);

ALTER TABLE worker_identities ENABLE ROW LEVEL SECURITY;
ALTER TABLE worker_identities FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation ON worker_identities;
CREATE POLICY tenant_isolation ON worker_identities
    USING (tenant_id = current_setting('app.tenant_id', true))
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true));

ALTER TABLE worker_proof_jtis ENABLE ROW LEVEL SECURITY;
ALTER TABLE worker_proof_jtis FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation ON worker_proof_jtis;
CREATE POLICY tenant_isolation ON worker_proof_jtis
    USING (tenant_id = current_setting('app.tenant_id', true))
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true));

DROP TRIGGER IF EXISTS set_worker_identities_updated_at ON worker_identities;
CREATE TRIGGER set_worker_identities_updated_at BEFORE UPDATE ON worker_identities
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();
