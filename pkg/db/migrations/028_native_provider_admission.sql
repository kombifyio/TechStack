-- Atomic native provider admission.
--
-- The runtime-server and runtime-lease rows deliberately reference each other.
-- Deferring both tenant-bound foreign keys lets one transaction create that
-- closed aggregate without a visible placeholder. The idempotency record also
-- retains the exact provider operation admitted under its request digest.

SET LOCAL lock_timeout = '5s';
SELECT pg_catalog.set_config(
    'search_path',
    pg_catalog.quote_ident(pg_catalog.current_schema()) || ', pg_catalog, pg_temp',
    true
);

ALTER TABLE servers
    DROP CONSTRAINT IF EXISTS servers_lease_tenant_fk;
ALTER TABLE servers
    ADD CONSTRAINT servers_lease_tenant_fk
        FOREIGN KEY (tenant_id, lease_id)
        REFERENCES techstack_vm_leases (tenant_id, id)
        ON UPDATE RESTRICT ON DELETE RESTRICT
        DEFERRABLE INITIALLY DEFERRED NOT VALID;

ALTER TABLE techstack_vm_leases
    DROP CONSTRAINT IF EXISTS techstack_vm_leases_server_tenant_fk;
ALTER TABLE techstack_vm_leases
    ADD CONSTRAINT techstack_vm_leases_server_tenant_fk
        FOREIGN KEY (tenant_id, server_id)
        REFERENCES servers (tenant_id, id)
        ON UPDATE RESTRICT ON DELETE RESTRICT
        DEFERRABLE INITIALLY DEFERRED NOT VALID;

ALTER TABLE runtime_lease_idempotency_records
    ADD COLUMN IF NOT EXISTS operation_id text;

ALTER TABLE runtime_lease_idempotency_records
    DROP CONSTRAINT IF EXISTS runtime_lease_idempotency_operation_required;
ALTER TABLE runtime_lease_idempotency_records
    ADD CONSTRAINT runtime_lease_idempotency_operation_required
        CHECK (operation_scope <> 'providercontrol.provision' OR operation_id IS NOT NULL) NOT VALID;

ALTER TABLE runtime_lease_idempotency_records
    DROP CONSTRAINT IF EXISTS runtime_lease_idempotency_operation_tenant_fk;
ALTER TABLE runtime_lease_idempotency_records
    ADD CONSTRAINT runtime_lease_idempotency_operation_tenant_fk
        FOREIGN KEY (tenant_id, operation_id, lease_id)
        REFERENCES provider_operations (tenant_id, operation_id, lease_id)
        ON UPDATE RESTRICT ON DELETE RESTRICT
        DEFERRABLE INITIALLY DEFERRED NOT VALID;
