package main

import (
	"context"
	"errors"
	"testing"

	"github.com/kombifyio/techstack/pkg/db"
)

func TestDatabaseDeliveryRefusesUnadmittedEffects(t *testing.T) {
	t.Setenv(db.EnvAllowDBGenesis, "false")
	t.Setenv(db.EnvDatabaseMigrationMode, "apply")
	for _, channel := range []string{"preview", "stable"} {
		t.Setenv(db.EnvReleaseChannel, channel)
		if err := runDatabaseDelivery(context.Background(), []string{"apply"}); !errors.Is(err, errDatabaseDeliveryAdmission) {
			t.Fatalf("unadmitted migration reached credential resolution: %v", err)
		}
	}
}
