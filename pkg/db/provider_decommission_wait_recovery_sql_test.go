package db

import (
	"strings"
	"testing"
)

func TestProviderDecommissionWaitRecoveryMigrationKeepsTenantDiscoveryBounded(t *testing.T) {
	content := readDBFile(t, "migrations/071_provider_decommission_wait_recovery.sql")
	for _, required := range []string{
		"CREATE TABLE IF NOT EXISTS provider_decommission_wait_tenants",
		"recovery_destroy_count integer NOT NULL",
		"REVOKE ALL ON TABLE provider_decommission_wait_tenants FROM PUBLIC",
		"CREATE OR REPLACE FUNCTION provider_control_refresh_decommission_wait_tenant()",
		"current_setting('app.tenant_id', true)",
		"provider decommission wait refresh requires the exact tenant scope",
		"providercontrol.decommission-wait-tenant/v1:",
		"job.state IN ('pending', 'running')",
		"job.type = 'destroy'",
		"job.result_json->'managed_provider_decommission_recovery'->>'schema' =",
		"'techstack.managed-provider-decommission-recovery/v1'",
		"job.result_json->'managed_provider_decommission_recovery'->>'tenant_id' = job.tenant_id",
		"job.result_json->'managed_provider_decommission_recovery'->>'stack_id' = job.stack_id",
		"WHEN 'running' THEN job.updated_at + interval '3 seconds'",
		"CREATE TRIGGER jobs_refresh_provider_decommission_wait_tenant",
		"CREATE OR REPLACE FUNCTION provider_control_list_due_decommission_wait_tenants(",
		"requested_limit IS NULL OR requested_limit < 1 OR requested_limit > 101",
		"directory.earliest_resume_at <= clock_timestamp()",
		"ORDER BY directory.tenant_id ASC",
		"ALTER FUNCTION %I.%I%s SET search_path TO pg_catalog, %I, pg_temp",
		"REVOKE ALL ON FUNCTION %I.%I%s FROM PUBLIC",
	} {
		if !strings.Contains(content, required) {
			t.Errorf("provider decommission wait recovery migration missing %q", required)
		}
	}
}

func TestProviderDecommissionWaitRecoveryMigrationBackfillsExactLegacyWaitMarker(t *testing.T) {
	content := readDBFile(t, "migrations/071_provider_decommission_wait_recovery.sql")
	for _, required := range []string{
		"UPDATE jobs",
		"'managed_provider_decommission_recovery'",
		"'tenant_id', tenant_id",
		"'stack_id', stack_id",
		"WHERE state IN ('pending', 'running')",
		"AND type = 'destroy'",
		"result_json->'job_wait'->>'state' = 'waiting'",
		"result_json->'job_wait'->>'reason' = 'waiting_provider_decommission'",
		"AND stack_id IS NOT NULL",
	} {
		if !strings.Contains(content, required) {
			t.Errorf("provider decommission wait legacy backfill missing %q", required)
		}
	}
}

func TestProviderDecommissionWaitRecoveryMigrationDoesNotOvergrantRuntimeRole(t *testing.T) {
	content := strings.ToLower(readDBFile(t, "migrations/071_provider_decommission_wait_recovery.sql"))
	for _, forbidden := range []string{
		"create role",
		"alter role",
		"password",
		"grant select on jobs",
		"grant select on provider_decommission_wait_tenants",
		"grant execute on function",
	} {
		if strings.Contains(content, forbidden) {
			t.Errorf("provider decommission wait recovery migration overgrants via %q", forbidden)
		}
	}
}
