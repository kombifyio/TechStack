-- Optional Local adapter. No installation, credential, key or authority row is
-- created here. A consumer installs its verifier through its migration owner.
DO $$ BEGIN
  CREATE TYPE kombify_ctx.local_input AS (credential_class text, credential text);
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;
DO $$ BEGIN
  CREATE TYPE kombify_ctx.local_context AS (
    subject text, org_id text, credential_class text, client_id text,
    scopes text[], modules jsonb, resources jsonb, purpose text,
    generation bigint, expires_at timestamptz
  );
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;

-- Credential-proof RLS can use this parser without recursing through identity
-- verification. It reads only the presenting transaction's own credential.
CREATE OR REPLACE FUNCTION kombify_ctx.local_input()
RETURNS kombify_ctx.local_input
LANGUAGE plpgsql STABLE
SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
  token text := current_setting('kombify.ctx_token', true);
  parts text[];
  raw bytea;
  result kombify_ctx.local_input;
BEGIN
  IF token IS NULL OR length(token) > 4096 THEN RETURN NULL; END IF;
  parts := string_to_array(token, '.');
  IF array_length(parts, 1) IS DISTINCT FROM 3 OR parts[1] <> 'local1'
     OR parts[2] NOT IN ('owner','readonly','mcp','background') THEN RETURN NULL; END IF;
  -- Decode here rather than granting the signed-token decoder to runtimes.
  IF parts[3] !~ '^[A-Za-z0-9_-]+$' OR length(parts[3]) % 4 = 1 THEN RETURN NULL; END IF;
  raw := decode(translate(parts[3], '-_', '+/') || repeat('=', (4-length(parts[3])%4)%4), 'base64');
  IF translate(rtrim(replace(encode(raw, 'base64'), E'\n', ''), '='), '+/', '-_') <> parts[3]
     OR octet_length(raw) < 16 OR octet_length(raw) > 2048 THEN RETURN NULL; END IF;
  result.credential := convert_from(raw, 'UTF8');
  IF result.credential !~ '^[A-Za-z0-9_-]+$' THEN RETURN NULL; END IF;
  result.credential_class := parts[2];
  RETURN result;
EXCEPTION WHEN others THEN RETURN NULL;
END $$;

-- The sole consumer seam. Its body is not supplied by Common: installing a
-- default owner or a fallback secret here would turn hosted databases Local.
-- The consumer hook MUST check its initializer-owned Local installation,
-- physical database, session_user, credential hash, generation and revocation,
-- and derive identity and finite capabilities from its protected persisted rows.
CREATE OR REPLACE FUNCTION kombify_ctx.verified_local_context()
RETURNS kombify_ctx.local_context
LANGUAGE plpgsql STABLE SECURITY DEFINER
SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
  input kombify_ctx.local_input;
  result kombify_ctx.local_context;
  verifier oid := to_regprocedure('kombify_ctx.verify_local_credential(text,text)');
BEGIN
  input := kombify_ctx.local_input();
  IF input.credential IS NULL OR verifier IS NULL THEN RETURN NULL; END IF;
  IF NOT EXISTS (
    SELECT 1 FROM pg_proc p
    WHERE p.oid=verifier AND p.prokind='f' AND p.prosecdef AND p.provolatile='s'
      AND NOT p.proretset AND p.pronargs=2 AND p.pronargdefaults=0 AND p.provariadic=0
      AND p.prorettype='kombify_ctx.local_context'::regtype
      AND p.proowner=(SELECT proowner FROM pg_proc WHERE oid='kombify_ctx.verified_context()'::regprocedure)
      AND p.proowner=current_user::regrole
      AND p.proconfig=ARRAY['search_path=pg_catalog, pg_temp']::text[]
      AND NOT EXISTS (SELECT 1 FROM aclexplode(coalesce(p.proacl,acldefault('f',p.proowner))) a
        WHERE a.grantee=0 AND a.privilege_type='EXECUTE')
  ) THEN RETURN NULL; END IF;
  -- Fixed qualified function and bound arguments; no caller-selected SQL.
  EXECUTE 'SELECT * FROM kombify_ctx.verify_local_credential($1,$2)'
    INTO result USING input.credential_class, input.credential;
  IF result.subject IS NULL OR length(result.subject) NOT BETWEEN 1 AND 256
     OR result.org_id IS NULL OR length(result.org_id)>256
     OR result.credential_class IS DISTINCT FROM input.credential_class
     OR result.client_id IS NULL OR length(result.client_id) NOT BETWEEN 1 AND 256
     OR result.purpose IS NULL OR length(result.purpose) NOT BETWEEN 1 AND 128
     OR result.generation IS NULL OR result.generation<1
     OR result.expires_at IS NULL OR result.expires_at<=statement_timestamp()
     OR result.expires_at>statement_timestamp()+interval '15 minutes'
     OR result.scopes IS NULL OR cardinality(result.scopes) NOT BETWEEN 1 AND 128
     OR array_ndims(result.scopes) IS DISTINCT FROM 1
     OR EXISTS(SELECT 1 FROM unnest(result.scopes) s WHERE s IS NULL OR s !~ '^[A-Za-z][A-Za-z0-9:_./-]{0,127}$')
     OR (SELECT count(*)<>count(DISTINCT s) FROM unnest(result.scopes) s)
     OR jsonb_typeof(result.modules) IS DISTINCT FROM 'object'
     OR jsonb_typeof(result.resources) IS DISTINCT FROM 'object'
     OR octet_length(result.modules::text)>4096 OR octet_length(result.resources::text)>4096
  THEN RETURN NULL; END IF;
  RETURN result;
EXCEPTION WHEN others THEN RETURN NULL;
END $$;

-- Metadata only: subject/org remain exclusively behind the existing authority.
-- A signed-v1 token has no Local claims and obtains no implicit Local capability.
CREATE OR REPLACE FUNCTION kombify_ctx.local_claims()
RETURNS jsonb
LANGUAGE sql STABLE SECURITY DEFINER
SET search_path = pg_catalog, pg_temp
AS $$
  SELECT CASE WHEN v.subject IS NULL THEN NULL ELSE
    jsonb_build_object('credential_class',v.credential_class,'client_id',v.client_id,
      'scopes',v.scopes,'modules',v.modules,'resources',v.resources,'purpose',v.purpose,
      'generation',v.generation,'expires_at',v.expires_at) END
  FROM kombify_ctx.verified_local_context() v
$$;

CREATE OR REPLACE FUNCTION kombify_ctx.local_has_scope(required_scope text)
RETURNS boolean
LANGUAGE sql STABLE
SET search_path = pg_catalog, pg_temp
AS $$ SELECT coalesce((kombify_ctx.local_claims()->'scopes') ? required_scope, false) $$;

REVOKE ALL ON FUNCTION kombify_ctx.local_input() FROM PUBLIC;
REVOKE ALL ON FUNCTION kombify_ctx.verified_local_context() FROM PUBLIC;
REVOKE ALL ON FUNCTION kombify_ctx.local_claims() FROM PUBLIC;
REVOKE ALL ON FUNCTION kombify_ctx.local_has_scope(text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION kombify_ctx.local_claims() TO PUBLIC;
GRANT EXECUTE ON FUNCTION kombify_ctx.local_has_scope(text) TO PUBLIC;
