-- 043_membership_user_lookup.sql
--
-- Organization resolution at login needs to enumerate the authenticated
-- user's own memberships across tenants (ListMembershipsByUser). The
-- tenant_isolation policy on techstack_memberships fences every read to the
-- request-local app.tenant_id GUC, which is unknown at that point. Add a
-- permissive SELECT-only policy keyed on a request-local app.user_id GUC so
-- the control-plane store can read exactly the caller's own rows and nothing
-- else. tenant_isolation stays authoritative for every other command.

CREATE INDEX IF NOT EXISTS idx_techstack_memberships_user
    ON techstack_memberships (user_id);

DROP POLICY IF EXISTS membership_self_lookup ON techstack_memberships;
CREATE POLICY membership_self_lookup ON techstack_memberships
    FOR SELECT
    USING (
        COALESCE(current_setting('app.user_id', true), '') <> ''
        AND user_id = current_setting('app.user_id', true)
    );
