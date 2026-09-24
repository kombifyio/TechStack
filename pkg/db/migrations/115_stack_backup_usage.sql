-- Control-plane measurement of managed backup storage, taken from the object
-- store rather than reported by the measured node.
--
-- This is the number the storage quota admits and denies on, and the number
-- the supplier invoices. The node-reported repo_size_bytes remains the
-- customer-facing "protected data" figure: it sums the latest snapshot per
-- source, so it is neither kombify's cost nor the customer's protected total.

CREATE TABLE IF NOT EXISTS stack_backup_usage (
    tenant_id text NOT NULL,
    stack_id text NOT NULL,
    bucket text NOT NULL,
    stored_bytes bigint NOT NULL,
    object_count bigint NOT NULL,
    measured_at timestamptz NOT NULL DEFAULT now(),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, stack_id),
    FOREIGN KEY (tenant_id, stack_id)
        REFERENCES stacks (tenant_id, id)
        ON DELETE CASCADE,
    CHECK (BTRIM(bucket) <> ''),
    CHECK (stored_bytes >= 0),
    CHECK (object_count >= 0)
);

ALTER TABLE stack_backup_usage ENABLE ROW LEVEL SECURITY;
ALTER TABLE stack_backup_usage FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation ON stack_backup_usage;
CREATE POLICY tenant_isolation ON stack_backup_usage
    USING (tenant_id = current_setting('app.tenant_id', true))
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true));

DROP TRIGGER IF EXISTS set_stack_backup_usage_updated_at ON stack_backup_usage;
CREATE TRIGGER set_stack_backup_usage_updated_at BEFORE UPDATE ON stack_backup_usage
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();
