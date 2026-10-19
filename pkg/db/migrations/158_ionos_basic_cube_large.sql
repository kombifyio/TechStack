-- Admit the policy-named Basic Cube L as a distinct offering. All existing
-- profiles, manifests and pinned resource generations retain their meaning.
-- No Centron Large package is admitted by this catalog successor.
SET LOCAL lock_timeout = '5s';
SELECT pg_catalog.set_config(
    'search_path',
    pg_catalog.quote_ident(pg_catalog.current_schema()) || ', pg_catalog, pg_temp',
    true
);

INSERT INTO provider_catalog_versions (catalog_version, status)
VALUES ('managed-vps-2026-10-18.1', 'draft')
ON CONFLICT (catalog_version) DO NOTHING;

INSERT INTO provider_catalog_profiles (
    catalog_version, provider_id, adapter_id, credential_mode,
    runtime_profile_id, offering_id, can_pause, stop_effect, can_recreate,
    capability_snapshot, provision_dispatch_mode, adapter_manifest_hash
)
SELECT
    'managed-vps-2026-10-18.1', profile.provider_id, profile.adapter_id,
    profile.credential_mode, profile.runtime_profile_id, profile.offering_id,
    profile.can_pause, profile.stop_effect, profile.can_recreate,
    profile.capability_snapshot, profile.provision_dispatch_mode,
    profile.adapter_manifest_hash
FROM provider_catalog_profiles AS profile
JOIN provider_catalog_versions AS version
  ON version.catalog_version = profile.catalog_version
WHERE version.status = 'active'
ON CONFLICT (catalog_version, provider_id, runtime_profile_id, offering_id)
DO NOTHING;

INSERT INTO provider_catalog_profiles (
    catalog_version, provider_id, adapter_id, credential_mode,
    runtime_profile_id, offering_id, can_pause, stop_effect, can_recreate,
    capability_snapshot, provision_dispatch_mode, adapter_manifest_hash
)
SELECT
    catalog_version, provider_id, adapter_id, credential_mode,
    'ionos-basic-cube-large', 'monthly-runtime-large',
    can_pause, stop_effect, can_recreate,
    jsonb_set(capability_snapshot, '{instance_types}', '["basic-cube-l"]'::jsonb),
    provision_dispatch_mode, adapter_manifest_hash
FROM provider_catalog_profiles
WHERE catalog_version = 'managed-vps-2026-10-18.1'
  AND provider_id = 'ionos'
  AND runtime_profile_id = 'ionos-basic-cube-premium'
  AND offering_id = 'monthly-runtime-premium'
  AND adapter_id = 'ionos-cloudapi-v6'
  AND adapter_manifest_hash = 'sha256:640fcf4852e807ed28fa571bdad0ff2b7a21d74a4be975f2729ca5c6e3aef8ad'
ON CONFLICT (catalog_version, provider_id, runtime_profile_id, offering_id)
DO NOTHING;

DO $$
BEGIN
    -- Fail closed if any previously active profile was lost or reinterpreted.
    IF EXISTS (
        SELECT provider_id, adapter_id, credential_mode, runtime_profile_id,
               offering_id, can_pause, stop_effect, can_recreate,
               capability_snapshot, provision_dispatch_mode, adapter_manifest_hash
        FROM provider_catalog_profiles AS profile
        JOIN provider_catalog_versions AS version USING (catalog_version)
        WHERE version.status = 'active'
        EXCEPT
        SELECT provider_id, adapter_id, credential_mode, runtime_profile_id,
               offering_id, can_pause, stop_effect, can_recreate,
               capability_snapshot, provision_dispatch_mode, adapter_manifest_hash
        FROM provider_catalog_profiles
        WHERE catalog_version = 'managed-vps-2026-10-18.1'
    ) THEN
        RAISE EXCEPTION 'Basic Cube L successor must preserve every active profile exactly';
    END IF;

    IF (SELECT count(*) FROM provider_catalog_profiles
        WHERE catalog_version = 'managed-vps-2026-10-18.1'
          AND offering_id = 'monthly-runtime-large') <> 1
       OR NOT EXISTS (
        SELECT 1 FROM provider_catalog_profiles
        WHERE catalog_version = 'managed-vps-2026-10-18.1'
          AND provider_id = 'ionos'
          AND runtime_profile_id = 'ionos-basic-cube-large'
          AND offering_id = 'monthly-runtime-large'
          AND credential_mode = 'managed'
          AND adapter_id = 'ionos-cloudapi-v6'
          AND adapter_manifest_hash = 'sha256:640fcf4852e807ed28fa571bdad0ff2b7a21d74a4be975f2729ca5c6e3aef8ad'
          AND provision_dispatch_mode = 'at_most_once_dispatch_manual_reconcile'
          AND can_pause AND stop_effect = 'pause' AND can_recreate
          AND capability_snapshot -> 'instance_types' = '["basic-cube-l"]'::jsonb
    ) THEN
        RAISE EXCEPTION 'Basic Cube L requires one exact IONOS adapter-pinned managed profile';
    END IF;
END;
$$;

UPDATE provider_catalog_versions
SET status = 'retired', retired_at = clock_timestamp()
WHERE status = 'active'
  AND catalog_version <> 'managed-vps-2026-10-18.1';

UPDATE provider_catalog_versions
SET status = 'active', activated_at = clock_timestamp()
WHERE catalog_version = 'managed-vps-2026-10-18.1'
  AND status = 'draft';
