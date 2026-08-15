package db

import (
	"strings"
	"testing"
)

func TestStackBackupStoresAreEncryptedAndTenantIsolated(t *testing.T) {
	content := readDBFile(t, "migrations/052_stack_backup_stores.sql")
	for _, required := range []string{
		"CREATE TABLE IF NOT EXISTS stack_backup_stores",
		"PRIMARY KEY (tenant_id, stack_id)",
		"REFERENCES stacks (tenant_id, id)",
		"secret_access_key_enc LIKE 'enc:v1:%'",
		"kopia_repo_password_enc LIKE 'enc:v1:%'",
		"ENABLE ROW LEVEL SECURITY",
		"FORCE ROW LEVEL SECURITY",
		"current_setting('app.tenant_id', true)",
	} {
		if !strings.Contains(content, required) {
			t.Fatalf("backup custody migration missing %q", required)
		}
	}
}

func TestStackBackupStoreAttestationIsAnAtomicDigestTimestampPair(t *testing.T) {
	content := readDBFile(t, "migrations/053_stack_backup_store_attestation.sql")
	for _, required := range []string{"custody_attestation_evidence", "attested_at", "stack_backup_stores_attestation_pair", "^sha256:[0-9a-f]{64}$"} {
		if !strings.Contains(content, required) {
			t.Fatalf("backup attestation migration missing %q", required)
		}
	}
}
