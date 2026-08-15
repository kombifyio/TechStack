-- Retain every accepted Guard source epoch so a superseded process can never
-- retake ownership by replaying sequence 1 after a newer epoch was accepted.

CREATE TABLE IF NOT EXISTS server_guard_source_epochs (
    tenant_id text NOT NULL,
    server_id text NOT NULL,
    generation bigint NOT NULL,
    source_id text NOT NULL,
    source_epoch text NOT NULL,
    first_observed_at timestamptz NOT NULL,
    last_observed_at timestamptz NOT NULL,
    last_sequence bigint NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT server_guard_source_epochs_pk
        PRIMARY KEY (tenant_id, server_id, generation, source_id, source_epoch),
    CONSTRAINT server_guard_source_epochs_server_fk
        FOREIGN KEY (tenant_id, server_id)
        REFERENCES servers (tenant_id, id)
        ON DELETE CASCADE,
    CONSTRAINT server_guard_source_epochs_values_valid CHECK (
        generation > 0 AND last_sequence > 0
        AND char_length(source_id) BETWEEN 1 AND 256
        AND char_length(source_epoch) BETWEEN 1 AND 128
        AND last_observed_at >= first_observed_at
    )
);

INSERT INTO server_guard_source_epochs (
    tenant_id, server_id, generation, source_id, source_epoch,
    first_observed_at, last_observed_at, last_sequence
)
SELECT tenant_id, id, generation, source_id, source_epoch,
       source_observed_at, source_observed_at, source_sequence
FROM servers
WHERE source_authority = 'guard'
  AND source_id IS NOT NULL
  AND source_epoch IS NOT NULL
  AND source_observed_at IS NOT NULL
  AND source_sequence > 0
ON CONFLICT DO NOTHING;

ALTER TABLE server_guard_source_epochs ENABLE ROW LEVEL SECURITY;
ALTER TABLE server_guard_source_epochs FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation ON server_guard_source_epochs;
CREATE POLICY tenant_isolation ON server_guard_source_epochs
    USING (tenant_id = current_setting('app.tenant_id', true))
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true));
