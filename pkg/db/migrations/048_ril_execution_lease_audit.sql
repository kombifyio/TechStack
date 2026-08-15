-- Make governed action and execution-lease transitions durable, correlated,
-- and append-only. Expired execution leases can then be taken over with a new
-- fencing token without erasing the previous ownership history.

SET LOCAL lock_timeout = '5s';
SELECT pg_catalog.set_config(
    'search_path',
    pg_catalog.quote_ident(pg_catalog.current_schema()) || ', pg_catalog, pg_temp',
    true
);

ALTER TABLE ril_action_execution_ledger
    ADD COLUMN IF NOT EXISTS audit_correlation_id text,
    ADD COLUMN IF NOT EXISTS takeover_count bigint NOT NULL DEFAULT 0;

ALTER TABLE ril_action_execution_ledger
    DROP CONSTRAINT IF EXISTS ril_action_execution_ledger_audit_correlation_required;
ALTER TABLE ril_action_execution_ledger
    ADD CONSTRAINT ril_action_execution_ledger_audit_correlation_required CHECK (
        audit_correlation_id IS NOT NULL
        AND NULLIF(BTRIM(audit_correlation_id), '') IS NOT NULL
    ) NOT VALID;

ALTER TABLE ril_action_execution_ledger
    DROP CONSTRAINT IF EXISTS ril_action_execution_ledger_takeover_count_valid;
ALTER TABLE ril_action_execution_ledger
    ADD CONSTRAINT ril_action_execution_ledger_takeover_count_valid CHECK (
        takeover_count >= 0
    );

CREATE TABLE IF NOT EXISTS ril_action_transition_audit (
    sequence_id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    tenant_id text NOT NULL,
    action_card_id text NOT NULL,
    from_status text,
    to_status text NOT NULL,
    audit_correlation_id text NOT NULL,
    actor_subject_id text NOT NULL,
    execution_id text,
    trace_id text,
    occurred_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CHECK (NULLIF(BTRIM(to_status), '') IS NOT NULL),
    CHECK (NULLIF(BTRIM(audit_correlation_id), '') IS NOT NULL),
    CHECK (NULLIF(BTRIM(actor_subject_id), '') IS NOT NULL)
);

CREATE INDEX IF NOT EXISTS idx_ril_action_transition_audit_card
    ON ril_action_transition_audit (tenant_id, action_card_id, sequence_id);

CREATE TABLE IF NOT EXISTS ril_execution_lease_audit (
    sequence_id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    tenant_id text NOT NULL,
    idempotency_key text NOT NULL,
    execution_id text NOT NULL,
    event_type text NOT NULL,
    reservation_token text NOT NULL,
    audit_correlation_id text NOT NULL,
    takeover_count bigint NOT NULL,
    occurred_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CHECK (event_type IN ('acquired', 'taken-over', 'completed')),
    CHECK (NULLIF(BTRIM(reservation_token), '') IS NOT NULL),
    CHECK (NULLIF(BTRIM(audit_correlation_id), '') IS NOT NULL),
    CHECK (takeover_count >= 0)
);

CREATE INDEX IF NOT EXISTS idx_ril_execution_lease_audit_identity
    ON ril_execution_lease_audit (tenant_id, idempotency_key, sequence_id);

ALTER TABLE ril_action_transition_audit ENABLE ROW LEVEL SECURITY;
ALTER TABLE ril_action_transition_audit FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation ON ril_action_transition_audit;
CREATE POLICY tenant_isolation ON ril_action_transition_audit
    USING (tenant_id = current_setting('app.tenant_id', true))
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true));

ALTER TABLE ril_execution_lease_audit ENABLE ROW LEVEL SECURITY;
ALTER TABLE ril_execution_lease_audit FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation ON ril_execution_lease_audit;
CREATE POLICY tenant_isolation ON ril_execution_lease_audit
    USING (tenant_id = current_setting('app.tenant_id', true))
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true));

CREATE OR REPLACE FUNCTION reject_ril_audit_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION 'RIL audit rows are append-only';
END;
$$;

DROP TRIGGER IF EXISTS ril_action_transition_audit_append_only ON ril_action_transition_audit;
CREATE TRIGGER ril_action_transition_audit_append_only
    BEFORE UPDATE OR DELETE ON ril_action_transition_audit
    FOR EACH ROW EXECUTE FUNCTION reject_ril_audit_mutation();

DROP TRIGGER IF EXISTS ril_execution_lease_audit_append_only ON ril_execution_lease_audit;
CREATE TRIGGER ril_execution_lease_audit_append_only
    BEFORE UPDATE OR DELETE ON ril_execution_lease_audit
    FOR EACH ROW EXECUTE FUNCTION reject_ril_audit_mutation();
