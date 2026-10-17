-- Cross-tenant subject directory for the Cloud data-subject participant.
-- Row-level security hides every other tenant, so an opaque subject ref
-- (sr1_ + base64url(sha256("kombify:subject:v1:" + subject))) cannot be
-- resolved from tenant-scoped reads. This migration-owned projection maps the
-- ref to the (tenant, owner subject) pairs that already exist in owner-scoped
-- rows. It is maintained by trigger and readable only through an exact-ref
-- lookup; source tables keep FORCE RLS and nothing here bypasses it.
-- Additive and backward compatible: no existing table, policy or function changes.
SET LOCAL lock_timeout = '5s';
SELECT pg_catalog.set_config('search_path',pg_catalog.quote_ident(pg_catalog.current_schema()) || ', pg_catalog, pg_temp',true);

CREATE OR REPLACE FUNCTION techstack_subject_ref(p_subject text)
RETURNS text LANGUAGE sql IMMUTABLE STRICT SET search_path FROM CURRENT AS $$
    SELECT 'sr1_' || translate(rtrim(encode(sha256(convert_to('kombify:subject:v1:' || p_subject, 'UTF8')), 'base64'), '='), '+/', '-_')
$$;

CREATE TABLE IF NOT EXISTS techstack_subject_directory (
    tenant_id text NOT NULL CHECK (btrim(tenant_id) <> ''),
    owner_subject_id text NOT NULL CHECK (btrim(owner_subject_id) <> ''),
    subject_ref text GENERATED ALWAYS AS (techstack_subject_ref(owner_subject_id)) STORED,
    PRIMARY KEY (tenant_id, owner_subject_id)
);
REVOKE ALL ON TABLE techstack_subject_directory FROM PUBLIC;
CREATE INDEX IF NOT EXISTS techstack_subject_directory_ref ON techstack_subject_directory(subject_ref);

-- The directory carries no foreign key, so a source write can never be blocked by it.
-- Records only the pair carried by the changed row itself; it reads no other
-- row and never raises, so it cannot block a source write.
CREATE OR REPLACE FUNCTION techstack_project_subject_directory()
RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER SET search_path FROM CURRENT AS $$
DECLARE subject_value text := NULLIF(btrim(to_jsonb(NEW)->>TG_ARGV[0]), '');
        tenant_value text := NULLIF(btrim(to_jsonb(NEW)->>'tenant_id'), '');
BEGIN
    IF subject_value IS NOT NULL AND tenant_value IS NOT NULL THEN
        INSERT INTO techstack_subject_directory(tenant_id, owner_subject_id)
        VALUES (tenant_value, subject_value) ON CONFLICT DO NOTHING;
    END IF;
    RETURN NEW;
END $$;

-- Exact-ref lookup, the only read path. A malformed ref is refused, so the
-- function cannot enumerate the directory.
CREATE OR REPLACE FUNCTION techstack_lookup_subject_directory(p_ref text)
RETURNS TABLE(tenant_id text, owner_subject_id text) LANGUAGE plpgsql SECURITY DEFINER STABLE SET search_path FROM CURRENT AS $$
BEGIN
    IF p_ref IS NULL OR p_ref !~ '^sr1_[A-Za-z0-9_-]{43}$' THEN
        RAISE EXCEPTION 'exact subject ref required' USING ERRCODE = '22023';
    END IF;
    RETURN QUERY SELECT d.tenant_id, d.owner_subject_id FROM techstack_subject_directory d
        WHERE d.subject_ref = p_ref ORDER BY d.tenant_id LIMIT 64;
END $$;

-- Erasure removes the pair inside its tenant-scoped transaction.
CREATE OR REPLACE FUNCTION techstack_forget_subject_directory(p_tenant text, p_subject text)
RETURNS void LANGUAGE plpgsql SECURITY DEFINER SET search_path FROM CURRENT AS $$
DECLARE scoped text := NULLIF(current_setting('app.tenant_id', true), '');
BEGIN
    IF scoped IS NULL OR p_tenant IS DISTINCT FROM scoped THEN
        RAISE EXCEPTION 'subject directory forget requires exact tenant scope' USING ERRCODE = '42501';
    END IF;
    DELETE FROM techstack_subject_directory d WHERE d.tenant_id = scoped AND d.owner_subject_id = p_subject;
END $$;

-- Attach the projection to every owner-scoped table (same discovery as the
-- participant: owner_subject_id, plus the lease table's subject_id).
DO $subject_directory_triggers$
DECLARE src record;
BEGIN
    FOR src IN
        SELECT c.table_name, c.column_name FROM information_schema.columns c
        JOIN information_schema.tables t ON t.table_schema = c.table_schema AND t.table_name = c.table_name AND t.table_type = 'BASE TABLE'
        JOIN information_schema.columns tc ON tc.table_schema = c.table_schema AND tc.table_name = c.table_name AND tc.column_name = 'tenant_id'
        WHERE c.table_schema = current_schema()
          AND (c.column_name = 'owner_subject_id' OR (c.table_name = 'techstack_vm_leases' AND c.column_name = 'subject_id'))
          AND c.table_name <> 'techstack_subject_directory'
    LOOP
        EXECUTE format('DROP TRIGGER IF EXISTS techstack_subject_directory_project ON %I', src.table_name);
        EXECUTE format('CREATE TRIGGER techstack_subject_directory_project AFTER INSERT OR UPDATE OF %I ON %I FOR EACH ROW EXECUTE FUNCTION techstack_project_subject_directory(%L)',
            src.column_name, src.table_name, src.column_name);
    END LOOP;
END $subject_directory_triggers$;

-- Backfill from existing rows, one exact tenant scope at a time, so FORCE RLS
-- stays in force. The tenant list is read through a transient owner-only
-- SELECT policy that is removed again before the backfill runs.
DO $subject_directory_tenants$
BEGIN
    IF NOT EXISTS(SELECT 1 FROM pg_class c WHERE c.oid = to_regclass('techstack_tenants')
        AND c.relowner = (SELECT oid FROM pg_roles WHERE rolname = current_user) AND c.relrowsecurity AND c.relforcerowsecurity) THEN
        RAISE EXCEPTION 'subject directory backfill requires the exact migration owner and FORCE RLS' USING ERRCODE = '42501';
    END IF;
    EXECUTE format('CREATE POLICY techstack_subject_directory_snapshot ON techstack_tenants FOR SELECT TO %I USING (true)', current_user);
END $subject_directory_tenants$;
CREATE TEMP TABLE techstack_subject_directory_tenants ON COMMIT DROP AS SELECT id FROM techstack_tenants;
REVOKE ALL ON TABLE techstack_subject_directory_tenants FROM PUBLIC;
DROP POLICY techstack_subject_directory_snapshot ON techstack_tenants;

DO $subject_directory_backfill$
DECLARE scope record; src record; previous_tenant text := current_setting('app.tenant_id', true);
BEGIN
    FOR scope IN SELECT id FROM techstack_subject_directory_tenants ORDER BY id LOOP
        PERFORM set_config('app.tenant_id', scope.id, true);
        FOR src IN
            SELECT t.tgrelid::regclass::text AS rel, replace(encode(t.tgargs, 'escape'), '\000', '') AS col
            FROM pg_trigger t WHERE t.tgname = 'techstack_subject_directory_project' AND NOT t.tgisinternal
        LOOP
            EXECUTE format('INSERT INTO techstack_subject_directory(tenant_id, owner_subject_id) SELECT DISTINCT tenant_id, btrim(%I) FROM %s WHERE tenant_id = $1 AND NULLIF(btrim(%I), '''') IS NOT NULL ON CONFLICT DO NOTHING',
                src.col, src.rel, src.col)
            USING scope.id;
        END LOOP;
    END LOOP;
    PERFORM set_config('app.tenant_id', COALESCE(previous_tenant, ''), true);
EXCEPTION WHEN OTHERS THEN
    PERFORM set_config('app.tenant_id', COALESCE(previous_tenant, ''), true);
    RAISE;
END $subject_directory_backfill$;

DO $subject_directory_posture$
DECLARE active_schema text := current_schema(); owner_role text := current_user; signature text;
BEGIN
    IF EXISTS(SELECT 1 FROM pg_policy WHERE polname = 'techstack_subject_directory_snapshot') THEN
        RAISE EXCEPTION 'subject directory transient read authority not removed' USING ERRCODE = '42501';
    END IF;
    FOREACH signature IN ARRAY ARRAY['techstack_project_subject_directory()', 'techstack_lookup_subject_directory(text)',
        'techstack_forget_subject_directory(text,text)'] LOOP
        EXECUTE format('ALTER FUNCTION %I.%s OWNER TO %I', active_schema, signature, owner_role);
        EXECUTE format('ALTER FUNCTION %I.%s SET search_path TO pg_catalog,%I,pg_temp', active_schema, signature, active_schema);
        EXECUTE format('REVOKE ALL ON FUNCTION %I.%s FROM PUBLIC', active_schema, signature);
    END LOOP;
END $subject_directory_posture$;
