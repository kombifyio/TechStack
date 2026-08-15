-- Liveness fencing for durable job executions plus the secret-free tenant
-- directory the reclaim sweep reads.
--
-- A job row is moved to state='running' by the per-stack execution claim in
-- controlplane.StartJob. Until now nothing could tell a live execution from a
-- row abandoned by a process death: an OOM kill or a Render revision restart
-- left the row 'running' forever, and because StartJob refuses a second
-- running job on the same stack, every later operation on that stack deferred
-- with waiting_stack_execution once per second, forever.
--
-- The fix is an explicit lease. StartJob stamps the owning process identity
-- plus a deadline; the 500ms progress heartbeat renews the deadline; the
-- reclaimer may terminalize only a running row whose deadline has passed. The
-- shape mirrors the server-registry observation sweep (072): a cross-tenant
-- wake-up directory carrying tenant IDs and one scheduling timestamp only,
-- with payload access staying behind FORCE RLS in tenant-scoped transactions.

SET LOCAL lock_timeout = '5s';
SELECT pg_catalog.set_config(
    'search_path',
    pg_catalog.quote_ident(pg_catalog.current_schema()) || ', pg_catalog, pg_temp',
    true
);

ALTER TABLE jobs
    ADD COLUMN IF NOT EXISTS execution_owner_id text,
    ADD COLUMN IF NOT EXISTS execution_lease_expires_at timestamptz;

COMMENT ON COLUMN jobs.execution_owner_id IS
    'Control-plane process (boot-scoped) that holds this running execution. Distinct from instance_id, which identifies a managed TechStack instance.';
COMMENT ON COLUMN jobs.execution_lease_expires_at IS
    'Deadline after which a running execution is presumed orphaned and may be reclaimed. Renewed by the progress heartbeat.';

-- The reclaim scan is a tenant-scoped keyset over expired leases only.
CREATE INDEX IF NOT EXISTS idx_jobs_execution_lease_expiry
    ON jobs (tenant_id, execution_lease_expires_at, id)
    WHERE state = 'running' AND execution_lease_expires_at IS NOT NULL;

CREATE TABLE IF NOT EXISTS job_execution_reclaim_tenants (
    tenant_id text PRIMARY KEY CHECK (BTRIM(tenant_id) <> ''),
    earliest_lease_expires_at timestamptz NOT NULL,
    refreshed_at timestamptz NOT NULL DEFAULT clock_timestamp()
);

REVOKE ALL ON TABLE job_execution_reclaim_tenants FROM PUBLIC;

-- Backfill runs before the wake trigger exists, exactly like 071, so the
-- cross-tenant statements below cannot trip a per-row tenant-scope guard.
-- The existing jobs triggers (set_jobs_updated_at and the provider
-- decommission wait refresh, which raises 42501 outside an exact tenant
-- scope) must stay quiet for the same reason.
ALTER TABLE jobs DISABLE TRIGGER USER;

LOCK TABLE jobs IN SHARE ROW EXCLUSIVE MODE;
ALTER TABLE jobs NO FORCE ROW LEVEL SECURITY;
ALTER TABLE jobs DISABLE ROW LEVEL SECURITY;

-- Historical debris is the point of this migration: rows stranded 'running'
-- by a dead process get a lease derived from their last heartbeat, so they are
-- already expired and the reclaimer can retire them on the first pass. A row
-- whose process is genuinely alive re-stamps its own lease within one
-- heartbeat, long before the reclaim grace period elapses.
UPDATE jobs
SET execution_lease_expires_at = updated_at + interval '90 seconds'
WHERE state = 'running' AND execution_lease_expires_at IS NULL;

TRUNCATE TABLE job_execution_reclaim_tenants;
INSERT INTO job_execution_reclaim_tenants (
    tenant_id,
    earliest_lease_expires_at,
    refreshed_at
)
SELECT
    job.tenant_id,
    MIN(job.execution_lease_expires_at),
    clock_timestamp()
FROM jobs AS job
WHERE job.state = 'running'
  AND job.execution_lease_expires_at IS NOT NULL
GROUP BY job.tenant_id;

ALTER TABLE jobs ENABLE ROW LEVEL SECURITY;
ALTER TABLE jobs FORCE ROW LEVEL SECURITY;
ALTER TABLE jobs ENABLE TRIGGER USER;

CREATE OR REPLACE FUNCTION jobs_wake_execution_reclaim_tenant()
RETURNS trigger
LANGUAGE plpgsql
SECURITY INVOKER
SET search_path FROM CURRENT
AS $$
BEGIN
    INSERT INTO job_execution_reclaim_tenants (
        tenant_id,
        earliest_lease_expires_at,
        refreshed_at
    ) VALUES (
        NEW.tenant_id,
        NEW.execution_lease_expires_at,
        clock_timestamp()
    )
    ON CONFLICT (tenant_id) DO UPDATE
      SET earliest_lease_expires_at = LEAST(
              job_execution_reclaim_tenants.earliest_lease_expires_at,
              EXCLUDED.earliest_lease_expires_at
          ),
          refreshed_at = EXCLUDED.refreshed_at;
    RETURN NEW;
END;
$$;

-- A renewal moves the deadline later while LEAST pins the directory at the
-- older instant, so a swept tenant stays due until the sweeper compacts it.
-- That is deliberate: an entry may only be retired by a pass that has just
-- recomputed the tenant's true minimum, never by an optimistic write.
DROP TRIGGER IF EXISTS jobs_wake_execution_reclaim ON jobs;
CREATE TRIGGER jobs_wake_execution_reclaim
AFTER INSERT OR UPDATE OF state, execution_lease_expires_at ON jobs
FOR EACH ROW
WHEN (NEW.state = 'running' AND NEW.execution_lease_expires_at IS NOT NULL)
EXECUTE FUNCTION jobs_wake_execution_reclaim_tenant();

COMMENT ON TABLE job_execution_reclaim_tenants IS
    'Secret-free tenant wake-up directory for the durable job execution reclaim sweep; tenant IDs and lease scheduling only.';
COMMENT ON FUNCTION jobs_wake_execution_reclaim_tenant() IS
    'Wakes the job execution reclaimer for a tenant that holds a leased running execution.';
