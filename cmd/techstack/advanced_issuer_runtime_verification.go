package main

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"strings"

	"github.com/kombifyio/techstack/internal/advancedissuer"
	"github.com/kombifyio/techstack/pkg/auth"
	"github.com/kombifyio/techstack/pkg/db"

	wire "github.com/kombifyio/techstack/internal/selfhostcontracts/advancedcapability"
)

// The normal producer verifies through the application's native database
// interface. This process never receives a migration/admin credential and
// refuses genesis before opening either existing runtime pool.
func verifyAdvancedIssuerRuntime(ctx context.Context) error {
	return verifyAdvancedIssuerRuntimeWithEvidence(ctx, nil)
}

func verifyAdvancedIssuerRuntimeWithEvidence(ctx context.Context, evidence map[string]any) error {
	if os.Getenv(db.EnvMigrationDatabaseURL) != "" ||
		os.Getenv(db.EnvDatabaseMigrationMode) != "verify" || os.Getenv("TECHSTACK_ALLOW_DB_GENESIS") != "false" ||
		strings.TrimSpace(os.Getenv("TECHSTACK_EXPECTED_DATABASE_IDENTITY")) == "" || len(os.Getenv("TECHSTACK_ENCRYPTION_KEY")) != 32 {
		return errors.New("runtime verification requires physical identity, preserved wrapping custody, verify mode and no administrative credential or genesis")
	}
	cfg, err := db.ConfigFromEnv()
	if err != nil {
		return err
	}
	appURL, err := url.Parse(cfg.DSN)
	if err != nil || appURL.User == nil || len(appURL.Query()["sslmode"]) != 1 || appURL.Query().Get("sslmode") != "verify-full" {
		return errors.New("application verification requires its declared login and verified transport")
	}
	application, err := db.OpenContext(ctx, cfg)
	if err != nil {
		return err
	}
	defer application.Close()
	if err = db.VerifyExpectedDatabaseIdentity(ctx, application.DB); err != nil {
		return err
	}
	var login string
	var privileged, keyRead, elevatedMembership bool
	if err = application.QueryRowContext(ctx, `SELECT current_user, rolsuper OR rolcreatedb OR rolcreaterole OR rolreplication OR rolbypassrls
 OR oid=(SELECT datdba FROM pg_catalog.pg_database WHERE datname=current_database())
 OR has_database_privilege(current_user,current_database(),'CREATE') OR has_schema_privilege(current_user,'public','CREATE'),
 has_table_privilege(current_user, 'public.advanced_issuer_keys', 'SELECT'),
 EXISTS(SELECT 1 FROM pg_catalog.pg_roles r WHERE r.rolname<>current_user AND pg_has_role(current_user,r.oid,'MEMBER')
 AND (r.rolsuper OR r.rolcreatedb OR r.rolcreaterole OR r.rolreplication OR r.rolbypassrls
 OR r.oid=(SELECT datdba FROM pg_catalog.pg_database WHERE datname=current_database())
 OR has_database_privilege(r.oid,current_database(),'CREATE') OR has_schema_privilege(r.oid,'public','CREATE')
 OR has_table_privilege(r.oid,'public.advanced_issuer_keys','SELECT')))
 FROM pg_catalog.pg_roles WHERE rolname=current_user`).Scan(&login, &privileged, &keyRead, &elevatedMembership); err != nil {
		return err
	}
	if login != appURL.User.Username() || privileged || keyRead || elevatedMembership {
		return errors.New("application login carries administrative or signing authority")
	}
	if err = application.Migrate(ctx); err != nil { // verify mode only
		return err
	}
	// Authenticate the signing role and prove that a key already exists before
	// OpenBound: the verifier cannot initialize a new installation identity.
	issuerDSN := os.Getenv(db.EnvAdvancedIssuerDatabaseURL)
	issuerURL, err := url.Parse(issuerDSN)
	port := func(u *url.URL) string {
		if u.Port() == "" {
			return "5432"
		}
		return u.Port()
	}
	if err != nil || issuerURL.User == nil || len(issuerURL.Query()["sslmode"]) != 1 || issuerURL.Query().Get("sslmode") != "verify-full" ||
		!strings.EqualFold(issuerURL.Hostname(), appURL.Hostname()) || port(issuerURL) != port(appURL) || issuerURL.Path != appURL.Path {
		return errors.New("signing login binding is absent")
	}
	signingConfig := db.DefaultConfig()
	signingConfig.Backend, signingConfig.DriverName, signingConfig.DSN = db.StoreBackendPostgres, db.PostgresDriverName, issuerDSN
	owner, err := db.ApplicationAuthorityOwner(ctx, application.DB)
	if err != nil {
		return err
	}
	if err = db.VerifyApplicationPrivileges(ctx, application.DB, db.ApplicationPrincipal{Role: login, AuthorityOwner: owner}, true); err != nil {
		return err
	}
	signing, err := db.OpenContext(ctx, signingConfig)
	if err != nil {
		return err
	}
	defer signing.Close()
	if err = db.VerifyExpectedDatabaseIdentity(ctx, signing.DB); err != nil {
		return err
	}
	if err = db.VerifyAdvancedIssuerRuntimePrivileges(ctx, signing.DB, issuerURL.User.Username(), login); err != nil {
		return err
	}
	store, err := advancedissuer.NewPostgresStoreWithKeyCustody(application, signing, auth.GetEncryptor())
	if err != nil {
		return err
	}
	issuerID, key, err := store.LoadExistingKey(ctx)
	if err != nil {
		return errors.New("governed installation signing preparation or retained wrapping custody is unavailable")
	}
	keyID := wire.KeyID(key.Public().(ed25519.PublicKey))
	receipt := map[string]any{
		"schema": "techstack.credential-preparation/v1", "operation": "verify", "status": "acknowledged", "source_sha": buildRevision,
		"application_role": login, "signing_role": issuerURL.User.Username(), "database_identity": os.Getenv("TECHSTACK_EXPECTED_DATABASE_IDENTITY"),
		"service_id":        "",
		"application_admin": false, "application_key_read": false, "genesis": false, "migration_mode": "verify", "issuer_id": issuerID, "key_id": keyID,
	}
	for key, value := range evidence {
		receipt[key] = value
	}
	return json.NewEncoder(os.Stdout).Encode(receipt)
}
