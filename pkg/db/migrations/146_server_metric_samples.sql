-- Bounded host-metric history per server, written from the authenticated Guard
-- inventory (cpu/memory/disk percent) and read by the private Cloud
-- monitoring cockpit.
--
-- The Guard heartbeat only ever kept the latest figures in servers.metadata_json,
-- so no trend could be shown. Samples are stored as fixed time buckets that
-- carry a sum and a count per metric, so a bucket merges any number of
-- reports without drift and a metric the Guard did not report stays absent
-- (count 0) instead of becoming zero.
--
-- Two resolutions bound the table: 60 s buckets are kept for 24 h and 900 s
-- buckets for 30 d (about 4.3 thousand rows per server at most). The writer
-- prunes a server's expired buckets whenever it opens a new 60 s bucket, so
-- retention needs no sweeper and is enforced where the rows are produced. The
-- authoritative retention constants live in pkg/controlplane
-- (inventory_monitoring.go); the CHECK below only fixes the admissible
-- resolutions.

CREATE TABLE IF NOT EXISTS server_metric_samples (
    tenant_id text NOT NULL REFERENCES techstack_tenants(id) ON DELETE CASCADE,
    server_id text NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
    resolution_seconds integer NOT NULL,
    bucket_start timestamptz NOT NULL,
    cpu_sum double precision NOT NULL DEFAULT 0,
    cpu_count integer NOT NULL DEFAULT 0,
    memory_sum double precision NOT NULL DEFAULT 0,
    memory_count integer NOT NULL DEFAULT 0,
    disk_sum double precision NOT NULL DEFAULT 0,
    disk_count integer NOT NULL DEFAULT 0,
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, server_id, resolution_seconds, bucket_start),
    CHECK (resolution_seconds IN (60, 900)),
    CHECK (cpu_count >= 0 AND memory_count >= 0 AND disk_count >= 0)
);

ALTER TABLE server_metric_samples ENABLE ROW LEVEL SECURITY;
ALTER TABLE server_metric_samples FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation ON server_metric_samples;
CREATE POLICY tenant_isolation ON server_metric_samples
    USING (tenant_id = current_setting('app.tenant_id', true))
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true));

