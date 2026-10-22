package main

import (
	"context"
	"crypto/ed25519"
	"database/sql"
	"errors"
	"os"

	"github.com/kombifyio/techstack/internal/advancedissuer"
	"github.com/kombifyio/techstack/pkg/auth"
	"github.com/kombifyio/techstack/pkg/db"
	"github.com/jackc/pgx/v5"

	wire "github.com/kombifyio/techstack/internal/selfhostcontracts/advancedcapability"
)

// Key generation belongs to explicit governed preparation. Normal runtime
// loads retained identity only. The central store converges concurrent first
// preparations and never replaces an existing key or its wrapping custody.
func initializePreparedAdvancedIssuerIdentity(ctx context.Context, applicationConfig db.Config, signingDSN string, encryptor *auth.SecretEncryptor) (string, string, error) {
	if os.Getenv("TECHSTACK_ADVANCED_ISSUER_PREPARATION_MODE") != "prepare" || encryptor == nil || os.Getenv(advancedissuer.EnvIssuerKey) != "" {
		return "", "", errors.New("installation identity initialization requires explicit preparation and retained wrapping custody")
	}
	application, err := db.OpenContext(ctx, applicationConfig)
	if err != nil {
		return "", "", err
	}
	defer application.Close()
	if err = db.VerifyExpectedDatabaseIdentity(ctx, application.DB); err != nil {
		return "", "", err
	}
	if err = application.VerifyMigrations(ctx); err != nil {
		return "", "", err
	}
	signingConfig := db.DefaultConfig()
	signingConfig.Backend, signingConfig.DriverName, signingConfig.DSN = db.StoreBackendPostgres, db.PostgresDriverName, signingDSN
	signing, err := db.OpenContext(ctx, signingConfig)
	if err != nil {
		return "", "", err
	}
	defer signing.Close()
	if err = db.VerifyExpectedDatabaseIdentity(ctx, signing.DB); err != nil {
		return "", "", err
	}
	store, err := advancedissuer.NewPostgresStoreWithKeyCustody(application, signing, encryptor)
	if err != nil {
		return "", "", err
	}
	issuerID, key, err := store.LoadExistingKey(ctx)
	if err == nil {
		return issuerID, wire.KeyID(key.Public().(ed25519.PublicKey)), nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return "", "", errors.New("existing installation identity or wrapping custody cannot be verified; no key change")
	}
	if !installationIssuerGenesisAuthorized() {
		return "", "", errors.New("retained installation identity is absent; no implicit signing key issuance")
	}
	issuerID, key, err = store.LoadOrCreateKey(ctx)
	if err != nil {
		return "", "", err
	}
	return issuerID, wire.KeyID(key.Public().(ed25519.PublicKey)), nil
}

func installationIssuerGenesisAuthorized() bool {
	return os.Getenv("TECHSTACK_ADVANCED_ISSUER_PREPARATION_MODE") == "prepare" && os.Getenv(db.EnvAllowDBGenesis) == "true"
}

// The explicit migration/key-table authority qualifies the retained identity
// before preparation effects. This uses only LoadExistingKey, with material
// confined to this one-shot process; ordinary runtime keeps its signing pool.
func verifyRetainedIssuerBeforePreparation(ctx context.Context, authority *db.DB, cfg advancedIssuerBootstrapConfig) (string, string, error) {
	declared, err := pgx.ParseConfig(cfg.migration.DSN)
	if err != nil {
		return "", "", errors.New("retained issuer qualification requires the declared migration login")
	}
	var owner, login, session string
	if err := authority.QueryRowContext(ctx, "SELECT pg_get_userbyid(relowner),current_user,session_user FROM pg_catalog.pg_class WHERE oid='public.advanced_issuer_keys'::regclass").Scan(&owner, &login, &session); err != nil || owner != login || login != session || login != declared.User {
		return "", "", errors.New("retained issuer qualification requires the existing key-table authority")
	}
	store, err := advancedissuer.NewPostgresStore(authority, cfg.encryptor)
	if err != nil {
		return "", "", err
	}
	issuerID, key, err := store.LoadExistingKey(ctx)
	if err != nil {
		return "", "", errors.New("existing issuer identity or wrapping custody is unqualified; no preparation effects")
	}
	defer clear(key)
	return issuerID, wire.KeyID(key.Public().(ed25519.PublicKey)), nil
}
