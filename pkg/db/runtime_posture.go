package db

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
)

// RuntimeRolePosture names how strictly the serving login is judged at boot.
type RuntimeRolePosture int

const (
	// RuntimeRoleLocalOwner is a self-hosted installation whose single local
	// login owns its own schema (internal/localdb). It may own objects, but it
	// never bypasses RLS: FORCE RLS still applies to it.
	RuntimeRoleLocalOwner RuntimeRolePosture = iota
	// RuntimeRoleHosted is every hosted runtime (stable, the Preview channel
	// and non-production deployments). D-2026-10-22-02: tenant isolation holds
	// by construction in both instances, so the serving login is a bounded
	// application role.
	RuntimeRoleHosted
)

// ErrRuntimeRoleUnsafe is the mandatory boot refusal. There is no override.
var ErrRuntimeRoleUnsafe = errors.New("runtime database login is not a bounded application role")

// HostedApplicationRole is the application principal provisioned by governed
// preparation. Migration and purpose-specific principals never serve HTTP.
const HostedApplicationRole = "techstack_application"

// VerifyRuntimeRolePosture is the mandatory boot assertion on the serving
// login. Every posture refuses a superuser, a BYPASSRLS login, a login
// switched to another role, and any role- or database-level default for an
// app.* setting (a default tenant or scope would widen RLS for every query).
// The hosted posture also refuses ownership of the database or any object,
// CREATE/TEMP on the database, CREATE on any schema, role administration,
// inheritance and any role membership, and requires the prepared application
// login, so migration or purpose-specific custody can never serve HTTP.
// Findings name only the failed property, never a role name or DSN.
func VerifyRuntimeRolePosture(ctx context.Context, q PrivilegeQuerier, posture RuntimeRolePosture) error {
	if posture == RuntimeRoleHosted && (strings.TrimSpace(os.Getenv(EnvBackupWorkerDatabaseURL)) != "" || strings.TrimSpace(os.Getenv("TECHSTACK_BACKUP_ENTITLEMENTS_DATABASE_URL")) != "" || strings.TrimSpace(os.Getenv("TECHSTACK_BACKUP_WORKER_MODE")) != "") {
		return fmt.Errorf("%w: HTTP application received private worker custody", ErrRuntimeRoleUnsafe)
	}
	return verifyRuntimeRolePosture(ctx, q, posture, HostedApplicationRole)
}

// VerifyBackupWorkerRuntimeRole applies the hosted posture to the one private
// backup principal. This login never serves HTTP or falls back to the app DSN.
func VerifyBackupWorkerRuntimeRole(ctx context.Context, q PrivilegeQuerier) error {
	return verifyRuntimeRolePosture(ctx, q, RuntimeRoleHosted, BackupWorkerRole)
}

// Private Execution already separates signing/provider custody from HTTP. Its
// restricted app pool retains the same catalog posture; worker custody belongs
// to that private process only and is never accepted by the serving assertion.
func VerifyPrivateExecutionApplicationRole(ctx context.Context, q PrivilegeQuerier) error {
	return verifyRuntimeRolePosture(ctx, q, RuntimeRoleHosted, HostedApplicationRole)
}

func verifyRuntimeRolePosture(ctx context.Context, q PrivilegeQuerier, posture RuntimeRolePosture, expectedRole string) error {
	if q == nil {
		return ErrRuntimeRoleUnsafe
	}
	var (
		switched, wrongLogin, superuser, bypassRLS, administrative, inherits, membership bool
		ownsDatabase, ownsObjects, databaseCreate, schemaCreate, settingDefaults         bool
	)
	err := q.QueryRowContext(ctx, `SELECT
	 current_user<>session_user,
	 current_user<>$1 OR session_user<>$1,
	 r.rolsuper,
	 r.rolbypassrls,
	 r.rolcreaterole OR r.rolcreatedb OR r.rolreplication,
	 r.rolinherit,
	 EXISTS(SELECT 1 FROM pg_catalog.pg_auth_members m WHERE m.member=r.oid),
	 EXISTS(SELECT 1 FROM pg_catalog.pg_database d WHERE d.datdba=r.oid),
	 EXISTS(SELECT 1 FROM pg_catalog.pg_shdepend d
	  WHERE d.refclassid='pg_catalog.pg_authid'::regclass AND d.refobjid=r.oid AND d.deptype='o'),
	 has_database_privilege(r.oid,current_database(),'CREATE') OR has_database_privilege(r.oid,current_database(),'TEMP'),
	 EXISTS(SELECT 1 FROM pg_catalog.pg_namespace n WHERE n.nspname<>'information_schema' AND n.nspname NOT LIKE 'pg\_%'
	  AND has_schema_privilege(r.oid,n.oid,'CREATE')),
	 EXISTS(SELECT 1 FROM pg_catalog.pg_db_role_setting s CROSS JOIN LATERAL unnest(s.setconfig) setting
	  WHERE s.setrole IN (0,r.oid)
	  AND s.setdatabase IN (0,(SELECT oid FROM pg_catalog.pg_database WHERE datname=current_database()))
	  AND lower(setting) LIKE 'app.%')
	 FROM pg_catalog.pg_roles r WHERE r.rolname=current_user`, expectedRole).Scan(
		&switched, &wrongLogin, &superuser, &bypassRLS, &administrative, &inherits, &membership,
		&ownsDatabase, &ownsObjects, &databaseCreate, &schemaCreate, &settingDefaults)
	if err != nil {
		return fmt.Errorf("%w: posture readback failed", ErrRuntimeRoleUnsafe)
	}
	var findings []string
	add := func(failed bool, finding string) {
		if failed {
			findings = append(findings, finding)
		}
	}
	add(switched, "session role switched")
	add(superuser, "superuser")
	add(bypassRLS, "bypassrls")
	add(settingDefaults, "app.* setting default")
	if posture == RuntimeRoleHosted {
		add(wrongLogin, "unexpected application login")
		add(administrative, "role/database administration")
		add(inherits, "role inheritance")
		add(membership, "role membership")
		add(ownsDatabase, "database ownership")
		add(ownsObjects, "object ownership")
		add(databaseCreate, "database CREATE/TEMP")
		add(schemaCreate, "schema CREATE")
	}
	if len(findings) > 0 {
		return fmt.Errorf("%w: %s", ErrRuntimeRoleUnsafe, strings.Join(findings, ", "))
	}
	return nil
}

// VerifyHostedRuntimeMigrationMode refuses DDL from a hosted serving process.
// Schema changes run only through the governed database delivery with its own
// migration login; the hosted runtime verifies the ledger and never applies.
func VerifyHostedRuntimeMigrationMode() error {
	if mode := strings.TrimSpace(os.Getenv(EnvDatabaseMigrationMode)); mode != "verify" {
		return fmt.Errorf("hosted runtime requires %s=verify; schema changes run only through the governed database delivery", EnvDatabaseMigrationMode)
	}
	if genesisOptInFromEnv() {
		return fmt.Errorf("hosted runtime refuses database genesis")
	}
	if strings.TrimSpace(os.Getenv(EnvMigrationDatabaseURL)) != "" {
		return fmt.Errorf("hosted runtime must not hold %s", EnvMigrationDatabaseURL)
	}
	return nil
}
