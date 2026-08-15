-- 006_pocketbase_retirement_schema.sql
--
-- Adds the missing Postgres control-plane tables required to retire
-- PocketBase as TechStack's runtime state authority. These tables are scoped to
-- TechStack runtime identity and control-plane state; they are not a central
-- kombify Cloud customer directory.

DROP INDEX IF EXISTS idx_stacks_tenant_name_active;
CREATE UNIQUE INDEX IF NOT EXISTS idx_stacks_owner_tenant_name_active
    ON stacks (tenant_id, COALESCE(owner_subject_id, ''), lower(name))
    WHERE deleted_at IS NULL;

CREATE TABLE IF NOT EXISTS techstack_tenants (
    id text PRIMARY KEY,
    external_org_id text,
    slug text,
    display_name text NOT NULL,
    kind text NOT NULL DEFAULT 'self_hosted',
    status text NOT NULL DEFAULT 'active',
    metadata_json jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (external_org_id),
    UNIQUE (slug),
    CHECK (kind IN ('self_hosted', 'saas', 'embedded')),
    CHECK (status IN ('active', 'suspended', 'archived'))
);

CREATE TABLE IF NOT EXISTS techstack_users (
    id text PRIMARY KEY,
    primary_email text,
    display_name text,
    status text NOT NULL DEFAULT 'active',
    metadata_json jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CHECK (status IN ('active', 'disabled', 'archived'))
);

CREATE INDEX IF NOT EXISTS idx_techstack_users_email
    ON techstack_users (lower(primary_email));

CREATE TABLE IF NOT EXISTS techstack_roles (
    id text PRIMARY KEY,
    tenant_id text NOT NULL REFERENCES techstack_tenants(id) ON DELETE CASCADE,
    role_key text NOT NULL,
    display_name text NOT NULL,
    permissions_json jsonb NOT NULL DEFAULT '[]'::jsonb,
    metadata_json jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, role_key)
);

CREATE TABLE IF NOT EXISTS techstack_memberships (
    id text PRIMARY KEY,
    tenant_id text NOT NULL REFERENCES techstack_tenants(id) ON DELETE CASCADE,
    user_id text NOT NULL REFERENCES techstack_users(id) ON DELETE CASCADE,
    role_key text NOT NULL,
    provider_key text,
    subject_id text,
    external_org_id text,
    status text NOT NULL DEFAULT 'active',
    metadata_json jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, user_id),
    UNIQUE (tenant_id, provider_key, subject_id),
    CHECK (status IN ('active', 'invited', 'disabled', 'archived'))
);

CREATE INDEX IF NOT EXISTS idx_techstack_memberships_subject
    ON techstack_memberships (provider_key, subject_id);

CREATE TABLE IF NOT EXISTS local_auth_identities (
    id text PRIMARY KEY,
    tenant_id text NOT NULL REFERENCES techstack_tenants(id) ON DELETE CASCADE,
    user_id text NOT NULL REFERENCES techstack_users(id) ON DELETE CASCADE,
    email text NOT NULL,
    password_hash text NOT NULL,
    disabled boolean NOT NULL DEFAULT false,
    password_updated_at timestamptz NOT NULL DEFAULT now(),
    last_login_at timestamptz,
    metadata_json jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

-- Postgres does not allow expressions in a table-level UNIQUE constraint;
-- enforce case-insensitive per-tenant email uniqueness via a unique index.
CREATE UNIQUE INDEX IF NOT EXISTS idx_local_auth_identities_tenant_email
    ON local_auth_identities (tenant_id, lower(email));

CREATE TABLE IF NOT EXISTS auth_sessions (
    id text PRIMARY KEY,
    tenant_id text NOT NULL REFERENCES techstack_tenants(id) ON DELETE CASCADE,
    user_id text REFERENCES techstack_users(id) ON DELETE CASCADE,
    subject_id text NOT NULL,
    provider_key text NOT NULL,
    session_hash text NOT NULL,
    expires_at timestamptz NOT NULL,
    revoked_at timestamptz,
    metadata_json jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_auth_sessions_subject
    ON auth_sessions (tenant_id, provider_key, subject_id);
CREATE INDEX IF NOT EXISTS idx_auth_sessions_expires
    ON auth_sessions (expires_at) WHERE revoked_at IS NULL;

CREATE TABLE IF NOT EXISTS pairing_tokens (
    id text PRIMARY KEY,
    tenant_id text NOT NULL,
    instance_id text REFERENCES techstack_instances(id) ON DELETE SET NULL,
    stack_id text REFERENCES stacks(id) ON DELETE CASCADE,
    owner_subject_id text NOT NULL,
    name text,
    token_hash text NOT NULL,
    status text NOT NULL DEFAULT 'active',
    expires_at timestamptz,
    used_at timestamptz,
    metadata_json jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, token_hash),
    CHECK (status IN ('active', 'used', 'revoked', 'expired'))
);

CREATE INDEX IF NOT EXISTS idx_pairing_tokens_stack
    ON pairing_tokens (stack_id);

ALTER TABLE workers
    ADD COLUMN IF NOT EXISTS stack_id text REFERENCES stacks(id) ON DELETE SET NULL;

CREATE INDEX IF NOT EXISTS idx_workers_stack
    ON workers (stack_id);

CREATE TABLE IF NOT EXISTS nodes (
    id text PRIMARY KEY,
    tenant_id text NOT NULL,
    instance_id text REFERENCES techstack_instances(id) ON DELETE SET NULL,
    stack_id text NOT NULL REFERENCES stacks(id) ON DELETE CASCADE,
    worker_id text REFERENCES workers(id) ON DELETE SET NULL,
    name text NOT NULL,
    role text NOT NULL DEFAULT 'foundation',
    status text NOT NULL DEFAULT 'pending',
    address text,
    metadata_json jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_nodes_stack ON nodes (stack_id);
CREATE INDEX IF NOT EXISTS idx_nodes_worker ON nodes (worker_id);

CREATE TABLE IF NOT EXISTS services (
    id text PRIMARY KEY,
    tenant_id text NOT NULL,
    instance_id text REFERENCES techstack_instances(id) ON DELETE SET NULL,
    stack_id text NOT NULL REFERENCES stacks(id) ON DELETE CASCADE,
    node_id text REFERENCES nodes(id) ON DELETE SET NULL,
    service_key text NOT NULL,
    name text NOT NULL,
    status text NOT NULL DEFAULT 'observed',
    source text NOT NULL DEFAULT 'observed',
    url text,
    migration_status text,
    metadata_json jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_services_stack ON services (stack_id);
CREATE INDEX IF NOT EXISTS idx_services_node ON services (node_id);
CREATE INDEX IF NOT EXISTS idx_services_source ON services (source);

CREATE TABLE IF NOT EXISTS activity_log (
    id text PRIMARY KEY,
    tenant_id text NOT NULL,
    instance_id text REFERENCES techstack_instances(id) ON DELETE SET NULL,
    stack_id text REFERENCES stacks(id) ON DELETE SET NULL,
    actor_subject_id text,
    action text NOT NULL,
    category text NOT NULL DEFAULT 'system',
    severity text NOT NULL DEFAULT 'info',
    message text,
    details_json jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_activity_log_stack_created
    ON activity_log (stack_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_activity_log_tenant_created
    ON activity_log (tenant_id, created_at DESC);

CREATE TABLE IF NOT EXISTS feature_consents (
    id text PRIMARY KEY,
    tenant_id text NOT NULL,
    subject_id text NOT NULL,
    feature_key text NOT NULL,
    consented_at timestamptz NOT NULL DEFAULT now(),
    revoked_at timestamptz,
    ip_address text,
    user_agent text,
    metadata_json jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, subject_id, feature_key, consented_at)
);

CREATE INDEX IF NOT EXISTS idx_feature_consents_active
    ON feature_consents (tenant_id, subject_id, feature_key)
    WHERE revoked_at IS NULL;

CREATE TABLE IF NOT EXISTS feature_audit (
    id bigserial PRIMARY KEY,
    tenant_id text NOT NULL,
    subject_id text,
    feature_key text NOT NULL,
    action text NOT NULL,
    details_json jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS auth_config (
    id text PRIMARY KEY,
    tenant_id text NOT NULL,
    instance_id text REFERENCES techstack_instances(id) ON DELETE SET NULL,
    mode text NOT NULL DEFAULT 'local',
    config_json jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, instance_id)
);

CREATE TABLE IF NOT EXISTS breakglass_admin (
    id text PRIMARY KEY,
    tenant_id text NOT NULL,
    user_id text REFERENCES techstack_users(id) ON DELETE SET NULL,
    email text NOT NULL,
    password_hash text NOT NULL,
    locked boolean NOT NULL DEFAULT false,
    last_used_at timestamptz,
    metadata_json jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (tenant_id)
);

CREATE TABLE IF NOT EXISTS ril_servers (
    id text PRIMARY KEY,
    tenant_id text NOT NULL,
    instance_id text REFERENCES techstack_instances(id) ON DELETE SET NULL,
    stack_id text REFERENCES stacks(id) ON DELETE SET NULL,
    node_id text REFERENCES nodes(id) ON DELETE SET NULL,
    name text NOT NULL,
    status text NOT NULL DEFAULT 'unknown',
    health_json jsonb NOT NULL DEFAULT '{}'::jsonb,
    inventory_json jsonb NOT NULL DEFAULT '{}'::jsonb,
    last_seen_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_ril_servers_stack ON ril_servers (stack_id);

CREATE TABLE IF NOT EXISTS ril_commands (
    id text PRIMARY KEY,
    tenant_id text NOT NULL,
    server_id text REFERENCES ril_servers(id) ON DELETE CASCADE,
    actor_subject_id text,
    command_class text NOT NULL,
    status text NOT NULL DEFAULT 'queued',
    request_json jsonb NOT NULL DEFAULT '{}'::jsonb,
    result_json jsonb NOT NULL DEFAULT '{}'::jsonb,
    error text,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    completed_at timestamptz
);

CREATE INDEX IF NOT EXISTS idx_ril_commands_server_status
    ON ril_commands (server_id, status);

CREATE TABLE IF NOT EXISTS ril_heal_events (
    id text PRIMARY KEY,
    tenant_id text NOT NULL,
    server_id text REFERENCES ril_servers(id) ON DELETE SET NULL,
    action_card_id text,
    status text NOT NULL,
    cause text,
    details_json jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS ril_action_cards (
    id text PRIMARY KEY,
    tenant_id text NOT NULL,
    server_id text REFERENCES ril_servers(id) ON DELETE SET NULL,
    stack_id text REFERENCES stacks(id) ON DELETE SET NULL,
    title text NOT NULL,
    status text NOT NULL DEFAULT 'open',
    severity text NOT NULL DEFAULT 'info',
    action_json jsonb NOT NULL DEFAULT '{}'::jsonb,
    decision_json jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    resolved_at timestamptz
);

CREATE INDEX IF NOT EXISTS idx_ril_action_cards_status
    ON ril_action_cards (tenant_id, status, created_at DESC);

CREATE TABLE IF NOT EXISTS pocketbase_migration_runs (
    id text PRIMARY KEY,
    tenant_id text,
    source_path text NOT NULL,
    status text NOT NULL DEFAULT 'pending',
    started_at timestamptz NOT NULL DEFAULT now(),
    completed_at timestamptz,
    report_json jsonb NOT NULL DEFAULT '{}'::jsonb,
    error text,
    CHECK (status IN ('pending', 'running', 'completed', 'failed'))
);

CREATE TABLE IF NOT EXISTS pocketbase_migration_counts (
    run_id text NOT NULL REFERENCES pocketbase_migration_runs(id) ON DELETE CASCADE,
    collection_name text NOT NULL,
    source_count integer NOT NULL DEFAULT 0,
    imported_count integer NOT NULL DEFAULT 0,
    skipped_count integer NOT NULL DEFAULT 0,
    mismatch_count integer NOT NULL DEFAULT 0,
    details_json jsonb NOT NULL DEFAULT '{}'::jsonb,
    PRIMARY KEY (run_id, collection_name)
);

DO $$
DECLARE
    t text;
    tables text[] := ARRAY[
        'techstack_roles',
        'techstack_memberships',
        'local_auth_identities',
        'auth_sessions',
        'pairing_tokens',
        'nodes',
        'services',
        'activity_log',
        'feature_consents',
        'feature_audit',
        'auth_config',
        'breakglass_admin',
        'ril_servers',
        'ril_commands',
        'ril_heal_events',
        'ril_action_cards'
    ];
BEGIN
    ALTER TABLE techstack_tenants ENABLE ROW LEVEL SECURITY;
    ALTER TABLE techstack_tenants FORCE ROW LEVEL SECURITY;
    DROP POLICY IF EXISTS tenant_isolation ON techstack_tenants;
    CREATE POLICY tenant_isolation ON techstack_tenants
        USING (id = current_setting('app.tenant_id', true))
        WITH CHECK (id = current_setting('app.tenant_id', true));

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

DO $$
DECLARE
    t text;
    tables text[] := ARRAY[
        'techstack_tenants',
        'techstack_users',
        'techstack_roles',
        'techstack_memberships',
        'local_auth_identities',
        'auth_sessions',
        'pairing_tokens',
        'nodes',
        'services',
        'feature_consents',
        'auth_config',
        'breakglass_admin',
        'ril_servers',
        'ril_commands',
        'ril_heal_events',
        'ril_action_cards'
    ];
BEGIN
    FOREACH t IN ARRAY tables LOOP
        EXECUTE format('DROP TRIGGER IF EXISTS set_%s_updated_at ON %I', t, t);
        EXECUTE format(
            'CREATE TRIGGER set_%s_updated_at BEFORE UPDATE ON %I '
            || 'FOR EACH ROW EXECUTE FUNCTION set_updated_at()',
            t,
            t
        );
    END LOOP;
END $$;
