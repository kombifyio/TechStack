-- Forward-only: scope the existing durable RIL store, not a parallel workflow authority.
-- Unknown or inconsistent historical tenant custody refuses the whole migration.
LOCK TABLE public.ril_workflow_runs, public.ril_workflow_steps,
 public.ril_workflow_timers, public.techstack_tenants,
 public.techstack_memberships, public.provider_incidents IN ACCESS EXCLUSIVE MODE;

DO $ril_owner$ DECLARE n text; own oid:=(SELECT oid FROM pg_roles WHERE rolname=current_user);
BEGIN
 FOREACH n IN ARRAY ARRAY['ril_workflow_runs','ril_workflow_steps','ril_workflow_timers','techstack_tenants','techstack_memberships','provider_incidents'] LOOP
  IF NOT EXISTS(SELECT 1 FROM pg_class WHERE oid=to_regclass('public.'||n) AND relowner=own) THEN
   RAISE EXCEPTION 'RIL scope migration requires exact table owner' USING ERRCODE='42501';
  END IF;
 END LOOP;
 IF EXISTS(SELECT 1 FROM pg_class c WHERE c.oid IN ('public.ril_workflow_runs'::regclass,'public.ril_workflow_steps'::regclass,'public.ril_workflow_timers'::regclass) AND (c.relrowsecurity OR EXISTS(SELECT 1 FROM pg_policy p WHERE p.polrelid=c.oid))) THEN
  RAISE EXCEPTION 'RIL historical scope is not the expected global source' USING ERRCODE='42501';
 END IF;
 FOREACH n IN ARRAY ARRAY['techstack_tenants','techstack_memberships','provider_incidents'] LOOP
  IF NOT EXISTS(SELECT 1 FROM pg_class WHERE oid=to_regclass('public.'||n) AND relrowsecurity AND relforcerowsecurity)
   OR EXISTS(SELECT 1 FROM pg_policy WHERE polrelid=to_regclass('public.'||n) AND polcmd IN ('r','*') AND NOT polpermissive) THEN
   RAISE EXCEPTION 'RIL tenant snapshot requires intact unrestricted owner snapshot admission' USING ERRCODE='42501';
  END IF;
  EXECUTE format('CREATE POLICY ril_scope_migration_owner_read ON public.%I FOR SELECT TO %I USING (true)',n,current_user);
 END LOOP;
END $ril_owner$;

CREATE TEMP TABLE ril_scope_known_tenants ON COMMIT DROP AS SELECT id FROM public.techstack_tenants;
CREATE TEMP TABLE ril_scope_members ON COMMIT DROP AS
 SELECT tenant_id,user_id,subject_id FROM public.techstack_memberships WHERE status='active';
CREATE TEMP TABLE ril_scope_incidents ON COMMIT DROP AS
 SELECT tenant_id,incident_key,evidence_json FROM public.provider_incidents;
REVOKE ALL ON TABLE ril_scope_known_tenants,ril_scope_members,ril_scope_incidents FROM PUBLIC;
DROP POLICY ril_scope_migration_owner_read ON public.techstack_tenants;
DROP POLICY ril_scope_migration_owner_read ON public.techstack_memberships;
DROP POLICY ril_scope_migration_owner_read ON public.provider_incidents;

CREATE TEMP TABLE ril_scope_run_binding ON COMMIT DROP AS
 SELECT r.run_id,CASE WHEN r.type='provider_incident_advisory' THEN p.tenant_id
  ELSE NULLIF(r.input->>'tenant_id','') END AS tenant_id
 FROM public.ril_workflow_runs r LEFT JOIN ril_scope_incidents p
  ON r.type='provider_incident_advisory' AND r.run_id='provider-incident-'||p.incident_key
  AND r.owner_id=p.tenant_id AND r.input->'evidence'=p.evidence_json
  AND (NOT(r.input ? 'tenant_id') OR r.input->>'tenant_id'=p.tenant_id);
REVOKE ALL ON TABLE ril_scope_run_binding FROM PUBLIC;
DO $ril_binding$ BEGIN
 IF EXISTS(SELECT 1 FROM public.ril_workflow_runs r JOIN ril_scope_run_binding b USING(run_id)
  WHERE b.tenant_id IS NULL OR NOT EXISTS(SELECT 1 FROM ril_scope_known_tenants t WHERE t.id=b.tenant_id)
   OR (r.type<>'provider_incident_advisory' AND NOT EXISTS(SELECT 1 FROM ril_scope_members m WHERE m.tenant_id=b.tenant_id
      AND (m.subject_id=r.owner_id OR m.user_id=r.owner_id)))) THEN
  RAISE EXCEPTION 'RIL historical run lacks registered tenant and canonical owner or incident custody' USING ERRCODE='42501';
 END IF;
END $ril_binding$;

ALTER TABLE public.ril_workflow_runs ADD COLUMN tenant_id text;
ALTER TABLE public.ril_workflow_steps ADD COLUMN tenant_id text;
ALTER TABLE public.ril_workflow_timers ADD COLUMN tenant_id text;
UPDATE public.ril_workflow_runs r SET tenant_id=b.tenant_id FROM ril_scope_run_binding b WHERE b.run_id=r.run_id;
UPDATE public.ril_workflow_steps s SET tenant_id=r.tenant_id FROM public.ril_workflow_runs r WHERE r.run_id=s.run_id;
UPDATE public.ril_workflow_timers t SET tenant_id=r.tenant_id FROM public.ril_workflow_runs r WHERE r.run_id=t.run_id;
ALTER TABLE public.ril_workflow_runs ALTER COLUMN tenant_id SET NOT NULL;
ALTER TABLE public.ril_workflow_steps ALTER COLUMN tenant_id SET NOT NULL;
ALTER TABLE public.ril_workflow_timers ALTER COLUMN tenant_id SET NOT NULL;
ALTER TABLE public.ril_workflow_runs ADD CONSTRAINT ril_run_tenant_ref FOREIGN KEY(tenant_id) REFERENCES public.techstack_tenants(id);
ALTER TABLE public.ril_workflow_runs ADD CONSTRAINT ril_run_tenant_identity UNIQUE(tenant_id,run_id);
ALTER TABLE public.ril_workflow_steps ADD CONSTRAINT ril_step_tenant_run_ref FOREIGN KEY(tenant_id,run_id) REFERENCES public.ril_workflow_runs(tenant_id,run_id) ON DELETE CASCADE;
ALTER TABLE public.ril_workflow_timers ADD CONSTRAINT ril_timer_tenant_run_ref FOREIGN KEY(tenant_id,run_id) REFERENCES public.ril_workflow_runs(tenant_id,run_id) ON DELETE CASCADE;

CREATE TABLE public.ril_workflow_tenants(tenant_id text PRIMARY KEY REFERENCES public.techstack_tenants(id) ON DELETE CASCADE);
REVOKE ALL ON TABLE public.ril_workflow_tenants FROM PUBLIC;
INSERT INTO public.ril_workflow_tenants SELECT DISTINCT tenant_id FROM public.ril_workflow_runs;
DO $ril_backfill_verify$ BEGIN
 IF EXISTS(SELECT 1 FROM public.ril_workflow_runs r JOIN ril_scope_run_binding b USING(run_id)
  WHERE r.tenant_id IS DISTINCT FROM b.tenant_id)
 OR EXISTS((SELECT tenant_id FROM public.ril_workflow_runs EXCEPT SELECT tenant_id FROM public.ril_workflow_tenants)
  UNION ALL (SELECT tenant_id FROM public.ril_workflow_tenants EXCEPT SELECT tenant_id FROM public.ril_workflow_runs)) THEN
  RAISE EXCEPTION 'RIL tenant custody or directory backfill differs' USING ERRCODE='42501';
 END IF;
END $ril_backfill_verify$;

DO $ril_rls$ DECLARE n text;
BEGIN
 FOREACH n IN ARRAY ARRAY['ril_workflow_runs','ril_workflow_steps','ril_workflow_timers'] LOOP
  EXECUTE format('ALTER TABLE public.%I ENABLE ROW LEVEL SECURITY',n);
  EXECUTE format('ALTER TABLE public.%I FORCE ROW LEVEL SECURITY',n);
  EXECUTE format('CREATE POLICY ril_exact_tenant ON public.%I USING (tenant_id=current_setting(''app.tenant_id'',true)) WITH CHECK (tenant_id=current_setting(''app.tenant_id'',true))',n);
 END LOOP;
END $ril_rls$;

CREATE FUNCTION public.techstack_project_ril_workflow_tenant() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public,pg_temp AS $$
DECLARE scoped text:=NULLIF(current_setting('app.tenant_id',true),'');
BEGIN
 IF scoped IS NULL OR (TG_OP<>'INSERT' AND OLD.tenant_id IS DISTINCT FROM scoped)
  OR (TG_OP<>'DELETE' AND NEW.tenant_id IS DISTINCT FROM scoped) THEN
  RAISE EXCEPTION 'RIL projection requires exact tenant' USING ERRCODE='42501';
 END IF;
 -- Serialize last-run deletion against concurrent inserts for this tenant.
 PERFORM pg_advisory_xact_lock(hashtextextended('ril.workflow.tenant/v1:'||scoped,0));
 IF TG_OP='DELETE' THEN
  IF NOT EXISTS(SELECT 1 FROM public.ril_workflow_runs WHERE tenant_id=OLD.tenant_id) THEN
   DELETE FROM public.ril_workflow_tenants WHERE tenant_id=OLD.tenant_id;
  END IF;
  RETURN OLD;
 END IF;
 INSERT INTO public.ril_workflow_tenants(tenant_id) VALUES(NEW.tenant_id) ON CONFLICT DO NOTHING;
 RETURN NEW;
END $$;
REVOKE ALL ON FUNCTION public.techstack_project_ril_workflow_tenant() FROM PUBLIC;
CREATE TRIGGER project_ril_workflow_tenant AFTER INSERT OR UPDATE OR DELETE ON public.ril_workflow_runs
 FOR EACH ROW EXECUTE FUNCTION public.techstack_project_ril_workflow_tenant();

CREATE FUNCTION public.techstack_list_ril_workflow_tenants(after_tenant text,page_limit integer)
RETURNS TABLE(tenant_id text) LANGUAGE plpgsql STABLE SECURITY DEFINER SET search_path=pg_catalog,public,pg_temp AS $$
BEGIN
 IF page_limit IS NULL OR page_limit<1 OR page_limit>500 OR after_tenant IS NULL THEN
  RAISE EXCEPTION 'RIL worker requires bounded cursor' USING ERRCODE='22023';
 END IF;
 RETURN QUERY SELECT d.tenant_id FROM public.ril_workflow_tenants d
  WHERE d.tenant_id>after_tenant ORDER BY d.tenant_id LIMIT page_limit;
END $$;
REVOKE ALL ON FUNCTION public.techstack_list_ril_workflow_tenants(text,integer) FROM PUBLIC;

-- No payload or signing grant is added here. The central managed capability
-- installer admits only the protected reader and exact FORCE-RLS table rights.
DO $ril_complete$ BEGIN
 IF EXISTS(SELECT 1 FROM pg_class WHERE oid IN ('public.ril_workflow_runs'::regclass,'public.ril_workflow_steps'::regclass,'public.ril_workflow_timers'::regclass)
  AND NOT(relrowsecurity AND relforcerowsecurity))
 OR EXISTS(SELECT 1 FROM pg_policy WHERE polname='ril_scope_migration_owner_read') THEN
  RAISE EXCEPTION 'RIL scope migration readback failed' USING ERRCODE='42501';
 END IF;
END $ril_complete$;
