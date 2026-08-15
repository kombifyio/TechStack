package db

import (
	"strings"
	"testing"
)

func TestProviderProvisionWaitRecoveryMigrationUsesOperationReferenceProjection(t *testing.T) {
	content := readDBFile(t, "migrations/084_provider_provision_wait_recovery.sql")
	for _, required := range []string{
		"CREATE TABLE IF NOT EXISTS provider_provision_wait_recovery",
		"PRIMARY KEY (tenant_id, operation_id, job_id)",
		"REVOKE ALL ON TABLE provider_provision_wait_recovery FROM PUBLIC",
		"idx_jobs_provider_provision_wait_operation",
		"CREATE OR REPLACE FUNCTION provider_control_refresh_provider_provision_wait()",
		"current_setting('app.tenant_id', true)",
		"CREATE TRIGGER jobs_refresh_provider_provision_wait_recovery",
		"CREATE OR REPLACE FUNCTION provider_control_list_provider_provision_waits(",
		"RETURNS TABLE (tenant_id text, operation_id text)",
		"(directory.tenant_id, directory.operation_id) >",
		"requested_limit IS NULL OR requested_limit < 1 OR requested_limit > 101",
		"ALTER FUNCTION %I.%I%s SET search_path TO pg_catalog, %I, pg_temp",
		"REVOKE ALL ON FUNCTION %I.%I%s FROM PUBLIC",
	} {
		if !strings.Contains(content, required) {
			t.Errorf("provider provision wait recovery migration missing %q", required)
		}
	}
}

func TestProviderProvisionWaitRecoveryMigrationDoesNotGrantRuntimeTableAccess(t *testing.T) {
	content := strings.ToLower(readDBFile(t, "migrations/084_provider_provision_wait_recovery.sql"))
	for _, forbidden := range []string{
		"grant select on provider_provision_wait_recovery",
		"grant select on jobs",
		"grant execute on function",
		"create role",
		"alter role",
		"password",
	} {
		if strings.Contains(content, forbidden) {
			t.Errorf("provider provision wait recovery migration overgrants via %q", forbidden)
		}
	}
}
