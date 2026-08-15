-- Provider-neutral host listener allocation and runtime evidence.
--
-- StackKits declares node-realized listeners. Techstack reserves those
-- listeners against one exact server generation before runtime mutation.
-- Observed/exposed facts are evidence only and never grant a reservation.

CREATE UNIQUE INDEX IF NOT EXISTS uq_servers_tenant_identity
    ON servers (tenant_id, id);

CREATE TABLE IF NOT EXISTS server_port_reservations (
    id text PRIMARY KEY,
    tenant_id text NOT NULL REFERENCES techstack_tenants(id) ON DELETE CASCADE,
    server_id text NOT NULL,
    server_generation bigint NOT NULL CHECK (server_generation > 0),
    transport text NOT NULL CHECK (transport IN ('tcp', 'udp')),
    bind_address text NOT NULL CHECK (length(bind_address) BETWEEN 1 AND 64),
    port integer NOT NULL CHECK (port BETWEEN 1 AND 65535),
    sharing text NOT NULL CHECK (sharing IN ('exclusive', 'virtual-host')),
    listener_group_ref text NOT NULL DEFAULT '',
    state text NOT NULL DEFAULT 'reserved' CHECK (state IN ('reserved', 'released')),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    released_at timestamptz,
    UNIQUE (tenant_id, id, server_id, server_generation),
    FOREIGN KEY (tenant_id, server_id)
        REFERENCES servers (tenant_id, id) ON DELETE CASCADE,
    CHECK (
        (sharing = 'exclusive' AND listener_group_ref = '')
        OR
        (sharing = 'virtual-host' AND length(listener_group_ref) BETWEEN 1 AND 256)
    ),
    CHECK (
        (state = 'reserved' AND released_at IS NULL)
        OR
        (state = 'released' AND released_at IS NOT NULL)
    )
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_server_port_reservation_socket
    ON server_port_reservations (
        tenant_id,
        server_id,
        server_generation,
        transport,
        bind_address,
        port,
        sharing,
        listener_group_ref
    );

CREATE INDEX IF NOT EXISTS idx_server_port_reservations_inventory
    ON server_port_reservations (
        tenant_id,
        server_id,
        server_generation,
        state,
        transport,
        port
    );

CREATE TABLE IF NOT EXISTS server_port_claim_generations (
    tenant_id text NOT NULL REFERENCES techstack_tenants(id) ON DELETE CASCADE,
    server_id text NOT NULL,
    server_generation bigint NOT NULL CHECK (server_generation > 0),
    stack_id text NOT NULL,
    resolved_plan_hash text NOT NULL,
    claim_set_digest text NOT NULL,
    state text NOT NULL DEFAULT 'pending',
    mutation_started_at timestamptz,
    activated_at timestamptz,
    uncertain_at timestamptz,
    released_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, server_id, server_generation, stack_id, resolved_plan_hash),
    FOREIGN KEY (tenant_id, server_id)
        REFERENCES servers (tenant_id, id) ON DELETE CASCADE,
    CHECK (length(stack_id) BETWEEN 1 AND 256),
    CHECK (length(resolved_plan_hash) BETWEEN 1 AND 256),
    CHECK (claim_set_digest ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (state IN ('pending', 'mutating', 'active', 'uncertain', 'released')),
    CHECK (state <> 'pending' OR (mutation_started_at IS NULL AND activated_at IS NULL AND uncertain_at IS NULL AND released_at IS NULL)),
    CHECK (state <> 'mutating' OR (mutation_started_at IS NOT NULL AND activated_at IS NULL AND uncertain_at IS NULL AND released_at IS NULL)),
    CHECK (state <> 'active' OR (mutation_started_at IS NOT NULL AND activated_at IS NOT NULL AND uncertain_at IS NULL AND released_at IS NULL)),
    CHECK (state <> 'uncertain' OR (mutation_started_at IS NOT NULL AND uncertain_at IS NOT NULL AND released_at IS NULL)),
    CHECK (state <> 'released' OR released_at IS NOT NULL)
);

CREATE INDEX IF NOT EXISTS idx_server_port_claim_generations_live
    ON server_port_claim_generations (
        tenant_id,
        server_id,
        server_generation,
        stack_id,
        state
    );

CREATE TABLE IF NOT EXISTS server_port_reservation_claims (
    id text PRIMARY KEY,
    tenant_id text NOT NULL REFERENCES techstack_tenants(id) ON DELETE CASCADE,
    reservation_id text NOT NULL,
    server_id text NOT NULL,
    server_generation bigint NOT NULL CHECK (server_generation > 0),
    stack_id text NOT NULL,
    resolved_plan_hash text NOT NULL,
    requirement_id text NOT NULL,
    node_ref text NOT NULL DEFAULT '',
    exposure text NOT NULL CHECK (exposure IN ('loopback', 'private', 'public')),
    source_route_refs_json jsonb NOT NULL DEFAULT '[]'::jsonb,
    claim_digest text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, server_id, server_generation, stack_id, resolved_plan_hash, requirement_id),
    FOREIGN KEY (tenant_id, reservation_id, server_id, server_generation)
        REFERENCES server_port_reservations (tenant_id, id, server_id, server_generation),
    FOREIGN KEY (tenant_id, server_id, server_generation, stack_id, resolved_plan_hash)
        REFERENCES server_port_claim_generations (
            tenant_id, server_id, server_generation, stack_id, resolved_plan_hash
        ),
    CHECK (length(stack_id) BETWEEN 1 AND 256),
    CHECK (length(resolved_plan_hash) BETWEEN 1 AND 256),
    CHECK (length(requirement_id) BETWEEN 1 AND 256),
    CHECK (jsonb_typeof(source_route_refs_json) = 'array'),
    CHECK (claim_digest ~ '^sha256:[0-9a-f]{64}$')
);

CREATE INDEX IF NOT EXISTS idx_server_port_claims_live_generation
    ON server_port_reservation_claims (
        tenant_id,
        server_id,
        server_generation,
        stack_id,
        resolved_plan_hash
    );

CREATE TABLE IF NOT EXISTS server_port_runtime_observations (
    id bigserial PRIMARY KEY,
    tenant_id text NOT NULL REFERENCES techstack_tenants(id) ON DELETE CASCADE,
    server_id text NOT NULL,
    server_generation bigint NOT NULL CHECK (server_generation > 0),
    source_epoch text NOT NULL,
    source_sequence bigint NOT NULL CHECK (source_sequence > 0),
    inventory_revision bigint NOT NULL CHECK (inventory_revision > 0),
    listeners_complete boolean NOT NULL,
    exposures_complete boolean NOT NULL,
    observed_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL,
    evidence_json jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, id, server_id, server_generation),
    UNIQUE (tenant_id, server_id, server_generation, source_epoch, source_sequence),
    FOREIGN KEY (tenant_id, server_id)
        REFERENCES servers (tenant_id, id) ON DELETE CASCADE,
    CHECK (length(source_epoch) BETWEEN 1 AND 256),
    CHECK (expires_at > observed_at),
    CHECK (jsonb_typeof(evidence_json) = 'object')
);

CREATE INDEX IF NOT EXISTS idx_server_port_runtime_observations_latest
    ON server_port_runtime_observations (
        tenant_id,
        server_id,
        server_generation,
        inventory_revision DESC,
        source_sequence DESC
    );

CREATE TABLE IF NOT EXISTS server_port_runtime_facts (
    id bigserial PRIMARY KEY,
    tenant_id text NOT NULL REFERENCES techstack_tenants(id) ON DELETE CASCADE,
    observation_id bigint NOT NULL,
    server_id text NOT NULL,
    server_generation bigint NOT NULL CHECK (server_generation > 0),
    fact_kind text NOT NULL CHECK (fact_kind IN ('observed', 'exposed')),
    transport text NOT NULL CHECK (transport IN ('tcp', 'udp')),
    bind_address text NOT NULL CHECK (length(bind_address) BETWEEN 1 AND 64),
    port integer NOT NULL CHECK (port BETWEEN 1 AND 65535),
    exposure text NOT NULL CHECK (exposure IN ('loopback', 'private', 'public')),
    evidence_json jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY (tenant_id, observation_id, server_id, server_generation)
        REFERENCES server_port_runtime_observations (
            tenant_id, id, server_id, server_generation
        ) ON DELETE CASCADE,
    UNIQUE (tenant_id, observation_id, fact_kind, transport, bind_address, port, exposure),
    CHECK (jsonb_typeof(evidence_json) = 'object')
);

CREATE INDEX IF NOT EXISTS idx_server_port_runtime_facts_inventory
    ON server_port_runtime_facts (
        tenant_id,
        server_id,
        server_generation,
        fact_kind,
        transport,
        port
    );

ALTER TABLE server_port_reservations ENABLE ROW LEVEL SECURITY;
ALTER TABLE server_port_reservations FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation ON server_port_reservations;
CREATE POLICY tenant_isolation ON server_port_reservations
    USING (tenant_id = current_setting('app.tenant_id', true))
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true));

ALTER TABLE server_port_reservation_claims ENABLE ROW LEVEL SECURITY;
ALTER TABLE server_port_reservation_claims FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation ON server_port_reservation_claims;
CREATE POLICY tenant_isolation ON server_port_reservation_claims
    USING (tenant_id = current_setting('app.tenant_id', true))
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true));

ALTER TABLE server_port_claim_generations ENABLE ROW LEVEL SECURITY;
ALTER TABLE server_port_claim_generations FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation ON server_port_claim_generations;
CREATE POLICY tenant_isolation ON server_port_claim_generations
    USING (tenant_id = current_setting('app.tenant_id', true))
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true));

ALTER TABLE server_port_runtime_observations ENABLE ROW LEVEL SECURITY;
ALTER TABLE server_port_runtime_observations FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation ON server_port_runtime_observations;
CREATE POLICY tenant_isolation ON server_port_runtime_observations
    USING (tenant_id = current_setting('app.tenant_id', true))
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true));

ALTER TABLE server_port_runtime_facts ENABLE ROW LEVEL SECURITY;
ALTER TABLE server_port_runtime_facts FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation ON server_port_runtime_facts;
CREATE POLICY tenant_isolation ON server_port_runtime_facts
    USING (tenant_id = current_setting('app.tenant_id', true))
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true));
