package localdb

import (
	"context"
	"errors"
	"net/url"
	"strings"

	"github.com/kombifyio/techstack/pkg/db"
	"github.com/jackc/pgx/v5"
)

// NativeApplicationRole keeps the product's existing local identity lifecycle.
// Hosted requests retain their separately registered, verified-context role.
const NativeApplicationRole = "techstack_local_application"

func nativeDBConfig(dsn string) db.Config {
	config := db.DefaultConfig()
	config.Backend, config.DSN, config.MaxOpenConns, config.MaxIdleConns = db.StoreBackendPostgres, dsn, 2, 1
	return config
}

// PrepareApplication runs only in the operator process, never in the server.
// All three principals are explicit and belong to the same physical cluster.
// Existing customer objects and PUBLIC excess are refused, not silently reset.
func PrepareApplication(ctx context.Context, operatorDSN, migrationDSN, applicationDSN string, pin db.ExpectedDatabaseIdentity) error {
	if strings.TrimSpace(operatorDSN) == "" || strings.TrimSpace(migrationDSN) == "" || strings.TrimSpace(applicationDSN) == "" {
		return errors.New("explicit operator, migration and application connections are required; no environment fallback")
	}
	operator, err := pgx.ParseConfig(operatorDSN)
	if err != nil {
		return errors.New("invalid native operator connection")
	}
	migration, err := pgx.ParseConfig(migrationDSN)
	if err != nil {
		return errors.New("invalid native migration connection")
	}
	application, err := pgx.ParseConfig(applicationDSN)
	if err != nil || application.User != NativeApplicationRole || application.Password == "" ||
		operator.User == application.User || migration.User == application.User {
		return errors.New("native preparation requires distinct operator, migration and bounded local application custody")
	}
	connections := []string{operatorDSN, migrationDSN}
	for _, dsn := range connections {
		parsed, _ := pgx.ParseConfig(dsn)
		if parsed.Host != application.Host || parsed.Port != application.Port || parsed.Database != application.Database {
			return errors.New("native preparation principals must target one installation")
		}
	}
	admin, err := db.OpenContext(ctx, nativeDBConfig(operatorDSN))
	if err != nil {
		return errors.New("native operator connection failed")
	}
	defer admin.Close()
	if err := verifyPhysicalPin(ctx, admin, pin); err != nil {
		return err
	}
	var owner, databaseOwner string
	if err := admin.QueryRowContext(ctx, "SELECT (SELECT pg_get_userbyid(nspowner) FROM pg_namespace WHERE nspname='public'),(SELECT pg_get_userbyid(datdba) FROM pg_database WHERE datname=current_database())").Scan(&owner, &databaseOwner); err != nil || owner != migration.User || databaseOwner != migration.User {
		return errors.New("native migration login must own this database and its public schema; no application credential was changed")
	}
	verifier, err := scramVerifier(application.Password)
	if err != nil {
		return err
	}
	role := pgx.Identifier{application.User}.Sanitize()
	var exists bool
	if err := admin.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM pg_roles WHERE rolname=$1)", application.User).Scan(&exists); err != nil {
		return err
	}
	statement := "CREATE ROLE " + role + " LOGIN NOSUPERUSER NOBYPASSRLS NOCREATEROLE NOCREATEDB NOREPLICATION NOINHERIT PASSWORD " + quoteLiteral(verifier)
	if exists {
		if err := db.VerifyRuntimeRolePostureForPrincipal(ctx, admin.DB, application.User); err != nil {
			return err
		}
		statement = "ALTER ROLE " + role + " PASSWORD " + quoteLiteral(verifier)
	}
	if _, err := admin.ExecContext(ctx, statement); err != nil {
		return errors.New("prepare native application login failed")
	}
	authority, err := db.OpenContext(ctx, nativeDBConfig(migrationDSN))
	if err != nil {
		return errors.New("native migration connection failed")
	}
	defer authority.Close()
	if err := verifyPhysicalPin(ctx, authority, pin); err != nil {
		return err
	}
	if err := authority.Migrate(ctx); err != nil {
		return err
	}
	tx, err := authority.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "SET LOCAL lock_timeout='5s'; SET LOCAL statement_timeout='2min'"); err != nil {
		return err
	}
	// These are the native installation's default database/schema rights only;
	// canonical object/function grants are installed by the shared policy.
	if _, err := tx.ExecContext(ctx, "REVOKE TEMPORARY,CREATE ON DATABASE "+pgx.Identifier{pin.DatabaseName}.Sanitize()+" FROM PUBLIC; REVOKE CREATE ON SCHEMA public FROM PUBLIC"); err != nil {
		return err
	}
	principal := db.ApplicationPrincipal{Role: application.User, AuthorityOwner: owner}
	if err := db.InstallApplicationCapabilitiesWithIdentity(ctx, tx, principal, pin); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return errors.New("native application grant outcome unknown; inspect the catalog before retry")
	}
	app, err := db.OpenContext(ctx, nativeDBConfig(applicationDSN))
	if err != nil {
		return errors.New("native application authentication readback failed")
	}
	defer app.Close()
	if err := verifyPhysicalPin(ctx, app, pin); err != nil {
		return err
	}
	if err := db.VerifyRuntimeRolePosture(ctx, app.DB, db.RuntimeRoleLocalOwner); err != nil {
		return err
	}
	if err := db.VerifyApplicationPrivileges(ctx, app.DB, principal, true); err != nil {
		return err
	}
	return app.VerifyMigrationsWithIdentity(ctx, pin)
}

func verifyPhysicalPin(ctx context.Context, database *db.DB, pin db.ExpectedDatabaseIdentity) error {
	if pin.DatabaseName == "" || pin.SystemIdentifier == "" {
		return errors.New("native preparation requires an exact physical pin")
	}
	var name, system string
	if err := database.QueryRowContext(ctx, "SELECT current_database(),system_identifier::text FROM pg_catalog.pg_control_system()").Scan(&name, &system); err != nil {
		return errors.New("native physical database identity read failed")
	}
	return pin.Verify(name, system)
}

func (pg *EmbeddedPostgres) PrepareApplication(ctx context.Context) (string, error) {
	if pg == nil || pg.custody == nil {
		return "", errors.New("native supervisor database is required")
	}
	password, err := randomCredential()
	if err != nil {
		return "", err
	}
	u, err := url.Parse(pg.dsn)
	if err != nil {
		return "", errors.New("invalid native migration connection")
	}
	u.User = url.UserPassword(NativeApplicationRole, password)
	name, system, _ := strings.Cut(pg.PhysicalIdentity(), "@")
	pin := db.ExpectedDatabaseIdentity{DatabaseName: name, SystemIdentifier: system}
	if err := PrepareApplication(ctx, pg.adminDSN, pg.dsn, u.String(), pin); err != nil {
		return "", err
	}
	return u.String(), nil
}
