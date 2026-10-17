package db

import (
	"context"
	"testing"
)

// Sensitive boundary (migrations): the Nightly channel shares the live
// database and must never apply schema changes or genesis.
func TestPreviewChannelRefusesToMigrate(t *testing.T) {
	t.Setenv(EnvReleaseChannel, "preview")
	for _, mode := range []string{"", "apply"} {
		t.Setenv(EnvDatabaseMigrationMode, mode)
		if err := (&DB{}).Migrate(context.Background()); err == nil {
			t.Fatalf("preview with migration mode %q migrated", mode)
		}
	}
	t.Setenv(EnvDatabaseMigrationMode, "verify")
	t.Setenv(EnvAllowDBGenesis, "true")
	if err := (&DB{}).Migrate(context.Background()); err == nil {
		t.Fatal("preview with genesis opt-in migrated")
	}
}
