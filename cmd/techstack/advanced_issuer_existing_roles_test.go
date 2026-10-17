package main

import (
	"context"
	"errors"
	"testing"

	"github.com/kombifyio/techstack/pkg/db"
)

func TestExistingRolePreparationRefusesSchemaOrGenesisAuthority(t *testing.T) {
	for _, policy := range []struct{ migration, genesis string }{{"apply", "false"}, {"verify", "true"}} {
		t.Setenv(db.EnvDatabaseMigrationMode, policy.migration)
		t.Setenv(db.EnvAllowDBGenesis, policy.genesis)
		err := prepareExistingPurposeRoles(context.Background())
		if !errors.Is(err, errExistingRolePreparationPolicy) {
			t.Fatalf("schema or genesis authority reached credential resolution: %v", err)
		}
	}
}
