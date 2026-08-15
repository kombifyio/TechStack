-- Revisioned desired routing state for an exact stack/server/lease target.
-- This is an overlay on immutable StackKit intent, not observed DNS/TLS state.

CREATE UNIQUE INDEX IF NOT EXISTS idx_stacks_tenant_id_unique
    ON stacks (tenant_id, id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_stacks_routing_identity_unique
    ON stacks (tenant_id, id, owner_subject_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_servers_tenant_id_unique
    ON servers (tenant_id, id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_servers_routing_identity_unique
    ON servers (tenant_id, id, stack_id, owner_subject_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_servers_routing_lease_unique
    ON servers (tenant_id, id, lease_id);

CREATE TABLE IF NOT EXISTS stack_routing_desired (
    tenant_id text NOT NULL,
    stack_id text NOT NULL,
    owner_subject_id text NOT NULL CHECK (owner_subject_id <> ''),
    server_id text NOT NULL,
    -- Managed provider targets carry the exact lease. User-owned/local
    -- targets intentionally keep this NULL rather than inventing a lease.
    lease_id text CHECK (lease_id IS NULL OR lease_id <> ''),
    revision bigint NOT NULL CHECK (revision > 0),
    mode text NOT NULL CHECK (mode = 'custom-domain'),
    domain text NOT NULL CHECK (domain <> ''),
    provenance_json jsonb NOT NULL,
    rollout_status text NOT NULL CHECK (rollout_status IN ('not_requested', 'pending', 'completed', 'failed')),
    rollout_job_id text,
    reason_code text,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, stack_id),
    FOREIGN KEY (tenant_id, stack_id, owner_subject_id)
        REFERENCES stacks (tenant_id, id, owner_subject_id) ON DELETE CASCADE,
    FOREIGN KEY (tenant_id, server_id, stack_id, owner_subject_id)
        REFERENCES servers (tenant_id, id, stack_id, owner_subject_id) ON DELETE RESTRICT,
    -- MATCH SIMPLE intentionally skips this optional relation for a NULL local
    -- lease, but enforces the exact managed lease whenever lease_id is present.
    FOREIGN KEY (tenant_id, server_id, lease_id)
        REFERENCES servers (tenant_id, id, lease_id) ON DELETE RESTRICT
);

CREATE INDEX IF NOT EXISTS idx_stack_routing_desired_target
    ON stack_routing_desired (tenant_id, server_id, lease_id);

-- Durable receipts make public PUT and Cloud retries replay-safe even after a
-- later routing revision has been written.
CREATE TABLE IF NOT EXISTS stack_routing_idempotency (
    tenant_id text NOT NULL,
    stack_id text NOT NULL,
    owner_subject_id text NOT NULL CHECK (owner_subject_id <> ''),
    idempotency_key text NOT NULL CHECK (idempotency_key <> '' AND length(idempotency_key) <= 256),
    request_hash text NOT NULL CHECK (request_hash ~ '^[0-9a-f]{64}$'),
    response_json jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, owner_subject_id, idempotency_key),
    FOREIGN KEY (tenant_id, stack_id, owner_subject_id)
        REFERENCES stacks (tenant_id, id, owner_subject_id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_stack_routing_idempotency_stack
    ON stack_routing_idempotency (tenant_id, stack_id, created_at DESC);

DO $$
DECLARE
    t text;
BEGIN
    FOREACH t IN ARRAY ARRAY['stack_routing_desired', 'stack_routing_idempotency'] LOOP
        EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
        EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
        EXECUTE format('DROP POLICY IF EXISTS tenant_isolation ON %I', t);
        EXECUTE format(
            'CREATE POLICY tenant_isolation ON %I USING (tenant_id = current_setting(''app.tenant_id'', true)) WITH CHECK (tenant_id = current_setting(''app.tenant_id'', true))',
            t
        );
    END LOOP;
END $$;

DROP TRIGGER IF EXISTS set_stack_routing_desired_updated_at ON stack_routing_desired;
CREATE TRIGGER set_stack_routing_desired_updated_at
    BEFORE UPDATE ON stack_routing_desired
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();
