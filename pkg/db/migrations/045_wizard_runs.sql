-- 045_wizard_runs.sql
-- Wizard-run ledger (ADR-0036 phase 3). One row per side-effectful wizard run
-- (first-run or expansion) so a re-submitted Idempotency-Key replays the same
-- outcome instead of minting a second stack/node. Rows are written only after
-- persistence succeeded (completed) or after a partial failure that left side
-- effects behind (failed); pre-persist rejections never consume a key.
-- homelab_id uses the composite-FK target from 044 so a run can never point
-- across tenants (FK checks bypass RLS). stack_id/job_id stay soft references
-- like jobs.stack_id: the referenced rows are soft-deleted, never dropped.

CREATE TABLE IF NOT EXISTS wizard_runs (
    id text PRIMARY KEY,
    tenant_id text NOT NULL REFERENCES techstack_tenants(id) ON DELETE CASCADE,
    owner_subject_id text NOT NULL,
    idempotency_key text,
    request_sha256 text NOT NULL,
    run_kind text NOT NULL,
    requested_run_kind text NOT NULL,
    homelab_id text,
    stack_id text,
    node_id text,
    job_id text,
    pairing_job_id text,
    status text NOT NULL,
    intent_json jsonb NOT NULL DEFAULT '{}'::jsonb,
    result_json jsonb NOT NULL DEFAULT '{}'::jsonb,
    error_reason text,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CHECK (BTRIM(id) <> ''),
    CHECK (BTRIM(owner_subject_id) <> ''),
    CHECK (BTRIM(request_sha256) <> ''),
    CHECK (run_kind IN ('first-run', 'expansion')),
    CHECK (requested_run_kind IN ('first-run', 'expansion')),
    CHECK (status IN ('completed', 'failed'))
);

CREATE INDEX IF NOT EXISTS idx_wizard_runs_tenant ON wizard_runs (tenant_id);
CREATE INDEX IF NOT EXISTS idx_wizard_runs_owner
    ON wizard_runs (tenant_id, owner_subject_id, created_at DESC);
-- One ledger entry per (tenant, owner, key); replay/conflict semantics live in
-- the store (hash match -> replay, mismatch on completed -> conflict).
CREATE UNIQUE INDEX IF NOT EXISTS uq_wizard_runs_idempotency
    ON wizard_runs (tenant_id, owner_subject_id, idempotency_key)
    WHERE idempotency_key IS NOT NULL;

ALTER TABLE wizard_runs DROP CONSTRAINT IF EXISTS fk_wizard_runs_homelab_tenant;
ALTER TABLE wizard_runs
    ADD CONSTRAINT fk_wizard_runs_homelab_tenant
    FOREIGN KEY (tenant_id, homelab_id)
    REFERENCES homelabs (tenant_id, id)
    ON DELETE RESTRICT;

ALTER TABLE wizard_runs ENABLE ROW LEVEL SECURITY;
ALTER TABLE wizard_runs FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation ON wizard_runs;
CREATE POLICY tenant_isolation ON wizard_runs
    USING (tenant_id = current_setting('app.tenant_id', true))
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true));

DROP TRIGGER IF EXISTS set_wizard_runs_updated_at ON wizard_runs;
CREATE TRIGGER set_wizard_runs_updated_at BEFORE UPDATE ON wizard_runs
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();
