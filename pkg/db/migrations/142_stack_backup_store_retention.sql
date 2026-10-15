-- Decommission retention for the managed off-site backup store.
--
-- When a stack is decommissioned its per-stack token is revoked at once; the
-- bucket stays restorable until wipe_after and is then wiped. The deadline is
-- stored here, not held by a timer, so it survives restarts and failovers.
-- Rows survive a stack soft delete, which is what keeps the deadline alive.

ALTER TABLE stack_backup_stores
    ADD COLUMN IF NOT EXISTS wipe_after timestamptz,
    ADD COLUMN IF NOT EXISTS token_revoked_at timestamptz,
    ADD COLUMN IF NOT EXISTS wiped_at timestamptz;

CREATE INDEX IF NOT EXISTS stack_backup_stores_wipe_due_idx
    ON stack_backup_stores (wipe_after)
    WHERE wipe_after IS NOT NULL AND wiped_at IS NULL;

SELECT pg_catalog.set_config(
    'search_path',
    pg_catalog.quote_ident(pg_catalog.current_schema()) || ', pg_catalog, pg_temp',
    true
);

-- Control-plane sweep across tenants through a SECURITY DEFINER function, as
-- techstack_list_due_backup_schedules does. Identity and token id only; no
-- credential leaves the table here.
CREATE OR REPLACE FUNCTION techstack_list_due_backup_wipes(
    p_now timestamptz,
    p_after_tenant text,
    p_after_stack text,
    p_limit integer
)
RETURNS TABLE (
    tenant_id text,
    stack_id text,
    token_id text
)
LANGUAGE sql
SECURITY DEFINER
SET search_path FROM CURRENT
AS $$
    SELECT s.tenant_id, s.stack_id, s.token_id
    FROM stack_backup_stores s
    WHERE s.wipe_after IS NOT NULL
      AND s.wiped_at IS NULL
      AND s.wipe_after <= p_now
      AND (s.tenant_id, s.stack_id) > (COALESCE(p_after_tenant, ''), COALESCE(p_after_stack, ''))
    ORDER BY s.tenant_id, s.stack_id
    LIMIT LEAST(GREATEST(COALESCE(p_limit, 50), 1), 200);
$$;
