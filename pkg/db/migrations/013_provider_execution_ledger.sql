-- TechStack-owned provider execution custody.
--
-- Provider adapters and credentials remain behind TechStack's execution
-- profile resolver. StackKits never writes these tables, and Simulate is only
-- an optional adapter/test consumer. Native references are retained after
-- cleanup so failed and absent resources remain auditable.

CREATE TABLE IF NOT EXISTS provider_desired_spec_revisions (
    tenant_id text NOT NULL,
    lease_id text NOT NULL,
    revision bigint NOT NULL CHECK (revision > 0),
    spec_ref text NOT NULL,
    spec_digest text NOT NULL,
    spec_json jsonb NOT NULL,
    created_at timestamptz NOT NULL,
    PRIMARY KEY (tenant_id, lease_id, revision),
    UNIQUE (tenant_id, spec_ref),
    FOREIGN KEY (tenant_id) REFERENCES techstack_tenants (id) ON DELETE RESTRICT
);

CREATE TABLE IF NOT EXISTS provider_operations (
    tenant_id text NOT NULL,
    operation_id text NOT NULL,
    lease_id text NOT NULL,
    operation text NOT NULL,
    idempotency_key text NOT NULL,
    adapter_id text NOT NULL,
    command_digest text NOT NULL,
    command_json jsonb NOT NULL,
    ledger_revision bigint NOT NULL CHECK (ledger_revision > 0),
    desired_spec_revision bigint,
    status text NOT NULL,
    phase text NOT NULL,
    head_sequence bigint NOT NULL CHECK (head_sequence > 0),
    head_receipt_digest text NOT NULL,
    requested_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    PRIMARY KEY (tenant_id, operation_id),
    UNIQUE (tenant_id, lease_id, operation, idempotency_key),
    UNIQUE (tenant_id, operation_id, command_digest),
    FOREIGN KEY (tenant_id) REFERENCES techstack_tenants (id) ON DELETE RESTRICT,
    FOREIGN KEY (tenant_id, lease_id, desired_spec_revision)
        REFERENCES provider_desired_spec_revisions (tenant_id, lease_id, revision),
    CHECK (operation IN ('plan', 'provision', 'observe', 'reconcile', 'decommission')),
    CHECK (status IN ('pending', 'succeeded', 'failed', 'denied')),
    CHECK (phase IN (
        'requested', 'accepted', 'planned', 'resources_bound', 'present',
        'delete_accepted', 'absence_pending', 'absent', 'failed', 'denied'
    )),
    CHECK (
        (operation IN ('plan', 'provision', 'reconcile') AND desired_spec_revision IS NOT NULL)
        OR (operation IN ('observe', 'decommission') AND desired_spec_revision IS NULL)
    )
);

CREATE INDEX IF NOT EXISTS idx_provider_operations_lease
    ON provider_operations (tenant_id, lease_id, updated_at DESC);
CREATE INDEX IF NOT EXISTS idx_provider_operations_active
    ON provider_operations (tenant_id, phase, updated_at)
    WHERE phase NOT IN ('planned', 'present', 'absent', 'failed', 'denied');

CREATE TABLE IF NOT EXISTS provider_operation_receipts (
    tenant_id text NOT NULL,
    operation_id text NOT NULL,
    sequence bigint NOT NULL CHECK (sequence > 0),
    previous_receipt_digest text,
    receipt_digest text NOT NULL,
    status text NOT NULL,
    phase text NOT NULL,
    receipt_json jsonb NOT NULL,
    issued_at timestamptz NOT NULL,
    PRIMARY KEY (tenant_id, operation_id, sequence),
    UNIQUE (tenant_id, operation_id, receipt_digest),
    FOREIGN KEY (tenant_id, operation_id)
        REFERENCES provider_operations (tenant_id, operation_id) ON DELETE RESTRICT,
    FOREIGN KEY (tenant_id, operation_id, previous_receipt_digest)
        REFERENCES provider_operation_receipts (tenant_id, operation_id, receipt_digest) ON DELETE RESTRICT,
    CHECK (status IN ('pending', 'succeeded', 'failed', 'denied')),
    CHECK (phase IN (
        'requested', 'accepted', 'planned', 'resources_bound', 'present',
        'delete_accepted', 'absence_pending', 'absent', 'failed', 'denied'
    )),
    CHECK (
        (sequence = 1 AND previous_receipt_digest IS NULL AND phase = 'requested' AND status = 'pending')
        OR (sequence > 1 AND previous_receipt_digest IS NOT NULL AND phase <> 'requested')
    )
);

CREATE TABLE IF NOT EXISTS provider_operation_resources (
    tenant_id text NOT NULL,
    operation_id text NOT NULL,
    binding_id text NOT NULL,
    kind text NOT NULL,
    native_ref text NOT NULL,
    parent_binding_id text,
    ownership_hash text NOT NULL,
    disposition text NOT NULL,
    observation text NOT NULL,
    cleanup_state text NOT NULL,
    first_receipt_sequence bigint NOT NULL CHECK (first_receipt_sequence > 0),
    last_receipt_sequence bigint NOT NULL CHECK (last_receipt_sequence >= first_receipt_sequence),
    PRIMARY KEY (tenant_id, operation_id, binding_id),
    FOREIGN KEY (tenant_id, operation_id)
        REFERENCES provider_operations (tenant_id, operation_id) ON DELETE RESTRICT,
    FOREIGN KEY (tenant_id, operation_id, first_receipt_sequence)
        REFERENCES provider_operation_receipts (tenant_id, operation_id, sequence) ON DELETE RESTRICT,
    FOREIGN KEY (tenant_id, operation_id, last_receipt_sequence)
        REFERENCES provider_operation_receipts (tenant_id, operation_id, sequence) ON DELETE RESTRICT,
    CHECK (disposition = 'delete'),
    CHECK (observation IN ('present', 'absent', 'unknown')),
    CHECK (cleanup_state IN ('not_required', 'required', 'pending', 'complete', 'blocked'))
);

CREATE INDEX IF NOT EXISTS idx_provider_resources_cleanup
    ON provider_operation_resources (tenant_id, cleanup_state, last_receipt_sequence)
    WHERE cleanup_state IN ('required', 'pending', 'blocked');

CREATE TABLE IF NOT EXISTS provider_operation_evidence (
    tenant_id text NOT NULL,
    operation_id text NOT NULL,
    binding_id text NOT NULL,
    evidence_ref text NOT NULL,
    evidence_digest text NOT NULL,
    attestation_ref text NOT NULL,
    attestation_digest text NOT NULL,
    observation text NOT NULL,
    source text NOT NULL,
    definitive boolean NOT NULL,
    collected_at timestamptz NOT NULL,
    first_receipt_sequence bigint NOT NULL CHECK (first_receipt_sequence > 0),
    evidence_json jsonb NOT NULL,
    PRIMARY KEY (tenant_id, operation_id, evidence_ref),
    UNIQUE (tenant_id, operation_id, evidence_digest),
    UNIQUE (tenant_id, operation_id, attestation_ref),
    UNIQUE (tenant_id, operation_id, attestation_digest),
    FOREIGN KEY (tenant_id, operation_id, binding_id)
        REFERENCES provider_operation_resources (tenant_id, operation_id, binding_id) ON DELETE RESTRICT,
    FOREIGN KEY (tenant_id, operation_id, first_receipt_sequence)
        REFERENCES provider_operation_receipts (tenant_id, operation_id, sequence) ON DELETE RESTRICT,
    CHECK (observation IN ('present', 'absent', 'unknown')),
    CHECK (source IN ('provider_api', 'provider_event', 'adapter')),
    CHECK (definitive)
);

-- The application validates the providerexecutor append chain before every
-- write. These triggers make the durable ledger fail closed as well: a tenant
-- scoped SQL caller cannot rewrite custody after it has been accepted. Exact
-- no-op updates remain valid because PostgreSQL uses them for the idempotent
-- ON CONFLICT paths below the coordinator.
CREATE OR REPLACE FUNCTION provider_execution_immutable_update()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF to_jsonb(NEW) IS NOT DISTINCT FROM to_jsonb(OLD) THEN
        RETURN NEW;
    END IF;

    CASE TG_TABLE_NAME
        WHEN 'provider_desired_spec_revisions', 'provider_operation_receipts', 'provider_operation_evidence' THEN
            RAISE EXCEPTION 'provider execution ledger row in % is immutable', TG_TABLE_NAME
                USING ERRCODE = '55000';

        WHEN 'provider_operations' THEN
            IF (to_jsonb(NEW) - ARRAY['status', 'phase', 'head_sequence', 'head_receipt_digest', 'updated_at'])
               IS DISTINCT FROM
               (to_jsonb(OLD) - ARRAY['status', 'phase', 'head_sequence', 'head_receipt_digest', 'updated_at']) THEN
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

CREATE OR REPLACE FUNCTION provider_execution_reject_delete()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION 'provider execution ledger rows are append-only and cannot be deleted'
        USING ERRCODE = '55000';
END;
$$;

CREATE TRIGGER provider_desired_spec_revisions_immutable_update
BEFORE UPDATE ON provider_desired_spec_revisions
FOR EACH ROW EXECUTE FUNCTION provider_execution_immutable_update();
CREATE TRIGGER provider_operations_immutable_update
BEFORE UPDATE ON provider_operations
FOR EACH ROW EXECUTE FUNCTION provider_execution_immutable_update();
CREATE TRIGGER provider_operation_receipts_immutable_update
BEFORE UPDATE ON provider_operation_receipts
FOR EACH ROW EXECUTE FUNCTION provider_execution_immutable_update();
CREATE TRIGGER provider_operation_resources_immutable_update
BEFORE UPDATE ON provider_operation_resources
FOR EACH ROW EXECUTE FUNCTION provider_execution_immutable_update();
CREATE TRIGGER provider_operation_evidence_immutable_update
BEFORE UPDATE ON provider_operation_evidence
FOR EACH ROW EXECUTE FUNCTION provider_execution_immutable_update();

CREATE TRIGGER provider_desired_spec_revisions_reject_delete
BEFORE DELETE ON provider_desired_spec_revisions
FOR EACH ROW EXECUTE FUNCTION provider_execution_reject_delete();
CREATE TRIGGER provider_operations_reject_delete
BEFORE DELETE ON provider_operations
FOR EACH ROW EXECUTE FUNCTION provider_execution_reject_delete();
CREATE TRIGGER provider_operation_receipts_reject_delete
BEFORE DELETE ON provider_operation_receipts
FOR EACH ROW EXECUTE FUNCTION provider_execution_reject_delete();
CREATE TRIGGER provider_operation_resources_reject_delete
BEFORE DELETE ON provider_operation_resources
FOR EACH ROW EXECUTE FUNCTION provider_execution_reject_delete();
CREATE TRIGGER provider_operation_evidence_reject_delete
BEFORE DELETE ON provider_operation_evidence
FOR EACH ROW EXECUTE FUNCTION provider_execution_reject_delete();

DO $$
DECLARE
    table_name text;
BEGIN
    FOREACH table_name IN ARRAY ARRAY[
        'provider_desired_spec_revisions',
        'provider_operations',
        'provider_operation_receipts',
        'provider_operation_resources',
        'provider_operation_evidence'
    ] LOOP
        EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', table_name);
        EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', table_name);
        EXECUTE format('DROP POLICY IF EXISTS tenant_isolation ON %I', table_name);
        EXECUTE format(
            'CREATE POLICY tenant_isolation ON %I USING (tenant_id = current_setting(''app.tenant_id'', true)) WITH CHECK (tenant_id = current_setting(''app.tenant_id'', true))',
            table_name
        );
    END LOOP;
END $$;
