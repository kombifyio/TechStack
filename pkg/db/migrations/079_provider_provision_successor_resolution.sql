-- A no-candidate decision is intentionally non-terminal and increments the
-- resolution revision so a later provider read may adopt one exact candidate.
-- The original operation-scoped attestation-ref uniqueness prevented that
-- append-only successor decision even though revision, observation,
-- idempotency key, request digest, decision digest, and attestation digest are
-- independently unique and verified by the insert trigger.
--
-- Keep every row immutable and every stronger uniqueness/CAS guard. Remove
-- only the redundant ref constraint so the stable operation-scoped
-- attestation reference can be reused by a later resolution revision.
ALTER TABLE provider_provision_resolution_decisions
    DROP CONSTRAINT IF EXISTS provider_provision_resolution_tenant_id_operation_id_operat_key;
