-- Service mutation lock: an owner-declared guardrail that refuses mutating
-- service operations, persisted as its own dimension instead of being folded
-- into the measured state.
--
-- Migration 073 separated desired/observed/health and 074 added management.
-- The card family exposed a Lock affordance with no backend behind it, and the
-- obvious shortcut - a `frozen` value in the observed vocabulary - would
-- collapse two orthogonal facts: a locked service keeps running, and its
-- measured state must keep reporting that truthfully. So the lock is a fifth
-- dimension of the same aggregate:
--
--   mutation_lock_state - may the control plane mutate this service?
--     unlocked : governed mutations (start/stop/restart) are admissible.
--     locked   : the control plane refuses every mutating operation
--                fail-closed, including the stack-scoped apply and
--                drift_reconcile paths. Read operations (logs) stay allowed.
--
-- The lock is control-plane authority only. A Guard (agent) event reports what
-- it measures and can never assert or clear an owner guardrail; the write
-- boundary enforces that, this CHECK only closes the vocabulary.
--
-- reason_code and actor exist for display: the detail surface has to be able to
-- say why a service is locked and who locked it without replaying the timeline.
-- They are bounded and carry no secret.
ALTER TABLE services
    ADD COLUMN IF NOT EXISTS mutation_lock_state text NOT NULL DEFAULT 'unlocked',
    ADD COLUMN IF NOT EXISTS mutation_lock_reason_code text,
    ADD COLUMN IF NOT EXISTS mutation_lock_actor text,
    ADD COLUMN IF NOT EXISTS mutation_lock_changed_at timestamptz;

ALTER TABLE services
    DROP CONSTRAINT IF EXISTS services_mutation_lock_state_check;
ALTER TABLE services
    ADD CONSTRAINT services_mutation_lock_state_check
    CHECK (mutation_lock_state IN ('unlocked', 'locked'));

ALTER TABLE services
    DROP CONSTRAINT IF EXISTS services_mutation_lock_shape;
ALTER TABLE services
    ADD CONSTRAINT services_mutation_lock_shape CHECK (
        (mutation_lock_reason_code IS NULL OR length(mutation_lock_reason_code) <= 128)
        AND (mutation_lock_actor IS NULL OR length(mutation_lock_actor) <= 256)
        AND (mutation_lock_state = 'unlocked' OR mutation_lock_changed_at IS NOT NULL)
    );

-- The transition timeline gains the lock dimension. Rows stay append-only and
-- change-only, so a repeated lock command writes nothing.
ALTER TABLE service_state_transitions
    DROP CONSTRAINT IF EXISTS service_state_transitions_dimension_check;
ALTER TABLE service_state_transitions
    ADD CONSTRAINT service_state_transitions_dimension_check
    CHECK (dimension IN ('desired', 'observed', 'health', 'management', 'mutation_lock'));
