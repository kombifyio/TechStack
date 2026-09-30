-- Correct the IONOS Basic Cube region (PROVIDER-CATALOG §11). The first live
-- Cube creates in de/fra were refused with HTTP 403 "427 Access Denied as the
-- location does not support the cube feature"; GET /locations lists the cube
-- feature for the Frankfurt sub-location de/fra/2 (frankfurt-east), where the
-- adapter now creates Cubes. The published capability snapshot must say so,
-- so this is a new immutable catalog version: every active profile is copied
-- and only the IONOS regions change. Centron rows are unchanged.

SET LOCAL lock_timeout = '5s';
SELECT pg_catalog.set_config(
    'search_path',
    pg_catalog.quote_ident(pg_catalog.current_schema()) || ', pg_catalog, pg_temp',
    true
);

INSERT INTO provider_catalog_versions (catalog_version, status)
VALUES ('managed-vps-2026-09-24.2', 'draft')
ON CONFLICT (catalog_version) DO NOTHING;

INSERT INTO provider_catalog_profiles (
    catalog_version, provider_id, adapter_id, credential_mode,
    runtime_profile_id, offering_id, can_pause, stop_effect, can_recreate,
    capability_snapshot, provision_dispatch_mode, adapter_manifest_hash
)
SELECT
    'managed-vps-2026-09-24.2', profile.provider_id, profile.adapter_id,
    profile.credential_mode, profile.runtime_profile_id, profile.offering_id,
    profile.can_pause, profile.stop_effect, profile.can_recreate,
    CASE WHEN profile.provider_id = 'ionos'
        THEN jsonb_set(profile.capability_snapshot, '{regions}', '["de-fra-2"]'::jsonb)
        ELSE profile.capability_snapshot
    END,
    profile.provision_dispatch_mode, profile.adapter_manifest_hash
FROM provider_catalog_profiles AS profile
JOIN provider_catalog_versions AS version
  ON version.catalog_version = profile.catalog_version
WHERE version.catalog_version = 'managed-vps-2026-09-24.1'
ON CONFLICT (catalog_version, provider_id, runtime_profile_id, offering_id)
DO NOTHING;

DO $$
DECLARE
    covered_pairs integer;
    cube_regions integer;
BEGIN
    SELECT count(*) INTO covered_pairs
    FROM provider_catalog_profiles
    WHERE catalog_version = 'managed-vps-2026-09-24.2';

    SELECT count(*) INTO cube_regions
    FROM provider_catalog_profiles
    WHERE catalog_version = 'managed-vps-2026-09-24.2'
      AND provider_id = 'ionos'
      AND capability_snapshot -> 'regions' = '["de-fra-2"]'::jsonb
      AND capability_snapshot -> 'instance_types' IN ('["basic-cube-s"]'::jsonb, '["basic-cube-m"]'::jsonb);

    IF covered_pairs <> 4 OR cube_regions <> 2 THEN
        RAISE EXCEPTION
            'IONOS Basic Cube location successor requires four profiles with two de-fra-2 Cube profiles, found % profiles and % Cube regions',
            covered_pairs,
            cube_regions;
    END IF;
END;
$$;

UPDATE provider_catalog_versions
SET status = 'retired', retired_at = clock_timestamp()
WHERE status = 'active'
  AND catalog_version <> 'managed-vps-2026-09-24.2';

UPDATE provider_catalog_versions
SET status = 'active', activated_at = clock_timestamp()
WHERE catalog_version = 'managed-vps-2026-09-24.2'
  AND status = 'draft';
