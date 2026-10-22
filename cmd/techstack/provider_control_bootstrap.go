package main

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
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

var providerControlRuntimeTableGrants = db.ProviderRuntimeTableGrants

var providerControlRuntimeSequences = []string{
	"server_state_transitions_id_seq",
	"server_registry_outbox_id_seq",
}

var providerControlRuntimeFunctions = []string{
	"substrate_guest_custody_valid(text,text,text,text)",
	"substrate_lock_binding(text,text,bigint)",
	"managed_runtime_capacity_policy_digest(text,text,text,text,text,integer,text)",
	"provider_control_lock_runtime_lease_projection(text)",
	"provider_control_count_unsettled_generation_dispatch_guards(text,uuid)",
	"provider_control_list_runnable_tenants(text,integer)",
	"provider_control_list_due_decommission_wait_tenants(text,integer)",
	"provider_control_list_provider_provision_waits(text,text,integer)",
	"provider_control_list_stale_capacity_recovery_candidates(text,text,integer)",
	"provider_control_list_never_enrolled_runtime_candidates(text,text,integer,integer)",
	"provider_control_runtime_authority()",
}

// The existing Render preDeploy command defaults to verification only.
// Privileged changes belong to the normal producer's explicit prepare phase.
func runProviderControlRuntimeBootstrap(ctx context.Context) error {
	if expected := os.Getenv("TECHSTACK_EXPECTED_PREPARATION_SOURCE_SHA"); expected != "" && expected != buildRevision {
		return errors.New("provider preparation artifact differs from the admitted source")
	}
	mode := os.Getenv("TECHSTACK_ADVANCED_ISSUER_PREPARATION_MODE")
	if mode == "prepare" {
		return prepareProviderAndIssuerRuntime(ctx)
	}
	if mode != "" && mode != "verify" {
		return errors.New("unknown provider preparation mode")
	}
	return verifyProviderAndIssuerPreDeploy(ctx)
}

// prepareProviderAndIssuerRuntime runs only in the explicit privileged producer
// phase. It applies migrations, creates the one canonical login only when it
// is absent, reconciles direct grants, then proves the resulting runtime DSN
// through the same posture gate used by normal startup. It never prints a DSN,
// username password, or connection payload.
func prepareProviderAndIssuerRuntime(ctx context.Context) error {
	issuerConfig, err := advancedIssuerBootstrapConfigFromEnv()
	if err != nil {
		return err
	}
	migrationConfig, err := db.MigrationConfigFromEnv()
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
	var retainedIssuerID, retainedKeyID string
	if !installationIssuerGenesisAuthorized() {
		retainedIssuerID, retainedKeyID, err = verifyRetainedIssuerBeforePreparation(ctx, migrationDatabase, issuerConfig)
		if err != nil {
			return err
		}
	}
	previousMode, modePresent := os.LookupEnv(db.EnvDatabaseMigrationMode)
	_ = os.Setenv(db.EnvDatabaseMigrationMode, "apply")
	defer func() {
		if modePresent {
			_ = os.Setenv(db.EnvDatabaseMigrationMode, previousMode)
		} else {
			_ = os.Unsetenv(db.EnvDatabaseMigrationMode)
		}
	}()
	if err := migrationDatabase.Migrate(ctx); err != nil {
		return fmt.Errorf("provider-control runtime bootstrap migrate: %w", err)
	}
	if err := prepareApplicationPrincipal(ctx, migrationDatabase, issuerConfig); err != nil {
		return err
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
	issuerID, keyID, err := prepareAdvancedIssuerFromConfigWithIdentity(ctx, migrationDatabase, issuerConfig)
	if err != nil {
		return err
	}
	if retainedIssuerID != "" && (issuerID != retainedIssuerID || keyID != retainedKeyID) {
		return errors.New("prepared issuer identity differs from the qualified retained identity")
	}
	issuerURL, _ := url.Parse(issuerConfig.issuerDSN)
	return json.NewEncoder(os.Stdout).Encode(map[string]any{"schema": "techstack.credential-preparation/v1", "operation": "prepare", "status": "acknowledged", "source_sha": buildRevision,
		"provider_role": providerControlRuntimeRoleName, "provider_privileges_verified": true, "application_role": issuerConfig.appRole,
		"signing_role": issuerURL.User.Username(), "database_identity": os.Getenv("TECHSTACK_EXPECTED_DATABASE_IDENTITY"),
		"issuer_id": issuerID, "key_id": keyID, "wrapping_verified": true})
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

	statements, statementErr := providerControlRuntimeBaseGrantStatementsTx(ctx, tx, databaseName, schemaName, roleName)
	if statementErr != nil {
		return statementErr
	}
	quotedRole := pgx.Identifier{roleName}.Sanitize()
	quotedSchema := pgx.Identifier{schemaName}.Sanitize()
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("provider-control runtime bootstrap reconcile base grants: %w", err)
		}
	}
	for _, grant := range providerControlRuntimeTableGrants {
		statement := "GRANT " + grant.Privileges + " ON TABLE " +
			pgx.Identifier{schemaName, grant.Table}.Sanitize() + " TO " + quotedRole
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("provider-control runtime bootstrap grant table %s: %w", grant.Table, err)
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

func providerControlRuntimeBaseGrantStatementsTx(
	ctx context.Context,
	tx *sql.Tx,
	databaseName string,
	schemaName string,
	roleName string,
) ([]string, error) {
	quotedRole := pgx.Identifier{roleName}.Sanitize()
	quotedDatabase := pgx.Identifier{databaseName}.Sanitize()
	statements := []string{
		"REVOKE CREATE, TEMPORARY ON DATABASE " + quotedDatabase + " FROM PUBLIC",
		"REVOKE ALL ON DATABASE " + quotedDatabase + " FROM " + quotedRole,
		"GRANT CONNECT ON DATABASE " + quotedDatabase + " TO " + quotedRole,
	}
	applicationStatements, applicationErr := providerControlApplicationSchemaRevocationsTx(ctx, tx, quotedRole)
	if applicationErr != nil {
		return nil, applicationErr
	}
	statements = append(statements, applicationStatements...)
	columnStatements, columnErr := providerControlColumnGrantRevocationsTx(ctx, tx, roleName, quotedRole)
	if columnErr != nil {
		return nil, columnErr
	}
	statements = append(statements, columnStatements...)
	return append(statements, "GRANT USAGE ON SCHEMA "+pgx.Identifier{schemaName}.Sanitize()+" TO "+quotedRole), nil
}

func providerControlApplicationSchemaRevocationsTx(ctx context.Context, tx *sql.Tx, quotedRole string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT nspname
		FROM pg_catalog.pg_namespace
		WHERE nspname <> 'information_schema'
		  AND nspname NOT LIKE 'pg_%'
		ORDER BY nspname
	`)
	if err != nil {
		return nil, fmt.Errorf("provider-control runtime bootstrap list application schemas: %w", err)
	}
	var statements []string
	for rows.Next() {
		var applicationSchema string
		if err := rows.Scan(&applicationSchema); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("provider-control runtime bootstrap scan application schema: %w", err)
		}
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
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("provider-control runtime bootstrap close schema rows: %w", err)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("provider-control runtime bootstrap iterate schemas: %w", err)
	}
	return statements, nil
}

func providerControlColumnGrantRevocationsTx(ctx context.Context, tx *sql.Tx, roleName, quotedRole string) ([]string, error) {
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
		return nil, fmt.Errorf("provider-control runtime bootstrap list excess column grants: %w", err)
	}
	var statements []string
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
			return nil, fmt.Errorf("provider-control runtime bootstrap scan excess column grant: %w", err)
		}
		switch privilege {
		case "SELECT", "INSERT", "UPDATE", "REFERENCES":
		default:
			_ = columnRows.Close()
			return nil, fmt.Errorf("provider-control runtime bootstrap found unsupported column privilege %q", privilege)
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
		return nil, fmt.Errorf("provider-control runtime bootstrap close column grant rows: %w", err)
	}
	if err := columnRows.Err(); err != nil {
		return nil, fmt.Errorf("provider-control runtime bootstrap iterate column grants: %w", err)
	}
	return statements, nil
}
