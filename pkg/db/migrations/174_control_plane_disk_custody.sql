-- The runtime database takes custody of the control-plane state that lived
-- only on the instance's data disk (kombify-Techstack-6vse8). Without that
-- disk a deploy starts the new instance before the old one stops.
--
-- runtime_host_key_pins: the SSH host-key pin of every managed target, keyed
-- by its normalized host:port. Secret-free and not tenant data (it is the
-- control plane's known_hosts). The application role reads and adds pins; no
-- runtime role changes or removes one, so a changed node key keeps failing
-- closed until an operator retires the pin deliberately.
--
-- stack_spec_files: the managed spec files of each stack (byte-exact intent,
-- requirements, StackKits handoff and immutable snapshots). Tenant data under
-- the standard FORCE RLS tenant policy; the application capability policy
-- grants it like every tenant table.
--
-- stack_disk_archive: the byte-exact copy of every file a stack directory
-- held on the disk, taken before the disk is removed (the backup kept in
-- existing storage). Tenant data under the same policy, removed with its stack.
--
-- control_plane_disk_archive: the byte-exact copy of the disk's tenant-free
-- control-plane files (pins, instance identity, enrollment record). The
-- application role adds and reads rows; it never changes one.
--
-- Additive: a binary that predates this migration neither reads nor writes
-- these tables.

SET LOCAL lock_timeout = '5s';
SELECT pg_catalog.set_config(
    'search_path',
    pg_catalog.quote_ident(pg_catalog.current_schema()) || ', pg_catalog, pg_temp',
    true
);

CREATE TABLE IF NOT EXISTS runtime_host_key_pins (
    address text PRIMARY KEY,
    fingerprint text NOT NULL,
    source text NOT NULL,
    pinned_at timestamptz NOT NULL DEFAULT now(),
    CHECK (BTRIM(address) <> '' AND address = lower(address) AND position(':' IN address) > 1),
    CHECK (fingerprint ~ '^SHA256:[A-Za-z0-9+/]+$'),
    CHECK (source IN ('observed', 'disk_import'))
);
REVOKE ALL ON TABLE runtime_host_key_pins FROM PUBLIC;

CREATE TABLE IF NOT EXISTS stack_spec_files (
    tenant_id text NOT NULL,
    stack_id text NOT NULL,
    filename text NOT NULL,
    content bytea NOT NULL,
    sha256 text NOT NULL,
    source text NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, stack_id, filename),
    FOREIGN KEY (tenant_id, stack_id) REFERENCES stacks(tenant_id, id) ON DELETE CASCADE,
    CHECK (filename IN ('kombination.yaml', 'requirements-spec.yaml', 'unified-spec.yaml',
                        'stack-spec.yaml', 'stack-spec.address-bound.v2.json')
           OR filename ~ '^stack-spec\.snapshot-[a-f0-9]{64}\.v2\.json$'),
    CHECK (sha256 ~ '^[0-9a-f]{64}$'),
    CHECK (source IN ('write', 'disk_import'))
);
ALTER TABLE stack_spec_files ENABLE ROW LEVEL SECURITY;
ALTER TABLE stack_spec_files FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation ON stack_spec_files;
CREATE POLICY tenant_isolation ON stack_spec_files
    USING (tenant_id = current_setting('app.tenant_id', true))
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true));

CREATE TABLE IF NOT EXISTS stack_disk_archive (
    tenant_id text NOT NULL,
    stack_id text NOT NULL,
    relative_path text NOT NULL,
    sha256 text NOT NULL,
    byte_size bigint NOT NULL,
    content bytea NOT NULL,
    archived_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, stack_id, relative_path, sha256),
    FOREIGN KEY (tenant_id, stack_id) REFERENCES stacks(tenant_id, id) ON DELETE CASCADE,
    CHECK (BTRIM(relative_path) <> '' AND left(relative_path, 1) <> '/' AND position('..' IN relative_path) = 0),
    CHECK (sha256 ~ '^[0-9a-f]{64}$'),
    CHECK (byte_size = octet_length(content))
);
ALTER TABLE stack_disk_archive ENABLE ROW LEVEL SECURITY;
ALTER TABLE stack_disk_archive FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation ON stack_disk_archive;
CREATE POLICY tenant_isolation ON stack_disk_archive
    USING (tenant_id = current_setting('app.tenant_id', true))
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true));

CREATE TABLE IF NOT EXISTS control_plane_disk_archive (
    source_path text NOT NULL,
    sha256 text NOT NULL,
    byte_size bigint NOT NULL,
    content bytea NOT NULL,
    archived_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (source_path, sha256),
    CHECK (BTRIM(source_path) <> '' AND left(source_path, 1) <> '/' AND position('..' IN source_path) = 0),
    CHECK (sha256 ~ '^[0-9a-f]{64}$'),
    CHECK (byte_size = octet_length(content))
);
REVOKE ALL ON TABLE control_plane_disk_archive FROM PUBLIC;

DO $control_plane_disk_custody_grants$
DECLARE
    active_schema text := pg_catalog.current_schema();
    application_role text := 'techstack_application';
    grant_target record;
    privilege text;
BEGIN
    IF pg_catalog.to_regrole(application_role) IS NULL THEN
        RETURN;
    END IF;
    IF EXISTS (
        SELECT 1
        FROM pg_catalog.pg_roles
        WHERE rolname = application_role
          AND (rolsuper OR rolbypassrls)
    ) THEN
        RAISE EXCEPTION 'disk custody grants refuse a superuser or BYPASSRLS role'
            USING ERRCODE = '42501';
    END IF;
    FOR grant_target IN
        SELECT *
        FROM (
            VALUES
                ('runtime_host_key_pins', ARRAY['SELECT', 'INSERT']::text[]),
                ('control_plane_disk_archive', ARRAY['SELECT', 'INSERT']::text[])
        ) AS grant_row(table_name, privileges)
    LOOP
        FOREACH privilege IN ARRAY grant_target.privileges
        LOOP
            EXECUTE pg_catalog.format(
                'GRANT %s ON TABLE %I.%I TO %I',
                privilege,
                active_schema,
                grant_target.table_name,
                application_role
            );
        END LOOP;
    END LOOP;
END
$control_plane_disk_custody_grants$;
