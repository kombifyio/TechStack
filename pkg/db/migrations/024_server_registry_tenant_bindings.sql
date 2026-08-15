-- Replace globally-keyed registry references with tenant-bound identities.
-- Existing rows are retained through NOT VALID constraints; every new or
-- updated binding is checked immediately and validation can follow after the
-- migration audit has repaired historical inconsistencies.

CREATE UNIQUE INDEX IF NOT EXISTS uq_techstack_instances_tenant_id
    ON techstack_instances (tenant_id, id);
CREATE UNIQUE INDEX IF NOT EXISTS uq_stacks_tenant_id
    ON stacks (tenant_id, id);
CREATE UNIQUE INDEX IF NOT EXISTS uq_workers_tenant_id
    ON workers (tenant_id, id);
CREATE UNIQUE INDEX IF NOT EXISTS uq_nodes_tenant_id
    ON nodes (tenant_id, id);

ALTER TABLE servers DROP CONSTRAINT IF EXISTS servers_instance_id_fkey;
ALTER TABLE servers DROP CONSTRAINT IF EXISTS servers_stack_id_fkey;
ALTER TABLE servers DROP CONSTRAINT IF EXISTS servers_worker_id_fkey;
ALTER TABLE servers DROP CONSTRAINT IF EXISTS servers_node_id_fkey;

ALTER TABLE servers
    ADD CONSTRAINT servers_instance_tenant_fk
        FOREIGN KEY (tenant_id, instance_id)
        REFERENCES techstack_instances (tenant_id, id)
        ON DELETE RESTRICT NOT VALID,
    ADD CONSTRAINT servers_stack_tenant_fk
        FOREIGN KEY (tenant_id, stack_id)
        REFERENCES stacks (tenant_id, id)
        ON DELETE RESTRICT NOT VALID,
    ADD CONSTRAINT servers_worker_tenant_fk
        FOREIGN KEY (tenant_id, worker_id)
        REFERENCES workers (tenant_id, id)
        ON DELETE RESTRICT NOT VALID,
    ADD CONSTRAINT servers_node_tenant_fk
        FOREIGN KEY (tenant_id, node_id)
        REFERENCES nodes (tenant_id, id)
        ON DELETE RESTRICT NOT VALID,
    ADD CONSTRAINT servers_lease_tenant_fk
        FOREIGN KEY (tenant_id, lease_id)
        REFERENCES techstack_vm_leases (tenant_id, id)
        ON DELETE RESTRICT NOT VALID;

CREATE UNIQUE INDEX IF NOT EXISTS uq_servers_tenant_worker_binding
    ON servers (tenant_id, worker_id)
    WHERE worker_id IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS uq_servers_tenant_node_binding
    ON servers (tenant_id, node_id)
    WHERE node_id IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS uq_servers_tenant_lease_binding
    ON servers (tenant_id, lease_id)
    WHERE lease_id IS NOT NULL;

ALTER TABLE server_state_transitions
    DROP CONSTRAINT IF EXISTS server_state_transitions_server_id_fkey;
ALTER TABLE server_state_transitions
    ADD CONSTRAINT server_state_transitions_server_tenant_fk
        FOREIGN KEY (tenant_id, server_id)
        REFERENCES servers (tenant_id, id)
        ON DELETE CASCADE NOT VALID;

ALTER TABLE server_inventory_snapshots
    DROP CONSTRAINT IF EXISTS server_inventory_snapshots_server_id_fkey;
ALTER TABLE server_inventory_snapshots
    ADD CONSTRAINT server_inventory_snapshots_server_tenant_fk
        FOREIGN KEY (tenant_id, server_id)
        REFERENCES servers (tenant_id, id)
        ON DELETE CASCADE NOT VALID;

ALTER TABLE services DROP CONSTRAINT IF EXISTS services_server_id_fkey;
ALTER TABLE services
    ADD CONSTRAINT services_server_tenant_fk
        FOREIGN KEY (tenant_id, server_id)
        REFERENCES servers (tenant_id, id)
        ON DELETE RESTRICT NOT VALID;

ALTER TABLE provider_desired_spec_revisions
    DROP CONSTRAINT IF EXISTS provider_desired_specs_lease_tenant_fk;
ALTER TABLE provider_desired_spec_revisions
    ADD CONSTRAINT provider_desired_specs_lease_tenant_fk
        FOREIGN KEY (tenant_id, lease_id)
        REFERENCES techstack_vm_leases (tenant_id, id)
        ON DELETE RESTRICT NOT VALID;
