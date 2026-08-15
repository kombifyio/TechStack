-- Normalize user-owned server intent and add an atomic, secret-free registry
-- integration outbox. Lifecycle remains an independent control-plane state.

-- Historical rows must be normalized outside tenant RLS. Migrations run as the
-- table owner in one transaction and restore the tenant fence before commit.
ALTER TABLE servers NO FORCE ROW LEVEL SECURITY;
ALTER TABLE servers DISABLE ROW LEVEL SECURITY;

-- Split the historical event-wide reason into authority-scoped projections.
-- Transition history is the strongest available backfill evidence; the
-- legacy head reason is used only when its last writer owns that dimension.
ALTER TABLE servers ADD COLUMN IF NOT EXISTS lifecycle_reason_code text;
ALTER TABLE servers ADD COLUMN IF NOT EXISTS desired_reason_code text;
ALTER TABLE servers ADD COLUMN IF NOT EXISTS connection_reason_code text;
ALTER TABLE servers ADD COLUMN IF NOT EXISTS health_reason_code text;
ALTER TABLE servers ADD COLUMN IF NOT EXISTS lifecycle_changed_at timestamptz;
ALTER TABLE servers ADD COLUMN IF NOT EXISTS desired_changed_at timestamptz;
ALTER TABLE servers ADD COLUMN IF NOT EXISTS health_changed_at timestamptz;

UPDATE servers AS s
SET lifecycle_reason_code = COALESCE(
    (SELECT CASE WHEN octet_length(COALESCE(t.reason_code, '')) <= 128 THEN t.reason_code END
     FROM server_state_transitions AS t
     WHERE t.tenant_id = s.tenant_id AND t.server_id = s.id AND t.dimension = 'lifecycle'
     ORDER BY t.observed_at DESC, t.id DESC LIMIT 1),
    CASE WHEN s.source_authority = 'control-plane' AND octet_length(COALESCE(s.reason_code, '')) <= 128 THEN s.reason_code END
)
WHERE s.lifecycle_reason_code IS NULL;

UPDATE servers AS s
SET desired_reason_code = COALESCE(
    (SELECT CASE WHEN octet_length(COALESCE(t.reason_code, '')) <= 128 THEN t.reason_code END
     FROM server_state_transitions AS t
     WHERE t.tenant_id = s.tenant_id AND t.server_id = s.id AND t.dimension = 'desired'
     ORDER BY t.observed_at DESC, t.id DESC LIMIT 1),
    CASE WHEN s.source_authority = 'control-plane' AND octet_length(COALESCE(s.reason_code, '')) <= 128 THEN s.reason_code END
)
WHERE s.desired_reason_code IS NULL;

UPDATE servers AS s
SET connection_reason_code = COALESCE(
    (SELECT CASE WHEN octet_length(COALESCE(t.reason_code, '')) <= 128 THEN t.reason_code END
     FROM server_state_transitions AS t
     WHERE t.tenant_id = s.tenant_id AND t.server_id = s.id AND t.dimension = 'connection'
     ORDER BY t.observed_at DESC, t.id DESC LIMIT 1),
    CASE WHEN s.source_authority = 'guard' AND octet_length(COALESCE(s.reason_code, '')) <= 128 THEN s.reason_code END
)
WHERE s.connection_reason_code IS NULL;

UPDATE servers AS s
SET health_reason_code = COALESCE(
    (SELECT CASE WHEN octet_length(COALESCE(t.reason_code, '')) <= 128 THEN t.reason_code END
     FROM server_state_transitions AS t
     WHERE t.tenant_id = s.tenant_id AND t.server_id = s.id AND t.dimension = 'health'
     ORDER BY t.observed_at DESC, t.id DESC LIMIT 1),
    CASE WHEN s.source_authority = 'guard' AND octet_length(COALESCE(s.reason_code, '')) <= 128 THEN s.reason_code END
)
WHERE s.health_reason_code IS NULL;

UPDATE servers AS s
SET lifecycle_changed_at = COALESCE(
    (SELECT t.observed_at
     FROM server_state_transitions AS t
     WHERE t.tenant_id = s.tenant_id AND t.server_id = s.id AND t.dimension = 'lifecycle'
     ORDER BY t.observed_at DESC, t.id DESC LIMIT 1),
    s.decommissioned_at,
    s.created_at
)
WHERE s.lifecycle_changed_at IS NULL;

UPDATE servers AS s
SET desired_changed_at = COALESCE(
    (SELECT t.observed_at
     FROM server_state_transitions AS t
     WHERE t.tenant_id = s.tenant_id AND t.server_id = s.id AND t.dimension = 'desired'
     ORDER BY t.observed_at DESC, t.id DESC LIMIT 1),
    s.created_at
)
WHERE s.desired_changed_at IS NULL;

UPDATE servers AS s
SET health_changed_at = COALESCE(
    (SELECT t.observed_at
     FROM server_state_transitions AS t
     WHERE t.tenant_id = s.tenant_id AND t.server_id = s.id AND t.dimension = 'health'
     ORDER BY t.observed_at DESC, t.id DESC LIMIT 1),
    s.connection_changed_at,
    s.created_at
)
WHERE s.health_changed_at IS NULL;

ALTER TABLE servers ALTER COLUMN lifecycle_changed_at SET DEFAULT now();
ALTER TABLE servers ALTER COLUMN lifecycle_changed_at SET NOT NULL;
ALTER TABLE servers ALTER COLUMN desired_changed_at SET DEFAULT now();
ALTER TABLE servers ALTER COLUMN desired_changed_at SET NOT NULL;
ALTER TABLE servers ALTER COLUMN health_changed_at SET DEFAULT now();
ALTER TABLE servers ALTER COLUMN health_changed_at SET NOT NULL;

ALTER TABLE servers DROP CONSTRAINT IF EXISTS servers_dimension_reason_codes_bounded;
ALTER TABLE servers
    ADD CONSTRAINT servers_dimension_reason_codes_bounded CHECK (
        octet_length(COALESCE(lifecycle_reason_code, '')) <= 128
        AND octet_length(COALESCE(desired_reason_code, '')) <= 128
        AND octet_length(COALESCE(connection_reason_code, '')) <= 128
        AND octet_length(COALESCE(health_reason_code, '')) <= 128
    );

-- The legacy constraint accepts active/decommissioned, while the normalized
-- values are running/absent. Remove it before writing the new vocabulary.
ALTER TABLE servers DROP CONSTRAINT IF EXISTS servers_desired_state_check;

UPDATE servers
SET desired_state = CASE desired_state
    WHEN 'active' THEN 'running'
    WHEN 'decommissioned' THEN 'absent'
    ELSE desired_state
END;

-- Teardown is a terminal tombstone. Normalize historical projections before
-- enforcing the invariant for all new and updated rows.
UPDATE servers
SET desired_state = 'absent'
WHERE lifecycle_state IN ('decommissioning', 'decommissioned');

UPDATE servers
SET decommissioned_at = COALESCE(decommissioned_at, updated_at, created_at)
WHERE lifecycle_state = 'decommissioned';

UPDATE servers
SET decommissioned_at = NULL
WHERE lifecycle_state <> 'decommissioned';

ALTER TABLE servers DROP CONSTRAINT IF EXISTS servers_desired_state_check;
ALTER TABLE servers ALTER COLUMN desired_state SET DEFAULT 'running';
ALTER TABLE servers
    ADD CONSTRAINT servers_desired_state_check
    CHECK (desired_state IN ('running', 'stopped', 'absent'));

ALTER TABLE servers DROP CONSTRAINT IF EXISTS servers_decommission_tombstone_check;
ALTER TABLE servers
    ADD CONSTRAINT servers_decommission_tombstone_check CHECK (
        (lifecycle_state NOT IN ('decommissioning', 'decommissioned') OR desired_state = 'absent')
        AND ((lifecycle_state = 'decommissioned') = (decommissioned_at IS NOT NULL))
    );

ALTER TABLE server_state_transitions
    DROP CONSTRAINT IF EXISTS server_state_transitions_dimension_check;
ALTER TABLE server_state_transitions
    ADD CONSTRAINT server_state_transitions_dimension_check
    CHECK (dimension IN ('lifecycle', 'desired', 'connection', 'health'));

ALTER TABLE servers ENABLE ROW LEVEL SECURITY;
ALTER TABLE servers FORCE ROW LEVEL SECURITY;

CREATE TABLE IF NOT EXISTS server_registry_outbox (
    id bigserial PRIMARY KEY,
    tenant_id text NOT NULL,
    server_id text NOT NULL,
    aggregate_revision bigint NOT NULL,
    generation bigint NOT NULL,
    authority text NOT NULL,
    source text NOT NULL,
    event_type text NOT NULL,
    payload_json jsonb NOT NULL DEFAULT '{}'::jsonb,
    occurred_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    available_at timestamptz NOT NULL DEFAULT now(),
    published_at timestamptz,
    attempts integer NOT NULL DEFAULT 0,
    claim_generation bigint NOT NULL DEFAULT 0,
    claim_owner text,
    claim_token_digest bytea,
    claim_expires_at timestamptz,
    last_error text,
    CONSTRAINT server_registry_outbox_revision_positive
        CHECK (aggregate_revision > 0 AND generation > 0),
    CONSTRAINT server_registry_outbox_claim_valid CHECK (
        attempts >= 0 AND claim_generation >= 0
        AND ((claim_owner IS NULL AND claim_token_digest IS NULL AND claim_expires_at IS NULL)
          OR (claim_owner IS NOT NULL AND octet_length(claim_token_digest) = 32 AND claim_expires_at IS NOT NULL))
    ),
    CONSTRAINT server_registry_outbox_server_fk
        FOREIGN KEY (tenant_id, server_id)
        REFERENCES servers (tenant_id, id)
        ON DELETE CASCADE,
    CONSTRAINT server_registry_outbox_revision_unique
        UNIQUE (tenant_id, server_id, aggregate_revision)
);

CREATE INDEX IF NOT EXISTS idx_server_registry_outbox_due
    ON server_registry_outbox (available_at, id)
    WHERE published_at IS NULL;

ALTER TABLE server_registry_outbox ENABLE ROW LEVEL SECURITY;
ALTER TABLE server_registry_outbox FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation ON server_registry_outbox;
CREATE POLICY tenant_isolation ON server_registry_outbox
    USING (tenant_id = current_setting('app.tenant_id', true))
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true));
