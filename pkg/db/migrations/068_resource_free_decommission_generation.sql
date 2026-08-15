-- Resource-free teardown starts from a sealed provision operation, but the
-- owner-requested decommission intent legitimately advances the RuntimeServer
-- aggregate before Provider Control discovers that no provider resources were
-- committed. Keep normal provider-absence releases pinned to their exact
-- operation generation; only resource-free teardown may target the current,
-- forward generation, with the same lease/resource/receipt fences.

CREATE OR REPLACE FUNCTION managed_runtime_capacity_release_validate_insert()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path FROM CURRENT
AS $$
DECLARE
    operation_kind text;
    operation_status text;
    operation_phase text;
    operation_server_id text;
    operation_server_generation bigint;
    operation_resource_generation_id uuid;
    head_sequence bigint;
    head_receipt_digest text;
    live_server_generation bigint;
    server_lifecycle text;
    server_desired text;
    server_decommissioned_at timestamptz;
BEGIN
    SELECT
        operation.operation,
        operation.status,
        operation.phase,
        operation.command_json #>> '{command,runtime_server_id}',
        NULLIF(operation.command_json->>'runtime_server_generation', '')::bigint,
        (operation.command_json #>> '{command,resource_generation_id}')::uuid,
        operation.head_sequence,
        operation.head_receipt_digest
    INTO
        operation_kind,
        operation_status,
        operation_phase,
        operation_server_id,
        operation_server_generation,
        operation_resource_generation_id,
        head_sequence,
        head_receipt_digest
    FROM provider_operations AS operation
    WHERE operation.tenant_id = NEW.tenant_id
      AND operation.operation_id = NEW.release_operation_id
      AND operation.lease_id = NEW.lease_id;

    SELECT
        server.generation,
        server.lifecycle_state,
        server.desired_state,
        server.decommissioned_at
    INTO
        live_server_generation,
        server_lifecycle,
        server_desired,
        server_decommissioned_at
    FROM servers AS server
    WHERE server.tenant_id = NEW.tenant_id
      AND server.id = NEW.server_id;

    IF NEW.release_authority NOT IN ('provider_absence', 'resource_free_teardown')
       OR (
        NEW.release_authority = 'provider_absence'
        AND (
            operation_kind IS DISTINCT FROM 'decommission'
            OR operation_status IS DISTINCT FROM 'succeeded'
            OR operation_phase IS DISTINCT FROM 'absent'
            OR operation_server_generation IS DISTINCT FROM NEW.server_generation
        )
    )
       OR (
        NEW.release_authority = 'resource_free_teardown'
        AND (
            operation_kind IS DISTINCT FROM 'provision'
            OR NOT (
                (
                    operation_status = 'pending'
                    AND operation_phase IN ('requested', 'accepted')
                )
                OR (
                    operation_status = 'failed'
                    AND operation_phase = 'failed'
                )
            )
            OR operation_server_generation IS NULL
            OR operation_server_generation < 1
            OR NEW.server_generation < operation_server_generation
            OR NOT EXISTS (
                SELECT 1
                FROM provider_operation_resource_free_terminalizations AS terminalization
                WHERE terminalization.tenant_id = NEW.tenant_id
                  AND terminalization.operation_id = NEW.release_operation_id
                  AND terminalization.lease_id = NEW.lease_id
                  AND terminalization.server_id = NEW.server_id
                  AND terminalization.server_generation = NEW.server_generation
                  AND terminalization.resource_generation_id = NEW.resource_generation_id
                  AND terminalization.head_sequence = NEW.receipt_sequence
                  AND terminalization.head_receipt_digest = NEW.receipt_digest
            )
        )
    )
       OR operation_server_id IS DISTINCT FROM NEW.server_id
       OR operation_resource_generation_id IS DISTINCT FROM NEW.resource_generation_id
       OR head_sequence IS DISTINCT FROM NEW.receipt_sequence
       OR head_receipt_digest IS DISTINCT FROM NEW.receipt_digest
       OR live_server_generation IS DISTINCT FROM NEW.server_generation
       OR server_lifecycle IS DISTINCT FROM 'decommissioned'
       OR server_desired IS DISTINCT FROM 'absent'
       OR server_decommissioned_at IS NULL
       OR NEW.released_at < server_decommissioned_at THEN
        RAISE EXCEPTION 'managed runtime capacity release lacks exact terminal absence custody'
            USING ERRCODE = '55000';
    END IF;
    RETURN NEW;
END;
$$;
