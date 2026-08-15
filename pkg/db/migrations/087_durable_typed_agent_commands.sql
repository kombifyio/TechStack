CREATE TABLE IF NOT EXISTS typed_agent_commands (
    command_id text PRIMARY KEY,
    tenant_id text NOT NULL,
    agent_id text NOT NULL,
    command_json jsonb NOT NULL,
    result_json jsonb,
    state text NOT NULL DEFAULT 'queued'
        CHECK (state IN ('queued', 'dispatched', 'completed', 'failed')),
    error text,
    created_at timestamptz NOT NULL DEFAULT now(),
    dispatched_at timestamptz,
    completed_at timestamptz,
    expires_at timestamptz NOT NULL
);

CREATE INDEX IF NOT EXISTS typed_agent_commands_agent_queue_idx
    ON typed_agent_commands (tenant_id, agent_id, state, created_at);

ALTER TABLE typed_agent_commands ENABLE ROW LEVEL SECURITY;
ALTER TABLE typed_agent_commands FORCE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS typed_agent_commands_tenant_isolation ON typed_agent_commands;
CREATE POLICY typed_agent_commands_tenant_isolation ON typed_agent_commands
    USING (tenant_id = current_setting('app.tenant_id', true))
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true));
