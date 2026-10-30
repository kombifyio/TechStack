-- Private cross-tenant projection reads only display fields. Credentials and
-- role activation remain in governed custody; this migration creates NOLOGIN.
SET LOCAL lock_timeout = '5s';
DO $$ BEGIN
 IF NOT EXISTS(SELECT 1 FROM pg_roles WHERE rolname='techstack_platform_projection_system') THEN
  CREATE ROLE techstack_platform_projection_system NOLOGIN NOSUPERUSER NOBYPASSRLS NOCREATEDB NOCREATEROLE NOINHERIT NOREPLICATION;
 END IF;
 IF EXISTS(SELECT 1 FROM pg_roles WHERE rolname='techstack_platform_projection_system' AND (rolsuper OR rolbypassrls OR rolcreatedb OR rolcreaterole OR rolinherit OR rolreplication)) OR EXISTS(SELECT 1 FROM pg_auth_members m JOIN pg_roles r ON r.oid=m.roleid OR r.oid=m.member WHERE r.rolname='techstack_platform_projection_system') THEN
  RAISE EXCEPTION 'platform projection role posture mismatch';
 END IF;
END $$;
INSERT INTO kombify_ctx.role_class(role_name,class) VALUES('techstack_platform_projection_system','system')
 ON CONFLICT (role_name) DO NOTHING;
DO $$ BEGIN
 IF NOT EXISTS(SELECT 1 FROM kombify_ctx.role_class WHERE role_name='techstack_platform_projection_system' AND class='system') THEN
  RAISE EXCEPTION 'platform projection role class mismatch';
 END IF;
END $$;
GRANT USAGE ON SCHEMA public,kombify_ctx TO techstack_platform_projection_system;
GRANT SELECT ON kombify_ctx.role_class,public.schema_migrations TO techstack_platform_projection_system;
GRANT EXECUTE ON FUNCTION pg_catalog.pg_control_system(),kombify_ctx.verified_context(),kombify_ctx.subject(),kombify_ctx.org_id() TO techstack_platform_projection_system;
GRANT SELECT(id,tenant_id,stack_id,owner_subject_id,name,lifecycle_state,connection_state,health_state,revision,metadata_json,decommissioned_at,updated_at,node_id) ON public.servers TO techstack_platform_projection_system;
GRANT SELECT(tenant_id,id,stack_id,server_id,service_key,service_instance,name,node_id) ON public.services TO techstack_platform_projection_system;
GRANT SELECT(tenant_id) ON public.server_registry_outbox_prune_tenants TO techstack_platform_projection_system;
CREATE POLICY platform_projection_system_read ON public.servers FOR SELECT TO techstack_platform_projection_system USING(true);
CREATE POLICY platform_projection_system_read ON public.services FOR SELECT TO techstack_platform_projection_system USING(true);
