-- Atomic provider-receipt projection into the exact RuntimeServer generation.
--
-- The providerexecutor UUID resource generation and the RuntimeServer numeric
-- generation are independent authorities. New native operations pin both in
-- their immutable private command envelope. Provider resources, lifecycle
-- transitions, registry outbox events, and capacity release facts can then be
-- committed by one application transaction without granting provider code any
-- Guard-owned health or inventory authority.

SET LOCAL lock_timeout = '5s';
SELECT pg_catalog.set_config(
    'search_path',
    pg_catalog.quote_ident(pg_catalog.current_schema()) || ', pg_catalog, pg_temp',
    true
);

-- This is a stopped-lane cutover, never a mixed rolling deploy. The external
-- provider-egress kill switch and drain are operator-controlled; the database
-- independently refuses the migration while any unsettled claim could still
-- own an ambiguous provider side effect. TTL expiry revokes execution
-- capability; it is not proof that the provider request stopped. RLS is
-- disabled only under migration ownership and restored in the same
-- transaction.
ALTER TABLE provider_operation_execution_claims NO FORCE ROW LEVEL SECURITY;
ALTER TABLE provider_operation_execution_claims DISABLE ROW LEVEL SECURITY;
ALTER TABLE provider_operations NO FORCE ROW LEVEL SECURITY;
ALTER TABLE provider_operations DISABLE ROW LEVEL SECURITY;
ALTER TABLE provider_provision_resolution_decisions NO FORCE ROW LEVEL SECURITY;
ALTER TABLE provider_provision_resolution_decisions DISABLE ROW LEVEL SECURITY;
ALTER TABLE provider_provision_discovery_observations NO FORCE ROW LEVEL SECURITY;
ALTER TABLE provider_provision_discovery_observations DISABLE ROW LEVEL SECURITY;
ALTER TABLE runtime_lease_execution_authorities NO FORCE ROW LEVEL SECURITY;
ALTER TABLE runtime_lease_execution_authorities DISABLE ROW LEVEL SECURITY;
ALTER TABLE managed_runtime_capacity_reservations NO FORCE ROW LEVEL SECURITY;
ALTER TABLE managed_runtime_capacity_reservations DISABLE ROW LEVEL SECURITY;
ALTER TABLE provider_operation_resources NO FORCE ROW LEVEL SECURITY;
ALTER TABLE provider_operation_resources DISABLE ROW LEVEL SECURITY;
ALTER TABLE techstack_vm_leases NO FORCE ROW LEVEL SECURITY;
ALTER TABLE techstack_vm_leases DISABLE ROW LEVEL SECURITY;
ALTER TABLE servers NO FORCE ROW LEVEL SECURITY;
ALTER TABLE servers DISABLE ROW LEVEL SECURITY;

DO $provider_receipt_cutover_preflight$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM provider_operation_execution_claims AS claim
        JOIN provider_operations AS operation
          ON operation.tenant_id = claim.tenant_id
         AND operation.operation_id = claim.operation_id
        WHERE claim.claim_access = 'side_effecting'
          AND claim.state IN ('active', 'released')
          AND operation.head_sequence = claim.head_sequence
          AND operation.head_receipt_digest = claim.head_receipt_digest
          AND NOT EXISTS (
              SELECT 1
              FROM provider_provision_resolution_decisions AS decision
              JOIN provider_provision_discovery_observations AS observation
                ON observation.tenant_id = decision.tenant_id
               AND observation.operation_id = decision.operation_id
               AND observation.observation_id = decision.observation_id
               AND observation.snapshot_digest =
                    decision.observation_snapshot_digest
               AND observation.head_sequence =
                    decision.expected_head_sequence
               AND observation.head_receipt_digest =
                    decision.expected_head_receipt_digest
              WHERE decision.tenant_id = claim.tenant_id
                AND decision.operation_id = claim.operation_id
                AND decision.expected_head_sequence = claim.head_sequence
                AND decision.expected_head_receipt_digest =
                    claim.head_receipt_digest
                AND decision.outcome = 'no_candidate_observed'
                AND observation.collected_at >= CASE claim.state
                    WHEN 'released' THEN claim.released_at
                    ELSE claim.lease_expires_at
                END
          )
    ) THEN
        RAISE EXCEPTION
            'provider receipt cutover requires external egress disablement and all unsettled side-effecting provider claims to be recovered or manually settled'
            USING ERRCODE = '55000';
    END IF;

    IF EXISTS (
        SELECT 1
        FROM provider_operations AS operation
        JOIN managed_runtime_capacity_reservations AS reservation
          ON reservation.tenant_id = operation.tenant_id
         AND reservation.operation_id = operation.operation_id
         AND reservation.lease_id = operation.lease_id
         AND reservation.resource_generation_id =
             (operation.command_json #>> '{command,resource_generation_id}')::uuid
        WHERE operation.operation = 'provision'
          AND operation.status = 'failed'
          AND operation.phase = 'failed'
          AND operation.command_json->>'schema_version' =
              'techstack.provider-control-operation/v1'
          AND operation.command_json->>'execution_authority' =
              'techstack_provider_control'
          AND NOT EXISTS (
              SELECT 1
              FROM provider_operation_resources AS resource
              WHERE resource.tenant_id = operation.tenant_id
                AND resource.operation_id = operation.operation_id
          )
          AND NOT (
              jsonb_typeof(
                  operation.command_json->'runtime_server_generation'
              ) = 'number'
              AND operation.command_json->>'runtime_server_generation'
                  ~ '^[1-9][0-9]{0,18}$'
              AND (
                  operation.command_json->>'runtime_server_generation'
              )::numeric <= 9223372036854775807
          )
    ) THEN
        RAISE EXCEPTION
            'provider receipt cutover found held zero-resource failed provision custody without an exact runtime server generation pin; operator reconciliation is required'
            USING ERRCODE = '55000';
    END IF;

    IF EXISTS (
        SELECT 1
        FROM techstack_vm_leases AS lease
        JOIN runtime_lease_execution_authorities AS authority
          ON authority.tenant_id = lease.tenant_id
         AND authority.lease_id = lease.id
        LEFT JOIN servers AS server
          ON server.tenant_id = lease.tenant_id
         AND server.id = lease.server_id
        WHERE authority.execution_authority = 'techstack_provider_control'
          AND (
              NULLIF(LOWER(BTRIM(lease.provider_id)), '') IS NULL
              OR NULLIF(LOWER(BTRIM(lease.provider_id)), '') NOT IN ('ionos', 'centron')
              OR server.id IS NULL
              OR server.lease_id IS DISTINCT FROM lease.id
              OR (
                  NULLIF(LOWER(BTRIM(server.provider_ref)), '') IS NOT NULL
                  AND NULLIF(LOWER(BTRIM(server.provider_ref)), '')
                      IS DISTINCT FROM NULLIF(LOWER(BTRIM(lease.provider_id)), '')
              )
          )
    ) THEN
        RAISE EXCEPTION
            'provider receipt cutover found a native lease without an exact IONOS/Centron RuntimeServer provider binding'
            USING ERRCODE = '55000';
    END IF;
END;
$provider_receipt_cutover_preflight$;

-- Native admissions before this migration did not always project a canonical
-- provider_ref onto the RuntimeServer. Bind blanks and canonicalize only an
-- equivalent case/whitespace variant from the exact immutable native lease
-- authority; conflicting nonblank identities fail in the preflight.
UPDATE servers AS server
SET provider_ref = LOWER(BTRIM(lease.provider_id)),
    revision = server.revision + 1
FROM techstack_vm_leases AS lease
JOIN runtime_lease_execution_authorities AS authority
  ON authority.tenant_id = lease.tenant_id
 AND authority.lease_id = lease.id
WHERE authority.execution_authority = 'techstack_provider_control'
  AND server.tenant_id = lease.tenant_id
  AND server.id = lease.server_id
  AND server.lease_id = lease.id
  AND (
      NULLIF(BTRIM(server.provider_ref), '') IS NULL
      OR (
          NULLIF(LOWER(BTRIM(server.provider_ref)), '') =
              NULLIF(LOWER(BTRIM(lease.provider_id)), '')
          AND server.provider_ref IS DISTINCT FROM
              LOWER(BTRIM(lease.provider_id))
      )
  );

ALTER TABLE servers ENABLE ROW LEVEL SECURITY;
ALTER TABLE servers FORCE ROW LEVEL SECURITY;
ALTER TABLE techstack_vm_leases ENABLE ROW LEVEL SECURITY;
ALTER TABLE techstack_vm_leases FORCE ROW LEVEL SECURITY;
ALTER TABLE runtime_lease_execution_authorities ENABLE ROW LEVEL SECURITY;
ALTER TABLE runtime_lease_execution_authorities FORCE ROW LEVEL SECURITY;
ALTER TABLE provider_provision_discovery_observations ENABLE ROW LEVEL SECURITY;
ALTER TABLE provider_provision_discovery_observations FORCE ROW LEVEL SECURITY;
ALTER TABLE provider_provision_resolution_decisions ENABLE ROW LEVEL SECURITY;
ALTER TABLE provider_provision_resolution_decisions FORCE ROW LEVEL SECURITY;
ALTER TABLE provider_operation_resources ENABLE ROW LEVEL SECURITY;
ALTER TABLE provider_operation_resources FORCE ROW LEVEL SECURITY;
ALTER TABLE managed_runtime_capacity_reservations ENABLE ROW LEVEL SECURITY;
ALTER TABLE managed_runtime_capacity_reservations FORCE ROW LEVEL SECURITY;
ALTER TABLE provider_operations ENABLE ROW LEVEL SECURITY;
ALTER TABLE provider_operations FORCE ROW LEVEL SECURITY;
ALTER TABLE provider_operation_execution_claims ENABLE ROW LEVEL SECURITY;
ALTER TABLE provider_operation_execution_claims FORCE ROW LEVEL SECURITY;

-- Runtime may read but must never receive UPDATE on Guard-owned leases. This
-- narrow boundary acquires the row lock needed to keep receipt preparation
-- and its later RuntimeServer projection in one teardown-consistent snapshot.
CREATE OR REPLACE FUNCTION provider_control_lock_runtime_lease_projection(
    requested_lease_id text
)
RETURNS TABLE (
    desired_state text,
    cancelled_at timestamptz,
    server_id text,
    resource_generation_id text
)
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path FROM CURRENT
AS $$
DECLARE
    scoped_tenant_id text := NULLIF(current_setting('app.tenant_id', true), '');
BEGIN
    IF scoped_tenant_id IS NULL
       OR requested_lease_id IS NULL
       OR BTRIM(requested_lease_id) = '' THEN
        RAISE EXCEPTION 'provider-control lease projection lock requires exact tenant and lease'
            USING ERRCODE = '42501';
    END IF;
    RETURN QUERY
    SELECT
        lease.desired_state,
        lease.cancelled_at,
        lease.server_id,
        lease.resource_generation_id::text
    FROM techstack_vm_leases AS lease
    WHERE lease.tenant_id = scoped_tenant_id
      AND lease.id = requested_lease_id
    FOR SHARE OF lease;
END;
$$;

ALTER TABLE server_provider_resource_bindings
    ADD COLUMN IF NOT EXISTS resource_generation_id uuid;

-- The binding ledger is FORCE-RLS and append-only at runtime. Migration
-- ownership is the only authority allowed to fill the new exact generation
-- column, and both fences are restored before any later statement can run.
ALTER TABLE server_provider_resource_bindings NO FORCE ROW LEVEL SECURITY;
ALTER TABLE server_provider_resource_bindings DISABLE ROW LEVEL SECURITY;
ALTER TABLE server_provider_resource_bindings
    DISABLE TRIGGER server_provider_resource_bindings_reject_update;
ALTER TABLE provider_operations NO FORCE ROW LEVEL SECURITY;
ALTER TABLE provider_operations DISABLE ROW LEVEL SECURITY;

UPDATE server_provider_resource_bindings AS binding
SET resource_generation_id =
    (operation.command_json #>> '{command,resource_generation_id}')::uuid
FROM provider_operations AS operation
WHERE operation.tenant_id = binding.tenant_id
  AND operation.operation_id = binding.operation_id
  AND binding.resource_generation_id IS NULL;

ALTER TABLE server_provider_resource_bindings
    ENABLE TRIGGER server_provider_resource_bindings_reject_update;
ALTER TABLE provider_operations ENABLE ROW LEVEL SECURITY;
ALTER TABLE provider_operations FORCE ROW LEVEL SECURITY;
ALTER TABLE server_provider_resource_bindings ENABLE ROW LEVEL SECURITY;
ALTER TABLE server_provider_resource_bindings FORCE ROW LEVEL SECURITY;

ALTER TABLE server_provider_resource_bindings
    ALTER COLUMN resource_generation_id SET NOT NULL;

CREATE INDEX IF NOT EXISTS idx_server_provider_bindings_resource_generation
    ON server_provider_resource_bindings (
        tenant_id,
        lease_id,
        resource_generation_id,
        server_generation,
        bound_at
    );

-- Existing operation envelopes predate the numeric pin. They remain readable
-- for inventory/forensics but cannot execute or project a fresh receipt. No
-- heuristic backfill guesses which historical RuntimeServer generation owned
-- an operation.
CREATE OR REPLACE FUNCTION provider_operation_runtime_generation_guard()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path FROM CURRENT
AS $$
DECLARE
    envelope_version text;
    execution_authority text;
    pinned_server_generation bigint;
    command_server_id text;
    command_provider_id text;
    command_lease_revision bigint;
    command_resource_generation_id uuid;
    live_server_generation bigint;
    live_server_lease_id text;
    live_server_provider_ref text;
    live_lease_revision bigint;
    live_lease_server_id text;
    live_resource_generation_id uuid;
BEGIN
    envelope_version := NEW.command_json->>'schema_version';
    execution_authority := NEW.command_json->>'execution_authority';
    IF envelope_version IS DISTINCT FROM 'techstack.provider-control-operation/v1'
       OR execution_authority IS DISTINCT FROM 'techstack_provider_control' THEN
        RETURN NEW;
    END IF;

    pinned_server_generation :=
        NULLIF(NEW.command_json->>'runtime_server_generation', '')::bigint;
    IF pinned_server_generation IS NULL OR pinned_server_generation < 1 THEN
        RAISE EXCEPTION 'native provider operation requires a positive runtime server generation pin'
            USING ERRCODE = '55000';
    END IF;
    IF TG_OP = 'UPDATE'
       AND NEW.command_json->>'runtime_server_generation'
            IS DISTINCT FROM OLD.command_json->>'runtime_server_generation' THEN
        RAISE EXCEPTION 'provider operation runtime server generation pin is immutable'
            USING ERRCODE = '55000';
    END IF;
    IF TG_OP = 'UPDATE' THEN
        RETURN NEW;
    END IF;

    command_server_id := NEW.command_json #>> '{command,runtime_server_id}';
    command_provider_id := NEW.command_json #>> '{command,provider_id}';
    command_lease_revision :=
        (NEW.command_json #>> '{command,lease_revision}')::bigint;
    command_resource_generation_id :=
        (NEW.command_json #>> '{command,resource_generation_id}')::uuid;

    SELECT server.generation, server.lease_id, server.provider_ref
    INTO live_server_generation, live_server_lease_id, live_server_provider_ref
    FROM servers AS server
    WHERE server.tenant_id = NEW.tenant_id
      AND server.id = command_server_id
    FOR SHARE OF server;

    SELECT lease.lease_revision, lease.server_id, lease.resource_generation_id
    INTO live_lease_revision, live_lease_server_id, live_resource_generation_id
    FROM techstack_vm_leases AS lease
    WHERE lease.tenant_id = NEW.tenant_id
      AND lease.id = NEW.lease_id;

    IF live_server_generation IS DISTINCT FROM pinned_server_generation
       OR live_server_lease_id IS DISTINCT FROM NEW.lease_id
       OR NULLIF(LOWER(BTRIM(command_provider_id)), '') IS NULL
       OR NULLIF(LOWER(BTRIM(live_server_provider_ref)), '')
            IS DISTINCT FROM NULLIF(LOWER(BTRIM(command_provider_id)), '')
       OR live_lease_revision IS DISTINCT FROM command_lease_revision
       OR live_lease_server_id IS DISTINCT FROM command_server_id
       OR live_resource_generation_id IS DISTINCT FROM command_resource_generation_id THEN
        RAISE EXCEPTION 'provider operation runtime server generation pin is stale'
            USING ERRCODE = '55000';
    END IF;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS provider_operations_runtime_generation_guard
    ON provider_operations;
CREATE TRIGGER provider_operations_runtime_generation_guard
BEFORE INSERT OR UPDATE ON provider_operations
FOR EACH ROW EXECUTE FUNCTION provider_operation_runtime_generation_guard();

-- Replace only the provider_operations trigger from the shared immutable
-- ledger function. A result from an exact consumed claim must retain any
-- provider handles even when teardown began while the provider call was in
-- flight. That custody append may advance its own head but can never revive
-- the RuntimeServer; new claims remain fenced separately below.
CREATE OR REPLACE FUNCTION provider_operation_head_update_guard()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path FROM CURRENT
AS $$
DECLARE
    live_lease_revision bigint;
    live_server_id text;
    live_resource_generation_id uuid;
    live_lease_desired_state text;
    live_cancelled_at timestamptz;
    live_server_lease_id text;
    live_server_generation bigint;
    live_server_provider_ref text;
    live_server_desired_state text;
    live_server_lifecycle_state text;
    live_server_decommissioned_at timestamptz;
    teardown_requested boolean;
    claimed_result_append boolean := false;
    amo_resources_bound_transition boolean := false;
    operator_adoption boolean := false;
BEGIN
    IF to_jsonb(NEW) IS NOT DISTINCT FROM to_jsonb(OLD) THEN
        RETURN NEW;
    END IF;
    IF (to_jsonb(NEW) - ARRAY[
            'status',
            'phase',
            'head_sequence',
            'head_receipt_digest',
            'updated_at'
        ])
       IS DISTINCT FROM
       (to_jsonb(OLD) - ARRAY[
            'status',
            'phase',
            'head_sequence',
            'head_receipt_digest',
            'updated_at'
        ]) THEN
        RAISE EXCEPTION 'provider operation command and custody fields are immutable'
            USING ERRCODE = '55000';
    END IF;
    IF NEW.head_sequence <> OLD.head_sequence + 1
       OR NEW.updated_at < OLD.updated_at THEN
        RAISE EXCEPTION 'provider operation head must advance by exactly one receipt'
            USING ERRCODE = '55000';
    END IF;
    PERFORM 1
    FROM provider_operation_receipts AS receipt
    WHERE receipt.tenant_id = NEW.tenant_id
      AND receipt.operation_id = NEW.operation_id
      AND receipt.sequence = NEW.head_sequence
      AND receipt.previous_receipt_digest = OLD.head_receipt_digest
      AND receipt.receipt_digest = NEW.head_receipt_digest
      AND receipt.status = NEW.status
      AND receipt.phase = NEW.phase;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'provider operation head must reference its appended receipt'
            USING ERRCODE = '55000';
    END IF;

    SELECT EXISTS (
        SELECT 1
        FROM provider_operation_execution_claims AS claim
        WHERE claim.tenant_id = OLD.tenant_id
          AND claim.operation_id = OLD.operation_id
          AND claim.head_sequence = OLD.head_sequence
          AND claim.head_receipt_digest = OLD.head_receipt_digest
          AND claim.state = 'consumed'
          AND claim.claim_token_digest = CASE
              WHEN NULLIF(
                    current_setting(
                        'app.provider_execution_claim_token',
                        true
                    ),
                    ''
                  ) IS NULL THEN NULL
              ELSE encode(
                    sha256(
                        convert_to(
                            current_setting(
                                'app.provider_execution_claim_token',
                                true
                            ),
                            'UTF8'
                        )
                    ),
                    'hex'
                  )
          END
          AND claim.claim_owner =
                current_setting(
                    'app.provider_execution_claim_owner',
                    true
                )
    ) INTO claimed_result_append;

    amo_resources_bound_transition :=
        OLD.operation = 'provision'
        AND OLD.provision_dispatch_mode =
            'at_most_once_dispatch_manual_reconcile'
        AND OLD.status = 'pending'
        AND OLD.phase = 'accepted'
        AND NEW.status = 'pending'
        AND NEW.phase = 'resources_bound';
    operator_adoption :=
        amo_resources_bound_transition AND NOT claimed_result_append;

    SELECT
        runtime_lease.lease_revision,
        runtime_lease.server_id,
        runtime_lease.resource_generation_id,
        runtime_lease.desired_state,
        runtime_lease.cancelled_at,
        runtime_server.lease_id,
        runtime_server.generation,
        runtime_server.provider_ref,
        runtime_server.desired_state,
        runtime_server.lifecycle_state,
        runtime_server.decommissioned_at
    INTO
        live_lease_revision,
        live_server_id,
        live_resource_generation_id,
        live_lease_desired_state,
        live_cancelled_at,
        live_server_lease_id,
        live_server_generation,
        live_server_provider_ref,
        live_server_desired_state,
        live_server_lifecycle_state,
        live_server_decommissioned_at
    FROM techstack_vm_leases AS runtime_lease
    JOIN servers AS runtime_server
      ON runtime_server.tenant_id = runtime_lease.tenant_id
     AND runtime_server.id = runtime_lease.server_id
    WHERE runtime_lease.tenant_id = OLD.tenant_id
      AND runtime_lease.id = OLD.lease_id
    FOR SHARE OF runtime_lease, runtime_server;

    IF NOT FOUND
       OR live_lease_revision IS DISTINCT FROM
            (OLD.command_json #>> '{command,lease_revision}')::bigint
       OR live_server_id IS DISTINCT FROM
            OLD.command_json #>> '{command,runtime_server_id}'
       OR live_resource_generation_id IS DISTINCT FROM
            (OLD.command_json #>> '{command,resource_generation_id}')::uuid
       OR live_server_lease_id IS DISTINCT FROM OLD.lease_id
       OR live_server_generation <
            NULLIF(
                OLD.command_json->>'runtime_server_generation',
                ''
            )::bigint
       OR (
            live_server_generation IS DISTINCT FROM
                NULLIF(
                    OLD.command_json->>'runtime_server_generation',
                    ''
                )::bigint
            AND NOT claimed_result_append
            AND NOT operator_adoption
       )
       OR NULLIF(
            LOWER(BTRIM(OLD.command_json #>> '{command,provider_id}')),
            ''
          ) IS NULL
       OR NULLIF(LOWER(BTRIM(live_server_provider_ref)), '')
            IS DISTINCT FROM NULLIF(
                LOWER(BTRIM(OLD.command_json #>> '{command,provider_id}')),
                ''
            )
       OR live_server_lifecycle_state = 'decommissioned'
       OR live_server_decommissioned_at IS NOT NULL THEN
        RAISE EXCEPTION 'provider operation head is stale against the live typed runtime generation'
            USING ERRCODE = '55000';
    END IF;

    teardown_requested := live_cancelled_at IS NOT NULL
        OR live_lease_desired_state = 'absent'
        OR live_server_desired_state = 'absent'
        OR live_server_lifecycle_state = 'decommissioning';
    IF OLD.operation = 'decommission' THEN
        IF NOT teardown_requested THEN
            RAISE EXCEPTION 'provider decommission head has no teardown intent'
                USING ERRCODE = '55000';
        END IF;
    ELSIF teardown_requested
          AND NOT claimed_result_append
          AND NOT operator_adoption THEN
        RAISE EXCEPTION 'provider operation head is fenced by cancellation or teardown intent'
            USING ERRCODE = '55000';
    END IF;

    IF OLD.phase = 'requested' AND NEW.phase = 'accepted' THEN
        IF OLD.provision_dispatch_mode = 'blocked' THEN
            RAISE EXCEPTION 'blocked provider operation cannot enter accepted custody'
                USING ERRCODE = '55000';
        END IF;
        PERFORM 1
        FROM provider_operation_execution_claims AS claim
        WHERE claim.tenant_id = OLD.tenant_id
          AND claim.operation_id = OLD.operation_id
          AND claim.head_sequence = OLD.head_sequence
          AND claim.head_receipt_digest = OLD.head_receipt_digest
          AND claim.state = 'active';
        IF FOUND THEN
            RAISE EXCEPTION 'coordinator-only accepted transition cannot orphan an active claim'
                USING ERRCODE = '55000';
        END IF;
    ELSIF operator_adoption THEN
        PERFORM 1
        FROM provider_provision_resolution_decisions AS decision
        JOIN provider_provision_discovery_observations AS observation
          ON observation.tenant_id = decision.tenant_id
         AND observation.operation_id = decision.operation_id
         AND observation.observation_id = decision.observation_id
         AND observation.snapshot_digest =
                decision.observation_snapshot_digest
        WHERE decision.tenant_id = OLD.tenant_id
          AND decision.operation_id = OLD.operation_id
          AND decision.expected_head_sequence = OLD.head_sequence
          AND decision.expected_head_receipt_digest =
                OLD.head_receipt_digest
          AND decision.outcome = 'adopted_exact_candidate'
          AND decision.result_receipt_sequence = NEW.head_sequence
          AND decision.result_receipt_digest = NEW.head_receipt_digest
          AND decision.decision_token_digest = CASE
              WHEN NULLIF(
                    current_setting(
                        'app.provider_resolution_token',
                        true
                    ),
                    ''
                  ) IS NULL THEN NULL
              ELSE encode(
                    sha256(
                        convert_to(
                            current_setting(
                                'app.provider_resolution_token',
                                true
                            ),
                            'UTF8'
                        )
                    ),
                    'hex'
                  )
          END
          AND observation.candidate_count = 1
          AND observation.candidate_graphs_json->0->>'graph_digest' =
                decision.selected_candidate_digest;
        IF NOT FOUND THEN
            RAISE EXCEPTION 'AMO exact-candidate adoption requires its transaction-bound decision'
                USING ERRCODE = '55000';
        END IF;
    ELSE
        IF NOT claimed_result_append THEN
            RAISE EXCEPTION 'provider operation transition requires a consumed execution claim'
                USING ERRCODE = '55000';
        END IF;
        IF OLD.provision_dispatch_mode = 'blocked' THEN
            RAISE EXCEPTION 'blocked provider operation cannot advance'
                USING ERRCODE = '55000';
        END IF;
        IF OLD.operation = 'provision' AND OLD.phase = 'accepted' THEN
            PERFORM 1
            FROM provider_provision_dispatch_guards AS dispatch_guard
            WHERE dispatch_guard.tenant_id = OLD.tenant_id
              AND dispatch_guard.lease_id = OLD.lease_id
              AND dispatch_guard.resource_generation_id =
                    (
                        OLD.command_json
                            #>> '{command,resource_generation_id}'
                    )::uuid
              AND dispatch_guard.operation_id = OLD.operation_id
              AND dispatch_guard.dispatch_mode =
                    OLD.provision_dispatch_mode
              AND dispatch_guard.guard_origin = 'first_claim';
            IF NOT FOUND THEN
                RAISE EXCEPTION 'provision accepted transition requires exact generation dispatch custody'
                    USING ERRCODE = '55000';
            END IF;
        END IF;
        IF OLD.operation = 'provision'
           AND OLD.provision_dispatch_mode =
                'at_most_once_dispatch_manual_reconcile'
           AND OLD.phase = 'accepted' THEN
            PERFORM 1
            FROM provider_operation_execution_claims AS claim
            JOIN provider_provision_dispatch_guards AS dispatch_guard
              ON dispatch_guard.tenant_id = claim.tenant_id
             AND dispatch_guard.operation_id = claim.operation_id
             AND dispatch_guard.head_sequence = claim.head_sequence
             AND dispatch_guard.head_receipt_digest =
                    claim.head_receipt_digest
             AND dispatch_guard.first_claim_token_digest =
                    claim.claim_token_digest
             AND dispatch_guard.first_claim_owner = claim.claim_owner
            WHERE claim.tenant_id = OLD.tenant_id
              AND claim.operation_id = OLD.operation_id
              AND claim.head_sequence = OLD.head_sequence
              AND claim.head_receipt_digest = OLD.head_receipt_digest
              AND claim.state = 'consumed'
              AND dispatch_guard.dispatch_mode =
                    OLD.provision_dispatch_mode
              AND dispatch_guard.guard_origin = 'first_claim';
            IF NOT FOUND THEN
                RAISE EXCEPTION 'AMO provision accepted transition requires its consumed first-claim guard'
                    USING ERRCODE = '55000';
            END IF;
        END IF;
    END IF;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS provider_operations_immutable_update
    ON provider_operations;
CREATE TRIGGER provider_operations_immutable_update
BEFORE UPDATE ON provider_operations
FOR EACH ROW EXECUTE FUNCTION provider_operation_head_update_guard();

-- A read-only AMO discovery snapshot freezes dispatch for its exact accepted
-- head. Serialize the snapshot with raw claim takeover as well as the normal
-- Go path: the operation advisory lock alone does not serialize two FOR SHARE
-- readers, while the exact claim-row lock does.
CREATE OR REPLACE FUNCTION provider_provision_discovery_active_claim_guard()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path FROM CURRENT
AS $$
DECLARE
    claim_state text;
    claim_expires_at timestamptz;
    claim_released_at timestamptz;
BEGIN
    IF current_setting('transaction_isolation') IS DISTINCT FROM 'read committed' THEN
        RAISE EXCEPTION 'provision discovery requires read committed serialization'
            USING ERRCODE = '55000';
    END IF;
    PERFORM pg_advisory_xact_lock(hashtext(NEW.tenant_id), hashtext(NEW.operation_id));

    PERFORM 1
    FROM provider_operations AS operation
    WHERE operation.tenant_id = NEW.tenant_id
      AND operation.operation_id = NEW.operation_id
    FOR SHARE OF operation;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'provision discovery operation does not exist'
            USING ERRCODE = '55000';
    END IF;

    SELECT claim.state, claim.lease_expires_at, claim.released_at
    INTO claim_state, claim_expires_at, claim_released_at
    FROM provider_operation_execution_claims AS claim
    WHERE claim.tenant_id = NEW.tenant_id
      AND claim.operation_id = NEW.operation_id
      AND claim.head_sequence = NEW.head_sequence
      AND claim.head_receipt_digest = NEW.head_receipt_digest
    FOR UPDATE;
    IF FOUND
       AND claim_state = 'active'
       AND claim_expires_at > clock_timestamp() THEN
        RAISE EXCEPTION 'provision discovery cannot race a live dispatch claim'
            USING ERRCODE = '55000';
    END IF;
    IF FOUND
       AND (
            (
                claim_state IN ('released', 'consumed')
                AND NEW.collected_at < claim_released_at
            )
            OR (
                claim_state = 'active'
                AND claim_expires_at <= clock_timestamp()
                AND NEW.collected_at < claim_expires_at
            )
       ) THEN
        RAISE EXCEPTION 'provision discovery predates the end of dispatch authority'
            USING ERRCODE = '55000';
    END IF;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS provider_provision_discovery_observations_active_claim_guard
    ON provider_provision_discovery_observations;
CREATE TRIGGER provider_provision_discovery_observations_active_claim_guard
BEFORE INSERT ON provider_provision_discovery_observations
FOR EACH ROW EXECUTE FUNCTION provider_provision_discovery_active_claim_guard();

-- Decisions use the same exact claim-row serialization. This also closes a
-- pre-cutover state in which an observation and a reactivated claim may
-- already coexist: an operator decision cannot settle that head while the
-- provider call still has a live execution capability.
CREATE OR REPLACE FUNCTION provider_provision_resolution_active_claim_guard()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path FROM CURRENT
AS $$
DECLARE
    claim_state text;
    claim_expires_at timestamptz;
BEGIN
    IF current_setting('transaction_isolation') IS DISTINCT FROM 'read committed' THEN
        RAISE EXCEPTION 'provision resolution requires read committed serialization'
            USING ERRCODE = '55000';
    END IF;
    PERFORM pg_advisory_xact_lock(hashtext(NEW.tenant_id), hashtext(NEW.operation_id));

    PERFORM 1
    FROM provider_operations AS operation
    WHERE operation.tenant_id = NEW.tenant_id
      AND operation.operation_id = NEW.operation_id
    FOR SHARE OF operation;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'provision resolution operation does not exist'
            USING ERRCODE = '55000';
    END IF;

    SELECT claim.state, claim.lease_expires_at
    INTO claim_state, claim_expires_at
    FROM provider_operation_execution_claims AS claim
    WHERE claim.tenant_id = NEW.tenant_id
      AND claim.operation_id = NEW.operation_id
      AND claim.head_sequence = NEW.expected_head_sequence
      AND claim.head_receipt_digest = NEW.expected_head_receipt_digest
    FOR UPDATE;
    IF FOUND
       AND claim_state = 'active'
       AND claim_expires_at > clock_timestamp() THEN
        RAISE EXCEPTION 'provision resolution cannot race a live dispatch claim'
            USING ERRCODE = '55000';
    END IF;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS provider_provision_resolution_decisions_active_claim_guard
    ON provider_provision_resolution_decisions;
CREATE TRIGGER provider_provision_resolution_decisions_active_claim_guard
BEFORE INSERT ON provider_provision_resolution_decisions
FOR EACH ROW EXECUTE FUNCTION provider_provision_resolution_active_claim_guard();

-- Migration 032 correctly denied every fresh non-decommission claim after
-- teardown, but it also denied a heartbeat for the exact already-authorized
-- in-flight claim. Preserve credential checks for every new authorization
-- while allowing only that same live token/owner/head to retain custody long
-- enough to append its result. The generation guard below independently
-- rejects drift and terminal tombstones.
CREATE OR REPLACE FUNCTION provider_execution_claim_credential_guard()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path FROM CURRENT
AS $$
DECLARE
    db_at timestamptz;
    expected_access text;
    command_provider_id text;
    command_custody_ref text;
    command_custody_hash text;
    command_connection_ref text;
    command_connection_hash text;
    profile_credential_mode text;
    command_operation text;
    command_phase text;
    command_lease_id text;
    command_lease_revision bigint;
    command_server_id text;
    command_resource_generation_id uuid;
    live_lease_revision bigint;
    live_server_id text;
    live_resource_generation_id uuid;
    live_lease_desired_state text;
    live_cancelled_at timestamptz;
    live_server_lease_id text;
    live_server_desired_state text;
    live_server_lifecycle_state text;
    live_server_decommissioned_at timestamptz;
    teardown_requested boolean;
    new_authorization boolean;
    recovering_unsettled_claim boolean := false;
    credential_found boolean := false;
    locked_custody_hash text;
    locked_connection_hash text;
    locked_revoked_at timestamptz;
    locked_valid_from timestamptz;
    locked_valid_until timestamptz;
    requested_ttl interval;
BEGIN
    IF NEW.state <> 'active' THEN
        RETURN NEW;
    END IF;

    IF current_setting('transaction_isolation') IS DISTINCT FROM 'read committed' THEN
        RAISE EXCEPTION 'provider execution authorization requires read committed serialization'
            USING ERRCODE = '55000';
    END IF;
    db_at := clock_timestamp();
    new_authorization := TG_OP = 'INSERT' OR (
        TG_OP = 'UPDATE'
        AND (
            OLD.state IS DISTINCT FROM 'active'
            OR NEW.claim_token_digest IS DISTINCT FROM OLD.claim_token_digest
            OR NEW.claim_owner IS DISTINCT FROM OLD.claim_owner
            OR NEW.claimed_at IS DISTINCT FROM OLD.claimed_at
        )
    );
    recovering_unsettled_claim :=
        TG_OP = 'UPDATE'
        AND (
            OLD.state = 'released'
            OR (
                OLD.state = 'active'
                AND OLD.lease_expires_at <= db_at
            )
        )
        AND new_authorization
        AND OLD.tenant_id = NEW.tenant_id
        AND OLD.operation_id = NEW.operation_id
        AND OLD.head_sequence = NEW.head_sequence
        AND OLD.head_receipt_digest = NEW.head_receipt_digest
        AND OLD.claim_access = 'side_effecting'
        AND NEW.claim_access = OLD.claim_access;

    IF new_authorization
       AND NEW.claim_access = 'side_effecting'
       AND EXISTS (
           SELECT 1
           FROM provider_provision_discovery_observations AS observation
           WHERE observation.tenant_id = NEW.tenant_id
             AND observation.operation_id = NEW.operation_id
             AND observation.head_sequence = NEW.head_sequence
             AND observation.head_receipt_digest = NEW.head_receipt_digest
       ) THEN
        RAISE EXCEPTION 'provider execution claim is frozen by an exact discovery observation'
            USING ERRCODE = '55000';
    END IF;

    SELECT
        provider_expected_execution_claim_access(
            operation.operation,
            operation.provision_dispatch_mode,
            operation.phase
        ),
        operation.command_json #>> '{command,provider_id}',
        operation.command_json #>> '{command,custody_ref}',
        operation.command_json #>> '{command,custody_hash}',
        operation.command_json #>> '{command,connection_ref}',
        operation.command_json #>> '{command,connection_hash}',
        operation.command_json #>> '{execution_profile,credential_mode}',
        operation.operation,
        operation.phase,
        operation.lease_id,
        (operation.command_json #>> '{command,lease_revision}')::bigint,
        operation.command_json #>> '{command,runtime_server_id}',
        (operation.command_json #>> '{command,resource_generation_id}')::uuid
    INTO
        expected_access,
        command_provider_id,
        command_custody_ref,
        command_custody_hash,
        command_connection_ref,
        command_connection_hash,
        profile_credential_mode,
        command_operation,
        command_phase,
        command_lease_id,
        command_lease_revision,
        command_server_id,
        command_resource_generation_id
    FROM provider_operations AS operation
    WHERE operation.tenant_id = NEW.tenant_id
      AND operation.operation_id = NEW.operation_id
      AND operation.head_sequence = NEW.head_sequence
      AND operation.head_receipt_digest = NEW.head_receipt_digest
    FOR SHARE OF operation;

    IF NOT FOUND OR expected_access IS NULL
       OR NEW.claim_access IS DISTINCT FROM expected_access THEN
        RAISE EXCEPTION 'provider execution claim access does not match the current head'
            USING ERRCODE = '55000';
    END IF;

    SELECT
        runtime_lease.lease_revision,
        runtime_lease.server_id,
        runtime_lease.resource_generation_id,
        runtime_lease.desired_state,
        runtime_lease.cancelled_at,
        runtime_server.lease_id,
        runtime_server.desired_state,
        runtime_server.lifecycle_state,
        runtime_server.decommissioned_at
    INTO
        live_lease_revision,
        live_server_id,
        live_resource_generation_id,
        live_lease_desired_state,
        live_cancelled_at,
        live_server_lease_id,
        live_server_desired_state,
        live_server_lifecycle_state,
        live_server_decommissioned_at
    FROM techstack_vm_leases AS runtime_lease
    JOIN servers AS runtime_server
      ON runtime_server.tenant_id = runtime_lease.tenant_id
     AND runtime_server.id = runtime_lease.server_id
    WHERE runtime_lease.tenant_id = NEW.tenant_id
      AND runtime_lease.id = command_lease_id
    FOR SHARE OF runtime_lease, runtime_server;
    IF NOT FOUND
       OR live_lease_revision IS DISTINCT FROM command_lease_revision
       OR live_server_id IS DISTINCT FROM command_server_id
       OR live_resource_generation_id IS DISTINCT FROM command_resource_generation_id
       OR live_server_lease_id IS DISTINCT FROM command_lease_id THEN
        RAISE EXCEPTION 'provider execution claim is stale against the live typed runtime lease and server'
            USING ERRCODE = '55000';
    END IF;
    IF live_server_lifecycle_state IS NOT DISTINCT FROM 'decommissioned'
       OR live_server_decommissioned_at IS NOT NULL THEN
        RAISE EXCEPTION 'provider execution claim is fenced by a terminal server decommission tombstone'
            USING ERRCODE = '55000';
    END IF;
    teardown_requested := live_cancelled_at IS NOT NULL
        OR live_lease_desired_state IS NOT DISTINCT FROM 'absent'
        OR live_server_desired_state IS NOT DISTINCT FROM 'absent'
        OR live_server_lifecycle_state IS NOT DISTINCT FROM 'decommissioning';
    IF command_operation = 'decommission' THEN
        IF NOT teardown_requested THEN
            RAISE EXCEPTION 'provider decommission claim has no teardown intent'
                USING ERRCODE = '55000';
        END IF;
    ELSIF teardown_requested
          AND new_authorization
          AND NOT (
              command_operation = 'provision'
              AND command_phase = 'resources_bound'
              AND NEW.claim_access = 'read_only'
          )
          AND NOT recovering_unsettled_claim THEN
        RAISE EXCEPTION 'provider execution claim is fenced by cancellation or teardown intent'
            USING ERRCODE = '55000';
    END IF;

    IF expected_access = 'side_effecting' AND new_authorization THEN
        SELECT
            credential.custody_hash,
            credential.connection_hash,
            credential.revoked_at,
            credential.valid_from,
            credential.valid_until
        INTO
            locked_custody_hash,
            locked_connection_hash,
            locked_revoked_at,
            locked_valid_from,
            locked_valid_until
        FROM provider_credential_handles AS credential
        WHERE credential.tenant_id = NEW.tenant_id
          AND credential.provider_id = command_provider_id
          AND credential.credential_mode = profile_credential_mode
          AND credential.custody_ref = command_custody_ref
          AND credential.connection_ref = command_connection_ref
        FOR SHARE OF credential;
        credential_found := FOUND;
    END IF;

    IF TG_OP = 'INSERT' OR (TG_OP = 'UPDATE' AND new_authorization) THEN
        requested_ttl := NEW.lease_expires_at - NEW.claimed_at;
        IF requested_ttl IS NULL
           OR requested_ttl <= interval '0 seconds'
           OR requested_ttl > interval '15 minutes' THEN
            RAISE EXCEPTION 'provider execution claim acquisition lease is outside the allowed range'
                USING ERRCODE = '55000';
        END IF;
        NEW.claimed_at := db_at;
        NEW.lease_expires_at := db_at + requested_ttl;
    ELSIF TG_OP = 'UPDATE' AND OLD.state = 'active' THEN
        IF OLD.lease_expires_at <= db_at THEN
            RAISE EXCEPTION 'expired provider execution claim cannot be heartbeat-renewed'
                USING ERRCODE = '55000';
        END IF;
        IF NEW.lease_expires_at <= OLD.lease_expires_at
           OR NEW.lease_expires_at <= db_at
           OR NEW.lease_expires_at > db_at + interval '15 minutes' THEN
            RAISE EXCEPTION 'provider execution claim heartbeat lease is outside the allowed range'
                USING ERRCODE = '55000';
        END IF;
    END IF;

    IF expected_access = 'side_effecting' AND new_authorization THEN
        IF NOT credential_found
           OR locked_custody_hash IS DISTINCT FROM command_custody_hash
           OR locked_connection_hash IS DISTINCT FROM command_connection_hash
           OR locked_revoked_at IS NOT NULL
           OR locked_valid_from IS NULL
           OR locked_valid_until IS NULL
           OR locked_valid_from > db_at
           OR locked_valid_until <= db_at THEN
            RAISE EXCEPTION 'side-effecting provider claim credential is not authorized'
                USING ERRCODE = '55000';
        END IF;
    END IF;
    RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION provider_execution_claim_runtime_generation_guard()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path FROM CURRENT
AS $$
DECLARE
    operation_kind text;
    operation_phase text;
    operation_lease_id text;
    operation_server_id text;
    operation_provider_id text;
    operation_server_generation bigint;
    operation_resource_generation_id uuid;
    operation_lease_revision bigint;
    operation_targets jsonb;
    live_lease_revision bigint;
    live_lease_server_id text;
    live_resource_generation_id uuid;
    live_lease_desired_state text;
    live_cancelled_at timestamptz;
    live_server_lease_id text;
    live_server_generation bigint;
    live_server_provider_ref text;
    live_server_desired_state text;
    live_server_lifecycle_state text;
    live_server_decommissioned_at timestamptz;
    teardown_requested boolean;
    continuing_claim boolean := false;
    recovering_unsettled_claim boolean := false;
    db_at timestamptz := clock_timestamp();
    target_count integer;
    target_distinct_count integer;
    authoritative_count integer;
BEGIN
    IF NEW.state IS DISTINCT FROM 'active' THEN
        RETURN NEW;
    END IF;
    continuing_claim :=
        TG_OP = 'UPDATE'
        AND OLD.state = 'active'
        AND OLD.operation_id = NEW.operation_id
        AND OLD.head_sequence = NEW.head_sequence
        AND OLD.head_receipt_digest = NEW.head_receipt_digest
        AND OLD.claim_token_digest = NEW.claim_token_digest
        AND OLD.claim_owner = NEW.claim_owner
        AND OLD.claim_access = NEW.claim_access;
    recovering_unsettled_claim :=
        TG_OP = 'UPDATE'
        AND (
            OLD.state = 'released'
            OR (
                OLD.state = 'active'
                AND OLD.lease_expires_at <= db_at
            )
        )
        AND OLD.operation_id = NEW.operation_id
        AND OLD.head_sequence = NEW.head_sequence
        AND OLD.head_receipt_digest = NEW.head_receipt_digest
        AND OLD.claim_access = 'side_effecting'
        AND NEW.claim_access = OLD.claim_access;

    IF (
        TG_OP = 'INSERT'
        OR (
            TG_OP = 'UPDATE'
            AND (
                OLD.state IS DISTINCT FROM 'active'
                OR NEW.claim_token_digest IS DISTINCT FROM OLD.claim_token_digest
                OR NEW.claim_owner IS DISTINCT FROM OLD.claim_owner
                OR NEW.claimed_at IS DISTINCT FROM OLD.claimed_at
            )
        )
    )
       AND NEW.claim_access = 'side_effecting'
       AND EXISTS (
           SELECT 1
           FROM provider_provision_discovery_observations AS observation
           WHERE observation.tenant_id = NEW.tenant_id
             AND observation.operation_id = NEW.operation_id
             AND observation.head_sequence = NEW.head_sequence
             AND observation.head_receipt_digest = NEW.head_receipt_digest
       ) THEN
        RAISE EXCEPTION 'provider execution claim is frozen by an exact discovery observation'
            USING ERRCODE = '55000';
    END IF;

    SELECT
        operation.operation,
        operation.phase,
        operation.lease_id,
        operation.command_json #>> '{command,runtime_server_id}',
        operation.command_json #>> '{command,provider_id}',
        NULLIF(
            operation.command_json->>'runtime_server_generation',
            ''
        )::bigint,
        (
            operation.command_json
                #>> '{command,resource_generation_id}'
        )::uuid,
        (
            operation.command_json
                #>> '{command,lease_revision}'
        )::bigint,
        operation.command_json #> '{command,targets}'
    INTO
        operation_kind,
        operation_phase,
        operation_lease_id,
        operation_server_id,
        operation_provider_id,
        operation_server_generation,
        operation_resource_generation_id,
        operation_lease_revision,
        operation_targets
    FROM provider_operations AS operation
    WHERE operation.tenant_id = NEW.tenant_id
      AND operation.operation_id = NEW.operation_id;
    IF NOT FOUND
       OR operation_server_generation IS NULL
       OR operation_server_generation < 1 THEN
        RAISE EXCEPTION 'provider execution claim has no immutable runtime generation'
            USING ERRCODE = '55000';
    END IF;

    PERFORM pg_advisory_xact_lock(
        hashtext(NEW.tenant_id),
        hashtext(
            operation_lease_id
            || ':'
            || operation_resource_generation_id::text
        )
    );

    SELECT
        lease.lease_revision,
        lease.server_id,
        lease.resource_generation_id,
        lease.desired_state,
        lease.cancelled_at,
        server.lease_id,
        server.generation,
        server.provider_ref,
        server.desired_state,
        server.lifecycle_state,
        server.decommissioned_at
    INTO
        live_lease_revision,
        live_lease_server_id,
        live_resource_generation_id,
        live_lease_desired_state,
        live_cancelled_at,
        live_server_lease_id,
        live_server_generation,
        live_server_provider_ref,
        live_server_desired_state,
        live_server_lifecycle_state,
        live_server_decommissioned_at
    FROM techstack_vm_leases AS lease
    JOIN servers AS server
      ON server.tenant_id = lease.tenant_id
     AND server.id = lease.server_id
    WHERE lease.tenant_id = NEW.tenant_id
      AND lease.id = operation_lease_id
    FOR SHARE OF lease, server;

    IF NOT FOUND
       OR live_lease_revision IS DISTINCT FROM operation_lease_revision
       OR live_lease_server_id IS DISTINCT FROM operation_server_id
       OR live_resource_generation_id IS DISTINCT FROM
            operation_resource_generation_id
       OR live_server_lease_id IS DISTINCT FROM operation_lease_id
       OR live_server_generation < operation_server_generation
       OR (
            live_server_generation IS DISTINCT FROM
                operation_server_generation
            AND NEW.claim_access IS DISTINCT FROM 'read_only'
            AND NOT continuing_claim
            AND NOT recovering_unsettled_claim
       )
       OR NULLIF(LOWER(BTRIM(operation_provider_id)), '') IS NULL
       OR NULLIF(LOWER(BTRIM(live_server_provider_ref)), '')
            IS DISTINCT FROM NULLIF(LOWER(BTRIM(operation_provider_id)), '')
       OR live_server_lifecycle_state = 'decommissioned'
       OR live_server_decommissioned_at IS NOT NULL THEN
        RAISE EXCEPTION 'provider execution claim is stale against the runtime generation'
            USING ERRCODE = '55000';
    END IF;

    teardown_requested := live_cancelled_at IS NOT NULL
        OR live_lease_desired_state = 'absent'
        OR live_server_desired_state = 'absent'
        OR live_server_lifecycle_state = 'decommissioning';
    IF operation_kind IN ('reconcile', 'decommission') THEN
        IF operation_kind = 'decommission' AND NOT teardown_requested THEN
            RAISE EXCEPTION 'provider decommission claim has no teardown intent'
                USING ERRCODE = '55000';
        END IF;
        IF operation_kind = 'reconcile'
           AND teardown_requested
           AND NOT continuing_claim
           AND NOT recovering_unsettled_claim THEN
            RAISE EXCEPTION 'new provider execution claim is fenced by teardown intent'
                USING ERRCODE = '55000';
        END IF;
        IF jsonb_typeof(operation_targets) IS DISTINCT FROM 'array' THEN
            RAISE EXCEPTION 'provider mutation claim has no exact target graph'
                USING ERRCODE = '55000';
        END IF;
        SELECT
            COUNT(*)::integer,
            COUNT(DISTINCT target->>'binding_id')::integer
        INTO target_count, target_distinct_count
        FROM jsonb_array_elements(operation_targets) AS target;

        -- Guard enrollment may advance the numeric RuntimeServer generation
        -- while the exact lease UUID continues to own the same provider
        -- graph. Resolve mutation authority across historical numeric pins,
        -- but never across tenant/server/lease/UUID boundaries.
        SELECT COUNT(*)::integer
        INTO authoritative_count
        FROM (
            SELECT DISTINCT
                resource.binding_id,
                resource.kind,
                resource.native_ref,
                COALESCE(resource.parent_binding_id, '') AS parent_binding_id,
                resource.ownership_hash,
                resource.disposition
            FROM server_provider_resource_bindings AS binding
            JOIN provider_operation_resources AS resource
              ON resource.tenant_id = binding.tenant_id
             AND resource.operation_id = binding.operation_id
             AND resource.binding_id = binding.binding_id
            JOIN provider_operations AS source_operation
              ON source_operation.tenant_id = binding.tenant_id
             AND source_operation.operation_id = binding.operation_id
            WHERE binding.tenant_id = NEW.tenant_id
              AND binding.server_id = operation_server_id
              AND binding.lease_id = operation_lease_id
              AND binding.resource_generation_id =
                    operation_resource_generation_id
              AND source_operation.operation = 'provision'
              AND (
                  (
                      operation_kind = 'reconcile'
                      AND source_operation.status = 'succeeded'
                      AND source_operation.phase = 'present'
                  )
                  OR (
                      operation_kind = 'decommission'
                      AND (
                          (
                              source_operation.status = 'succeeded'
                              AND source_operation.phase = 'present'
                          )
                          OR (
                              source_operation.status = 'failed'
                              AND source_operation.phase = 'failed'
                          )
                      )
                  )
              )
        ) AS authoritative;

        IF authoritative_count < 1
           OR target_count IS DISTINCT FROM authoritative_count
           OR target_distinct_count IS DISTINCT FROM target_count
           OR EXISTS (
                WITH authoritative AS (
                    SELECT DISTINCT
                        resource.binding_id,
                        resource.kind,
                        resource.native_ref,
                        COALESCE(resource.parent_binding_id, '') AS parent_binding_id,
                        resource.ownership_hash,
                        resource.disposition
                    FROM server_provider_resource_bindings AS binding
                    JOIN provider_operation_resources AS resource
                      ON resource.tenant_id = binding.tenant_id
                     AND resource.operation_id = binding.operation_id
                     AND resource.binding_id = binding.binding_id
                    JOIN provider_operations AS source_operation
                      ON source_operation.tenant_id = binding.tenant_id
                     AND source_operation.operation_id = binding.operation_id
                    WHERE binding.tenant_id = NEW.tenant_id
                      AND binding.server_id = operation_server_id
                      AND binding.lease_id = operation_lease_id
                      AND binding.resource_generation_id =
                            operation_resource_generation_id
                      AND source_operation.operation = 'provision'
                      AND (
                          (
                              operation_kind = 'reconcile'
                              AND source_operation.status = 'succeeded'
                              AND source_operation.phase = 'present'
                          )
                          OR (
                              operation_kind = 'decommission'
                              AND (
                                  (
                                      source_operation.status = 'succeeded'
                                      AND source_operation.phase = 'present'
                                  )
                                  OR (
                                      source_operation.status = 'failed'
                                      AND source_operation.phase = 'failed'
                                  )
                              )
                          )
                      )
                ),
                targets AS (
                    SELECT
                        target->>'binding_id' AS binding_id,
                        target->>'kind' AS kind,
                        target->>'native_ref' AS native_ref,
                        COALESCE(target->>'parent_binding_id', '') AS parent_binding_id,
                        target->>'ownership_hash' AS ownership_hash,
                        target->>'disposition' AS disposition
                    FROM jsonb_array_elements(operation_targets) AS target
                )
                SELECT 1
                FROM (
                    (
                        SELECT binding_id, kind, native_ref, parent_binding_id,
                               ownership_hash, disposition
                        FROM authoritative
                        EXCEPT
                        SELECT binding_id, kind, native_ref, parent_binding_id,
                               ownership_hash, disposition
                        FROM targets
                    )
                    UNION ALL
                    (
                        SELECT binding_id, kind, native_ref, parent_binding_id,
                               ownership_hash, disposition
                        FROM targets
                        EXCEPT
                        SELECT binding_id, kind, native_ref, parent_binding_id,
                               ownership_hash, disposition
                        FROM authoritative
                    )
                ) AS difference
           ) THEN
            RAISE EXCEPTION 'provider mutation claim targets do not exactly match prior provision custody'
                USING ERRCODE = '55000';
        END IF;
        IF EXISTS (
            SELECT 1
            FROM provider_operation_execution_claims AS claim
            JOIN provider_operations AS claimed_operation
              ON claimed_operation.tenant_id = claim.tenant_id
             AND claimed_operation.operation_id = claim.operation_id
            WHERE claimed_operation.tenant_id = NEW.tenant_id
              AND claimed_operation.lease_id = operation_lease_id
              AND claimed_operation.command_json
                    #>> '{command,resource_generation_id}' =
                    operation_resource_generation_id::text
              AND claim.claim_access = 'side_effecting'
              AND claim.state IN ('active', 'released')
              AND claimed_operation.head_sequence = claim.head_sequence
              AND claimed_operation.head_receipt_digest =
                    claim.head_receipt_digest
              AND NOT EXISTS (
                  SELECT 1
                  FROM provider_provision_resolution_decisions AS decision
                  WHERE decision.tenant_id = claim.tenant_id
                    AND decision.operation_id = claim.operation_id
                    AND decision.expected_head_sequence =
                        claim.head_sequence
                    AND decision.expected_head_receipt_digest =
                        claim.head_receipt_digest
                    AND decision.outcome = 'no_candidate_observed'
              )
              AND NOT (
                  claim.operation_id = NEW.operation_id
                  AND claim.head_sequence = NEW.head_sequence
                  AND claim.head_receipt_digest = NEW.head_receipt_digest
              )
        ) THEN
            RAISE EXCEPTION 'provider mutation claim conflicts with unsettled generation side-effect custody'
                USING ERRCODE = '55000';
        END IF;
    ELSIF teardown_requested
          AND NOT (
              operation_kind = 'provision'
              AND operation_phase = 'resources_bound'
              AND NEW.claim_access = 'read_only'
          )
          AND NOT continuing_claim
          AND NOT recovering_unsettled_claim THEN
        RAISE EXCEPTION 'new provider execution claim is fenced by teardown intent'
            USING ERRCODE = '55000';
    END IF;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS provider_execution_claims_runtime_generation_guard
    ON provider_operation_execution_claims;
CREATE TRIGGER provider_execution_claims_runtime_generation_guard
BEFORE INSERT OR UPDATE ON provider_operation_execution_claims
FOR EACH ROW EXECUTE FUNCTION provider_execution_claim_runtime_generation_guard();

-- Resource bindings validate against the immutable operation pin rather than
-- the mutable current server head. This retains a late provider result for its
-- admitted historical generation without authorizing a newer server head.
CREATE OR REPLACE FUNCTION server_provider_resource_binding_guard()
RETURNS trigger
LANGUAGE plpgsql
SET search_path FROM CURRENT
AS $$
DECLARE
    operation_lease_id text;
    operation_server_id text;
    operation_server_generation bigint;
    operation_resource_generation_id uuid;
BEGIN
    IF TG_OP <> 'INSERT' THEN
        RAISE EXCEPTION 'server provider resource bindings are immutable'
            USING ERRCODE = '55000';
    END IF;

    SELECT
        operation.lease_id,
        operation.command_json #>> '{command,runtime_server_id}',
        NULLIF(operation.command_json->>'runtime_server_generation', '')::bigint,
        (operation.command_json #>> '{command,resource_generation_id}')::uuid
    INTO
        operation_lease_id,
        operation_server_id,
        operation_server_generation,
        operation_resource_generation_id
    FROM provider_operations AS operation
    WHERE operation.tenant_id = NEW.tenant_id
      AND operation.operation_id = NEW.operation_id;

    IF operation_lease_id IS DISTINCT FROM NEW.lease_id
       OR operation_server_id IS DISTINCT FROM NEW.server_id
       OR operation_server_generation IS DISTINCT FROM NEW.server_generation
       OR operation_resource_generation_id IS DISTINCT FROM NEW.resource_generation_id THEN
        RAISE EXCEPTION 'provider resource binding does not match its immutable operation generation'
            USING ERRCODE = '23503';
    END IF;
    RETURN NEW;
END;
$$;

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
        tenant_id,
        server_id,
        server_generation,
        lease_id,
        resource_generation_id,
        operation_id,
        binding_id,
        bound_at
    )
    SELECT
        NEW.tenant_id,
        NEW.id,
        NEW.generation,
        NEW.lease_id,
        (operation.command_json #>> '{command,resource_generation_id}')::uuid,
        operation.operation_id,
        resource.binding_id,
        clock_timestamp()
    FROM provider_operations AS operation
    JOIN provider_operation_resources AS resource
      ON resource.tenant_id = operation.tenant_id
     AND resource.operation_id = operation.operation_id
    WHERE operation.tenant_id = NEW.tenant_id
      AND operation.lease_id = NEW.lease_id
      AND NULLIF(
            operation.command_json->>'runtime_server_generation',
            ''
          )::bigint = NEW.generation
    ON CONFLICT (tenant_id, operation_id, binding_id) DO NOTHING;
    RETURN NEW;
END;
$$;

-- Operator Reconcile normally binds one guarded pending/accepted AMO head.
-- A handle-free failed/failed receipt is a second, narrower settlement case:
-- the immutable observation remains bound to the original dispatch guard,
-- while the operator decision binds the exact terminal child receipt. Only a
-- certified zero-candidate result is admissible for that terminal branch.
CREATE OR REPLACE FUNCTION provider_provision_discovery_validate_insert()
RETURNS trigger
LANGUAGE plpgsql
SET search_path FROM CURRENT
AS $$
DECLARE
    operation_kind text;
    operation_status text;
    operation_phase text;
    operation_mode text;
    operation_lease_id text;
    operation_lease_revision bigint;
    operation_server_id text;
    operation_generation_id uuid;
    operation_manifest_hash text;
    operation_head_sequence bigint;
    operation_head_digest text;
    current_receipt_previous_digest text;
    operation_resource_count integer;
    guard_head_sequence bigint;
    guard_head_digest text;
    guard_prepared_digest text;
    guard_credential_hash text;
    guard_scope_hash text;
    guard_correlation_hash text;
    guard_manifest_hash text;
    guard_origin text;
    guard_guarded_at timestamptz;
    live_lease_revision bigint;
    live_server_id text;
    live_generation_id uuid;
    live_desired_state text;
    live_cancelled_at timestamptz;
    server_lease_id text;
    server_desired_state text;
    server_lifecycle_state text;
    server_decommissioned_at timestamptz;
    current_resolution_revision bigint;
    terminal_resolution boolean;
    terminal_failure boolean;
BEGIN
    PERFORM pg_advisory_xact_lock(hashtext(NEW.tenant_id), hashtext(NEW.operation_id));

    SELECT
        operation.operation,
        operation.status,
        operation.phase,
        operation.provision_dispatch_mode,
        operation.lease_id,
        (operation.command_json #>> '{command,lease_revision}')::bigint,
        operation.command_json #>> '{command,runtime_server_id}',
        (operation.command_json #>> '{command,resource_generation_id}')::uuid,
        operation.command_json #>> '{execution_profile,adapter_manifest_hash}',
        operation.head_sequence,
        operation.head_receipt_digest,
        current_receipt.previous_receipt_digest,
        (
            SELECT count(*)::integer
            FROM provider_operation_resources AS resource
            WHERE resource.tenant_id = operation.tenant_id
              AND resource.operation_id = operation.operation_id
        ),
        guard.head_sequence,
        guard.head_receipt_digest,
        guard.prepared_request_digest,
        guard.credential_version_hash,
        guard.provider_scope_hash,
        guard.correlation_hash,
        guard.adapter_manifest_hash,
        guard.guard_origin,
        guard.guarded_at
    INTO
        operation_kind,
        operation_status,
        operation_phase,
        operation_mode,
        operation_lease_id,
        operation_lease_revision,
        operation_server_id,
        operation_generation_id,
        operation_manifest_hash,
        operation_head_sequence,
        operation_head_digest,
        current_receipt_previous_digest,
        operation_resource_count,
        guard_head_sequence,
        guard_head_digest,
        guard_prepared_digest,
        guard_credential_hash,
        guard_scope_hash,
        guard_correlation_hash,
        guard_manifest_hash,
        guard_origin,
        guard_guarded_at
    FROM provider_operations AS operation
    JOIN provider_operation_receipts AS current_receipt
      ON current_receipt.tenant_id = operation.tenant_id
     AND current_receipt.operation_id = operation.operation_id
     AND current_receipt.sequence = operation.head_sequence
     AND current_receipt.receipt_digest = operation.head_receipt_digest
    JOIN provider_provision_dispatch_guards AS guard
      ON guard.tenant_id = operation.tenant_id
     AND guard.operation_id = operation.operation_id
    WHERE operation.tenant_id = NEW.tenant_id
      AND operation.operation_id = NEW.operation_id
    FOR SHARE OF operation, current_receipt, guard;

    terminal_failure :=
        operation_status = 'failed'
        AND operation_phase = 'failed'
        AND operation_resource_count = 0
        AND operation_head_sequence = guard_head_sequence + 1
        AND current_receipt_previous_digest = guard_head_digest;

    IF NOT FOUND
       OR operation_kind IS DISTINCT FROM 'provision'
       OR operation_mode IS DISTINCT FROM 'at_most_once_dispatch_manual_reconcile'
       OR guard_origin IS DISTINCT FROM 'first_claim'
       OR guard_prepared_digest IS NULL
       OR NOT (
            (
                operation_status = 'pending'
                AND operation_phase = 'accepted'
                AND operation_head_sequence = guard_head_sequence
                AND operation_head_digest = guard_head_digest
            )
            OR terminal_failure
       )
       OR (terminal_failure AND NEW.candidate_count <> 0) THEN
        RAISE EXCEPTION 'provision discovery requires the exact guarded AMO accepted head or its zero-resource terminal failure'
            USING ERRCODE = '55000';
    END IF;

    SELECT
        runtime_lease.lease_revision,
        runtime_lease.server_id,
        runtime_lease.resource_generation_id,
        runtime_lease.desired_state,
        runtime_lease.cancelled_at,
        runtime_server.lease_id,
        runtime_server.desired_state,
        runtime_server.lifecycle_state,
        runtime_server.decommissioned_at
    INTO
        live_lease_revision,
        live_server_id,
        live_generation_id,
        live_desired_state,
        live_cancelled_at,
        server_lease_id,
        server_desired_state,
        server_lifecycle_state,
        server_decommissioned_at
    FROM techstack_vm_leases AS runtime_lease
    JOIN servers AS runtime_server
      ON runtime_server.tenant_id = runtime_lease.tenant_id
     AND runtime_server.id = runtime_lease.server_id
    WHERE runtime_lease.tenant_id = NEW.tenant_id
      AND runtime_lease.id = operation_lease_id
    FOR SHARE OF runtime_lease, runtime_server;

    IF NOT FOUND
       OR live_lease_revision IS DISTINCT FROM operation_lease_revision
       OR live_server_id IS DISTINCT FROM operation_server_id
       OR live_generation_id IS DISTINCT FROM operation_generation_id
       OR server_lease_id IS DISTINCT FROM operation_lease_id
       OR server_decommissioned_at IS NOT NULL
       OR (
            NOT terminal_failure
            AND (
                live_cancelled_at IS NOT NULL
                OR live_desired_state NOT IN ('running', 'stopped')
                OR server_desired_state = 'absent'
                OR server_lifecycle_state IN ('decommissioning', 'decommissioned')
            )
       )
       OR (
            terminal_failure
            AND server_lifecycle_state NOT IN (
                'planned', 'provisioning', 'failed', 'decommissioning'
            )
       ) THEN
        RAISE EXCEPTION 'provision discovery is stale against live lease or server custody'
            USING ERRCODE = '55000';
    END IF;

    SELECT
        coalesce(max(decision.resolution_revision), 0),
        coalesce(bool_or(decision.outcome IN (
            'adopted_exact_candidate', 'multiple_candidates_quarantined'
        )), false)
    INTO current_resolution_revision, terminal_resolution
    FROM provider_provision_resolution_decisions AS decision
    WHERE decision.tenant_id = NEW.tenant_id
      AND decision.operation_id = NEW.operation_id;

    IF terminal_resolution
       OR NEW.observed_resolution_revision IS DISTINCT FROM current_resolution_revision THEN
        RAISE EXCEPTION 'provision discovery resolution revision is stale or terminal'
            USING ERRCODE = '40001';
    END IF;

    PERFORM 1
    FROM provider_operation_execution_claims AS claim
    WHERE claim.tenant_id = NEW.tenant_id
      AND claim.operation_id = NEW.operation_id
      AND claim.head_sequence = guard_head_sequence
      AND claim.head_receipt_digest = guard_head_digest
      AND claim.state = 'active'
      AND claim.lease_expires_at > clock_timestamp();
    IF FOUND THEN
        RAISE EXCEPTION 'provision discovery cannot race a live dispatch claim'
            USING ERRCODE = '55000';
    END IF;

    IF NEW.lease_id IS DISTINCT FROM operation_lease_id
       OR NEW.lease_revision IS DISTINCT FROM operation_lease_revision
       OR NEW.server_id IS DISTINCT FROM operation_server_id
       OR NEW.resource_generation_id IS DISTINCT FROM operation_generation_id
       OR NEW.head_sequence IS DISTINCT FROM guard_head_sequence
       OR NEW.head_receipt_digest IS DISTINCT FROM guard_head_digest
       OR NEW.adapter_manifest_hash IS DISTINCT FROM operation_manifest_hash
       OR NEW.adapter_manifest_hash IS DISTINCT FROM guard_manifest_hash
       OR NEW.prepared_request_digest IS DISTINCT FROM guard_prepared_digest
       OR NEW.credential_version_hash IS DISTINCT FROM guard_credential_hash
       OR NEW.provider_scope_hash IS DISTINCT FROM guard_scope_hash
       OR NEW.correlation_hash IS DISTINCT FROM guard_correlation_hash
       OR NEW.guarded_at IS DISTINCT FROM guard_guarded_at
       OR NEW.collected_at < guard_guarded_at
       OR NEW.collected_at > clock_timestamp() THEN
        RAISE EXCEPTION 'provision discovery does not bind immutable dispatch custody'
            USING ERRCODE = '23514';
    END IF;

    IF jsonb_typeof(NEW.candidate_graphs_json) IS DISTINCT FROM 'array'
       OR jsonb_array_length(NEW.candidate_graphs_json) IS DISTINCT FROM NEW.candidate_count THEN
        RAISE EXCEPTION 'provision discovery candidate count does not match its graph set'
            USING ERRCODE = '23514';
    END IF;

    NEW.recorded_at := clock_timestamp();
    RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION provider_provision_resolution_validate_insert()
RETURNS trigger
LANGUAGE plpgsql
SET search_path FROM CURRENT
AS $$
DECLARE
    observation provider_provision_discovery_observations%ROWTYPE;
    operation_kind text;
    operation_status text;
    operation_phase text;
    operation_mode text;
    operation_head_sequence bigint;
    operation_head_digest text;
    current_receipt_previous_digest text;
    operation_resource_count integer;
    live_lease_revision bigint;
    live_server_id text;
    live_generation_id uuid;
    live_desired_state text;
    live_cancelled_at timestamptz;
    server_lease_id text;
    server_desired_state text;
    server_lifecycle_state text;
    server_decommissioned_at timestamptz;
    current_resolution_revision bigint;
    terminal_resolution boolean;
    terminal_failure boolean;
    teardown_requested boolean;
    derived_outcome text;
    bound_token_digest text := CASE
        WHEN nullif(current_setting('app.provider_resolution_token', true), '') IS NULL THEN NULL
        ELSE encode(sha256(convert_to(current_setting('app.provider_resolution_token', true), 'UTF8')), 'hex')
    END;
BEGIN
    PERFORM pg_advisory_xact_lock(hashtext(NEW.tenant_id), hashtext(NEW.operation_id));

    SELECT * INTO observation
    FROM provider_provision_discovery_observations
    WHERE tenant_id = NEW.tenant_id
      AND operation_id = NEW.operation_id
      AND observation_id = NEW.observation_id
      AND snapshot_digest = NEW.observation_snapshot_digest
    FOR SHARE;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'provision resolution requires its exact immutable discovery observation'
            USING ERRCODE = '55000';
    END IF;

    SELECT
        operation.operation,
        operation.status,
        operation.phase,
        operation.provision_dispatch_mode,
        operation.head_sequence,
        operation.head_receipt_digest,
        current_receipt.previous_receipt_digest,
        (
            SELECT count(*)::integer
            FROM provider_operation_resources AS resource
            WHERE resource.tenant_id = operation.tenant_id
              AND resource.operation_id = operation.operation_id
        )
    INTO
        operation_kind,
        operation_status,
        operation_phase,
        operation_mode,
        operation_head_sequence,
        operation_head_digest,
        current_receipt_previous_digest,
        operation_resource_count
    FROM provider_operations AS operation
    JOIN provider_operation_receipts AS current_receipt
      ON current_receipt.tenant_id = operation.tenant_id
     AND current_receipt.operation_id = operation.operation_id
     AND current_receipt.sequence = operation.head_sequence
     AND current_receipt.receipt_digest = operation.head_receipt_digest
    WHERE operation.tenant_id = NEW.tenant_id
      AND operation.operation_id = NEW.operation_id
    FOR SHARE OF operation, current_receipt;

    terminal_failure :=
        operation_status = 'failed'
        AND operation_phase = 'failed'
        AND operation_resource_count = 0
        AND operation_head_sequence = observation.head_sequence + 1
        AND current_receipt_previous_digest = observation.head_receipt_digest;

    IF NOT FOUND
       OR operation_kind IS DISTINCT FROM 'provision'
       OR operation_mode IS DISTINCT FROM 'at_most_once_dispatch_manual_reconcile'
       OR NEW.expected_head_sequence IS DISTINCT FROM operation_head_sequence
       OR NEW.expected_head_receipt_digest IS DISTINCT FROM operation_head_digest
       OR NOT (
            (
                operation_status = 'pending'
                AND operation_phase = 'accepted'
                AND operation_head_sequence = observation.head_sequence
                AND operation_head_digest = observation.head_receipt_digest
            )
            OR terminal_failure
       ) THEN
        RAISE EXCEPTION 'provision resolution expected head is stale or not an exact AMO settlement head'
            USING ERRCODE = '40001';
    END IF;

    SELECT
        runtime_lease.lease_revision,
        runtime_lease.server_id,
        runtime_lease.resource_generation_id,
        runtime_lease.desired_state,
        runtime_lease.cancelled_at,
        runtime_server.lease_id,
        runtime_server.desired_state,
        runtime_server.lifecycle_state,
        runtime_server.decommissioned_at
    INTO
        live_lease_revision,
        live_server_id,
        live_generation_id,
        live_desired_state,
        live_cancelled_at,
        server_lease_id,
        server_desired_state,
        server_lifecycle_state,
        server_decommissioned_at
    FROM techstack_vm_leases AS runtime_lease
    JOIN servers AS runtime_server
      ON runtime_server.tenant_id = runtime_lease.tenant_id
     AND runtime_server.id = runtime_lease.server_id
    WHERE runtime_lease.tenant_id = NEW.tenant_id
      AND runtime_lease.id = observation.lease_id
    FOR SHARE OF runtime_lease, runtime_server;

    teardown_requested :=
        live_cancelled_at IS NOT NULL
        OR live_desired_state = 'absent'
        OR server_desired_state = 'absent'
        OR server_lifecycle_state = 'decommissioning';
    IF NOT FOUND
       OR live_lease_revision IS DISTINCT FROM observation.lease_revision
       OR live_server_id IS DISTINCT FROM observation.server_id
       OR live_generation_id IS DISTINCT FROM observation.resource_generation_id
       OR server_lease_id IS DISTINCT FROM observation.lease_id
       OR server_decommissioned_at IS NOT NULL
       OR (
            NOT terminal_failure
            AND (
                teardown_requested
                OR live_desired_state NOT IN ('running', 'stopped')
                OR server_lifecycle_state = 'decommissioned'
            )
       )
       OR (
             terminal_failure
             AND (
                 NOT teardown_requested
                 OR server_lifecycle_state NOT IN (
                     'planned', 'provisioning', 'failed', 'decommissioning'
                 )
             )
        ) THEN
        RAISE EXCEPTION 'provision resolution is stale against live lease or server custody'
            USING ERRCODE = '55000';
    END IF;

    SELECT
        coalesce(max(decision.resolution_revision), 0),
        coalesce(bool_or(decision.outcome IN (
            'adopted_exact_candidate', 'multiple_candidates_quarantined'
        )), false)
    INTO current_resolution_revision, terminal_resolution
    FROM provider_provision_resolution_decisions AS decision
    WHERE decision.tenant_id = NEW.tenant_id
      AND decision.operation_id = NEW.operation_id;

    IF terminal_resolution
       OR observation.observed_resolution_revision IS DISTINCT FROM current_resolution_revision
       OR NEW.resolution_revision IS DISTINCT FROM current_resolution_revision + 1 THEN
        RAISE EXCEPTION 'provision resolution revision is stale or terminal'
            USING ERRCODE = '40001';
    END IF;

    PERFORM 1
    FROM provider_operation_execution_claims AS claim
    WHERE claim.tenant_id = NEW.tenant_id
      AND claim.operation_id = NEW.operation_id
      AND claim.head_sequence = observation.head_sequence
      AND claim.head_receipt_digest = observation.head_receipt_digest
      AND claim.state = 'active'
      AND claim.lease_expires_at > clock_timestamp();
    IF FOUND THEN
        RAISE EXCEPTION 'provision resolution cannot race a live dispatch claim'
            USING ERRCODE = '55000';
    END IF;

    derived_outcome := CASE
        WHEN observation.candidate_count = 0 THEN 'no_candidate_observed'
        WHEN observation.candidate_count = 1 THEN 'adopted_exact_candidate'
        ELSE 'multiple_candidates_quarantined'
    END;
    IF NEW.outcome IS DISTINCT FROM derived_outcome
       OR (terminal_failure AND derived_outcome <> 'no_candidate_observed') THEN
        RAISE EXCEPTION 'provision resolution outcome must be server-derived and terminal failure may only certify zero candidates'
            USING ERRCODE = '23514';
    END IF;
    IF derived_outcome = 'adopted_exact_candidate' THEN
        IF NEW.selected_candidate_digest IS DISTINCT FROM
             observation.candidate_graphs_json->0->>'graph_digest'
           OR NEW.result_receipt_sequence IS DISTINCT FROM observation.head_sequence + 1
           OR NEW.result_receipt_digest IS NULL THEN
            RAISE EXCEPTION 'exact-candidate adoption does not bind its sole candidate and next receipt'
                USING ERRCODE = '23514';
        END IF;
    ELSIF NEW.selected_candidate_digest IS NOT NULL
       OR NEW.result_receipt_sequence IS NOT NULL
       OR NEW.result_receipt_digest IS NOT NULL THEN
        RAISE EXCEPTION 'non-adoption resolution cannot bind candidate handles or a receipt'
            USING ERRCODE = '23514';
    END IF;

    IF bound_token_digest IS NULL
       OR NEW.decision_token_digest IS DISTINCT FROM bound_token_digest THEN
        RAISE EXCEPTION 'provision resolution is not bound to its transaction capability'
            USING ERRCODE = '55000';
    END IF;

    NEW.decided_at := clock_timestamp();
    RETURN NEW;
END;
$$;

-- A canceled native provision can be terminalized without a provider delete
-- only when the database proves that no provider resource exists. There are
-- exactly two authorities: no dispatch custody ever existed, or an immutable
-- Operator Reconcile decision verified zero candidates after dispatch
-- ambiguity was frozen. The fact remains separate from providerexecutor
-- receipts because accepted -> denied is intentionally not a provider wire
-- transition.
CREATE TABLE IF NOT EXISTS provider_operation_resource_free_terminalizations (
    tenant_id text NOT NULL CHECK (BTRIM(tenant_id) <> ''),
    operation_id text NOT NULL CHECK (BTRIM(operation_id) <> ''),
    lease_id text NOT NULL CHECK (BTRIM(lease_id) <> ''),
    lease_revision bigint NOT NULL CHECK (lease_revision > 0),
    server_id text NOT NULL CHECK (BTRIM(server_id) <> ''),
    server_generation bigint NOT NULL CHECK (server_generation > 0),
    resource_generation_id uuid NOT NULL,
    head_sequence bigint NOT NULL CHECK (head_sequence > 0),
    head_receipt_digest text NOT NULL
        CHECK (head_receipt_digest ~ '^sha256:[0-9a-f]{64}$'),
    authority text NOT NULL CHECK (authority IN (
        'no_dispatch_custody',
        'no_candidate_observed'
    )),
    resolution_revision bigint CHECK (resolution_revision > 0),
    decision_digest text CHECK (
        decision_digest IS NULL
        OR decision_digest ~ '^sha256:[0-9a-f]{64}$'
    ),
    terminalized_at timestamptz NOT NULL,
    PRIMARY KEY (tenant_id, operation_id),
    UNIQUE (tenant_id, lease_id, resource_generation_id),
    FOREIGN KEY (tenant_id, operation_id, lease_id)
        REFERENCES provider_operations (tenant_id, operation_id, lease_id)
        ON UPDATE RESTRICT ON DELETE RESTRICT,
    FOREIGN KEY (tenant_id, operation_id, head_sequence, head_receipt_digest)
        REFERENCES provider_operation_receipts (
            tenant_id, operation_id, sequence, receipt_digest
        )
        ON UPDATE RESTRICT ON DELETE RESTRICT,
    FOREIGN KEY (tenant_id, lease_id, resource_generation_id)
        REFERENCES managed_runtime_capacity_reservations (
            tenant_id, lease_id, resource_generation_id
        )
        ON UPDATE RESTRICT ON DELETE RESTRICT,
    FOREIGN KEY (tenant_id, server_id)
        REFERENCES servers (tenant_id, id)
        ON UPDATE RESTRICT ON DELETE RESTRICT,
    CHECK (
        (
            authority = 'no_dispatch_custody'
            AND resolution_revision IS NULL
            AND decision_digest IS NULL
        )
        OR
        (
            authority = 'no_candidate_observed'
            AND resolution_revision IS NOT NULL
            AND decision_digest IS NOT NULL
        )
    )
);

CREATE OR REPLACE FUNCTION provider_resource_free_terminalization_validate_insert()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path FROM CURRENT
AS $$
DECLARE
    operation_kind text;
    operation_status text;
    operation_phase text;
    operation_lease_revision bigint;
    operation_server_id text;
    operation_server_generation bigint;
    operation_resource_generation_id uuid;
    operation_head_sequence bigint;
    operation_head_digest text;
    live_lease_revision bigint;
    live_lease_server_id text;
    live_resource_generation_id uuid;
    live_lease_desired_state text;
    live_cancelled_at timestamptz;
    live_server_lease_id text;
    live_server_generation bigint;
    live_server_desired_state text;
    live_server_lifecycle_state text;
    live_server_decommissioned_at timestamptz;
BEGIN
    PERFORM pg_advisory_xact_lock(hashtext(NEW.tenant_id), hashtext(NEW.operation_id));
    PERFORM pg_advisory_xact_lock(
        hashtext(NEW.tenant_id),
        hashtext(NEW.lease_id || ':' || NEW.resource_generation_id::text)
    );

    SELECT
        operation.operation,
        operation.status,
        operation.phase,
        (operation.command_json #>> '{command,lease_revision}')::bigint,
        operation.command_json #>> '{command,runtime_server_id}',
        NULLIF(operation.command_json->>'runtime_server_generation', '')::bigint,
        (operation.command_json #>> '{command,resource_generation_id}')::uuid,
        operation.head_sequence,
        operation.head_receipt_digest
    INTO
        operation_kind,
        operation_status,
        operation_phase,
        operation_lease_revision,
        operation_server_id,
        operation_server_generation,
        operation_resource_generation_id,
        operation_head_sequence,
        operation_head_digest
    FROM provider_operations AS operation
    WHERE operation.tenant_id = NEW.tenant_id
      AND operation.operation_id = NEW.operation_id
      AND operation.lease_id = NEW.lease_id
    FOR UPDATE OF operation;

    IF NOT FOUND
       OR operation_kind IS DISTINCT FROM 'provision'
       OR NOT (
            (
                operation_status = 'pending'
                AND operation_phase IN ('requested', 'accepted')
            )
            OR (
                operation_status = 'failed'
                AND operation_phase = 'failed'
                AND NEW.authority = 'no_candidate_observed'
            )
       )
       OR operation_lease_revision IS DISTINCT FROM NEW.lease_revision
       OR operation_server_id IS DISTINCT FROM NEW.server_id
       OR operation_server_generation IS DISTINCT FROM NEW.server_generation
       OR operation_resource_generation_id IS DISTINCT FROM NEW.resource_generation_id
       OR operation_head_sequence IS DISTINCT FROM NEW.head_sequence
       OR operation_head_digest IS DISTINCT FROM NEW.head_receipt_digest THEN
        RAISE EXCEPTION 'resource-free terminalization is stale against its exact provision head'
            USING ERRCODE = '55000';
    END IF;

    SELECT
        lease.lease_revision,
        lease.server_id,
        lease.resource_generation_id,
        lease.desired_state,
        lease.cancelled_at,
        server.lease_id,
        server.generation,
        server.desired_state,
        server.lifecycle_state,
        server.decommissioned_at
    INTO
        live_lease_revision,
        live_lease_server_id,
        live_resource_generation_id,
        live_lease_desired_state,
        live_cancelled_at,
        live_server_lease_id,
        live_server_generation,
        live_server_desired_state,
        live_server_lifecycle_state,
        live_server_decommissioned_at
    FROM techstack_vm_leases AS lease
    JOIN servers AS server
      ON server.tenant_id = lease.tenant_id
     AND server.id = lease.server_id
    WHERE lease.tenant_id = NEW.tenant_id
      AND lease.id = NEW.lease_id
    FOR UPDATE OF lease, server;

    IF NOT FOUND
       OR live_lease_revision IS DISTINCT FROM NEW.lease_revision
       OR live_lease_server_id IS DISTINCT FROM NEW.server_id
       OR live_resource_generation_id IS DISTINCT FROM NEW.resource_generation_id
       OR live_server_lease_id IS DISTINCT FROM NEW.lease_id
       OR live_server_generation IS DISTINCT FROM NEW.server_generation
       OR live_server_decommissioned_at IS NOT NULL
       OR live_server_lifecycle_state NOT IN (
            'planned', 'provisioning', 'failed', 'decommissioning'
       )
       OR NOT (
            live_cancelled_at IS NOT NULL
            OR live_lease_desired_state = 'absent'
            OR live_server_desired_state = 'absent'
            OR live_server_lifecycle_state = 'decommissioning'
       ) THEN
        RAISE EXCEPTION 'resource-free terminalization requires exact teardown-bound lease and server custody'
            USING ERRCODE = '55000';
    END IF;

    IF EXISTS (
        SELECT 1
        FROM provider_operation_resources AS resource
        WHERE resource.tenant_id = NEW.tenant_id
          AND resource.operation_id = NEW.operation_id
    ) OR EXISTS (
        SELECT 1
        FROM server_provider_resource_bindings AS binding
        WHERE binding.tenant_id = NEW.tenant_id
          AND binding.lease_id = NEW.lease_id
          AND binding.resource_generation_id = NEW.resource_generation_id
    ) THEN
        RAISE EXCEPTION 'resource-free terminalization conflicts with provider resource custody'
            USING ERRCODE = '55000';
    END IF;

    IF EXISTS (
        SELECT 1
        FROM provider_operation_resources AS resource
        JOIN provider_operations AS operation
          ON operation.tenant_id = resource.tenant_id
         AND operation.operation_id = resource.operation_id
        WHERE operation.tenant_id = NEW.tenant_id
          AND operation.lease_id = NEW.lease_id
          AND (operation.command_json #>> '{command,resource_generation_id}')::uuid =
                NEW.resource_generation_id
    ) OR EXISTS (
        SELECT 1
        FROM provider_operation_execution_claims AS claim
        JOIN provider_operations AS operation
          ON operation.tenant_id = claim.tenant_id
         AND operation.operation_id = claim.operation_id
        WHERE operation.tenant_id = NEW.tenant_id
          AND operation.lease_id = NEW.lease_id
          AND (operation.command_json #>> '{command,resource_generation_id}')::uuid =
                NEW.resource_generation_id
          AND claim.claim_access = 'side_effecting'
          AND claim.state IN ('active', 'released')
          AND operation.head_sequence = claim.head_sequence
          AND operation.head_receipt_digest = claim.head_receipt_digest
          AND NOT EXISTS (
              SELECT 1
              FROM provider_provision_resolution_decisions AS decision
              WHERE decision.tenant_id = claim.tenant_id
                AND decision.operation_id = claim.operation_id
                AND decision.expected_head_sequence = claim.head_sequence
                AND decision.expected_head_receipt_digest =
                      claim.head_receipt_digest
                AND decision.outcome = 'no_candidate_observed'
          )
    ) OR EXISTS (
        SELECT 1
        FROM provider_provision_dispatch_guards AS dispatch_guard
        JOIN provider_operations AS operation
          ON operation.tenant_id = dispatch_guard.tenant_id
         AND operation.operation_id = dispatch_guard.operation_id
        WHERE operation.tenant_id = NEW.tenant_id
          AND operation.lease_id = NEW.lease_id
          AND (operation.command_json #>> '{command,resource_generation_id}')::uuid =
                NEW.resource_generation_id
          AND NOT EXISTS (
              SELECT 1
              FROM provider_provision_resolution_decisions AS decision
              JOIN provider_provision_discovery_observations AS observation
                ON observation.tenant_id = decision.tenant_id
               AND observation.operation_id = decision.operation_id
               AND observation.observation_id = decision.observation_id
               AND observation.snapshot_digest =
                    decision.observation_snapshot_digest
              WHERE decision.tenant_id = dispatch_guard.tenant_id
                AND decision.operation_id = dispatch_guard.operation_id
                AND decision.outcome = 'no_candidate_observed'
                AND observation.head_sequence = dispatch_guard.head_sequence
                AND observation.head_receipt_digest =
                      dispatch_guard.head_receipt_digest
          )
    ) OR EXISTS (
        SELECT 1
        FROM provider_operations AS operation
        WHERE operation.tenant_id = NEW.tenant_id
          AND operation.lease_id = NEW.lease_id
          AND operation.operation = 'provision'
          AND operation.status = 'failed'
          AND operation.phase = 'failed'
          AND (operation.command_json #>> '{command,resource_generation_id}')::uuid =
                NEW.resource_generation_id
          AND NOT EXISTS (
              SELECT 1
              FROM provider_operation_resources AS resource
              WHERE resource.tenant_id = operation.tenant_id
                AND resource.operation_id = operation.operation_id
          )
          AND NOT (
              operation.operation_id = NEW.operation_id
              AND operation.head_sequence = NEW.head_sequence
              AND operation.head_receipt_digest = NEW.head_receipt_digest
              AND EXISTS (
                  SELECT 1
                  FROM provider_provision_resolution_decisions AS decision
                  JOIN provider_provision_discovery_observations AS observation
                    ON observation.tenant_id = decision.tenant_id
                   AND observation.operation_id = decision.operation_id
                   AND observation.observation_id = decision.observation_id
                   AND observation.snapshot_digest =
                        decision.observation_snapshot_digest
                  JOIN provider_provision_dispatch_guards AS dispatch_guard
                    ON dispatch_guard.tenant_id = observation.tenant_id
                   AND dispatch_guard.operation_id = observation.operation_id
                   AND dispatch_guard.head_sequence = observation.head_sequence
                   AND dispatch_guard.head_receipt_digest =
                        observation.head_receipt_digest
                  WHERE decision.tenant_id = operation.tenant_id
                    AND decision.operation_id = operation.operation_id
                    AND decision.expected_head_sequence =
                        operation.head_sequence
                    AND decision.expected_head_receipt_digest =
                        operation.head_receipt_digest
                    AND decision.outcome = 'no_candidate_observed'
                    AND observation.candidate_count = 0
                    AND dispatch_guard.dispatch_mode =
                        'at_most_once_dispatch_manual_reconcile'
                    AND dispatch_guard.guard_origin = 'first_claim'
              )
          )
    ) THEN
        RAISE EXCEPTION 'resource-free terminalization conflicts with unsettled generation-wide provider custody'
            USING ERRCODE = '55000';
    END IF;

    IF NEW.authority = 'no_dispatch_custody' THEN
        IF EXISTS (
            SELECT 1
            FROM provider_operation_execution_claims AS claim
            WHERE claim.tenant_id = NEW.tenant_id
              AND claim.operation_id = NEW.operation_id
              AND claim.claim_access = 'side_effecting'
        ) OR EXISTS (
            SELECT 1
            FROM provider_provision_dispatch_guards AS dispatch_guard
            WHERE dispatch_guard.tenant_id = NEW.tenant_id
              AND dispatch_guard.operation_id = NEW.operation_id
        ) OR EXISTS (
            SELECT 1
            FROM provider_provision_discovery_observations AS observation
            WHERE observation.tenant_id = NEW.tenant_id
              AND observation.operation_id = NEW.operation_id
        ) OR EXISTS (
            SELECT 1
            FROM provider_provision_resolution_decisions AS decision
            WHERE decision.tenant_id = NEW.tenant_id
              AND decision.operation_id = NEW.operation_id
        ) THEN
            RAISE EXCEPTION 'no-dispatch terminalization has provider dispatch or resolution custody'
                USING ERRCODE = '55000';
        END IF;
    ELSE
        PERFORM 1
        FROM provider_provision_resolution_decisions AS decision
        JOIN provider_provision_discovery_observations AS observation
          ON observation.tenant_id = decision.tenant_id
         AND observation.operation_id = decision.operation_id
         AND observation.observation_id = decision.observation_id
         AND observation.snapshot_digest = decision.observation_snapshot_digest
        JOIN provider_provision_dispatch_guards AS dispatch_guard
          ON dispatch_guard.tenant_id = observation.tenant_id
         AND dispatch_guard.operation_id = observation.operation_id
         AND dispatch_guard.head_sequence = observation.head_sequence
         AND dispatch_guard.head_receipt_digest =
              observation.head_receipt_digest
        WHERE decision.tenant_id = NEW.tenant_id
          AND decision.operation_id = NEW.operation_id
          AND decision.resolution_revision = NEW.resolution_revision
          AND decision.decision_digest = NEW.decision_digest
          AND decision.expected_head_sequence = NEW.head_sequence
          AND decision.expected_head_receipt_digest = NEW.head_receipt_digest
          AND decision.outcome = 'no_candidate_observed'
          AND observation.candidate_count = 0
          AND dispatch_guard.dispatch_mode =
              'at_most_once_dispatch_manual_reconcile'
          AND dispatch_guard.guard_origin = 'first_claim'
          AND (
              (
                  observation.head_sequence = NEW.head_sequence
                  AND observation.head_receipt_digest =
                      NEW.head_receipt_digest
              )
              OR (
                  operation_status = 'failed'
                  AND operation_phase = 'failed'
                  AND NEW.head_sequence = observation.head_sequence + 1
                  AND EXISTS (
                      SELECT 1
                      FROM provider_operation_receipts AS failed_receipt
                      WHERE failed_receipt.tenant_id = NEW.tenant_id
                        AND failed_receipt.operation_id = NEW.operation_id
                        AND failed_receipt.sequence = NEW.head_sequence
                        AND failed_receipt.receipt_digest =
                            NEW.head_receipt_digest
                        AND failed_receipt.previous_receipt_digest =
                            observation.head_receipt_digest
                  )
              )
          );
        IF NOT FOUND OR EXISTS (
            SELECT 1
            FROM provider_operation_execution_claims AS claim
            JOIN provider_provision_discovery_observations AS observation
              ON observation.tenant_id = claim.tenant_id
             AND observation.operation_id = claim.operation_id
            JOIN provider_provision_resolution_decisions AS decision
              ON decision.tenant_id = observation.tenant_id
             AND decision.operation_id = observation.operation_id
             AND decision.observation_id = observation.observation_id
             AND decision.observation_snapshot_digest =
                  observation.snapshot_digest
            WHERE decision.tenant_id = NEW.tenant_id
              AND decision.operation_id = NEW.operation_id
              AND decision.resolution_revision = NEW.resolution_revision
              AND decision.decision_digest = NEW.decision_digest
              AND claim.head_sequence = observation.head_sequence
              AND claim.head_receipt_digest =
                  observation.head_receipt_digest
              AND claim.state = 'active'
              AND claim.lease_expires_at > clock_timestamp()
        ) THEN
            RAISE EXCEPTION 'no-candidate terminalization lacks an exact quiescent operator decision'
                USING ERRCODE = '55000';
        END IF;
    END IF;

    NEW.terminalized_at := clock_timestamp();
    RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION provider_resource_free_terminalization_reject_mutation()
RETURNS trigger
LANGUAGE plpgsql
SET search_path FROM CURRENT
AS $$
BEGIN
    RAISE EXCEPTION 'resource-free provider terminalization facts are immutable'
        USING ERRCODE = '55000';
END;
$$;

DROP TRIGGER IF EXISTS provider_operation_resource_free_terminalizations_validate_insert
    ON provider_operation_resource_free_terminalizations;
CREATE TRIGGER provider_operation_resource_free_terminalizations_validate_insert
BEFORE INSERT ON provider_operation_resource_free_terminalizations
FOR EACH ROW EXECUTE FUNCTION provider_resource_free_terminalization_validate_insert();
DROP TRIGGER IF EXISTS provider_operation_resource_free_terminalizations_reject_mutation
    ON provider_operation_resource_free_terminalizations;
CREATE TRIGGER provider_operation_resource_free_terminalizations_reject_mutation
BEFORE UPDATE OR DELETE ON provider_operation_resource_free_terminalizations
FOR EACH ROW EXECUTE FUNCTION provider_resource_free_terminalization_reject_mutation();

ALTER TABLE provider_operation_resource_free_terminalizations ENABLE ROW LEVEL SECURITY;
ALTER TABLE provider_operation_resource_free_terminalizations FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation ON provider_operation_resource_free_terminalizations;
CREATE POLICY tenant_isolation ON provider_operation_resource_free_terminalizations
    USING (tenant_id = current_setting('app.tenant_id', true))
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true));

CREATE TABLE IF NOT EXISTS managed_runtime_capacity_release_facts (
    tenant_id text NOT NULL CHECK (BTRIM(tenant_id) <> ''),
    lease_id text NOT NULL CHECK (BTRIM(lease_id) <> ''),
    resource_generation_id uuid NOT NULL,
    server_id text NOT NULL CHECK (BTRIM(server_id) <> ''),
    server_generation bigint NOT NULL CHECK (server_generation > 0),
    release_operation_id text NOT NULL
        CHECK (BTRIM(release_operation_id) <> ''),
    release_authority text NOT NULL CHECK (release_authority IN (
        'provider_absence',
        'resource_free_teardown'
    )),
    receipt_sequence bigint NOT NULL CHECK (receipt_sequence > 0),
    receipt_digest text NOT NULL
        CHECK (receipt_digest ~ '^sha256:[0-9a-f]{64}$'),
    released_at timestamptz NOT NULL,
    PRIMARY KEY (tenant_id, lease_id, resource_generation_id),
    UNIQUE (tenant_id, release_operation_id),
    FOREIGN KEY (tenant_id, lease_id, resource_generation_id)
        REFERENCES managed_runtime_capacity_reservations (
            tenant_id,
            lease_id,
            resource_generation_id
        )
        ON UPDATE RESTRICT ON DELETE RESTRICT,
    FOREIGN KEY (tenant_id, release_operation_id, lease_id)
        REFERENCES provider_operations (tenant_id, operation_id, lease_id)
        ON UPDATE RESTRICT ON DELETE RESTRICT,
    FOREIGN KEY (
        tenant_id,
        release_operation_id,
        receipt_sequence
    )
        REFERENCES provider_operation_receipts (
            tenant_id,
            operation_id,
            sequence
        )
        ON UPDATE RESTRICT ON DELETE RESTRICT,
    FOREIGN KEY (tenant_id, server_id)
        REFERENCES servers (tenant_id, id)
        ON UPDATE RESTRICT ON DELETE RESTRICT
);

-- Capacity reservations remain immutable custody facts. A terminal, exact
-- provider-absence release fact makes the old reservation non-held without
-- deleting or rewriting its history. Keep this authoritative check aligned
-- with the application-side count under the same tenant+owner advisory lock.
CREATE OR REPLACE FUNCTION managed_runtime_capacity_reservation_validate_insert()
RETURNS trigger
LANGUAGE plpgsql
SET search_path FROM CURRENT
AS $$
DECLARE
    operation_kind text;
    operation_status text;
    operation_phase text;
    operation_head_sequence bigint;
    operation_idempotency_key text;
    operation_schema_version text;
    operation_execution_authority text;
    operation_generation_id text;
    operation_provider_id text;
    live_owner_subject_id text;
    live_provider_id text;
    live_resource_generation_id uuid;
    live_execution_authority text;
    held_capacity integer;
    expected_policy_digest text;
BEGIN
    IF NEW.reservation_origin IS DISTINCT FROM 'native_admission'
       OR NEW.reservation_mode NOT IN ('limited', 'unlimited')
       OR NEW.operation_id IS NULL THEN
        RAISE EXCEPTION 'new managed runtime capacity reservation must be native admission custody'
            USING ERRCODE = '55000';
    END IF;

    SELECT
        operation.operation,
        operation.status,
        operation.phase,
        operation.head_sequence,
        operation.idempotency_key,
        operation.command_json->>'schema_version',
        operation.command_json->>'execution_authority',
        operation.command_json #>> '{command,resource_generation_id}',
        operation.command_json #>> '{command,provider_id}'
    INTO
        operation_kind,
        operation_status,
        operation_phase,
        operation_head_sequence,
        operation_idempotency_key,
        operation_schema_version,
        operation_execution_authority,
        operation_generation_id,
        operation_provider_id
    FROM provider_operations AS operation
    WHERE operation.tenant_id = NEW.tenant_id
      AND operation.operation_id = NEW.operation_id
      AND operation.lease_id = NEW.lease_id;

    IF NOT FOUND
       OR operation_kind IS DISTINCT FROM 'provision'
       OR operation_status IS DISTINCT FROM 'pending'
       OR operation_phase IS DISTINCT FROM 'requested'
       OR operation_head_sequence IS DISTINCT FROM 1
       OR operation_idempotency_key IS DISTINCT FROM NEW.admission_idempotency_key
       OR operation_schema_version IS DISTINCT FROM 'techstack.provider-control-operation/v1'
       OR operation_execution_authority IS DISTINCT FROM 'techstack_provider_control'
       OR operation_generation_id IS DISTINCT FROM NEW.resource_generation_id::text
       OR operation_provider_id IS DISTINCT FROM NEW.provider_id THEN
        RAISE EXCEPTION 'capacity reservation must bind the initial native provision operation'
            USING ERRCODE = '55000';
    END IF;

    SELECT
        lease.owner_subject_id,
        COALESCE(
            NULLIF(LOWER(BTRIM(lease.provider_id)), ''),
            NULLIF(LOWER(BTRIM(lease.lease_json->'resource'->>'provider_id')), '')
        ),
        lease.resource_generation_id,
        authority.execution_authority
    INTO
        live_owner_subject_id,
        live_provider_id,
        live_resource_generation_id,
        live_execution_authority
    FROM techstack_vm_leases AS lease
    JOIN runtime_lease_execution_authorities AS authority
      ON authority.tenant_id = lease.tenant_id
     AND authority.lease_id = lease.id
    WHERE lease.tenant_id = NEW.tenant_id
      AND lease.id = NEW.lease_id;

    IF NOT FOUND
       OR live_owner_subject_id IS DISTINCT FROM NEW.owner_subject_id
       OR live_provider_id IS DISTINCT FROM NEW.provider_id
       OR live_resource_generation_id IS DISTINCT FROM NEW.resource_generation_id
       OR live_execution_authority IS DISTINCT FROM 'techstack_provider_control' THEN
        RAISE EXCEPTION 'capacity reservation must bind the live native lease owner and generation'
            USING ERRCODE = '55000';
    END IF;

    PERFORM pg_advisory_xact_lock(
        hashtext(NEW.tenant_id),
        hashtext('providercontrol.capacity:owner_subject:' || NEW.owner_subject_id)
    );
    IF NEW.reservation_mode = 'limited' THEN
        SELECT count(*)
        INTO held_capacity
        FROM managed_runtime_capacity_reservations AS reservation
        WHERE reservation.tenant_id = NEW.tenant_id
          AND reservation.owner_subject_id = NEW.owner_subject_id
          AND NOT EXISTS (
              SELECT 1
              FROM managed_runtime_capacity_release_facts AS release
              WHERE release.tenant_id = reservation.tenant_id
                AND release.lease_id = reservation.lease_id
                AND release.resource_generation_id = reservation.resource_generation_id
          );
        IF held_capacity >= NEW.capacity_limit THEN
            RAISE EXCEPTION 'managed runtime capacity reservation exceeds the authoritative owner limit'
                USING ERRCODE = '55000';
        END IF;
    END IF;

    expected_policy_digest := managed_runtime_capacity_policy_digest(
        NEW.tenant_id,
        NEW.provider_id,
        'owner_subject',
        NEW.owner_subject_id,
        NEW.reservation_mode,
        COALESCE(NEW.capacity_limit, 0),
        NEW.policy_source
    );
    IF NEW.policy_digest IS DISTINCT FROM expected_policy_digest THEN
        RAISE EXCEPTION 'managed runtime capacity policy digest does not match its canonical projection'
            USING ERRCODE = '55000';
    END IF;
    RETURN NEW;
END;
$$;

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
        server.lifecycle_state,
        server.desired_state,
        server.decommissioned_at
    INTO
        server_lifecycle,
        server_desired,
        server_decommissioned_at
    FROM servers AS server
    WHERE server.tenant_id = NEW.tenant_id
      AND server.id = NEW.server_id;

    IF (
        NEW.release_authority = 'provider_absence'
        AND (
            operation_kind IS DISTINCT FROM 'decommission'
            OR operation_status IS DISTINCT FROM 'succeeded'
            OR operation_phase IS DISTINCT FROM 'absent'
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
            OR NOT EXISTS (
                SELECT 1
                FROM provider_operation_resource_free_terminalizations AS terminalization
                WHERE terminalization.tenant_id = NEW.tenant_id
                  AND terminalization.operation_id = NEW.release_operation_id
                  AND terminalization.lease_id = NEW.lease_id
                  AND terminalization.server_id = NEW.server_id
                  AND terminalization.server_generation = NEW.server_generation
                  AND terminalization.resource_generation_id =
                        NEW.resource_generation_id
                  AND terminalization.head_sequence = NEW.receipt_sequence
                  AND terminalization.head_receipt_digest = NEW.receipt_digest
            )
        )
    )
       OR operation_server_id IS DISTINCT FROM NEW.server_id
       OR operation_server_generation IS DISTINCT FROM NEW.server_generation
       OR operation_resource_generation_id IS DISTINCT FROM NEW.resource_generation_id
       OR head_sequence IS DISTINCT FROM NEW.receipt_sequence
       OR head_receipt_digest IS DISTINCT FROM NEW.receipt_digest
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

CREATE OR REPLACE FUNCTION managed_runtime_capacity_release_reject_mutation()
RETURNS trigger
LANGUAGE plpgsql
SET search_path FROM CURRENT
AS $$
BEGIN
    RAISE EXCEPTION 'managed runtime capacity release facts are immutable'
        USING ERRCODE = '55000';
END;
$$;

DROP TRIGGER IF EXISTS managed_runtime_capacity_release_validate_insert
    ON managed_runtime_capacity_release_facts;
CREATE TRIGGER managed_runtime_capacity_release_validate_insert
BEFORE INSERT ON managed_runtime_capacity_release_facts
FOR EACH ROW EXECUTE FUNCTION managed_runtime_capacity_release_validate_insert();

DROP TRIGGER IF EXISTS managed_runtime_capacity_release_reject_mutation
    ON managed_runtime_capacity_release_facts;
CREATE TRIGGER managed_runtime_capacity_release_reject_mutation
BEFORE UPDATE OR DELETE ON managed_runtime_capacity_release_facts
FOR EACH ROW EXECUTE FUNCTION managed_runtime_capacity_release_reject_mutation();

-- The terminalization fact, RuntimeServer tombstone, and capacity release are
-- one atomic proof. Runtime may insert the fact only as part of that complete
-- transaction; a raw fact can never suppress scheduling or strand capacity.
CREATE OR REPLACE FUNCTION provider_resource_free_terminalization_validate_commit()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path FROM CURRENT
AS $$
BEGIN
    PERFORM 1
    FROM servers AS server
    JOIN managed_runtime_capacity_release_facts AS release
      ON release.tenant_id = NEW.tenant_id
     AND release.lease_id = NEW.lease_id
     AND release.resource_generation_id = NEW.resource_generation_id
     AND release.server_id = NEW.server_id
     AND release.server_generation = NEW.server_generation
     AND release.release_operation_id = NEW.operation_id
     AND release.release_authority = 'resource_free_teardown'
     AND release.receipt_sequence = NEW.head_sequence
     AND release.receipt_digest = NEW.head_receipt_digest
    WHERE server.tenant_id = NEW.tenant_id
      AND server.id = NEW.server_id
      AND server.lease_id = NEW.lease_id
      AND server.generation = NEW.server_generation
      AND server.desired_state = 'absent'
      AND server.lifecycle_state = 'decommissioned'
      AND server.decommissioned_at IS NOT NULL;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'resource-free terminalization must atomically include its exact RuntimeServer tombstone and capacity release'
            USING ERRCODE = '55000';
    END IF;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS provider_operation_resource_free_terminalizations_validate_commit
    ON provider_operation_resource_free_terminalizations;
CREATE CONSTRAINT TRIGGER provider_operation_resource_free_terminalizations_validate_commit
AFTER INSERT ON provider_operation_resource_free_terminalizations
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION provider_resource_free_terminalization_validate_commit();

ALTER TABLE managed_runtime_capacity_release_facts ENABLE ROW LEVEL SECURITY;
ALTER TABLE managed_runtime_capacity_release_facts FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation ON managed_runtime_capacity_release_facts;
CREATE POLICY tenant_isolation ON managed_runtime_capacity_release_facts
    USING (tenant_id = current_setting('app.tenant_id', true))
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true));

-- Historical native envelopes have no numeric RuntimeServer pin and are
-- forensic-only. Keep them out of both runnable projections so an oldest-first
-- bounded scheduler cannot repeatedly select them and starve pinned work.
CREATE OR REPLACE FUNCTION provider_control_refresh_runnable_tenant()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path FROM CURRENT
AS $$
DECLARE
    operation_tenant_id text := COALESCE(NEW.tenant_id, OLD.tenant_id);
    scoped_tenant_id text := NULLIF(current_setting('app.tenant_id', true), '');
    runnable_count integer;
BEGIN
    IF operation_tenant_id IS NULL OR BTRIM(operation_tenant_id) = '' THEN
        RAISE EXCEPTION 'provider-control runnable tenant refresh requires an exact tenant'
            USING ERRCODE = '55000';
    END IF;
    IF scoped_tenant_id IS DISTINCT FROM operation_tenant_id THEN
        RAISE EXCEPTION 'provider-control runnable tenant refresh requires the exact tenant scope'
            USING ERRCODE = '42501';
    END IF;

    PERFORM pg_catalog.pg_advisory_xact_lock(
        pg_catalog.hashtextextended(
            'providercontrol.runnable-tenant/v1:' || operation_tenant_id,
            0
        )
    );

    SELECT COUNT(*)::integer
    INTO runnable_count
    FROM provider_operations AS operation
    WHERE operation.tenant_id = operation_tenant_id
      AND operation.command_json->>'schema_version' = 'techstack.provider-control-operation/v1'
      AND operation.command_json->>'execution_authority' = 'techstack_provider_control'
      AND CASE
          WHEN jsonb_typeof(operation.command_json->'runtime_server_generation') = 'number'
           AND operation.command_json->>'runtime_server_generation' ~ '^[1-9][0-9]{0,18}$'
          THEN (operation.command_json->>'runtime_server_generation')::numeric
                <= 9223372036854775807
          ELSE false
      END
      AND operation.provision_dispatch_mode <> 'blocked'
      AND operation.status = 'pending'
      AND operation.phase NOT IN ('planned', 'present', 'absent', 'failed', 'denied')
      AND NOT EXISTS (
          SELECT 1
          FROM provider_operation_resource_free_terminalizations AS terminalization
          WHERE terminalization.tenant_id = operation.tenant_id
            AND terminalization.operation_id = operation.operation_id
            AND terminalization.head_sequence = operation.head_sequence
            AND terminalization.head_receipt_digest =
                  operation.head_receipt_digest
      )
      AND NOT (
          operation.operation = 'provision'
          AND operation.phase = 'accepted'
          AND EXISTS (
              SELECT 1
              FROM provider_provision_dispatch_guards AS dispatch_guard
              WHERE dispatch_guard.tenant_id = operation.tenant_id
                AND dispatch_guard.lease_id = operation.lease_id
                AND dispatch_guard.resource_generation_id =
                    (operation.command_json #>> '{command,resource_generation_id}')::uuid
                AND (
                    dispatch_guard.operation_id <> operation.operation_id
                    OR dispatch_guard.dispatch_mode = 'at_most_once_dispatch_manual_reconcile'
                    OR dispatch_guard.guard_origin = 'migration_quarantine'
                )
          )
          AND NOT (
              EXISTS (
                  SELECT 1
                  FROM provider_provision_resolution_decisions AS decision
                  WHERE decision.tenant_id = operation.tenant_id
                    AND decision.operation_id = operation.operation_id
                    AND decision.expected_head_sequence = operation.head_sequence
                    AND decision.expected_head_receipt_digest =
                          operation.head_receipt_digest
                    AND decision.outcome = 'no_candidate_observed'
              )
              AND EXISTS (
                  SELECT 1
                  FROM techstack_vm_leases AS lease
                  JOIN servers AS server
                    ON server.tenant_id = lease.tenant_id
                   AND server.id = lease.server_id
                  WHERE lease.tenant_id = operation.tenant_id
                    AND lease.id = operation.lease_id
                    AND (
                        lease.cancelled_at IS NOT NULL
                        OR lease.desired_state = 'absent'
                        OR server.desired_state = 'absent'
                        OR server.lifecycle_state = 'decommissioning'
                    )
              )
          )
      );

    IF runnable_count > 0 THEN
        INSERT INTO provider_control_runnable_tenants (
            tenant_id,
            runnable_operation_count,
            refreshed_at
        ) VALUES (
            operation_tenant_id,
            runnable_count,
            clock_timestamp()
        )
        ON CONFLICT (tenant_id) DO UPDATE
        SET runnable_operation_count = EXCLUDED.runnable_operation_count,
            refreshed_at = EXCLUDED.refreshed_at;
    ELSE
        DELETE FROM provider_control_runnable_tenants
        WHERE tenant_id = operation_tenant_id;
    END IF;
    RETURN COALESCE(NEW, OLD);
END;
$$;

DROP TRIGGER IF EXISTS provider_operation_resource_free_terminalizations_refresh_runnable
    ON provider_operation_resource_free_terminalizations;
CREATE TRIGGER provider_operation_resource_free_terminalizations_refresh_runnable
AFTER INSERT ON provider_operation_resource_free_terminalizations
FOR EACH ROW EXECUTE FUNCTION provider_control_refresh_runnable_tenant();

DROP TRIGGER IF EXISTS provider_provision_resolution_decisions_refresh_runnable
    ON provider_provision_resolution_decisions;
CREATE TRIGGER provider_provision_resolution_decisions_refresh_runnable
AFTER INSERT ON provider_provision_resolution_decisions
FOR EACH ROW EXECUTE FUNCTION provider_control_refresh_runnable_tenant();

DROP TRIGGER IF EXISTS provider_control_lease_teardown_refresh_runnable
    ON techstack_vm_leases;
CREATE TRIGGER provider_control_lease_teardown_refresh_runnable
AFTER UPDATE OF desired_state, cancelled_at ON techstack_vm_leases
FOR EACH ROW
WHEN (
    OLD.desired_state IS DISTINCT FROM NEW.desired_state
    OR OLD.cancelled_at IS DISTINCT FROM NEW.cancelled_at
)
EXECUTE FUNCTION provider_control_refresh_runnable_tenant();

DROP TRIGGER IF EXISTS provider_control_server_teardown_refresh_runnable
    ON servers;
CREATE TRIGGER provider_control_server_teardown_refresh_runnable
AFTER UPDATE OF desired_state, lifecycle_state, decommissioned_at ON servers
FOR EACH ROW
WHEN (
    OLD.desired_state IS DISTINCT FROM NEW.desired_state
    OR OLD.lifecycle_state IS DISTINCT FROM NEW.lifecycle_state
    OR OLD.decommissioned_at IS DISTINCT FROM NEW.decommissioned_at
)
EXECUTE FUNCTION provider_control_refresh_runnable_tenant();

ALTER TABLE provider_operations NO FORCE ROW LEVEL SECURITY;
ALTER TABLE provider_operations DISABLE ROW LEVEL SECURITY;
ALTER TABLE provider_provision_dispatch_guards NO FORCE ROW LEVEL SECURITY;
ALTER TABLE provider_provision_dispatch_guards DISABLE ROW LEVEL SECURITY;
ALTER TABLE provider_operation_resource_free_terminalizations NO FORCE ROW LEVEL SECURITY;
ALTER TABLE provider_operation_resource_free_terminalizations DISABLE ROW LEVEL SECURITY;
ALTER TABLE provider_provision_resolution_decisions NO FORCE ROW LEVEL SECURITY;
ALTER TABLE provider_provision_resolution_decisions DISABLE ROW LEVEL SECURITY;
ALTER TABLE techstack_vm_leases NO FORCE ROW LEVEL SECURITY;
ALTER TABLE techstack_vm_leases DISABLE ROW LEVEL SECURITY;
ALTER TABLE servers NO FORCE ROW LEVEL SECURITY;
ALTER TABLE servers DISABLE ROW LEVEL SECURITY;

TRUNCATE TABLE provider_control_runnable_tenants;
INSERT INTO provider_control_runnable_tenants (
    tenant_id,
    runnable_operation_count,
    refreshed_at
)
SELECT
    operation.tenant_id,
    COUNT(*)::integer,
    clock_timestamp()
FROM provider_operations AS operation
WHERE operation.command_json->>'schema_version' = 'techstack.provider-control-operation/v1'
  AND operation.command_json->>'execution_authority' = 'techstack_provider_control'
  AND CASE
      WHEN jsonb_typeof(operation.command_json->'runtime_server_generation') = 'number'
       AND operation.command_json->>'runtime_server_generation' ~ '^[1-9][0-9]{0,18}$'
      THEN (operation.command_json->>'runtime_server_generation')::numeric
            <= 9223372036854775807
      ELSE false
  END
  AND operation.provision_dispatch_mode <> 'blocked'
  AND operation.status = 'pending'
  AND operation.phase NOT IN ('planned', 'present', 'absent', 'failed', 'denied')
  AND NOT EXISTS (
      SELECT 1
      FROM provider_operation_resource_free_terminalizations AS terminalization
      WHERE terminalization.tenant_id = operation.tenant_id
        AND terminalization.operation_id = operation.operation_id
        AND terminalization.head_sequence = operation.head_sequence
        AND terminalization.head_receipt_digest = operation.head_receipt_digest
  )
  AND NOT (
      operation.operation = 'provision'
      AND operation.phase = 'accepted'
      AND EXISTS (
          SELECT 1
          FROM provider_provision_dispatch_guards AS dispatch_guard
          WHERE dispatch_guard.tenant_id = operation.tenant_id
            AND dispatch_guard.lease_id = operation.lease_id
            AND dispatch_guard.resource_generation_id =
                (operation.command_json #>> '{command,resource_generation_id}')::uuid
            AND (
                dispatch_guard.operation_id <> operation.operation_id
                OR dispatch_guard.dispatch_mode = 'at_most_once_dispatch_manual_reconcile'
                OR dispatch_guard.guard_origin = 'migration_quarantine'
            )
      )
      AND NOT (
          EXISTS (
              SELECT 1
              FROM provider_provision_resolution_decisions AS decision
              WHERE decision.tenant_id = operation.tenant_id
                AND decision.operation_id = operation.operation_id
                AND decision.expected_head_sequence = operation.head_sequence
                AND decision.expected_head_receipt_digest =
                      operation.head_receipt_digest
                AND decision.outcome = 'no_candidate_observed'
          )
          AND EXISTS (
              SELECT 1
              FROM techstack_vm_leases AS lease
              JOIN servers AS server
                ON server.tenant_id = lease.tenant_id
               AND server.id = lease.server_id
              WHERE lease.tenant_id = operation.tenant_id
                AND lease.id = operation.lease_id
                AND (
                    lease.cancelled_at IS NOT NULL
                    OR lease.desired_state = 'absent'
                    OR server.desired_state = 'absent'
                    OR server.lifecycle_state = 'decommissioning'
                )
          )
      )
  )
GROUP BY operation.tenant_id;

ALTER TABLE servers ENABLE ROW LEVEL SECURITY;
ALTER TABLE servers FORCE ROW LEVEL SECURITY;
ALTER TABLE techstack_vm_leases ENABLE ROW LEVEL SECURITY;
ALTER TABLE techstack_vm_leases FORCE ROW LEVEL SECURITY;
ALTER TABLE provider_provision_resolution_decisions ENABLE ROW LEVEL SECURITY;
ALTER TABLE provider_provision_resolution_decisions FORCE ROW LEVEL SECURITY;
ALTER TABLE provider_operation_resource_free_terminalizations ENABLE ROW LEVEL SECURITY;
ALTER TABLE provider_operation_resource_free_terminalizations FORCE ROW LEVEL SECURITY;
ALTER TABLE provider_provision_dispatch_guards ENABLE ROW LEVEL SECURITY;
ALTER TABLE provider_provision_dispatch_guards FORCE ROW LEVEL SECURITY;
ALTER TABLE provider_operations ENABLE ROW LEVEL SECURITY;
ALTER TABLE provider_operations FORCE ROW LEVEL SECURITY;

-- Runtime teardown only needs the count of exact generation-bound dispatch
-- guards that still lack a matching zero-candidate settlement. Keep raw
-- operator discovery evidence admin-only and expose this single tenant-scoped
-- answer through a hardened boundary.
CREATE OR REPLACE FUNCTION provider_control_count_unsettled_generation_dispatch_guards(
    requested_lease_id text,
    requested_resource_generation_id uuid
)
RETURNS bigint
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path FROM CURRENT
AS $$
DECLARE
    scoped_tenant_id text := NULLIF(current_setting('app.tenant_id', true), '');
    unsettled_count bigint;
BEGIN
    IF scoped_tenant_id IS NULL
       OR requested_lease_id IS NULL
       OR BTRIM(requested_lease_id) = ''
       OR requested_resource_generation_id IS NULL THEN
        RAISE EXCEPTION
            'provider-control dispatch settlement count requires exact tenant, lease, and resource generation'
            USING ERRCODE = '42501';
    END IF;

    SELECT count(*)
    INTO unsettled_count
    FROM provider_provision_dispatch_guards AS dispatch_guard
    JOIN provider_operations AS operation
      ON operation.tenant_id = dispatch_guard.tenant_id
     AND operation.operation_id = dispatch_guard.operation_id
    WHERE operation.tenant_id = scoped_tenant_id
      AND operation.lease_id = requested_lease_id
      AND operation.command_json #>> '{command,resource_generation_id}' =
          requested_resource_generation_id::text
      AND NOT EXISTS (
          SELECT 1
          FROM provider_provision_resolution_decisions AS decision
          JOIN provider_provision_discovery_observations AS observation
            ON observation.tenant_id = decision.tenant_id
           AND observation.operation_id = decision.operation_id
           AND observation.observation_id = decision.observation_id
           AND observation.snapshot_digest =
               decision.observation_snapshot_digest
          WHERE decision.tenant_id = dispatch_guard.tenant_id
            AND decision.operation_id = dispatch_guard.operation_id
            AND decision.outcome = 'no_candidate_observed'
            AND observation.head_sequence = dispatch_guard.head_sequence
            AND observation.head_receipt_digest =
                dispatch_guard.head_receipt_digest
      );

    RETURN unsettled_count;
END;
$$;

-- Replacing trigger functions resets their configured search path to the
-- migration transaction's current order. Re-harden the complete runtime
-- boundary to the exact posture asserted at startup.
DO $provider_control_secure_functions$
DECLARE
    active_schema text := current_schema();
    boundary_function text;
BEGIN
    FOREACH boundary_function IN ARRAY ARRAY[
        'provider_execution_immutable_update',
        'provider_provision_dispatch_guard_validate_insert',
        'provider_provision_discovery_active_claim_guard',
        'provider_provision_resolution_active_claim_guard',
        'provider_execution_claim_current_head',
        'provider_execution_claim_credential_guard',
        'provider_operation_runtime_generation_guard',
        'provider_operation_head_update_guard',
        'provider_execution_claim_runtime_generation_guard',
        'provider_resource_free_terminalization_validate_insert',
        'provider_resource_free_terminalization_validate_commit',
        'managed_runtime_capacity_release_validate_insert',
        'provider_control_refresh_runnable_tenant',
        'provider_control_lock_runtime_lease_projection',
        'provider_control_count_unsettled_generation_dispatch_guards',
        'provider_control_list_runnable_tenants',
        'provider_control_runtime_authority'
    ] LOOP
        EXECUTE pg_catalog.format(
            'ALTER FUNCTION %I.%I%s SECURITY DEFINER',
            active_schema,
            boundary_function,
            CASE boundary_function
                WHEN 'provider_control_list_runnable_tenants' THEN '(text, integer)'
                WHEN 'provider_control_lock_runtime_lease_projection' THEN '(text)'
                WHEN 'provider_control_count_unsettled_generation_dispatch_guards' THEN '(text, uuid)'
                ELSE '()'
            END
        );
        EXECUTE pg_catalog.format(
            'ALTER FUNCTION %I.%I%s SET search_path TO pg_catalog, %I, pg_temp',
            active_schema,
            boundary_function,
            CASE boundary_function
                WHEN 'provider_control_list_runnable_tenants' THEN '(text, integer)'
                WHEN 'provider_control_lock_runtime_lease_projection' THEN '(text)'
                WHEN 'provider_control_count_unsettled_generation_dispatch_guards' THEN '(text, uuid)'
                ELSE '()'
            END,
            active_schema
        );
        EXECUTE pg_catalog.format(
            'REVOKE ALL ON FUNCTION %I.%I%s FROM PUBLIC',
            active_schema,
            boundary_function,
            CASE boundary_function
                WHEN 'provider_control_list_runnable_tenants' THEN '(text, integer)'
                WHEN 'provider_control_lock_runtime_lease_projection' THEN '(text)'
                WHEN 'provider_control_count_unsettled_generation_dispatch_guards' THEN '(text, uuid)'
                ELSE '()'
            END
        );
    END LOOP;
END;
$provider_control_secure_functions$;

REVOKE ALL ON FUNCTION managed_runtime_capacity_reservation_validate_insert() FROM PUBLIC;
REVOKE ALL ON FUNCTION managed_runtime_capacity_release_reject_mutation() FROM PUBLIC;
REVOKE ALL ON FUNCTION provider_resource_free_terminalization_reject_mutation() FROM PUBLIC;

COMMENT ON COLUMN server_provider_resource_bindings.resource_generation_id IS
    'Lease-Authority UUID generation independently pinned beside the numeric RuntimeServer generation.';
COMMENT ON TABLE managed_runtime_capacity_release_facts IS
    'Append-only capacity release proof created only with exact terminal provider absence and RuntimeServer tombstone custody.';
