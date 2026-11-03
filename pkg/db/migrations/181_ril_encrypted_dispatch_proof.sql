-- Encrypted, expiring RIL proof custody is scoped by the existing tenant RLS.
ALTER TABLE typed_agent_commands ADD COLUMN IF NOT EXISTS ril_proof_ciphertext bytea;
ALTER TABLE typed_agent_commands ADD COLUMN IF NOT EXISTS ril_proof_expires_at timestamptz;
ALTER TABLE typed_agent_commands ADD CONSTRAINT typed_agent_commands_ril_proof_pair CHECK (
  (ril_proof_ciphertext IS NULL AND ril_proof_expires_at IS NULL)
  OR (ril_proof_ciphertext IS NOT NULL AND ril_proof_expires_at IS NOT NULL)
);
