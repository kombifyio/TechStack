-- Persist the opaque source evidence used by the StackKits
-- custodyAttestationRef. Credential rotation in the repository clears these
-- fields; a new real target sentinel must attest the new custody generation.

ALTER TABLE stack_backup_stores
    ADD COLUMN IF NOT EXISTS custody_attestation_evidence text,
    ADD COLUMN IF NOT EXISTS attested_at timestamptz;

ALTER TABLE stack_backup_stores
    DROP CONSTRAINT IF EXISTS stack_backup_stores_attestation_pair;
ALTER TABLE stack_backup_stores
    ADD CONSTRAINT stack_backup_stores_attestation_pair CHECK (
        (custody_attestation_evidence IS NULL AND attested_at IS NULL)
        OR
        (custody_attestation_evidence ~ '^sha256:[0-9a-f]{64}$' AND attested_at IS NOT NULL)
    );
