package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"regexp"
	"strconv"

	"github.com/kombifyio/techstack/pkg/db"
)

// runContractMigrations is the post-activation contract step of the
// production database leg (D-2026-10-27-06, platform-0bd32). The Delivery
// adapter reads every live revision's health schema.embedded_max first and
// passes the lowest as TECHSTACK_CONTRACT_LIVE_EMBEDDED_MIN; a contract step
// numbered above it is held, never forced, and the next deploy retries.
func runContractMigrations(ctx context.Context) error {
	live, err := strconv.Atoi(os.Getenv("TECHSTACK_CONTRACT_LIVE_EMBEDDED_MIN"))
	if err != nil || live < 0 ||
		os.Getenv(db.EnvAllowDBGenesis) != "false" || db.ReleaseChannel() != "stable" ||
		os.Getenv("DELIVERY_OPERATION") != "smoke" || os.Getenv("DELIVERY_ARTIFACT") != "database-schema" ||
		os.Getenv("SOURCE_REPOSITORY") != "kombify-Techstack" || os.Getenv("SOURCE_SHA") != buildRevision ||
		!regexp.MustCompile(`^[a-f0-9]{40}$`).MatchString(buildRevision) ||
		os.Getenv("DELIVERY_DATABASE_ADMITTED") != "true" || os.Getenv(db.EnvMigrationDatabaseURL) == "" {
		return errDatabaseDeliveryAdmission
	}
	host, err := db.DeliveryForwardDatabaseHost()
	if err != nil {
		return err
	}
	cfg, err := db.MigrationConfigFromEnv()
	if err != nil {
		return err
	}
	if uri, err := url.Parse(cfg.DSN); err != nil || uri.Hostname() != host || uri.Query().Get("sslmode") != "verify-full" {
		return errors.New("contract migration target or transport differs")
	}
	authority, err := db.OpenContext(ctx, cfg)
	if err != nil {
		return err
	}
	defer authority.Close()
	if err := db.VerifyExpectedDatabaseIdentity(ctx, authority.DB); err != nil {
		return err
	}
	result, err := authority.ApplyContractMigrations(ctx, live)
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]any{
		"schema": "techstack.database-contract/v1", "operation": "contract", "source_sha": buildRevision,
		"status": "acknowledged", "database_effects": len(result.Applied) > 0, "contract": result,
		"database_identity": os.Getenv(db.EnvExpectedDatabaseIdentity),
	})
}
