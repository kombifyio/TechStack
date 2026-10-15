-- The node, not the control plane, holds the managed off-site backup custody and
-- issues its own backup-target binding. This row records only what Core must
-- compare against: the node's recipient key pinned at enrollment (so a later
-- substituted key is refused) and the opaque references the node issued when it
-- opened the delivery. No credential and no reusable value is stored here.

ALTER TABLE stack_backup_stores
    ADD COLUMN IF NOT EXISTS recipient_owner_public_key text,
    ADD COLUMN IF NOT EXISTS recipient_owner_key_id text,
    ADD COLUMN IF NOT EXISTS recipient_fingerprint text,
    ADD COLUMN IF NOT EXISTS recipient_generation bigint,
    ADD COLUMN IF NOT EXISTS recipient_site_ref text,
    ADD COLUMN IF NOT EXISTS recipient_node_ref text,
    ADD COLUMN IF NOT EXISTS recipient_pinned_at timestamptz,
    ADD COLUMN IF NOT EXISTS node_backup_target_ref text,
    ADD COLUMN IF NOT EXISTS node_custody_attestation_ref text,
    ADD COLUMN IF NOT EXISTS node_binding_recorded_at timestamptz;

ALTER TABLE stack_backup_stores
    DROP CONSTRAINT IF EXISTS stack_backup_stores_recipient_pin;
ALTER TABLE stack_backup_stores
    ADD CONSTRAINT stack_backup_stores_recipient_pin CHECK (
        (recipient_fingerprint IS NULL AND recipient_owner_public_key IS NULL AND recipient_owner_key_id IS NULL
            AND recipient_generation IS NULL AND recipient_site_ref IS NULL AND recipient_node_ref IS NULL AND recipient_pinned_at IS NULL)
        OR
        (recipient_fingerprint ~ '^sha256:[0-9a-f]{64}$' AND BTRIM(recipient_owner_public_key) <> '' AND BTRIM(recipient_owner_key_id) <> ''
            AND recipient_generation > 0 AND BTRIM(recipient_site_ref) <> '' AND BTRIM(recipient_node_ref) <> '' AND recipient_pinned_at IS NOT NULL)
    );

ALTER TABLE stack_backup_stores
    DROP CONSTRAINT IF EXISTS stack_backup_stores_node_binding;
ALTER TABLE stack_backup_stores
    ADD CONSTRAINT stack_backup_stores_node_binding CHECK (
        (node_backup_target_ref IS NULL AND node_custody_attestation_ref IS NULL AND node_binding_recorded_at IS NULL)
        OR
        (node_backup_target_ref ~ '^backup-target://sha256/[0-9a-f]{64}$'
            AND node_custody_attestation_ref ~ '^backup-custody-attestation://sha256/[0-9a-f]{64}$'
            AND node_binding_recorded_at IS NOT NULL AND recipient_fingerprint IS NOT NULL)
    );
