-- kombify Guard hardening (finding F-18): the two cross-tenant SECURITY DEFINER
-- readers of migration 133 were executable by PUBLIC, so any database role,
-- including a tenant-scoped NOBYPASSRLS one, could list other tenants'
-- identifiers through them. Execution is limited to the owning application
-- role, which is the role the reconciler runs as; the pattern is migration 070.
DO $guard_reach_definer_execute_posture$
DECLARE
    active_schema text := current_schema();
    migration_role text := current_user;
    boundary_function text;
BEGIN
    FOREACH boundary_function IN ARRAY ARRAY[
        'techstack_list_due_guard_reach(timestamptz, text, text, integer)',
        'techstack_find_guard_reach_by_connect(text, text)'
    ] LOOP
        EXECUTE pg_catalog.format(
            'REVOKE ALL ON FUNCTION %I.%s FROM PUBLIC',
            active_schema,
            boundary_function
        );
        EXECUTE pg_catalog.format(
            'GRANT EXECUTE ON FUNCTION %I.%s TO %I',
            active_schema,
            boundary_function,
            migration_role
        );
    END LOOP;
END
$guard_reach_definer_execute_posture$;

-- kombify Guard hardening (finding F-09): a legacy bearer worker's key bind is
-- held as upgrade_pending until the owner approves that exact key. The bearer
-- credential that authenticates a bind request says nothing about which key
-- belongs to the server, so whoever presented it first must not win the bind.
-- approved_by and approved_at record the owner decision.
ALTER TABLE worker_identities ADD COLUMN IF NOT EXISTS approved_by text;
ALTER TABLE worker_identities ADD COLUMN IF NOT EXISTS approved_at timestamptz;

DO $guard_identity_upgrade_state$
DECLARE
    state_check text;
BEGIN
    FOR state_check IN
        SELECT c.conname
        FROM pg_catalog.pg_constraint c
        WHERE c.conrelid = 'worker_identities'::regclass
          AND c.contype = 'c'
          AND pg_catalog.pg_get_constraintdef(c.oid) LIKE '%issued_unconfirmed%'
    LOOP
        EXECUTE pg_catalog.format('ALTER TABLE worker_identities DROP CONSTRAINT %I', state_check);
    END LOOP;
    ALTER TABLE worker_identities ADD CONSTRAINT worker_identities_state_check
        CHECK (state IN ('issued_unconfirmed', 'bound', 'revoked', 'upgrade_pending'));
END
$guard_identity_upgrade_state$;

-- kombify Guard hardening (finding F-12): publishing a server at a public
-- kombify.me address is an explicit owner decision. public_enabled is off by
-- default, so no route is created and nothing is served until the owner turns
-- it on and names the service and port (there is no default origin). A row
-- that had a route before this migration reads as not enabled and the
-- reconciler withdraws its route assignment.
ALTER TABLE guard_reach_states ADD COLUMN IF NOT EXISTS public_enabled boolean NOT NULL DEFAULT false;
ALTER TABLE guard_reach_states ADD COLUMN IF NOT EXISTS public_service text NOT NULL DEFAULT '';
ALTER TABLE guard_reach_states ADD COLUMN IF NOT EXISTS public_scheme text NOT NULL DEFAULT '';
ALTER TABLE guard_reach_states ADD COLUMN IF NOT EXISTS public_port integer NOT NULL DEFAULT 0;
ALTER TABLE guard_reach_states ADD COLUMN IF NOT EXISTS public_enabled_by text NOT NULL DEFAULT '';
ALTER TABLE guard_reach_states ADD COLUMN IF NOT EXISTS public_enabled_at timestamptz;

DO $guard_reach_public$
DECLARE
    existing_check text;
BEGIN
    FOR existing_check IN
        SELECT c.conname
        FROM pg_catalog.pg_constraint c
        WHERE c.conrelid = 'guard_reach_states'::regclass
          AND c.contype = 'c'
          AND (pg_catalog.pg_get_constraintdef(c.oid) LIKE '%me_state%' OR c.conname = 'guard_reach_states_public_check')
    LOOP
        EXECUTE pg_catalog.format('ALTER TABLE guard_reach_states DROP CONSTRAINT %I', existing_check);
    END LOOP;
    ALTER TABLE guard_reach_states ADD CONSTRAINT guard_reach_states_me_state_check
        CHECK (me_state IN ('pending', 'registered', 'failed', 'revoked', 'not_enabled'));
    ALTER TABLE guard_reach_states ADD CONSTRAINT guard_reach_states_public_check
        CHECK (public_port BETWEEN 0 AND 65535 AND (NOT public_enabled OR (public_port >= 1 AND public_service <> '')));
END
$guard_reach_public$;
