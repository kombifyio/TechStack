-- Authoritative provider catalog and tenant-scoped credential custody handles.
--
-- Catalog versions and profiles are global product configuration. They contain
-- only stable identities and public capability data. Credential handles are
-- tenant-owned lookup records; the actual provider credential is held behind
-- the opaque custody/connection references and never enters this schema.

SELECT pg_catalog.set_config(
    'search_path',
    pg_catalog.quote_ident(pg_catalog.current_schema()) || ', pg_catalog, pg_temp',
    true
);

CREATE TABLE IF NOT EXISTS provider_catalog_versions (
    catalog_version text PRIMARY KEY,
    status text NOT NULL DEFAULT 'draft',
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    activated_at timestamptz,
    retired_at timestamptz,
    CHECK (catalog_version ~ '^[a-z0-9][a-z0-9._-]{0,127}$'),
    CHECK (status IN ('draft', 'active', 'retired')),
    CHECK (
        (status = 'draft' AND activated_at IS NULL AND retired_at IS NULL)
        OR (status = 'active' AND activated_at IS NOT NULL AND retired_at IS NULL)
        OR (status = 'retired' AND retired_at IS NOT NULL)
    ),
    CHECK (activated_at IS NULL OR activated_at >= created_at),
    CHECK (retired_at IS NULL OR retired_at >= COALESCE(activated_at, created_at))
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_provider_catalog_one_active_version
    ON provider_catalog_versions ((status))
    WHERE status = 'active';

-- Capability snapshots are deliberately narrow. Provider-specific extension
-- blobs would make the catalog a second adapter API and could smuggle secrets
-- into durable operation hashes. New capability fields require a schema
-- migration and a corresponding resolver-contract change.
CREATE OR REPLACE FUNCTION provider_catalog_capability_json_within_limits(snapshot jsonb)
RETURNS boolean
LANGUAGE sql
IMMUTABLE
STRICT
PARALLEL SAFE
SET search_path FROM CURRENT
AS $$
    WITH RECURSIVE nodes(node_value, depth) AS (
        SELECT snapshot, 1
        UNION ALL
        SELECT child.node_value, nodes.depth + 1
        FROM nodes
        CROSS JOIN LATERAL (
            SELECT array_item AS node_value
            FROM jsonb_array_elements(
                CASE
                    WHEN jsonb_typeof(nodes.node_value) = 'array' THEN nodes.node_value
                    ELSE '[]'::jsonb
                END
            ) AS array_items(array_item)
            UNION ALL
            SELECT object_item AS node_value
            FROM jsonb_each(
                CASE
                    WHEN jsonb_typeof(nodes.node_value) = 'object' THEN nodes.node_value
                    ELSE '{}'::jsonb
                END
            ) AS object_items(object_key, object_item)
        ) AS child
    )
    SELECT octet_length(snapshot::text) <= 16384
       AND (SELECT count(*) FROM nodes) <= 256
       AND (SELECT max(depth) FROM nodes) <= 4
       AND NOT EXISTS (
            SELECT 1
            FROM nodes
            WHERE jsonb_typeof(node_value) = 'null'
               OR (
                    jsonb_typeof(node_value) = 'string'
                    AND length(node_value #>> '{}') NOT BETWEEN 1 AND 128
               )
       );
$$;

CREATE OR REPLACE FUNCTION provider_catalog_capability_string_array_is_valid(value jsonb)
RETURNS boolean
LANGUAGE sql
IMMUTABLE
STRICT
PARALLEL SAFE
SET search_path FROM CURRENT
AS $$
    SELECT CASE
        WHEN jsonb_typeof(value) <> 'array' THEN false
        WHEN jsonb_array_length(value) > 32 THEN false
        ELSE NOT EXISTS (
            SELECT 1
            FROM jsonb_array_elements(value) AS entries(item)
            WHERE jsonb_typeof(item) <> 'string'
               OR (item #>> '{}') !~ '^[a-z0-9][a-z0-9._-]{0,127}$'
        )
        AND (
            SELECT count(DISTINCT item #>> '{}') = jsonb_array_length(value)
            FROM jsonb_array_elements(value) AS entries(item)
        )
    END;
$$;

CREATE OR REPLACE FUNCTION provider_catalog_capability_snapshot_is_valid(snapshot jsonb)
RETURNS boolean
LANGUAGE sql
IMMUTABLE
STRICT
PARALLEL SAFE
SET search_path FROM CURRENT
AS $$
    SELECT CASE
        WHEN jsonb_typeof(snapshot) <> 'object' THEN false
        WHEN snapshot - ARRAY[
            'regions', 'zones', 'instance_types', 'architectures', 'network', 'storage'
        ]::text[] <> '{}'::jsonb THEN false
        ELSE
            CASE
                WHEN snapshot ? 'regions'
                    THEN provider_catalog_capability_string_array_is_valid(snapshot -> 'regions')
                ELSE true
            END
        AND CASE
                WHEN snapshot ? 'zones'
                    THEN provider_catalog_capability_string_array_is_valid(snapshot -> 'zones')
                ELSE true
            END
        AND CASE
                WHEN snapshot ? 'instance_types'
                    THEN provider_catalog_capability_string_array_is_valid(snapshot -> 'instance_types')
                ELSE true
            END
        AND CASE
                WHEN snapshot ? 'architectures'
                    THEN provider_catalog_capability_string_array_is_valid(snapshot -> 'architectures')
                ELSE true
            END
        AND CASE
                WHEN NOT (snapshot ? 'network') THEN true
                WHEN jsonb_typeof(snapshot -> 'network') <> 'object' THEN false
                ELSE
                    (snapshot -> 'network') - ARRAY[
                        'ipv4', 'ipv6', 'private_network', 'floating_ip'
                    ]::text[] = '{}'::jsonb
                    AND NOT EXISTS (
                        SELECT 1
                        FROM jsonb_each(snapshot -> 'network') AS fields(field_name, field_value)
                        WHERE jsonb_typeof(field_value) <> 'boolean'
                    )
            END
        AND CASE
                WHEN NOT (snapshot ? 'storage') THEN true
                WHEN jsonb_typeof(snapshot -> 'storage') <> 'object' THEN false
                ELSE
                    (snapshot -> 'storage') - ARRAY[
                        'volume_types', 'snapshots', 'online_resize'
                    ]::text[] = '{}'::jsonb
                    AND CASE
                            WHEN (snapshot -> 'storage') ? 'volume_types'
                                THEN provider_catalog_capability_string_array_is_valid(
                                    snapshot -> 'storage' -> 'volume_types'
                                )
                            ELSE true
                        END
                    AND NOT EXISTS (
                        SELECT 1
                        FROM jsonb_each(snapshot -> 'storage') AS fields(field_name, field_value)
                        WHERE field_name IN ('snapshots', 'online_resize')
                          AND jsonb_typeof(field_value) <> 'boolean'
                    )
            END
    END;
$$;

CREATE TABLE IF NOT EXISTS provider_catalog_profiles (
    catalog_version text NOT NULL,
    provider_id text NOT NULL,
    adapter_id text NOT NULL,
    credential_mode text NOT NULL,
    runtime_profile_id text NOT NULL,
    offering_id text NOT NULL,
    can_pause boolean NOT NULL,
    stop_effect text NOT NULL,
    can_recreate boolean NOT NULL,
    capability_snapshot jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (catalog_version, provider_id, runtime_profile_id, offering_id),
    FOREIGN KEY (catalog_version)
        REFERENCES provider_catalog_versions (catalog_version) ON DELETE RESTRICT,
    CHECK (provider_id ~ '^[a-z0-9][a-z0-9._-]{0,127}$'),
    CHECK (provider_id NOT IN ('centron-managed', 'ionos-managed')),
    CHECK (adapter_id ~ '^[a-z0-9][a-z0-9._-]{0,127}$'),
    CHECK (credential_mode IN ('managed', 'byok')),
    CHECK (runtime_profile_id ~ '^[a-z0-9][a-z0-9._-]{0,127}$'),
    CHECK (offering_id ~ '^[a-z0-9][a-z0-9._-]{0,127}$'),
    CHECK (stop_effect IN ('pause', 'destroy')),
    CHECK (stop_effect <> 'pause' OR can_pause),
    CHECK (provider_catalog_capability_json_within_limits(capability_snapshot)),
    CHECK (provider_catalog_capability_snapshot_is_valid(capability_snapshot))
);

CREATE INDEX IF NOT EXISTS idx_provider_catalog_profiles_adapter
    ON provider_catalog_profiles (catalog_version, adapter_id);

CREATE TABLE IF NOT EXISTS provider_credential_handles (
    tenant_id text NOT NULL,
    handle_id text NOT NULL,
    handle_version bigint NOT NULL,
    provider_id text NOT NULL,
    credential_mode text NOT NULL,
    subject_kind text NOT NULL,
    subject_id text NOT NULL,
    grant_id text NOT NULL,
    credential_scope text NOT NULL,
    custody_ref text NOT NULL,
    connection_ref text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    revoked_at timestamptz,
    PRIMARY KEY (tenant_id, handle_id, handle_version),
    UNIQUE (tenant_id, custody_ref),
    UNIQUE (tenant_id, connection_ref),
    FOREIGN KEY (tenant_id) REFERENCES techstack_tenants (id) ON DELETE RESTRICT,
    CHECK (handle_id <> '' AND length(handle_id) <= 256),
    CHECK (handle_version > 0),
    CHECK (provider_id ~ '^[a-z0-9][a-z0-9._-]{0,127}$'),
    CHECK (provider_id NOT IN ('centron-managed', 'ionos-managed')),
    CHECK (credential_mode IN ('managed', 'byok')),
    CHECK (subject_kind IN ('user', 'org')),
    CHECK (subject_id <> '' AND length(subject_id) <= 512),
    CHECK (grant_id <> '' AND length(grant_id) <= 512),
    CHECK (credential_scope <> '' AND length(credential_scope) <= 512),
    CHECK (
        octet_length(custody_ref) BETWEEN 11 AND 512
        AND custody_ref ~ '^custody://[A-Za-z0-9][A-Za-z0-9._~/-]*$'
    ),
    CHECK (
        octet_length(connection_ref) BETWEEN 23 AND 512
        AND connection_ref ~ '^provider-connection://[A-Za-z0-9][A-Za-z0-9._~/-]*$'
    ),
    CHECK (revoked_at IS NULL OR revoked_at >= created_at)
);

CREATE INDEX IF NOT EXISTS idx_provider_credential_handles_subject
    ON provider_credential_handles (
        tenant_id, provider_id, credential_mode, subject_kind, subject_id
    )
    WHERE revoked_at IS NULL;

CREATE OR REPLACE FUNCTION provider_catalog_version_transition_guard()
RETURNS trigger
LANGUAGE plpgsql
SET search_path FROM CURRENT
AS $$
BEGIN
    IF to_jsonb(NEW) IS NOT DISTINCT FROM to_jsonb(OLD) THEN
        RETURN NEW;
    END IF;
    IF NEW.catalog_version IS DISTINCT FROM OLD.catalog_version
       OR NEW.created_at IS DISTINCT FROM OLD.created_at THEN
        RAISE EXCEPTION 'provider catalog version identity is immutable'
            USING ERRCODE = '55000';
    END IF;
    IF OLD.activated_at IS NOT NULL
       AND NEW.activated_at IS DISTINCT FROM OLD.activated_at THEN
        RAISE EXCEPTION 'provider catalog activation time is immutable'
            USING ERRCODE = '55000';
    END IF;
    IF OLD.status = 'draft' AND NEW.status = 'retired' AND NEW.activated_at IS NOT NULL THEN
        RAISE EXCEPTION 'a never-active catalog version cannot acquire an activation time'
            USING ERRCODE = '55000';
    END IF;
    IF NOT (
        (OLD.status = 'draft' AND NEW.status IN ('active', 'retired'))
        OR (OLD.status = 'active' AND NEW.status = 'retired')
    ) THEN
        RAISE EXCEPTION 'invalid provider catalog version transition % -> %', OLD.status, NEW.status
            USING ERRCODE = '55000';
    END IF;
    RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION provider_catalog_profile_insert_guard()
RETURNS trigger
LANGUAGE plpgsql
SET search_path FROM CURRENT
AS $$
DECLARE
    version_status text;
BEGIN
    SELECT status INTO version_status
    FROM provider_catalog_versions
    WHERE catalog_version = NEW.catalog_version
    FOR UPDATE;
    IF version_status IS DISTINCT FROM 'draft' THEN
        RAISE EXCEPTION 'provider catalog profiles may be added only to a draft version'
            USING ERRCODE = '55000';
    END IF;
    RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION provider_catalog_reject_mutation()
RETURNS trigger
LANGUAGE plpgsql
SET search_path FROM CURRENT
AS $$
BEGIN
    RAISE EXCEPTION 'published provider catalog rows are immutable; create a new version'
        USING ERRCODE = '55000';
END;
$$;

CREATE OR REPLACE FUNCTION provider_credential_handle_update_guard()
RETURNS trigger
LANGUAGE plpgsql
SET search_path FROM CURRENT
AS $$
BEGIN
    IF to_jsonb(NEW) IS NOT DISTINCT FROM to_jsonb(OLD) THEN
        RETURN NEW;
    END IF;
    IF (to_jsonb(NEW) - 'revoked_at') IS DISTINCT FROM (to_jsonb(OLD) - 'revoked_at')
       OR OLD.revoked_at IS NOT NULL
       OR NEW.revoked_at IS NULL THEN
        RAISE EXCEPTION 'credential handle identity is immutable and revocation is irreversible'
            USING ERRCODE = '55000';
    END IF;
    RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION provider_credential_handle_reject_delete()
RETURNS trigger
LANGUAGE plpgsql
SET search_path FROM CURRENT
AS $$
BEGIN
    RAISE EXCEPTION 'credential custody handles cannot be deleted'
        USING ERRCODE = '55000';
END;
$$;

DROP TRIGGER IF EXISTS provider_catalog_versions_transition_guard
    ON provider_catalog_versions;
CREATE TRIGGER provider_catalog_versions_transition_guard
BEFORE UPDATE ON provider_catalog_versions
FOR EACH ROW EXECUTE FUNCTION provider_catalog_version_transition_guard();

DROP TRIGGER IF EXISTS provider_catalog_profiles_insert_guard
    ON provider_catalog_profiles;
CREATE TRIGGER provider_catalog_profiles_insert_guard
BEFORE INSERT ON provider_catalog_profiles
FOR EACH ROW EXECUTE FUNCTION provider_catalog_profile_insert_guard();

DROP TRIGGER IF EXISTS provider_catalog_profiles_reject_update
    ON provider_catalog_profiles;
CREATE TRIGGER provider_catalog_profiles_reject_update
BEFORE UPDATE ON provider_catalog_profiles
FOR EACH ROW EXECUTE FUNCTION provider_catalog_reject_mutation();

DROP TRIGGER IF EXISTS provider_catalog_profiles_reject_delete
    ON provider_catalog_profiles;
CREATE TRIGGER provider_catalog_profiles_reject_delete
BEFORE DELETE ON provider_catalog_profiles
FOR EACH ROW EXECUTE FUNCTION provider_catalog_reject_mutation();

DROP TRIGGER IF EXISTS provider_catalog_versions_reject_delete
    ON provider_catalog_versions;
CREATE TRIGGER provider_catalog_versions_reject_delete
BEFORE DELETE ON provider_catalog_versions
FOR EACH ROW EXECUTE FUNCTION provider_catalog_reject_mutation();

DROP TRIGGER IF EXISTS provider_credential_handles_update_guard
    ON provider_credential_handles;
CREATE TRIGGER provider_credential_handles_update_guard
BEFORE UPDATE ON provider_credential_handles
FOR EACH ROW EXECUTE FUNCTION provider_credential_handle_update_guard();

DROP TRIGGER IF EXISTS provider_credential_handles_reject_delete
    ON provider_credential_handles;
CREATE TRIGGER provider_credential_handles_reject_delete
BEFORE DELETE ON provider_credential_handles
FOR EACH ROW EXECUTE FUNCTION provider_credential_handle_reject_delete();

ALTER TABLE provider_credential_handles ENABLE ROW LEVEL SECURITY;
ALTER TABLE provider_credential_handles FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation ON provider_credential_handles;
CREATE POLICY tenant_isolation ON provider_credential_handles
    USING (tenant_id = current_setting('app.tenant_id', true))
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true));

COMMENT ON TABLE provider_catalog_versions IS
    'Global immutable provider-catalog publications; exactly zero or one version is active.';
COMMENT ON TABLE provider_catalog_profiles IS
    'Secret-free execution profiles published by Techstack catalog authority.';
COMMENT ON COLUMN provider_catalog_profiles.capability_snapshot IS
    'Typed provider capabilities: regions, zones, instance_types, architectures, network, and storage only; bounded and secret-free.';
COMMENT ON TABLE provider_credential_handles IS
    'Tenant-owned versioned opaque custody handles; provider credential material remains in the external custody backend.';
