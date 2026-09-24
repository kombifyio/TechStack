-- Reuse the durable authenticated Guard rendezvous. Provider operations retain
-- their admission/lease/claim authority; a command family is not new authority.
ALTER TABLE typed_agent_commands ADD COLUMN IF NOT EXISTS command_family text NOT NULL DEFAULT 'stackkit';
ALTER TABLE typed_agent_commands ADD CONSTRAINT typed_agent_commands_family_check
    CHECK (command_family IN ('stackkit', 'provider'));
CREATE INDEX IF NOT EXISTS typed_agent_commands_family_queue_idx
    ON typed_agent_commands (tenant_id, agent_id, command_family, state, created_at);
