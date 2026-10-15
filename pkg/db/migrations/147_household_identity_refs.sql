-- Household identity references (Kombify Connect D10) and the smart-home
-- notice admission ledger (Companion Feed contract v1, section 2.5).
--
-- household_identity_refs is the HomelabIdentityRefV1 link authority: one row
-- ties a Cloud (Auth0) subject to a Pocket ID username on one stack. An
-- invitation alone writes status 'invited'; only a verified Pocket ID OIDC
-- login of the invited person sets 'linked'. The homelab identity provider
-- stays the authority for the person: this table never holds a credential, a
-- passkey, a session or an activation link.

CREATE TABLE IF NOT EXISTS household_identity_refs (
    tenant_id text NOT NULL,
    stack_id text NOT NULL,
    username text NOT NULL,
    idp text NOT NULL DEFAULT 'pocket-id',
    role text NOT NULL DEFAULT 'member',
    owner_subject_id text NOT NULL,
    cloud_subject text NOT NULL,
    email text NOT NULL,
    display_name text NOT NULL DEFAULT '',
    status text NOT NULL DEFAULT 'invited',
    invited_at timestamptz NOT NULL DEFAULT now(),
    linked_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, stack_id, username),
    UNIQUE (tenant_id, stack_id, cloud_subject),
    FOREIGN KEY (tenant_id, stack_id)
        REFERENCES stacks (tenant_id, id)
        ON DELETE CASCADE,
    CHECK (idp IN ('pocket-id', 'tinyauth')),
    CHECK (role IN ('owner', 'member')),
    CHECK (status IN ('invited', 'linked')),
    CHECK (BTRIM(username) <> ''),
    CHECK (BTRIM(cloud_subject) <> ''),
    CHECK (BTRIM(owner_subject_id) <> ''),
    CHECK ((status = 'linked') = (linked_at IS NOT NULL))
);

CREATE INDEX IF NOT EXISTS idx_household_identity_refs_owner
    ON household_identity_refs (tenant_id, owner_subject_id);

ALTER TABLE household_identity_refs ENABLE ROW LEVEL SECURITY;
ALTER TABLE household_identity_refs FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation ON household_identity_refs;
CREATE POLICY tenant_isolation ON household_identity_refs
    USING (tenant_id = current_setting('app.tenant_id', true))
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true));

DROP TRIGGER IF EXISTS set_household_identity_refs_updated_at ON household_identity_refs;
CREATE TRIGGER set_household_identity_refs_updated_at BEFORE UPDATE ON household_identity_refs
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- One row per accepted user notification, kept only long enough to enforce the
-- per-node rate limit (30 per hour). It holds no text, no recipient and no
-- audience: the notice itself lives only in the Feed item the engine stores.
CREATE TABLE IF NOT EXISTS user_notice_admissions (
    id bigserial PRIMARY KEY,
    tenant_id text NOT NULL,
    stack_id text NOT NULL,
    admitted_at timestamptz NOT NULL DEFAULT now(),
    CHECK (BTRIM(stack_id) <> '')
);

CREATE INDEX IF NOT EXISTS idx_user_notice_admissions_node
    ON user_notice_admissions (tenant_id, stack_id, admitted_at DESC);

ALTER TABLE user_notice_admissions ENABLE ROW LEVEL SECURITY;
ALTER TABLE user_notice_admissions FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation ON user_notice_admissions;
CREATE POLICY tenant_isolation ON user_notice_admissions
    USING (tenant_id = current_setting('app.tenant_id', true))
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true));
