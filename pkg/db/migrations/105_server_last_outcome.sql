-- Durable, user-facing server result. The aggregate revision is the ordering
-- authority; stale events are rejected before this projection can change.
ALTER TABLE servers
    ADD COLUMN IF NOT EXISTS last_outcome_json jsonb,
    ADD COLUMN IF NOT EXISTS outcome_changed_at timestamptz;

ALTER TABLE servers
    DROP CONSTRAINT IF EXISTS servers_last_outcome_shape;
ALTER TABLE servers
    ADD CONSTRAINT servers_last_outcome_shape CHECK (
        (last_outcome_json IS NULL AND outcome_changed_at IS NULL)
        OR (
            jsonb_typeof(last_outcome_json) = 'object'
            AND last_outcome_json ? 'status'
            AND last_outcome_json ? 'retryable'
            AND last_outcome_json ? 'occurred_at'
            AND outcome_changed_at IS NOT NULL
        )
    );

ALTER TABLE server_state_transitions
    DROP CONSTRAINT IF EXISTS server_state_transitions_dimension_check;
ALTER TABLE server_state_transitions
    ADD CONSTRAINT server_state_transitions_dimension_check
    CHECK (dimension IN ('lifecycle', 'desired', 'connection', 'health', 'outcome'));
