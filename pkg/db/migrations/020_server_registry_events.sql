-- Transactional server-registry aggregate fencing.
--
-- revision is the compare-and-swap head revision. generation changes only
-- when the control plane replaces a runtime binding. The source checkpoint is
-- owned by the authenticated Guard stream and makes retries idempotent while
-- fencing observations from a superseded process epoch.

ALTER TABLE servers ADD COLUMN IF NOT EXISTS revision bigint NOT NULL DEFAULT 1;
ALTER TABLE servers ADD COLUMN IF NOT EXISTS generation bigint NOT NULL DEFAULT 1;
ALTER TABLE servers ADD COLUMN IF NOT EXISTS source_authority text;
ALTER TABLE servers ADD COLUMN IF NOT EXISTS source_id text;
ALTER TABLE servers ADD COLUMN IF NOT EXISTS source_epoch text;
ALTER TABLE servers ADD COLUMN IF NOT EXISTS source_sequence bigint NOT NULL DEFAULT 0;
ALTER TABLE servers ADD COLUMN IF NOT EXISTS source_observed_at timestamptz;

UPDATE servers SET revision = 1 WHERE revision < 1;
UPDATE servers SET generation = 1 WHERE generation < 1;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'servers_revision_positive'
          AND conrelid = 'servers'::regclass
    ) THEN
        ALTER TABLE servers
            ADD CONSTRAINT servers_revision_positive CHECK (revision > 0);
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'servers_generation_positive'
          AND conrelid = 'servers'::regclass
    ) THEN
        ALTER TABLE servers
            ADD CONSTRAINT servers_generation_positive CHECK (generation > 0);
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'servers_source_checkpoint_valid'
          AND conrelid = 'servers'::regclass
    ) THEN
        ALTER TABLE servers
            ADD CONSTRAINT servers_source_checkpoint_valid CHECK (
                source_sequence >= 0
                AND char_length(COALESCE(source_authority, '')) <= 64
                AND char_length(COALESCE(source_id, '')) <= 256
                AND char_length(COALESCE(source_epoch, '')) <= 128
                AND (
                    (source_epoch IS NULL AND source_sequence = 0)
                    OR (source_epoch IS NOT NULL AND source_sequence > 0)
                )
            );
    END IF;
END $$;

CREATE UNIQUE INDEX IF NOT EXISTS idx_servers_tenant_identity
    ON servers (tenant_id, id);

CREATE INDEX IF NOT EXISTS idx_servers_guard_checkpoint
    ON servers (tenant_id, worker_id, generation, source_epoch, source_sequence)
    WHERE worker_id IS NOT NULL;
