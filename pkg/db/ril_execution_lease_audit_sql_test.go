package db

import (
	"strings"
	"testing"
)

func TestRILExecutionLeaseAuditMigrationIsTenantScopedAndAppendOnly(t *testing.T) {
	content := strings.ToLower(readDBFile(t, "migrations/048_ril_execution_lease_audit.sql"))
	for _, required := range []string{
		"create table if not exists ril_action_transition_audit",
		"create table if not exists ril_execution_lease_audit",
		"audit_correlation_id text not null",
		"enable row level security",
		"force row level security",
		"create policy tenant_isolation",
		"before update or delete on ril_action_transition_audit",
		"before update or delete on ril_execution_lease_audit",
		"raise exception 'ril audit rows are append-only'",
	} {
		if !strings.Contains(content, required) {
			t.Fatalf("migration omitted %q", required)
		}
	}
}
