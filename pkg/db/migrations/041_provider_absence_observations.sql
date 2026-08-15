-- Durable provider-native absence observations.
--
-- providerexecutor refuses to seal a receipt that claims a resource is absent
-- unless the claim carries definitive provider_api evidence AND an evidence
-- verifier accepts it (validation.go ErrAbsenceProofRequired). Techstack shipped
-- neither: the IONOS and centron adapters returned Observation=absent with an
-- empty Evidence slice, and FailClosedEvidenceVerifier rejected every envelope
-- as a placeholder. The consequence was total: measured on 2026-07-27, 0 of 115
-- managed-runtime capacity reservations had ever produced a release fact,
-- because no decommission could ever prove absence. Every VM a customer had ever
-- provisioned still held a plan slot.
--
-- This table is the missing durable store. The adapter writes the exact provider
-- read that observed the resource gone -- request URL, HTTP status, observed
-- time -- before it returns the absent result. The verifier then resolves the
-- receipt's evidence_ref here and recomputes the digest over the stored
-- document, so an absence claim is only accepted when a matching provider
-- observation was independently recorded at observation time.
--
-- What this proves: the claim is backed by a durable, digest-bound provider read
-- that the control plane recorded itself. What it does not prove: a third-party
-- attestation of that read. The attestation columns carry the adapter's own
-- signature over the document and are kept distinct so a certified external
-- attestor can replace them without a schema change.

CREATE TABLE IF NOT EXISTS provider_absence_observations (
    tenant_id text NOT NULL CHECK (BTRIM(tenant_id) <> ''),
    operation_id text NOT NULL CHECK (BTRIM(operation_id) <> ''),
    binding_id text NOT NULL CHECK (BTRIM(binding_id) <> ''),
    provider_id text NOT NULL CHECK (provider_id IN ('ionos', 'centron')),
    evidence_ref text NOT NULL
        CHECK (evidence_ref LIKE 'provider-evidence://%'),
    evidence_digest text NOT NULL
        CHECK (evidence_digest ~ '^sha256:[0-9a-f]{64}$'),
    attestation_ref text NOT NULL
        CHECK (attestation_ref LIKE 'provider-attestation://%'),
    attestation_digest text NOT NULL
        CHECK (attestation_digest ~ '^sha256:[0-9a-f]{64}$'),
    -- Only definitive provider-API absence belongs here. Adapter-sourced
    -- absence is rejected by the wire contract itself, so storing it would
    -- create a record that can never be redeemed.
    observation text NOT NULL CHECK (observation = 'absent'),
    source text NOT NULL CHECK (source = 'provider_api'),
    -- The subject hash binds the observation to one exact command and resource
    -- target. A row recorded for a different operation, lease revision, or
    -- resource graph cannot be replayed to satisfy this one.
    subject_hash text NOT NULL CHECK (subject_hash ~ '^sha256:[0-9a-f]{64}$'),
    native_ref_hash text NOT NULL CHECK (native_ref_hash ~ '^sha256:[0-9a-f]{64}$'),
    collected_at timestamptz NOT NULL,
    document_json jsonb NOT NULL,
    PRIMARY KEY (tenant_id, evidence_ref),
    UNIQUE (tenant_id, attestation_ref),
    UNIQUE (tenant_id, operation_id, binding_id)
);

CREATE INDEX IF NOT EXISTS provider_absence_observations_operation_idx
    ON provider_absence_observations (tenant_id, operation_id);

-- An absence observation is a custody fact. Rewriting one would let a caller
-- turn a present resource into a provable absence after the fact, which is the
-- exact forgery the verifier exists to prevent. Exact no-op updates stay valid
-- so idempotent ON CONFLICT paths keep working.
CREATE OR REPLACE FUNCTION provider_absence_observation_reject_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP = 'UPDATE' AND to_jsonb(NEW) IS NOT DISTINCT FROM to_jsonb(OLD) THEN
        RETURN NEW;
    END IF;
    RAISE EXCEPTION 'provider absence observations are immutable'
        USING ERRCODE = '55000';
END;
$$;

DROP TRIGGER IF EXISTS provider_absence_observations_reject_mutation
    ON provider_absence_observations;
CREATE TRIGGER provider_absence_observations_reject_mutation
BEFORE UPDATE OR DELETE ON provider_absence_observations
FOR EACH ROW EXECUTE FUNCTION provider_absence_observation_reject_mutation();

ALTER TABLE provider_absence_observations ENABLE ROW LEVEL SECURITY;
ALTER TABLE provider_absence_observations FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation ON provider_absence_observations;
CREATE POLICY tenant_isolation ON provider_absence_observations
    USING (tenant_id = current_setting('app.tenant_id', true))
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true));
