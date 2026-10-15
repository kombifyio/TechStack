-- 135_guard_posture.sql
-- kombify Guard posture: the latest host security, backup and restore-drill
-- observation of each Guard, the findings ledger those observations raise, the
-- bounded repairs an owner admitted, the owner's optional approval of automatic
-- repair, and the per-Guard cursor of the telemetry a Guard spools during an
-- outage. Every row is tenant scoped; none carries a secret or a credential.
--
-- There is deliberately no foreign key to workers: a detached Guard's ledger and
-- audit rows outlive it until their retention passes.

CREATE TABLE IF NOT EXISTS guard_posture_states (
    tenant_id text NOT NULL,
    worker_id text NOT NULL,
    server_id text NOT NULL,
    owner_subject_id text NOT NULL,
    display_name text NOT NULL DEFAULT '',
    source_epoch text NOT NULL DEFAULT '',
    source_sequence bigint NOT NULL DEFAULT 0,
    reported_at timestamptz,
    guard_version text NOT NULL DEFAULT '',
    guard_signed boolean NOT NULL DEFAULT false,
    security_status text NOT NULL DEFAULT '',
    security_reason_code text NOT NULL DEFAULT '',
    security_reported_at timestamptz,
    evidence jsonb,
    backup_status text NOT NULL DEFAULT '',
    backup_reason_code text NOT NULL DEFAULT '',
    backup_ready boolean NOT NULL DEFAULT false,
    backup_snapshot_state text NOT NULL DEFAULT '',
    backup_last_snapshot_at timestamptz,
    backup_restore_state text NOT NULL DEFAULT '',
    backup_last_restore_at timestamptz,
    backup_reported_at timestamptz,
    guard_mode text NOT NULL DEFAULT '',
    guard_update_state text NOT NULL DEFAULT '',
    guard_update_version text NOT NULL DEFAULT '',
    guard_deny_mutations boolean NOT NULL DEFAULT false,
    guard_observed_at timestamptz,
    verify_requested_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, worker_id),
    CHECK (BTRIM(server_id) <> '' AND BTRIM(owner_subject_id) <> ''),
    CHECK (source_sequence >= 0),
    CHECK (octet_length(evidence::text) <= 262144)
);

CREATE INDEX IF NOT EXISTS idx_guard_posture_states_server
    ON guard_posture_states (tenant_id, server_id);

ALTER TABLE guard_posture_states ENABLE ROW LEVEL SECURITY;
ALTER TABLE guard_posture_states FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation ON guard_posture_states;
CREATE POLICY tenant_isolation ON guard_posture_states
    USING (tenant_id = current_setting('app.tenant_id', true))
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true));

DROP TRIGGER IF EXISTS set_guard_posture_states_updated_at ON guard_posture_states;
CREATE TRIGGER set_guard_posture_states_updated_at BEFORE UPDATE ON guard_posture_states
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- The findings ledger. One row is one episode: opened when a fact is first
-- observed wrong, resolved when it is observed right again. At most one episode
-- of a key is open, which is what makes one finding one alert and one
-- resolution. Resolved episodes are the findings history and are pruned after
-- 90 days.
CREATE TABLE IF NOT EXISTS guard_posture_findings (
    id text PRIMARY KEY,
    tenant_id text NOT NULL,
    worker_id text NOT NULL,
    server_id text NOT NULL DEFAULT '',
    owner_subject_id text NOT NULL DEFAULT '',
    display_name text NOT NULL DEFAULT '',
    finding_key text NOT NULL,
    scope text NOT NULL,
    severity text NOT NULL,
    state text NOT NULL,
    expected text NOT NULL DEFAULT '',
    observed text NOT NULL DEFAULT '',
    reason_code text NOT NULL DEFAULT '',
    capability text NOT NULL DEFAULT '',
    opened_at timestamptz NOT NULL,
    last_seen_at timestamptz NOT NULL,
    alert_due_at timestamptz NOT NULL,
    alerted_at timestamptz,
    resolved_at timestamptz,
    CHECK (scope IN ('security', 'recovery', 'guard')),
    CHECK (BTRIM(finding_key) <> '')
);

CREATE UNIQUE INDEX IF NOT EXISTS guard_posture_findings_one_open
    ON guard_posture_findings (tenant_id, worker_id, finding_key)
    WHERE resolved_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_guard_posture_findings_history
    ON guard_posture_findings (tenant_id, worker_id, opened_at DESC);

ALTER TABLE guard_posture_findings ENABLE ROW LEVEL SECURITY;
ALTER TABLE guard_posture_findings FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation ON guard_posture_findings;
CREATE POLICY tenant_isolation ON guard_posture_findings
    USING (tenant_id = current_setting('app.tenant_id', true))
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true));

-- Bounded repairs of drifted controls. The row is the audit record: who asked,
-- for which controls, what the Guard reported and when.
CREATE TABLE IF NOT EXISTS guard_posture_repairs (
    id text PRIMARY KEY,
    tenant_id text NOT NULL,
    worker_id text NOT NULL,
    server_id text NOT NULL,
    mode text NOT NULL,
    requested_by text NOT NULL,
    controls jsonb NOT NULL,
    status text NOT NULL,
    reason_code text NOT NULL DEFAULT '',
    command_id text NOT NULL UNIQUE,
    steps jsonb NOT NULL DEFAULT '[]'::jsonb,
    requested_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL,
    dispatched_at timestamptz,
    finished_at timestamptz,
    CHECK (mode IN ('operator', 'auto')),
    CHECK (status IN ('queued', 'dispatched', 'succeeded', 'manual_required', 'blocked', 'failed', 'expired', 'outcome_unknown')),
    CHECK (jsonb_typeof(controls) = 'array' AND jsonb_array_length(controls) BETWEEN 1 AND 12),
    CHECK (BTRIM(requested_by) <> '')
);

CREATE INDEX IF NOT EXISTS idx_guard_posture_repairs_worker
    ON guard_posture_repairs (tenant_id, worker_id, requested_at DESC);

ALTER TABLE guard_posture_repairs ENABLE ROW LEVEL SECURITY;
ALTER TABLE guard_posture_repairs FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation ON guard_posture_repairs;
CREATE POLICY tenant_isolation ON guard_posture_repairs
    USING (tenant_id = current_setting('app.tenant_id', true))
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true));

-- The owner's approval of automatic repair for one Guard. Absent, disabled or
-- expired means observe-only.
CREATE TABLE IF NOT EXISTS guard_repair_policies (
    tenant_id text NOT NULL,
    worker_id text NOT NULL,
    enabled boolean NOT NULL DEFAULT false,
    controls jsonb NOT NULL DEFAULT '[]'::jsonb,
    expires_at timestamptz,
    approved_by text NOT NULL DEFAULT '',
    approved_at timestamptz,
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, worker_id),
    CHECK (jsonb_typeof(controls) = 'array' AND jsonb_array_length(controls) <= 12)
);

ALTER TABLE guard_repair_policies ENABLE ROW LEVEL SECURITY;
ALTER TABLE guard_repair_policies FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation ON guard_repair_policies;
CREATE POLICY tenant_isolation ON guard_repair_policies
    USING (tenant_id = current_setting('app.tenant_id', true))
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true));

-- The telemetry a Guard spools during an outage is sent with an epoch and a
-- sequence number. The cursor is the highest sequence this control plane has
-- accepted for that epoch, which makes a replay after a lost acknowledgement
-- idempotent.
CREATE TABLE IF NOT EXISTS guard_telemetry_cursors (
    tenant_id text NOT NULL,
    worker_id text NOT NULL,
    source_epoch text NOT NULL,
    last_sequence bigint NOT NULL DEFAULT 0,
    last_received_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, worker_id),
    CHECK (last_sequence >= 0)
);

ALTER TABLE guard_telemetry_cursors ENABLE ROW LEVEL SECURITY;
ALTER TABLE guard_telemetry_cursors FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation ON guard_telemetry_cursors;
CREATE POLICY tenant_isolation ON guard_telemetry_cursors
    USING (tenant_id = current_setting('app.tenant_id', true))
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true));
