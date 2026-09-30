-- Owner-requested server maintenance (OS update plan, OS update, reboot).
--
-- One row per request, keyed by an id derived from tenant, principal and the
-- request's Idempotency-Key (the raw key is never stored). The ledger is kept
-- apart from `jobs`: a node operation has no stack and its own fence.
--
-- Two unique partial indexes are the durable per-server fence: a second
-- active reboot or OS update for the same server fails the insert with a
-- unique violation on every replica, so two concurrent requests can never
-- both reboot or update one node. Update plans hold their own slot, so
-- repeated plans cannot starve a reboot or update.

CREATE TABLE IF NOT EXISTS server_maintenance_jobs (
    tenant_id text NOT NULL,
    id text NOT NULL,
    server_id text NOT NULL,
    agent_id text NOT NULL,
    stack_id text NOT NULL DEFAULT '',
    owner_subject_id text NOT NULL,
    action text NOT NULL,
    request_digest text NOT NULL,
    state text NOT NULL DEFAULT 'queued',
    inventory_revision bigint NOT NULL,
    plan_digest text NOT NULL DEFAULT '',
    reason_code text NOT NULL DEFAULT '',
    result_json jsonb NOT NULL DEFAULT '{}'::jsonb,
    deadline_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    completed_at timestamptz,
    PRIMARY KEY (tenant_id, id),
    CHECK (action IN ('os_update_plan', 'os_update', 'reboot')),
    CHECK (state IN ('queued', 'running', 'waiting', 'awaiting_node_return', 'completed', 'failed', 'cancelled')),
    CHECK (plan_digest = '' OR plan_digest ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (length(reason_code) <= 128)
);

CREATE UNIQUE INDEX IF NOT EXISTS server_maintenance_jobs_one_active_mutation
    ON server_maintenance_jobs (tenant_id, server_id)
    WHERE action <> 'os_update_plan' AND state IN ('queued', 'running', 'waiting', 'awaiting_node_return');

CREATE UNIQUE INDEX IF NOT EXISTS server_maintenance_jobs_one_active_plan
    ON server_maintenance_jobs (tenant_id, server_id)
    WHERE action = 'os_update_plan' AND state IN ('queued', 'running', 'waiting', 'awaiting_node_return');

CREATE INDEX IF NOT EXISTS server_maintenance_jobs_active_agent_idx
    ON server_maintenance_jobs (tenant_id, agent_id)
    WHERE action <> 'os_update_plan' AND state IN ('queued', 'running', 'waiting', 'awaiting_node_return');

CREATE INDEX IF NOT EXISTS server_maintenance_jobs_plan_idx
    ON server_maintenance_jobs (tenant_id, server_id, plan_digest, completed_at DESC)
    WHERE action = 'os_update_plan' AND state = 'completed';

ALTER TABLE server_maintenance_jobs ENABLE ROW LEVEL SECURITY;
ALTER TABLE server_maintenance_jobs FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation ON server_maintenance_jobs;
CREATE POLICY tenant_isolation ON server_maintenance_jobs
    USING (tenant_id = current_setting('app.tenant_id', true))
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true));
