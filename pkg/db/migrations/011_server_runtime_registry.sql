-- Canonical TechStack server runtime registry.
--
-- `workers`, `nodes`, `ril_servers`, and VM leases remain source adapters
-- during the bounded migration. Operator APIs read this aggregate once the
-- adapter has observed a real lifecycle or connection event.

CREATE TABLE IF NOT EXISTS servers (
    id text PRIMARY KEY,
    tenant_id text NOT NULL REFERENCES techstack_tenants(id) ON DELETE CASCADE,
    instance_id text REFERENCES techstack_instances(id) ON DELETE SET NULL,
    stack_id text REFERENCES stacks(id) ON DELETE SET NULL,
    owner_subject_id text NOT NULL,
    worker_id text REFERENCES workers(id) ON DELETE SET NULL,
    node_id text REFERENCES nodes(id) ON DELETE SET NULL,
    lease_id text,
    provider_ref text,
    name text NOT NULL,
    lifecycle_state text NOT NULL DEFAULT 'planned',
    desired_state text NOT NULL DEFAULT 'active',
    connection_state text NOT NULL DEFAULT 'pending',
    health_state text NOT NULL DEFAULT 'unknown',
    reason_code text,
    connection_changed_at timestamptz NOT NULL DEFAULT now(),
    last_heartbeat_at timestamptz,
    inventory_revision bigint NOT NULL DEFAULT 0,
    channels_json jsonb NOT NULL DEFAULT '[]'::jsonb,
    metadata_json jsonb NOT NULL DEFAULT '{}'::jsonb,
    decommissioned_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CHECK (lifecycle_state IN ('planned', 'provisioning', 'enrolling', 'active', 'failed', 'decommissioning', 'decommissioned')),
    CHECK (desired_state IN ('active', 'decommissioned')),
    CHECK (connection_state IN ('pending', 'connecting', 'connected', 'degraded', 'stale', 'offline', 'revoked')),
    CHECK (health_state IN ('unknown', 'healthy', 'degraded', 'unhealthy')),
    CHECK (inventory_revision >= 0)
);

CREATE INDEX IF NOT EXISTS idx_servers_tenant_stack ON servers (tenant_id, stack_id);
CREATE INDEX IF NOT EXISTS idx_servers_tenant_owner ON servers (tenant_id, owner_subject_id);
CREATE INDEX IF NOT EXISTS idx_servers_tenant_worker ON servers (tenant_id, worker_id);
CREATE INDEX IF NOT EXISTS idx_servers_tenant_lease ON servers (tenant_id, lease_id);
CREATE INDEX IF NOT EXISTS idx_servers_connection ON servers (tenant_id, connection_state, last_heartbeat_at DESC);

CREATE TABLE IF NOT EXISTS server_state_transitions (
    id bigserial PRIMARY KEY,
    tenant_id text NOT NULL REFERENCES techstack_tenants(id) ON DELETE CASCADE,
    server_id text NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
    dimension text NOT NULL,
    from_state text,
    to_state text NOT NULL,
    reason_code text,
    source text NOT NULL,
    observed_at timestamptz NOT NULL,
    evidence_json jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now(),
    CHECK (dimension IN ('lifecycle', 'connection', 'health'))
);

CREATE INDEX IF NOT EXISTS idx_server_state_transitions_timeline
    ON server_state_transitions (tenant_id, server_id, observed_at DESC, id DESC);

CREATE TABLE IF NOT EXISTS server_inventory_snapshots (
    id bigserial PRIMARY KEY,
    tenant_id text NOT NULL REFERENCES techstack_tenants(id) ON DELETE CASCADE,
    server_id text NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
    revision bigint NOT NULL,
    source text NOT NULL,
    observed_at timestamptz NOT NULL,
    inventory_json jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, server_id, revision),
    CHECK (revision > 0)
);

CREATE INDEX IF NOT EXISTS idx_server_inventory_snapshots_latest
    ON server_inventory_snapshots (tenant_id, server_id, revision DESC);

ALTER TABLE services ADD COLUMN IF NOT EXISTS server_id text REFERENCES servers(id) ON DELETE SET NULL;
ALTER TABLE services ADD COLUMN IF NOT EXISTS service_instance text NOT NULL DEFAULT 'default';
ALTER TABLE services ADD COLUMN IF NOT EXISTS desired_state text NOT NULL DEFAULT 'running';
ALTER TABLE services ADD COLUMN IF NOT EXISTS observed_state text NOT NULL DEFAULT 'unknown';
ALTER TABLE services ADD COLUMN IF NOT EXISTS health_state text NOT NULL DEFAULT 'unknown';
ALTER TABLE services ADD COLUMN IF NOT EXISTS observed_at timestamptz;
ALTER TABLE services ADD COLUMN IF NOT EXISTS stackkit_version text;
ALTER TABLE services ADD COLUMN IF NOT EXISTS access_json jsonb NOT NULL DEFAULT '{"mode":"unavailable"}'::jsonb;
ALTER TABLE services ADD COLUMN IF NOT EXISTS capabilities_json jsonb NOT NULL DEFAULT '[]'::jsonb;

CREATE UNIQUE INDEX IF NOT EXISTS idx_services_runtime_identity
    ON services (tenant_id, stack_id, server_id, service_key, service_instance)
    WHERE server_id IS NOT NULL;

DO $$
DECLARE
    t text;
BEGIN
    FOREACH t IN ARRAY ARRAY['servers', 'server_state_transitions', 'server_inventory_snapshots'] LOOP
        EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
        EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
        EXECUTE format('DROP POLICY IF EXISTS tenant_isolation ON %I', t);
        EXECUTE format(
            'CREATE POLICY tenant_isolation ON %I USING (tenant_id = current_setting(''app.tenant_id'', true)) WITH CHECK (tenant_id = current_setting(''app.tenant_id'', true))',
            t
        );
    END LOOP;
END $$;

DROP TRIGGER IF EXISTS set_servers_updated_at ON servers;
CREATE TRIGGER set_servers_updated_at
    BEFORE UPDATE ON servers
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();
