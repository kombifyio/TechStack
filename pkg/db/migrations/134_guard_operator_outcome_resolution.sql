-- kombify Guard operator release of an escalated outcome_unknown: a resolution
-- revision may now be committed by an authenticated tenant operator. The
-- revision stays append-only; the actor and the operator's note are recorded
-- with it so the release is auditable.
ALTER TABLE typed_agent_command_revisions
    ADD COLUMN IF NOT EXISTS actor_kind text NOT NULL DEFAULT 'guard',
    ADD COLUMN IF NOT EXISTS actor_id text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS note text NOT NULL DEFAULT '';

ALTER TABLE typed_agent_command_revisions DROP CONSTRAINT IF EXISTS typed_agent_command_revisions_actor_check;
ALTER TABLE typed_agent_command_revisions ADD CONSTRAINT typed_agent_command_revisions_actor_check
    CHECK (actor_kind IN ('guard', 'operator') AND (actor_kind = 'guard' OR actor_id <> ''));

-- The operator list reads the unresolved outcomes of one worker.
CREATE INDEX IF NOT EXISTS typed_agent_commands_outcome_unknown_idx
    ON typed_agent_commands (tenant_id, agent_id, created_at)
    WHERE command_family = 'stackkit' AND state = 'outcome_unknown';
