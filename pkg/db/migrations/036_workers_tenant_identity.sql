-- Make worker identity tenant-bound throughout the control-plane schema.
--
-- The original schema keyed workers globally by id. Runtime workers are
-- tenant-owned, so the durable identity and every reference must instead use
-- (tenant_id, id). The migration fails closed if historical references cannot
-- be proven to belong to the same tenant.

SET LOCAL lock_timeout = '5s';

LOCK TABLE workers, agent_commands, precheck_results, nodes, servers
    IN ACCESS EXCLUSIVE MODE;

-- Migrations run without app.tenant_id while all five tables FORCE tenant RLS.
-- Suspend RLS only inside this migration transaction so the integrity audit
-- cannot silently observe zero rows.
ALTER TABLE workers NO FORCE ROW LEVEL SECURITY;
ALTER TABLE workers DISABLE ROW LEVEL SECURITY;
ALTER TABLE agent_commands NO FORCE ROW LEVEL SECURITY;
ALTER TABLE agent_commands DISABLE ROW LEVEL SECURITY;
ALTER TABLE precheck_results NO FORCE ROW LEVEL SECURITY;
ALTER TABLE precheck_results DISABLE ROW LEVEL SECURITY;
ALTER TABLE nodes NO FORCE ROW LEVEL SECURITY;
ALTER TABLE nodes DISABLE ROW LEVEL SECURITY;
ALTER TABLE servers NO FORCE ROW LEVEL SECURITY;
ALTER TABLE servers DISABLE ROW LEVEL SECURITY;

DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM agent_commands AS command
        LEFT JOIN workers AS worker ON worker.id = command.worker_id
        WHERE command.worker_id IS NOT NULL
          AND (
              worker.id IS NULL
              OR command.tenant_id IS DISTINCT FROM worker.tenant_id
          )
    ) THEN
        RAISE EXCEPTION
            'worker tenant identity migration found cross-tenant references in agent_commands'
            USING ERRCODE = '23503';
    END IF;

    IF EXISTS (
        SELECT 1
        FROM precheck_results AS result
        LEFT JOIN workers AS worker ON worker.id = result.worker_id
        WHERE result.worker_id IS NOT NULL
          AND (
              worker.id IS NULL
              OR result.tenant_id IS DISTINCT FROM worker.tenant_id
          )
    ) THEN
        RAISE EXCEPTION
            'worker tenant identity migration found cross-tenant references in precheck_results'
            USING ERRCODE = '23503';
    END IF;

    IF EXISTS (
        SELECT 1
        FROM nodes AS node
        LEFT JOIN workers AS worker ON worker.id = node.worker_id
        WHERE node.worker_id IS NOT NULL
          AND (
              worker.id IS NULL
              OR node.tenant_id IS DISTINCT FROM worker.tenant_id
          )
    ) THEN
        RAISE EXCEPTION
            'worker tenant identity migration found cross-tenant references in nodes'
            USING ERRCODE = '23503';
    END IF;

    IF EXISTS (
        SELECT 1
        FROM servers AS server
        LEFT JOIN workers AS worker ON worker.id = server.worker_id
        WHERE server.worker_id IS NOT NULL
          AND (
              worker.id IS NULL
              OR server.tenant_id IS DISTINCT FROM worker.tenant_id
          )
    ) THEN
        RAISE EXCEPTION
            'worker tenant identity migration found cross-tenant references in servers'
            USING ERRCODE = '23503';
    END IF;
END;
$$;

-- Drop every real worker reference before replacing the globally-keyed
-- primary key. Constraint names come from migrations 001, 006, and 024.
ALTER TABLE agent_commands
    DROP CONSTRAINT IF EXISTS agent_commands_worker_id_fkey;
ALTER TABLE precheck_results
    DROP CONSTRAINT IF EXISTS precheck_results_worker_id_fkey;
ALTER TABLE nodes
    DROP CONSTRAINT IF EXISTS nodes_worker_id_fkey;
ALTER TABLE servers
    DROP CONSTRAINT IF EXISTS servers_worker_tenant_fk;

ALTER TABLE workers
    DROP CONSTRAINT workers_pkey;
ALTER TABLE workers
    ADD CONSTRAINT workers_pkey PRIMARY KEY USING INDEX uq_workers_tenant_id;

ALTER TABLE agent_commands
    ADD CONSTRAINT agent_commands_worker_tenant_fk
        FOREIGN KEY (tenant_id, worker_id)
        REFERENCES workers (tenant_id, id)
        ON DELETE CASCADE NOT VALID;

-- Restrict SET NULL to worker_id. tenant_id is part of the tenant boundary and
-- remains NOT NULL when an optional worker binding is removed.
ALTER TABLE precheck_results
    ADD CONSTRAINT precheck_results_worker_tenant_fk
        FOREIGN KEY (tenant_id, worker_id)
        REFERENCES workers (tenant_id, id)
        ON DELETE SET NULL (worker_id) NOT VALID;
ALTER TABLE nodes
    ADD CONSTRAINT nodes_worker_tenant_fk
        FOREIGN KEY (tenant_id, worker_id)
        REFERENCES workers (tenant_id, id)
        ON DELETE SET NULL (worker_id) NOT VALID;
ALTER TABLE servers
    ADD CONSTRAINT servers_worker_tenant_fk
        FOREIGN KEY (tenant_id, worker_id)
        REFERENCES workers (tenant_id, id)
        ON DELETE RESTRICT NOT VALID;

ALTER TABLE agent_commands
    VALIDATE CONSTRAINT agent_commands_worker_tenant_fk;
ALTER TABLE precheck_results
    VALIDATE CONSTRAINT precheck_results_worker_tenant_fk;
ALTER TABLE nodes
    VALIDATE CONSTRAINT nodes_worker_tenant_fk;
ALTER TABLE servers
    VALIDATE CONSTRAINT servers_worker_tenant_fk;

ALTER TABLE workers ENABLE ROW LEVEL SECURITY;
ALTER TABLE workers FORCE ROW LEVEL SECURITY;
ALTER TABLE agent_commands ENABLE ROW LEVEL SECURITY;
ALTER TABLE agent_commands FORCE ROW LEVEL SECURITY;
ALTER TABLE precheck_results ENABLE ROW LEVEL SECURITY;
ALTER TABLE precheck_results FORCE ROW LEVEL SECURITY;
ALTER TABLE nodes ENABLE ROW LEVEL SECURITY;
ALTER TABLE nodes FORCE ROW LEVEL SECURITY;
ALTER TABLE servers ENABLE ROW LEVEL SECURITY;
ALTER TABLE servers FORCE ROW LEVEL SECURITY;
