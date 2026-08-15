-- Publish the first executable managed-VPS catalog used by the Creation
-- Wizard. The IONOS profile creates a fresh dedicated DCD aggregate; it never
-- targets the operator's pre-existing private data centers.

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
    'managed-vps-2026-07-24.1',
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
) VALUES (
    'managed-vps-2026-07-24.1',
    'ionos',
    'ionos-cloudapi-v6',
    'managed',
    'ionos-dcd-managed-vps',
    'monthly-runtime-standard',
    false,
    'destroy',
    true,
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
    'at_most_once_dispatch_manual_reconcile',
    'sha256:15a6fd62a729f4a6478eefdd3c368b81d023aeb1e41ed624abdf226a61c5a969'
)
ON CONFLICT (
    catalog_version,
    provider_id,
    runtime_profile_id,
    offering_id
) DO NOTHING;

UPDATE provider_catalog_versions
SET status = 'retired',
    retired_at = clock_timestamp()
WHERE status = 'active'
  AND catalog_version <> 'managed-vps-2026-07-24.1';

UPDATE provider_catalog_versions
SET status = 'active',
    activated_at = clock_timestamp()
WHERE catalog_version = 'managed-vps-2026-07-24.1'
  AND status = 'draft';
