package main

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"time"

	"github.com/kombifyio/techstack/pkg/db"
)

// Ordinary preDeploy receives application/purpose credentials, never the
// migration owner. Validate the same exact grants and tenant fences used at
// preparation/startup; read the real authority owner instead of impersonating it.
// preDeploySchemaFloorWait bounds how long an activation waits for its own
// Delivery's database leg to apply the schema floor.
const preDeploySchemaFloorWait = 15 * time.Minute

func verifyProviderAndIssuerPreDeploy(ctx context.Context) error {
	if mode, err := advancedIssuerCustodyMode(); err != nil {
		return err
	} else if mode == advancedIssuerCustodyRemote {
		return verifyAdvancedIssuerRuntimeWithEvidence(ctx, map[string]any{"provider_custody": "execution-process", "application_provider_custody": false})
	}
	if os.Getenv(db.EnvMigrationDatabaseURL) != "" ||
		os.Getenv(db.EnvDatabaseMigrationMode) != "verify" || os.Getenv("TECHSTACK_ALLOW_DB_GENESIS") != "false" {
		return errors.New("provider preDeploy requires runtime verify-only custody without administrative credentials or genesis")
	}
	applicationConfig, err := db.ConfigFromEnv()
	if err != nil {
		return err
	}
	providerConfig, err := providerControlRuntimeDatabaseConfigFromEnv()
	if err != nil {
		return err
	}
	for _, dsn := range []string{applicationConfig.DSN, providerConfig.DSN} {
		u, err := url.Parse(dsn)
		if err != nil || len(u.Query()["sslmode"]) != 1 || u.Query().Get("sslmode") != "verify-full" {
			return errors.New("provider preDeploy requires verified transport for both declared runtime pools")
		}
	}
	application, err := db.OpenContext(ctx, applicationConfig)
	if err != nil {
		return err
	}
	defer application.Close()
	if err = db.VerifyExpectedDatabaseIdentity(ctx, application.DB); err != nil {
		return err
	}
	// The same Delivery's database leg applies this binary's schema floor.
	// Activation waits for that leg only, never for another environment.
	if err = application.WaitForSchemaFloor(ctx, preDeploySchemaFloorWait, 15*time.Second); err != nil {
		return err
	}
	provider, err := db.OpenContext(ctx, providerConfig)
	if err != nil {
		return err
	}
	defer provider.Close()
	if err = verifyProviderControlRuntimeRolePosture(ctx, provider.DB, application.DB); err != nil {
		return fmt.Errorf("provider preDeploy runtime authority: %w", err)
	}
	return verifyAdvancedIssuerRuntimeWithEvidence(ctx, map[string]any{
		"provider_role": providerControlRuntimeRoleName, "provider_privileges_verified": true, "tenant_fences_verified": true,
	})
}
