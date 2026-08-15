package main

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/kombifyio/techstack/pkg/db"
	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/pbkdf2"
	"golang.org/x/text/secure/precis"
)

var errProviderControlRuntimeRoleUnsafe = errors.New("provider-control runtime role is unsafe")

const (
	providerControlSCRAMIterations = 4096
	providerControlSCRAMSaltBytes  = 16
)

type providerControlTableGrant struct {
	table      string
	privileges string
}

var providerControlRuntimeTableGrants = []providerControlTableGrant{
	{table: "provider_catalog_versions", privileges: "SELECT"},
	{table: "provider_catalog_profiles", privileges: "SELECT"},
	{table: "provider_credential_handles", privileges: "SELECT,INSERT"},
	{table: "servers", privileges: "SELECT,INSERT,UPDATE"},
	{table: "server_state_transitions", privileges: "SELECT,INSERT"},
	{table: "server_registry_outbox", privileges: "SELECT,INSERT"},
	{table: "techstack_vm_leases", privileges: "SELECT,INSERT,UPDATE"},
	{table: "runtime_lease_execution_authorities", privileges: "SELECT,INSERT"},
	{table: "runtime_lease_idempotency_records", privileges: "SELECT,INSERT"},
	{table: "managed_runtime_capacity_reservations", privileges: "SELECT,INSERT"},
	{table: "managed_runtime_server_slots", privileges: "SELECT,INSERT"},
	{table: "managed_runtime_server_slot_generations", privileges: "SELECT,INSERT"},
	{table: "managed_runtime_capacity_release_facts", privileges: "SELECT,INSERT"},
	{table: "managed_runtime_capacity_quarantine_retirements", privileges: "SELECT"},
	{table: "provider_desired_spec_revisions", privileges: "SELECT,INSERT,UPDATE"},
	{table: "provider_operations", privileges: "SELECT,INSERT,UPDATE"},
	{table: "provider_async_requests", privileges: "SELECT,INSERT,UPDATE"},
	{table: "provider_operation_receipts", privileges: "SELECT,INSERT"},
	{table: "provider_operation_resources", privileges: "SELECT,INSERT,UPDATE"},
	{table: "provider_operation_evidence", privileges: "SELECT,INSERT,UPDATE"},
	// Absence observations are append-only custody: the adapter records the
	// provider read, the verifier reads it back. No UPDATE, no DELETE.
	{table: "provider_absence_observations", privileges: "SELECT,INSERT"},
	{table: "provider_operation_execution_claims", privileges: "SELECT,INSERT,UPDATE"},
	{table: "provider_provision_dispatch_guards", privileges: "SELECT,INSERT"},
	{table: "provider_provision_resolution_decisions", privileges: "SELECT"},
	{table: "server_provider_resource_bindings", privileges: "SELECT,INSERT"},
	{table: "provider_operation_resource_free_terminalizations", privileges: "SELECT,INSERT"},
	// Managed provisioning records the node's Guard enrolment capability on the
	// dispatch path so the VM can redeem it on first boot. INSERT only: the
	// runtime never reads a capability back, and redemption is a separate
	// authenticated surface.
	{table: "pairing_tokens", privileges: "SELECT,INSERT"},
	// Terminal observation only upserts/reads incidents and enqueues an
	// idempotent workflow run. Advisory advancement remains control-plane-only.
	{table: "provider_incidents", privileges: "SELECT,INSERT"},
	// ON CONFLICT DO NOTHING requires SELECT on the conflict key for the
	// idempotency check; no workflow state mutation is granted.
	{table: "ril_workflow_runs", privileges: "SELECT,INSERT"},
}

var providerControlRuntimeSequences = []string{
	"server_state_transitions_id_seq",
	"server_registry_outbox_id_seq",
}

var providerControlRuntimeFunctions = []string{
	"managed_runtime_capacity_policy_digest(text,text,text,text,text,integer,text)",
	"provider_control_lock_runtime_lease_projection(text)",
	"provider_control_count_unsettled_generation_dispatch_guards(text,uuid)",
	"provider_control_list_runnable_tenants(text,integer)",
	"provider_control_list_due_decommission_wait_tenants(text,integer)",
	"provider_control_list_provider_provision_waits(text,text,integer)",
	"provider_control_list_stale_capacity_recovery_candidates(text,text,integer)",
	"provider_control_runtime_authority()",
}

// runProviderControlRuntimeBootstrap is a deployment-only command. It applies
// migrations with DATABASE_URL, creates the one canonical login only when it
// is absent, reconciles direct grants, then proves the resulting runtime DSN
// through the same posture gate used by normal startup. It never prints a DSN,
// username password, or connection payload.
func runProviderControlRuntimeBootstrap(ctx context.Context) error {
	migrationConfig, err := db.ConfigFromEnv("")
	if err != nil {
		return fmt.Errorf("provider-control runtime bootstrap migration database: %w", err)
	}
	if migrationConfig.Backend != db.StoreBackendPostgres {
		return fmt.Errorf("provider-control runtime bootstrap migration database: %s is required", db.EnvDatabaseURL)
	}
	runtimeConfig, err := providerControlRuntimeDatabaseConfigFromEnv()
	if err != nil {
		return err
	}
	runtimePGXConfig, err := pgx.ParseConfig(runtimeConfig.DSN)
	if err != nil {
		return fmt.Errorf("provider-control runtime bootstrap parse runtime DSN: %w", err)
	}
	if runtimePGXConfig.User != providerControlRuntimeRoleName {
		return fmt.Errorf(
			"provider-control runtime bootstrap: runtime DSN user must be %q",
			providerControlRuntimeRoleName,
		)
	}
	if runtimePGXConfig.Password == "" {
		return fmt.Errorf("provider-control runtime bootstrap: runtime DSN must contain a non-empty secret")
	}

	migrationDatabase, err := db.Open(migrationConfig)
	if err != nil {
		return fmt.Errorf("provider-control runtime bootstrap open migration database: %w", err)
	}
	defer func() { _ = migrationDatabase.Close() }()
	// Enforce the expected-identity pin before any DDL: a reverted DSN must
	// fail this deployment command closed instead of provisioning the wrong
	// database (incident 2026-08-12). Migrate re-checks the same pin.
	if err := db.VerifyExpectedDatabaseIdentity(ctx, migrationDatabase.DB); err != nil {
		return fmt.Errorf("provider-control runtime bootstrap verify expected database identity: %w", err)
	}
	if err := migrationDatabase.Migrate(ctx); err != nil {
		return fmt.Errorf("provider-control runtime bootstrap migrate: %w", err)
	}
	if err := ensureProviderControlRuntimeLogin(
		ctx,
		migrationDatabase.DB,
		providerControlRuntimeRoleName,
		runtimePGXConfig.Password,
	); err != nil {
		return err
	}
	if err := installProviderControlRuntimeGrants(
		ctx,
		migrationDatabase.DB,
		providerControlRuntimeRoleName,
	); err != nil {
		return err
	}

	runtimeDatabase, err := db.Open(runtimeConfig)
	if err != nil {
		return fmt.Errorf("provider-control runtime bootstrap verify runtime login: %w", err)
	}
	defer func() { _ = runtimeDatabase.Close() }()
	if err := verifyProviderControlRuntimeRolePosture(
		ctx,
		runtimeDatabase.DB,
		migrationDatabase.DB,
	); err != nil {
		return err
	}
	return nil
}

func ensureProviderControlRuntimeLogin(
	ctx context.Context,
	database *sql.DB,
	roleName string,
	password string,
) error {
	if database == nil || strings.TrimSpace(roleName) == "" || password == "" {
		return fmt.Errorf("provider-control runtime bootstrap: database, role, and secret are required")
	}
	var (
		canLogin, superuser, inherit, createRole, createDatabase, replication, bypassRLS bool
		membership                                                                       bool
	)
	err := database.QueryRowContext(ctx, `
		SELECT
			role.rolcanlogin,
			role.rolsuper,
			role.rolinherit,
			role.rolcreaterole,
			role.rolcreatedb,
			role.rolreplication,
			role.rolbypassrls,
			EXISTS (
				SELECT 1
				FROM pg_catalog.pg_roles AS granted_role
				WHERE granted_role.oid <> role.oid
				  AND pg_catalog.pg_has_role(role.oid, granted_role.oid, 'MEMBER')
			)
		FROM pg_catalog.pg_roles AS role
		WHERE role.rolname = $1
	`, roleName).Scan(
		&canLogin,
		&superuser,
		&inherit,
		&createRole,
		&createDatabase,
		&replication,
		&bypassRLS,
		&membership,
	)
	if errors.Is(err, sql.ErrNoRows) {
		verifier, verifierErr := postgresSCRAMVerifier(password)
		if verifierErr != nil {
			return fmt.Errorf("provider-control runtime bootstrap derive login verifier: %w", verifierErr)
		}
		var statement string
		if err := database.QueryRowContext(ctx, `
			SELECT pg_catalog.format(
				'CREATE ROLE %I LOGIN PASSWORD %L NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOBYPASSRLS NOREPLICATION',
				$1::text,
				$2::text
			)
		`, roleName, verifier).Scan(&statement); err != nil {
			return fmt.Errorf("provider-control runtime bootstrap prepare login: %w", err)
		}
		if _, err := database.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("provider-control runtime bootstrap create login: %w", err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("provider-control runtime bootstrap inspect login: %w", err)
	}
	if !canLogin || superuser || inherit || createRole || createDatabase || replication || bypassRLS || membership {
		return fmt.Errorf("%w: existing %q login has forbidden attributes or memberships", errProviderControlRuntimeRoleUnsafe, roleName)
	}
	return nil
}

// postgresSCRAMVerifier derives the exact PostgreSQL SCRAM-SHA-256 storage
// form before any SQL is sent. Server-side DDL/audit logging can therefore see
// at most a randomly salted verifier, never the runtime login's raw secret.
func postgresSCRAMVerifier(password string) (string, error) {
	if password == "" {
		return "", fmt.Errorf("non-empty password is required")
	}
	// Match pgx/PostgreSQL SCRAM authentication exactly. PostgreSQL permits
	// strings rejected by SASLprep, so the client intentionally falls back to
	// the original bytes when the OpaqueString profile cannot normalize them.
	if normalized, err := precis.OpaqueString.String(password); err == nil {
		password = normalized
	}
	salt := make([]byte, providerControlSCRAMSaltBytes)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	saltedPassword := pbkdf2.Key(
		[]byte(password),
		salt,
		providerControlSCRAMIterations,
		sha256.Size,
		sha256.New,
	)
	defer clear(saltedPassword)
	clientMAC := hmac.New(sha256.New, saltedPassword)
	_, _ = clientMAC.Write([]byte("Client Key"))
	clientKey := clientMAC.Sum(nil)
	defer clear(clientKey)
	storedKey := sha256.Sum256(clientKey)
	serverMAC := hmac.New(sha256.New, saltedPassword)
	_, _ = serverMAC.Write([]byte("Server Key"))
	serverKey := serverMAC.Sum(nil)
	defer clear(serverKey)

	return fmt.Sprintf(
		"SCRAM-SHA-256$%d:%s$%s:%s",
		providerControlSCRAMIterations,
		base64.StdEncoding.EncodeToString(salt),
		base64.StdEncoding.EncodeToString(storedKey[:]),
		base64.StdEncoding.EncodeToString(serverKey),
	), nil
}

func installProviderControlRuntimeGrants(ctx context.Context, database *sql.DB, roleName string) error {
	if database == nil || roleName != providerControlRuntimeRoleName {
		return fmt.Errorf("provider-control runtime bootstrap: exact canonical role is required")
	}
	var databaseName, schemaName, migrationRole string
	if err := database.QueryRowContext(ctx, `
		SELECT current_database(), current_schema(), current_user
	`).Scan(&databaseName, &schemaName, &migrationRole); err != nil {
		return fmt.Errorf("provider-control runtime bootstrap load migration identity: %w", err)
	}
	if databaseName == "" || schemaName == "" || migrationRole == "" || migrationRole == roleName ||
		schemaName == "information_schema" || strings.HasPrefix(schemaName, "pg_") {
		return fmt.Errorf("provider-control runtime bootstrap: unsafe database, schema, or role identity")
	}

	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("provider-control runtime bootstrap begin grant transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, "SET LOCAL lock_timeout = '5s'"); err != nil {
		return fmt.Errorf("provider-control runtime bootstrap bound grant lock: %w", err)
	}

	quotedRole := pgx.Identifier{roleName}.Sanitize()
	quotedDatabase := pgx.Identifier{databaseName}.Sanitize()
	quotedSchema := pgx.Identifier{schemaName}.Sanitize()
	statements := []string{
		"REVOKE CREATE, TEMPORARY ON DATABASE " + quotedDatabase + " FROM PUBLIC",
		"REVOKE ALL ON DATABASE " + quotedDatabase + " FROM " + quotedRole,
		"GRANT CONNECT ON DATABASE " + quotedDatabase + " TO " + quotedRole,
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT nspname
		FROM pg_catalog.pg_namespace
		WHERE nspname <> 'information_schema'
		  AND nspname NOT LIKE 'pg_%'
		ORDER BY nspname
	`)
	if err != nil {
		return fmt.Errorf("provider-control runtime bootstrap list application schemas: %w", err)
	}
	var applicationSchemas []string
	for rows.Next() {
		var applicationSchema string
		if err := rows.Scan(&applicationSchema); err != nil {
			_ = rows.Close()
			return fmt.Errorf("provider-control runtime bootstrap scan application schema: %w", err)
		}
		applicationSchemas = append(applicationSchemas, applicationSchema)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("provider-control runtime bootstrap close schema rows: %w", err)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("provider-control runtime bootstrap iterate schemas: %w", err)
	}
	for _, applicationSchema := range applicationSchemas {
		quotedApplicationSchema := pgx.Identifier{applicationSchema}.Sanitize()
		statements = append(statements,
			"REVOKE CREATE ON SCHEMA "+quotedApplicationSchema+" FROM PUBLIC",
			"REVOKE ALL ON SCHEMA "+quotedApplicationSchema+" FROM "+quotedRole,
			"REVOKE ALL ON ALL TABLES IN SCHEMA "+quotedApplicationSchema+" FROM PUBLIC",
			"REVOKE ALL ON ALL TABLES IN SCHEMA "+quotedApplicationSchema+" FROM "+quotedRole,
			"REVOKE ALL ON ALL SEQUENCES IN SCHEMA "+quotedApplicationSchema+" FROM PUBLIC",
			"REVOKE ALL ON ALL SEQUENCES IN SCHEMA "+quotedApplicationSchema+" FROM "+quotedRole,
			"REVOKE ALL ON ALL FUNCTIONS IN SCHEMA "+quotedApplicationSchema+" FROM "+quotedRole,
			"REVOKE EXECUTE ON ALL FUNCTIONS IN SCHEMA "+quotedApplicationSchema+" FROM PUBLIC",
		)
	}
	columnRows, err := tx.QueryContext(ctx, `
		SELECT
			acl.privilege_type,
			namespace.nspname,
			object.relname,
			attribute.attname,
			acl.grantee = 0 AS granted_to_public
		FROM pg_catalog.pg_attribute AS attribute
		JOIN pg_catalog.pg_class AS object ON object.oid = attribute.attrelid
		JOIN pg_catalog.pg_namespace AS namespace ON namespace.oid = object.relnamespace
		CROSS JOIN LATERAL pg_catalog.aclexplode(attribute.attacl) AS acl
		WHERE attribute.attnum > 0
		  AND NOT attribute.attisdropped
		  AND namespace.nspname <> 'information_schema'
		  AND namespace.nspname NOT LIKE 'pg_%'
		  AND (
			acl.grantee = 0
			OR acl.grantee = (SELECT oid FROM pg_catalog.pg_roles WHERE rolname = $1)
		  )
		ORDER BY namespace.nspname, object.relname, attribute.attnum, acl.privilege_type
	`, roleName)
	if err != nil {
		return fmt.Errorf("provider-control runtime bootstrap list excess column grants: %w", err)
	}
	for columnRows.Next() {
		var privilege, columnSchema, tableName, columnName string
		var grantedToPublic bool
		if err := columnRows.Scan(
			&privilege,
			&columnSchema,
			&tableName,
			&columnName,
			&grantedToPublic,
		); err != nil {
			_ = columnRows.Close()
			return fmt.Errorf("provider-control runtime bootstrap scan excess column grant: %w", err)
		}
		switch privilege {
		case "SELECT", "INSERT", "UPDATE", "REFERENCES":
		default:
			_ = columnRows.Close()
			return fmt.Errorf("provider-control runtime bootstrap found unsupported column privilege %q", privilege)
		}
		grantee := quotedRole
		if grantedToPublic {
			grantee = "PUBLIC"
		}
		statements = append(statements,
			"REVOKE "+privilege+" ("+pgx.Identifier{columnName}.Sanitize()+") ON TABLE "+
				pgx.Identifier{columnSchema, tableName}.Sanitize()+" FROM "+grantee,
		)
	}
	if err := columnRows.Close(); err != nil {
		return fmt.Errorf("provider-control runtime bootstrap close column grant rows: %w", err)
	}
	if err := columnRows.Err(); err != nil {
		return fmt.Errorf("provider-control runtime bootstrap iterate column grants: %w", err)
	}
	statements = append(statements, "GRANT USAGE ON SCHEMA "+quotedSchema+" TO "+quotedRole)
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("provider-control runtime bootstrap reconcile base grants: %w", err)
		}
	}
	for _, grant := range providerControlRuntimeTableGrants {
		statement := "GRANT " + grant.privileges + " ON TABLE " +
			pgx.Identifier{schemaName, grant.table}.Sanitize() + " TO " + quotedRole
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("provider-control runtime bootstrap grant table %s: %w", grant.table, err)
		}
	}
	for _, sequence := range providerControlRuntimeSequences {
		statement := "GRANT USAGE ON SEQUENCE " +
			pgx.Identifier{schemaName, sequence}.Sanitize() + " TO " + quotedRole
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("provider-control runtime bootstrap grant sequence %s: %w", sequence, err)
		}
	}
	for _, function := range providerControlRuntimeFunctions {
		statement := "GRANT EXECUTE ON FUNCTION " + quotedSchema + "." + function + " TO " + quotedRole
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("provider-control runtime bootstrap grant function %s: %w", function, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("provider-control runtime bootstrap commit grants: %w", err)
	}
	return nil
}

func providerControlBootstrapRequested(args []string) bool {
	return len(args) > 1 && strings.TrimSpace(args[1]) == "provider-control-bootstrap"
}

func providerControlBootstrapEnvironmentConfigured() bool {
	return strings.TrimSpace(os.Getenv(db.EnvDatabaseURL)) != "" &&
		strings.TrimSpace(os.Getenv(providerControlRuntimeDatabaseURLEnv)) != ""
}
