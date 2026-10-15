-- kombify Guard durable outcomes: a typed command is admitted under a binding
-- (worker generation, payload digest, authority expiry, contract version) and
-- its outcome is reported as immutable, acknowledged revisions. A command
-- dispatched to a Guard that predates the contract keeps the pre-binding
-- at-most-once semantics and is marked contract_version = 'legacy'.
ALTER TABLE typed_agent_commands
    ADD COLUMN IF NOT EXISTS payload_digest text,
    ADD COLUMN IF NOT EXISTS worker_generation bigint,
    ADD COLUMN IF NOT EXISTS result_generation bigint,
    ADD COLUMN IF NOT EXISTS authority_expires_at timestamptz,
    ADD COLUMN IF NOT EXISTS contract_version text,
    ADD COLUMN IF NOT EXISTS mutating boolean NOT NULL DEFAULT false,
    ADD COLUMN IF NOT EXISTS result_revision integer NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS result_digest text,
    ADD COLUMN IF NOT EXISTS last_offered_at timestamptz,
    ADD COLUMN IF NOT EXISTS acked_at timestamptz;

-- outcome_unknown is non-terminal: the effect may or may not have happened and
-- the command keeps fencing conflicting work until a resolution revision is
-- committed.
ALTER TABLE typed_agent_commands DROP CONSTRAINT IF EXISTS typed_agent_commands_state_check;
ALTER TABLE typed_agent_commands ADD CONSTRAINT typed_agent_commands_state_check
    CHECK (state IN ('queued', 'dispatched', 'completed', 'failed', 'outcome_unknown'));

ALTER TABLE typed_agent_commands ADD CONSTRAINT typed_agent_commands_binding_check
    CHECK (result_revision >= 0 AND (payload_digest IS NULL OR payload_digest ~ '^[0-9a-f]{64}$'));

-- The conflict fence reads unresolved mutating commands per worker.
CREATE INDEX IF NOT EXISTS typed_agent_commands_unresolved_idx
    ON typed_agent_commands (tenant_id, agent_id, state)
    WHERE command_family = 'stackkit' AND mutating AND state IN ('dispatched', 'outcome_unknown');

-- Append-only outcome revisions. A second submission of the same revision is
-- accepted only when its digest is identical; a resolution revision references
-- the digest of the evidence it supersedes.
CREATE TABLE IF NOT EXISTS typed_agent_command_revisions (
    command_id text NOT NULL REFERENCES typed_agent_commands (command_id) ON DELETE CASCADE,
    revision integer NOT NULL CHECK (revision >= 1),
    tenant_id text NOT NULL,
    state text NOT NULL CHECK (state IN ('terminal', 'outcome_unknown', 'rejected', 'resolved')),
    digest text NOT NULL CHECK (digest ~ '^[0-9a-f]{64}$'),
    prior_digest text,
    reason text NOT NULL DEFAULT '',
    resolution text NOT NULL DEFAULT '',
    truncated boolean NOT NULL DEFAULT false,
    worker_generation bigint,
    result_json jsonb,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (command_id, revision)
);

ALTER TABLE typed_agent_command_revisions ENABLE ROW LEVEL SECURITY;
ALTER TABLE typed_agent_command_revisions FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS typed_agent_command_revisions_tenant_isolation ON typed_agent_command_revisions;
CREATE POLICY typed_agent_command_revisions_tenant_isolation ON typed_agent_command_revisions
    USING (tenant_id = current_setting('app.tenant_id', true))
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true));
