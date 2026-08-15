-- 002_rls_setup.sql
--
-- Row-Level-Security policies for tenant-scoped tables.
--
-- Authority: docs/plans/2026-05-01-techstack-v2-implementation-plan.md §5
-- Tenancy & Isolation Model. Every tenant-scoped table gets RLS enabled with a
-- single policy that compares `tenant_id` to the GUC `app.tenant_id`, which
-- the V2 runtime sets per request via `pkg/db.WithTenant`.
--
-- Notes:
-- * `current_setting('app.tenant_id', true)` returns NULL when the GUC is not
--   set; in that case the policy denies all rows (NULL = anything is FALSE).
-- * The pgx connection pool is owned by the kombify_techstack runtime user,
--   which must NOT be a superuser and must NOT have BYPASSRLS. The policies
--   below intentionally do not include a superuser bypass clause.
-- * `feature_rollouts` is platform-global (Admin API authoritative) and is
--   intentionally NOT enabled — it has no tenant_id.
-- * `identity_providers` rows are platform-global registry entries; tenant
--   scoping happens via `identity_subject_links`. Not RLS-enabled.

DO $$
DECLARE
    t text;
    tables text[] := ARRAY[
        'techstack_instances',
        'identity_subject_links',
        'stacks',
        'jobs',
        'workers',
        'agent_commands',
        'drift_results',
        'precheck_results',
        'feature_preferences',
        'wallet_items',
        'audit_events',
        'tofu_states'
    ];
BEGIN
    FOREACH t IN ARRAY tables LOOP
        EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
        EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
        EXECUTE format('DROP POLICY IF EXISTS tenant_isolation ON %I', t);
        EXECUTE format(
            'CREATE POLICY tenant_isolation ON %I '
            || 'USING (tenant_id = current_setting(''app.tenant_id'', true)) '
            || 'WITH CHECK (tenant_id = current_setting(''app.tenant_id'', true))',
            t
        );
    END LOOP;
END $$;
