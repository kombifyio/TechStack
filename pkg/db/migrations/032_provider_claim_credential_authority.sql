-- Revalidate the exact credential custody selected at admission before every
-- newly acquired side-effecting provider claim. Historical handles and claims
-- remain readable but fail closed because their new authority fields are NULL.

ALTER TABLE provider_credential_handles
    ADD COLUMN IF NOT EXISTS custody_hash text,
    ADD COLUMN IF NOT EXISTS connection_hash text,
    ADD COLUMN IF NOT EXISTS valid_from timestamptz,
    ADD COLUMN IF NOT EXISTS valid_until timestamptz;

ALTER TABLE provider_credential_handles
    DROP CONSTRAINT IF EXISTS provider_credential_handles_claim_authority_check;
ALTER TABLE provider_credential_handles
    ADD CONSTRAINT provider_credential_handles_claim_authority_check CHECK (
        (
            custody_hash IS NULL
            AND connection_hash IS NULL
            AND valid_from IS NULL
            AND valid_until IS NULL
        )
        OR (
            custody_hash ~ '^sha256:[0-9a-f]{64}$'
            AND connection_hash ~ '^sha256:[0-9a-f]{64}$'
            AND valid_from IS NOT NULL
            AND valid_until IS NOT NULL
            AND valid_until > valid_from
        )
    );

CREATE OR REPLACE FUNCTION provider_credential_handle_require_claim_authority()
RETURNS trigger
LANGUAGE plpgsql
SET search_path FROM CURRENT
AS $$
BEGIN
    IF NEW.custody_hash IS NULL
       OR NEW.connection_hash IS NULL
       OR NEW.valid_from IS NULL
       OR NEW.valid_until IS NULL THEN
        RAISE EXCEPTION 'new credential handle requires immutable claim authority'
            USING ERRCODE = '55000';
    END IF;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS provider_credential_handles_require_claim_authority
    ON provider_credential_handles;
CREATE TRIGGER provider_credential_handles_require_claim_authority
BEFORE INSERT ON provider_credential_handles
FOR EACH ROW EXECUTE FUNCTION provider_credential_handle_require_claim_authority();

ALTER TABLE provider_operation_execution_claims
    ADD COLUMN IF NOT EXISTS claim_access text;
ALTER TABLE provider_operation_execution_claims
    DROP CONSTRAINT IF EXISTS provider_operation_execution_claims_access_check;
ALTER TABLE provider_operation_execution_claims
    ADD CONSTRAINT provider_operation_execution_claims_access_check
    CHECK (claim_access IS NULL OR claim_access IN ('read_only', 'side_effecting'));

CREATE OR REPLACE FUNCTION provider_execution_claim_access_immutable()
RETURNS trigger
LANGUAGE plpgsql
SET search_path FROM CURRENT
AS $$
BEGIN
    IF NEW.claim_access IS DISTINCT FROM OLD.claim_access THEN
        RAISE EXCEPTION 'provider execution claim access is immutable'
            USING ERRCODE = '55000';
    END IF;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS provider_execution_claims_access_immutable
    ON provider_operation_execution_claims;
CREATE TRIGGER provider_execution_claims_access_immutable
BEFORE UPDATE ON provider_operation_execution_claims
FOR EACH ROW EXECUTE FUNCTION provider_execution_claim_access_immutable();

CREATE OR REPLACE FUNCTION provider_expected_execution_claim_access(
    operation_name text,
    dispatch_mode text,
    receipt_phase text
)
RETURNS text
LANGUAGE sql
IMMUTABLE
STRICT
AS $$
    SELECT CASE
        WHEN dispatch_mode = 'blocked' THEN NULL
        WHEN operation_name IN ('plan', 'observe')
             AND receipt_phase = 'accepted'
            THEN 'read_only'
        WHEN operation_name = 'provision'
             AND receipt_phase = 'accepted'
            THEN 'side_effecting'
        WHEN operation_name = 'provision'
             AND receipt_phase = 'resources_bound'
            THEN 'read_only'
        WHEN operation_name = 'reconcile'
             AND receipt_phase = 'accepted'
            THEN 'side_effecting'
        WHEN operation_name = 'decommission'
             AND receipt_phase = 'accepted'
            THEN 'side_effecting'
        WHEN operation_name = 'decommission'
             AND receipt_phase IN ('delete_accepted', 'absence_pending')
            THEN 'read_only'
        ELSE NULL
    END
$$;

-- This trigger is a database backstop for direct runtime-role DML. Go repeats
-- the same authorization with hash recomputation in the claim transaction.
-- Heartbeat renewal of the same token is intentionally not reauthorized: a
-- revocation during an in-flight provider call must not prevent durable result
-- append or exact-handle cleanup custody.
CREATE OR REPLACE FUNCTION provider_execution_claim_credential_guard()
RETURNS trigger
LANGUAGE plpgsql
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
    ELSIF teardown_requested THEN
        RAISE EXCEPTION 'provider execution claim is fenced by cancellation or teardown intent'
            USING ERRCODE = '55000';
    END IF;

    new_authorization := TG_OP = 'INSERT' OR (
        TG_OP = 'UPDATE'
        AND (
            OLD.state IS DISTINCT FROM 'active'
            OR NEW.claim_token_digest IS DISTINCT FROM OLD.claim_token_digest
            OR NEW.claim_owner IS DISTINCT FROM OLD.claim_owner
            OR NEW.claimed_at IS DISTINCT FROM OLD.claimed_at
        )
    );

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

    -- Capture authorization time only after every live authority row is
    -- locked. A credential which expires while this transaction waits for a
    -- concurrent publisher/revoker must not authorize a later claim commit.
    db_at := clock_timestamp();

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

DROP TRIGGER IF EXISTS provider_execution_claims_credential_guard
    ON provider_operation_execution_claims;
CREATE TRIGGER provider_execution_claims_credential_guard
BEFORE INSERT OR UPDATE ON provider_operation_execution_claims
FOR EACH ROW EXECUTE FUNCTION provider_execution_claim_credential_guard();

-- Migration 031 treated every AMO accepted -> resources_bound transition as
-- operator adoption. Preserve both authorities: an exact consumed first claim
-- appends the one-shot dispatch result normally, while a claim-free transition
-- still requires the transaction-bound exact-candidate decision.
CREATE OR REPLACE FUNCTION provider_execution_immutable_update()
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
    live_server_desired_state text;
    live_server_lifecycle_state text;
    live_server_decommissioned_at timestamptz;
    teardown_requested boolean;
    amo_resources_bound_transition boolean := false;
    first_dispatch_append boolean := false;
    operator_adoption boolean := false;
BEGIN
    IF to_jsonb(NEW) IS NOT DISTINCT FROM to_jsonb(OLD) THEN
        RETURN NEW;
    END IF;

    CASE TG_TABLE_NAME
        WHEN 'provider_desired_spec_revisions', 'provider_operation_receipts', 'provider_operation_evidence' THEN
            RAISE EXCEPTION 'provider execution ledger row in % is immutable', TG_TABLE_NAME
                USING ERRCODE = '55000';

        WHEN 'provider_operations' THEN
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
            WHERE runtime_lease.tenant_id = OLD.tenant_id
              AND runtime_lease.id = OLD.lease_id
            FOR SHARE OF runtime_lease, runtime_server;
            IF NOT FOUND
               OR live_lease_revision IS DISTINCT FROM (OLD.command_json #>> '{command,lease_revision}')::bigint
               OR live_server_id IS DISTINCT FROM OLD.command_json #>> '{command,runtime_server_id}'
               OR live_resource_generation_id IS DISTINCT FROM (OLD.command_json #>> '{command,resource_generation_id}')::uuid
               OR live_server_lease_id IS DISTINCT FROM OLD.lease_id
               OR live_server_lifecycle_state = 'decommissioned'
               OR live_server_decommissioned_at IS NOT NULL THEN
                RAISE EXCEPTION 'provider operation head is stale against the live typed runtime lease'
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
            ELSIF teardown_requested THEN
                RAISE EXCEPTION 'provider operation head is fenced by cancellation or teardown intent'
                    USING ERRCODE = '55000';
            END IF;
            IF (to_jsonb(NEW) - ARRAY['status', 'phase', 'head_sequence', 'head_receipt_digest', 'updated_at'])
               IS DISTINCT FROM
               (to_jsonb(OLD) - ARRAY['status', 'phase', 'head_sequence', 'head_receipt_digest', 'updated_at']) THEN
                RAISE EXCEPTION 'provider operation command and custody fields are immutable'
                    USING ERRCODE = '55000';
            END IF;
            IF NEW.head_sequence <> OLD.head_sequence + 1 OR NEW.updated_at < OLD.updated_at THEN
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

            amo_resources_bound_transition :=
                OLD.operation = 'provision'
                AND OLD.provision_dispatch_mode = 'at_most_once_dispatch_manual_reconcile'
                AND OLD.status = 'pending'
                AND OLD.phase = 'accepted'
                AND NEW.status = 'pending'
                AND NEW.phase = 'resources_bound';
            IF amo_resources_bound_transition THEN
                SELECT EXISTS (
                    SELECT 1
                    FROM provider_operation_execution_claims AS claim
                    WHERE claim.tenant_id = OLD.tenant_id
                      AND claim.operation_id = OLD.operation_id
                      AND claim.head_sequence = OLD.head_sequence
                      AND claim.head_receipt_digest = OLD.head_receipt_digest
                      AND claim.state = 'consumed'
                      AND claim.claim_token_digest = CASE
                          WHEN nullif(current_setting('app.provider_execution_claim_token', true), '') IS NULL THEN NULL
                          ELSE encode(sha256(convert_to(current_setting('app.provider_execution_claim_token', true), 'UTF8')), 'hex')
                      END
                      AND claim.claim_owner = current_setting('app.provider_execution_claim_owner', true)
                ) INTO first_dispatch_append;
            END IF;
            operator_adoption := amo_resources_bound_transition AND NOT first_dispatch_append;

            IF OLD.phase = 'requested' AND NEW.phase = 'accepted' THEN
                IF OLD.provision_dispatch_mode = 'blocked' THEN
                    RAISE EXCEPTION 'blocked provider operation cannot enter accepted custody'
                        USING ERRCODE = '55000';
                END IF;
                PERFORM 1 FROM provider_operation_execution_claims AS claim
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
                 AND observation.snapshot_digest = decision.observation_snapshot_digest
                WHERE decision.tenant_id = OLD.tenant_id
                  AND decision.operation_id = OLD.operation_id
                  AND decision.expected_head_sequence = OLD.head_sequence
                  AND decision.expected_head_receipt_digest = OLD.head_receipt_digest
                  AND decision.outcome = 'adopted_exact_candidate'
                  AND decision.result_receipt_sequence = NEW.head_sequence
                  AND decision.result_receipt_digest = NEW.head_receipt_digest
                  AND decision.decision_token_digest = CASE
                      WHEN nullif(current_setting('app.provider_resolution_token', true), '') IS NULL THEN NULL
                      ELSE encode(sha256(convert_to(current_setting('app.provider_resolution_token', true), 'UTF8')), 'hex')
                  END
                  AND observation.candidate_count = 1
                  AND observation.candidate_graphs_json->0->>'graph_digest' = decision.selected_candidate_digest;
                IF NOT FOUND THEN
                    RAISE EXCEPTION 'AMO exact-candidate adoption requires its transaction-bound decision'
                        USING ERRCODE = '55000';
                END IF;
            ELSE
                PERFORM 1
                FROM provider_operation_execution_claims AS claim
                WHERE claim.tenant_id = OLD.tenant_id
                  AND claim.operation_id = OLD.operation_id
                  AND claim.head_sequence = OLD.head_sequence
                  AND claim.head_receipt_digest = OLD.head_receipt_digest
                  AND claim.state = 'consumed'
                  AND claim.claim_token_digest = CASE
                      WHEN nullif(current_setting('app.provider_execution_claim_token', true), '') IS NULL THEN NULL
                      ELSE encode(sha256(convert_to(current_setting('app.provider_execution_claim_token', true), 'UTF8')), 'hex')
                  END
                  AND claim.claim_owner = current_setting('app.provider_execution_claim_owner', true);
                IF NOT FOUND THEN
                    RAISE EXCEPTION 'provider operation transition requires a consumed execution claim'
                        USING ERRCODE = '55000';
                END IF;
                IF OLD.provision_dispatch_mode = 'blocked' THEN
                    RAISE EXCEPTION 'blocked provider operation cannot advance'
                        USING ERRCODE = '55000';
                END IF;
                IF OLD.operation = 'provision' AND OLD.phase = 'accepted' THEN
                    PERFORM 1
                    FROM provider_provision_dispatch_guards AS guard
                    WHERE guard.tenant_id = OLD.tenant_id
                      AND guard.lease_id = OLD.lease_id
                      AND guard.resource_generation_id =
                          (OLD.command_json #>> '{command,resource_generation_id}')::uuid
                      AND guard.operation_id = OLD.operation_id
                      AND guard.dispatch_mode = OLD.provision_dispatch_mode
                      AND guard.guard_origin = 'first_claim';
                    IF NOT FOUND THEN
                        RAISE EXCEPTION 'provision accepted transition requires exact generation dispatch custody'
                            USING ERRCODE = '55000';
                    END IF;
                END IF;
                IF OLD.operation = 'provision'
                   AND OLD.provision_dispatch_mode = 'at_most_once_dispatch_manual_reconcile'
                   AND OLD.phase = 'accepted' THEN
                    PERFORM 1
                    FROM provider_operation_execution_claims AS claim
                    JOIN provider_provision_dispatch_guards AS guard
                      ON guard.tenant_id = claim.tenant_id
                     AND guard.operation_id = claim.operation_id
                     AND guard.head_sequence = claim.head_sequence
                     AND guard.head_receipt_digest = claim.head_receipt_digest
                     AND guard.first_claim_token_digest = claim.claim_token_digest
                     AND guard.first_claim_owner = claim.claim_owner
                    WHERE claim.tenant_id = OLD.tenant_id
                      AND claim.operation_id = OLD.operation_id
                      AND claim.head_sequence = OLD.head_sequence
                      AND claim.head_receipt_digest = OLD.head_receipt_digest
                      AND claim.state = 'consumed'
                      AND guard.dispatch_mode = OLD.provision_dispatch_mode
                      AND guard.guard_origin = 'first_claim';
                    IF NOT FOUND THEN
                        RAISE EXCEPTION 'AMO provision accepted transition requires its consumed first-claim guard'
                            USING ERRCODE = '55000';
                    END IF;
                END IF;
            END IF;
            RETURN NEW;

        WHEN 'provider_operation_resources' THEN
            IF (to_jsonb(NEW) - ARRAY['observation', 'cleanup_state', 'last_receipt_sequence'])
               IS DISTINCT FROM
               (to_jsonb(OLD) - ARRAY['observation', 'cleanup_state', 'last_receipt_sequence']) THEN
                RAISE EXCEPTION 'provider resource identity and custody fields are immutable'
                    USING ERRCODE = '55000';
            END IF;
            IF NEW.last_receipt_sequence <> OLD.last_receipt_sequence + 1 THEN
                RAISE EXCEPTION 'provider resource receipt sequence must advance by exactly one receipt'
                    USING ERRCODE = '55000';
            END IF;
            RETURN NEW;
    END CASE;

    RAISE EXCEPTION 'unknown provider execution ledger table %', TG_TABLE_NAME
        USING ERRCODE = '55000';
END;
$$;

COMMENT ON COLUMN provider_credential_handles.custody_hash IS
    'Immutable exact digest of the versioned tenant/provider custody handle; legacy NULL rows are non-executable.';
COMMENT ON COLUMN provider_credential_handles.connection_hash IS
    'Immutable exact digest of the versioned provider connection handle; legacy NULL rows are non-executable.';
COMMENT ON COLUMN provider_credential_handles.valid_from IS
    'Immutable inclusive DB-time validity bound for new side-effecting claims.';
COMMENT ON COLUMN provider_credential_handles.valid_until IS
    'Immutable exclusive DB-time validity bound for new side-effecting claims.';
COMMENT ON COLUMN provider_operation_execution_claims.claim_access IS
    'Immutable per-head authority: read_only or side_effecting; legacy NULL claims cannot reactivate.';
