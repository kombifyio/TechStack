-- Recommendation/v1 records on the RIL action-card authority (Beads
-- kombify-Techstack-xu7g, workspace epic platform-wig6t.1). A layout
-- recommendation is a card of kind 'layout-recommendation': the same tenant
-- RLS, owner scope and append-only transition audit as governed actions,
-- with its own lifecycle (open, accepted, rejected, snoozed, expired,
-- superseded, executing, done, failed). Nothing runs before the owner
-- accepts it (owner decision D2, 2026-10-19).

ALTER TABLE ril_action_cards
    ADD COLUMN IF NOT EXISTS card_kind text NOT NULL DEFAULT 'governed-action',
    ADD COLUMN IF NOT EXISTS recommendation_scope text,
    ADD COLUMN IF NOT EXISTS recommendation_json jsonb,
    ADD COLUMN IF NOT EXISTS proposal_digest text,
    ADD COLUMN IF NOT EXISTS snapshot_digest text,
    ADD COLUMN IF NOT EXISTS material_digest text,
    ADD COLUMN IF NOT EXISTS expires_at timestamptz,
    ADD COLUMN IF NOT EXISTS snoozed_until timestamptz,
    ADD COLUMN IF NOT EXISTS superseded_by text;

ALTER TABLE ril_action_cards
    DROP CONSTRAINT IF EXISTS ril_action_cards_card_kind_check;
ALTER TABLE ril_action_cards
    ADD CONSTRAINT ril_action_cards_card_kind_check CHECK (
        card_kind IN ('governed-action', 'layout-recommendation')
    ) NOT VALID;

-- Governed actions keep exactly their own states; recommendation states are
-- held by the recommendation shape check below.
ALTER TABLE ril_action_cards
    DROP CONSTRAINT IF EXISTS ril_action_cards_governed_status_check;
ALTER TABLE ril_action_cards
    ADD CONSTRAINT ril_action_cards_governed_status_check CHECK (
        card_kind <> 'governed-action'
        OR status IN (
            'planned', 'awaiting_grant', 'awaiting_approval', 'denied',
            'approved', 'executing', 'verifying', 'completed', 'failed',
            'rollback_required', 'open', 'pending', 'dismissed'
        )
    ) NOT VALID;

-- Execution admission binds governed actions to a server and lease. A layout
-- recommendation executes through the creation flow's own job and carries
-- that job as its execution id instead.
ALTER TABLE ril_action_cards
    DROP CONSTRAINT IF EXISTS ril_action_cards_execution_status_requires_admission;
ALTER TABLE ril_action_cards
    ADD CONSTRAINT ril_action_cards_execution_status_requires_admission CHECK (
        card_kind <> 'governed-action'
        OR status NOT IN ('executing', 'verifying', 'completed', 'failed')
        OR (
            admission_inventory_revision IS NOT NULL
            AND admission_inventory_revision > 0
            AND admission_server_revision IS NOT NULL
            AND admission_server_revision > 0
            AND admission_server_generation IS NOT NULL
            AND admission_server_generation > 0
            AND admission_lease_id IS NOT NULL
            AND NULLIF(BTRIM(admission_lease_id), '') IS NOT NULL
            AND admission_lease_revision IS NOT NULL
            AND admission_lease_revision BETWEEN 1 AND 9007199254740991
            AND admission_resource_generation_id IS NOT NULL
            AND execution_admission_digest IS NOT NULL
            AND execution_admission_digest ~ '^sha256:[a-f0-9]{64}$'
        )
    ) NOT VALID;

ALTER TABLE ril_action_cards
    DROP CONSTRAINT IF EXISTS ril_action_cards_recommendation_shape_check;
ALTER TABLE ril_action_cards
    ADD CONSTRAINT ril_action_cards_recommendation_shape_check CHECK (
        card_kind <> 'layout-recommendation'
        OR (
            status IN ('open', 'accepted', 'rejected', 'snoozed', 'expired', 'superseded', 'executing', 'done', 'failed')
            AND NULLIF(BTRIM(recommendation_scope), '') IS NOT NULL
            AND recommendation_json IS NOT NULL
            AND proposal_digest IS NOT NULL AND proposal_digest ~ '^sha256:[a-f0-9]{64}$'
            AND snapshot_digest IS NOT NULL AND snapshot_digest ~ '^sha256:[a-f0-9]{64}$'
            AND material_digest IS NOT NULL AND material_digest ~ '^sha256:[a-f0-9]{64}$'
            AND expires_at IS NOT NULL
            AND (status <> 'snoozed' OR snoozed_until IS NOT NULL)
            AND (status <> 'superseded' OR NULLIF(BTRIM(superseded_by), '') IS NOT NULL)
            AND (status NOT IN ('executing', 'done', 'failed') OR NULLIF(BTRIM(execution_id), '') IS NOT NULL)
        )
    ) NOT VALID;

-- The same advice is live at most once per household scope.
CREATE UNIQUE INDEX IF NOT EXISTS idx_ril_recommendations_live_proposal
    ON ril_action_cards (tenant_id, recommendation_scope, proposal_digest)
    WHERE card_kind = 'layout-recommendation' AND status IN ('open', 'snoozed');

CREATE INDEX IF NOT EXISTS idx_ril_recommendations_scope
    ON ril_action_cards (tenant_id, owner_subject_id, recommendation_scope, created_at DESC)
    WHERE card_kind = 'layout-recommendation';
