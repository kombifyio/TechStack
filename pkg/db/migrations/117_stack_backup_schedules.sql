-- Per-stack backup cadence, projected from the ResolvedPlan at deploy.
--
-- StackKits owns the schedule authority (#BackupScheduleV1); Techstack only
-- executes it, so this table is a projection and never the source of truth.
-- Without it the control plane has no way to know a stack is due: the
-- BackupPolicyRef on service runtime placement is a reference string, not a
-- cadence, and stack_backup_stores exposes no cross-tenant listing.
--
-- next_due_at is stored rather than derived so the scanner can index on it and
-- claim due rows without recomputing a cadence for every stack on every pass.

CREATE TABLE IF NOT EXISTS stack_backup_schedules (
    tenant_id text NOT NULL,
    stack_id text NOT NULL,
    owner_id text NOT NULL DEFAULT '',
    stack_name text NOT NULL DEFAULT '',
    cadence text NOT NULL,
    hour_utc smallint NOT NULL DEFAULT 2,
    minute_utc smallint NOT NULL DEFAULT 0,
    weekday_utc text NOT NULL DEFAULT 'sunday',
    include_content boolean NOT NULL DEFAULT false,
    enabled boolean NOT NULL DEFAULT true,
    next_due_at timestamptz NOT NULL,
    last_run_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, stack_id),
    FOREIGN KEY (tenant_id, stack_id)
        REFERENCES stacks (tenant_id, id)
        ON DELETE CASCADE,
    CHECK (cadence IN ('hourly', 'daily', 'weekly')),
    CHECK (hour_utc >= 0 AND hour_utc <= 23),
    CHECK (minute_utc >= 0 AND minute_utc <= 59),
    CHECK (weekday_utc IN ('sunday','monday','tuesday','wednesday','thursday','friday','saturday'))
);

CREATE INDEX IF NOT EXISTS stack_backup_schedules_due_idx
    ON stack_backup_schedules (next_due_at)
    WHERE enabled;

ALTER TABLE stack_backup_schedules ENABLE ROW LEVEL SECURITY;
ALTER TABLE stack_backup_schedules FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation ON stack_backup_schedules;
CREATE POLICY tenant_isolation ON stack_backup_schedules
    USING (tenant_id = current_setting('app.tenant_id', true))
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true));

DROP TRIGGER IF EXISTS set_stack_backup_schedules_updated_at ON stack_backup_schedules;
CREATE TRIGGER set_stack_backup_schedules_updated_at BEFORE UPDATE ON stack_backup_schedules
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- The scanner is control-plane and must see every tenant's due rows, which
-- tenant RLS correctly forbids for a tenant-scoped connection. It reads through
-- this SECURITY DEFINER function instead of being granted BYPASSRLS, matching
-- provider_control_list_stale_capacity_recovery_candidates. The function
-- returns identity only - never a credential - and is keyset-paginated so a
-- pass is bounded.
CREATE OR REPLACE FUNCTION techstack_list_due_backup_schedules(
    p_now timestamptz,
    p_after_tenant text,
    p_after_stack text,
    p_limit integer
)
RETURNS TABLE (
    tenant_id text,
    stack_id text,
    owner_id text,
    stack_name text,
    include_content boolean
)
LANGUAGE sql
SECURITY DEFINER
SET search_path = pg_catalog, public
AS $$
    SELECT s.tenant_id, s.stack_id, s.owner_id, s.stack_name, s.include_content
    FROM stack_backup_schedules s
    WHERE s.enabled
      AND s.next_due_at <= p_now
      AND (s.tenant_id, s.stack_id) > (COALESCE(p_after_tenant, ''), COALESCE(p_after_stack, ''))
    ORDER BY s.tenant_id, s.stack_id
    LIMIT LEAST(GREATEST(COALESCE(p_limit, 50), 1), 200);
$$;
