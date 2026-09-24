-- 099_user_onboarding_state.sql
-- Per-principal onboarding record (ONBOARDING-JOURNEY-STANDARD §8). state_json
-- is the exact onboarding-state.v1 document; the scalar columns
-- (journey_version, status, revision) are projections for queries and the
-- compare-and-swap on user actions. Rows are created on the first user action
-- or derived completion, never on a pristine read.

CREATE TABLE IF NOT EXISTS user_onboarding_state (
    id text PRIMARY KEY,
    tenant_id text NOT NULL REFERENCES techstack_tenants(id) ON DELETE CASCADE,
    owner_subject_id text NOT NULL,
    product text NOT NULL,
    journey_id text NOT NULL,
    journey_version integer NOT NULL,
    schema_version integer NOT NULL DEFAULT 1,
    state_json jsonb NOT NULL,
    status text NOT NULL,
    revision integer NOT NULL DEFAULT 0,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CHECK (BTRIM(id) <> ''),
    CHECK (BTRIM(owner_subject_id) <> ''),
    CHECK (BTRIM(journey_id) <> ''),
    CHECK (product IN ('techstack')),
    CHECK (schema_version = 1),
    CHECK (journey_version >= 1),
    CHECK (revision >= 0),
    CHECK (status IN ('active', 'completed', 'dismissed'))
);

-- One record per principal and journey; a product column keeps the table
-- ready for a second journey without a second table.
CREATE UNIQUE INDEX IF NOT EXISTS uq_user_onboarding_state_principal_journey
    ON user_onboarding_state (tenant_id, owner_subject_id, product, journey_id);

ALTER TABLE user_onboarding_state ENABLE ROW LEVEL SECURITY;
ALTER TABLE user_onboarding_state FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation ON user_onboarding_state;
CREATE POLICY tenant_isolation ON user_onboarding_state
    USING (tenant_id = current_setting('app.tenant_id', true))
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true));

DROP TRIGGER IF EXISTS set_user_onboarding_state_updated_at ON user_onboarding_state;
CREATE TRIGGER set_user_onboarding_state_updated_at BEFORE UPDATE ON user_onboarding_state
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();
