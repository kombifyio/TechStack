package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/url"
	"os"
)

const EnvAdvancedIssuerDatabaseURL = "TECHSTACK_ADVANCED_ISSUER_DATABASE_URL"

// InspectManagedTestIssuer reports only exact-target custody metadata. It
// cannot create a login, change a grant or read/print encrypted private keys.
func InspectManagedTestIssuer(ctx context.Context, a ManagedTestAssignment, adminDSN string) error {
	if err := a.Validate(); err != nil {
		return err
	}
	if err := a.VerifyPin(); err != nil {
		return err
	}
	if _, err := a.Connection(adminDSN, "postgres"); err != nil {
		return err
	}
	u, _ := url.Parse(adminDSN)
	u.Path = "/" + a.Database
	pool, err := sql.Open(PostgresDriverName, u.String())
	if err != nil {
		return errors.New("signing custody inspection connection failed")
	}
	defer pool.Close()
	pool.SetMaxOpenConns(1)
	var name, system, login, owner string
	err = pool.QueryRowContext(ctx, `SELECT current_database(),system_identifier::text,session_user,pg_get_userbyid(d.datdba)
 FROM pg_catalog.pg_control_system(),pg_catalog.pg_database d WHERE d.datname=current_database()`).Scan(&name, &system, &login, &owner)
	if err != nil || name != a.Database || system != a.SystemIdentifier || login != "postgres" || owner != ManagedTestOwner {
		return errors.New("signing custody inspection target differs")
	}
	var roleExists, keyExists bool
	err = pool.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_catalog.pg_roles WHERE rolname=$1),
 EXISTS(SELECT 1 FROM public.advanced_issuer_keys WHERE singleton)`, ManagedTestIssuer).Scan(&roleExists, &keyExists)
	if err != nil {
		return errors.New("signing custody inspection unavailable")
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]any{"schema": "techstack.managed-test-issuer-inspection/v1", "service_id": a.ServiceID, "database": name, "system_identifier": system, "issuer_role": ManagedTestIssuer, "role_exists": roleExists, "key_exists": keyExists, "private_key_read": false, "writes": "none"})
}

// PrepareManagedTestIssuer admits signing custody on the existing assigned
// database. It creates no database or schema and never changes app privileges,
// existing credentials, keys or provider authority.
func PrepareManagedTestIssuer(ctx context.Context, a ManagedTestAssignment, adminDSN, issuerDSN, verifier string) error {
	if err := a.Validate(); err != nil {
		return err
	}
	if err := a.VerifyPin(); err != nil {
		return err
	}
	if _, err := a.Connection(adminDSN, "postgres"); err != nil {
		return err
	}
	issuerConfig, err := a.Connection(issuerDSN, ManagedTestIssuer)
	if err != nil {
		return err
	}
	if len(issuerConfig.Password) < 32 || !PostgresSCRAMSecretMatches(issuerConfig.Password, verifier) {
		return errors.New("managed signing custody requires its assigned SCRAM credential")
	}
	parsed, _ := url.Parse(adminDSN)
	parsed.Path = "/" + a.Database
	admin, err := sql.Open(PostgresDriverName, parsed.String())
	if err != nil {
		return errors.New("managed signing custody administrator connection failed")
	}
	defer admin.Close()
	admin.SetMaxOpenConns(1)
	var name, system, login, owner string
	err = admin.QueryRowContext(ctx, `SELECT current_database(),system_identifier::text,session_user,pg_get_userbyid(d.datdba)
 FROM pg_catalog.pg_control_system(),pg_catalog.pg_database d WHERE d.datname=current_database()`).Scan(&name, &system, &login, &owner)
	if err != nil || name != a.Database || system != a.SystemIdentifier || login != "postgres" || owner != ManagedTestOwner {
		return errors.New("managed signing custody administrative target differs")
	}
	tx, err := admin.BeginTx(ctx, nil)
	if err != nil {
		return issuerPreparationError("begin-transaction", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, migrationAdvisoryLockKey); err != nil {
		return issuerPreparationError("migration-lock", err)
	}
	var tableOwner string
	err = tx.QueryRowContext(ctx, `SELECT pg_get_userbyid(c.relowner) FROM pg_catalog.pg_class c
 JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='public' AND c.relname='advanced_issuer_keys' AND c.relkind='r'`).Scan(&tableOwner)
	if err != nil || tableOwner != ManagedTestOwner {
		return errors.New("managed signing custody requires the reviewed existing key table")
	}
	var appKeyAccess bool
	err = tx.QueryRowContext(ctx, `SELECT has_table_privilege($1,'public.advanced_issuer_keys','SELECT,INSERT,UPDATE,DELETE,TRUNCATE,REFERENCES,TRIGGER')
 OR has_any_column_privilege($1,'public.advanced_issuer_keys','SELECT,INSERT,UPDATE,REFERENCES')`, ManagedTestApp).Scan(&appKeyAccess)
	if err != nil || appKeyAccess {
		return errors.New("managed signing custody refuses existing app key authority")
	}
	if err := prepareAdvancedIssuerKeyLogin(ctx, tx, ManagedTestIssuer, issuerConfig.Password, verifier, a.Database, "public.techstack_test_database_identity()"); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return issuerPreparationError("commit-outcome-requires-inspection", err)
	}
	// Authenticate the actual purpose-bound credential; SET ROLE is not proof.
	runtime, err := sql.Open(PostgresDriverName, issuerDSN)
	if err != nil {
		return errors.New("managed signing custody resulting login unavailable")
	}
	defer runtime.Close()
	return VerifyManagedTestIssuerRuntimePrivileges(ctx, runtime, a)
}
