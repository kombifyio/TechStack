-- Preserve the existing secret-free directory and FORCE-RLS stack authority.
-- Only the stack trigger may project into it; runtime callers use bounded
-- enumeration and exact-tenant compaction, never direct directory privileges.
SET LOCAL lock_timeout = '5s';

DO $drift_owner$
DECLARE owner_oid oid := (SELECT oid FROM pg_catalog.pg_roles WHERE rolname=current_user);
BEGIN
    IF NOT EXISTS(SELECT 1 FROM pg_catalog.pg_class WHERE oid='public.stacks'::regclass
        AND relowner=owner_oid AND relrowsecurity AND relforcerowsecurity)
        OR NOT EXISTS(SELECT 1 FROM pg_catalog.pg_class WHERE oid='public.drift_schedule_tenants'::regclass AND relowner=owner_oid)
        OR EXISTS(SELECT 1 FROM pg_catalog.pg_roles WHERE oid=owner_oid AND (rolsuper OR rolbypassrls)) THEN
        RAISE EXCEPTION 'drift projection requires the exact non-bypass migration owner and FORCE RLS' USING ERRCODE='42501';
    END IF;
END $drift_owner$;

CREATE OR REPLACE FUNCTION public.drift_schedule_wake_tenant()
RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER
SET search_path=pg_catalog,public,pg_temp AS $$
DECLARE scoped text:=NULLIF(current_setting('app.tenant_id',true),'');
BEGIN
    IF TG_TABLE_SCHEMA<>'public' OR TG_TABLE_NAME<>'stacks' OR TG_OP NOT IN ('INSERT','UPDATE')
        OR scoped IS NULL OR NEW.tenant_id IS DISTINCT FROM scoped
        OR (TG_OP='UPDATE' AND OLD.tenant_id IS DISTINCT FROM scoped) THEN
        RAISE EXCEPTION 'drift projection requires the exact stack tenant' USING ERRCODE='42501';
    END IF;
    PERFORM pg_advisory_xact_lock(hashtextextended('techstack.drift.schedule/v1:'||scoped,0));
    IF NEW.deleted_at IS NULL THEN
        INSERT INTO public.drift_schedule_tenants(tenant_id,refreshed_at)
        VALUES(scoped,clock_timestamp())
        ON CONFLICT(tenant_id) DO UPDATE SET refreshed_at=EXCLUDED.refreshed_at;
    END IF;
    RETURN NEW;
END $$;

CREATE FUNCTION public.techstack_list_drift_schedule_tenants(after_tenant text,page_limit integer)
RETURNS TABLE(tenant_id text) LANGUAGE plpgsql SECURITY DEFINER
SET search_path=pg_catalog,public,pg_temp AS $$
BEGIN
    IF page_limit IS NULL OR page_limit<1 OR page_limit>100 THEN
        RAISE EXCEPTION 'bounded drift tenant page required' USING ERRCODE='22023';
    END IF;
    RETURN QUERY SELECT d.tenant_id FROM public.drift_schedule_tenants d
        WHERE d.tenant_id>COALESCE(after_tenant,'') ORDER BY d.tenant_id LIMIT page_limit;
END $$;

CREATE FUNCTION public.techstack_compact_drift_schedule_tenant(target_tenant text)
RETURNS void LANGUAGE plpgsql SECURITY DEFINER
SET search_path=pg_catalog,public,pg_temp AS $$
DECLARE scoped text:=NULLIF(current_setting('app.tenant_id',true),'');
BEGIN
    IF scoped IS NULL OR target_tenant IS DISTINCT FROM scoped THEN
        RAISE EXCEPTION 'drift compaction requires exact tenant scope' USING ERRCODE='42501';
    END IF;
    PERFORM pg_advisory_xact_lock(hashtextextended('techstack.drift.schedule/v1:'||scoped,0));
    DELETE FROM public.drift_schedule_tenants d WHERE d.tenant_id=scoped
        AND d.refreshed_at<clock_timestamp()-interval '1 minute'
        AND NOT EXISTS(SELECT 1 FROM public.stacks s WHERE s.tenant_id=scoped AND s.deleted_at IS NULL);
END $$;

DO $drift_posture$
DECLARE signature text;
BEGIN
    FOREACH signature IN ARRAY ARRAY['drift_schedule_wake_tenant()',
        'techstack_list_drift_schedule_tenants(text,integer)','techstack_compact_drift_schedule_tenant(text)'] LOOP
        EXECUTE format('ALTER FUNCTION public.%s OWNER TO %I',signature,current_user);
        EXECUTE format('REVOKE ALL ON FUNCTION public.%s FROM PUBLIC',signature);
    END LOOP;
END $drift_posture$;
-- No table grant, role replacement, historical migration replay or backfill.
