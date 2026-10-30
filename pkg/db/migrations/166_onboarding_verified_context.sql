-- First hosted user-data consumer of the canonical Common verifier installed
-- by 165. Existing native/local and separate system roles retain their own
-- authority; the hosted HTTP login cannot switch role or change session_user.
-- There is no flag/GUC that disables this additional hosted restriction.
-- Issuer key registration runs only in the governed migration-custody path;
-- neither a signing key nor migration rights reach the application runtime.
SET LOCAL lock_timeout = '5s';

ALTER TABLE user_onboarding_state ENABLE ROW LEVEL SECURITY;
ALTER TABLE user_onboarding_state FORCE ROW LEVEL SECURITY;

CREATE POLICY onboarding_verified_identity ON user_onboarding_state
    AS RESTRICTIVE FOR ALL TO PUBLIC
    USING (
        session_user <> 'techstack_application'
        OR (
            tenant_id = (SELECT kombify_ctx.org_id())
            AND owner_subject_id = (SELECT kombify_ctx.subject())
        )
    )
    WITH CHECK (
        session_user <> 'techstack_application'
        OR (
            tenant_id = (SELECT kombify_ctx.org_id())
            AND owner_subject_id = (SELECT kombify_ctx.subject())
        )
    );
