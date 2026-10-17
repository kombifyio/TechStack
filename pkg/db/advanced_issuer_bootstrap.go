package db

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Keep bootstrap diagnostics useful without exposing SQL, credentials or the
// server's message/detail fields. Stages are fixed by the caller.
func issuerPreparationError(stage string, cause error) error {
	message := "advanced issuer preparation: " + stage
	var postgres *pgconn.PgError
	if errors.As(cause, &postgres) {
		message += " (SQLSTATE " + postgres.Code + ")"
	}
	return errors.New(message)
}

// prepareAdvancedIssuerKeyLogin is shared by governed deployment and assigned
// test preparation. Existing credentials are authenticated by their stored
// SCRAM verifier; no password, key, app grant or ownership is changed.
func prepareAdvancedIssuerKeyLogin(ctx context.Context, tx *sql.Tx, role, password, verifier, databaseName, identityFunction string) error {
	if role == "" || len(password) < 32 || !PostgresSCRAMSecretMatches(password, verifier) {
		return errors.New("advanced issuer preparation requires its bound SCRAM credential")
	}
	// Managed Postgres runs without a superuser, so pg_authid is unreadable.
	// There an existing login's credential is proven by the caller's
	// authenticated readback after commit instead of a stored-verifier match.
	var exists, credentialReadable bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_catalog.pg_roles WHERE rolname=$1),
	 has_table_privilege('pg_catalog.pg_authid','SELECT')`, role).Scan(&exists, &credentialReadable); err != nil {
		return issuerPreparationError("inspect-existing-credential", err)
	}
	if exists && credentialReadable {
		var existing string
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(rolpassword,'') FROM pg_catalog.pg_authid WHERE rolname=$1`, role).Scan(&existing); err != nil {
			return issuerPreparationError("inspect-existing-credential", err)
		}
		if !PostgresSCRAMSecretMatches(password, existing) {
			return errors.New("advanced issuer existing credential differs; no change")
		}
	}
	quoted := pgx.Identifier{role}.Sanitize()
	var err error
	if !exists {
		statement := "CREATE ROLE " + quoted + " LOGIN NOINHERIT NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS PASSWORD '" + strings.ReplaceAll(verifier, "'", "''") + "'"
		if _, err = tx.ExecContext(ctx, statement); err != nil {
			return issuerPreparationError("create-login", err)
		}
	}
	if err = inspectAdvancedIssuerKeyPrivileges(ctx, tx, role, identityFunction, false); err != nil {
		return err
	}
	statements := []string{
		"GRANT CONNECT ON DATABASE " + pgx.Identifier{databaseName}.Sanitize() + " TO " + quoted,
		"GRANT USAGE ON SCHEMA public TO " + quoted,
		"GRANT SELECT,INSERT ON TABLE public.advanced_issuer_keys TO " + quoted,
	}
	if identityFunction != "" {
		statements = append(statements, "GRANT EXECUTE ON FUNCTION "+identityFunction+" TO "+quoted)
	}
	stages := []string{"grant-database-connect", "grant-schema-usage", "grant-key-custody", "grant-identity-read"}
	for index, statement := range statements {
		if _, err = tx.ExecContext(ctx, statement); err != nil {
			return issuerPreparationError(stages[index], err)
		}
	}
	return inspectAdvancedIssuerKeyPrivileges(ctx, tx, role, identityFunction, true)
}

// PrepareAdvancedIssuerKeyCustody uses the existing migration/bootstrap
// authority. The transaction owns only the signing login and direct grants.
// Assignment-specific service, owner and identity-helper rules stay in the
// managed test wrapper; this function does not choose a fixture or database.
func PrepareAdvancedIssuerKeyCustody(ctx context.Context, database *DB, issuerDSN, applicationRole, verifier string) error {
	if database == nil || database.DB == nil {
		return errors.New("advanced issuer bootstrap database is absent")
	}
	u, parseErr := url.Parse(issuerDSN)
	if parseErr != nil || len(u.Query()["sslmode"]) != 1 || u.Query().Get("sslmode") != "verify-full" {
		return errors.New("advanced issuer signing transport must verify the server identity")
	}
	cfg, err := pgx.ParseConfig(issuerDSN)
	if err != nil || cfg.User == "" || cfg.User == applicationRole || applicationRole == "" {
		return errors.New("advanced issuer bootstrap needs a distinct signing login")
	}
	if isManagedTestDSN(issuerDSN) {
		return errors.New("assigned database signing custody requires its governed assignment preparation")
	}
	if err = VerifyExpectedDatabaseIdentity(ctx, database.DB); err != nil {
		return err
	}
	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, migrationAdvisoryLockKey); err != nil {
		return err
	}
	var databaseName, login, schema, keyOwner string
	err = tx.QueryRowContext(ctx, `SELECT current_database(),current_user,current_schema(),pg_get_userbyid(c.relowner)
 FROM pg_catalog.pg_class c WHERE c.oid=to_regclass('public.advanced_issuer_keys') AND c.relkind='r'`).Scan(&databaseName, &login, &schema, &keyOwner)
	if err != nil || schema != "public" || databaseName != cfg.Database || login != keyOwner || cfg.User == login {
		return errors.New("advanced issuer bootstrap requires the existing key-table authority and exact database")
	}
	var appKeyAccess bool
	err = tx.QueryRowContext(ctx, `SELECT has_table_privilege($1,'public.advanced_issuer_keys','SELECT,INSERT,UPDATE,DELETE,TRUNCATE,REFERENCES,TRIGGER')
 OR has_any_column_privilege($1,'public.advanced_issuer_keys','SELECT,INSERT,UPDATE,REFERENCES')`, applicationRole).Scan(&appKeyAccess)
	if err != nil || appKeyAccess {
		return errors.New("advanced issuer bootstrap refuses application key authority")
	}
	if err = prepareAdvancedIssuerKeyLogin(ctx, tx, cfg.User, cfg.Password, verifier, databaseName, ""); err != nil {
		return err
	}
	if pin, pinned, pinErr := ExpectedDatabaseIdentityFromEnv(); pinErr != nil {
		return pinErr
	} else if pinned && pin.SystemIdentifier != "" {
		if _, err = tx.ExecContext(ctx, "GRANT EXECUTE ON FUNCTION pg_catalog.pg_control_system() TO "+pgx.Identifier{cfg.User}.Sanitize()); err != nil {
			return errors.New("advanced issuer identity-read grant failed")
		}
	}
	if err = tx.Commit(); err != nil {
		return errors.New("advanced issuer bootstrap commit outcome requires independent inspection")
	}
	runtime, err := sql.Open(PostgresDriverName, issuerDSN)
	if err != nil {
		return errors.New("advanced issuer authenticated readback unavailable")
	}
	defer runtime.Close()
	if err = VerifyExpectedDatabaseIdentity(ctx, runtime); err != nil {
		return err
	}
	return VerifyAdvancedIssuerRuntimePrivileges(ctx, runtime, cfg.User, applicationRole)
}
