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
	// RuntimeRoleHosted is every hosted runtime on the one live database
	// (stable and the Preview channel). D-2026-10-22-02: tenant isolation holds
	// by construction, so the serving login is a bounded application role.
	RuntimeRoleHosted
)

// ErrRuntimeRoleUnsafe is the mandatory boot refusal. There is no override.
var ErrRuntimeRoleUnsafe = errors.New("runtime database login is not a bounded application role")

// VerifyRuntimeRolePosture is the mandatory boot assertion on the serving
// login. Every posture refuses a superuser, a BYPASSRLS login, a login
// switched to another role, and any role- or database-level default for an
// app.* setting (a default tenant or scope would widen RLS for every query).
// The hosted posture also refuses ownership of the database or any object,
// CREATE/TEMP on the database, CREATE on any schema, role administration and
// any role membership, so the login can neither run DDL nor SET ROLE.
// Findings name only the failed property, never a role name or DSN.
func VerifyRuntimeRolePosture(ctx context.Context, q PrivilegeQuerier, posture RuntimeRolePosture) error {
	if q == nil {
		return ErrRuntimeRoleUnsafe
	}
	var (
		switched, superuser, bypassRLS, administrative, membership               bool
		ownsDatabase, ownsObjects, databaseCreate, schemaCreate, settingDefaults bool
	)
	err := q.QueryRowContext(ctx, `SELECT
	 current_user<>session_user,
	 r.rolsuper,
	 r.rolbypassrls,
	 r.rolcreaterole OR r.rolcreatedb OR r.rolreplication,
	 EXISTS(SELECT 1 FROM pg_catalog.pg_auth_members m WHERE m.member=r.oid),
	 EXISTS(SELECT 1 FROM pg_catalog.pg_database d WHERE d.datdba=r.oid),
	 EXISTS(SELECT 1 FROM pg_catalog.pg_namespace n WHERE n.nspowner=r.oid)
	  OR EXISTS(SELECT 1 FROM pg_catalog.pg_class c WHERE c.relowner=r.oid)
	  OR EXISTS(SELECT 1 FROM pg_catalog.pg_proc p WHERE p.proowner=r.oid)
	  OR EXISTS(SELECT 1 FROM pg_catalog.pg_type t WHERE t.typowner=r.oid),
	 has_database_privilege(r.oid,current_database(),'CREATE') OR has_database_privilege(r.oid,current_database(),'TEMP'),
	 EXISTS(SELECT 1 FROM pg_catalog.pg_namespace n WHERE n.nspname<>'information_schema' AND n.nspname NOT LIKE 'pg\_%'
	  AND has_schema_privilege(r.oid,n.oid,'CREATE')),
	 EXISTS(SELECT 1 FROM pg_catalog.pg_db_role_setting s CROSS JOIN LATERAL unnest(s.setconfig) setting
	  WHERE s.setrole IN (0,r.oid)
	  AND s.setdatabase IN (0,(SELECT oid FROM pg_catalog.pg_database WHERE datname=current_database()))
	  AND lower(setting) LIKE 'app.%')
	 FROM pg_catalog.pg_roles r WHERE r.rolname=current_user`).Scan(
		&switched, &superuser, &bypassRLS, &administrative, &membership,
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
		add(administrative, "role/database administration")
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
