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
