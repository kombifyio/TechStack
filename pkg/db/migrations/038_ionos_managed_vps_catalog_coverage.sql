-- Publish one executable IONOS DCD profile for every managed-runtime
-- offering exposed by the Creation Wizard. Catalog versions are immutable:
-- coverage changes create and activate a complete successor version.

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
    'managed-vps-2026-07-24.2',
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
    'managed-vps-2026-07-24.2',
    'ionos',
    'ionos-cloudapi-v6',
    'managed',
    profile.runtime_profile_id,
    profile.offering_id,
    false,
    'destroy',
    true,
    profile.capability_snapshot,
    'at_most_once_dispatch_manual_reconcile',
    'sha256:15a6fd62a729f4a6478eefdd3c368b81d023aeb1e41ed624abdf226a61c5a969'
FROM (
    VALUES
        (
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
            }'::jsonb
        ),
        (
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
            }'::jsonb
        )
) AS profile(runtime_profile_id, offering_id, capability_snapshot)
ON CONFLICT (
    catalog_version,
    provider_id,
    runtime_profile_id,
    offering_id
) DO NOTHING;

DO $$
DECLARE
    covered_offerings integer;
BEGIN
    SELECT count(*)
    INTO covered_offerings
    FROM provider_catalog_profiles
    WHERE catalog_version = 'managed-vps-2026-07-24.2'
      AND provider_id = 'ionos'
      AND offering_id IN (
          'monthly-runtime-standard',
          'monthly-runtime-premium'
      );
    IF covered_offerings <> 2 THEN
        RAISE EXCEPTION
            'IONOS managed-VPS catalog must cover exactly two product offerings';
    END IF;
END;
$$;

UPDATE provider_catalog_versions
SET status = 'retired',
    retired_at = clock_timestamp()
WHERE status = 'active'
  AND catalog_version <> 'managed-vps-2026-07-24.2';

UPDATE provider_catalog_versions
SET status = 'active',
    activated_at = clock_timestamp()
WHERE catalog_version = 'managed-vps-2026-07-24.2'
  AND status = 'draft';
