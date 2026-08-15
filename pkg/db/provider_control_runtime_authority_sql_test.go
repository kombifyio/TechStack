package db

import (
	"strings"
	"testing"
)

func TestProviderControlRuntimeAuthorityMigrationIsBounded(t *testing.T) {
	content := readDBFile(t, "migrations/034_provider_control_runtime_authority.sql")
	for _, required := range []string{
		"CREATE TABLE IF NOT EXISTS provider_control_runnable_tenants",
		"runnable_operation_count integer NOT NULL",
		"REVOKE ALL ON TABLE provider_control_runnable_tenants FROM PUBLIC",
		"CREATE OR REPLACE FUNCTION provider_control_refresh_runnable_tenant()",
		"pg_advisory_xact_lock",
		"providercontrol.runnable-tenant/v1:",
		"current_setting('app.tenant_id', true)",
		"provider-control runnable tenant refresh requires the exact tenant scope",
		"AFTER INSERT OR UPDATE OR DELETE ON provider_operations",
		"AFTER INSERT OR UPDATE OR DELETE ON provider_provision_dispatch_guards",
		"CREATE OR REPLACE FUNCTION provider_control_list_runnable_tenants(",
		"requested_limit IS NULL OR requested_limit < 1 OR requested_limit > 101",
		"ORDER BY directory.tenant_id ASC",
		"CREATE OR REPLACE FUNCTION provider_control_runtime_authority()",
		"pg_catalog.pg_control_system()",
		"control.system_identifier::text",
		"provider_execution_immutable_update",
		"provider_provision_dispatch_guard_validate_insert",
		"provider_execution_claim_current_head",
		"provider_execution_claim_credential_guard",
		"SET search_path TO pg_catalog, %I, pg_temp",
		"REVOKE ALL ON FUNCTION %I.%I%s FROM PUBLIC",
	} {
		if !strings.Contains(content, required) {
			t.Errorf("provider-control runtime authority migration missing %q", required)
		}
	}
}

func TestProviderControlRuntimeAuthorityDoesNotGrantTheRuntimeRole(t *testing.T) {
	content := strings.ToLower(readDBFile(t, "migrations/034_provider_control_runtime_authority.sql"))
	for _, forbidden := range []string{
		"create role",
		"alter role",
		"password",
		"grant select on provider_operations",
		"grant insert on provider_operations",
		"grant update on provider_operations",
		"grant execute on function provider_execution_immutable_update",
		"from public;\n\ngrant",
	} {
		if strings.Contains(content, forbidden) {
			t.Errorf("migration embeds deployment role authority via %q", forbidden)
		}
	}
}

func TestProviderControlRunnableDirectoryUsesSamePredicateForBackfillAndRefresh(t *testing.T) {
	content := readDBFile(t, "migrations/034_provider_control_runtime_authority.sql")
	for _, predicate := range []string{
		"operation.command_json->>'schema_version' = 'techstack.provider-control-operation/v1'",
		"operation.command_json->>'execution_authority' = 'techstack_provider_control'",
		"operation.provision_dispatch_mode <> 'blocked'",
		"operation.status = 'pending'",
		"operation.phase NOT IN ('planned', 'present', 'absent', 'failed', 'denied')",
		"dispatch_guard.dispatch_mode = 'at_most_once_dispatch_manual_reconcile'",
		"dispatch_guard.guard_origin = 'migration_quarantine'",
	} {
		if count := strings.Count(content, predicate); count != 2 {
			t.Errorf("runnable predicate %q appears %d times, want refresh and backfill", predicate, count)
		}
	}
}
