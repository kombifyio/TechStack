-- Managed Day-2 power semantics (TS-2).
--
-- Stop and start of a managed IONOS or Centron server are an in-place power
-- transition of the same resource generation: the exact provider graph, the
-- bound public IPv4, the lease, the capacity reservation and monthly billing
-- all persist. Decommission stays the only destroy. The provider-control
-- ledger executes the transition as a generation-bound reconcile whose one
-- side-effecting step runs under the accepted-head claim and whose
-- convergence is observed read-only at resources_bound.

SET LOCAL lock_timeout = '5s';
SELECT pg_catalog.set_config(
    'search_path',
    pg_catalog.quote_ident(pg_catalog.current_schema()) || ', pg_catalog, pg_temp',
    true
);

-- Reconcile convergence polling is read-only, exactly like provision polling.
-- Without this row the database backstop rejected every claim at the
-- resources_bound head, so a reconcile could never reach present.
CREATE OR REPLACE FUNCTION provider_expected_execution_claim_access(
    operation_name text,
    dispatch_mode text,
    receipt_phase text
)
RETURNS text
LANGUAGE sql
IMMUTABLE
STRICT
AS $$
    SELECT CASE
        WHEN dispatch_mode = 'blocked' THEN NULL
        WHEN operation_name IN ('plan', 'observe')
             AND receipt_phase = 'accepted'
            THEN 'read_only'
        WHEN operation_name = 'provision'
             AND receipt_phase = 'accepted'
            THEN 'side_effecting'
        WHEN operation_name = 'provision'
             AND receipt_phase = 'resources_bound'
            THEN 'read_only'
        WHEN operation_name = 'reconcile'
             AND receipt_phase = 'accepted'
            THEN 'side_effecting'
        WHEN operation_name = 'reconcile'
             AND receipt_phase = 'resources_bound'
            THEN 'read_only'
        WHEN operation_name = 'decommission'
             AND receipt_phase = 'accepted'
            THEN 'side_effecting'
        WHEN operation_name = 'decommission'
             AND receipt_phase IN ('delete_accepted', 'absence_pending')
            THEN 'read_only'
        ELSE NULL
    END
$$;

-- Publish the power-reconcile adapter successors and declare pause semantics.
-- Historical manifests remain continuation-only and cannot authorize a new
-- create or a new reconcile.
INSERT INTO provider_catalog_versions (catalog_version, status)
VALUES ('managed-vps-2026-09-22.1', 'draft')
ON CONFLICT (catalog_version) DO NOTHING;

INSERT INTO provider_catalog_profiles (
    catalog_version, provider_id, adapter_id, credential_mode,
    runtime_profile_id, offering_id, can_pause, stop_effect, can_recreate,
    capability_snapshot, provision_dispatch_mode, adapter_manifest_hash
)
SELECT
    'managed-vps-2026-09-22.1', profile.provider_id, profile.adapter_id,
    profile.credential_mode, profile.runtime_profile_id, profile.offering_id,
    true, 'pause', profile.can_recreate,
    profile.capability_snapshot, profile.provision_dispatch_mode,
    CASE profile.provider_id
        WHEN 'ionos' THEN 'sha256:b45ec3df6fe32649a5ce23e2f97a7265c66681dd65b52fd64a4f066513a5f5f9'
        WHEN 'centron' THEN 'sha256:15f74f306d6285eb1e9418ada04332796b99df9ea35de6090eb59a0e6edf16cf'
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
ON CONFLICT (catalog_version, provider_id, runtime_profile_id, offering_id)
DO NOTHING;

DO $$
DECLARE
    covered_pairs integer;
    exact_pins integer;
BEGIN
    SELECT count(*) INTO covered_pairs
    FROM provider_catalog_profiles
    WHERE catalog_version = 'managed-vps-2026-09-22.1';

    SELECT count(*) INTO exact_pins
    FROM provider_catalog_profiles
    WHERE catalog_version = 'managed-vps-2026-09-22.1'
      AND credential_mode = 'managed'
      AND provision_dispatch_mode = 'at_most_once_dispatch_manual_reconcile'
      AND can_pause
      AND stop_effect = 'pause'
      AND (
          (provider_id = 'ionos' AND adapter_id = 'ionos-cloudapi-v6'
           AND adapter_manifest_hash = 'sha256:b45ec3df6fe32649a5ce23e2f97a7265c66681dd65b52fd64a4f066513a5f5f9')
          OR
          (provider_id = 'centron' AND adapter_id = 'centron-ccloud-v1'
           AND adapter_manifest_hash = 'sha256:15f74f306d6285eb1e9418ada04332796b99df9ea35de6090eb59a0e6edf16cf')
      );

    IF covered_pairs <> 4 OR exact_pins <> 4 THEN
        RAISE EXCEPTION
            'managed power-reconcile successor requires four exact adapter-pinned pause profiles, found % profiles and % exact pins',
            covered_pairs,
            exact_pins;
    END IF;
END;
$$;

UPDATE provider_catalog_versions
SET status = 'retired', retired_at = clock_timestamp()
WHERE status = 'active'
  AND catalog_version <> 'managed-vps-2026-09-22.1';

UPDATE provider_catalog_versions
SET status = 'active', activated_at = clock_timestamp()
WHERE catalog_version = 'managed-vps-2026-09-22.1'
  AND status = 'draft';
