-- Head-specific provider provision dispatch custody.
--
-- A dispatch mode is an immutable catalog safety pin copied to every provider
-- operation, including non-provision work. Every historical operation is
-- deliberately blocked because migration-time state cannot prove its custody.
-- The at-most-once mode has execution semantics only for provision accepted
-- heads: it guarantees at most one TechStack dispatch decision, not at most one
-- provider resource. Its immutable guard is committed in the same transaction
-- as the first accepted-head claim and is never reset.

SET LOCAL lock_timeout = '5s';
SELECT pg_catalog.set_config(
    'search_path',
    pg_catalog.quote_ident(pg_catalog.current_schema()) || ', pg_catalog, pg_temp',
    true
);

ALTER TABLE provider_catalog_profiles
    ADD COLUMN IF NOT EXISTS provision_dispatch_mode text;
ALTER TABLE provider_catalog_profiles
    ADD COLUMN IF NOT EXISTS adapter_manifest_hash text;

-- Published profiles predate the provider API audit and cannot be upgraded in
-- place. They remain immutable quarantine records; a new draft catalog version
-- must choose an executable mode explicitly before publication.
ALTER TABLE provider_catalog_profiles
    DISABLE TRIGGER provider_catalog_profiles_reject_update;
UPDATE provider_catalog_profiles
SET provision_dispatch_mode = 'blocked'
WHERE provision_dispatch_mode IS NULL;
ALTER TABLE provider_catalog_profiles
    ENABLE TRIGGER provider_catalog_profiles_reject_update;

ALTER TABLE provider_catalog_profiles
    ALTER COLUMN provision_dispatch_mode SET NOT NULL;
ALTER TABLE provider_catalog_profiles
    DROP CONSTRAINT IF EXISTS provider_catalog_profiles_provision_dispatch_mode_check;
ALTER TABLE provider_catalog_profiles
    ADD CONSTRAINT provider_catalog_profiles_provision_dispatch_mode_check
    CHECK (provision_dispatch_mode IN (
        'blocked',
        'native_idempotency',
        'provider_correlation',
        'at_most_once_dispatch_manual_reconcile'
    ));
ALTER TABLE provider_catalog_profiles
    DROP CONSTRAINT IF EXISTS provider_catalog_profiles_adapter_manifest_hash_check;
ALTER TABLE provider_catalog_profiles
    ADD CONSTRAINT provider_catalog_profiles_adapter_manifest_hash_check
    CHECK (
        adapter_manifest_hash IS NULL
        OR adapter_manifest_hash ~ '^sha256:[0-9a-f]{64}$'
    );

ALTER TABLE provider_operations
    ADD COLUMN IF NOT EXISTS provision_dispatch_mode text;

-- Every existing operation is ambiguous regardless of kind or current phase,
-- so it is quarantined as blocked. Every new operation instead copies its exact
-- immutable profile pin. Temporarily suspend only the migration-owner guards;
-- the enclosing migration transaction restores them or rolls back atomically.
ALTER TABLE provider_operations
    DISABLE TRIGGER provider_operations_immutable_update;
ALTER TABLE provider_operations NO FORCE ROW LEVEL SECURITY;
ALTER TABLE provider_operations DISABLE ROW LEVEL SECURITY;
UPDATE provider_operations
SET provision_dispatch_mode = 'blocked'
WHERE provision_dispatch_mode IS NULL;
ALTER TABLE provider_operations ENABLE ROW LEVEL SECURITY;
ALTER TABLE provider_operations FORCE ROW LEVEL SECURITY;
ALTER TABLE provider_operations
    ENABLE TRIGGER provider_operations_immutable_update;

ALTER TABLE provider_operations
    ALTER COLUMN provision_dispatch_mode SET NOT NULL;
ALTER TABLE provider_operations
    DROP CONSTRAINT IF EXISTS provider_operations_provision_dispatch_mode_check;
ALTER TABLE provider_operations
    ADD CONSTRAINT provider_operations_provision_dispatch_mode_check
    CHECK (provision_dispatch_mode IN (
        'blocked',
        'native_idempotency',
        'provider_correlation',
        'at_most_once_dispatch_manual_reconcile'
    ));

-- Migration 027 already makes profile rows immutable after INSERT. Replace its
-- insert guard so `blocked` can exist only as the migration backfill above;
-- every new draft profile must declare an executable dispatch guarantee.
CREATE OR REPLACE FUNCTION provider_catalog_profile_insert_guard()
RETURNS trigger
LANGUAGE plpgsql
SET search_path FROM CURRENT
AS $$
DECLARE
    version_status text;
BEGIN
    SELECT status INTO version_status
    FROM provider_catalog_versions
    WHERE catalog_version = NEW.catalog_version
    FOR UPDATE;
    IF version_status IS DISTINCT FROM 'draft' THEN
        RAISE EXCEPTION 'provider catalog profiles may be added only to a draft version'
            USING ERRCODE = '55000';
    END IF;
    IF NEW.provision_dispatch_mode NOT IN (
        'native_idempotency',
        'provider_correlation',
        'at_most_once_dispatch_manual_reconcile'
    ) THEN
        RAISE EXCEPTION 'new provider catalog profile requires an executable provision dispatch mode'
            USING ERRCODE = '55000';
    END IF;
    IF NEW.adapter_manifest_hash IS NULL
       OR NEW.adapter_manifest_hash !~ '^sha256:[0-9a-f]{64}$' THEN
        RAISE EXCEPTION 'new provider catalog profile requires an immutable adapter manifest digest'
            USING ERRCODE = '55000';
    END IF;
    RETURN NEW;
END;
$$;

-- New operation envelopes copy the immutable catalog pin. Historical rows
-- were backfilled before this trigger and remain blocked. The catalog lookup
-- prevents a caller from claiming a stronger mode in command_json than the
-- exact provider/profile/offering publication selected by the resolver.
CREATE OR REPLACE FUNCTION provider_operation_dispatch_mode_insert_guard()
RETURNS trigger
LANGUAGE plpgsql
SET search_path FROM CURRENT
AS $$
DECLARE
    catalog_dispatch_mode text;
    snapshot_dispatch_mode text;
    catalog_adapter_manifest_hash text;
    snapshot_adapter_manifest_hash text;
BEGIN
    IF NEW.command_json->>'schema_version' IS DISTINCT FROM 'techstack.provider-control-operation/v1'
       OR NEW.command_json->>'execution_authority' IS DISTINCT FROM 'techstack_provider_control' THEN
        RAISE EXCEPTION 'new provider operation requires the native provider-control envelope'
            USING ERRCODE = '55000';
    END IF;

    snapshot_dispatch_mode := NEW.command_json #>> '{execution_profile,provision_dispatch_mode}';
    snapshot_adapter_manifest_hash := NEW.command_json #>> '{execution_profile,adapter_manifest_hash}';
    IF NEW.provision_dispatch_mode = 'blocked'
       OR snapshot_dispatch_mode IS DISTINCT FROM NEW.provision_dispatch_mode
       OR snapshot_adapter_manifest_hash IS NULL
       OR snapshot_adapter_manifest_hash !~ '^sha256:[0-9a-f]{64}$' THEN
        RAISE EXCEPTION 'provider operation must copy an executable catalog dispatch-mode pin'
            USING ERRCODE = '55000';
    END IF;

    SELECT profile.provision_dispatch_mode, profile.adapter_manifest_hash
    INTO catalog_dispatch_mode, catalog_adapter_manifest_hash
    FROM provider_catalog_profiles AS profile
    WHERE profile.catalog_version = NEW.command_json #>> '{execution_profile,catalog_version}'
      AND profile.provider_id = NEW.command_json #>> '{execution_profile,provider_id}'
      AND profile.adapter_id = NEW.command_json #>> '{execution_profile,adapter_id}'
      AND profile.credential_mode = NEW.command_json #>> '{execution_profile,credential_mode}'
      AND profile.runtime_profile_id = NEW.command_json #>> '{execution_profile,runtime_profile_id}'
      AND profile.offering_id = NEW.command_json #>> '{execution_profile,offering_id}';
    IF catalog_dispatch_mode IS NULL
       OR catalog_dispatch_mode IS DISTINCT FROM NEW.provision_dispatch_mode
       OR catalog_adapter_manifest_hash IS DISTINCT FROM snapshot_adapter_manifest_hash THEN
        RAISE EXCEPTION 'provider operation dispatch mode does not match its immutable catalog profile'
            USING ERRCODE = '55000';
    END IF;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS provider_operations_dispatch_mode_insert_guard
    ON provider_operations;
CREATE TRIGGER provider_operations_dispatch_mode_insert_guard
BEFORE INSERT ON provider_operations
FOR EACH ROW EXECUTE FUNCTION provider_operation_dispatch_mode_insert_guard();

CREATE TABLE IF NOT EXISTS provider_provision_dispatch_guards (
    tenant_id text NOT NULL,
    operation_id text NOT NULL,
    lease_id text NOT NULL,
    lease_revision bigint NOT NULL
        CHECK (lease_revision BETWEEN 1 AND 9007199254740991),
    server_id text NOT NULL CHECK (server_id <> ''),
    resource_generation_id uuid NOT NULL,
    head_sequence bigint NOT NULL CHECK (head_sequence > 0),
    head_receipt_digest text NOT NULL
        CHECK (head_receipt_digest ~ '^sha256:[0-9a-f]{64}$'),
    capability_snapshot_hash text NOT NULL
        CHECK (capability_snapshot_hash ~ '^sha256:[0-9a-f]{64}$'),
    execution_profile_hash text NOT NULL
        CHECK (execution_profile_hash ~ '^sha256:[0-9a-f]{64}$'),
    dispatch_mode text NOT NULL
        CHECK (dispatch_mode IN (
            'native_idempotency',
            'provider_correlation',
            'at_most_once_dispatch_manual_reconcile'
        )),
    prepared_request_digest text
        CHECK (prepared_request_digest IS NULL OR prepared_request_digest ~ '^sha256:[0-9a-f]{64}$'),
    credential_version_hash text
        CHECK (credential_version_hash IS NULL OR credential_version_hash ~ '^sha256:[0-9a-f]{64}$'),
    provider_scope_hash text
        CHECK (provider_scope_hash IS NULL OR provider_scope_hash ~ '^sha256:[0-9a-f]{64}$'),
    correlation_hash text
        CHECK (correlation_hash IS NULL OR correlation_hash ~ '^sha256:[0-9a-f]{64}$'),
    adapter_manifest_hash text
        CHECK (adapter_manifest_hash IS NULL OR adapter_manifest_hash ~ '^sha256:[0-9a-f]{64}$'),
    guard_origin text NOT NULL
        CHECK (guard_origin IN ('first_claim', 'migration_quarantine')),
    first_claim_token_digest text,
    first_claim_owner text,
    guarded_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (tenant_id, operation_id),
    UNIQUE (tenant_id, operation_id, head_sequence, head_receipt_digest),
    UNIQUE (tenant_id, lease_id, resource_generation_id),
    FOREIGN KEY (tenant_id, operation_id, lease_id)
        REFERENCES provider_operations (tenant_id, operation_id, lease_id)
        ON UPDATE RESTRICT ON DELETE RESTRICT,
    FOREIGN KEY (tenant_id, operation_id, head_sequence, head_receipt_digest)
        REFERENCES provider_operation_receipts (
            tenant_id, operation_id, sequence, receipt_digest
        ) ON UPDATE RESTRICT ON DELETE RESTRICT,
    FOREIGN KEY (tenant_id, lease_id)
        REFERENCES techstack_vm_leases (tenant_id, id)
        ON UPDATE RESTRICT ON DELETE RESTRICT,
    CHECK (
        (
            guard_origin = 'first_claim'
            AND first_claim_token_digest ~ '^[0-9a-f]{64}$'
            AND first_claim_owner IS NOT NULL
            AND first_claim_owner <> ''
            AND adapter_manifest_hash IS NOT NULL
            AND (
                (
                    dispatch_mode = 'at_most_once_dispatch_manual_reconcile'
                    AND prepared_request_digest IS NOT NULL
                    AND credential_version_hash IS NOT NULL
                    AND provider_scope_hash IS NOT NULL
                    AND correlation_hash IS NOT NULL
                )
                OR (
                    dispatch_mode IN ('native_idempotency', 'provider_correlation')
                    AND prepared_request_digest IS NULL
                    AND credential_version_hash IS NULL
                    AND provider_scope_hash IS NULL
                    AND correlation_hash IS NULL
                )
            )
        )
        OR (
            guard_origin = 'migration_quarantine'
            AND dispatch_mode = 'at_most_once_dispatch_manual_reconcile'
            AND prepared_request_digest IS NULL
            AND credential_version_hash IS NULL
            AND provider_scope_hash IS NULL
            AND correlation_hash IS NULL
            AND adapter_manifest_hash IS NULL
            AND first_claim_token_digest IS NULL
            AND first_claim_owner IS NULL
        )
    )
);

-- Permit an idempotent migration rerun to backfill before the runtime INSERT
-- guard is restored. A native provision which ever entered accepted already
-- crossed executor custody and must never be inferred safe from its later
-- phase. Malformed/missing typed lease bindings or duplicate lease-generation
-- dispatches intentionally abort this migration rather than weakening the FK.
DROP TRIGGER IF EXISTS provider_provision_dispatch_guards_validate_insert
    ON provider_provision_dispatch_guards;

INSERT INTO provider_provision_dispatch_guards (
    tenant_id,
    operation_id,
    lease_id,
    lease_revision,
    server_id,
    resource_generation_id,
    head_sequence,
    head_receipt_digest,
    capability_snapshot_hash,
    execution_profile_hash,
    dispatch_mode,
    prepared_request_digest,
    credential_version_hash,
    provider_scope_hash,
    correlation_hash,
    adapter_manifest_hash,
    guard_origin,
    first_claim_token_digest,
    first_claim_owner,
    guarded_at
)
SELECT
    operation.tenant_id,
    operation.operation_id,
    operation.lease_id,
    (operation.command_json #>> '{command,lease_revision}')::bigint,
    operation.command_json #>> '{command,runtime_server_id}',
    (operation.command_json #>> '{command,resource_generation_id}')::uuid,
    accepted.sequence,
    accepted.receipt_digest,
    operation.command_json #>> '{execution_profile,capability_snapshot_hash}',
    operation.command_json #>> '{execution_profile,execution_profile_hash}',
    'at_most_once_dispatch_manual_reconcile',
    NULL,
    NULL,
    NULL,
    NULL,
    NULL,
    'migration_quarantine',
    NULL,
    NULL,
    clock_timestamp()
FROM provider_operations AS operation
JOIN LATERAL (
    SELECT receipt.sequence, receipt.receipt_digest
    FROM provider_operation_receipts AS receipt
    WHERE receipt.tenant_id = operation.tenant_id
      AND receipt.operation_id = operation.operation_id
      AND receipt.phase = 'accepted'
    ORDER BY receipt.sequence ASC
    LIMIT 1
) AS accepted ON true
WHERE operation.operation = 'provision'
  AND operation.command_json->>'schema_version' = 'techstack.provider-control-operation/v1'
  AND operation.command_json->>'execution_authority' = 'techstack_provider_control'
ON CONFLICT (tenant_id, operation_id) DO NOTHING;

CREATE OR REPLACE FUNCTION provider_provision_dispatch_guard_validate_insert()
RETURNS trigger
LANGUAGE plpgsql
SET search_path FROM CURRENT
AS $$
DECLARE
    operation_kind text;
    operation_mode text;
    operation_lease_id text;
    operation_schema_version text;
    operation_execution_authority text;
    command_lease_revision text;
    command_server_id text;
    command_resource_generation_id text;
    command_capability_snapshot_hash text;
    command_execution_profile_hash text;
    command_custody_hash text;
    command_connection_hash text;
    profile_capability_snapshot_hash text;
    profile_execution_profile_hash text;
    profile_adapter_manifest_hash text;
    receipt_status text;
    receipt_phase text;
    receipt_resources jsonb;
    live_lease_revision bigint;
    live_server_id text;
    live_resource_generation_id uuid;
    bound_claim_token_digest text := CASE
        WHEN nullif(current_setting('app.provider_execution_claim_token', true), '') IS NULL THEN NULL
        ELSE encode(
            sha256(convert_to(current_setting('app.provider_execution_claim_token', true), 'UTF8')),
            'hex'
        )
    END;
    bound_claim_owner text := nullif(
        current_setting('app.provider_execution_claim_owner', true),
        ''
    );
BEGIN
    IF NEW.guard_origin IS DISTINCT FROM 'first_claim' THEN
        RAISE EXCEPTION 'runtime dispatch guards must originate from the first claim'
            USING ERRCODE = '55000';
    END IF;

    -- Serialize every first provision-custody decision for this generation,
    -- including crash-recoverable and AMO operation IDs.
    PERFORM pg_advisory_xact_lock(
        hashtext(NEW.tenant_id),
        hashtext(NEW.lease_id || ':' || NEW.resource_generation_id::text)
    );

    SELECT
        operation.operation,
        operation.provision_dispatch_mode,
        operation.lease_id,
        operation.command_json->>'schema_version',
        operation.command_json->>'execution_authority',
        operation.command_json #>> '{command,lease_revision}',
        operation.command_json #>> '{command,runtime_server_id}',
        operation.command_json #>> '{command,resource_generation_id}',
        operation.command_json #>> '{command,capability_snapshot_hash}',
        operation.command_json #>> '{command,execution_profile_hash}',
        operation.command_json #>> '{command,custody_hash}',
        operation.command_json #>> '{command,connection_hash}',
        operation.command_json #>> '{execution_profile,capability_snapshot_hash}',
        operation.command_json #>> '{execution_profile,execution_profile_hash}',
        operation.command_json #>> '{execution_profile,adapter_manifest_hash}',
        receipt.status,
        receipt.phase,
        receipt.receipt_json->'resources'
    INTO
        operation_kind,
        operation_mode,
        operation_lease_id,
        operation_schema_version,
        operation_execution_authority,
        command_lease_revision,
        command_server_id,
        command_resource_generation_id,
        command_capability_snapshot_hash,
        command_execution_profile_hash,
        command_custody_hash,
        command_connection_hash,
        profile_capability_snapshot_hash,
        profile_execution_profile_hash,
        profile_adapter_manifest_hash,
        receipt_status,
        receipt_phase,
        receipt_resources
    FROM provider_operations AS operation
    JOIN provider_operation_receipts AS receipt
      ON receipt.tenant_id = operation.tenant_id
     AND receipt.operation_id = operation.operation_id
     AND receipt.sequence = operation.head_sequence
     AND receipt.receipt_digest = operation.head_receipt_digest
    WHERE operation.tenant_id = NEW.tenant_id
      AND operation.operation_id = NEW.operation_id
      AND operation.head_sequence = NEW.head_sequence
      AND operation.head_receipt_digest = NEW.head_receipt_digest
    FOR SHARE OF operation, receipt;

    IF NOT FOUND
       OR operation_kind IS DISTINCT FROM 'provision'
       OR operation_mode IS DISTINCT FROM NEW.dispatch_mode
       OR operation_mode NOT IN (
            'native_idempotency',
            'provider_correlation',
            'at_most_once_dispatch_manual_reconcile'
       )
       OR operation_schema_version IS DISTINCT FROM 'techstack.provider-control-operation/v1'
       OR operation_execution_authority IS DISTINCT FROM 'techstack_provider_control'
       OR receipt_status IS DISTINCT FROM 'pending'
       OR receipt_phase IS DISTINCT FROM 'accepted' THEN
        RAISE EXCEPTION 'dispatch custody must bind the current native provision accepted head'
            USING ERRCODE = '55000';
    END IF;

    SELECT
        runtime_lease.lease_revision,
        runtime_lease.server_id,
        runtime_lease.resource_generation_id
    INTO
        live_lease_revision,
        live_server_id,
        live_resource_generation_id
    FROM techstack_vm_leases AS runtime_lease
    WHERE runtime_lease.tenant_id = NEW.tenant_id
      AND runtime_lease.id = NEW.lease_id
    FOR SHARE OF runtime_lease;
    IF NOT FOUND
       OR live_lease_revision IS DISTINCT FROM NEW.lease_revision
       OR live_server_id IS DISTINCT FROM NEW.server_id
       OR live_resource_generation_id IS DISTINCT FROM NEW.resource_generation_id THEN
        RAISE EXCEPTION 'dispatch custody does not match the live typed runtime lease'
            USING ERRCODE = '55000';
    END IF;

    -- A previous claim for any operation ID on this generation is already a
    -- durable dispatch decision. It cannot be reclassified by a later mode.
    PERFORM 1
    FROM provider_operation_execution_claims AS prior_claim
    JOIN provider_operations AS prior_operation
      ON prior_operation.tenant_id = prior_claim.tenant_id
     AND prior_operation.operation_id = prior_claim.operation_id
    WHERE prior_operation.tenant_id = NEW.tenant_id
      AND prior_operation.lease_id = NEW.lease_id
      AND prior_operation.operation = 'provision'
      AND (prior_operation.command_json #>> '{command,resource_generation_id}')::uuid = NEW.resource_generation_id
    LIMIT 1;
    IF FOUND THEN
        RAISE EXCEPTION 'resource generation already has provision execution custody'
            USING ERRCODE = '55000';
    END IF;
    IF receipt_resources IS NOT NULL THEN
        IF jsonb_typeof(receipt_resources) IS DISTINCT FROM 'array' THEN
            RAISE EXCEPTION 'dispatch guard requires a handle-free provision accepted receipt'
                USING ERRCODE = '55000';
        END IF;
        IF jsonb_array_length(receipt_resources) <> 0 THEN
            RAISE EXCEPTION 'dispatch guard requires a handle-free provision accepted receipt'
                USING ERRCODE = '55000';
        END IF;
    END IF;

    IF operation_lease_id IS DISTINCT FROM NEW.lease_id
       OR command_lease_revision IS DISTINCT FROM NEW.lease_revision::text
       OR command_server_id IS DISTINCT FROM NEW.server_id
       OR command_resource_generation_id IS DISTINCT FROM NEW.resource_generation_id::text
       OR command_capability_snapshot_hash IS DISTINCT FROM NEW.capability_snapshot_hash
       OR profile_capability_snapshot_hash IS DISTINCT FROM NEW.capability_snapshot_hash
       OR command_execution_profile_hash IS DISTINCT FROM NEW.execution_profile_hash
       OR profile_execution_profile_hash IS DISTINCT FROM NEW.execution_profile_hash
       OR profile_adapter_manifest_hash IS DISTINCT FROM NEW.adapter_manifest_hash THEN
        RAISE EXCEPTION 'dispatch guard lease, UUID, or execution-profile projection mismatch'
            USING ERRCODE = '23514';
    END IF;

    IF NEW.dispatch_mode = 'at_most_once_dispatch_manual_reconcile' THEN
        IF command_custody_hash IS DISTINCT FROM NEW.credential_version_hash
           OR command_connection_hash IS DISTINCT FROM NEW.provider_scope_hash THEN
            RAISE EXCEPTION 'AMO prepared request custody or provider scope mismatch'
                USING ERRCODE = '23514';
        END IF;
    ELSIF NEW.prepared_request_digest IS NOT NULL
       OR NEW.credential_version_hash IS NOT NULL
       OR NEW.provider_scope_hash IS NOT NULL
       OR NEW.correlation_hash IS NOT NULL THEN
        RAISE EXCEPTION 'crash-recoverable provision custody cannot carry AMO preparation fields'
            USING ERRCODE = '23514';
    END IF;

    IF bound_claim_token_digest IS NULL
       OR bound_claim_owner IS NULL
       OR NEW.first_claim_token_digest IS DISTINCT FROM bound_claim_token_digest
       OR NEW.first_claim_owner IS DISTINCT FROM bound_claim_owner THEN
        RAISE EXCEPTION 'dispatch guard is not bound to the transaction claim capability'
            USING ERRCODE = '55000';
    END IF;

    NEW.guarded_at := clock_timestamp();
    RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION provider_provision_dispatch_guard_reject_mutation()
RETURNS trigger
LANGUAGE plpgsql
SET search_path FROM CURRENT
AS $$
BEGIN
    RAISE EXCEPTION 'provider provision dispatch guards are immutable'
        USING ERRCODE = '55000';
END;
$$;

CREATE TRIGGER provider_provision_dispatch_guards_validate_insert
BEFORE INSERT ON provider_provision_dispatch_guards
FOR EACH ROW EXECUTE FUNCTION provider_provision_dispatch_guard_validate_insert();

DROP TRIGGER IF EXISTS provider_provision_dispatch_guards_reject_update
    ON provider_provision_dispatch_guards;
CREATE TRIGGER provider_provision_dispatch_guards_reject_update
BEFORE UPDATE ON provider_provision_dispatch_guards
FOR EACH ROW EXECUTE FUNCTION provider_provision_dispatch_guard_reject_mutation();

DROP TRIGGER IF EXISTS provider_provision_dispatch_guards_reject_delete
    ON provider_provision_dispatch_guards;
CREATE TRIGGER provider_provision_dispatch_guards_reject_delete
BEFORE DELETE ON provider_provision_dispatch_guards
FOR EACH ROW EXECUTE FUNCTION provider_provision_dispatch_guard_reject_mutation();

ALTER TABLE provider_provision_dispatch_guards ENABLE ROW LEVEL SECURITY;
ALTER TABLE provider_provision_dispatch_guards FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation ON provider_provision_dispatch_guards;
CREATE POLICY tenant_isolation ON provider_provision_dispatch_guards
    USING (tenant_id = current_setting('app.tenant_id', true))
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true));

-- Replace migration 014's active-claim guard. Blocked historical operations
-- can never acquire or renew execution custody. An AMO provision accepted head
-- is executable only by the exact first claim whose digest and owner were
-- sealed into the immutable guard in the same transaction.
CREATE OR REPLACE FUNCTION provider_execution_claim_current_head()
RETURNS trigger
LANGUAGE plpgsql
SET search_path FROM CURRENT
AS $$
DECLARE
    db_at timestamptz := clock_timestamp();
    requested_ttl interval;
    current_operation text;
    current_dispatch_mode text;
    current_phase text;
    current_lease_id text;
    current_lease_revision bigint;
    current_server_id text;
    current_resource_generation_id uuid;
    live_lease_revision bigint;
    live_server_id text;
    live_resource_generation_id uuid;
    custody_operation_id text;
    custody_dispatch_mode text;
    custody_head_sequence bigint;
    custody_head_receipt_digest text;
    custody_guard_origin text;
    custody_first_claim_token_digest text;
    custody_first_claim_owner text;
    bound_token_digest text := CASE
        WHEN nullif(current_setting('app.provider_execution_claim_token', true), '') IS NULL THEN NULL
        ELSE encode(sha256(convert_to(current_setting('app.provider_execution_claim_token', true), 'UTF8')), 'hex')
    END;
    bound_owner text := current_setting('app.provider_execution_claim_owner', true);
BEGIN
    IF NEW.state = 'active' THEN
        IF bound_token_digest IS DISTINCT FROM NEW.claim_token_digest
           OR bound_owner IS DISTINCT FROM NEW.claim_owner THEN
            RAISE EXCEPTION 'provider execution claim capability is not bound to this transaction'
                USING ERRCODE = '55000';
        END IF;

        IF TG_OP = 'INSERT' THEN
            requested_ttl := NEW.lease_expires_at - NEW.claimed_at;
            IF requested_ttl <= interval '0 seconds'
               OR requested_ttl > interval '15 minutes' THEN
                RAISE EXCEPTION 'provider execution claim lease is outside the allowed range'
                    USING ERRCODE = '55000';
            END IF;
            NEW.claimed_at := db_at;
            NEW.lease_expires_at := db_at + requested_ttl;
        END IF;

        SELECT
            operation.operation,
            operation.provision_dispatch_mode,
            operation.phase,
            operation.lease_id,
            (operation.command_json #>> '{command,lease_revision}')::bigint,
            operation.command_json #>> '{command,runtime_server_id}',
            (operation.command_json #>> '{command,resource_generation_id}')::uuid
        INTO
            current_operation,
            current_dispatch_mode,
            current_phase,
            current_lease_id,
            current_lease_revision,
            current_server_id,
            current_resource_generation_id
        FROM provider_operations AS operation
        JOIN provider_operation_receipts AS receipt
          ON receipt.tenant_id = operation.tenant_id
         AND receipt.operation_id = operation.operation_id
         AND receipt.sequence = operation.head_sequence
         AND receipt.receipt_digest = operation.head_receipt_digest
        WHERE operation.tenant_id = NEW.tenant_id
          AND operation.operation_id = NEW.operation_id
          AND operation.head_sequence = NEW.head_sequence
          AND operation.head_receipt_digest = NEW.head_receipt_digest
          AND operation.phase <> 'requested'
          AND receipt.status = operation.status
          AND receipt.phase = operation.phase;
        IF NOT FOUND THEN
            RAISE EXCEPTION 'provider execution claim must bind the current claim-required head'
                USING ERRCODE = '55000';
        END IF;

        IF current_dispatch_mode = 'blocked' THEN
            RAISE EXCEPTION 'blocked provider operation cannot acquire execution custody'
                USING ERRCODE = '55000';
        END IF;
        SELECT
            runtime_lease.lease_revision,
            runtime_lease.server_id,
            runtime_lease.resource_generation_id
        INTO
            live_lease_revision,
            live_server_id,
            live_resource_generation_id
        FROM techstack_vm_leases AS runtime_lease
        WHERE runtime_lease.tenant_id = NEW.tenant_id
          AND runtime_lease.id = current_lease_id
        FOR SHARE OF runtime_lease;
        IF NOT FOUND
           OR live_lease_revision IS DISTINCT FROM current_lease_revision
           OR live_server_id IS DISTINCT FROM current_server_id
           OR live_resource_generation_id IS DISTINCT FROM current_resource_generation_id THEN
            RAISE EXCEPTION 'provider execution claim is stale against the live typed runtime lease'
                USING ERRCODE = '55000';
        END IF;
        IF current_operation = 'provision' AND current_phase = 'accepted' THEN
            PERFORM pg_advisory_xact_lock(
                hashtext(NEW.tenant_id),
                hashtext(current_lease_id || ':' || current_resource_generation_id::text)
            );
            SELECT
                guard.operation_id,
                guard.dispatch_mode,
                guard.head_sequence,
                guard.head_receipt_digest,
                guard.guard_origin,
                guard.first_claim_token_digest,
                guard.first_claim_owner
            INTO
                custody_operation_id,
                custody_dispatch_mode,
                custody_head_sequence,
                custody_head_receipt_digest,
                custody_guard_origin,
                custody_first_claim_token_digest,
                custody_first_claim_owner
            FROM provider_provision_dispatch_guards AS guard
            WHERE guard.tenant_id = NEW.tenant_id
              AND guard.lease_id = current_lease_id
              AND guard.resource_generation_id = current_resource_generation_id
            FOR SHARE;
            IF NOT FOUND THEN
                RAISE EXCEPTION 'provision accepted claim requires generation-bound dispatch custody'
                    USING ERRCODE = '55000';
            END IF;
            IF custody_operation_id IS DISTINCT FROM NEW.operation_id
               OR custody_dispatch_mode IS DISTINCT FROM current_dispatch_mode
               OR custody_guard_origin IS DISTINCT FROM 'first_claim' THEN
                RAISE EXCEPTION 'resource generation dispatch custody belongs to another operation or mode'
                    USING ERRCODE = '55000';
            END IF;
            IF current_dispatch_mode = 'at_most_once_dispatch_manual_reconcile' THEN
                IF custody_head_sequence IS DISTINCT FROM NEW.head_sequence
                   OR custody_head_receipt_digest IS DISTINCT FROM NEW.head_receipt_digest
                   OR custody_first_claim_token_digest IS DISTINCT FROM NEW.claim_token_digest
                   OR custody_first_claim_owner IS DISTINCT FROM NEW.claim_owner THEN
                    RAISE EXCEPTION 'AMO provision accepted claim requires its exact first-claim dispatch guard'
                        USING ERRCODE = '55000';
                END IF;
            ELSIF current_dispatch_mode NOT IN (
                'native_idempotency',
                'provider_correlation'
            ) THEN
                RAISE EXCEPTION 'provision operation has no executable dispatch mode'
                    USING ERRCODE = '55000';
            END IF;
        END IF;
    END IF;
    RETURN NEW;
END;
$$;

-- Replace migration 014's operation-update guard. Claim consumption remains
-- necessary for every adapter-owned head. Blocked historical work cannot move;
-- an AMO provision accepted head additionally requires its first-claim guard.
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
				runtime_lease.resource_generation_id
			INTO
				live_lease_revision,
				live_server_id,
				live_resource_generation_id
			FROM techstack_vm_leases AS runtime_lease
			WHERE runtime_lease.tenant_id = OLD.tenant_id
			  AND runtime_lease.id = OLD.lease_id
			FOR SHARE OF runtime_lease;
			IF NOT FOUND
			   OR live_lease_revision IS DISTINCT FROM (OLD.command_json #>> '{command,lease_revision}')::bigint
			   OR live_server_id IS DISTINCT FROM OLD.command_json #>> '{command,runtime_server_id}'
			   OR live_resource_generation_id IS DISTINCT FROM (OLD.command_json #>> '{command,resource_generation_id}')::uuid THEN
				RAISE EXCEPTION 'provider operation head is stale against the live typed runtime lease'
					USING ERRCODE = '55000';
			END IF;
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

REVOKE ALL ON FUNCTION provider_catalog_profile_insert_guard() FROM PUBLIC;
REVOKE ALL ON FUNCTION provider_operation_dispatch_mode_insert_guard() FROM PUBLIC;
REVOKE ALL ON FUNCTION provider_provision_dispatch_guard_validate_insert() FROM PUBLIC;
REVOKE ALL ON FUNCTION provider_provision_dispatch_guard_reject_mutation() FROM PUBLIC;
REVOKE ALL ON FUNCTION provider_execution_claim_current_head() FROM PUBLIC;
REVOKE ALL ON FUNCTION provider_execution_immutable_update() FROM PUBLIC;

COMMENT ON COLUMN provider_catalog_profiles.provision_dispatch_mode IS
    'Immutable catalog safety pin copied to every new provider operation; AMO execution semantics apply only to provision accepted heads.';
COMMENT ON COLUMN provider_operations.provision_dispatch_mode IS
    'Immutable catalog safety pin retained by every operation; historical rows are blocked and AMO execution semantics apply only to provision accepted heads.';
COMMENT ON TABLE provider_provision_dispatch_guards IS
    'Immutable first-dispatch custody for one tenant/lease/resource-generation; guard presence never authorizes retry.';
