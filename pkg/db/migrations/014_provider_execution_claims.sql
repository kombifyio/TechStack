-- Durable ownership for adapter-bearing provider execution. A claim is a
-- short-lived capability for exactly one immutable operation head; it is not
-- a replacement for provider-native idempotency at the adapter boundary.

CREATE UNIQUE INDEX IF NOT EXISTS idx_provider_receipts_head_identity
    ON provider_operation_receipts (tenant_id, operation_id, sequence, receipt_digest);

CREATE TABLE IF NOT EXISTS provider_operation_execution_claims (
    tenant_id text NOT NULL,
    operation_id text NOT NULL,
    head_sequence bigint NOT NULL CHECK (head_sequence > 0),
    head_receipt_digest text NOT NULL,
    claim_token_digest text NOT NULL CHECK (claim_token_digest ~ '^[0-9a-f]{64}$'),
    claim_owner text NOT NULL CHECK (claim_owner <> ''),
    state text NOT NULL CHECK (state IN ('active', 'released', 'consumed')),
    claimed_at timestamptz NOT NULL,
    lease_expires_at timestamptz NOT NULL CHECK (lease_expires_at > claimed_at),
    released_at timestamptz,
    PRIMARY KEY (tenant_id, operation_id, head_receipt_digest),
    FOREIGN KEY (tenant_id, operation_id)
        REFERENCES provider_operations (tenant_id, operation_id) ON DELETE RESTRICT,
    FOREIGN KEY (tenant_id, operation_id, head_sequence, head_receipt_digest)
        REFERENCES provider_operation_receipts (tenant_id, operation_id, sequence, receipt_digest) ON DELETE RESTRICT,
    CHECK (
        (state = 'active' AND released_at IS NULL)
        OR (state IN ('released', 'consumed') AND released_at IS NOT NULL AND released_at >= claimed_at)
    )
);

-- The operation row lock in the acquire/append transaction is the authority
-- for the current head. This partial uniqueness is a second fail-closed
-- guard: there can never be two active adapter capabilities for one operation.
CREATE UNIQUE INDEX IF NOT EXISTS idx_provider_execution_claims_one_active
    ON provider_operation_execution_claims (tenant_id, operation_id)
    WHERE state = 'active';
CREATE INDEX IF NOT EXISTS idx_provider_execution_claims_expiry
    ON provider_operation_execution_claims (lease_expires_at)
    WHERE state = 'active';

CREATE OR REPLACE FUNCTION provider_execution_claim_update()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    db_at timestamptz := clock_timestamp();
    requested_ttl interval;
    bound_token_digest text := CASE
        WHEN nullif(current_setting('app.provider_execution_claim_token', true), '') IS NULL THEN NULL
        ELSE encode(sha256(convert_to(current_setting('app.provider_execution_claim_token', true), 'UTF8')), 'hex')
    END;
    bound_owner text := current_setting('app.provider_execution_claim_owner', true);
BEGIN
    IF to_jsonb(NEW) IS NOT DISTINCT FROM to_jsonb(OLD) THEN
        RETURN NEW;
    END IF;

    IF NEW.tenant_id IS DISTINCT FROM OLD.tenant_id
       OR NEW.operation_id IS DISTINCT FROM OLD.operation_id
       OR NEW.head_sequence IS DISTINCT FROM OLD.head_sequence
       OR NEW.head_receipt_digest IS DISTINCT FROM OLD.head_receipt_digest THEN
        RAISE EXCEPTION 'provider execution claim identity is immutable'
            USING ERRCODE = '55000';
    END IF;

    IF OLD.state = 'consumed' THEN
        RAISE EXCEPTION 'consumed provider execution claim is immutable'
            USING ERRCODE = '55000';
    END IF;

    IF OLD.state = 'active' AND NEW.state = 'active' THEN
        -- A renewal must present the existing unexpired capability. Lease
        -- bounds are authorized by the database clock, never caller time.
        IF bound_token_digest = OLD.claim_token_digest
           AND bound_owner = OLD.claim_owner
           AND NEW.claim_token_digest = OLD.claim_token_digest
           AND NEW.claim_owner = OLD.claim_owner
           AND NEW.claimed_at = OLD.claimed_at
           AND NEW.released_at IS NULL
           AND OLD.lease_expires_at > db_at
           AND NEW.lease_expires_at > OLD.lease_expires_at
           AND NEW.lease_expires_at > db_at
           AND NEW.lease_expires_at <= db_at + interval '15 minutes' THEN
            RETURN NEW;
        END IF;

        -- Expiry is also decided by the database clock. For takeover, the
        -- caller supplies only the requested duration; the trigger replaces
        -- both timestamps with database-authoritative values.
        requested_ttl := NEW.lease_expires_at - NEW.claimed_at;
        IF OLD.lease_expires_at <= db_at
           AND bound_token_digest = NEW.claim_token_digest
           AND bound_owner = NEW.claim_owner
           AND NEW.released_at IS NULL
           AND requested_ttl > interval '0 seconds'
           AND requested_ttl <= interval '15 minutes' THEN
            NEW.claimed_at := db_at;
            NEW.lease_expires_at := db_at + requested_ttl;
            RETURN NEW;
        END IF;
    ELSIF OLD.state = 'active' AND NEW.state IN ('released', 'consumed') THEN
        IF bound_token_digest = OLD.claim_token_digest
           AND bound_owner = OLD.claim_owner
           AND OLD.lease_expires_at > db_at
           AND NEW.claim_token_digest = OLD.claim_token_digest
           AND NEW.claim_owner = OLD.claim_owner
           AND NEW.claimed_at = OLD.claimed_at
           AND NEW.lease_expires_at = OLD.lease_expires_at
           AND NEW.released_at IS NOT NULL THEN
            NEW.released_at := db_at;
            RETURN NEW;
        END IF;
    ELSIF OLD.state = 'released' AND NEW.state = 'active' THEN
        requested_ttl := NEW.lease_expires_at - NEW.claimed_at;
        IF bound_token_digest = NEW.claim_token_digest
           AND bound_owner = NEW.claim_owner
           AND NEW.released_at IS NULL
           AND requested_ttl > interval '0 seconds'
           AND requested_ttl <= interval '15 minutes' THEN
            NEW.claimed_at := db_at;
            NEW.lease_expires_at := db_at + requested_ttl;
            RETURN NEW;
        END IF;
    END IF;

    RAISE EXCEPTION 'invalid provider execution claim lifecycle transition'
        USING ERRCODE = '55000';
END;
$$;

CREATE OR REPLACE FUNCTION provider_execution_claim_current_head()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    db_at timestamptz := clock_timestamp();
    requested_ttl interval;
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

        PERFORM 1
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
    END IF;
    RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION provider_execution_claim_reject_delete()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION 'provider execution claims are retained and cannot be deleted'
        USING ERRCODE = '55000';
END;
$$;

CREATE TRIGGER provider_execution_claims_update
BEFORE UPDATE ON provider_operation_execution_claims
FOR EACH ROW EXECUTE FUNCTION provider_execution_claim_update();
CREATE TRIGGER provider_execution_claims_current_head
BEFORE INSERT OR UPDATE ON provider_operation_execution_claims
FOR EACH ROW EXECUTE FUNCTION provider_execution_claim_current_head();
CREATE TRIGGER provider_execution_claims_reject_delete
BEFORE DELETE ON provider_operation_execution_claims
FOR EACH ROW EXECUTE FUNCTION provider_execution_claim_reject_delete();

ALTER TABLE provider_operation_execution_claims ENABLE ROW LEVEL SECURITY;
ALTER TABLE provider_operation_execution_claims FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation ON provider_operation_execution_claims;
CREATE POLICY tenant_isolation ON provider_operation_execution_claims
    USING (tenant_id = current_setting('app.tenant_id', true))
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true));

-- Replace migration 013's operation-update guard with a claim-aware version.
-- The sole claim-free head shift is requested -> accepted. Every later shift
-- requires this transaction to have consumed the exact previous-head claim;
-- a merely released or expired claim cannot authorize a ledger append.
CREATE OR REPLACE FUNCTION provider_execution_immutable_update()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path FROM CURRENT
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
            IF OLD.phase = 'requested' AND NEW.phase = 'accepted' THEN
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

-- Deployment contract: migrations run as a trusted owner on a controlled
-- search_path. Runtime roles receive DML plus SELECT on non-secret claim
-- columns. Only the SHA-256 digest of the bearer claim token is persisted;
-- plaintext tokens exist solely in worker memory and transaction-local state.
-- Runtime roles must not own these tables or have BYPASSRLS,
-- schema CREATE, trigger/function replacement, or migration privileges.
REVOKE ALL ON FUNCTION provider_execution_immutable_update() FROM PUBLIC;
