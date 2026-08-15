package db

import (
	"strings"
	"testing"
)

func TestRILSignalOutboxMigrationIsTenantScopedAndTokenFenced(t *testing.T) {
	content := readDBFile(t, "migrations/049_ril_signal_outbox.sql")
	for _, required := range []string{
		"FOREIGN KEY (tenant_id, server_id)",
		"UNIQUE (tenant_id, signal_id)",
		"UNIQUE (tenant_id, dedupe_key)",
		"ALTER TABLE ril_signal_outbox ENABLE ROW LEVEL SECURITY",
		"ALTER TABLE ril_signal_outbox FORCE ROW LEVEL SECURITY",
		"current_setting('app.tenant_id', true)",
		"octet_length(claim_token_digest) = 32",
		"trace_id text NOT NULL",
		"audit_id text NOT NULL",
	} {
		if !strings.Contains(content, required) {
			t.Fatalf("migration missing %q", required)
		}
	}
}
