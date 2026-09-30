-- Advanced capability issuer (StackKits ADR-0031 amendment 2026-09-24).
--
-- One Ed25519 issuer key per Techstack installation signs short-lived
-- stackkit.advanced-capability/v1 documents for managed deployments. The
-- private seed is stored only as TECHSTACK_ENCRYPTION_KEY ciphertext
-- (enc:v1:, AES-256-GCM); the public key and key id are public trust data.

CREATE TABLE IF NOT EXISTS advanced_issuer_keys (
    singleton boolean PRIMARY KEY DEFAULT true,
    issuer_id text NOT NULL,
    key_id text NOT NULL,
    public_key text NOT NULL,
    private_key_enc text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CHECK (singleton),
    CHECK (issuer_id ~ '^[a-z0-9][a-z0-9._-]{0,127}$'),
    CHECK (key_id ~ '^ed25519://sha256/[0-9a-f]{64}$'),
    CHECK (private_key_enc LIKE 'enc:v1:%')
);

-- Every issued capability is recorded before it leaves the issuer, so each
-- managed deployment has an auditable capability history. Rows are kept
-- after the stack is removed.
CREATE TABLE IF NOT EXISTS advanced_capability_issuances (
    tenant_id text NOT NULL,
    capability_id text NOT NULL,
    stack_id text NOT NULL,
    stackkit_stack_id text NOT NULL,
    owner_ref text NOT NULL,
    operations text[] NOT NULL,
    issuer_id text NOT NULL,
    key_id text NOT NULL,
    issued_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, capability_id),
    CHECK (expires_at > issued_at),
    CHECK (cardinality(operations) >= 1)
);

CREATE INDEX IF NOT EXISTS advanced_capability_issuances_stack_idx
    ON advanced_capability_issuances (tenant_id, stack_id, issued_at DESC);

ALTER TABLE advanced_capability_issuances ENABLE ROW LEVEL SECURITY;
ALTER TABLE advanced_capability_issuances FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation ON advanced_capability_issuances;
CREATE POLICY tenant_isolation ON advanced_capability_issuances
    USING (tenant_id = current_setting('app.tenant_id', true))
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true));

-- The host's import of the installation trust bundle, and the local Owner
-- reference StackKits bound it to. Capabilities for the deployment are scoped
-- to this Owner and StackKit stack id.
CREATE TABLE IF NOT EXISTS advanced_trust_bindings (
    tenant_id text NOT NULL,
    stack_id text NOT NULL,
    stackkit_stack_id text NOT NULL DEFAULT '',
    owner_ref text NOT NULL,
    bundle_sha256 text NOT NULL,
    issuer_id text NOT NULL,
    key_id text NOT NULL,
    imported_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, stack_id),
    CHECK (owner_ref ~ '^owner/local/[0-9a-f]{32}$'),
    CHECK (bundle_sha256 ~ '^sha256:[0-9a-f]{64}$')
);

ALTER TABLE advanced_trust_bindings ENABLE ROW LEVEL SECURITY;
ALTER TABLE advanced_trust_bindings FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation ON advanced_trust_bindings;
CREATE POLICY tenant_isolation ON advanced_trust_bindings
    USING (tenant_id = current_setting('app.tenant_id', true))
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true));
