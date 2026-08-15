-- Durable, tenant-scoped custody for credentials of the managed offsite
-- backup target. Provider details stay in the Techstack control plane and
-- are never projected into StackKits specs, artifacts, or Inventory.

CREATE TABLE IF NOT EXISTS stack_backup_stores (
    tenant_id text NOT NULL,
    stack_id text NOT NULL,
    bucket text NOT NULL,
    endpoint text NOT NULL,
    token_id text NOT NULL,
    access_key_id text NOT NULL,
    secret_access_key_enc text NOT NULL,
    kopia_repo_password_enc text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, stack_id),
    FOREIGN KEY (tenant_id, stack_id)
        REFERENCES stacks (tenant_id, id)
        ON DELETE CASCADE,
    CHECK (BTRIM(bucket) <> ''),
    CHECK (endpoint ~ '^https://'),
    CHECK (BTRIM(token_id) <> ''),
    CHECK (BTRIM(access_key_id) <> ''),
    CHECK (secret_access_key_enc LIKE 'enc:v1:%'),
    CHECK (kopia_repo_password_enc LIKE 'enc:v1:%')
);

ALTER TABLE stack_backup_stores ENABLE ROW LEVEL SECURITY;
ALTER TABLE stack_backup_stores FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation ON stack_backup_stores;
CREATE POLICY tenant_isolation ON stack_backup_stores
    USING (tenant_id = current_setting('app.tenant_id', true))
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true));

DROP TRIGGER IF EXISTS set_stack_backup_stores_updated_at ON stack_backup_stores;
CREATE TRIGGER set_stack_backup_stores_updated_at BEFORE UPDATE ON stack_backup_stores
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();
