-- Generation-bound projection from the durable runtime-server identity to
-- TechStack's provider resource ledger. The projection is append-only: old
-- resource handles remain auditable after a server receives a new lease or
-- provider generation.

-- Pin every unqualified reference to the schema selected by the migration
-- connection while excluding later, caller-controlled search-path entries.
-- This keeps the migration usable in isolated schemas as well as public.
SELECT pg_catalog.set_config(
    'search_path',
    pg_catalog.quote_ident(pg_catalog.current_schema()) || ', pg_catalog, pg_temp',
    true
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_provider_operations_tenant_operation_lease
    ON provider_operations (tenant_id, operation_id, lease_id);

CREATE TABLE IF NOT EXISTS server_provider_resource_bindings (
    tenant_id text NOT NULL,
    server_id text NOT NULL,
    server_generation bigint NOT NULL CHECK (server_generation > 0),
    lease_id text NOT NULL,
    operation_id text NOT NULL,
    binding_id text NOT NULL,
    bound_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, server_id, server_generation, operation_id, binding_id),
    UNIQUE (tenant_id, operation_id, binding_id),
    FOREIGN KEY (tenant_id, server_id)
        REFERENCES servers (tenant_id, id) ON DELETE RESTRICT,
    FOREIGN KEY (tenant_id, lease_id)
        REFERENCES techstack_vm_leases (tenant_id, id) ON DELETE RESTRICT,
    FOREIGN KEY (tenant_id, operation_id, lease_id)
        REFERENCES provider_operations (tenant_id, operation_id, lease_id) ON DELETE RESTRICT,
    FOREIGN KEY (tenant_id, operation_id, binding_id)
        REFERENCES provider_operation_resources (tenant_id, operation_id, binding_id) ON DELETE RESTRICT
);

CREATE INDEX IF NOT EXISTS idx_server_provider_bindings_server_generation
    ON server_provider_resource_bindings (tenant_id, server_id, server_generation, bound_at);

-- Existing ledger resources may predate the canonical server projection.
-- Bind only the server head whose current lease owns the operation.
INSERT INTO server_provider_resource_bindings (
    tenant_id, server_id, server_generation, lease_id,
    operation_id, binding_id, bound_at
)
SELECT
    server.tenant_id, server.id, server.generation, server.lease_id,
    operation.operation_id, resource.binding_id, clock_timestamp()
FROM servers AS server
JOIN provider_operations AS operation
  ON operation.tenant_id = server.tenant_id
 AND operation.lease_id = server.lease_id
JOIN provider_operation_resources AS resource
  ON resource.tenant_id = operation.tenant_id
 AND resource.operation_id = operation.operation_id
WHERE server.lease_id IS NOT NULL
ON CONFLICT (tenant_id, operation_id, binding_id) DO NOTHING;

CREATE OR REPLACE FUNCTION server_provider_resource_binding_guard()
RETURNS trigger
LANGUAGE plpgsql
SET search_path FROM CURRENT
AS $$
BEGIN
    IF TG_OP <> 'INSERT' THEN
        RAISE EXCEPTION 'server provider resource bindings are immutable'
            USING ERRCODE = '55000';
    END IF;

    PERFORM 1
    FROM servers
    WHERE tenant_id = NEW.tenant_id
      AND id = NEW.server_id
      AND generation = NEW.server_generation
      AND lease_id = NEW.lease_id;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'provider resource binding does not match the current server generation and lease'
            USING ERRCODE = '23503';
    END IF;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS server_provider_resource_bindings_guard_insert
    ON server_provider_resource_bindings;
CREATE TRIGGER server_provider_resource_bindings_guard_insert
    BEFORE INSERT ON server_provider_resource_bindings
    FOR EACH ROW EXECUTE FUNCTION server_provider_resource_binding_guard();

DROP TRIGGER IF EXISTS server_provider_resource_bindings_reject_update
    ON server_provider_resource_bindings;
CREATE TRIGGER server_provider_resource_bindings_reject_update
    BEFORE UPDATE ON server_provider_resource_bindings
    FOR EACH ROW EXECUTE FUNCTION server_provider_resource_binding_guard();

DROP TRIGGER IF EXISTS server_provider_resource_bindings_reject_delete
    ON server_provider_resource_bindings;
CREATE TRIGGER server_provider_resource_bindings_reject_delete
    BEFORE DELETE ON server_provider_resource_bindings
    FOR EACH ROW EXECUTE FUNCTION server_provider_resource_binding_guard();

-- Server creation and generation changes can happen after provider receipts
-- have already been recovered. Project those durable resources without
-- rewriting their historical generation bindings.
CREATE OR REPLACE FUNCTION project_server_provider_resource_bindings()
RETURNS trigger
LANGUAGE plpgsql
SET search_path FROM CURRENT
AS $$
BEGIN
    IF NEW.lease_id IS NULL THEN
        RETURN NEW;
    END IF;
    INSERT INTO server_provider_resource_bindings (
        tenant_id, server_id, server_generation, lease_id,
        operation_id, binding_id, bound_at
    )
    SELECT
        NEW.tenant_id, NEW.id, NEW.generation, NEW.lease_id,
        operation.operation_id, resource.binding_id, clock_timestamp()
    FROM provider_operations AS operation
    JOIN provider_operation_resources AS resource
      ON resource.tenant_id = operation.tenant_id
     AND resource.operation_id = operation.operation_id
    WHERE operation.tenant_id = NEW.tenant_id
      AND operation.lease_id = NEW.lease_id
    ON CONFLICT (tenant_id, operation_id, binding_id) DO NOTHING;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS servers_project_provider_resource_bindings ON servers;
CREATE TRIGGER servers_project_provider_resource_bindings
    AFTER INSERT OR UPDATE OF lease_id, generation ON servers
    FOR EACH ROW EXECUTE FUNCTION project_server_provider_resource_bindings();

ALTER TABLE server_provider_resource_bindings ENABLE ROW LEVEL SECURITY;
ALTER TABLE server_provider_resource_bindings FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation ON server_provider_resource_bindings;
CREATE POLICY tenant_isolation ON server_provider_resource_bindings
    USING (tenant_id = current_setting('app.tenant_id', true))
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true));
