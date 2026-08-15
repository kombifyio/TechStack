package db

import (
	"os"
	"strings"
	"testing"
)

func TestOperatorAdoptedClaimCustodyMigrationStaysFailClosed(t *testing.T) {
	raw, err := os.ReadFile("migrations/059_operator_adopted_claim_custody.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(raw)
	for _, required := range []string{
		"provider_execution_claim_runtime_generation_guard()",
		"regexp_count(definition, predicate_pattern, 1, 'n') <> 2",
		"decision.outcome = 'adopted_exact_candidate'",
		"decision.result_receipt_sequence = source_operation.head_sequence",
		"decision.result_receipt_digest = source_operation.head_receipt_digest",
		"decision.selected_candidate_digest IS NOT NULL",
	} {
		if !strings.Contains(sql, required) {
			t.Fatalf("migration omitted %q", required)
		}
	}
	for _, forbidden := range []string{
		"DISABLE TRIGGER", "DROP TRIGGER", "DROP CONSTRAINT", "DISABLE ROW LEVEL SECURITY",
	} {
		if strings.Contains(sql, forbidden) {
			t.Fatalf("migration weakens custody with %q", forbidden)
		}
	}
}
