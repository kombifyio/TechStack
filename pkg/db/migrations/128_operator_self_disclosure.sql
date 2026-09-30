-- 128_operator_self_disclosure.sql
-- Voluntary operator self-disclosure (CREATION-EXPERIENCE-STANDARD §4, trust
-- order 1). One row per authenticated principal. disclosure_json holds the raw
-- answers only; the capability score and band are derived by the Unifier on
-- every read, so a mapping change never needs a data migration and no client
-- can store a score.

CREATE TABLE IF NOT EXISTS operator_self_disclosures (
    id text PRIMARY KEY,
    tenant_id text NOT NULL REFERENCES techstack_tenants(id) ON DELETE CASCADE,
    owner_subject_id text NOT NULL,
    schema_version integer NOT NULL DEFAULT 1,
    source text NOT NULL,
    disclosure_json jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CHECK (BTRIM(id) <> ''),
    CHECK (BTRIM(owner_subject_id) <> ''),
    CHECK (schema_version = 1),
    CHECK (source IN ('cloud', 'techstack')),
    CHECK (jsonb_typeof(disclosure_json) = 'object')
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_operator_self_disclosures_principal
    ON operator_self_disclosures (tenant_id, owner_subject_id);

ALTER TABLE operator_self_disclosures ENABLE ROW LEVEL SECURITY;
ALTER TABLE operator_self_disclosures FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation ON operator_self_disclosures;
CREATE POLICY tenant_isolation ON operator_self_disclosures
    USING (tenant_id = current_setting('app.tenant_id', true))
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true));

DROP TRIGGER IF EXISTS set_operator_self_disclosures_updated_at ON operator_self_disclosures;
CREATE TRIGGER set_operator_self_disclosures_updated_at BEFORE UPDATE ON operator_self_disclosures
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();
