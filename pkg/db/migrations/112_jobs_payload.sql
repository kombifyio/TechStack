-- Job recovery payload (boot re-enqueue) and the secret-free pending-job
-- tenant directory.
--
-- The handler input previously lived only in process memory, so a crash
-- between the durable row insert and the queue enqueue stranded the job until
-- the user retried. payload_json carries the redacted recovery projection;
-- secret-bearing keys are stripped before the write (see
-- redactedJobPayloadForRecovery in pkg/orchestrator) and a redacted job is
-- deliberately not auto-resumed.
--
-- pending_job_tenants mirrors the 075/072 wake-up-directory pattern: tenant
-- IDs plus one scheduling timestamp only, so the boot scan never crosses
-- FORCE RLS to discover work.

SET LOCAL lock_timeout = '5s';
SELECT pg_catalog.set_config(
    'search_path',
    pg_catalog.quote_ident(pg_catalog.current_schema()) || ', pg_catalog, pg_temp',
    true
);

ALTER TABLE jobs
    ADD COLUMN IF NOT EXISTS payload_json jsonb NOT NULL DEFAULT '{}'::jsonb;

COMMENT ON COLUMN jobs.payload_json IS
    'Redacted recovery projection of the handler input for a pending job; secret-bearing keys are never persisted.';

CREATE INDEX IF NOT EXISTS idx_jobs_pending_tenant
    ON jobs (tenant_id, scheduled_for)
    WHERE state = 'pending';

CREATE TABLE IF NOT EXISTS pending_job_tenants (
    tenant_id text PRIMARY KEY CHECK (BTRIM(tenant_id) <> ''),
    earliest_scheduled_for timestamptz NOT NULL,
    refreshed_at timestamptz NOT NULL DEFAULT clock_timestamp()
);

REVOKE ALL ON TABLE pending_job_tenants FROM PUBLIC;

-- Backfill runs before the wake trigger exists, exactly like 075. The
-- existing jobs triggers must stay quiet for the cross-tenant statements.
ALTER TABLE jobs DISABLE TRIGGER USER;

LOCK TABLE jobs IN SHARE ROW EXCLUSIVE MODE;
ALTER TABLE jobs NO FORCE ROW LEVEL SECURITY;
ALTER TABLE jobs DISABLE ROW LEVEL SECURITY;

TRUNCATE TABLE pending_job_tenants;
INSERT INTO pending_job_tenants (tenant_id, earliest_scheduled_for, refreshed_at)
SELECT
    job.tenant_id,
    MIN(job.scheduled_for),
    clock_timestamp()
FROM jobs AS job
WHERE job.state = 'pending'
GROUP BY job.tenant_id;

ALTER TABLE jobs ENABLE ROW LEVEL SECURITY;
ALTER TABLE jobs FORCE ROW LEVEL SECURITY;
ALTER TABLE jobs ENABLE TRIGGER USER;

CREATE OR REPLACE FUNCTION jobs_wake_pending_tenant()
RETURNS trigger
LANGUAGE plpgsql
SECURITY INVOKER
SET search_path FROM CURRENT
AS $$
BEGIN
    INSERT INTO pending_job_tenants (
        tenant_id,
        earliest_scheduled_for,
        refreshed_at
    ) VALUES (
        NEW.tenant_id,
        NEW.scheduled_for,
        clock_timestamp()
    )
    ON CONFLICT (tenant_id) DO UPDATE
      SET earliest_scheduled_for = LEAST(
              pending_job_tenants.earliest_scheduled_for,
              EXCLUDED.earliest_scheduled_for
          ),
          refreshed_at = EXCLUDED.refreshed_at;
    RETURN NEW;
END;
$$;

-- A pass retires an entry only after recomputing the tenant's true pending
-- minimum, never by an optimistic write, so a compacted tenant that still has
-- pending work is re-added by this trigger on the next admit.
DROP TRIGGER IF EXISTS jobs_wake_pending_tenant ON jobs;
CREATE TRIGGER jobs_wake_pending_tenant
AFTER INSERT OR UPDATE OF state, scheduled_for ON jobs
FOR EACH ROW
WHEN (NEW.state = 'pending')
EXECUTE FUNCTION jobs_wake_pending_tenant();

COMMENT ON TABLE pending_job_tenants IS
    'Secret-free tenant wake-up directory for the boot pending-job re-enqueue; tenant IDs and scheduling only.';
COMMENT ON FUNCTION jobs_wake_pending_tenant() IS
    'Wakes the pending-job re-enqueue for a tenant that admitted pending work.';

