-- kombifyTechstack PostgreSQL state scaffold
-- Migration: 001_initial_schema
--
-- Authority rules:
-- - The central database repository owns the authoritative Prisma schema.
-- - This file is a local sqlc/implementation draft for the TechStack state model.
-- - Do not create a SaaS user directory here.
-- - Do not create a central Homelab member directory here.
-- - User references are external subject ids from Auth0, Pocket ID, or a future OIDC provider.

CREATE TABLE IF NOT EXISTS techstack_instances (
    id text PRIMARY KEY,
    tenant_id text NOT NULL,
    instance_key text NOT NULL,
    display_name text,
    public_url text,
    status text NOT NULL DEFAULT 'active',
    metadata_json jsonb NOT NULL DEFAULT '{}'::jsonb,
    last_seen_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, instance_key)
);

CREATE INDEX IF NOT EXISTS idx_techstack_instances_tenant ON techstack_instances (tenant_id);
CREATE INDEX IF NOT EXISTS idx_techstack_instances_status ON techstack_instances (status);

CREATE TABLE IF NOT EXISTS identity_providers (
    provider_key text PRIMARY KEY,
    provider_type text NOT NULL,
    display_name text NOT NULL,
    issuer_url text,
    client_id text,
    audience text,
    admin_group text,
    enabled boolean NOT NULL DEFAULT true,
    config_json jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CHECK (provider_type IN ('auth0', 'pocket_id', 'pocketbase', 'generic_oidc', 'api_key'))
);

CREATE INDEX IF NOT EXISTS idx_identity_providers_enabled ON identity_providers (enabled);
CREATE INDEX IF NOT EXISTS idx_identity_providers_type ON identity_providers (provider_type);

CREATE TABLE IF NOT EXISTS identity_subject_links (
    id text PRIMARY KEY,
    tenant_id text NOT NULL,
    instance_id text REFERENCES techstack_instances(id) ON DELETE CASCADE,
    provider_key text NOT NULL REFERENCES identity_providers(provider_key) ON DELETE RESTRICT,
    subject_id text NOT NULL,
    subject_email text,
    subject_display_name text,
    local_role text NOT NULL DEFAULT 'operator',
    is_admin boolean NOT NULL DEFAULT false,
    metadata_json jsonb NOT NULL DEFAULT '{}'::jsonb,
    last_login_at timestamptz,
    linked_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, provider_key, subject_id)
);

CREATE INDEX IF NOT EXISTS idx_identity_subject_links_tenant ON identity_subject_links (tenant_id);
CREATE INDEX IF NOT EXISTS idx_identity_subject_links_instance ON identity_subject_links (instance_id);
CREATE INDEX IF NOT EXISTS idx_identity_subject_links_subject ON identity_subject_links (provider_key, subject_id);
CREATE INDEX IF NOT EXISTS idx_identity_subject_links_email ON identity_subject_links (lower(subject_email));

CREATE TABLE IF NOT EXISTS stacks (
    id text PRIMARY KEY,
    tenant_id text NOT NULL,
    instance_id text REFERENCES techstack_instances(id) ON DELETE SET NULL,
    owner_subject_id text,
    name text NOT NULL,
    description text,
    mode text NOT NULL DEFAULT 'easy',
    status text NOT NULL DEFAULT 'draft',
    config_json jsonb NOT NULL DEFAULT '{}'::jsonb,
    services_json jsonb NOT NULL DEFAULT '[]'::jsonb,
    runtime_summary_json jsonb NOT NULL DEFAULT '{}'::jsonb,
    drift_status text,
    drift_checked_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    deleted_at timestamptz
);

CREATE INDEX IF NOT EXISTS idx_stacks_tenant ON stacks (tenant_id);
CREATE INDEX IF NOT EXISTS idx_stacks_instance ON stacks (instance_id);
CREATE INDEX IF NOT EXISTS idx_stacks_owner_subject ON stacks (owner_subject_id);
CREATE INDEX IF NOT EXISTS idx_stacks_status ON stacks (status);
CREATE UNIQUE INDEX IF NOT EXISTS idx_stacks_tenant_name_active ON stacks (tenant_id, lower(name)) WHERE deleted_at IS NULL;

CREATE TABLE IF NOT EXISTS jobs (
    id text PRIMARY KEY,
    tenant_id text NOT NULL,
    instance_id text REFERENCES techstack_instances(id) ON DELETE SET NULL,
    stack_id text REFERENCES stacks(id) ON DELETE CASCADE,
    type text NOT NULL,
    state text NOT NULL DEFAULT 'pending',
    priority integer NOT NULL DEFAULT 0,
    progress integer NOT NULL DEFAULT 0 CHECK (progress >= 0 AND progress <= 100),
    step text,
    message text,
    error text,
    error_details text,
    logs_json jsonb NOT NULL DEFAULT '[]'::jsonb,
    result_json jsonb NOT NULL DEFAULT '{}'::jsonb,
    scheduled_for timestamptz NOT NULL DEFAULT now(),
    started_at timestamptz,
    completed_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CHECK (type IN ('provision', 'destroy', 'update', 'restart', 'deploy', 'drift_check', 'drift_resolve')),
    CHECK (state IN ('pending', 'running', 'completed', 'failed', 'cancelled'))
);

CREATE INDEX IF NOT EXISTS idx_jobs_tenant ON jobs (tenant_id);
CREATE INDEX IF NOT EXISTS idx_jobs_instance ON jobs (instance_id);
CREATE INDEX IF NOT EXISTS idx_jobs_stack ON jobs (stack_id);
CREATE INDEX IF NOT EXISTS idx_jobs_state ON jobs (state);
CREATE INDEX IF NOT EXISTS idx_jobs_pending_schedule ON jobs (scheduled_for, priority DESC) WHERE state = 'pending';

CREATE TABLE IF NOT EXISTS workers (
    id text PRIMARY KEY,
    tenant_id text NOT NULL,
    instance_id text REFERENCES techstack_instances(id) ON DELETE SET NULL,
    hostname text NOT NULL,
    ip text,
    os text,
    arch text,
    token_hash text,
    status text NOT NULL DEFAULT 'pending',
    approved boolean NOT NULL DEFAULT false,
    approved_at timestamptz,
    last_seen_at timestamptz,
    cpu_cores integer NOT NULL DEFAULT 0,
    ram_mb integer NOT NULL DEFAULT 0,
    disk_gb integer NOT NULL DEFAULT 0,
    gpu text,
    has_nvme boolean NOT NULL DEFAULT false,
    has_hw_transcode boolean NOT NULL DEFAULT false,
    docker_version text,
    type text,
    provider text,
    tags_json jsonb NOT NULL DEFAULT '{}'::jsonb,
    owner_subject_id text,
    capabilities_json jsonb NOT NULL DEFAULT '{}'::jsonb,
    resources_json jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_workers_tenant ON workers (tenant_id);
CREATE INDEX IF NOT EXISTS idx_workers_instance ON workers (instance_id);
CREATE INDEX IF NOT EXISTS idx_workers_owner_subject ON workers (owner_subject_id);
CREATE INDEX IF NOT EXISTS idx_workers_status ON workers (status);
CREATE INDEX IF NOT EXISTS idx_workers_token_hash ON workers (token_hash);
CREATE UNIQUE INDEX IF NOT EXISTS idx_workers_token_host ON workers (token_hash, lower(hostname)) WHERE token_hash IS NOT NULL;

CREATE TABLE IF NOT EXISTS agent_commands (
    id text PRIMARY KEY,
    tenant_id text NOT NULL,
    instance_id text REFERENCES techstack_instances(id) ON DELETE SET NULL,
    worker_id text REFERENCES workers(id) ON DELETE CASCADE,
    stack_id text REFERENCES stacks(id) ON DELETE SET NULL,
    type text NOT NULL,
    state text NOT NULL DEFAULT 'queued',
    payload_json jsonb NOT NULL DEFAULT '{}'::jsonb,
    result_json jsonb NOT NULL DEFAULT '{}'::jsonb,
    error text,
    queued_at timestamptz NOT NULL DEFAULT now(),
    claimed_at timestamptz,
    completed_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_agent_commands_worker_state ON agent_commands (worker_id, state);
CREATE INDEX IF NOT EXISTS idx_agent_commands_tenant ON agent_commands (tenant_id);
CREATE INDEX IF NOT EXISTS idx_agent_commands_stack ON agent_commands (stack_id);

CREATE TABLE IF NOT EXISTS drift_results (
    id text PRIMARY KEY,
    tenant_id text NOT NULL,
    instance_id text REFERENCES techstack_instances(id) ON DELETE SET NULL,
    stack_id text REFERENCES stacks(id) ON DELETE CASCADE,
    job_id text REFERENCES jobs(id) ON DELETE SET NULL,
    status text NOT NULL,
    affected_resources jsonb NOT NULL DEFAULT '[]'::jsonb,
    plan_summary jsonb NOT NULL DEFAULT '{}'::jsonb,
    details_json jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_drift_results_stack_created ON drift_results (stack_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_drift_results_status ON drift_results (status);

CREATE TABLE IF NOT EXISTS precheck_results (
    id text PRIMARY KEY,
    tenant_id text NOT NULL,
    instance_id text REFERENCES techstack_instances(id) ON DELETE SET NULL,
    stack_id text REFERENCES stacks(id) ON DELETE CASCADE,
    worker_id text REFERENCES workers(id) ON DELETE SET NULL,
    check_key text NOT NULL,
    status text NOT NULL,
    severity text NOT NULL DEFAULT 'info',
    message text,
    details_json jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_precheck_results_stack ON precheck_results (stack_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_precheck_results_status ON precheck_results (status);

CREATE TABLE IF NOT EXISTS feature_rollouts (
    feature_key text PRIMARY KEY,
    enabled boolean NOT NULL DEFAULT false,
    reason text,
    applied_by text NOT NULL,
    source text NOT NULL DEFAULT 'platform',
    metadata_json jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_feature_rollouts_enabled ON feature_rollouts (enabled);
CREATE INDEX IF NOT EXISTS idx_feature_rollouts_source ON feature_rollouts (source);

CREATE TABLE IF NOT EXISTS feature_preferences (
    id text PRIMARY KEY,
    tenant_id text NOT NULL,
    subject_id text NOT NULL,
    feature_key text NOT NULL,
    enabled boolean NOT NULL DEFAULT false,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, subject_id, feature_key)
);

CREATE INDEX IF NOT EXISTS idx_feature_preferences_subject ON feature_preferences (tenant_id, subject_id);

CREATE TABLE IF NOT EXISTS wallet_items (
    id text PRIMARY KEY,
    tenant_id text NOT NULL,
    instance_id text REFERENCES techstack_instances(id) ON DELETE SET NULL,
    stack_id text REFERENCES stacks(id) ON DELETE SET NULL,
    item_type text NOT NULL,
    provider text,
    external_ref text,
    metadata_json jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_wallet_items_tenant ON wallet_items (tenant_id);
CREATE INDEX IF NOT EXISTS idx_wallet_items_stack ON wallet_items (stack_id);

CREATE TABLE IF NOT EXISTS audit_events (
    id bigserial PRIMARY KEY,
    tenant_id text NOT NULL,
    instance_id text REFERENCES techstack_instances(id) ON DELETE SET NULL,
    actor_subject_id text,
    action text NOT NULL,
    resource_type text NOT NULL,
    resource_id text,
    details_json jsonb NOT NULL DEFAULT '{}'::jsonb,
    ip_address text,
    user_agent text,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_audit_events_tenant_created ON audit_events (tenant_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_audit_events_actor ON audit_events (actor_subject_id);
CREATE INDEX IF NOT EXISTS idx_audit_events_resource ON audit_events (resource_type, resource_id);

CREATE TABLE IF NOT EXISTS tofu_states (
    id text PRIMARY KEY,
    tenant_id text NOT NULL,
    instance_id text REFERENCES techstack_instances(id) ON DELETE SET NULL,
    stack_id text NOT NULL REFERENCES stacks(id) ON DELETE CASCADE,
    workspace text NOT NULL DEFAULT 'default',
    state_json jsonb NOT NULL DEFAULT '{}'::jsonb,
    lock_id text,
    locked_at timestamptz,
    locked_by_subject_id text,
    version integer NOT NULL DEFAULT 1,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (stack_id, workspace)
);

CREATE INDEX IF NOT EXISTS idx_tofu_states_stack ON tofu_states (stack_id);
CREATE INDEX IF NOT EXISTS idx_tofu_states_locked ON tofu_states (lock_id) WHERE lock_id IS NOT NULL;

CREATE OR REPLACE FUNCTION set_updated_at()
RETURNS trigger AS $$
BEGIN
    NEW.updated_at = now();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS set_techstack_instances_updated_at ON techstack_instances;
CREATE TRIGGER set_techstack_instances_updated_at
    BEFORE UPDATE ON techstack_instances
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

DROP TRIGGER IF EXISTS set_identity_providers_updated_at ON identity_providers;
CREATE TRIGGER set_identity_providers_updated_at
    BEFORE UPDATE ON identity_providers
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

DROP TRIGGER IF EXISTS set_identity_subject_links_updated_at ON identity_subject_links;
CREATE TRIGGER set_identity_subject_links_updated_at
    BEFORE UPDATE ON identity_subject_links
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

DROP TRIGGER IF EXISTS set_stacks_updated_at ON stacks;
CREATE TRIGGER set_stacks_updated_at
    BEFORE UPDATE ON stacks
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

DROP TRIGGER IF EXISTS set_jobs_updated_at ON jobs;
CREATE TRIGGER set_jobs_updated_at
    BEFORE UPDATE ON jobs
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

DROP TRIGGER IF EXISTS set_workers_updated_at ON workers;
CREATE TRIGGER set_workers_updated_at
    BEFORE UPDATE ON workers
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

DROP TRIGGER IF EXISTS set_agent_commands_updated_at ON agent_commands;
CREATE TRIGGER set_agent_commands_updated_at
    BEFORE UPDATE ON agent_commands
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

DROP TRIGGER IF EXISTS set_feature_rollouts_updated_at ON feature_rollouts;
CREATE TRIGGER set_feature_rollouts_updated_at
    BEFORE UPDATE ON feature_rollouts
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

DROP TRIGGER IF EXISTS set_feature_preferences_updated_at ON feature_preferences;
CREATE TRIGGER set_feature_preferences_updated_at
    BEFORE UPDATE ON feature_preferences
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

DROP TRIGGER IF EXISTS set_wallet_items_updated_at ON wallet_items;
CREATE TRIGGER set_wallet_items_updated_at
    BEFORE UPDATE ON wallet_items
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

DROP TRIGGER IF EXISTS set_tofu_states_updated_at ON tofu_states;
CREATE TRIGGER set_tofu_states_updated_at
    BEFORE UPDATE ON tofu_states
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();
