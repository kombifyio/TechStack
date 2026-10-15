package main

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/kombifyio/techstack/pkg/db"
)

// Administrative access must never fall through to a production/default DSN
// when the separately assigned test runtime is missing. These are real CLI
// pre-connection denials, with no store, SQL or identity fixture.
func TestManagedTestBootstrapRequiresAssignmentBeforeConnection(t *testing.T) {
	t.Setenv(db.EnvManagedTestAssignment, filepath.Join(t.TempDir(), "missing-assignment.json"))
	for _, operation := range []string{"inspect", "prepare", "migrate", "admit", "verify", "ril-scope", "ril-scope-cancel", "ril-scope-recover"} {
		if err := runManagedTestBootstrap(context.Background(), []string{operation}); err == nil {
			t.Fatalf("%s admitted an unassigned administrative operation", operation)
		}
	}
	if err := runManagedTestBootstrap(context.Background(), []string{"--sql", "SELECT 1"}); err == nil {
		t.Fatal("arbitrary SQL operation admitted")
	}
}

// Existing privileged login custody cannot be borrowed, rotated or guessed.
// This executes the actual SCRAM primitives, without a database fixture.
func TestManagedTestStoredSCRAMRejectsUnmatchedCustody(t *testing.T) {
	verifier, err := postgresSCRAMVerifier("assigned-shared-test-custody-secret")
	if err != nil {
		t.Fatal(err)
	}
	if !db.PostgresSCRAMSecretMatches("assigned-shared-test-custody-secret", verifier) {
		t.Fatal("stored custody could not be proved")
	}
	if db.PostgresSCRAMSecretMatches("another-role-secret", verifier) || db.PostgresSCRAMSecretMatches("assigned-shared-test-custody-secret", "unsupported-custody") {
		t.Fatal("unmatched custody was adopted")
	}
}
