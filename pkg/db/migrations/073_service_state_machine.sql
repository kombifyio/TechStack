-- Service lifecycle aggregate: canonical state vocabulary, a compare-and-swap
-- revision fence, and an immutable per-dimension transition timeline.
--
-- Services had no state machine: no CHECK constraints, no history table, and a
-- legacy `status` column that was overwritten with the measured health value.
-- This migration separates the three independent dimensions the control plane
-- already stores and pins them to the StackKits runtime-observation contract
-- (kombify-StackKits/internal/runtimeobservation/contract.go):
--
--   service status : running | stopped | starting | error | unknown
--   health status  : healthy | unhealthy | starting | not-required | unknown
--
-- `desired_state` is user-owned intent and stays the binary running/stopped
-- projection of that vocabulary. The legacy `status` column keeps serving
-- existing readers, but it is now derived from `observed_state` instead of
-- carrying the health value; control-plane workflow states (migrating,
-- deploying, pending_verification, archived) still win over the derivation.
--
-- Existing rows are migrated to legal values before the constraints are added.

SET LOCAL lock_timeout = '5s';
SELECT pg_catalog.set_config(
    'search_path',
    pg_catalog.quote_ident(pg_catalog.current_schema()) || ', pg_catalog, pg_temp',
    true
);

-- 1) Compare-and-swap fence for the service aggregate head. Existing rows start
-- at revision 0; the first accepted event moves them to 1.
ALTER TABLE services ADD COLUMN IF NOT EXISTS revision bigint NOT NULL DEFAULT 0;

ALTER TABLE services DROP CONSTRAINT IF EXISTS services_revision_nonnegative;
ALTER TABLE services ADD CONSTRAINT services_revision_nonnegative CHECK (revision >= 0);

-- 2) Migrate existing rows to the canonical vocabulary. Constraints are dropped
-- first so a partially constrained table can never block the backfill.
ALTER TABLE services DROP CONSTRAINT IF EXISTS services_desired_state_check;
ALTER TABLE services DROP CONSTRAINT IF EXISTS services_observed_state_check;
ALTER TABLE services DROP CONSTRAINT IF EXISTS services_health_state_check;

UPDATE services
SET desired_state = CASE
    WHEN lower(btrim(COALESCE(desired_state, ''))) IN (
        'stopped', 'stop', 'stopping', 'exited', 'dead', 'down', 'absent', 'archived'
    ) THEN 'stopped'
    ELSE 'running'
END
WHERE lower(btrim(COALESCE(desired_state, ''))) NOT IN ('running', 'stopped');

UPDATE services
SET observed_state = CASE
    WHEN lower(btrim(COALESCE(observed_state, ''))) IN (
        'running', 'ready', 'up', 'healthy', 'reachable', 'active', 'observed'
    ) THEN 'running'
    WHEN lower(btrim(COALESCE(observed_state, ''))) IN (
        'stopped', 'stop', 'exited', 'dead', 'down', 'archived'
    ) THEN 'stopped'
    WHEN lower(btrim(COALESCE(observed_state, ''))) IN (
        'starting', 'created', 'restarting', 'pending', 'deploying', 'initializing',
        'migrating', 'pending_verification'
    ) THEN 'starting'
    WHEN lower(btrim(COALESCE(observed_state, ''))) IN (
        'failed', 'error', 'unhealthy', 'crashed', 'degraded'
    ) THEN 'error'
    ELSE 'unknown'
END
WHERE lower(btrim(COALESCE(observed_state, ''))) NOT IN (
    'running', 'stopped', 'starting', 'error', 'unknown'
);

UPDATE services
SET health_state = CASE
    WHEN lower(btrim(COALESCE(health_state, ''))) IN ('healthy', 'ok', 'pass', 'passing', 'up') THEN 'healthy'
    WHEN lower(btrim(COALESCE(health_state, ''))) IN (
        'unhealthy', 'error', 'failed', 'critical', 'down'
    ) THEN 'unhealthy'
    -- `reachable` proves the service answers without proving a health probe
    -- passed. runtimehealth treats an unproven-but-live service as `starting`,
    -- so the canonical projection keeps that meaning instead of claiming green.
    WHEN lower(btrim(COALESCE(health_state, ''))) IN (
        'starting', 'reachable', 'created', 'restarting', 'pending', 'initializing', 'deploying'
    ) THEN 'starting'
    WHEN lower(btrim(COALESCE(health_state, ''))) IN (
        'not-required', 'not_required', 'none', 'disabled', 'n/a'
    ) THEN 'not-required'
    ELSE 'unknown'
END
WHERE lower(btrim(COALESCE(health_state, ''))) NOT IN (
    'healthy', 'unhealthy', 'starting', 'not-required', 'unknown'
);

ALTER TABLE services ADD CONSTRAINT services_desired_state_check
    CHECK (desired_state IN ('running', 'stopped'));
ALTER TABLE services ADD CONSTRAINT services_observed_state_check
    CHECK (observed_state IN ('running', 'stopped', 'starting', 'error', 'unknown'));
ALTER TABLE services ADD CONSTRAINT services_health_state_check
    CHECK (health_state IN ('healthy', 'unhealthy', 'starting', 'not-required', 'unknown'));

-- 3) Undo the historical status/health conflation. Only rows whose `status`
-- currently carries a health value are repaired; control-plane workflow states
-- and untouched legacy values stay exactly as they are.
UPDATE services
SET status = observed_state
WHERE lower(btrim(COALESCE(migration_status, ''))) NOT IN (
        'migrating', 'deploying', 'pending_verification', 'archived'
    )
    AND lower(btrim(COALESCE(status, ''))) IN (
        'healthy', 'unhealthy', 'reachable', 'not-required', 'not_required'
    );

-- 4) Immutable per-dimension transition timeline, mirroring
-- server_state_transitions. Rows are append-only: the write boundary emits one
-- row per dimension that actually changed, and never for a no-op.
-- The timeline binds tenant and service together, so a transition can never be
-- attached to another tenant's service id even when the row passes the RLS
-- policy. This mirrors the composite tenant key server_registry_outbox uses.
CREATE UNIQUE INDEX IF NOT EXISTS idx_services_tenant_identity
    ON services (tenant_id, id);

-- The tenant column deliberately carries a non-empty CHECK instead of the
-- techstack_tenants foreign key that server_state_transitions uses: neither
-- `services` nor `stacks` constrains tenant_id, so a foreign key here would
-- fail closed on rows those tables already accept. Tenant isolation stays with
-- the composite services key plus the forced RLS policy below.
CREATE TABLE IF NOT EXISTS service_state_transitions (
    id bigserial PRIMARY KEY,
    tenant_id text NOT NULL,
    service_id text NOT NULL,
    FOREIGN KEY (tenant_id, service_id) REFERENCES services (tenant_id, id) ON DELETE CASCADE,
    dimension text NOT NULL,
    from_state text,
    to_state text NOT NULL,
    reason_code text,
    source text NOT NULL,
    observed_at timestamptz NOT NULL,
    evidence_json jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now(),
    CHECK (dimension IN ('desired', 'observed', 'health')),
    CHECK (btrim(tenant_id) <> ''),
    CHECK (btrim(to_state) <> ''),
    CHECK (from_state IS NULL OR btrim(from_state) <> ''),
    CHECK (from_state IS DISTINCT FROM to_state),
    CHECK (btrim(source) <> ''),
    CHECK (reason_code IS NULL OR (btrim(reason_code) <> '' AND octet_length(reason_code) <= 128))
);

CREATE INDEX IF NOT EXISTS idx_service_state_transitions_timeline
    ON service_state_transitions (tenant_id, service_id, id DESC);

-- Same tenant posture as server_state_transitions: enabled, forced, and keyed
-- off the app.tenant_id GUC that every store transaction sets.
ALTER TABLE service_state_transitions ENABLE ROW LEVEL SECURITY;
ALTER TABLE service_state_transitions FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation ON service_state_transitions;
CREATE POLICY tenant_isolation ON service_state_transitions
    USING (tenant_id = current_setting('app.tenant_id', true))
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true));

REVOKE ALL ON TABLE service_state_transitions FROM PUBLIC;
