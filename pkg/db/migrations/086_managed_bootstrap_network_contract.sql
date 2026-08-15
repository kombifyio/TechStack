-- Add the provider-neutral network requirements required before a managed
-- runtime can install and enrol Guard. Provider adapter exceptions remain
-- executable code facts selected by provider_id + adapter_id; this catalog
-- payload contains no credentials and owns no lifecycle.

SET LOCAL lock_timeout = '5s';
SELECT pg_catalog.set_config(
    'search_path',
    pg_catalog.quote_ident(pg_catalog.current_schema()) || ', pg_catalog, pg_temp',
    true
);

CREATE OR REPLACE FUNCTION provider_catalog_managed_bootstrap_network_is_valid(value jsonb)
RETURNS boolean
LANGUAGE sql
IMMUTABLE
STRICT
PARALLEL SAFE
SET search_path FROM CURRENT
AS $$
    SELECT CASE
        WHEN jsonb_typeof(value) <> 'object' THEN false
        WHEN value - ARRAY[
            'control_plane_egress', 'dns_resolution', 'public_ipv4',
            'ssh_ingress_port', 'host_firewall'
        ]::text[] <> '{}'::jsonb THEN false
        WHEN NOT (value ?& ARRAY[
            'control_plane_egress', 'dns_resolution', 'public_ipv4',
            'ssh_ingress_port', 'host_firewall'
        ]::text[]) THEN false
        ELSE
            jsonb_typeof(value -> 'control_plane_egress') = 'string'
            AND value ->> 'control_plane_egress' = 'https'
            AND jsonb_typeof(value -> 'dns_resolution') = 'boolean'
            AND value ->> 'dns_resolution' = 'true'
            AND jsonb_typeof(value -> 'public_ipv4') = 'boolean'
            AND value ->> 'public_ipv4' = 'true'
            AND jsonb_typeof(value -> 'ssh_ingress_port') = 'number'
            AND value ->> 'ssh_ingress_port' = '22'
            AND jsonb_typeof(value -> 'host_firewall') = 'string'
            AND value ->> 'host_firewall' = 'ssh_only'
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
                        'ipv4', 'ipv6', 'private_network', 'floating_ip', 'managed_bootstrap'
                    ]::text[] = '{}'::jsonb
                    AND NOT EXISTS (
                        SELECT 1
                        FROM jsonb_each(snapshot -> 'network') AS fields(field_name, field_value)
                        WHERE field_name <> 'managed_bootstrap'
                          AND jsonb_typeof(field_value) <> 'boolean'
                    )
                    AND CASE
                            WHEN (snapshot -> 'network') ? 'managed_bootstrap'
                                THEN provider_catalog_managed_bootstrap_network_is_valid(
                                    snapshot -> 'network' -> 'managed_bootstrap'
                                )
                            ELSE true
                        END
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

INSERT INTO provider_catalog_versions (catalog_version, status)
VALUES ('managed-vps-2026-08-14.2', 'draft')
ON CONFLICT (catalog_version) DO NOTHING;

INSERT INTO provider_catalog_profiles (
    catalog_version, provider_id, adapter_id, credential_mode,
    runtime_profile_id, offering_id, can_pause, stop_effect, can_recreate,
    capability_snapshot, provision_dispatch_mode, adapter_manifest_hash
)
SELECT
    'managed-vps-2026-08-14.2', profile.provider_id, profile.adapter_id,
    profile.credential_mode, profile.runtime_profile_id, profile.offering_id,
    profile.can_pause, profile.stop_effect, profile.can_recreate,
    profile.capability_snapshot || jsonb_build_object(
        'network', COALESCE(profile.capability_snapshot -> 'network', '{}'::jsonb)
            || jsonb_build_object(
                'managed_bootstrap', jsonb_build_object(
                    'control_plane_egress', 'https',
                    'dns_resolution', true,
                    'public_ipv4', true,
                    'ssh_ingress_port', 22,
                    'host_firewall', 'ssh_only'
                )
            )
    ),
    profile.provision_dispatch_mode, profile.adapter_manifest_hash
FROM provider_catalog_profiles AS profile
JOIN provider_catalog_versions AS version
  ON version.catalog_version = profile.catalog_version
WHERE version.status = 'active'
  AND (profile.provider_id, profile.offering_id) IN (
      ('ionos', 'monthly-runtime-standard'),
      ('ionos', 'monthly-runtime-premium'),
      ('centron', 'monthly-runtime-standard'),
      ('centron', 'monthly-runtime-premium')
  )
ON CONFLICT (catalog_version, provider_id, runtime_profile_id, offering_id)
DO NOTHING;

DO $$
DECLARE
    covered_profiles integer;
    contract_profiles integer;
BEGIN
    SELECT count(*) INTO covered_profiles
    FROM provider_catalog_profiles
    WHERE catalog_version = 'managed-vps-2026-08-14.2';

    SELECT count(*) INTO contract_profiles
    FROM provider_catalog_profiles
    WHERE catalog_version = 'managed-vps-2026-08-14.2'
      AND capability_snapshot #>> '{network,managed_bootstrap,control_plane_egress}' = 'https'
      AND capability_snapshot #>> '{network,managed_bootstrap,host_firewall}' = 'ssh_only'
      AND capability_snapshot #>> '{network,managed_bootstrap,ssh_ingress_port}' = '22';

    IF covered_profiles <> 4 OR contract_profiles <> 4 THEN
        RAISE EXCEPTION
            'managed bootstrap network contract requires four provider profiles, found % profiles and % contract profiles',
            covered_profiles,
            contract_profiles;
    END IF;
END;
$$;

UPDATE provider_catalog_versions
SET status = 'retired', retired_at = clock_timestamp()
WHERE status = 'active'
  AND catalog_version <> 'managed-vps-2026-08-14.2';

UPDATE provider_catalog_versions
SET status = 'active', activated_at = clock_timestamp()
WHERE catalog_version = 'managed-vps-2026-08-14.2'
  AND status = 'draft';
