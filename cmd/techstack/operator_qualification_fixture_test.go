package main

import (
	"errors"
	"testing"

	"github.com/kombifyio/techstack/pkg/db"
)

// The old helper could create a fictional owner on a local database. Execute
// its real CLI handler: an unassigned call must fail before any DSN is opened.
// No local database, memory store, token or identity fixture is substituted.
func TestOperatorQualificationRequiresManagedAssignmentBeforeConnection(t *testing.T) {
	t.Setenv(operatorQualificationFixtureGate, "1")
	t.Setenv("TECHSTACK_ENV", "local")
	t.Setenv("TECHSTACK_V2_DEFAULT_TENANT_ID", "unassigned")
	t.Setenv(db.EnvManagedTestAssignment, "")
	t.Setenv("TECHSTACK_MANAGED_TEST_ID_TOKEN", "")
	t.Setenv("DATABASE_URL", "invalid-unassigned-connection")
	err := runOperatorQualificationFixture(t.Context(), []string{"seed-standalone-cloud", "a3d1b5c7-3344-4a01-a943-066d5dcd0f3a", "unassigned", "ionos", "de-fra"})
	if !errors.Is(err, errOperatorQualificationPrerequisite) {
		t.Fatalf("unassigned qualification did not refuse its prerequisites: %v", err)
	}
}
