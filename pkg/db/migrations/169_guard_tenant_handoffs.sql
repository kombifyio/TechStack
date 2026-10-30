-- kombify Guard G04 cross-tenant handoff. A server whose Guard still holds
-- command evidence of one tenant may be bound to another tenant only after
-- that evidence left the Guard state by a custodian-confirmed export or
-- quarantine. The prior tenant's owner confirms here; the code is stored only
-- as its SHA-256. The rebind presents the server's removal receipt and
-- consumes the confirmation exactly once (receipt_digest is then immutable).
-- Rows are tenant-isolated and belong to the prior tenant; the receipt holds
-- digests and a count, never a payload.
--
-- Additive and backward compatible: a binary that predates this migration
-- neither writes nor reads the table, and refuses no rebind it did not refuse.

CREATE TABLE IF NOT EXISTS guard_tenant_handoffs (
    tenant_id text NOT NULL,
    handoff_id text NOT NULL,
    worker_id text NOT NULL,
    server_id text NOT NULL DEFAULT '',
    disposition text NOT NULL,
    confirmation_sha256 text NOT NULL,
    confirmed_by text NOT NULL,
    confirmed_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL,
    receipt_digest text,
    receipt_entries integer,
    receipted_at timestamptz,
    PRIMARY KEY (tenant_id, handoff_id),
    FOREIGN KEY (tenant_id, worker_id) REFERENCES workers (tenant_id, id) ON DELETE CASCADE,
    CHECK (handoff_id ~ '^gth_[0-9a-f]{32}$'),
    CHECK (disposition IN ('export', 'quarantine')),
    CHECK (confirmation_sha256 ~ '^[0-9a-f]{64}$'),
    CHECK (BTRIM(confirmed_by) <> ''),
    CHECK (expires_at > confirmed_at),
    CHECK ((receipt_digest IS NULL) = (receipted_at IS NULL)),
    CHECK ((receipt_digest IS NULL) = (receipt_entries IS NULL)),
    CHECK (receipt_digest IS NULL OR receipt_digest ~ '^[0-9a-f]{64}$'),
    CHECK (receipt_entries IS NULL OR receipt_entries >= 0)
);

CREATE INDEX IF NOT EXISTS idx_guard_tenant_handoffs_worker
    ON guard_tenant_handoffs (tenant_id, worker_id);

ALTER TABLE guard_tenant_handoffs ENABLE ROW LEVEL SECURITY;
ALTER TABLE guard_tenant_handoffs FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation ON guard_tenant_handoffs;
CREATE POLICY tenant_isolation ON guard_tenant_handoffs
    USING (tenant_id = current_setting('app.tenant_id', true))
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true));

-- A consumed receipt is the audit record: it never changes or disappears
-- except with the prior worker itself.
CREATE OR REPLACE FUNCTION guard_tenant_handoff_receipt_immutable() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, public AS $$
BEGIN
    IF ROW(NEW.tenant_id, NEW.handoff_id, NEW.worker_id, NEW.server_id, NEW.disposition,
           NEW.confirmation_sha256, NEW.confirmed_by, NEW.confirmed_at, NEW.expires_at)
       IS DISTINCT FROM ROW(OLD.tenant_id, OLD.handoff_id, OLD.worker_id, OLD.server_id, OLD.disposition,
           OLD.confirmation_sha256, OLD.confirmed_by, OLD.confirmed_at, OLD.expires_at) THEN
        RAISE EXCEPTION 'guard tenant handoff confirmation is immutable';
    END IF;
    IF OLD.receipt_digest IS NOT NULL AND ROW(NEW.receipt_digest, NEW.receipt_entries, NEW.receipted_at)
       IS DISTINCT FROM ROW(OLD.receipt_digest, OLD.receipt_entries, OLD.receipted_at) THEN
        RAISE EXCEPTION 'a consumed guard tenant handoff receipt is immutable';
    END IF;
    RETURN NEW;
END $$;
REVOKE ALL ON FUNCTION guard_tenant_handoff_receipt_immutable() FROM PUBLIC;

DROP TRIGGER IF EXISTS guard_tenant_handoff_receipt_immutable ON guard_tenant_handoffs;
CREATE TRIGGER guard_tenant_handoff_receipt_immutable BEFORE UPDATE ON guard_tenant_handoffs
    FOR EACH ROW EXECUTE FUNCTION guard_tenant_handoff_receipt_immutable();
