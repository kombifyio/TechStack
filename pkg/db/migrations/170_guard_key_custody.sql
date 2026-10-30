-- kombify Guard G04 cross-tenant handoff, server-authoritative part. Migration
-- 169 trusted the re-pairing Guard's own declaration of what it holds. This
-- migration lets the control plane find out itself whether the host key that a
-- pairing proves possession of already served a worker of another tenant, and
-- binds every handoff confirmation to that key and to the pairing that
-- consumes it.
--
-- guard_key_custody is a secret-free, migration-owned directory: key thumbprint
-- -> tenant and worker. Only the SECURITY DEFINER triggers below write it and
-- only techstack_find_guard_key_custody reads it; no runtime role reads or
-- writes it directly and no role receives BYPASSRLS; it has no row security,
-- so the application privilege policy (pkg/db/application_privileges.go)
-- grants it to no runtime role. A link ends only when the prior tenant's
-- confirmation for that exact key is consumed by a rebind: deleting a worker
-- or its identity keeps it, because the server may still hold that tenant's
-- evidence (fail closed).
SELECT pg_catalog.set_config('search_path',pg_catalog.quote_ident(pg_catalog.current_schema()) || ', pg_catalog, pg_temp',true);

ALTER TABLE guard_tenant_handoffs
    ADD COLUMN IF NOT EXISTS key_thumbprint text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS consumed_by_tenant_id text,
    ADD COLUMN IF NOT EXISTS consumed_by_worker_id text,
    ADD COLUMN IF NOT EXISTS consumed_by_key_thumbprint text;
ALTER TABLE guard_tenant_handoffs DROP CONSTRAINT IF EXISTS guard_tenant_handoffs_consumer_complete;
ALTER TABLE guard_tenant_handoffs ADD CONSTRAINT guard_tenant_handoffs_consumer_complete
    CHECK ((receipt_digest IS NULL) = (consumed_by_tenant_id IS NULL)
       AND (receipt_digest IS NULL) = (consumed_by_worker_id IS NULL)
       AND (receipt_digest IS NULL) = (consumed_by_key_thumbprint IS NULL)) NOT VALID;
-- A receipt consumed under migration 169 has no recorded consumer. Such a row
-- is kept unchanged as the audit record (the immutability trigger forbids
-- inventing a consumer), and it can never be repeated or consumed again: its
-- empty consumer and key match no pairing. The constraint binds every new and
-- changed row at once and is validated when no such legacy row exists.
-- Validation scans the table without row security, so it sees every tenant.
DO $consumer_complete$
BEGIN
    ALTER TABLE guard_tenant_handoffs VALIDATE CONSTRAINT guard_tenant_handoffs_consumer_complete;
EXCEPTION WHEN check_violation THEN
    RAISE NOTICE 'guard_tenant_handoffs keeps receipts consumed before migration 170 without a recorded consumer; the consumer constraint stays NOT VALID for them';
END $consumer_complete$;

CREATE OR REPLACE FUNCTION guard_tenant_handoff_receipt_immutable() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, public AS $$
BEGIN
    IF ROW(NEW.tenant_id, NEW.handoff_id, NEW.worker_id, NEW.server_id, NEW.disposition,
           NEW.confirmation_sha256, NEW.confirmed_by, NEW.confirmed_at, NEW.expires_at, NEW.key_thumbprint)
       IS DISTINCT FROM ROW(OLD.tenant_id, OLD.handoff_id, OLD.worker_id, OLD.server_id, OLD.disposition,
           OLD.confirmation_sha256, OLD.confirmed_by, OLD.confirmed_at, OLD.expires_at, OLD.key_thumbprint) THEN
        RAISE EXCEPTION 'guard tenant handoff confirmation is immutable';
    END IF;
    IF OLD.receipt_digest IS NOT NULL AND ROW(NEW.receipt_digest, NEW.receipt_entries, NEW.receipted_at,
           NEW.consumed_by_tenant_id, NEW.consumed_by_worker_id, NEW.consumed_by_key_thumbprint)
       IS DISTINCT FROM ROW(OLD.receipt_digest, OLD.receipt_entries, OLD.receipted_at,
           OLD.consumed_by_tenant_id, OLD.consumed_by_worker_id, OLD.consumed_by_key_thumbprint) THEN
        RAISE EXCEPTION 'a consumed guard tenant handoff receipt is immutable';
    END IF;
    RETURN NEW;
END $$;
REVOKE ALL ON FUNCTION guard_tenant_handoff_receipt_immutable() FROM PUBLIC;

CREATE TABLE IF NOT EXISTS guard_key_custody (
    key_thumbprint text NOT NULL CHECK (BTRIM(key_thumbprint) <> ''),
    tenant_id text NOT NULL CHECK (BTRIM(tenant_id) <> ''),
    worker_id text NOT NULL CHECK (BTRIM(worker_id) <> ''),
    linked_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (key_thumbprint, tenant_id, worker_id)
);
REVOKE ALL ON TABLE guard_key_custody FROM PUBLIC;
CREATE INDEX IF NOT EXISTS guard_key_custody_worker ON guard_key_custody (tenant_id, worker_id);

-- Every key a worker identity binds is linked to that worker. Rotated keys
-- keep their link: the server may still hold evidence produced under it.
CREATE OR REPLACE FUNCTION techstack_project_guard_key_custody()
RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER SET search_path FROM CURRENT AS $$
BEGIN
    IF BTRIM(COALESCE(NEW.key_thumbprint, '')) <> '' THEN
        INSERT INTO guard_key_custody (key_thumbprint, tenant_id, worker_id)
        VALUES (NEW.key_thumbprint, NEW.tenant_id, NEW.worker_id)
        ON CONFLICT DO NOTHING;
    END IF;
    RETURN NEW;
END $$;

DROP TRIGGER IF EXISTS worker_identities_guard_key_custody ON worker_identities;
CREATE TRIGGER worker_identities_guard_key_custody
    AFTER INSERT ON worker_identities
    FOR EACH ROW EXECUTE FUNCTION techstack_project_guard_key_custody();
-- Only a changed key adds a link: rewriting the same thumbprint must not
-- restore a link a consumed handoff released.
DROP TRIGGER IF EXISTS worker_identities_guard_key_custody_rotation ON worker_identities;
CREATE TRIGGER worker_identities_guard_key_custody_rotation
    AFTER UPDATE OF key_thumbprint ON worker_identities
    FOR EACH ROW WHEN (OLD.key_thumbprint IS DISTINCT FROM NEW.key_thumbprint)
    EXECUTE FUNCTION techstack_project_guard_key_custody();

-- A consumed confirmation for exactly the key it was confirmed for releases
-- that key from the prior tenant. Consumption runs in the prior tenant's scope.
CREATE OR REPLACE FUNCTION techstack_release_guard_key_custody()
RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER SET search_path FROM CURRENT AS $$
BEGIN
    IF NEW.tenant_id IS DISTINCT FROM NULLIF(current_setting('app.tenant_id', true), '') THEN
        RAISE EXCEPTION 'guard key custody release requires exact tenant scope' USING ERRCODE = '42501';
    END IF;
    DELETE FROM guard_key_custody WHERE key_thumbprint = NEW.key_thumbprint AND tenant_id = NEW.tenant_id;
    RETURN NEW;
END $$;

DROP TRIGGER IF EXISTS guard_tenant_handoffs_release_key_custody ON guard_tenant_handoffs;
CREATE TRIGGER guard_tenant_handoffs_release_key_custody
    AFTER UPDATE OF receipt_digest ON guard_tenant_handoffs
    FOR EACH ROW
    WHEN (OLD.receipt_digest IS NULL AND NEW.receipt_digest IS NOT NULL
          AND NEW.key_thumbprint <> '' AND NEW.consumed_by_key_thumbprint = NEW.key_thumbprint)
    EXECUTE FUNCTION techstack_release_guard_key_custody();

-- The tenants other than p_tenant whose workers used this key. Called only
-- after the caller proved possession of the key.
CREATE OR REPLACE FUNCTION techstack_find_guard_key_custody(p_thumbprint text, p_tenant text)
RETURNS TABLE(tenant_id text) LANGUAGE sql SECURITY DEFINER SET search_path FROM CURRENT AS $$
    SELECT DISTINCT c.tenant_id FROM guard_key_custody c
    WHERE c.key_thumbprint = p_thumbprint AND c.tenant_id <> COALESCE(p_tenant, '')
    ORDER BY c.tenant_id LIMIT 16
$$;

INSERT INTO guard_key_custody (key_thumbprint, tenant_id, worker_id)
SELECT key_thumbprint, tenant_id, worker_id FROM worker_identities
WHERE BTRIM(key_thumbprint) <> ''
ON CONFLICT DO NOTHING;

DO $guard_key_custody_posture$
DECLARE active_schema text := current_schema(); owner_role text := current_user; signature text;
BEGIN
    FOREACH signature IN ARRAY ARRAY['techstack_project_guard_key_custody()', 'techstack_release_guard_key_custody()',
        'techstack_find_guard_key_custody(text,text)'] LOOP
        EXECUTE format('ALTER FUNCTION %I.%s OWNER TO %I', active_schema, signature, owner_role);
        EXECUTE format('ALTER FUNCTION %I.%s SET search_path TO pg_catalog,%I,pg_temp', active_schema, signature, active_schema);
        EXECUTE format('REVOKE ALL ON FUNCTION %I.%s FROM PUBLIC', active_schema, signature);
    END LOOP;
END $guard_key_custody_posture$;
