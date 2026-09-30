-- A revision belongs to the owner, not a homelab row: soft deletion and a
-- later replacement must never make an old If-Match valid again. The receipt
-- is committed with the winning identity edit before the client is answered.
CREATE TABLE homelab_identity_revisions (
    tenant_id text NOT NULL REFERENCES techstack_tenants(id) ON DELETE CASCADE,
    owner_subject_id text NOT NULL,
    revision bigint NOT NULL DEFAULT 0 CHECK (revision >= 0),
    PRIMARY KEY (tenant_id, owner_subject_id),
    CHECK (BTRIM(owner_subject_id) <> '')
);

CREATE TABLE homelab_identity_mutation_receipts (
    tenant_id text NOT NULL,
    owner_subject_id text NOT NULL,
    mutation_id text NOT NULL,
    payload_sha256 text NOT NULL,
    revision bigint NOT NULL CHECK (revision > 0),
    result_json jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, owner_subject_id, mutation_id),
    FOREIGN KEY (tenant_id, owner_subject_id)
        REFERENCES homelab_identity_revisions(tenant_id, owner_subject_id) ON DELETE CASCADE,
    CHECK (length(mutation_id) BETWEEN 16 AND 128),
    CHECK (payload_sha256 ~ '^[0-9a-f]{64}$')
);

ALTER TABLE homelab_identity_revisions ENABLE ROW LEVEL SECURITY;
ALTER TABLE homelab_identity_revisions FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON homelab_identity_revisions
    USING (tenant_id = current_setting('app.tenant_id', true))
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true));

ALTER TABLE homelab_identity_mutation_receipts ENABLE ROW LEVEL SECURITY;
ALTER TABLE homelab_identity_mutation_receipts FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON homelab_identity_mutation_receipts
    USING (tenant_id = current_setting('app.tenant_id', true))
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true));
