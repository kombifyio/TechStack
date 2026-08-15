-- Republish managed-VPS coverage against the centron adapter build whose
-- request shape was verified by a live create and delete, not inferred from
-- documentation. The adapter manifest hash changes because the create body now
-- carries a per-generation correlation tag and because correlation, readiness
-- and deletion address the provider-assigned hostname instead of the create
-- response id.
--
-- Catalog versions are immutable, so this successor re-declares the IONOS
-- profiles unchanged alongside the corrected centron ones.

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
    'managed-vps-2026-07-26.2',
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
    'managed-vps-2026-07-26.2',
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
            'sha256:47196d1827dbb33186d87d8468c12b9a57a0110acba6841feb7c21e86f8494e3'
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
            'sha256:47196d1827dbb33186d87d8468c12b9a57a0110acba6841feb7c21e86f8494e3'
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
    WHERE catalog_version = 'managed-vps-2026-07-26.2'
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
  AND catalog_version <> 'managed-vps-2026-07-26.2';

UPDATE provider_catalog_versions
SET status = 'active',
    activated_at = clock_timestamp()
WHERE catalog_version = 'managed-vps-2026-07-26.2'
  AND status = 'draft';
