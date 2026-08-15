package db

import (
	"strings"
	"testing"
)

func TestRILGovernedActionCardsUseCanonicalInventoryAndDurableEvidence(t *testing.T) {
	content := readDBFile(t, "migrations/031_ril_governed_action_cards.sql")
	for _, required := range []string{
		"owner_subject_id text NOT NULL",
		"FOREIGN KEY (server_id) REFERENCES servers(id)",
		"execution_request_json jsonb",
		"evidence_json jsonb",
		"UNIQUE INDEX IF NOT EXISTS idx_ril_action_cards_idempotency",
		"'awaiting_approval'", "'approved'", "'denied'", "'executing'", "'completed'", "'failed'",
	} {
		if !strings.Contains(content, required) {
			t.Fatalf("migration missing %q", required)
		}
	}
	for _, forbidden := range []string{"provider_id", "credential", "secret", "raw_log", "command_output"} {
		if strings.Contains(strings.ToLower(content), forbidden) {
			t.Fatalf("governed action persistence exposes %q", forbidden)
		}
	}
}
