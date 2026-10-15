package main

import (
	"context"
	"errors"
	"github.com/kombifyio/techstack/pkg/db"
	"github.com/jackc/pgx/v5"
	"net/url"
)

// The existing ordered producer owns this one-shot stage. No owner credential
// reaches the authenticated app pool or ordinary runtime.
func prepareApplicationPrincipal(ctx context.Context, authority *db.DB, cfg advancedIssuerBootstrapConfig) error {
	u, parseErr := url.Parse(cfg.application.DSN)
	if parseErr != nil || len(u.Query()["sslmode"]) != 1 || u.Query().Get("sslmode") != "verify-full" {
		return errors.New("application preparation requires verified transport")
	}
	app, err := pgx.ParseConfig(cfg.application.DSN)
	if err != nil || app.User == "" || app.User != cfg.appRole || app.Password == "" || app.User == providerControlRuntimeRoleName {
		return errors.New("application preparation requires its explicit distinct purpose login")
	}
	migration, err := pgx.ParseConfig(cfg.migration.DSN)
	if err != nil || app.User == migration.User {
		return errors.New("application preparation refuses migration/legacy owner alias")
	}
	owner, err := db.ApplicationAuthorityOwner(ctx, authority.DB)
	if err != nil || owner != migration.User {
		return errors.New("application preparation requires the declared existing schema authority")
	}
	principal := db.ApplicationPrincipal{Role: app.User, AuthorityOwner: owner}
	if err = ensureProviderControlRuntimeLogin(ctx, authority.DB, app.User, app.Password); err != nil {
		return err
	}
	tx, err := authority.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "SET LOCAL lock_timeout='5s'"); err != nil {
		return err
	}
	if err = db.InstallApplicationCapabilities(ctx, tx, principal); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	application, err := db.OpenContext(ctx, cfg.application)
	if err != nil {
		return errors.New("prepared application login authentication failed")
	}
	defer application.Close()
	if err = db.VerifyExpectedDatabaseIdentity(ctx, application.DB); err != nil {
		return err
	}
	var login, session string
	if err = application.QueryRowContext(ctx, "SELECT current_user,session_user").Scan(&login, &session); err != nil || login != app.User || session != login {
		return errors.New("prepared application session differs from the declared login")
	}
	return db.VerifyApplicationPrivileges(ctx, application.DB, principal, true)
}
