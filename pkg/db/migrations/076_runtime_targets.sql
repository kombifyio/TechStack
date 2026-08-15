-- Evidence-backed runtime targets for servers and services.
--
-- A Kombify-operated VPS is still environment_class=cloud with
-- offering=managed_vps. Provider-native workloads are modeled on services as
-- target_kind=managed_workload and never receive a fake servers row.

SET LOCAL lock_timeout = '5s';
SELECT pg_catalog.set_config(
    'search_path',
    pg_catalog.quote_ident(pg_catalog.current_schema()) || ', pg_catalog, pg_temp',
    true
);

ALTER TABLE servers ADD COLUMN IF NOT EXISTS environment_class text NOT NULL DEFAULT 'unknown';
ALTER TABLE servers ADD COLUMN IF NOT EXISTS offering text;
ALTER TABLE servers ADD COLUMN IF NOT EXISTS provider_id text;
ALTER TABLE servers ADD COLUMN IF NOT EXISTS provider_target_ref text;
ALTER TABLE servers ADD COLUMN IF NOT EXISTS availability_owner text;
ALTER TABLE servers ADD COLUMN IF NOT EXISTS operations_owner text;
ALTER TABLE servers ADD COLUMN IF NOT EXISTS runtime_target_evidence_ref text;
ALTER TABLE servers ADD COLUMN IF NOT EXISTS runtime_target_observed_at timestamptz;

ALTER TABLE services ADD COLUMN IF NOT EXISTS target_kind text NOT NULL DEFAULT 'unknown';
ALTER TABLE services ADD COLUMN IF NOT EXISTS provider_id text;
ALTER TABLE services ADD COLUMN IF NOT EXISTS managed_target_ref text;
ALTER TABLE services ADD COLUMN IF NOT EXISTS provider_receipt_ref text;
ALTER TABLE services ADD COLUMN IF NOT EXISTS sla_policy_ref text;
ALTER TABLE services ADD COLUMN IF NOT EXISTS backup_policy_ref text;
ALTER TABLE services ADD COLUMN IF NOT EXISTS placement_evidence_ref text;
ALTER TABLE services ADD COLUMN IF NOT EXISTS placement_observed_at timestamptz;

-- Existing exact server bindings are sufficient to identify server placement.
-- Missing bindings stay unknown; they are never guessed to be Local.
UPDATE services
SET target_kind = CASE WHEN server_id IS NOT NULL THEN 'server' ELSE 'unknown' END
WHERE target_kind NOT IN ('server', 'managed_workload')
   OR (target_kind = 'server' AND server_id IS NULL);

-- A Hostinger provider binding is an external VPS only for the closed forms
-- accepted by the aggregate helper. The row timestamp records when this
-- migration observed the existing provider binding; it is not an SLA claim.
UPDATE servers
SET environment_class = 'cloud',
    offering = 'external_vps',
    provider_id = 'hostinger',
    provider_target_ref = provider_ref,
    availability_owner = 'provider',
    operations_owner = 'customer',
    runtime_target_evidence_ref = 'server-provider-binding:hostinger',
    runtime_target_observed_at = updated_at
WHERE environment_class = 'unknown'
  AND provider_ref IS NOT NULL
  AND (
      lower(btrim(provider_ref)) IN ('hostinger', 'hostinger-vps')
      OR lower(btrim(provider_ref)) LIKE 'hostinger:%'
      OR lower(btrim(provider_ref)) LIKE 'hostinger-vps:%'
  );

-- Provider-controlled runtime leases become Managed VPS only when the
-- provider projection already persisted the exact provider resource id.
UPDATE servers
SET environment_class = 'cloud',
    offering = 'managed_vps',
    provider_id = lower(btrim(provider_ref)),
    provider_target_ref = btrim(metadata_json->>'engine_vm_id'),
    availability_owner = 'provider',
    operations_owner = 'kombify',
    runtime_target_evidence_ref = 'runtime-lease:' || lease_id,
    runtime_target_observed_at = COALESCE(source_observed_at, updated_at)
WHERE environment_class = 'unknown'
  AND lease_id IS NOT NULL
  AND btrim(lease_id) <> ''
  AND provider_ref IS NOT NULL
  AND btrim(provider_ref) <> ''
  AND NULLIF(btrim(metadata_json->>'engine_vm_id'), '') IS NOT NULL;

ALTER TABLE servers DROP CONSTRAINT IF EXISTS servers_runtime_target_shape;
ALTER TABLE servers ADD CONSTRAINT servers_runtime_target_shape CHECK (
    (environment_class = 'unknown'
        AND offering IS NULL AND provider_id IS NULL AND provider_target_ref IS NULL
        AND availability_owner IS NULL AND operations_owner IS NULL
        AND runtime_target_evidence_ref IS NULL AND runtime_target_observed_at IS NULL)
    OR
    (environment_class = 'local'
        AND offering = 'self_owned_device'
        AND provider_id IS NULL AND provider_target_ref IS NULL
        AND availability_owner = 'customer' AND operations_owner = 'customer'
        AND runtime_target_evidence_ref IS NOT NULL AND runtime_target_observed_at IS NOT NULL)
    OR
    (environment_class = 'cloud'
        AND offering = 'external_vps'
        AND provider_id IS NOT NULL AND provider_target_ref IS NOT NULL
        AND availability_owner = 'provider' AND operations_owner = 'customer'
        AND runtime_target_evidence_ref IS NOT NULL AND runtime_target_observed_at IS NOT NULL)
    OR
    (environment_class = 'cloud'
        AND offering = 'managed_vps' AND lease_id IS NOT NULL
        AND provider_id IS NOT NULL AND provider_target_ref IS NOT NULL
        AND availability_owner = 'provider' AND operations_owner = 'kombify'
        AND runtime_target_evidence_ref IS NOT NULL AND runtime_target_observed_at IS NOT NULL)
) NOT VALID;

ALTER TABLE services DROP CONSTRAINT IF EXISTS services_runtime_target_shape;
ALTER TABLE services ADD CONSTRAINT services_runtime_target_shape CHECK (
    (target_kind = 'server'
        AND server_id IS NOT NULL
        AND provider_id IS NULL AND managed_target_ref IS NULL
        AND provider_receipt_ref IS NULL AND sla_policy_ref IS NULL
        AND backup_policy_ref IS NULL AND placement_evidence_ref IS NULL
        AND placement_observed_at IS NULL)
    OR
    (target_kind = 'managed_workload'
        AND server_id IS NULL
        AND provider_id IS NOT NULL AND managed_target_ref IS NOT NULL
        AND provider_receipt_ref IS NOT NULL AND sla_policy_ref IS NOT NULL
        AND backup_policy_ref IS NOT NULL AND placement_evidence_ref IS NOT NULL
        AND placement_observed_at IS NOT NULL)
    OR
    (target_kind = 'unknown'
        AND server_id IS NULL
        AND provider_id IS NULL AND managed_target_ref IS NULL
        AND provider_receipt_ref IS NULL AND sla_policy_ref IS NULL
        AND backup_policy_ref IS NULL AND placement_evidence_ref IS NULL
        AND placement_observed_at IS NULL)
) NOT VALID;

CREATE INDEX IF NOT EXISTS idx_servers_tenant_environment_offering
    ON servers (tenant_id, environment_class, offering);
CREATE INDEX IF NOT EXISTS idx_services_tenant_target
    ON services (tenant_id, target_kind, server_id);

-- Reassert the existing tenant posture after the additive schema change.
ALTER TABLE servers ENABLE ROW LEVEL SECURITY;
ALTER TABLE servers FORCE ROW LEVEL SECURITY;
ALTER TABLE services ENABLE ROW LEVEL SECURITY;
ALTER TABLE services FORCE ROW LEVEL SECURITY;
