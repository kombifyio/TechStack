package db

import (
	"regexp"
	"strings"
	"testing"
)

func TestProviderReceiptRuntimeServerProjectionMigrationIsGenerationBoundAndStoppedLane(t *testing.T) {
	content := readDBFile(t, "migrations/035_provider_receipt_runtime_server_projection.sql")
	for _, required := range []string{
		"provider receipt cutover requires external egress disablement and all unsettled side-effecting provider claims to be recovered or manually settled",
		"claim.state IN ('active', 'released')",
		"operation.head_sequence = claim.head_sequence",
		"decision.outcome = 'no_candidate_observed'",
		"ALTER TABLE provider_operation_execution_claims DISABLE ROW LEVEL SECURITY",
		"ALTER TABLE provider_operation_execution_claims FORCE ROW LEVEL SECURITY",
		"ALTER TABLE provider_provision_discovery_observations DISABLE ROW LEVEL SECURITY",
		"ALTER TABLE provider_provision_discovery_observations FORCE ROW LEVEL SECURITY",
		"ALTER TABLE managed_runtime_capacity_reservations DISABLE ROW LEVEL SECURITY",
		"ALTER TABLE managed_runtime_capacity_reservations FORCE ROW LEVEL SECURITY",
		"ALTER TABLE provider_operation_resources DISABLE ROW LEVEL SECURITY",
		"ALTER TABLE provider_operation_resources FORCE ROW LEVEL SECURITY",
		"observation.collected_at >= CASE claim.state",
		"provider execution authorization requires read committed serialization",
		"provider receipt cutover found a native lease without an exact IONOS/Centron RuntimeServer provider binding",
		"provider receipt cutover found held zero-resource failed provision custody without an exact runtime server generation pin; operator reconciliation is required",
		"SET provider_ref = LOWER(BTRIM(lease.provider_id))",
		"server.lease_id = lease.id",
		"ADD COLUMN IF NOT EXISTS resource_generation_id uuid",
		"DISABLE TRIGGER server_provider_resource_bindings_reject_update",
		"ALTER TABLE provider_operations DISABLE ROW LEVEL SECURITY",
		"ALTER TABLE provider_operations FORCE ROW LEVEL SECURITY",
		"ALTER COLUMN resource_generation_id SET NOT NULL",
		"runtime_server_generation",
		"server.provider_ref",
		"provider execution claim is stale against the runtime generation",
		"managed_runtime_capacity_release_facts",
		"release.resource_generation_id = reservation.resource_generation_id",
		"server_provider_resource_binding_guard",
		"provider_operation_head_update_guard",
		"provider_execution_claim_runtime_generation_guard",
		"provider_control_lock_runtime_lease_projection",
		"provider_control_count_unsettled_generation_dispatch_guards",
		"FOR SHARE OF lease",
		"provider_resource_free_terminalization_validate_commit",
		"zero-resource terminal failure",
		"terminal failure may only certify zero candidates",
		"DEFERRABLE INITIALLY DEFERRED",
		"provider_provision_resolution_decisions_refresh_runnable",
		"operation_kind IN ('reconcile', 'decommission')",
		"source_operation.operation = 'provision'",
		"provider mutation claim targets do not exactly match prior provision custody",
	} {
		if !strings.Contains(content, required) {
			t.Fatalf("provider receipt RuntimeServer projection migration missing %q", required)
		}
	}

	for _, forbidden := range []string{
		"legacy_simulate",
		"simulate_url",
		"provisioning-executor/v1",
		"health_state =",
		"connection_state =",
		"inventory_revision =",
		"ON DELETE CASCADE",
		"source_operation.operation IN ('provision', 'reconcile')",
	} {
		if strings.Contains(strings.ToLower(content), strings.ToLower(forbidden)) {
			t.Fatalf("provider receipt projection introduced forbidden authority via %q", forbidden)
		}
	}
}

func TestProviderReceiptRuntimeServerProjectionHardensEveryPrivilegedBoundary(t *testing.T) {
	content := readDBFile(t, "migrations/035_provider_receipt_runtime_server_projection.sql")
	for _, required := range []string{
		"DO $provider_control_secure_functions$",
		"'provider_execution_immutable_update'",
		"'provider_provision_dispatch_guard_validate_insert'",
		"'provider_provision_discovery_active_claim_guard'",
		"'provider_provision_resolution_active_claim_guard'",
		"'provider_execution_claim_current_head'",
		"'provider_execution_claim_credential_guard'",
		"'provider_operation_runtime_generation_guard'",
		"'provider_operation_head_update_guard'",
		"'provider_execution_claim_runtime_generation_guard'",
		"'provider_resource_free_terminalization_validate_insert'",
		"'provider_resource_free_terminalization_validate_commit'",
		"REVOKE ALL ON FUNCTION provider_resource_free_terminalization_reject_mutation() FROM PUBLIC",
		"'managed_runtime_capacity_release_validate_insert'",
		"'provider_control_refresh_runnable_tenant'",
		"'provider_control_lock_runtime_lease_projection'",
		"'provider_control_count_unsettled_generation_dispatch_guards'",
		"'provider_control_list_runnable_tenants'",
		"'provider_control_runtime_authority'",
		"ALTER FUNCTION %I.%I%s SECURITY DEFINER",
		"SET search_path TO pg_catalog, %I, pg_temp",
		"REVOKE ALL ON FUNCTION %I.%I%s FROM PUBLIC",
	} {
		if !strings.Contains(content, required) {
			t.Fatalf("provider receipt projection function hardening missing %q", required)
		}
	}
}

func TestProviderReceiptRuntimeServerProjectionFunctionNamesFitPostgresLimit(t *testing.T) {
	content := readDBFile(t, "migrations/035_provider_receipt_runtime_server_projection.sql")
	matches := regexp.MustCompile(
		`(?m)CREATE OR REPLACE FUNCTION\s+([a-zA-Z0-9_]+)\s*\(`,
	).FindAllStringSubmatch(content, -1)
	if len(matches) == 0 {
		t.Fatal("provider receipt projection migration contains no function declarations")
	}
	for _, match := range matches {
		if len(match[1]) > 63 {
			t.Fatalf(
				"PostgreSQL function identifier %q is %d bytes, want at most 63",
				match[1],
				len(match[1]),
			)
		}
	}
}
