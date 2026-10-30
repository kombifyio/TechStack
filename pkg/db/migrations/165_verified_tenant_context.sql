-- Common tenantdb schema foundation only. Existing tenant policies and
-- transaction context are unchanged; no issuer key is installed here.
-- Canonical Common source: 01e60c8e28bcf87497259243da0bb0d8d94ea20a
-- SQL blob: 8fb4970771e2cebc771346fabae7b10be55985c6
SET LOCAL lock_timeout = '5s';

-- The locked migration runner and ledger own replay. Never replace an existing
-- namespace when this migration has not already committed in that ledger.
DO $techstack_context_namespace$
BEGIN
    IF pg_catalog.to_regnamespace('kombify_ctx') IS NOT NULL THEN
        RAISE EXCEPTION 'verified context namespace already exists outside migration 165'
            USING ERRCODE = '42501';
    END IF;
END
$techstack_context_namespace$;

-- BEGIN canonical Common ContextSchemaSQL (byte-identical projection)
-- kombify_ctx v1: tenant context bound to a verified identity.
--
-- Apply through the governed migration path as the schema owner (the
-- migration role). Never apply it as, or grant ownership to, a runtime role.
-- The script is idempotent: re-running it replaces the functions in place.
--
-- RLS policies read the tenant context only through kombify_ctx.subject()
-- and kombify_ctx.org_id(). Both return the claims of the signed context
-- token in the transaction-local setting kombify.ctx_token after verifying,
-- inside the database:
--   * the HMAC-SHA256 signature with a key from kombify_ctx.keys, which no
--     runtime role can read;
--   * that the token is unexpired, at most 15 minutes long and not issued in
--     the future;
--   * that session_user (the login role) is one of the token's audiences.
-- A missing, malformed, forged, expired or foreign-audience token yields
-- NULL, so a policy comparing a column to it matches zero rows.
--
-- No setting widens access. Cross-tenant system work uses separate login
-- roles registered in kombify_ctx.role_class and policies targeted
-- TO those roles.

CREATE SCHEMA IF NOT EXISTS kombify_ctx;
REVOKE ALL ON SCHEMA kombify_ctx FROM PUBLIC;
GRANT USAGE ON SCHEMA kombify_ctx TO PUBLIC;

CREATE TABLE IF NOT EXISTS kombify_ctx.keys (
    key_id     text PRIMARY KEY CHECK (key_id ~ '^[A-Za-z0-9_-]{1,64}$'),
    secret     bytea NOT NULL CHECK (length(secret) >= 32),
    not_before timestamptz NOT NULL DEFAULT now(),
    not_after  timestamptz,
    created_at timestamptz NOT NULL DEFAULT now()
);
REVOKE ALL ON TABLE kombify_ctx.keys FROM PUBLIC;

CREATE TABLE IF NOT EXISTS kombify_ctx.role_class (
    role_name  name PRIMARY KEY,
    class      text NOT NULL CHECK (class IN ('runtime', 'system')),
    created_at timestamptz NOT NULL DEFAULT now()
);
REVOKE ALL ON TABLE kombify_ctx.role_class FROM PUBLIC;
GRANT SELECT ON TABLE kombify_ctx.role_class TO PUBLIC;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_type t JOIN pg_namespace n ON n.oid = t.typnamespace
        WHERE n.nspname = 'kombify_ctx' AND t.typname = 'context'
    ) THEN
        CREATE TYPE kombify_ctx.context AS (
            subject    text,
            org_id     text,
            key_id     text,
            expires_at timestamptz
        );
    END IF;
END
$$;

-- HMAC-SHA256 (RFC 2104) on the core sha256() function, so no extension is
-- needed on managed Postgres.
CREATE OR REPLACE FUNCTION kombify_ctx.hmac_sha256(key bytea, msg bytea)
RETURNS bytea
LANGUAGE plpgsql IMMUTABLE STRICT PARALLEL SAFE
SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
    k    bytea := key;
    ipad bytea;
    opad bytea;
    b    int;
BEGIN
    IF length(k) > 64 THEN
        k := sha256(k);
    END IF;
    k := k || decode(repeat('00', 64 - length(k)), 'hex');
    ipad := k;
    opad := k;
    FOR i IN 0..63 LOOP
        b := get_byte(k, i);
        ipad := set_byte(ipad, i, b # 54);
        opad := set_byte(opad, i, b # 92);
    END LOOP;
    RETURN sha256(opad || sha256(ipad || msg));
END
$$;

CREATE OR REPLACE FUNCTION kombify_ctx.b64url_decode(txt text)
RETURNS bytea
LANGUAGE plpgsql IMMUTABLE STRICT PARALLEL SAFE
SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
    s text;
BEGIN
    IF txt !~ '^[A-Za-z0-9_-]*$' OR length(txt) % 4 = 1 THEN
        RETURN NULL;
    END IF;
    s := translate(txt, '-_', '+/');
    RETURN decode(s || repeat('=', (4 - length(s) % 4) % 4), 'base64');
EXCEPTION WHEN others THEN
    RETURN NULL;
END
$$;

-- verified_context returns the verified claims of kombify.ctx_token, or NULL.
-- SECURITY DEFINER so it can read kombify_ctx.keys; it reads session_user,
-- which SECURITY DEFINER does not change, for the audience check.
CREATE OR REPLACE FUNCTION kombify_ctx.verified_context()
RETURNS kombify_ctx.context
LANGUAGE plpgsql STABLE SECURITY DEFINER
SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
    tok     text := current_setting('kombify.ctx_token', true);
    parts   text[];
    key_sec bytea;
    sig     bytea;
    payload jsonb;
    now_ts  timestamptz := statement_timestamp();
    iat_ts  timestamptz;
    exp_ts  timestamptz;
    result  kombify_ctx.context;
BEGIN
    IF tok IS NULL OR tok = '' OR length(tok) > 4096 THEN
        RETURN NULL;
    END IF;
    parts := string_to_array(tok, '.');
    IF array_length(parts, 1) IS DISTINCT FROM 4 OR parts[1] <> 'v1'
       OR parts[2] !~ '^[A-Za-z0-9_-]{1,64}$' THEN
        RETURN NULL;
    END IF;

    SELECT k.secret INTO key_sec
    FROM kombify_ctx.keys k
    WHERE k.key_id = parts[2]
      AND k.not_before <= now_ts
      AND (k.not_after IS NULL OR k.not_after > now_ts);
    IF key_sec IS NULL THEN
        RETURN NULL;
    END IF;

    sig := kombify_ctx.b64url_decode(parts[4]);
    IF sig IS NULL OR sig IS DISTINCT FROM kombify_ctx.hmac_sha256(
        key_sec, convert_to(parts[1] || '.' || parts[2] || '.' || parts[3], 'UTF8')) THEN
        RETURN NULL;
    END IF;

    payload := convert_from(kombify_ctx.b64url_decode(parts[3]), 'UTF8')::jsonb;
    IF jsonb_typeof(payload) IS DISTINCT FROM 'object'
       OR jsonb_typeof(payload -> 'sub') IS DISTINCT FROM 'string'
       OR payload ->> 'sub' = ''
       OR jsonb_typeof(payload -> 'org') IS DISTINCT FROM 'string'
       OR jsonb_typeof(payload -> 'aud') IS DISTINCT FROM 'array'
       OR jsonb_typeof(payload -> 'iat') IS DISTINCT FROM 'number'
       OR jsonb_typeof(payload -> 'exp') IS DISTINCT FROM 'number' THEN
        RETURN NULL;
    END IF;

    IF NOT (payload -> 'aud') @> to_jsonb(ARRAY[session_user::text]) THEN
        RETURN NULL;
    END IF;

    iat_ts := to_timestamp((payload ->> 'iat')::numeric);
    exp_ts := to_timestamp((payload ->> 'exp')::numeric);
    IF exp_ts <= now_ts OR iat_ts > now_ts + interval '60 seconds'
       OR exp_ts <= iat_ts OR exp_ts - iat_ts > interval '15 minutes' THEN
        RETURN NULL;
    END IF;

    result.subject := payload ->> 'sub';
    result.org_id := NULLIF(payload ->> 'org', '');
    result.key_id := parts[2];
    result.expires_at := exp_ts;
    RETURN result;
EXCEPTION WHEN others THEN
    RETURN NULL;
END
$$;

-- subject and org_id are the only context accessors RLS policies use. Write
-- policies as  col = (SELECT kombify_ctx.org_id())  so the verification runs
-- once per statement.
CREATE OR REPLACE FUNCTION kombify_ctx.subject()
RETURNS text
LANGUAGE sql STABLE
SET search_path = pg_catalog, pg_temp
AS $$ SELECT (kombify_ctx.verified_context()).subject $$;

CREATE OR REPLACE FUNCTION kombify_ctx.org_id()
RETURNS text
LANGUAGE sql STABLE
SET search_path = pg_catalog, pg_temp
AS $$ SELECT (kombify_ctx.verified_context()).org_id $$;

REVOKE ALL ON FUNCTION kombify_ctx.hmac_sha256(bytea, bytea) FROM PUBLIC;
REVOKE ALL ON FUNCTION kombify_ctx.b64url_decode(text) FROM PUBLIC;
REVOKE ALL ON FUNCTION kombify_ctx.verified_context() FROM PUBLIC;
REVOKE ALL ON FUNCTION kombify_ctx.subject() FROM PUBLIC;
REVOKE ALL ON FUNCTION kombify_ctx.org_id() FROM PUBLIC;
GRANT EXECUTE ON FUNCTION kombify_ctx.verified_context() TO PUBLIC;
GRANT EXECUTE ON FUNCTION kombify_ctx.subject() TO PUBLIC;
GRANT EXECUTE ON FUNCTION kombify_ctx.org_id() TO PUBLIC;
-- END canonical Common ContextSchemaSQL

-- Close only the new Common surface's PUBLIC grants. The existing capability
-- installer grants the exact runtime principal its read/verifier rights; the
-- key table and provisioning/signing remain solely with the migration owner.
REVOKE ALL ON SCHEMA kombify_ctx FROM PUBLIC;
REVOKE ALL ON TABLE kombify_ctx.role_class FROM PUBLIC;
REVOKE ALL ON FUNCTION kombify_ctx.verified_context() FROM PUBLIC;
REVOKE ALL ON FUNCTION kombify_ctx.subject() FROM PUBLIC;
REVOKE ALL ON FUNCTION kombify_ctx.org_id() FROM PUBLIC;
INSERT INTO kombify_ctx.role_class (role_name, class)
VALUES ('techstack_application', 'runtime');
