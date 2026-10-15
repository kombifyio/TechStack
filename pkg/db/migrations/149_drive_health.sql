-- Drive evidence and alert episodes are committed together. Device paths are
-- only locations; immutable hardware IDs own all continuity and history.
CREATE TABLE IF NOT EXISTS guard_drive_health (
 tenant_id text NOT NULL,
 server_id text NOT NULL CHECK (btrim(server_id) <> ''),
 worker_id text NOT NULL CHECK (btrim(worker_id) <> ''),
 source_epoch text NOT NULL DEFAULT '',
 source_sequence bigint NOT NULL DEFAULT 0 CHECK (source_sequence >= 0),
 snapshot_json jsonb NOT NULL CHECK (jsonb_typeof(snapshot_json) = 'object'),
 updated_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY (tenant_id,server_id)
);
CREATE TABLE IF NOT EXISTS guard_drive_health_outbox (
 tenant_id text NOT NULL,
 event_id text NOT NULL,
 event_order bigint GENERATED ALWAYS AS IDENTITY,
 server_id text NOT NULL,
 episode_id text NOT NULL,
 event_json jsonb NOT NULL CHECK (jsonb_typeof(event_json) = 'object'),
 created_at timestamptz NOT NULL DEFAULT now(),
 lease_until timestamptz,
 delivered_at timestamptz,
 PRIMARY KEY (tenant_id,event_id),
 FOREIGN KEY (tenant_id,server_id) REFERENCES guard_drive_health(tenant_id,server_id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS guard_drive_health_outbox_pending ON guard_drive_health_outbox(tenant_id,created_at) WHERE delivered_at IS NULL;
CREATE INDEX IF NOT EXISTS guard_drive_health_outbox_episode_pending ON guard_drive_health_outbox(tenant_id,episode_id,event_order) WHERE delivered_at IS NULL;
ALTER TABLE guard_drive_health ENABLE ROW LEVEL SECURITY;
ALTER TABLE guard_drive_health FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation ON guard_drive_health;
CREATE POLICY tenant_isolation ON guard_drive_health USING (tenant_id = current_setting('app.tenant_id',true)) WITH CHECK (tenant_id = current_setting('app.tenant_id',true));
ALTER TABLE guard_drive_health_outbox ENABLE ROW LEVEL SECURITY;
ALTER TABLE guard_drive_health_outbox FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation ON guard_drive_health_outbox;
CREATE POLICY tenant_isolation ON guard_drive_health_outbox USING (tenant_id = current_setting('app.tenant_id',true)) WITH CHECK (tenant_id = current_setting('app.tenant_id',true));

-- Trusted background enumeration contains only tenant IDs, matching the
-- scheduled-drift directory. All evidence and delivery reads still enter RLS.
CREATE TABLE IF NOT EXISTS guard_drive_health_tenants (
 tenant_id text PRIMARY KEY,
 refreshed_at timestamptz NOT NULL DEFAULT now()
);
REVOKE ALL ON TABLE guard_drive_health_tenants FROM PUBLIC;
COMMENT ON TABLE guard_drive_health_tenants IS 'Secret-free tenant directory for drive health aging and durable notification retry.';
