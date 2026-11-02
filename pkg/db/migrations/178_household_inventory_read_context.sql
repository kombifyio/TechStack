-- kombify:contract objects=public.servers,public.stacks,public.services
-- Household inventory uses the existing edge signer and live FGA decisions.
-- The member remains the SQL actor. Only SELECT may use the signed owner and
-- collection/exact-server scopes; no runtime setting supplies that authority.
-- Additive: without a verified context (internal service calls, jobs) every
-- policy below is a no-op and the existing GUC tenant policy decides as before.
-- With a verified context, rows outside the actor's tenant are visible only
-- through the signed household read scopes, and DML stays own-tenant. Closing
-- GUC-only access needs its own inventory of token-less callers (follow-up).
SET LOCAL lock_timeout = '5s';

-- Canonical Common scope accessor, unchanged from tenantdb.ContextScopeSchemaSQL.
-- Optional capabilities over the existing verified identity authority. This
-- creates no issuer, key, installation, role or credential registration right.
CREATE OR REPLACE FUNCTION kombify_ctx.scopes()
RETURNS text[]
LANGUAGE plpgsql STABLE SECURITY DEFINER
SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
  token text := current_setting('kombify.ctx_token', true);
  claims jsonb;
  result text[];
BEGIN
  -- Never parse an unsigned GUC as authority. This verifies the current token's
  -- MAC, audience and lifetime, or the installed Local credential hook.
  IF (kombify_ctx.verified_context()).subject IS NULL THEN RETURN ARRAY[]::text[]; END IF;
  IF token LIKE 'local1.%' THEN
    IF to_regprocedure('kombify_ctx.local_claims()') IS NULL THEN RETURN ARRAY[]::text[]; END IF;
    EXECUTE 'SELECT kombify_ctx.local_claims()->''scopes''' INTO claims;
  ELSE
    IF split_part(token, '.', 1) <> 'v1' THEN RETURN ARRAY[]::text[]; END IF;
    claims := convert_from(kombify_ctx.b64url_decode(split_part(token, '.', 3)), 'UTF8')::jsonb->'scp';
  END IF;
  IF jsonb_typeof(claims) IS DISTINCT FROM 'array' THEN RETURN ARRAY[]::text[]; END IF;
  IF jsonb_array_length(claims)>128 OR EXISTS (
    SELECT 1 FROM jsonb_array_elements(claims) value
    WHERE jsonb_typeof(value) IS DISTINCT FROM 'string'
      OR (value #>> '{}') !~ '^[A-Za-z][A-Za-z0-9:_./-]{0,127}$'
  ) THEN RETURN ARRAY[]::text[]; END IF;
  SELECT coalesce(array_agg(value ORDER BY ordinal), ARRAY[]::text[])
    INTO result FROM jsonb_array_elements_text(claims) WITH ORDINALITY item(value,ordinal);
  IF cardinality(result)<>(SELECT count(DISTINCT value) FROM unnest(result) value)
    THEN RETURN ARRAY[]::text[]; END IF;
  RETURN result;
EXCEPTION WHEN others THEN RETURN ARRAY[]::text[];
END $$;

CREATE OR REPLACE FUNCTION kombify_ctx.has_scope(required_scope text)
RETURNS boolean
LANGUAGE sql STABLE
SET search_path = pg_catalog, pg_temp
AS $$ SELECT coalesce(required_scope = ANY(kombify_ctx.scopes()), false) $$;

REVOKE ALL ON FUNCTION kombify_ctx.scopes() FROM PUBLIC;
REVOKE ALL ON FUNCTION kombify_ctx.has_scope(text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION kombify_ctx.scopes() TO PUBLIC;
GRANT EXECUTE ON FUNCTION kombify_ctx.has_scope(text) TO PUBLIC;

REVOKE ALL ON FUNCTION kombify_ctx.scopes(),kombify_ctx.has_scope(text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION kombify_ctx.scopes(),kombify_ctx.has_scope(text) TO techstack_application,techstack_backup_worker;

CREATE FUNCTION public.techstack_household_inventory_owner(p_tenant text,p_owner text)
RETURNS boolean LANGUAGE sql STABLE SECURITY DEFINER
SET search_path=pg_catalog,public,pg_temp AS $$
 SELECT coalesce(
   p_owner <> '' AND p_owner <> kombify_ctx.subject()
   AND p_tenant = 'usr:' || p_owner
   AND kombify_ctx.org_id() = 'usr:' || kombify_ctx.subject()
   AND kombify_ctx.has_scope('techstack.household.owner.' || encode(sha256(convert_to(p_owner,'UTF8')),'hex'))
   AND (kombify_ctx.has_scope('techstack.household.collection') OR EXISTS (
     SELECT 1 FROM unnest(kombify_ctx.scopes()) scope
     WHERE scope ~ '^techstack[.]household[.]server[.][0-9a-f]{64}$'
   )), false)
$$;

CREATE FUNCTION public.techstack_household_inventory_server(p_tenant text,p_owner text,p_server text)
RETURNS boolean LANGUAGE sql STABLE SECURITY DEFINER
SET search_path=pg_catalog,public,pg_temp AS $$
 SELECT public.techstack_household_inventory_owner(p_tenant,p_owner)
   AND (kombify_ctx.has_scope('techstack.household.collection')
        OR kombify_ctx.has_scope('techstack.household.server.' || encode(sha256(convert_to(p_server,'UTF8')),'hex')))
$$;
REVOKE ALL ON FUNCTION public.techstack_household_inventory_owner(text,text),public.techstack_household_inventory_server(text,text,text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION public.techstack_household_inventory_owner(text,text),public.techstack_household_inventory_server(text,text,text) TO techstack_application,techstack_backup_worker;

CREATE POLICY household_inventory_read ON public.servers AS RESTRICTIVE FOR SELECT TO techstack_application
 USING ((SELECT (kombify_ctx.verified_context()).subject) IS NULL OR tenant_id = (SELECT kombify_ctx.org_id())
   OR public.techstack_household_inventory_server(tenant_id,owner_subject_id,id));
CREATE POLICY household_inventory_read ON public.stacks AS RESTRICTIVE FOR SELECT TO techstack_application
 USING ((SELECT (kombify_ctx.verified_context()).subject) IS NULL OR tenant_id = (SELECT kombify_ctx.org_id()) OR (
   public.techstack_household_inventory_owner(tenant_id,owner_subject_id)
   AND (kombify_ctx.has_scope('techstack.household.collection') OR EXISTS (
     SELECT 1 FROM public.servers authorized_server
     WHERE authorized_server.tenant_id=stacks.tenant_id
       AND authorized_server.owner_subject_id=stacks.owner_subject_id
       AND authorized_server.stack_id=stacks.id
       AND public.techstack_household_inventory_server(authorized_server.tenant_id,authorized_server.owner_subject_id,authorized_server.id)
   ))));
CREATE POLICY household_inventory_read ON public.services AS RESTRICTIVE FOR SELECT TO techstack_application
 USING ((SELECT (kombify_ctx.verified_context()).subject) IS NULL OR tenant_id = (SELECT kombify_ctx.org_id()) OR EXISTS (
   SELECT 1 FROM public.servers authorized_server
   WHERE authorized_server.tenant_id=services.tenant_id
     AND authorized_server.id=services.server_id
     AND authorized_server.stack_id=services.stack_id
     AND public.techstack_household_inventory_server(authorized_server.tenant_id,authorized_server.owner_subject_id,authorized_server.id)
 ));

-- Even a valid delegated read token cannot authorize DML via a chosen GUC.
CREATE POLICY household_inventory_insert ON public.servers AS RESTRICTIVE FOR INSERT TO techstack_application
 WITH CHECK ((SELECT (kombify_ctx.verified_context()).subject) IS NULL OR tenant_id = (SELECT kombify_ctx.org_id()));
CREATE POLICY household_inventory_update ON public.servers AS RESTRICTIVE FOR UPDATE TO techstack_application
 USING ((SELECT (kombify_ctx.verified_context()).subject) IS NULL OR tenant_id = (SELECT kombify_ctx.org_id())) WITH CHECK ((SELECT (kombify_ctx.verified_context()).subject) IS NULL OR tenant_id = (SELECT kombify_ctx.org_id()));
CREATE POLICY household_inventory_delete ON public.servers AS RESTRICTIVE FOR DELETE TO techstack_application
 USING ((SELECT (kombify_ctx.verified_context()).subject) IS NULL OR tenant_id = (SELECT kombify_ctx.org_id()));
CREATE POLICY household_inventory_insert ON public.stacks AS RESTRICTIVE FOR INSERT TO techstack_application
 WITH CHECK ((SELECT (kombify_ctx.verified_context()).subject) IS NULL OR tenant_id = (SELECT kombify_ctx.org_id()));
CREATE POLICY household_inventory_update ON public.stacks AS RESTRICTIVE FOR UPDATE TO techstack_application
 USING ((SELECT (kombify_ctx.verified_context()).subject) IS NULL OR tenant_id = (SELECT kombify_ctx.org_id())) WITH CHECK ((SELECT (kombify_ctx.verified_context()).subject) IS NULL OR tenant_id = (SELECT kombify_ctx.org_id()));
CREATE POLICY household_inventory_delete ON public.stacks AS RESTRICTIVE FOR DELETE TO techstack_application
 USING ((SELECT (kombify_ctx.verified_context()).subject) IS NULL OR tenant_id = (SELECT kombify_ctx.org_id()));
CREATE POLICY household_inventory_insert ON public.services AS RESTRICTIVE FOR INSERT TO techstack_application
 WITH CHECK ((SELECT (kombify_ctx.verified_context()).subject) IS NULL OR tenant_id = (SELECT kombify_ctx.org_id()));
CREATE POLICY household_inventory_update ON public.services AS RESTRICTIVE FOR UPDATE TO techstack_application
 USING ((SELECT (kombify_ctx.verified_context()).subject) IS NULL OR tenant_id = (SELECT kombify_ctx.org_id())) WITH CHECK ((SELECT (kombify_ctx.verified_context()).subject) IS NULL OR tenant_id = (SELECT kombify_ctx.org_id()));
CREATE POLICY household_inventory_delete ON public.services AS RESTRICTIVE FOR DELETE TO techstack_application
 USING ((SELECT (kombify_ctx.verified_context()).subject) IS NULL OR tenant_id = (SELECT kombify_ctx.org_id()));
