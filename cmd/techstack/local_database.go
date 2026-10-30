package main

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/kombifyio/techstack/internal/localdb"
	"github.com/kombifyio/techstack/pkg/config"
	"github.com/kombifyio/techstack/pkg/db"
)

// Explicit native operator commands never start HTTP or pretend to hold a
// company database-delivery identity. They use the customer's actual storage.
func runLocalDatabase(ctx context.Context, args []string) (result error) {
	cfg, err := config.Load("")
	if err != nil {
		return err
	}
	if hostedRuntime(cfg) || !cfg.DeploymentMode.IsSelfHosted() || os.Getenv("TECHSTACK_DATABASE_CONTEXT") == "stage-development" {
		return errors.New("local-database requires the customer's self-hosted installation")
	}
	if len(args) == 3 && args[0] == "adopt" && args[1] == "--identity" {
		if os.Getenv(db.EnvDatabaseURL) != "" || !localdb.EnabledFromEnv() {
			return errors.New("legacy adoption requires embedded mode and no external DATABASE_URL")
		}
		pg, err := localdb.StartEmbeddedPostgresWithAdoption(cfg.Server.DataDir, args[2])
		if pg != nil {
			defer func() { result = errors.Join(result, pg.Stop()) }()
		}
		if err != nil {
			return err
		}
		fmt.Println("Native operator custody adopted and physical identity verified; existing data retained. Start the installation normally.")
		return nil
	}
	if len(args) == 1 && args[0] == "prepare" {
		pin, pinned, err := db.ExpectedDatabaseIdentityFromEnv()
		if err != nil || !pinned || pin.SystemIdentifier == "" {
			return errors.New("native preparation requires TECHSTACK_EXPECTED_DATABASE_IDENTITY=database@system_identifier")
		}
		if os.Getenv(db.EnvDatabaseMigrationMode) != "apply" {
			return errors.New("offline native preparation requires TECHSTACK_DATABASE_MIGRATION_MODE=apply")
		}
		if err := localdb.PrepareApplication(ctx, os.Getenv("TECHSTACK_LOCAL_DATABASE_OPERATOR_URL"), os.Getenv(db.EnvMigrationDatabaseURL), os.Getenv(db.EnvDatabaseURL), pin); err != nil {
			return err
		}
		fmt.Println("Native application capabilities and migration ledger verified. Serve with application custody only and verify mode.")
		return nil
	}
	return errors.New("usage: techstack local-database adopt --identity <database@system_identifier> | prepare")
}
