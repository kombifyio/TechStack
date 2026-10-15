-- kombify Guard posture: a verify request is answered only by an observation
-- the Guard started after it learned of the request. The request carries an
-- opaque token the Guard echoes in that observation's report; the control plane
-- keeps the pending token and counts the reports that arrived without it, so a
-- Guard that predates tokens is not asked in a loop. Empty means no request is
-- pending. A request pending before this migration is not carried over: the
-- routine observation answers the host's state and the owner can ask again.
ALTER TABLE guard_posture_states
    ADD COLUMN IF NOT EXISTS verify_request_id text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS verify_unechoed integer NOT NULL DEFAULT 0;

ALTER TABLE guard_posture_states DROP CONSTRAINT IF EXISTS guard_posture_states_verify_check;
ALTER TABLE guard_posture_states ADD CONSTRAINT guard_posture_states_verify_check
    CHECK (verify_unechoed >= 0 AND char_length(verify_request_id) <= 64);
