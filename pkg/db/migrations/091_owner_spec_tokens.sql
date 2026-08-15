-- Native control-plane custody for the short-lived StackKit owner bootstrap.
-- PocketBase collections are absent in the standalone desktop runtime, so the
-- one-use token must live beside its canonical Postgres stack.

CREATE TABLE IF NOT EXISTS owner_spec_tokens (
    token_hash text PRIMARY KEY,
    tenant_id text NOT NULL,
    stack_id text NOT NULL,
    owner_id text NOT NULL,
    status text NOT NULL DEFAULT 'issued',
    expires_at timestamptz NOT NULL,
    consumed_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    CHECK (status IN ('issued', 'consumed')),
    FOREIGN KEY (tenant_id, stack_id) REFERENCES stacks (tenant_id, id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_owner_spec_tokens_stack
    ON owner_spec_tokens (tenant_id, stack_id, created_at DESC);

ALTER TABLE owner_spec_tokens ENABLE ROW LEVEL SECURITY;
ALTER TABLE owner_spec_tokens FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation ON owner_spec_tokens;
CREATE POLICY tenant_isolation ON owner_spec_tokens
    USING (tenant_id = current_setting('app.tenant_id', true))
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true));
