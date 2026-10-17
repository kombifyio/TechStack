package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// InspectManagedTest is the only discovery path without a physical pin. Its
// sole operation is an authenticated read-only transaction on the exact test
// administrator endpoint, never role/database preparation or migration.
func InspectManagedTest(parent context.Context) error {
	a, err := loadManagedTestAssignment(false)
	if err != nil {
		return err
	}
	dsn := os.Getenv(EnvManagedTestAdminDatabaseURL)
	if _, err := a.connection(dsn, "postgres", false); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	defer cancel()
	database, err := sql.Open(PostgresDriverName, dsn)
	if err != nil {
		return fmt.Errorf("managed test inspection transport failed")
	}
	defer database.Close()
	database.SetMaxOpenConns(1)
	tx, err := database.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var physical, name, login, session, version string
	if err := tx.QueryRowContext(ctx, `SELECT system_identifier::text,current_database(),current_user,session_user,current_setting('server_version') FROM pg_catalog.pg_control_system()`).Scan(&physical, &name, &login, &session, &version); err != nil {
		return err
	}
	if name != "postgres" || login != "postgres" || session != login || !strings.HasPrefix(version, "18.") || (a.SystemIdentifier != "" && a.SystemIdentifier != physical) {
		return fmt.Errorf("managed test discovery identity differs from its assignment")
	}
	var foreignPublic bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_catalog.pg_database d CROSS JOIN LATERAL pg_catalog.aclexplode(COALESCE(d.datacl,pg_catalog.acldefault('d',d.datdba))) acl
 WHERE d.datallowconn AND d.datname<>$1 AND acl.grantee=0 AND acl.privilege_type IN ('CONNECT','TEMPORARY'))
 OR EXISTS(SELECT 1 FROM pg_catalog.pg_database d CROSS JOIN pg_catalog.pg_roles r
 WHERE d.datallowconn AND d.datname<>$1 AND r.rolname IN ($2,$3,$4)
 AND (has_database_privilege(r.oid,d.oid,'CONNECT') OR has_database_privilege(r.oid,d.oid,'TEMP')))`, ManagedTestDatabase, ManagedTestOwner, ManagedTestApp, ManagedTestProvider).Scan(&foreignPublic); err != nil {
		return err
	}
	roles := map[string]any{}
	for _, role := range []string{ManagedTestOwner, ManagedTestApp, ManagedTestProvider} {
		var unsafe bool
		err := tx.QueryRowContext(ctx, `SELECT r.rolsuper OR r.rolbypassrls OR r.rolcreatedb OR r.rolcreaterole OR r.rolreplication OR r.rolinherit OR NOT r.rolcanlogin
		 OR EXISTS(SELECT 1 FROM pg_catalog.pg_auth_members WHERE member=r.oid)
		 FROM pg_catalog.pg_roles r WHERE rolname=$1`, role).Scan(&unsafe)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		roles[role] = map[string]bool{"exists": err == nil, "unsafe_flags_or_membership": err == nil && unsafe}
	}
	var assignedOwner string
	err = tx.QueryRowContext(ctx, `SELECT pg_catalog.pg_get_userbyid(datdba) FROM pg_catalog.pg_database WHERE datname=$1`, ManagedTestDatabase).Scan(&assignedOwner)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]any{"schema": "techstack.managed-test-inspection/v1", "service_id": a.ServiceID, "system_identifier": physical, "database": name, "session_user": session, "foreign_public_database_authority": foreignPublic, "assigned_database_exists": assignedOwner != "", "assigned_database_owner": assignedOwner, "assigned_roles": roles, "schema_role_data_writes": false})
}

// ManagedTestPreparation contains only the assigned connection inputs. The CLI
// derives SCRAM with the existing authority; raw passwords never enter SQL.
type ManagedTestPreparation struct {
	Assignment                                   ManagedTestAssignment
	AdminDSN, OwnerDSN, AppDSN, ProviderDSN      string
	OwnerVerifier, AppVerifier, ProviderVerifier string
}

func (p ManagedTestPreparation) Validate() error {
	if err := p.Assignment.VerifyPin(); err != nil {
		return err
	}
	secrets := map[string]bool{}
	for _, item := range []struct{ dsn, role string }{{p.AdminDSN, "postgres"}, {p.OwnerDSN, ManagedTestOwner}, {p.AppDSN, ManagedTestApp}, {p.ProviderDSN, ManagedTestProvider}} {
		cfg, err := p.Assignment.Connection(item.dsn, item.role)
		if err != nil {
			return err
		}
		if cfg.Password == "" || (item.role != "postgres" && len(cfg.Password) < 32) || secrets[cfg.Password] {
			return fmt.Errorf("managed test logins require distinct assigned secrets")
		}
		secrets[cfg.Password] = true
	}
	for _, verifier := range []string{p.OwnerVerifier, p.AppVerifier, p.ProviderVerifier} {
		if !strings.HasPrefix(verifier, "SCRAM-SHA-256$4096:") {
			return fmt.Errorf("managed test role preparation requires existing SCRAM derivation")
		}
	}
	return nil
}

// PrepareManagedTest is the typed administrative operation run only after the
// native source review and governed Delivery checkpoint. It never migrates,
// copies another database, rotates an existing login or dispatches a provider.
func PrepareManagedTest(parent context.Context, p ManagedTestPreparation) error {
	if err := p.Validate(); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(parent, 90*time.Second)
	defer cancel()
	admin, err := sql.Open(PostgresDriverName, p.AdminDSN)
	if err != nil {
		return fmt.Errorf("managed test admin connection failed")
	}
	defer admin.Close()
	admin.SetMaxOpenConns(1)
	admin.SetMaxIdleConns(1)
	var systemID, login, session, version string
	if err := admin.QueryRowContext(ctx, `SELECT system_identifier::text,current_user,session_user,current_setting('server_version') FROM pg_catalog.pg_control_system()`).Scan(&systemID, &login, &session, &version); err != nil {
		return fmt.Errorf("managed test admin identity: %w", err)
	}
	if systemID != p.Assignment.SystemIdentifier || login != "postgres" || session != login || !strings.HasPrefix(version, "18.") {
		return fmt.Errorf("managed test admin identity differs from the approved assignment")
	}
	var foreignPublic bool
	if err := admin.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_catalog.pg_database d
	 CROSS JOIN LATERAL pg_catalog.aclexplode(COALESCE(d.datacl,pg_catalog.acldefault('d',d.datdba))) acl
 WHERE d.datallowconn AND d.datname<>$1 AND acl.grantee=0
 AND acl.privilege_type IN ('CONNECT','TEMPORARY'))
 OR EXISTS(SELECT 1 FROM pg_catalog.pg_database d CROSS JOIN pg_catalog.pg_roles r
 WHERE d.datallowconn AND d.datname<>$1 AND r.rolname IN ($2,$3,$4)
 AND (has_database_privilege(r.oid,d.oid,'CONNECT') OR has_database_privilege(r.oid,d.oid,'TEMP')))`, ManagedTestDatabase, ManagedTestOwner, ManagedTestApp, ManagedTestProvider).Scan(&foreignPublic); err != nil {
		return err
	}
	if foreignPublic {
		return fmt.Errorf("managed test service still grants PUBLIC database access outside the assignment; no roles changed")
	}
	// Refuse unsafe existing logins before any role or database mutation.
	for _, role := range []string{ManagedTestOwner, ManagedTestApp, ManagedTestProvider} {
		var unsafe bool
		err := admin.QueryRowContext(ctx, `SELECT r.rolsuper OR r.rolbypassrls OR r.rolcreatedb OR r.rolcreaterole OR r.rolreplication OR r.rolinherit OR NOT r.rolcanlogin OR COALESCE(array_length(r.rolconfig,1),0)>0
		 OR EXISTS(SELECT 1 FROM pg_catalog.pg_auth_members WHERE member=r.oid)
		 OR EXISTS(SELECT 1 FROM pg_catalog.pg_database WHERE datdba=r.oid AND datname<>$2)
		 FROM pg_catalog.pg_roles r WHERE r.rolname=$1`, role, ManagedTestDatabase).Scan(&unsafe)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err == nil && unsafe {
			return fmt.Errorf("managed test existing login has unassigned authority")
		}
		if err == nil {
			var verifier string
			if err := admin.QueryRowContext(ctx, `SELECT COALESCE(rolpassword,'') FROM pg_catalog.pg_authid WHERE rolname=$1`, role).Scan(&verifier); err != nil {
				return fmt.Errorf("managed test existing credential custody cannot be inspected")
			}
			dsn := p.OwnerDSN
			if role == ManagedTestApp {
				dsn = p.AppDSN
			}
			if role == ManagedTestProvider {
				dsn = p.ProviderDSN
			}
			cfg, err := p.Assignment.Connection(dsn, role)
			if err != nil {
				return err
			}
			if !PostgresSCRAMSecretMatches(cfg.Password, verifier) {
				return fmt.Errorf("managed test existing credential custody does not match; no roles changed")
			}
		}
	}
	var owner string
	err = admin.QueryRowContext(ctx, `SELECT pg_catalog.pg_get_userbyid(datdba) FROM pg_catalog.pg_database WHERE datname=$1`, ManagedTestDatabase).Scan(&owner)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err == nil && owner != ManagedTestOwner {
		return fmt.Errorf("managed test database is owned by another authority; adoption refused")
	}
	existingDatabase := err == nil
	if existingDatabase {
		parsed, _ := url.Parse(p.AdminDSN)
		parsed.Path = "/" + ManagedTestDatabase
		inspection, err := sql.Open(PostgresDriverName, parsed.String())
		if err != nil {
			return err
		}
		var unsafeOwnership bool
		err = inspection.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_catalog.pg_roles r WHERE r.rolname IN ($1,$2) AND (
		 EXISTS(SELECT 1 FROM pg_catalog.pg_namespace WHERE nspowner=r.oid)
		 OR EXISTS(SELECT 1 FROM pg_catalog.pg_class WHERE relowner=r.oid)
		 OR EXISTS(SELECT 1 FROM pg_catalog.pg_proc WHERE proowner=r.oid)))`, ManagedTestApp, ManagedTestProvider).Scan(&unsafeOwnership)
		inspection.Close()
		if err != nil {
			return err
		}
		if unsafeOwnership {
			return fmt.Errorf("managed test existing runtime owns database objects; no roles changed")
		}
		// Existing excess authority is not silently adopted while preparation
		// proves login custody. Capability reconciliation is a separate migration
		// operation, followed by the complete effective-rights readback.
		var appExists bool
		if err := admin.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_roles WHERE rolname=$1)`, ManagedTestApp).Scan(&appExists); err != nil {
			return err
		}
		if appExists {
			inspection, err := sql.Open(PostgresDriverName, parsed.String())
			if err != nil {
				return err
			}
			err = VerifyManagedTestApplicationPrivileges(ctx, inspection, false)
			inspection.Close()
			if err != nil {
				return err
			}
		}
	}
	for _, item := range []struct{ role, verifier string }{{ManagedTestOwner, p.OwnerVerifier}, {ManagedTestApp, p.AppVerifier}, {ManagedTestProvider, p.ProviderVerifier}} {
		var exists bool
		if err := admin.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_catalog.pg_roles WHERE rolname=$1)`, item.role).Scan(&exists); err != nil {
			return err
		}
		if exists {
			continue
		}
		var statement string
		if err := admin.QueryRowContext(ctx, `SELECT pg_catalog.format('CREATE ROLE %I LOGIN PASSWORD %L NOSUPERUSER NOBYPASSRLS NOCREATEDB NOCREATEROLE NOREPLICATION NOINHERIT',$1::text,$2::text)`, item.role, item.verifier).Scan(&statement); err != nil {
			return err
		}
		if _, err := admin.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("managed test create assigned login: %w", err)
		}
	}
	if !existingDatabase {
		// CREATE DATABASE cannot be part of a PostgreSQL transaction. An
		// interrupted result is inspected by root; it is never automatically retried.
		statement := "CREATE DATABASE " + pgx.Identifier{ManagedTestDatabase}.Sanitize() + " OWNER " + pgx.Identifier{ManagedTestOwner}.Sanitize() + " TEMPLATE template0"
		if _, err := admin.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("managed test create assigned database: %w", err)
		}
	}
	parsed, _ := url.Parse(p.AdminDSN)
	parsed.Path = "/" + ManagedTestDatabase
	target, err := sql.Open(PostgresDriverName, parsed.String())
	if err != nil {
		return fmt.Errorf("managed test assigned admin connection failed")
	}
	defer target.Close()
	target.SetMaxOpenConns(1)
	var targetSystem, targetDB string
	if err := target.QueryRowContext(ctx, `SELECT system_identifier::text,current_database() FROM pg_catalog.pg_control_system()`).Scan(&targetSystem, &targetDB); err != nil {
		return err
	}
	if targetSystem != systemID || targetDB != ManagedTestDatabase {
		return fmt.Errorf("managed test administrative database moved")
	}
	tx, err := target.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	ownerRole := pgx.Identifier{ManagedTestOwner}.Sanitize()
	statements := []string{
		"REVOKE ALL ON DATABASE " + pgx.Identifier{ManagedTestDatabase}.Sanitize() + " FROM PUBLIC",
		"REVOKE ALL ON SCHEMA public FROM PUBLIC",
		"ALTER SCHEMA public OWNER TO " + ownerRole,
		"ALTER DEFAULT PRIVILEGES FOR ROLE " + ownerRole + " REVOKE EXECUTE ON FUNCTIONS FROM PUBLIC",
		"ALTER DEFAULT PRIVILEGES FOR ROLE " + ownerRole + " IN SCHEMA public REVOKE ALL ON TABLES FROM PUBLIC",
		"ALTER DEFAULT PRIVILEGES FOR ROLE " + ownerRole + " IN SCHEMA public REVOKE ALL ON SEQUENCES FROM PUBLIC",
		"GRANT EXECUTE ON FUNCTION pg_catalog.pg_control_system() TO " + ownerRole,
		`CREATE OR REPLACE FUNCTION public.techstack_test_database_identity()
		 RETURNS TABLE(database_name text,system_identifier text) LANGUAGE sql STABLE SECURITY DEFINER
		 SET search_path TO pg_catalog,public,pg_temp
		 AS 'SELECT current_database()::text,system_identifier::text FROM pg_catalog.pg_control_system()'`,
		"ALTER FUNCTION public.techstack_test_database_identity() OWNER TO " + ownerRole,
		"REVOKE ALL ON FUNCTION public.techstack_test_database_identity() FROM PUBLIC",
	}
	for _, role := range []string{ManagedTestOwner, ManagedTestApp, ManagedTestProvider} {
		quoted := pgx.Identifier{role}.Sanitize()
		statements = append(statements, "GRANT CONNECT ON DATABASE "+pgx.Identifier{ManagedTestDatabase}.Sanitize()+" TO "+quoted,
			"GRANT USAGE ON SCHEMA public TO "+quoted)
		if role != ManagedTestProvider {
			statements = append(statements, "GRANT EXECUTE ON FUNCTION public.techstack_test_database_identity() TO "+quoted)
		}
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("managed test scoped preparation: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	// Every resulting login is authenticated independently, including existing
	// roles whose stored custody was proved before the first mutation.
	for _, item := range []struct{ dsn, role string }{{p.OwnerDSN, ManagedTestOwner}, {p.AppDSN, ManagedTestApp}, {p.ProviderDSN, ManagedTestProvider}} {
		login, err := sql.Open(PostgresDriverName, item.dsn)
		if err != nil {
			return fmt.Errorf("managed test resulting login could not be opened")
		}
		var databaseName, sessionName, roleName string
		var unsafe bool
		err = login.QueryRowContext(ctx, `SELECT current_database(),session_user,current_user,
		 r.rolsuper OR r.rolbypassrls OR r.rolcreatedb OR r.rolcreaterole OR r.rolreplication OR r.rolinherit
		 OR EXISTS(SELECT 1 FROM pg_catalog.pg_auth_members WHERE member=r.oid)
		 FROM pg_catalog.pg_roles r WHERE rolname=current_user`).Scan(&databaseName, &sessionName, &roleName, &unsafe)
		login.Close()
		if err != nil || databaseName != ManagedTestDatabase || sessionName != item.role || roleName != sessionName || unsafe {
			return fmt.Errorf("managed test resulting login authentication/readback failed")
		}
	}
	return nil
}

// VerifyManagedTestSession reads the actual authenticated login and its
// effective privileges; it does not substitute SET ROLE or an owner connection.
func VerifyManagedTestSession(ctx context.Context, database *sql.DB, a ManagedTestAssignment, role string) error {
	return verifyManagedTestSession(ctx, database, a, role, "")
}

// The original exact identity/posture and every existing capability remain
// required. Only the two not-yet-created153 routines can be absent, and only
// while both routines and the153 ledger entry are independently absent.
func VerifyManagedTestDriftForwardSession(ctx context.Context, database *sql.DB, a ManagedTestAssignment) error {
	return verifyManagedTestSession(ctx, database, a, ManagedTestApp, "drift")
}

// Only both absent154 functions and its absent ledger admit this prospective
// check; every existing capability, identity and authority fence stays strict.
func VerifyManagedTestPendingJobForwardSession(ctx context.Context, database *sql.DB, a ManagedTestAssignment) error {
	return verifyManagedTestSession(ctx, database, a, ManagedTestApp, "pending-jobs")
}

// Only both absent155 routines and its absent ledger admit this prospective
// purpose. Every original identity, membership and capability remains required.
func VerifyManagedTestExecutionForwardSession(ctx context.Context, database *sql.DB, a ManagedTestAssignment) error {
	return verifyManagedTestSession(ctx, database, a, ManagedTestApp, "execution-reclaim")
}

// Only all absent 156 routines and its absent ledger admit this prospective purpose.
func VerifyManagedTestSubjectDirectoryForwardSession(ctx context.Context, database *sql.DB, a ManagedTestAssignment) error {
	return verifyManagedTestSession(ctx, database, a, ManagedTestApp, "subject-directory")
}

func verifyManagedTestSession(ctx context.Context, database *sql.DB, a ManagedTestAssignment, role string, pendingForward string) error {
	if err := a.Validate(); err != nil {
		return err
	}
	if err := a.VerifyPin(); err != nil {
		return err
	}
	if role != ManagedTestApp && role != ManagedTestOwner && role != ManagedTestProvider && role != ManagedTestIssuer {
		return fmt.Errorf("managed test session role is not assigned")
	}
	var login, session, name, system, version string
	var unsafe, ownership, ddl bool
	err := database.QueryRowContext(ctx, `SELECT current_user,session_user,i.database_name,i.system_identifier,current_setting('server_version'),
	 r.rolsuper OR r.rolbypassrls OR r.rolcreatedb OR r.rolcreaterole OR r.rolreplication OR r.rolinherit OR NOT r.rolcanlogin
	 OR COALESCE(array_length(r.rolconfig,1),0)>0 OR current_schema()<>'public'
	 OR EXISTS(SELECT 1 FROM pg_catalog.pg_auth_members WHERE member=r.oid),
	 EXISTS(SELECT 1 FROM pg_catalog.pg_database WHERE datdba=r.oid)
	 OR EXISTS(SELECT 1 FROM pg_catalog.pg_class WHERE relowner=r.oid)
	 OR EXISTS(SELECT 1 FROM pg_catalog.pg_proc WHERE proowner=r.oid)
	 OR EXISTS(SELECT 1 FROM pg_catalog.pg_namespace WHERE nspowner=r.oid),
	 has_database_privilege(current_user,current_database(),'CREATE') OR has_database_privilege(current_user,current_database(),'TEMP')
 OR EXISTS(SELECT 1 FROM pg_catalog.pg_namespace n WHERE n.nspname<>'information_schema' AND n.nspname NOT LIKE 'pg_%' AND has_schema_privilege(current_user,n.oid,'CREATE'))
	 FROM pg_catalog.pg_roles r CROSS JOIN public.techstack_test_database_identity() i WHERE r.rolname=current_user`).Scan(&login, &session, &name, &system, &version, &unsafe, &ownership, &ddl)
	if err != nil {
		return fmt.Errorf("managed test authenticated posture: %w", err)
	}
	if login != role || session != login || name != a.Database || system != a.SystemIdentifier || !strings.HasPrefix(version, "18.") || unsafe || (role != ManagedTestOwner && (ownership || ddl)) {
		return fmt.Errorf("managed test authenticated session has unassigned authority or identity")
	}
	if role == ManagedTestApp {
		if pendingForward != "" {
			var alreadyPresent bool
			version, list, compact := "153_drift_schedule_authority.sql", ManagedTestDriftScheduleListFunction, ManagedTestDriftScheduleCompactFunction
			if pendingForward == "pending-jobs" {
				version, list, compact = "154_pending_job_directory_authority.sql", ManagedTestPendingJobListFunction, ManagedTestPendingJobCompactFunction
			} else if pendingForward == "execution-reclaim" {
				version, list, compact = "155_job_execution_reclaim_authority.sql", ManagedTestExecutionReclaimListFunction, ManagedTestExecutionReclaimCompactFunction
			} else if pendingForward == "subject-directory" {
				version, list, compact = "156_subject_directory.sql", ManagedTestSubjectDirectoryLookupFunction, ManagedTestSubjectDirectoryForgetFunction
			} else if pendingForward != "drift" {
				return fmt.Errorf("unknown managed forward purpose")
			}
			if err := database.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM public.schema_migrations WHERE version=$1)
			 OR to_regprocedure($2) IS NOT NULL OR to_regprocedure($3) IS NOT NULL`, version, "public."+list, "public."+compact).Scan(&alreadyPresent); err != nil {
				return err
			}
			if alreadyPresent {
				return fmt.Errorf("managed forward already present; inspect original receipt")
			}
			return verifyManagedTestApplicationPrivileges(ctx, database, true, pendingForward)
		}
		return VerifyManagedTestApplicationPrivileges(ctx, database, true)
	}
	return nil
}

// ManagedTestAuthorityOwnerOID checks the schema/definer owner independently of
// the app session. The returned oid is an object owner, not an authenticated user.
func ManagedTestAuthorityOwnerOID(ctx context.Context, database *sql.DB, a ManagedTestAssignment) (int64, error) {
	if err := VerifyManagedTestSession(ctx, database, a, ManagedTestApp); err != nil {
		return 0, err
	}
	var oid int64
	err := database.QueryRowContext(ctx, `SELECT r.oid::bigint FROM pg_catalog.pg_roles r
	 JOIN pg_catalog.pg_database d ON d.datdba=r.oid AND d.datname=current_database()
	 JOIN pg_catalog.pg_namespace n ON n.nspowner=r.oid AND n.nspname='public'
	 WHERE r.rolname=$1 AND NOT(r.rolsuper OR r.rolbypassrls OR r.rolcreatedb OR r.rolcreaterole OR r.rolreplication)
	 AND NOT EXISTS(SELECT 1 FROM pg_catalog.pg_auth_members WHERE member=r.oid)`, ManagedTestOwner).Scan(&oid)
	if err != nil {
		return 0, fmt.Errorf("managed test schema authority owner: %w", err)
	}
	return oid, nil
}
