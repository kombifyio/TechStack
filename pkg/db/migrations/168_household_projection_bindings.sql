-- Retained dispatch authority for a Cloud owner/member generation. D10 link
-- status remains exclusively in household_identity_refs. No credentials or
-- one-time enrollment links belong in this relation. No stack FK/cascade:
-- deleting inventory or a link must not erase a dispatched target/tombstone.
SET LOCAL lock_timeout = '5s';

-- An OIDC callback may only complete the exact reference it began against.
-- Recreating the same subject/username after removal creates a new identity.
ALTER TABLE household_identity_refs ADD COLUMN reference_id uuid NOT NULL DEFAULT gen_random_uuid();
CREATE UNIQUE INDEX household_identity_ref_incarnation ON household_identity_refs(reference_id);
CREATE FUNCTION household_reference_immutable() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, public AS $$
BEGIN
 IF NEW.reference_id IS DISTINCT FROM OLD.reference_id THEN
  RAISE EXCEPTION 'household reference incarnation is immutable';
 END IF;
 RETURN NEW;
END $$;
REVOKE ALL ON FUNCTION household_reference_immutable() FROM PUBLIC;
CREATE TRIGGER household_reference_immutable BEFORE UPDATE ON household_identity_refs
 FOR EACH ROW EXECUTE FUNCTION household_reference_immutable();

CREATE TABLE household_projection_bindings (
    tenant_id text NOT NULL CHECK (btrim(tenant_id) <> ''),
    owner_subject_id text NOT NULL CHECK (btrim(owner_subject_id) <> ''),
    cloud_subject text NOT NULL CHECK (btrim(cloud_subject) <> ''),
    revision bigint NOT NULL CHECK (revision > 0),
    action text NOT NULL CHECK (action IN ('add', 'remove')),
    effect_digest text NOT NULL CHECK (effect_digest ~ '^[0-9a-f]{64}$'),
    username text NOT NULL CHECK (username ~ '^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$'),
    stack_id text NOT NULL DEFAULT '',
    agent_id text NOT NULL DEFAULT '',
    stackkit_instance_id text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, owner_subject_id, cloud_subject),
    CHECK (owner_subject_id <> cloud_subject),
    CHECK ((agent_id = '') = (stackkit_instance_id = '')),
    CHECK (stack_id <> '' OR agent_id = '')
);
-- A retained removal must never later address a different person's reused
-- username. Binding adoption conflicts require explicit reconciliation.
CREATE UNIQUE INDEX household_projection_bound_username ON household_projection_bindings
    (tenant_id, stack_id, username) WHERE stack_id <> '';

ALTER TABLE household_projection_bindings ENABLE ROW LEVEL SECURITY;
ALTER TABLE household_projection_bindings FORCE ROW LEVEL SECURITY;
-- The canonical M01 verifier is the only authority, including for system
-- projections. Missing owner context fails closed; app.* and runtime roles
-- cannot substitute for a verified owner subject and organization.
CREATE POLICY household_projection_verified_owner ON household_projection_bindings
    USING (tenant_id = (SELECT kombify_ctx.org_id())
        AND owner_subject_id = (SELECT kombify_ctx.subject()))
    WITH CHECK (tenant_id = (SELECT kombify_ctx.org_id())
        AND owner_subject_id = (SELECT kombify_ctx.subject()));

CREATE FUNCTION household_projection_binding_monotonic() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, public AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'household projection bindings and tombstones are retained';
    END IF;
    IF ROW(NEW.tenant_id, NEW.owner_subject_id, NEW.cloud_subject, NEW.username, NEW.created_at)
       IS DISTINCT FROM ROW(OLD.tenant_id, OLD.owner_subject_id, OLD.cloud_subject, OLD.username, OLD.created_at)
       OR (OLD.stack_id <> '' AND NEW.stack_id IS DISTINCT FROM OLD.stack_id)
       OR (OLD.agent_id <> '' AND ROW(NEW.agent_id, NEW.stackkit_instance_id)
           IS DISTINCT FROM ROW(OLD.agent_id, OLD.stackkit_instance_id)) THEN
        RAISE EXCEPTION 'household projection target and person are immutable';
    END IF;
    IF NEW.revision < OLD.revision OR
       (NEW.revision = OLD.revision AND ROW(NEW.action, NEW.effect_digest)
           IS DISTINCT FROM ROW(OLD.action, OLD.effect_digest)) THEN
        RAISE EXCEPTION 'household projection generation cannot regress or conflict';
    END IF;
    IF OLD.stack_id = '' AND NEW.stack_id <> '' AND NEW.action <> 'add' THEN
        RAISE EXCEPTION 'a targetless removal cannot select a new node';
    END IF;
    RETURN NEW;
END $$;
REVOKE ALL ON FUNCTION household_projection_binding_monotonic() FROM PUBLIC;
CREATE TRIGGER household_projection_binding_monotonic
    BEFORE UPDATE OR DELETE ON household_projection_bindings
    FOR EACH ROW EXECUTE FUNCTION household_projection_binding_monotonic();

-- A Cloud owner/member pair cannot silently migrate organizations on retry.
CREATE UNIQUE INDEX household_projection_pair_tenant ON household_projection_bindings(owner_subject_id,cloud_subject);
DO $$ BEGIN
 IF NOT EXISTS(SELECT 1 FROM pg_roles WHERE rolname='techstack_household_worker') THEN
  CREATE ROLE techstack_household_worker NOLOGIN NOSUPERUSER NOBYPASSRLS NOCREATEDB NOCREATEROLE NOINHERIT NOREPLICATION;
 END IF;
 IF EXISTS(SELECT 1 FROM pg_roles WHERE rolname='techstack_household_worker' AND (rolsuper OR rolbypassrls OR rolcreatedb OR rolcreaterole OR rolinherit OR rolreplication)) OR EXISTS(SELECT 1 FROM pg_auth_members m JOIN pg_roles r ON r.oid=m.roleid OR r.oid=m.member WHERE r.rolname='techstack_household_worker') THEN RAISE EXCEPTION 'household worker role posture mismatch';END IF;
END $$;
INSERT INTO kombify_ctx.role_class(role_name,class) VALUES('techstack_household_worker','system');
GRANT USAGE ON SCHEMA public,kombify_ctx TO techstack_household_worker;
GRANT SELECT ON kombify_ctx.role_class TO techstack_household_worker;
GRANT EXECUTE ON FUNCTION pg_catalog.pg_control_system() TO techstack_household_worker;
GRANT EXECUTE ON FUNCTION kombify_ctx.verified_context(),kombify_ctx.subject(),kombify_ctx.org_id() TO techstack_household_worker;
GRANT SELECT ON schema_migrations,techstack_memberships,techstack_tenants,homelabs,stacks,workers,servers TO techstack_household_worker;
GRANT SELECT,INSERT,UPDATE ON household_projection_bindings TO techstack_household_worker;
GRANT SELECT,INSERT,UPDATE,DELETE ON household_identity_refs,typed_agent_commands TO techstack_household_worker;
CREATE POLICY household_system_binding ON household_projection_bindings TO techstack_household_worker USING(true) WITH CHECK(true);
CREATE POLICY household_system_refs ON household_identity_refs TO techstack_household_worker USING(true) WITH CHECK(true);
CREATE POLICY household_system_commands ON typed_agent_commands TO techstack_household_worker
 USING(command_family='stackkit' AND command_json->>'operation'='STACKKIT_OPERATION_HOUSEHOLD_PROJECT')
 WITH CHECK(command_family='stackkit' AND command_json->>'operation'='STACKKIT_OPERATION_HOUSEHOLD_PROJECT');
-- Existing permissive tenant policies must not widen the worker's opcode scope.
CREATE POLICY household_worker_command_ceiling ON typed_agent_commands AS RESTRICTIVE TO PUBLIC
 USING(session_user<>'techstack_household_worker' OR (command_family='stackkit' AND command_json->>'operation'='STACKKIT_OPERATION_HOUSEHOLD_PROJECT'))
 WITH CHECK(session_user<>'techstack_household_worker' OR (command_family='stackkit' AND command_json->>'operation'='STACKKIT_OPERATION_HOUSEHOLD_PROJECT'));
DO $$ DECLARE t text; BEGIN
 FOREACH t IN ARRAY ARRAY['techstack_memberships','techstack_tenants','homelabs','stacks','workers','servers'] LOOP
  EXECUTE format('CREATE POLICY household_system_read ON %I FOR SELECT TO techstack_household_worker USING(true)',t);
 END LOOP;
END $$;
-- Only command intent is retained: no result, setup URL or human token.
CREATE TABLE household_projection_attempts(
 tenant_id text NOT NULL,projection_key text NOT NULL CHECK(projection_key~'^[0-9a-f]{64}$'),
 projection_revision bigint NOT NULL CHECK(projection_revision>0),command_id text NOT NULL UNIQUE,
 agent_id text NOT NULL,stackkit_instance_id text NOT NULL,command_json jsonb NOT NULL,
 PRIMARY KEY(tenant_id,projection_key));
ALTER TABLE household_projection_attempts ENABLE ROW LEVEL SECURITY;
ALTER TABLE household_projection_attempts FORCE ROW LEVEL SECURITY;
CREATE POLICY household_system_attempt ON household_projection_attempts TO techstack_household_worker USING(true) WITH CHECK(true);
GRANT SELECT,INSERT,UPDATE ON household_projection_attempts TO techstack_household_worker;
