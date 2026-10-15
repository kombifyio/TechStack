-- Preserve the existing migration-owned lease directory and FORCE-RLS jobs.
-- Only the normal job trigger projects leases; runtime discovery is bounded.
SET LOCAL lock_timeout = '5s';

DO $execution_owner$
DECLARE owner_oid oid := (SELECT oid FROM pg_catalog.pg_roles WHERE rolname=current_user);
BEGIN
    IF NOT EXISTS(SELECT 1 FROM pg_catalog.pg_class WHERE oid='public.jobs'::regclass
        AND relowner=owner_oid AND relrowsecurity AND relforcerowsecurity)
        OR NOT EXISTS(SELECT 1 FROM pg_catalog.pg_class WHERE oid='public.job_execution_reclaim_tenants'::regclass AND relowner=owner_oid)
        OR NOT EXISTS(SELECT 1 FROM pg_catalog.pg_proc WHERE oid='public.jobs_wake_execution_reclaim_tenant()'::regprocedure AND proowner=owner_oid)
        OR NOT EXISTS(SELECT 1 FROM pg_catalog.pg_namespace WHERE nspname='public' AND nspowner=owner_oid)
        OR EXISTS(SELECT 1 FROM pg_catalog.pg_roles WHERE oid=owner_oid AND (rolsuper OR rolbypassrls)) THEN
        RAISE EXCEPTION 'execution projection requires the exact non-bypass migration owner and FORCE RLS' USING ERRCODE='42501';
    END IF;
END $execution_owner$;

CREATE OR REPLACE FUNCTION public.jobs_wake_execution_reclaim_tenant()
RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER
SET search_path=pg_catalog,public,pg_temp AS $$
DECLARE scoped text:=NULLIF(current_setting('app.tenant_id',true),'');
BEGIN
    IF TG_TABLE_SCHEMA<>'public' OR TG_TABLE_NAME<>'jobs' OR TG_OP NOT IN ('INSERT','UPDATE')
        OR scoped IS NULL OR NEW.tenant_id IS DISTINCT FROM scoped
        OR NEW.state<>'running' OR NEW.execution_lease_expires_at IS NULL
        OR (TG_OP='UPDATE' AND (OLD.tenant_id IS DISTINCT FROM scoped OR OLD.id IS DISTINCT FROM NEW.id)) THEN
        RAISE EXCEPTION 'execution projection requires the exact leased job tenant and identity' USING ERRCODE='42501';
    END IF;
    PERFORM pg_advisory_xact_lock(hashtextextended('techstack.jobs.execution-reclaim/v1:'||scoped,0));
    INSERT INTO public.job_execution_reclaim_tenants AS directory(tenant_id,earliest_lease_expires_at,refreshed_at)
    VALUES(scoped,NEW.execution_lease_expires_at,clock_timestamp())
    ON CONFLICT(tenant_id) DO UPDATE SET
        earliest_lease_expires_at=LEAST(directory.earliest_lease_expires_at,EXCLUDED.earliest_lease_expires_at),
        refreshed_at=EXCLUDED.refreshed_at;
    RETURN NEW;
END $$;

CREATE FUNCTION public.techstack_list_job_execution_reclaim_tenants(after_tenant text,lease_cutoff timestamptz,page_limit integer)
RETURNS TABLE(tenant_id text) LANGUAGE plpgsql SECURITY DEFINER
SET search_path=pg_catalog,public,pg_temp AS $$
BEGIN
    IF page_limit IS NULL OR page_limit<1 OR page_limit>100 OR lease_cutoff IS NULL THEN
        RAISE EXCEPTION 'bounded execution tenant page and lease cutoff required' USING ERRCODE='22023';
    END IF;
    RETURN QUERY SELECT d.tenant_id FROM public.job_execution_reclaim_tenants d
        WHERE d.tenant_id>COALESCE(after_tenant,'') AND d.earliest_lease_expires_at<=lease_cutoff
        ORDER BY d.tenant_id LIMIT page_limit;
END $$;

CREATE FUNCTION public.techstack_compact_job_execution_reclaim_tenant(target_tenant text)
RETURNS void LANGUAGE plpgsql SECURITY DEFINER
SET search_path=pg_catalog,public,pg_temp AS $$
DECLARE scoped text:=NULLIF(current_setting('app.tenant_id',true),'');
        earliest timestamptz;
BEGIN
    IF scoped IS NULL OR target_tenant IS DISTINCT FROM scoped THEN
        RAISE EXCEPTION 'execution compaction requires exact tenant scope' USING ERRCODE='42501';
    END IF;
    PERFORM pg_advisory_xact_lock(hashtextextended('techstack.jobs.execution-reclaim/v1:'||scoped,0));
    -- Recompute under FORCE RLS and the same admission/renewal tenant lock.
    SELECT MIN(j.execution_lease_expires_at) INTO earliest FROM public.jobs j
        WHERE j.tenant_id=scoped AND j.state='running' AND j.execution_lease_expires_at IS NOT NULL;
    IF earliest IS NULL THEN
        DELETE FROM public.job_execution_reclaim_tenants d WHERE d.tenant_id=scoped;
    ELSE
        INSERT INTO public.job_execution_reclaim_tenants(tenant_id,earliest_lease_expires_at,refreshed_at)
        VALUES(scoped,earliest,clock_timestamp())
        ON CONFLICT(tenant_id) DO UPDATE SET earliest_lease_expires_at=EXCLUDED.earliest_lease_expires_at,refreshed_at=EXCLUDED.refreshed_at;
    END IF;
END $$;

DO $execution_posture$
DECLARE signature text;
BEGIN
    FOREACH signature IN ARRAY ARRAY['jobs_wake_execution_reclaim_tenant()',
        'techstack_list_job_execution_reclaim_tenants(text,timestamp with time zone,integer)',
        'techstack_compact_job_execution_reclaim_tenant(text)'] LOOP
        EXECUTE format('ALTER FUNCTION public.%s OWNER TO %I',signature,current_user);
        EXECUTE format('REVOKE ALL ON FUNCTION public.%s FROM PUBLIC',signature);
    END LOOP;
END $execution_posture$;
-- No directory grant, historical SQL/trigger replay, role/key effect or backfill.
