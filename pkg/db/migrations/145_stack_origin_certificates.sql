-- Custody of the Cloudflare Origin CA certificate ids of managed kombify.me
-- installs (ADR-0047). Ids, hostnames and expiry only: the private key is
-- generated on the node and never exists in the control plane, and the
-- certificate itself is public data that is not stored here.
-- retired_certificate_ids lists superseded certificates that still need a
-- Cloudflare revocation; a revocation clears its id, decommission revokes the
-- current id and every retired one.

CREATE TABLE IF NOT EXISTS stack_origin_certificates (
    tenant_id text NOT NULL,
    stack_id text NOT NULL,
    certificate_id text NOT NULL,
    hostnames text[] NOT NULL,
    certificate_sha256 text,
    not_after timestamptz NOT NULL,
    installed_at timestamptz,
    retired_certificate_ids text[] NOT NULL DEFAULT '{}',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, stack_id),
    FOREIGN KEY (tenant_id, stack_id)
        REFERENCES stacks (tenant_id, id)
        ON DELETE CASCADE,
    CHECK (BTRIM(certificate_id) <> ''),
    CHECK (cardinality(hostnames) > 0),
    CHECK (certificate_sha256 IS NULL OR certificate_sha256 ~ '^sha256:[0-9a-f]{64}$')
);

ALTER TABLE stack_origin_certificates ENABLE ROW LEVEL SECURITY;
ALTER TABLE stack_origin_certificates FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation ON stack_origin_certificates;
CREATE POLICY tenant_isolation ON stack_origin_certificates
    USING (tenant_id = current_setting('app.tenant_id', true))
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true));

DROP TRIGGER IF EXISTS set_stack_origin_certificates_updated_at ON stack_origin_certificates;
CREATE TRIGGER set_stack_origin_certificates_updated_at BEFORE UPDATE ON stack_origin_certificates
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();
