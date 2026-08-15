-- Append-only Operator Reconcile custody for an ambiguous at-most-once
-- provision dispatch. Discovery is read-only; a decision can never re-arm or
-- delete the immutable dispatch guard. Exactly one verified candidate may be
-- adopted through a receipt-head CAS without creating an execution claim.

CREATE TABLE IF NOT EXISTS provider_provision_discovery_observations (
    tenant_id text NOT NULL,
    operation_id text NOT NULL,
    observation_id uuid NOT NULL,
    lease_id text NOT NULL,
    lease_revision bigint NOT NULL CHECK (lease_revision > 0),
    server_id text NOT NULL,
    resource_generation_id uuid NOT NULL,
    head_sequence bigint NOT NULL CHECK (head_sequence > 0),
    head_receipt_digest text NOT NULL CHECK (head_receipt_digest ~ '^sha256:[0-9a-f]{64}$'),
    observed_resolution_revision bigint NOT NULL CHECK (observed_resolution_revision >= 0),
    adapter_manifest_hash text NOT NULL CHECK (adapter_manifest_hash ~ '^sha256:[0-9a-f]{64}$'),
    prepared_request_digest text NOT NULL CHECK (prepared_request_digest ~ '^sha256:[0-9a-f]{64}$'),
    credential_version_hash text NOT NULL CHECK (credential_version_hash ~ '^sha256:[0-9a-f]{64}$'),
    provider_scope_hash text NOT NULL CHECK (provider_scope_hash ~ '^sha256:[0-9a-f]{64}$'),
    correlation_hash text NOT NULL CHECK (correlation_hash ~ '^sha256:[0-9a-f]{64}$'),
    guarded_at timestamptz NOT NULL,
    candidate_count integer NOT NULL CHECK (candidate_count BETWEEN 0 AND 64),
    candidate_graphs_json jsonb NOT NULL,
    observation_ref text NOT NULL,
    observation_digest text NOT NULL CHECK (observation_digest ~ '^sha256:[0-9a-f]{64}$'),
    attestation_ref text NOT NULL,
    attestation_digest text NOT NULL CHECK (attestation_digest ~ '^sha256:[0-9a-f]{64}$'),
    collected_at timestamptz NOT NULL,
    requested_by_subject_id text NOT NULL,
    idempotency_key text NOT NULL,
    request_digest text NOT NULL CHECK (request_digest ~ '^sha256:[0-9a-f]{64}$'),
    snapshot_digest text NOT NULL CHECK (snapshot_digest ~ '^sha256:[0-9a-f]{64}$'),
    recorded_at timestamptz NOT NULL,
    PRIMARY KEY (tenant_id, operation_id, observation_id),
    UNIQUE (tenant_id, operation_id, idempotency_key),
    UNIQUE (tenant_id, operation_id, snapshot_digest),
    UNIQUE (tenant_id, operation_id, observation_ref),
    UNIQUE (tenant_id, operation_id, observation_digest),
    UNIQUE (tenant_id, operation_id, attestation_ref),
    UNIQUE (tenant_id, operation_id, attestation_digest),
    UNIQUE (tenant_id, operation_id, observation_id, snapshot_digest),
    FOREIGN KEY (tenant_id, operation_id)
        REFERENCES provider_operations (tenant_id, operation_id) ON DELETE RESTRICT,
    FOREIGN KEY (tenant_id, operation_id, head_sequence, head_receipt_digest)
        REFERENCES provider_provision_dispatch_guards (
            tenant_id, operation_id, head_sequence, head_receipt_digest
        ) ON DELETE RESTRICT,
    FOREIGN KEY (tenant_id, lease_id)
        REFERENCES techstack_vm_leases (tenant_id, id) ON UPDATE RESTRICT ON DELETE RESTRICT,
    CHECK (jsonb_typeof(candidate_graphs_json) = 'array'),
    CHECK (jsonb_array_length(candidate_graphs_json) = candidate_count)
);

CREATE TABLE IF NOT EXISTS provider_provision_resolution_decisions (
    tenant_id text NOT NULL,
    operation_id text NOT NULL,
    resolution_revision bigint NOT NULL CHECK (resolution_revision > 0),
    observation_id uuid NOT NULL,
    observation_snapshot_digest text NOT NULL CHECK (observation_snapshot_digest ~ '^sha256:[0-9a-f]{64}$'),
    expected_head_sequence bigint NOT NULL CHECK (expected_head_sequence > 0),
    expected_head_receipt_digest text NOT NULL CHECK (expected_head_receipt_digest ~ '^sha256:[0-9a-f]{64}$'),
    outcome text NOT NULL CHECK (outcome IN (
        'no_candidate_observed',
        'adopted_exact_candidate',
        'multiple_candidates_quarantined'
    )),
    selected_candidate_digest text CHECK (selected_candidate_digest ~ '^sha256:[0-9a-f]{64}$'),
    operator_subject_id text NOT NULL,
    operator_attestation_ref text NOT NULL,
    operator_attestation_digest text NOT NULL CHECK (operator_attestation_digest ~ '^sha256:[0-9a-f]{64}$'),
    idempotency_key text NOT NULL,
    request_digest text NOT NULL CHECK (request_digest ~ '^sha256:[0-9a-f]{64}$'),
    decision_digest text NOT NULL CHECK (decision_digest ~ '^sha256:[0-9a-f]{64}$'),
    result_receipt_sequence bigint CHECK (result_receipt_sequence > 0),
    result_receipt_digest text CHECK (result_receipt_digest ~ '^sha256:[0-9a-f]{64}$'),
    decision_token_digest text NOT NULL CHECK (decision_token_digest ~ '^[0-9a-f]{64}$'),
    decided_at timestamptz NOT NULL,
    PRIMARY KEY (tenant_id, operation_id, resolution_revision),
    UNIQUE (tenant_id, operation_id, observation_id),
    UNIQUE (tenant_id, operation_id, idempotency_key),
    UNIQUE (tenant_id, operation_id, decision_digest),
    UNIQUE (tenant_id, operation_id, operator_attestation_ref),
    UNIQUE (tenant_id, operation_id, operator_attestation_digest),
    FOREIGN KEY (tenant_id, operation_id)
        REFERENCES provider_operations (tenant_id, operation_id) ON DELETE RESTRICT,
    FOREIGN KEY (tenant_id, operation_id, observation_id, observation_snapshot_digest)
        REFERENCES provider_provision_discovery_observations (
            tenant_id, operation_id, observation_id, snapshot_digest
        ) ON DELETE RESTRICT,
    FOREIGN KEY (tenant_id, operation_id, result_receipt_sequence, result_receipt_digest)
        REFERENCES provider_operation_receipts (
            tenant_id, operation_id, sequence, receipt_digest
        ) DEFERRABLE INITIALLY DEFERRED,
    CHECK (
        (outcome = 'adopted_exact_candidate'
            AND selected_candidate_digest IS NOT NULL
            AND result_receipt_sequence IS NOT NULL
            AND result_receipt_digest IS NOT NULL)
        OR
        (outcome IN ('no_candidate_observed', 'multiple_candidates_quarantined')
            AND selected_candidate_digest IS NULL
            AND result_receipt_sequence IS NULL
            AND result_receipt_digest IS NULL)
    )
);

CREATE OR REPLACE FUNCTION provider_provision_resolution_reject_mutation()
RETURNS trigger
LANGUAGE plpgsql
SET search_path FROM CURRENT
AS $$
BEGIN
    RAISE EXCEPTION 'provider provision discovery and resolution rows are append-only'
        USING ERRCODE = '55000';
END;
$$;

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
        guard_prepared_digest,
        guard_credential_hash,
        guard_scope_hash,
        guard_correlation_hash,
        guard_manifest_hash,
        guard_origin,
        guard_guarded_at
    FROM provider_operations AS operation
    JOIN provider_provision_dispatch_guards AS guard
      ON guard.tenant_id = operation.tenant_id
     AND guard.operation_id = operation.operation_id
     AND guard.head_sequence = operation.head_sequence
     AND guard.head_receipt_digest = operation.head_receipt_digest
    WHERE operation.tenant_id = NEW.tenant_id
      AND operation.operation_id = NEW.operation_id
      AND operation.head_sequence = NEW.head_sequence
      AND operation.head_receipt_digest = NEW.head_receipt_digest
    FOR SHARE OF operation, guard;

    IF NOT FOUND
       OR operation_kind IS DISTINCT FROM 'provision'
       OR operation_status IS DISTINCT FROM 'pending'
       OR operation_phase IS DISTINCT FROM 'accepted'
       OR operation_mode IS DISTINCT FROM 'at_most_once_dispatch_manual_reconcile'
       OR guard_origin IS DISTINCT FROM 'first_claim'
       OR guard_prepared_digest IS NULL THEN
        RAISE EXCEPTION 'provision discovery requires the exact guarded AMO accepted head'
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
       OR live_cancelled_at IS NOT NULL
       OR live_desired_state NOT IN ('running', 'stopped')
       OR server_lease_id IS DISTINCT FROM operation_lease_id
       OR server_desired_state = 'absent'
       OR server_lifecycle_state IN ('decommissioning', 'decommissioned')
       OR server_decommissioned_at IS NOT NULL THEN
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

    IF terminal_resolution OR NEW.observed_resolution_revision IS DISTINCT FROM current_resolution_revision THEN
        RAISE EXCEPTION 'provision discovery resolution revision is stale or terminal'
            USING ERRCODE = '40001';
    END IF;

    PERFORM 1
    FROM provider_operation_execution_claims AS claim
    WHERE claim.tenant_id = NEW.tenant_id
      AND claim.operation_id = NEW.operation_id
      AND claim.head_sequence = NEW.head_sequence
      AND claim.head_receipt_digest = NEW.head_receipt_digest
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

    SELECT operation.operation, operation.status, operation.phase, operation.provision_dispatch_mode
    INTO operation_kind, operation_status, operation_phase, operation_mode
    FROM provider_operations AS operation
    WHERE operation.tenant_id = NEW.tenant_id
      AND operation.operation_id = NEW.operation_id
      AND operation.head_sequence = NEW.expected_head_sequence
      AND operation.head_receipt_digest = NEW.expected_head_receipt_digest
    FOR SHARE OF operation;
    IF NOT FOUND
       OR operation_kind IS DISTINCT FROM 'provision'
       OR operation_status IS DISTINCT FROM 'pending'
       OR operation_phase IS DISTINCT FROM 'accepted'
       OR operation_mode IS DISTINCT FROM 'at_most_once_dispatch_manual_reconcile'
       OR NEW.expected_head_sequence IS DISTINCT FROM observation.head_sequence
       OR NEW.expected_head_receipt_digest IS DISTINCT FROM observation.head_receipt_digest THEN
        RAISE EXCEPTION 'provision resolution expected head is stale or not AMO accepted'
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
    IF NOT FOUND
       OR live_lease_revision IS DISTINCT FROM observation.lease_revision
       OR live_server_id IS DISTINCT FROM observation.server_id
       OR live_generation_id IS DISTINCT FROM observation.resource_generation_id
       OR live_cancelled_at IS NOT NULL
       OR live_desired_state NOT IN ('running', 'stopped')
       OR server_lease_id IS DISTINCT FROM observation.lease_id
       OR server_desired_state = 'absent'
       OR server_lifecycle_state IN ('decommissioning', 'decommissioned')
       OR server_decommissioned_at IS NOT NULL THEN
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

    derived_outcome := CASE
        WHEN observation.candidate_count = 0 THEN 'no_candidate_observed'
        WHEN observation.candidate_count = 1 THEN 'adopted_exact_candidate'
        ELSE 'multiple_candidates_quarantined'
    END;
    IF NEW.outcome IS DISTINCT FROM derived_outcome THEN
        RAISE EXCEPTION 'provision resolution outcome must be server-derived from candidate count'
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

    IF bound_token_digest IS NULL OR NEW.decision_token_digest IS DISTINCT FROM bound_token_digest THEN
        RAISE EXCEPTION 'provision resolution is not bound to its transaction capability'
            USING ERRCODE = '55000';
    END IF;

    NEW.decided_at := clock_timestamp();
    RETURN NEW;
END;
$$;

CREATE TRIGGER provider_provision_discovery_observations_validate_insert
BEFORE INSERT ON provider_provision_discovery_observations
FOR EACH ROW EXECUTE FUNCTION provider_provision_discovery_validate_insert();
CREATE TRIGGER provider_provision_discovery_observations_reject_update
BEFORE UPDATE ON provider_provision_discovery_observations
FOR EACH ROW EXECUTE FUNCTION provider_provision_resolution_reject_mutation();
CREATE TRIGGER provider_provision_discovery_observations_reject_delete
BEFORE DELETE ON provider_provision_discovery_observations
FOR EACH ROW EXECUTE FUNCTION provider_provision_resolution_reject_mutation();

CREATE TRIGGER provider_provision_resolution_decisions_validate_insert
BEFORE INSERT ON provider_provision_resolution_decisions
FOR EACH ROW EXECUTE FUNCTION provider_provision_resolution_validate_insert();
CREATE TRIGGER provider_provision_resolution_decisions_reject_update
BEFORE UPDATE ON provider_provision_resolution_decisions
FOR EACH ROW EXECUTE FUNCTION provider_provision_resolution_reject_mutation();
CREATE TRIGGER provider_provision_resolution_decisions_reject_delete
BEFORE DELETE ON provider_provision_resolution_decisions
FOR EACH ROW EXECUTE FUNCTION provider_provision_resolution_reject_mutation();

ALTER TABLE provider_provision_discovery_observations ENABLE ROW LEVEL SECURITY;
ALTER TABLE provider_provision_discovery_observations FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON provider_provision_discovery_observations
    USING (tenant_id = current_setting('app.tenant_id', true))
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true));

ALTER TABLE provider_provision_resolution_decisions ENABLE ROW LEVEL SECURITY;
ALTER TABLE provider_provision_resolution_decisions FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON provider_provision_resolution_decisions
    USING (tenant_id = current_setting('app.tenant_id', true))
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true));

-- Replace migration 029's operation update guard with one narrowly scoped
-- local adoption exception. Every adapter-owned transition still requires its
-- consumed claim. The exception never changes or recreates dispatch custody.
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

            operator_adoption :=
                OLD.operation = 'provision'
                AND OLD.provision_dispatch_mode = 'at_most_once_dispatch_manual_reconcile'
                AND OLD.status = 'pending'
                AND OLD.phase = 'accepted'
                AND NEW.status = 'pending'
                AND NEW.phase = 'resources_bound';

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

REVOKE ALL ON FUNCTION provider_provision_resolution_reject_mutation() FROM PUBLIC;
REVOKE ALL ON FUNCTION provider_provision_discovery_validate_insert() FROM PUBLIC;
REVOKE ALL ON FUNCTION provider_provision_resolution_validate_insert() FROM PUBLIC;
REVOKE ALL ON FUNCTION provider_execution_immutable_update() FROM PUBLIC;

COMMENT ON TABLE provider_provision_discovery_observations IS
    'Immutable, secret-free read-only candidate observations for a guarded AMO provision head; presence never authorizes dispatch.';
COMMENT ON TABLE provider_provision_resolution_decisions IS
    'Append-only Operator Reconcile decisions; only exact-one adoption may CAS local custody to resources_bound and no decision can re-arm dispatch.';
