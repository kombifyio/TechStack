-- 133_guard_reach.sql
-- kombify Guard reach: per-server state of the two registrations a key-bound
-- Guard receives from the control plane, the kombify.me relay binding and the
-- Connect techstack-node, plus the durable cursor of the Gateway revocation
-- log. The row carries no secret: proofs a Guard mints are verified, forwarded
-- and dropped, never stored.
--
-- There is deliberately no foreign key to workers: a retired or deleted worker
-- must keep its row until both registries confirmed the revocation.

CREATE TABLE IF NOT EXISTS guard_reach_states (
    tenant_id text NOT NULL,
    worker_id text NOT NULL,
    server_id text NOT NULL,
    stack_id text NOT NULL DEFAULT '',
    owner_subject_id text NOT NULL,
    display_name text NOT NULL DEFAULT '',
    placement text NOT NULL DEFAULT 'byo',
    connect_tenant_id text NOT NULL DEFAULT '',
    connect_tenant_observed boolean NOT NULL DEFAULT false,
    owner_roles text NOT NULL DEFAULT '',
    desired_state text NOT NULL DEFAULT 'active',
    key_thumbprint text NOT NULL DEFAULT '',
    credential_epoch bigint NOT NULL DEFAULT 0,
    binding_generation bigint NOT NULL DEFAULT 0,
    me_state text NOT NULL DEFAULT 'pending',
    me_generation bigint NOT NULL DEFAULT 0,
    me_address text NOT NULL DEFAULT '',
    me_reason text NOT NULL DEFAULT '',
    me_updated_at timestamptz,
    connect_state text NOT NULL DEFAULT 'pending',
    connect_generation bigint NOT NULL DEFAULT 0,
    connect_node_id text NOT NULL DEFAULT '',
    connect_reason text NOT NULL DEFAULT '',
    connect_updated_at timestamptz,
    connect_retry_at timestamptz,
    relay_state text NOT NULL DEFAULT '',
    relay_reason text NOT NULL DEFAULT '',
    relay_generation bigint NOT NULL DEFAULT 0,
    relay_reported_at timestamptz,
    revoke_reason text NOT NULL DEFAULT '',
    attempts integer NOT NULL DEFAULT 0,
    next_attempt_at timestamptz NOT NULL DEFAULT now(),
    last_error text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, worker_id),
    CHECK (BTRIM(server_id) <> '' AND BTRIM(owner_subject_id) <> ''),
    CHECK (desired_state IN ('active', 'revoked')),
    CHECK (me_state IN ('pending', 'registered', 'failed', 'revoked')),
    CHECK (connect_state IN ('pending', 'registered', 'failed', 'revoked')),
    CHECK (placement IN ('managed', 'byo', 'local')),
    CHECK (binding_generation >= 0 AND me_generation >= 0 AND connect_generation >= 0 AND attempts >= 0)
);

CREATE INDEX IF NOT EXISTS idx_guard_reach_states_due
    ON guard_reach_states (next_attempt_at);

CREATE INDEX IF NOT EXISTS idx_guard_reach_states_connect
    ON guard_reach_states (connect_tenant_id, server_id)
    WHERE connect_tenant_id <> '';

ALTER TABLE guard_reach_states ENABLE ROW LEVEL SECURITY;
ALTER TABLE guard_reach_states FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation ON guard_reach_states;
CREATE POLICY tenant_isolation ON guard_reach_states
    USING (tenant_id = current_setting('app.tenant_id', true))
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true));

DROP TRIGGER IF EXISTS set_guard_reach_states_updated_at ON guard_reach_states;
CREATE TRIGGER set_guard_reach_states_updated_at BEFORE UPDATE ON guard_reach_states
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- The Gateway revocation log is global, so its consumer position is too.
CREATE TABLE IF NOT EXISTS guard_reach_cursors (
    name text PRIMARY KEY,
    position bigint NOT NULL DEFAULT 0 CHECK (position >= 0),
    updated_at timestamptz NOT NULL DEFAULT now()
);

-- The migration schema must be first so the unqualified function DDL below
-- creates the functions there. Keep pg_catalog and pg_temp in the captured
-- search path; a hardcoded `public` breaks non-public migration schemas.
SELECT pg_catalog.set_config(
    'search_path',
    pg_catalog.quote_ident(pg_catalog.current_schema()) || ', pg_catalog, pg_temp',
    true
);

-- The reconciler is control-plane and must see every tenant's due rows, which
-- tenant RLS forbids for a tenant-scoped connection. It lists identity only
-- (never a proof or credential) through this SECURITY DEFINER function,
-- keyset-paginated so a pass is bounded, and then works each row inside an
-- exact tenant scope.
CREATE OR REPLACE FUNCTION techstack_list_due_guard_reach(
    p_now timestamptz,
    p_after_tenant text,
    p_after_worker text,
    p_limit integer
)
RETURNS TABLE (tenant_id text, worker_id text)
LANGUAGE sql
SECURITY DEFINER
SET search_path FROM CURRENT
AS $$
    SELECT s.tenant_id, s.worker_id
    FROM guard_reach_states s
    WHERE s.next_attempt_at <= p_now
      AND (s.tenant_id, s.worker_id) > (COALESCE(p_after_tenant, ''), COALESCE(p_after_worker, ''))
    ORDER BY s.tenant_id, s.worker_id
    LIMIT LEAST(GREATEST(COALESCE(p_limit, 50), 1), 200);
$$;

-- A Gateway revocation names the Connect tenant and server, not the Techstack
-- tenant, so the consumer resolves the row through this function.
CREATE OR REPLACE FUNCTION techstack_find_guard_reach_by_connect(
    p_connect_tenant text,
    p_server text
)
RETURNS TABLE (tenant_id text, worker_id text)
LANGUAGE sql
SECURITY DEFINER
SET search_path FROM CURRENT
AS $$
    SELECT s.tenant_id, s.worker_id
    FROM guard_reach_states s
    WHERE s.connect_tenant_id = p_connect_tenant AND s.server_id = p_server
    ORDER BY s.updated_at DESC
    LIMIT 8;
$$;
