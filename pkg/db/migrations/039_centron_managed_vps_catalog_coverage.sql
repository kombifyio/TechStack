-- Add executable centron ccloud coverage alongside IONOS. Catalog versions are
-- immutable, so this successor must re-declare every profile the active version
-- carries: activating a version that listed only centron would silently strip
-- IONOS coverage and fail-close the provider that currently works.
--
-- centron declares one instance type for both offerings on purpose. The adapter
-- provisions from its custody bundle without branching on offering_id, so
-- claiming a larger premium machine here would make this authority table lie
-- about what the executor actually creates.

SET LOCAL lock_timeout = '5s';
SELECT pg_catalog.set_config(
    'search_path',
    pg_catalog.quote_ident(pg_catalog.current_schema()) || ', pg_catalog, pg_temp',
    true
);

INSERT INTO provider_catalog_versions (
    catalog_version,
    status
) VALUES (
    'managed-vps-2026-07-26.1',
    'draft'
)
ON CONFLICT (catalog_version) DO NOTHING;

INSERT INTO provider_catalog_profiles (
    catalog_version,
    provider_id,
    adapter_id,
    credential_mode,
    runtime_profile_id,
    offering_id,
    can_pause,
    stop_effect,
    can_recreate,
    capability_snapshot,
    provision_dispatch_mode,
    adapter_manifest_hash
)
SELECT
    'managed-vps-2026-07-26.1',
    profile.provider_id,
    profile.adapter_id,
    'managed',
    profile.runtime_profile_id,
    profile.offering_id,
    false,
    'destroy',
    true,
    profile.capability_snapshot,
    'at_most_once_dispatch_manual_reconcile',
    profile.adapter_manifest_hash
FROM (
    VALUES
        (
            'ionos',
            'ionos-cloudapi-v6',
            'ionos-dcd-managed-vps-standard',
            'monthly-runtime-standard',
            '{
              "regions": ["de-fra"],
              "instance_types": ["vcpu-s"],
              "architectures": ["amd64"],
              "network": {
                "ipv4": true,
                "ipv6": false,
                "private_network": false,
                "floating_ip": false
              },
              "storage": {
                "volume_types": ["ssd-standard"],
                "snapshots": false,
                "online_resize": false
              }
            }'::jsonb,
            'sha256:15a6fd62a729f4a6478eefdd3c368b81d023aeb1e41ed624abdf226a61c5a969'
        ),
        (
            'ionos',
            'ionos-cloudapi-v6',
            'ionos-dcd-managed-vps-premium',
            'monthly-runtime-premium',
            '{
              "regions": ["de-fra"],
              "instance_types": ["vcpu-l"],
              "architectures": ["amd64"],
              "network": {
                "ipv4": true,
                "ipv6": false,
                "private_network": false,
                "floating_ip": false
              },
              "storage": {
                "volume_types": ["ssd-standard"],
                "snapshots": false,
                "online_resize": false
              }
            }'::jsonb,
            'sha256:15a6fd62a729f4a6478eefdd3c368b81d023aeb1e41ed624abdf226a61c5a969'
        ),
        (
            'centron',
            'centron-ccloud-v1',
            'centron-ccloud-managed-vps-standard',
            'monthly-runtime-standard',
            '{
              "regions": ["de-bam"],
              "instance_types": ["ccloud-worker-2c4g"],
              "architectures": ["amd64"],
              "network": {
                "ipv4": true,
                "ipv6": false,
                "private_network": false,
                "floating_ip": false
              },
              "storage": {
                "volume_types": ["ssd-standard"],
                "snapshots": false,
                "online_resize": false
              }
            }'::jsonb,
            'sha256:0f0b5d6a4de6f0b81cbb0f2b4e9c2f2b4a7e91a5c3d80f6b2e4a1c7d5b9e3f80'
        ),
        (
            'centron',
            'centron-ccloud-v1',
            'centron-ccloud-managed-vps-premium',
            'monthly-runtime-premium',
            '{
              "regions": ["de-bam"],
              "instance_types": ["ccloud-worker-2c4g"],
              "architectures": ["amd64"],
              "network": {
                "ipv4": true,
                "ipv6": false,
                "private_network": false,
                "floating_ip": false
              },
              "storage": {
                "volume_types": ["ssd-standard"],
                "snapshots": false,
                "online_resize": false
              }
            }'::jsonb,
            'sha256:0f0b5d6a4de6f0b81cbb0f2b4e9c2f2b4a7e91a5c3d80f6b2e4a1c7d5b9e3f80'
        )
) AS profile(
    provider_id,
    adapter_id,
    runtime_profile_id,
    offering_id,
    capability_snapshot,
    adapter_manifest_hash
)
ON CONFLICT (
    catalog_version,
    provider_id,
    runtime_profile_id,
    offering_id
) DO NOTHING;

DO $$
DECLARE
    covered_pairs integer;
BEGIN
    SELECT count(*)
    INTO covered_pairs
    FROM provider_catalog_profiles
    WHERE catalog_version = 'managed-vps-2026-07-26.1'
      AND (provider_id, offering_id) IN (
          ('ionos', 'monthly-runtime-standard'),
          ('ionos', 'monthly-runtime-premium'),
          ('centron', 'monthly-runtime-standard'),
          ('centron', 'monthly-runtime-premium')
      );
    IF covered_pairs <> 4 THEN
        RAISE EXCEPTION
            'managed-VPS catalog must cover both offerings for IONOS and centron, found %',
            covered_pairs;
    END IF;
END;
$$;

UPDATE provider_catalog_versions
SET status = 'retired',
    retired_at = clock_timestamp()
WHERE status = 'active'
  AND catalog_version <> 'managed-vps-2026-07-26.1';

UPDATE provider_catalog_versions
SET status = 'active',
    activated_at = clock_timestamp()
WHERE catalog_version = 'managed-vps-2026-07-26.1'
  AND status = 'draft';
