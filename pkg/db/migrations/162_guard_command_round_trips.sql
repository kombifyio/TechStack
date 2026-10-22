-- kombify Guard readiness: the last authenticated command/result round trip of
-- each Guard. A heartbeat proves that the Guard still authenticates; only a
-- typed command whose terminal result the control plane accepted under the
-- Guard's current identity proves that the command channel works, so the
-- Connected readiness fact is derived from this row and never from the
-- heartbeat alone. The row outlives the pruning of settled command rows.
--
-- transport names the channel that carried the result: 'https' is the bound
-- HTTPS hub (identity = credential generation), 'mtls' the gRPC listener
-- (identity = the client certificate serial in credential_ref).
--
-- Additive and backward compatible: a binary that predates this migration
-- neither writes nor reads the table, and its servers read as not yet
-- round-trip evidenced.

CREATE TABLE IF NOT EXISTS guard_command_round_trips (
    tenant_id text NOT NULL,
    agent_id text NOT NULL,
    transport text NOT NULL DEFAULT 'https',
    command_id text NOT NULL,
    result_generation bigint NOT NULL,
    credential_ref text NOT NULL DEFAULT '',
    result_revision integer NOT NULL,
    result_digest text NOT NULL,
    acknowledged_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, agent_id),
    CHECK (BTRIM(command_id) <> ''),
    CHECK (
        (transport = 'https' AND result_generation > 0)
        OR (transport = 'mtls' AND result_generation = 0 AND BTRIM(credential_ref) <> '')
    ),
    CHECK (result_revision >= 1),
    CHECK (result_digest ~ '^[0-9a-f]{64}$')
);

ALTER TABLE guard_command_round_trips ENABLE ROW LEVEL SECURITY;
ALTER TABLE guard_command_round_trips FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation ON guard_command_round_trips;
CREATE POLICY tenant_isolation ON guard_command_round_trips
    USING (tenant_id = current_setting('app.tenant_id', true))
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true));
