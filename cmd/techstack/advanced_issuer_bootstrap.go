package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"strings"

	"github.com/kombifyio/techstack/pkg/auth"
	"github.com/kombifyio/techstack/pkg/db"
)

type advancedIssuerBootstrapConfig struct {
	application db.Config
	migration   db.Config
	issuerDSN   string
	appRole     string
	encryptor   *auth.SecretEncryptor
}

// Resolve every binding before migration or role changes. The migration
// credential belongs to this one-shot process; ordinary runtime uses verify
// mode with its own application credential.
func advancedIssuerBootstrapConfigFromEnv() (advancedIssuerBootstrapConfig, error) {
	var result advancedIssuerBootstrapConfig
	if strings.TrimSpace(os.Getenv(db.EnvMigrationDatabaseURL)) == "" {
		return result, errors.New("issuer bootstrap requires the separate migration custody binding")
	}
	app, err := db.ConfigFromEnv()
	if err != nil {
		return result, err
	}
	result.application = app
	result.encryptor, err = auth.NewSecretEncryptor([]byte(os.Getenv("TECHSTACK_ENCRYPTION_KEY")))
	if err != nil {
		return result, errors.New("issuer preparation requires the retained 32-byte wrapping key before effects")
	}
	result.migration, err = db.MigrationConfigFromEnv()
	if err != nil {
		return result, err
	}
	result.issuerDSN = strings.TrimSpace(os.Getenv(db.EnvAdvancedIssuerDatabaseURL))
	appURL, appErr := url.Parse(app.DSN)
	migrationURL, migrationErr := url.Parse(result.migration.DSN)
	issuerURL, issuerErr := url.Parse(result.issuerDSN)
	if appErr != nil || migrationErr != nil || issuerErr != nil || appURL.User == nil || migrationURL.User == nil || issuerURL.User == nil || issuerURL.User.Username() == "" {
		return result, errors.New("issuer bootstrap requires explicit application, migration and signing database URLs")
	}
	sameEndpoint := func(a, b *url.URL) bool {
		port := func(u *url.URL) string {
			if u.Port() == "" {
				return "5432"
			}
			return u.Port()
		}
		return strings.EqualFold(a.Hostname(), b.Hostname()) && port(a) == port(b) && a.Path == b.Path
	}
	if !sameEndpoint(appURL, migrationURL) || !sameEndpoint(appURL, issuerURL) || appURL.User.Username() == migrationURL.User.Username() || issuerURL.User.Username() == appURL.User.Username() || issuerURL.User.Username() == migrationURL.User.Username() {
		return result, errors.New("issuer bootstrap requires distinct custody principals on the same database")
	}
	if len(issuerURL.Query()["sslmode"]) != 1 || issuerURL.Query().Get("sslmode") != "verify-full" {
		return result, errors.New("issuer bootstrap signing transport must verify the server identity")
	}
	result.appRole = appURL.User.Username()
	return result, nil
}

func prepareAdvancedIssuerFromConfigWithIdentity(ctx context.Context, database *db.DB, cfg advancedIssuerBootstrapConfig) (string, string, error) {
	u, err := url.Parse(cfg.issuerDSN)
	if err != nil || u.User == nil {
		return "", "", errors.New("issuer bootstrap signing binding is invalid")
	}
	password, ok := u.User.Password()
	if !ok {
		return "", "", errors.New("issuer bootstrap signing credential is absent")
	}
	verifier, err := postgresSCRAMVerifier(password)
	if err != nil {
		return "", "", err
	}
	if err = db.PrepareAdvancedIssuerKeyCustody(ctx, database, cfg.issuerDSN, cfg.appRole, verifier); err != nil {
		return "", "", err
	}
	if os.Getenv("TECHSTACK_ADVANCED_ISSUER_PREPARATION_MODE") == "prepare" {
		return initializePreparedAdvancedIssuerIdentity(ctx, cfg.application, cfg.issuerDSN, cfg.encryptor)
	}
	return "", "", nil
}

func prepareAdvancedIssuerFromConfig(ctx context.Context, database *db.DB, cfg advancedIssuerBootstrapConfig) error {
	_, _, err := prepareAdvancedIssuerFromConfigWithIdentity(ctx, database, cfg)
	return err
}

// Self-managed installations use the same ordered preparation as the hosted
// deployment checkpoint, without composing provider execution.
func runAdvancedIssuerBootstrap(ctx context.Context) error {
	if expected := os.Getenv("TECHSTACK_EXPECTED_PREPARATION_SOURCE_SHA"); expected != "" && expected != buildRevision {
		return errors.New("credential preparation artifact differs from the admitted source")
	}
	if os.Getenv("TECHSTACK_ADVANCED_ISSUER_PREPARATION_MODE") == "verify" {
		return verifyAdvancedIssuerRuntime(ctx)
	}
	if os.Getenv("TECHSTACK_ADVANCED_ISSUER_PREPARATION_MODE") == "prepare-existing-roles" {
		return prepareExistingPurposeRoles(ctx)
	}
	if mode := os.Getenv("TECHSTACK_ADVANCED_ISSUER_PREPARATION_MODE"); mode != "" && mode != "prepare" {
		return errors.New("unknown advanced issuer preparation mode")
	}
	// This explicit one-shot command is preparation even for existing callers
	// that omit the mode. Ordinary runtime/provider entrypoints never select it.
	priorPreparationMode, hadPreparationMode := os.LookupEnv("TECHSTACK_ADVANCED_ISSUER_PREPARATION_MODE")
	_ = os.Setenv("TECHSTACK_ADVANCED_ISSUER_PREPARATION_MODE", "prepare")
	defer func() {
		if hadPreparationMode {
			_ = os.Setenv("TECHSTACK_ADVANCED_ISSUER_PREPARATION_MODE", priorPreparationMode)
		} else {
			_ = os.Unsetenv("TECHSTACK_ADVANCED_ISSUER_PREPARATION_MODE")
		}
	}()
	cfg, err := advancedIssuerBootstrapConfigFromEnv()
	if err != nil {
		return err
	}
	database, err := db.OpenContext(ctx, cfg.migration)
	if err != nil {
		return err
	}
	defer database.Close()
	if err = db.VerifyExpectedDatabaseIdentity(ctx, database.DB); err != nil {
		return err
	}
	priorMode, present := os.LookupEnv(db.EnvDatabaseMigrationMode)
	os.Setenv(db.EnvDatabaseMigrationMode, "apply")
	defer func() {
		if present {
			os.Setenv(db.EnvDatabaseMigrationMode, priorMode)
		} else {
			os.Unsetenv(db.EnvDatabaseMigrationMode)
		}
	}()
	if err = database.Migrate(ctx); err != nil {
		return err
	}
	if err = prepareApplicationPrincipal(ctx, database, cfg); err != nil {
		return err
	}
	if err = prepareAdvancedIssuerFromConfig(ctx, database, cfg); err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]any{"schema": "techstack.credential-preparation/v1", "operation": "prepare", "status": "acknowledged", "source_sha": buildRevision})
}
