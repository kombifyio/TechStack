package advancedissuer

import (
	"context"
	"errors"
	"net/url"
	"os"
	"strings"

	"github.com/kombifyio/techstack/pkg/auth"
	"github.com/kombifyio/techstack/pkg/db"
)

// EnvKeyCustodyDatabaseURL binds only the installation signing key store.
// Tenant records continue to use DATABASE_URL and its customer-scoped role.
const EnvKeyCustodyDatabaseURL = db.EnvAdvancedIssuerDatabaseURL

// OpenBound opens the existing issuer with explicit signing custody. The
// returned pool belongs to the runtime and must close after admission drains.
func OpenBound(ctx context.Context, application *db.DB, encryptor *auth.SecretEncryptor) (*Issuer, *db.DB, error) {
	if application == nil || application.DB == nil {
		return nil, nil, errors.New("advanced issuer application storage is unavailable")
	}
	dsn := strings.TrimSpace(os.Getenv(EnvKeyCustodyDatabaseURL))
	if dsn == "" {
		return nil, nil, errors.New("advanced issuer signing custody binding is not configured")
	}
	if os.Getenv(EnvIssuerKey) != "" {
		return nil, nil, errors.New("bound advanced issuer cannot substitute a development key")
	}
	parsed, err := url.Parse(dsn)
	if err != nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") || parsed.User == nil || parsed.User.Username() == "" ||
		len(parsed.Query()["sslmode"]) != 1 || parsed.Query().Get("sslmode") != "verify-full" {
		return nil, nil, errors.New("advanced issuer custody requires its signing-only login and verified transport")
	}
	applicationConfig, err := db.ConfigFromEnv()
	if err != nil {
		return nil, nil, errors.New("advanced issuer application database configuration is unavailable")
	}
	applicationEndpoint, err := url.Parse(applicationConfig.DSN)
	port := func(endpoint *url.URL) string {
		if endpoint.Port() == "" {
			return "5432"
		}
		return endpoint.Port()
	}
	if err != nil || !strings.EqualFold(parsed.Hostname(), applicationEndpoint.Hostname()) || port(parsed) != port(applicationEndpoint) || parsed.Path != applicationEndpoint.Path {
		return nil, nil, errors.New("advanced issuer signing custody must share the application database endpoint")
	}
	cfg := db.DefaultConfig()
	cfg.Backend, cfg.DriverName, cfg.DSN = db.StoreBackendPostgres, db.PostgresDriverName, dsn
	cfg.MaxOpenConns, cfg.MaxIdleConns = 1, 1
	pool, err := db.OpenContext(ctx, cfg)
	if err != nil {
		return nil, nil, errors.New("advanced issuer custody login failed")
	}
	fail := func(message string) (*Issuer, *db.DB, error) {
		_ = pool.Close()
		return nil, nil, errors.New(message)
	}
	if db.VerifyExpectedDatabaseIdentity(ctx, application.DB) != nil || db.VerifyExpectedDatabaseIdentity(ctx, pool.DB) != nil {
		return fail("advanced issuer storage differs from its registered physical database")
	}
	var applicationLogin string
	if application.QueryRowContext(ctx, `SELECT session_user`).Scan(&applicationLogin) != nil {
		return fail("advanced issuer application identity is unavailable")
	}
	if db.VerifyAdvancedIssuerRuntimePrivileges(ctx, pool.DB, parsed.User.Username(), applicationLogin) != nil {
		return fail("advanced issuer custody login has unassigned authority")
	}
	issuer, err := OpenWithKeyCustody(ctx, application, pool, encryptor)
	if err != nil {
		_ = pool.Close()
		return nil, nil, err
	}
	return issuer, pool, nil
}
