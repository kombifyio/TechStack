ALTER TABLE ril_action_cards
    ADD COLUMN IF NOT EXISTS owner_subject_id text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS action_template_json jsonb NOT NULL DEFAULT '{}'::jsonb,
    ADD COLUMN IF NOT EXISTS approval_json jsonb,
    ADD COLUMN IF NOT EXISTS execution_request_json jsonb,
    ADD COLUMN IF NOT EXISTS execution_id text,
    ADD COLUMN IF NOT EXISTS idempotency_key text,
    ADD COLUMN IF NOT EXISTS trace_id text,
    ADD COLUMN IF NOT EXISTS evidence_json jsonb,
    ADD COLUMN IF NOT EXISTS error_code text,
    ADD COLUMN IF NOT EXISTS approved_at timestamptz,
    ADD COLUMN IF NOT EXISTS denied_at timestamptz,
    ADD COLUMN IF NOT EXISTS execution_started_at timestamptz,
    ADD COLUMN IF NOT EXISTS completed_at timestamptz;

-- Canonical Runtime Inventory owns the server identity. The retired RIL
-- inventory table must not remain an action-authority prerequisite.
ALTER TABLE ril_action_cards
    DROP CONSTRAINT IF EXISTS ril_action_cards_server_id_fkey;
ALTER TABLE ril_action_cards
    ADD CONSTRAINT ril_action_cards_server_id_fkey
    FOREIGN KEY (server_id) REFERENCES servers(id) ON DELETE SET NULL NOT VALID;

CREATE UNIQUE INDEX IF NOT EXISTS idx_ril_action_cards_execution_id
    ON ril_action_cards (tenant_id, execution_id)
    WHERE execution_id IS NOT NULL;

CREATE UNIQUE INDEX IF NOT EXISTS idx_ril_action_cards_idempotency
    ON ril_action_cards (tenant_id, idempotency_key)
    WHERE idempotency_key IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_ril_action_cards_owner_status
    ON ril_action_cards (tenant_id, owner_subject_id, status, created_at DESC);

ALTER TABLE ril_action_cards
    DROP CONSTRAINT IF EXISTS ril_action_cards_governed_status_check;
ALTER TABLE ril_action_cards
    ADD CONSTRAINT ril_action_cards_governed_status_check CHECK (
        status IN (
            'planned', 'awaiting_grant', 'awaiting_approval', 'denied',
            'approved', 'executing', 'verifying', 'completed', 'failed',
            'rollback_required', 'open', 'pending', 'dismissed'
        )
    ) NOT VALID;
