-- Secret-free, migration-owned projection for the existing bounded Guard and
-- backup enumerators. The source tables retain FORCE RLS throughout. No runtime
-- can read/write this directory directly and no role receives BYPASSRLS.
SELECT pg_catalog.set_config('search_path',pg_catalog.quote_ident(pg_catalog.current_schema()) || ', pg_catalog, pg_temp',true);
LOCK TABLE techstack_tenants,guard_reach_states,stack_backup_schedules,stack_backup_stores IN SHARE ROW EXCLUSIVE MODE;

CREATE TABLE IF NOT EXISTS techstack_maintenance_enumeration (
    kind text NOT NULL CHECK (kind IN ('guard','backup_schedule','backup_wipe')),
    tenant_id text NOT NULL REFERENCES techstack_tenants(id),
    target_id text NOT NULL,
    next_due_at timestamptz NOT NULL,
    connect_tenant_id text NOT NULL DEFAULT '',
    server_id text NOT NULL DEFAULT '',
    owner_id text NOT NULL DEFAULT '',
    stack_name text NOT NULL DEFAULT '',
    include_content boolean NOT NULL DEFAULT false,
    token_id text NOT NULL DEFAULT '',
    source_updated_at timestamptz NOT NULL,
    PRIMARY KEY(kind,tenant_id,target_id)
);
REVOKE ALL ON TABLE techstack_maintenance_enumeration FROM PUBLIC;
CREATE INDEX IF NOT EXISTS techstack_maintenance_enumeration_due ON techstack_maintenance_enumeration(kind,next_due_at,tenant_id,target_id);
CREATE INDEX IF NOT EXISTS techstack_maintenance_enumeration_connect ON techstack_maintenance_enumeration(connect_tenant_id,server_id,source_updated_at DESC) WHERE kind='guard';

CREATE OR REPLACE FUNCTION techstack_project_maintenance_enumeration()
RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER SET search_path FROM CURRENT AS $$
DECLARE
    source_kind text;
    scoped_tenant text := NULLIF(current_setting('app.tenant_id',true),'');
    source_tenant text;
    old_target text;
BEGIN
    IF TG_TABLE_NAME='guard_reach_states' THEN source_kind:='guard';
    ELSIF TG_TABLE_NAME='stack_backup_schedules' THEN source_kind:='backup_schedule';
    ELSIF TG_TABLE_NAME='stack_backup_stores' THEN source_kind:='backup_wipe';
    ELSE RAISE EXCEPTION 'unassigned maintenance projection source' USING ERRCODE='42501'; END IF;
    IF TG_OP='DELETE' THEN source_tenant:=OLD.tenant_id; ELSE source_tenant:=NEW.tenant_id; END IF;
    IF source_tenant IS DISTINCT FROM scoped_tenant THEN
        RAISE EXCEPTION 'maintenance projection requires exact tenant scope' USING ERRCODE='42501';
    END IF;
    IF TG_OP IN ('DELETE','UPDATE') THEN
        IF source_kind='guard' THEN old_target:=OLD.worker_id; ELSE old_target:=OLD.stack_id; END IF;
        IF OLD.tenant_id IS DISTINCT FROM scoped_tenant THEN
            RAISE EXCEPTION 'maintenance projection cannot move another tenant' USING ERRCODE='42501';
        END IF;
        DELETE FROM techstack_maintenance_enumeration WHERE kind=source_kind AND tenant_id=OLD.tenant_id AND target_id=old_target;
    END IF;
    IF TG_OP='DELETE' THEN RETURN OLD; END IF;
    IF source_kind='guard' THEN
        INSERT INTO techstack_maintenance_enumeration(kind,tenant_id,target_id,next_due_at,connect_tenant_id,server_id,source_updated_at)
        VALUES(source_kind,NEW.tenant_id,NEW.worker_id,NEW.next_attempt_at,NEW.connect_tenant_id,NEW.server_id,NEW.updated_at)
        ON CONFLICT(kind,tenant_id,target_id) DO UPDATE SET next_due_at=EXCLUDED.next_due_at,connect_tenant_id=EXCLUDED.connect_tenant_id,server_id=EXCLUDED.server_id,source_updated_at=EXCLUDED.source_updated_at;
    ELSIF source_kind='backup_schedule' THEN
      IF NEW.enabled THEN
        INSERT INTO techstack_maintenance_enumeration(kind,tenant_id,target_id,next_due_at,owner_id,stack_name,include_content,source_updated_at)
        VALUES(source_kind,NEW.tenant_id,NEW.stack_id,NEW.next_due_at,NEW.owner_id,NEW.stack_name,NEW.include_content,NEW.updated_at)
        ON CONFLICT(kind,tenant_id,target_id) DO UPDATE SET next_due_at=EXCLUDED.next_due_at,owner_id=EXCLUDED.owner_id,stack_name=EXCLUDED.stack_name,include_content=EXCLUDED.include_content,source_updated_at=EXCLUDED.source_updated_at;
      END IF;
    ELSIF source_kind='backup_wipe' THEN
      IF NEW.wipe_after IS NOT NULL AND NEW.wiped_at IS NULL THEN
        INSERT INTO techstack_maintenance_enumeration(kind,tenant_id,target_id,next_due_at,token_id,source_updated_at)
        VALUES(source_kind,NEW.tenant_id,NEW.stack_id,NEW.wipe_after,NEW.token_id,NEW.updated_at)
        ON CONFLICT(kind,tenant_id,target_id) DO UPDATE SET next_due_at=EXCLUDED.next_due_at,token_id=EXCLUDED.token_id,source_updated_at=EXCLUDED.source_updated_at;
      END IF;
    END IF;
    RETURN NEW;
END $$;

DROP TRIGGER IF EXISTS project_guard_maintenance_enumeration ON guard_reach_states;
CREATE TRIGGER project_guard_maintenance_enumeration AFTER INSERT OR UPDATE OR DELETE ON guard_reach_states FOR EACH ROW EXECUTE FUNCTION techstack_project_maintenance_enumeration();
DROP TRIGGER IF EXISTS project_backup_schedule_enumeration ON stack_backup_schedules;
CREATE TRIGGER project_backup_schedule_enumeration AFTER INSERT OR UPDATE OR DELETE ON stack_backup_schedules FOR EACH ROW EXECUTE FUNCTION techstack_project_maintenance_enumeration();
DROP TRIGGER IF EXISTS project_backup_wipe_enumeration ON stack_backup_stores;
CREATE TRIGGER project_backup_wipe_enumeration AFTER INSERT OR UPDATE OR DELETE ON stack_backup_stores FOR EACH ROW EXECUTE FUNCTION techstack_project_maintenance_enumeration();

-- A transaction-local SELECT policy admits only the authenticated migration
-- owner for a frozen source read. Runtime policies and FORCE RLS do not change.
-- Refuse a restrictive/select policy that could make this snapshot incomplete.
DO $maintenance_snapshot_admission$
DECLARE table_name text; owner_oid oid:=(SELECT oid FROM pg_roles WHERE rolname=current_user);
BEGIN
    FOREACH table_name IN ARRAY ARRAY['techstack_tenants','guard_reach_states','stack_backup_schedules','stack_backup_stores'] LOOP
        IF NOT EXISTS(SELECT 1 FROM pg_class c WHERE c.oid=to_regclass(table_name)
            AND c.relowner=owner_oid AND c.relrowsecurity AND c.relforcerowsecurity) THEN
            RAISE EXCEPTION 'maintenance snapshot requires the exact migration owner and FORCE RLS' USING ERRCODE='42501';
        END IF;
        IF EXISTS(SELECT 1 FROM pg_policy policy WHERE policy.polrelid=to_regclass(table_name)
            AND ((NOT policy.polpermissive AND policy.polcmd IN ('r','*'))
                OR policy.polname='techstack_maintenance_migration_snapshot')) THEN
            RAISE EXCEPTION 'maintenance snapshot authority is ambiguous or restrictive' USING ERRCODE='42501';
        END IF;
        EXECUTE format('CREATE POLICY techstack_maintenance_migration_snapshot ON %I FOR SELECT TO %I USING (true)',table_name,current_user);
    END LOOP;
END $maintenance_snapshot_admission$;

CREATE TEMP TABLE techstack_maintenance_migration_tenants ON COMMIT DROP AS SELECT id FROM techstack_tenants;
REVOKE ALL ON TABLE techstack_maintenance_migration_tenants FROM PUBLIC;
CREATE TEMP TABLE techstack_maintenance_migration_snapshot ON COMMIT DROP AS
    SELECT 'guard'::text AS kind,tenant_id,worker_id AS target_id,next_attempt_at AS next_due_at,
        connect_tenant_id,server_id,''::text AS owner_id,''::text AS stack_name,false AS include_content,''::text AS token_id,updated_at AS source_updated_at
    FROM guard_reach_states
    UNION ALL
    SELECT 'backup_schedule',tenant_id,stack_id,next_due_at,'','',owner_id,stack_name,include_content,'',updated_at
    FROM stack_backup_schedules WHERE enabled
    UNION ALL
    SELECT 'backup_wipe',tenant_id,stack_id,wipe_after,'','','','',false,token_id,updated_at
    FROM stack_backup_stores WHERE wipe_after IS NOT NULL AND wiped_at IS NULL;
REVOKE ALL ON TABLE techstack_maintenance_migration_snapshot FROM PUBLIC;

DO $maintenance_snapshot_readback$
DECLARE table_name text;
BEGIN
    IF EXISTS(SELECT tenant_id FROM techstack_maintenance_migration_snapshot
        EXCEPT SELECT id FROM techstack_maintenance_migration_tenants) THEN
        RAISE EXCEPTION 'maintenance sources contain an unregistered tenant; backfill incomplete' USING ERRCODE='55000';
    END IF;
    -- Snapshot creation and readback share source write locks and the owner-only
    -- complete SELECT authority. No ambient tenant GUC limits either read.
    IF EXISTS((SELECT id FROM techstack_tenants EXCEPT SELECT id FROM techstack_maintenance_migration_tenants)
        UNION ALL (SELECT id FROM techstack_maintenance_migration_tenants EXCEPT SELECT id FROM techstack_tenants)) THEN
        RAISE EXCEPTION 'maintenance tenant snapshot changed' USING ERRCODE='55000';
    END IF;
    FOREACH table_name IN ARRAY ARRAY['techstack_tenants','guard_reach_states','stack_backup_schedules','stack_backup_stores'] LOOP
        EXECUTE format('DROP POLICY techstack_maintenance_migration_snapshot ON %I',table_name);
    END LOOP;
END $maintenance_snapshot_readback$;

-- Backfill returns to exact-tenant source RLS after removing the transient read
-- authority. The complete frozen tenant snapshot remains the enumeration input.
DO $maintenance_backfill$
DECLARE scope record; previous_tenant text:=current_setting('app.tenant_id',true);
BEGIN
    FOR scope IN SELECT id FROM techstack_maintenance_migration_tenants ORDER BY id LOOP
        PERFORM set_config('app.tenant_id',scope.id,true);
        INSERT INTO techstack_maintenance_enumeration(kind,tenant_id,target_id,next_due_at,connect_tenant_id,server_id,source_updated_at)
        SELECT 'guard',tenant_id,worker_id,next_attempt_at,connect_tenant_id,server_id,updated_at FROM guard_reach_states
        WHERE tenant_id=scope.id ON CONFLICT(kind,tenant_id,target_id) DO UPDATE SET next_due_at=EXCLUDED.next_due_at,connect_tenant_id=EXCLUDED.connect_tenant_id,server_id=EXCLUDED.server_id,source_updated_at=EXCLUDED.source_updated_at;
        INSERT INTO techstack_maintenance_enumeration(kind,tenant_id,target_id,next_due_at,owner_id,stack_name,include_content,source_updated_at)
        SELECT 'backup_schedule',tenant_id,stack_id,next_due_at,owner_id,stack_name,include_content,updated_at FROM stack_backup_schedules
        WHERE tenant_id=scope.id AND enabled ON CONFLICT(kind,tenant_id,target_id) DO UPDATE SET next_due_at=EXCLUDED.next_due_at,owner_id=EXCLUDED.owner_id,stack_name=EXCLUDED.stack_name,include_content=EXCLUDED.include_content,source_updated_at=EXCLUDED.source_updated_at;
        INSERT INTO techstack_maintenance_enumeration(kind,tenant_id,target_id,next_due_at,token_id,source_updated_at)
        SELECT 'backup_wipe',tenant_id,stack_id,wipe_after,token_id,updated_at FROM stack_backup_stores
        WHERE tenant_id=scope.id AND wipe_after IS NOT NULL AND wiped_at IS NULL ON CONFLICT(kind,tenant_id,target_id) DO UPDATE SET next_due_at=EXCLUDED.next_due_at,token_id=EXCLUDED.token_id,source_updated_at=EXCLUDED.source_updated_at;
    END LOOP;
    PERFORM set_config('app.tenant_id',COALESCE(previous_tenant,''),true);
EXCEPTION WHEN OTHERS THEN
    PERFORM set_config('app.tenant_id',COALESCE(previous_tenant,''),true);
    RAISE;
END $maintenance_backfill$;

-- Reader switching is admitted only after exact, bidirectional fact readback.
-- No fixed counts, empty-genesis inference or approximate pg_class statistics.
DO $maintenance_projection_completeness$
BEGIN
    IF EXISTS((SELECT kind,tenant_id,target_id,next_due_at,connect_tenant_id,server_id,owner_id,stack_name,include_content,token_id,source_updated_at
        FROM techstack_maintenance_migration_snapshot
        EXCEPT SELECT kind,tenant_id,target_id,next_due_at,connect_tenant_id,server_id,owner_id,stack_name,include_content,token_id,source_updated_at
        FROM techstack_maintenance_enumeration)
        UNION ALL
        (SELECT kind,tenant_id,target_id,next_due_at,connect_tenant_id,server_id,owner_id,stack_name,include_content,token_id,source_updated_at
        FROM techstack_maintenance_enumeration
        EXCEPT SELECT kind,tenant_id,target_id,next_due_at,connect_tenant_id,server_id,owner_id,stack_name,include_content,token_id,source_updated_at
        FROM techstack_maintenance_migration_snapshot)) THEN
        RAISE EXCEPTION 'maintenance projection differs from frozen complete source snapshot; reader switch refused' USING ERRCODE='55000';
    END IF;
    IF EXISTS(SELECT 1 FROM pg_class c WHERE c.oid IN (to_regclass('techstack_tenants'),to_regclass('guard_reach_states'),to_regclass('stack_backup_schedules'),to_regclass('stack_backup_stores'))
        AND (NOT c.relrowsecurity OR NOT c.relforcerowsecurity))
        OR EXISTS(SELECT 1 FROM pg_policy WHERE polname='techstack_maintenance_migration_snapshot') THEN
        RAISE EXCEPTION 'maintenance source RLS or transient read authority not restored' USING ERRCODE='42501';
    END IF;
END $maintenance_projection_completeness$;

CREATE OR REPLACE FUNCTION techstack_list_due_guard_reach(p_now timestamptz,p_after_tenant text,p_after_worker text,p_limit integer)
RETURNS TABLE(tenant_id text,worker_id text) LANGUAGE sql SECURITY DEFINER SET search_path FROM CURRENT AS $$
    SELECT e.tenant_id,e.target_id FROM techstack_maintenance_enumeration e WHERE e.kind='guard' AND e.next_due_at<=p_now
    AND (e.tenant_id,e.target_id)>(COALESCE(p_after_tenant,''),COALESCE(p_after_worker,'')) ORDER BY e.tenant_id,e.target_id
    LIMIT LEAST(GREATEST(COALESCE(p_limit,50),1),200)
$$;
CREATE OR REPLACE FUNCTION techstack_find_guard_reach_by_connect(p_connect_tenant text,p_server text)
RETURNS TABLE(tenant_id text,worker_id text) LANGUAGE sql SECURITY DEFINER SET search_path FROM CURRENT AS $$
    SELECT e.tenant_id,e.target_id FROM techstack_maintenance_enumeration e WHERE e.kind='guard' AND e.connect_tenant_id=p_connect_tenant AND e.server_id=p_server
    ORDER BY e.source_updated_at DESC LIMIT 8
$$;
CREATE OR REPLACE FUNCTION techstack_list_due_backup_schedules(p_now timestamptz,p_after_tenant text,p_after_stack text,p_limit integer)
RETURNS TABLE(tenant_id text,stack_id text,owner_id text,stack_name text,include_content boolean) LANGUAGE sql SECURITY DEFINER SET search_path FROM CURRENT AS $$
    SELECT e.tenant_id,e.target_id,e.owner_id,e.stack_name,e.include_content FROM techstack_maintenance_enumeration e WHERE e.kind='backup_schedule' AND e.next_due_at<=p_now
    AND (e.tenant_id,e.target_id)>(COALESCE(p_after_tenant,''),COALESCE(p_after_stack,'')) ORDER BY e.tenant_id,e.target_id
    LIMIT LEAST(GREATEST(COALESCE(p_limit,50),1),200)
$$;
CREATE OR REPLACE FUNCTION techstack_list_due_backup_wipes(p_now timestamptz,p_after_tenant text,p_after_stack text,p_limit integer)
RETURNS TABLE(tenant_id text,stack_id text,token_id text) LANGUAGE sql SECURITY DEFINER SET search_path FROM CURRENT AS $$
    SELECT e.tenant_id,e.target_id,e.token_id FROM techstack_maintenance_enumeration e WHERE e.kind='backup_wipe' AND e.next_due_at<=p_now
    AND (e.tenant_id,e.target_id)>(COALESCE(p_after_tenant,''),COALESCE(p_after_stack,'')) ORDER BY e.tenant_id,e.target_id
    LIMIT LEAST(GREATEST(COALESCE(p_limit,50),1),200)
$$;

DO $maintenance_authority_posture$
DECLARE active_schema text:=current_schema(); owner_role text:=current_user; signature text;
BEGIN
    FOREACH signature IN ARRAY ARRAY['techstack_project_maintenance_enumeration()',
        'techstack_list_due_guard_reach(timestamptz,text,text,integer)','techstack_find_guard_reach_by_connect(text,text)',
        'techstack_list_due_backup_schedules(timestamptz,text,text,integer)','techstack_list_due_backup_wipes(timestamptz,text,text,integer)'] LOOP
        EXECUTE format('ALTER FUNCTION %I.%s OWNER TO %I',active_schema,signature,owner_role);
        EXECUTE format('ALTER FUNCTION %I.%s SET search_path TO pg_catalog,%I,pg_temp',active_schema,signature,active_schema);
        EXECUTE format('REVOKE ALL ON FUNCTION %I.%s FROM PUBLIC',active_schema,signature);
    END LOOP;
END $maintenance_authority_posture$;
