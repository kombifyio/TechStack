-- Cross-tenant wake-up directories for the server-registry observation
-- sweeper (honest observed state) and the server_registry_outbox prune.
--
-- Both directories mirror the ril_signal_delivery_tenants pattern: they carry
-- tenant identifiers plus one scheduling timestamp only. Server aggregates and
-- outbox payloads stay behind FORCE RLS; the sweeper reads tenant IDs from the
-- directories and then switches to tenant-scoped transactions (app.tenant_id)
-- before touching any payload row.

SET LOCAL lock_timeout = '5s';
SELECT pg_catalog.set_config(
    'search_path',
    pg_catalog.quote_ident(pg_catalog.current_schema()) || ', pg_catalog, pg_temp',
    true
);

-- 1) Observation sweep directory: tenants that currently have servers with a
-- recorded Guard heartbeat in a demotable connection state. earliest
-- heartbeat drives the due predicate (now - freshness window).
CREATE TABLE IF NOT EXISTS server_registry_sweep_tenants (
    tenant_id text PRIMARY KEY CHECK (BTRIM(tenant_id) <> ''),
    earliest_heartbeat_at timestamptz NOT NULL,
    refreshed_at timestamptz NOT NULL DEFAULT clock_timestamp()
);

REVOKE ALL ON TABLE server_registry_sweep_tenants FROM PUBLIC;

CREATE OR REPLACE FUNCTION server_registry_wake_sweep_tenant()
RETURNS trigger
LANGUAGE plpgsql
SECURITY INVOKER
SET search_path FROM CURRENT
AS $$
BEGIN
    INSERT INTO server_registry_sweep_tenants (tenant_id, earliest_heartbeat_at, refreshed_at)
    VALUES (NEW.tenant_id, NEW.last_heartbeat_at, clock_timestamp())
    ON CONFLICT (tenant_id) DO UPDATE
      SET earliest_heartbeat_at = LEAST(
              server_registry_sweep_tenants.earliest_heartbeat_at,
              EXCLUDED.earliest_heartbeat_at
          ),
          refreshed_at = EXCLUDED.refreshed_at;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS servers_wake_registry_sweep ON servers;
CREATE TRIGGER servers_wake_registry_sweep
AFTER INSERT OR UPDATE OF last_heartbeat_at, connection_state, lifecycle_state
ON servers
FOR EACH ROW
WHEN (
    NEW.last_heartbeat_at IS NOT NULL
    AND NEW.connection_state IN ('connected', 'degraded', 'stale')
    AND NEW.lifecycle_state NOT IN ('decommissioning', 'decommissioned')
)
EXECUTE FUNCTION server_registry_wake_sweep_tenant();

-- 2) Outbox prune directory: tenants that still hold outbox history, keyed by
-- their oldest row. Due when oldest_created_at falls behind the retention
-- window.
CREATE TABLE IF NOT EXISTS server_registry_outbox_prune_tenants (
    tenant_id text PRIMARY KEY CHECK (BTRIM(tenant_id) <> ''),
    oldest_created_at timestamptz NOT NULL,
    refreshed_at timestamptz NOT NULL DEFAULT clock_timestamp()
);

REVOKE ALL ON TABLE server_registry_outbox_prune_tenants FROM PUBLIC;

CREATE OR REPLACE FUNCTION server_registry_wake_outbox_prune_tenant()
RETURNS trigger
LANGUAGE plpgsql
SECURITY INVOKER
SET search_path FROM CURRENT
AS $$
BEGIN
    INSERT INTO server_registry_outbox_prune_tenants (tenant_id, oldest_created_at, refreshed_at)
    VALUES (NEW.tenant_id, NEW.created_at, clock_timestamp())
    ON CONFLICT (tenant_id) DO UPDATE
      SET oldest_created_at = LEAST(
              server_registry_outbox_prune_tenants.oldest_created_at,
              EXCLUDED.oldest_created_at
          ),
          refreshed_at = EXCLUDED.refreshed_at;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS server_registry_outbox_wake_prune ON server_registry_outbox;
CREATE TRIGGER server_registry_outbox_wake_prune
AFTER INSERT ON server_registry_outbox
FOR EACH ROW
EXECUTE FUNCTION server_registry_wake_outbox_prune_tenant();

-- Keyset prune deletes and MIN(created_at) compaction reads stay index-only.
CREATE INDEX IF NOT EXISTS idx_server_registry_outbox_tenant_created
    ON server_registry_outbox (tenant_id, created_at, id);

-- Backfill both directories during the migration transaction only. Runtime
-- payload access remains FORCE RLS tenant-scoped after this migration commits.
LOCK TABLE servers IN SHARE ROW EXCLUSIVE MODE;
ALTER TABLE servers NO FORCE ROW LEVEL SECURITY;
ALTER TABLE servers DISABLE ROW LEVEL SECURITY;

TRUNCATE TABLE server_registry_sweep_tenants;
INSERT INTO server_registry_sweep_tenants (tenant_id, earliest_heartbeat_at, refreshed_at)
SELECT
    tracked.tenant_id,
    MIN(tracked.last_heartbeat_at),
    clock_timestamp()
FROM servers AS tracked
WHERE tracked.last_heartbeat_at IS NOT NULL
  AND tracked.connection_state IN ('connected', 'degraded', 'stale')
  AND tracked.lifecycle_state NOT IN ('decommissioning', 'decommissioned')
GROUP BY tracked.tenant_id;

ALTER TABLE servers ENABLE ROW LEVEL SECURITY;
ALTER TABLE servers FORCE ROW LEVEL SECURITY;

LOCK TABLE server_registry_outbox IN SHARE ROW EXCLUSIVE MODE;
ALTER TABLE server_registry_outbox NO FORCE ROW LEVEL SECURITY;
ALTER TABLE server_registry_outbox DISABLE ROW LEVEL SECURITY;

TRUNCATE TABLE server_registry_outbox_prune_tenants;
INSERT INTO server_registry_outbox_prune_tenants (tenant_id, oldest_created_at, refreshed_at)
SELECT
    history.tenant_id,
    MIN(history.created_at),
    clock_timestamp()
FROM server_registry_outbox AS history
GROUP BY history.tenant_id;

ALTER TABLE server_registry_outbox ENABLE ROW LEVEL SECURITY;
ALTER TABLE server_registry_outbox FORCE ROW LEVEL SECURITY;

COMMENT ON TABLE server_registry_sweep_tenants IS
    'Secret-free tenant wake-up directory for the server-registry observation sweeper; tenant IDs and heartbeat scheduling only.';
COMMENT ON TABLE server_registry_outbox_prune_tenants IS
    'Secret-free tenant wake-up directory for server_registry_outbox retention pruning; tenant IDs and oldest-row scheduling only.';
COMMENT ON FUNCTION server_registry_wake_sweep_tenant() IS
    'Wakes the observation sweeper for a tenant whose server rows carry demotable Guard heartbeat evidence.';
COMMENT ON FUNCTION server_registry_wake_outbox_prune_tenant() IS
    'Wakes the outbox prune for a tenant on every accepted server_registry_outbox insert.';
