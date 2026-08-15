package db

import (
	"strings"
	"testing"
)

func TestRILActionExecutionLedgerMigrationIsTenantIsolatedAndTokenFenced(t *testing.T) {
	content := readDBFile(t, "migrations/030_ril_action_execution_ledger.sql")
	for _, required := range []string{
		"CREATE TABLE IF NOT EXISTS ril_action_execution_ledger",
		"PRIMARY KEY (tenant_id, idempotency_key)",
		"UNIQUE (tenant_id, execution_id)",
		"CHECK (valid_until > requested_at)",
		"ENABLE ROW LEVEL SECURITY",
		"FORCE ROW LEVEL SECURITY",
		"current_setting('app.tenant_id', true)",
	} {
		if !strings.Contains(content, required) {
			t.Fatalf("migration missing %q", required)
		}
	}
	for _, forbidden := range []string{"provider_id", "credential", "transport", "callback_url"} {
		if strings.Contains(strings.ToLower(content), forbidden) {
			t.Fatalf("provider-free execution ledger contains %q", forbidden)
		}
	}
}
