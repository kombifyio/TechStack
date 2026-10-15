-- Preserve the migration-owned secret-free directory and FORCE-RLS jobs.
-- Runtime callers receive only bounded enumeration and exact-tenant pruning.
SET LOCAL lock_timeout = '5s';

DO $pending_owner$
DECLARE owner_oid oid := (SELECT oid FROM pg_catalog.pg_roles WHERE rolname=current_user);
BEGIN
    IF NOT EXISTS(SELECT 1 FROM pg_catalog.pg_class WHERE oid='public.jobs'::regclass
        AND relowner=owner_oid AND relrowsecurity AND relforcerowsecurity)
        OR NOT EXISTS(SELECT 1 FROM pg_catalog.pg_class WHERE oid='public.pending_job_tenants'::regclass AND relowner=owner_oid)
        OR NOT EXISTS(SELECT 1 FROM pg_catalog.pg_proc WHERE oid='public.jobs_wake_pending_tenant()'::regprocedure AND proowner=owner_oid)
        OR NOT EXISTS(SELECT 1 FROM pg_catalog.pg_namespace WHERE nspname='public' AND nspowner=owner_oid)
        OR EXISTS(SELECT 1 FROM pg_catalog.pg_roles WHERE oid=owner_oid AND (rolsuper OR rolbypassrls)) THEN
        RAISE EXCEPTION 'pending projection requires the exact non-bypass migration owner and FORCE RLS' USING ERRCODE='42501';
    END IF;
END $pending_owner$;

CREATE OR REPLACE FUNCTION public.jobs_wake_pending_tenant()
RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER
SET search_path=pg_catalog,public,pg_temp AS $$
DECLARE scoped text:=NULLIF(current_setting('app.tenant_id',true),'');
BEGIN
    IF TG_TABLE_SCHEMA<>'public' OR TG_TABLE_NAME<>'jobs' OR TG_OP NOT IN ('INSERT','UPDATE')
        OR scoped IS NULL OR NEW.tenant_id IS DISTINCT FROM scoped
        OR NEW.state<>'pending'
        OR (TG_OP='UPDATE' AND OLD.tenant_id IS DISTINCT FROM scoped) THEN
        RAISE EXCEPTION 'pending projection requires the exact job tenant' USING ERRCODE='42501';
    END IF;
    PERFORM pg_advisory_xact_lock(hashtextextended('techstack.jobs.pending/v1:'||scoped,0));
    INSERT INTO public.pending_job_tenants AS directory(tenant_id,earliest_scheduled_for,refreshed_at)
    VALUES(scoped,NEW.scheduled_for,clock_timestamp())
    ON CONFLICT(tenant_id) DO UPDATE SET
        earliest_scheduled_for=LEAST(directory.earliest_scheduled_for,EXCLUDED.earliest_scheduled_for),
        refreshed_at=EXCLUDED.refreshed_at;
    RETURN NEW;
END $$;

CREATE FUNCTION public.techstack_list_pending_job_tenants(after_tenant text,page_limit integer)
RETURNS TABLE(tenant_id text) LANGUAGE plpgsql SECURITY DEFINER
SET search_path=pg_catalog,public,pg_temp AS $$
BEGIN
    IF page_limit IS NULL OR page_limit<1 OR page_limit>100 THEN
        RAISE EXCEPTION 'bounded pending tenant page required' USING ERRCODE='22023';
    END IF;
    RETURN QUERY SELECT d.tenant_id FROM public.pending_job_tenants d
        WHERE d.tenant_id>COALESCE(after_tenant,'') ORDER BY d.tenant_id LIMIT page_limit;
END $$;

CREATE FUNCTION public.techstack_compact_pending_job_tenant(target_tenant text)
RETURNS void LANGUAGE plpgsql SECURITY DEFINER
SET search_path=pg_catalog,public,pg_temp AS $$
DECLARE scoped text:=NULLIF(current_setting('app.tenant_id',true),'');
        earliest timestamptz;
BEGIN
    IF scoped IS NULL OR target_tenant IS DISTINCT FROM scoped THEN
        RAISE EXCEPTION 'pending compaction requires exact tenant scope' USING ERRCODE='42501';
    END IF;
    PERFORM pg_advisory_xact_lock(hashtextextended('techstack.jobs.pending/v1:'||scoped,0));
    -- FORCE RLS applies even to this non-bypass owner. Recompute under the
    -- same tenant lock as the admission trigger before retiring the directory.
    SELECT MIN(j.scheduled_for) INTO earliest FROM public.jobs j WHERE j.tenant_id=scoped AND j.state='pending';
    IF earliest IS NULL THEN
        DELETE FROM public.pending_job_tenants d WHERE d.tenant_id=scoped;
    ELSE
        INSERT INTO public.pending_job_tenants(tenant_id,earliest_scheduled_for,refreshed_at)
        VALUES(scoped,earliest,clock_timestamp())
        ON CONFLICT(tenant_id) DO UPDATE SET earliest_scheduled_for=EXCLUDED.earliest_scheduled_for,refreshed_at=EXCLUDED.refreshed_at;
    END IF;
END $$;

DO $pending_posture$
DECLARE signature text;
BEGIN
    FOREACH signature IN ARRAY ARRAY['jobs_wake_pending_tenant()',
        'techstack_list_pending_job_tenants(text,integer)','techstack_compact_pending_job_tenant(text)'] LOOP
        EXECUTE format('ALTER FUNCTION public.%s OWNER TO %I',signature,current_user);
        EXECUTE format('REVOKE ALL ON FUNCTION public.%s FROM PUBLIC',signature);
    END LOOP;
END $pending_posture$;
-- No directory grant, historical SQL replay, trigger replacement or backfill.
