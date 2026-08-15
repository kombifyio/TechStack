package db

import (
	"strings"
	"testing"
)

func TestManagedRuntimeCapacityMigrationBindsImmutableNativeCustody(t *testing.T) {
	content := readDBFile(t, "migrations/033_managed_runtime_capacity_reservations.sql")
	for _, required := range []string{
		"CREATE TABLE IF NOT EXISTS managed_runtime_capacity_reservations",
		"'provider_provision_dispatch_guard_validate_insert'",
		"'provider_execution_claim_current_head'",
		"'provider_execution_claim_credential_guard'",
		"'ALTER FUNCTION %I.%I() SECURITY DEFINER'",
		"'ALTER FUNCTION %I.%I() SET search_path TO pg_catalog, %I'",
		"'REVOKE ALL ON FUNCTION %I.%I() FROM PUBLIC'",
		"owner_subject_id text NOT NULL",
		"provider_id text NOT NULL CHECK (provider_id IN ('ionos', 'centron'))",
		"resource_generation_id uuid NOT NULL",
		"reservation_mode IN ('limited', 'unlimited', 'quarantine')",
		"policy_source text NOT NULL",
		"edge_v2_entitlement+signed_budget:cloud.runtime.credits#managed_servers",
		"static_release_manifest:selfhost-oss",
		"migration_quarantine:managed-runtime-capacity/v1",
		"policy_digest ~ '^sha256:[0-9a-f]{64}$'",
		"reservation_origin IN ('native_admission', 'migration_quarantine')",
		"PRIMARY KEY (tenant_id, lease_id, resource_generation_id)",
		"UNIQUE (tenant_id, operation_id)",
		"FOREIGN KEY (tenant_id, operation_id, lease_id)",
		"REFERENCES provider_operations (tenant_id, operation_id, lease_id)",
		"DEFERRABLE INITIALLY DEFERRED",
		"reservation_mode = 'limited' AND capacity_limit BETWEEN 1 AND 2147483647",
		"reservation_mode IN ('unlimited', 'quarantine') AND capacity_limit IS NULL",
		"CREATE TRIGGER managed_runtime_capacity_reservations_validate_insert",
		"operation.command_json #>> '{command,provider_id}'",
		"operation_generation_id IS DISTINCT FROM NEW.resource_generation_id::text",
		"operation_provider_id IS DISTINCT FROM NEW.provider_id",
		"live_owner_subject_id IS DISTINCT FROM NEW.owner_subject_id",
		"live_execution_authority IS DISTINCT FROM 'techstack_provider_control'",
		"pg_advisory_xact_lock",
		"hashtext('providercontrol.capacity:owner_subject:' || NEW.owner_subject_id)",
		"held_capacity >= NEW.capacity_limit",
		"managed runtime capacity reservation exceeds the authoritative owner limit",
		"CREATE OR REPLACE FUNCTION managed_runtime_capacity_policy_digest",
		"providercontrol/managed-runtime-capacity-policy/v2",
		"managed runtime capacity policy digest does not match its canonical projection",
		"BEFORE UPDATE OR DELETE ON managed_runtime_capacity_reservations",
		"ALTER TABLE managed_runtime_capacity_reservations FORCE ROW LEVEL SECURITY",
		"current_setting('app.tenant_id', true)",
	} {
		if !strings.Contains(content, required) {
			t.Fatalf("managed runtime capacity migration missing %q", required)
		}
	}

	for _, forbidden := range []string{
		"released_at",
		"release_proof",
		"ON DELETE CASCADE",
		"legacy_simulate' THEN RETURN NEW",
		"provisioning-executor/v1",
		"simulate_url",
		"FOR SHARE",
	} {
		if strings.Contains(strings.ToLower(content), strings.ToLower(forbidden)) {
			t.Fatalf("managed runtime capacity migration introduced forbidden release/fallback behavior via %q", forbidden)
		}
	}
}

func TestManagedRuntimeCapacityMigrationQuarantinesHistoryAndClosesOldWriterGap(t *testing.T) {
	content := readDBFile(t, "migrations/033_managed_runtime_capacity_reservations.sql")
	for _, required := range []string{
		"LOCK TABLE techstack_vm_leases",
		"provider_operation_execution_claims",
		"ALTER TABLE provider_operation_execution_claims DISABLE ROW LEVEL SECURITY",
		"ALTER TABLE provider_operation_execution_claims FORCE ROW LEVEL SECURITY",
		"'centron', 'ionos', 'centron-managed', 'ionos-managed'",
		"lease.lease_json->>'billing_mode'",
		"lease.lease_json->>'lifecycle_class'",
		"lease.lease_json->'resource'->>'provider_id'",
		"NULLIF(BTRIM(lease.engine_vm_id), '') IS NOT NULL",
		"authority.execution_authority IN ('legacy_simulate', 'techstack_provider_control')",
		"managed runtime capacity backfill found externally cost-bearing custody without exact IONOS or Centron identity",
		"managed runtime capacity backfill found conflicting IONOS and Centron custody identity",
		"managed runtime capacity backfill found cost custody without exact tenant, owner, or resource generation",
		"managed runtime capacity backfill found multiple native provision operations for one resource generation",
		"claim.lease_expires_at > clock_timestamp()",
		"managed runtime capacity cutover requires all provider execution claims to be drained",
		"'managed-runtime-capacity-quarantine/v1'",
		"'quarantine'",
		"'migration_quarantine'",
		"SET CONSTRAINTS ALL IMMEDIATE",
		"CREATE CONSTRAINT TRIGGER provider_operations_require_capacity_reservation",
		"AFTER INSERT ON provider_operations",
		"DEFERRABLE INITIALLY DEFERRED",
		"native provision operation requires an atomic managed runtime capacity reservation",
		"CREATE TRIGGER managed_runtime_capacity_execution_claim_guard",
		"operation_kind NOT IN ('provision', 'reconcile')",
		"operation_schema_version IS DISTINCT FROM 'techstack.provider-control-operation/v1'",
		"operation_execution_authority IS DISTINCT FROM 'techstack_provider_control'",
		"operation_profile_provider_id IS DISTINCT FROM operation_provider_id",
		"reservation.provider_id = operation_provider_id",
		"provider execution claim is blocked by missing native managed runtime capacity authority",
	} {
		if !strings.Contains(content, required) {
			t.Fatalf("managed runtime capacity quarantine/cutover missing %q", required)
		}
	}
}
