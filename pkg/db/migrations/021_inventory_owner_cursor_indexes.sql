-- Canonical inventory reads receive either an owner scope or an explicitly
-- FGA-authorized tenant collection/object scope. These indexes support the
-- immutable (created_at, id) keyset cursor without weakening tenant RLS.

CREATE INDEX IF NOT EXISTS idx_servers_inventory_owner_cursor
    ON servers (tenant_id, owner_subject_id, created_at, id);

CREATE INDEX IF NOT EXISTS idx_servers_inventory_tenant_cursor
    ON servers (tenant_id, created_at, id);

CREATE INDEX IF NOT EXISTS idx_stacks_inventory_owner
    ON stacks (tenant_id, owner_subject_id, id)
    WHERE deleted_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_services_inventory_server_cursor
    ON services (tenant_id, server_id, created_at, id)
    WHERE server_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_services_inventory_tenant_cursor
    ON services (tenant_id, created_at, id)
    WHERE server_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_services_inventory_legacy_cursor
    ON services (tenant_id, stack_id, node_id, created_at, id)
    WHERE server_id IS NULL AND node_id IS NOT NULL;
