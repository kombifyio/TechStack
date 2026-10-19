-- kombify Guard G07: local-policy repair evidence. An owner's approval of
-- automatic repair becomes a registered, versioned policy authority, and a
-- repair a Guard ran on its own under one version is recorded in the same
-- repair journal as an online repair, so the cooldown, the daily attempt budget
-- and the circuit breaker count it and no reconnect resets them.
--
-- Additive and backward compatible: existing rows keep mode operator/auto with
-- NULL local columns, and a binary that predates this migration neither writes
-- nor reads the new columns or table.

ALTER TABLE guard_posture_repairs
    ADD COLUMN IF NOT EXISTS operation_id text,
    ADD COLUMN IF NOT EXISTS policy_id text,
    ADD COLUMN IF NOT EXISTS policy_version bigint,
    ADD COLUMN IF NOT EXISTS worker_generation bigint,
    ADD COLUMN IF NOT EXISTS evidence_revision bigint,
    ADD COLUMN IF NOT EXISTS evidence_digest text;

ALTER TABLE guard_posture_repairs DROP CONSTRAINT IF EXISTS guard_posture_repairs_mode_check;
ALTER TABLE guard_posture_repairs ADD CONSTRAINT guard_posture_repairs_mode_check
    CHECK (mode IN ('operator', 'auto', 'local'));

-- A local record carries its complete binding; an online repair carries none.
ALTER TABLE guard_posture_repairs DROP CONSTRAINT IF EXISTS guard_posture_repairs_local_binding_check;
ALTER TABLE guard_posture_repairs ADD CONSTRAINT guard_posture_repairs_local_binding_check
    CHECK (
        (mode = 'local'
            AND BTRIM(COALESCE(operation_id, '')) <> '' AND length(operation_id) <= 128
            AND BTRIM(COALESCE(policy_id, '')) <> '' AND policy_version > 0
            AND worker_generation > 0 AND evidence_revision BETWEEN 1 AND 1000
            AND BTRIM(COALESCE(evidence_digest, '')) <> '')
        OR (mode <> 'local' AND operation_id IS NULL)
    );

-- One record per Guard-generated operation ID and worker.
CREATE UNIQUE INDEX IF NOT EXISTS uq_guard_posture_repairs_operation
    ON guard_posture_repairs (tenant_id, worker_id, operation_id)
    WHERE operation_id IS NOT NULL;

-- The current approval names its policy and version; 0 means none registered.
ALTER TABLE guard_repair_policies
    ADD COLUMN IF NOT EXISTS policy_id text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS version bigint NOT NULL DEFAULT 0;

-- Every registered version, append-only except for superseded_at, which the
-- next version sets. Evidence is accepted only against the version it names and
-- only for an operation started inside that version's lifetime.
CREATE TABLE IF NOT EXISTS guard_repair_policy_versions (
    tenant_id text NOT NULL,
    worker_id text NOT NULL,
    policy_id text NOT NULL,
    version bigint NOT NULL,
    enabled boolean NOT NULL,
    controls jsonb NOT NULL DEFAULT '[]'::jsonb,
    expires_at timestamptz,
    approved_by text NOT NULL DEFAULT '',
    approved_at timestamptz,
    registered_at timestamptz NOT NULL DEFAULT now(),
    superseded_at timestamptz,
    PRIMARY KEY (tenant_id, worker_id, policy_id, version),
    CHECK (version > 0),
    CHECK (jsonb_typeof(controls) = 'array' AND jsonb_array_length(controls) <= 12),
    CHECK (NOT enabled OR (expires_at IS NOT NULL AND approved_at IS NOT NULL AND jsonb_array_length(controls) > 0))
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_guard_repair_policy_versions_current
    ON guard_repair_policy_versions (tenant_id, worker_id)
    WHERE superseded_at IS NULL;

ALTER TABLE guard_repair_policy_versions ENABLE ROW LEVEL SECURITY;
ALTER TABLE guard_repair_policy_versions FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation ON guard_repair_policy_versions;
CREATE POLICY tenant_isolation ON guard_repair_policy_versions
    USING (tenant_id = current_setting('app.tenant_id', true))
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true));
