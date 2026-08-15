-- Publish the provider-neutral Centron at-most-once dispatch semantics. An
-- ambiguous create remains handle-free and can be adopted only by the
-- expected-revision Operator Reconcile workflow.

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
    'managed-vps-2026-08-09.3',
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
    'managed-vps-2026-08-09.3',
    profile.provider_id,
    profile.adapter_id,
    profile.credential_mode,
    profile.runtime_profile_id,
    profile.offering_id,
    profile.can_pause,
    profile.stop_effect,
    profile.can_recreate,
    profile.capability_snapshot,
    profile.provision_dispatch_mode,
    CASE profile.provider_id
        WHEN 'centron' THEN 'sha256:f04850b487a87e5531d097ec6f9d334fc656cf8f0d5b79957426ea209b99f47a'
        ELSE profile.adapter_manifest_hash
    END
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
ON CONFLICT (
    catalog_version,
    provider_id,
    runtime_profile_id,
    offering_id
) DO NOTHING;

DO $$
DECLARE
    covered_pairs integer;
    exact_pins integer;
BEGIN
    SELECT count(*)
    INTO covered_pairs
    FROM provider_catalog_profiles
    WHERE catalog_version = 'managed-vps-2026-08-09.3';

    SELECT count(*)
    INTO exact_pins
    FROM provider_catalog_profiles
    WHERE catalog_version = 'managed-vps-2026-08-09.3'
      AND credential_mode = 'managed'
      AND provision_dispatch_mode = 'at_most_once_dispatch_manual_reconcile'
      AND (
          (
              provider_id = 'ionos'
              AND adapter_id = 'ionos-cloudapi-v6'
              AND adapter_manifest_hash = 'sha256:15a6fd62a729f4a6478eefdd3c368b81d023aeb1e41ed624abdf226a61c5a969'
          ) OR (
              provider_id = 'centron'
              AND adapter_id = 'centron-ccloud-v1'
              AND adapter_manifest_hash = 'sha256:f04850b487a87e5531d097ec6f9d334fc656cf8f0d5b79957426ea209b99f47a'
          )
      );

    IF covered_pairs <> 4 OR exact_pins <> 4 THEN
        RAISE EXCEPTION
            'managed-VPS successor requires four exact adapter-pinned profiles, found % profiles and % exact pins',
            covered_pairs,
            exact_pins;
    END IF;
END;
$$;

UPDATE provider_catalog_versions
SET status = 'retired',
    retired_at = clock_timestamp()
WHERE status = 'active'
  AND catalog_version <> 'managed-vps-2026-08-09.3';

UPDATE provider_catalog_versions
SET status = 'active',
    activated_at = clock_timestamp()
WHERE catalog_version = 'managed-vps-2026-08-09.3'
  AND status = 'draft';
