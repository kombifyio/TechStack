-- Publish the IONOS Basic Cube catalog successor (PROVIDER-CATALOG §11, owner
-- decision 2026-09-24). IONOS offerings now provision a fixed Basic Cube from
-- its template instead of a custom VCPU server:
--   monthly-runtime-standard -> Basic Cube S (2 vCPU / 4 GB / 120 GB DAS)
--   monthly-runtime-premium  -> Basic Cube M (4 vCPU / 8 GB / 240 GB DAS)
-- The change of instance type and storage class gets new runtime profiles and
-- a new adapter manifest in a new immutable catalog version; the published
-- versions are not edited. Centron rows are copied unchanged. Generations
-- pinned to earlier IONOS manifests keep read-only continuation and
-- decommission through the adapter's compatible-continuation list.

SET LOCAL lock_timeout = '5s';
SELECT pg_catalog.set_config(
    'search_path',
    pg_catalog.quote_ident(pg_catalog.current_schema()) || ', pg_catalog, pg_temp',
    true
);

INSERT INTO provider_catalog_versions (catalog_version, status)
VALUES ('managed-vps-2026-09-24.1', 'draft')
ON CONFLICT (catalog_version) DO NOTHING;

INSERT INTO provider_catalog_profiles (
    catalog_version, provider_id, adapter_id, credential_mode,
    runtime_profile_id, offering_id, can_pause, stop_effect, can_recreate,
    capability_snapshot, provision_dispatch_mode, adapter_manifest_hash
)
SELECT
    'managed-vps-2026-09-24.1', profile.provider_id, profile.adapter_id,
    profile.credential_mode, profile.runtime_profile_id, profile.offering_id,
    profile.can_pause, profile.stop_effect, profile.can_recreate,
    profile.capability_snapshot, profile.provision_dispatch_mode,
    profile.adapter_manifest_hash
FROM provider_catalog_profiles AS profile
JOIN provider_catalog_versions AS version
  ON version.catalog_version = profile.catalog_version
WHERE version.status = 'active'
  AND profile.provider_id = 'centron'
  AND profile.offering_id IN ('monthly-runtime-standard', 'monthly-runtime-premium')
ON CONFLICT (catalog_version, provider_id, runtime_profile_id, offering_id)
DO NOTHING;

INSERT INTO provider_catalog_profiles (
    catalog_version, provider_id, adapter_id, credential_mode,
    runtime_profile_id, offering_id, can_pause, stop_effect, can_recreate,
    capability_snapshot, provision_dispatch_mode, adapter_manifest_hash
)
SELECT
    'managed-vps-2026-09-24.1', 'ionos', 'ionos-cloudapi-v6', 'managed',
    cube.runtime_profile_id, cube.offering_id,
    -- A Cube stops by the provider suspend action and starts by resume; the
    -- same resource generation, its storage and its billing persist.
    true, 'pause', true,
    cube.capability_snapshot,
    'at_most_once_dispatch_manual_reconcile',
    'sha256:14cfeec2043a458b242ab26e39ebe3383ece9e38abd9e3cb04499ad889a92434'
FROM (
    VALUES
        (
            'ionos-basic-cube-standard',
            'monthly-runtime-standard',
            '{
              "regions": ["de-fra"],
              "instance_types": ["basic-cube-s"],
              "architectures": ["amd64"],
              "network": {
                "ipv4": true,
                "ipv6": false,
                "private_network": false,
                "floating_ip": false
              },
              "storage": {
                "volume_types": ["das"],
                "snapshots": false,
                "online_resize": false
              }
            }'::jsonb
        ),
        (
            'ionos-basic-cube-premium',
            'monthly-runtime-premium',
            '{
              "regions": ["de-fra"],
              "instance_types": ["basic-cube-m"],
              "architectures": ["amd64"],
              "network": {
                "ipv4": true,
                "ipv6": false,
                "private_network": false,
                "floating_ip": false
              },
              "storage": {
                "volume_types": ["das"],
                "snapshots": false,
                "online_resize": false
              }
            }'::jsonb
        )
) AS cube(runtime_profile_id, offering_id, capability_snapshot)
ON CONFLICT (catalog_version, provider_id, runtime_profile_id, offering_id)
DO NOTHING;

DO $$
DECLARE
    covered_pairs integer;
    exact_pins integer;
BEGIN
    SELECT count(*) INTO covered_pairs
    FROM provider_catalog_profiles
    WHERE catalog_version = 'managed-vps-2026-09-24.1';

    SELECT count(*) INTO exact_pins
    FROM provider_catalog_profiles
    WHERE catalog_version = 'managed-vps-2026-09-24.1'
      AND credential_mode = 'managed'
      AND provision_dispatch_mode = 'at_most_once_dispatch_manual_reconcile'
      AND can_pause
      AND stop_effect = 'pause'
      AND (
          (provider_id = 'ionos' AND adapter_id = 'ionos-cloudapi-v6'
           AND adapter_manifest_hash = 'sha256:14cfeec2043a458b242ab26e39ebe3383ece9e38abd9e3cb04499ad889a92434'
           AND (runtime_profile_id, offering_id, capability_snapshot -> 'instance_types') IN (
               ('ionos-basic-cube-standard', 'monthly-runtime-standard', '["basic-cube-s"]'::jsonb),
               ('ionos-basic-cube-premium', 'monthly-runtime-premium', '["basic-cube-m"]'::jsonb)
           ))
          OR
          (provider_id = 'centron' AND adapter_id = 'centron-ccloud-v1'
           AND adapter_manifest_hash = 'sha256:15f74f306d6285eb1e9418ada04332796b99df9ea35de6090eb59a0e6edf16cf')
      );

    IF covered_pairs <> 4 OR exact_pins <> 4 THEN
        RAISE EXCEPTION
            'IONOS Basic Cube successor requires four exact adapter-pinned pause profiles, found % profiles and % exact pins',
            covered_pairs,
            exact_pins;
    END IF;
END;
$$;

UPDATE provider_catalog_versions
SET status = 'retired', retired_at = clock_timestamp()
WHERE status = 'active'
  AND catalog_version <> 'managed-vps-2026-09-24.1';

UPDATE provider_catalog_versions
SET status = 'active', activated_at = clock_timestamp()
WHERE catalog_version = 'managed-vps-2026-09-24.1'
  AND status = 'draft';
