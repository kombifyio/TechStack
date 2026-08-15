package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kombifyio/techstack/internal/localdb"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/stdlib"
	"golang.org/x/text/secure/precis"
)

func TestIntegrationProviderControlRuntimeBootstrapIsIdempotentAndLeastPrivilege(t *testing.T) {
	if strings.TrimSpace(os.Getenv("TECHSTACK_PROVIDERCONTROL_EMBEDDED_POSTGRES")) != "1" {
		t.Skip("TECHSTACK_PROVIDERCONTROL_EMBEDDED_POSTGRES=1 is required")
	}
	baseDir := t.TempDir()
	t.Setenv(localdb.EnvEmbeddedPostgresDir, filepath.Join(baseDir, "postgres"))
	embedded, err := localdb.StartEmbeddedPostgres(baseDir)
	if err != nil {
		t.Fatalf("start embedded PostgreSQL: %v", err)
	}
	t.Cleanup(func() { _ = embedded.Stop() })

	adminConfig, err := pgx.ParseConfig(embedded.DSN())
	if err != nil {
		t.Fatalf("parse embedded PostgreSQL DSN: %v", err)
	}
	adminDatabase := stdlib.OpenDB(*adminConfig)
	t.Cleanup(func() { _ = adminDatabase.Close() })
	const migrationRole = "techstack_provider_control_migration_test"
	migrationPassword := "migration-" + strings.ReplaceAll(t.Name(), "/", "-")
	migrationVerifier, err := postgresSCRAMVerifier(migrationPassword)
	if err != nil {
		t.Fatalf("derive migration role verifier: %v", err)
	}
	var databaseName, createMigrationRole string
	if err := adminDatabase.QueryRowContext(t.Context(), `
		SELECT current_database(), pg_catalog.format(
			'CREATE ROLE %I LOGIN PASSWORD %L NOSUPERUSER NOCREATEDB CREATEROLE NOINHERIT NOBYPASSRLS NOREPLICATION',
			$1::text,
			$2::text
		)
	`, migrationRole, migrationVerifier).Scan(&databaseName, &createMigrationRole); err != nil {
		t.Fatalf("prepare migration role: %v", err)
	}
	if _, err := adminDatabase.ExecContext(t.Context(), createMigrationRole); err != nil {
		t.Fatalf("create non-superuser migration role: %v", err)
	}
	quotedMigrationRole := pgx.Identifier{migrationRole}.Sanitize()
	if _, err := adminDatabase.ExecContext(t.Context(),
		"ALTER DATABASE "+pgx.Identifier{databaseName}.Sanitize()+" OWNER TO "+quotedMigrationRole); err != nil {
		t.Fatalf("bind migration database owner: %v", err)
	}
	if _, err := adminDatabase.ExecContext(t.Context(),
		"GRANT EXECUTE ON FUNCTION pg_catalog.pg_control_system() TO "+quotedMigrationRole); err != nil {
		t.Fatalf("grant read-only physical cluster identity: %v", err)
	}
	migrationURL, err := url.Parse(embedded.DSN())
	if err != nil {
		t.Fatalf("parse migration URL: %v", err)
	}
	migrationURL.User = url.UserPassword(migrationRole, migrationPassword)
	migrationQuery := migrationURL.Query()
	migrationQuery.Set("search_path", "public")
	migrationURL.RawQuery = migrationQuery.Encode()
	migrationConfig, err := pgx.ParseConfig(migrationURL.String())
	if err != nil {
		t.Fatalf("parse migration PostgreSQL DSN: %v", err)
	}
	runtimeURL, err := url.Parse(embedded.DSN())
	if err != nil {
		t.Fatalf("parse runtime URL: %v", err)
	}
	runtimePassword := "runtime-\u212b-" + strings.ReplaceAll(t.Name(), "/", "-")
	normalizedRuntimePassword, err := precis.OpaqueString.String(runtimePassword)
	if err != nil || normalizedRuntimePassword == runtimePassword {
		t.Fatalf("Unicode runtime password fixture must exercise SCRAM normalization: normalized=%q error=%v", normalizedRuntimePassword, err)
	}
	runtimeURL.User = url.UserPassword(providerControlRuntimeRoleName, runtimePassword)
	runtimeQuery := runtimeURL.Query()
	runtimeQuery.Set("search_path", "public")
	runtimeURL.RawQuery = runtimeQuery.Encode()
	runtimeConfig, err := pgx.ParseConfig(runtimeURL.String())
	if err != nil {
		t.Fatalf("parse runtime PostgreSQL DSN: %v", err)
	}
	t.Setenv("DATABASE_URL", migrationURL.String())
	t.Setenv(providerControlRuntimeDatabaseURLEnv, runtimeURL.String())
	t.Setenv("TECHSTACK_STORE_BACKEND", "postgres")

	for attempt := 1; attempt <= 2; attempt++ {
		if err := runProviderControlRuntimeBootstrap(t.Context()); err != nil {
			t.Fatalf("bootstrap attempt %d: %v", attempt, err)
		}
	}

	if _, err := adminDatabase.ExecContext(t.Context(), `
		INSERT INTO techstack_tenants (id, display_name, kind, status)
		VALUES ('tenant-bootstrap', 'Bootstrap Test', 'saas', 'active');
		INSERT INTO provider_control_runnable_tenants (tenant_id, runnable_operation_count)
		VALUES ('tenant-bootstrap', 1);
	`); err != nil {
		t.Fatalf("seed bounded tenant directory: %v", err)
	}

	runtimeDatabase := stdlib.OpenDB(*runtimeConfig)
	t.Cleanup(func() { _ = runtimeDatabase.Close() })
	if err := runtimeDatabase.PingContext(t.Context()); err != nil {
		t.Fatalf("ping bootstrapped runtime role: %v", err)
	}
	var capacityPolicyDigest string
	if err := runtimeDatabase.QueryRowContext(t.Context(), `
		SELECT managed_runtime_capacity_policy_digest(
			'tenant-bootstrap',
			'ionos',
			'owner_subject',
			'owner-bootstrap',
			'limited',
			3,
			'edge_v2_entitlement+signed_budget:cloud.runtime.credits#managed_servers'
		)
	`).Scan(&capacityPolicyDigest); err != nil {
		t.Fatalf("call managed runtime capacity policy digest: %v", err)
	}
	if !strings.HasPrefix(capacityPolicyDigest, "sha256:") {
		t.Fatalf("managed runtime capacity policy digest = %q", capacityPolicyDigest)
	}
	var tenantID string
	if err := runtimeDatabase.QueryRowContext(t.Context(), `
		SELECT tenant_id FROM provider_control_list_runnable_tenants('', 2)
	`).Scan(&tenantID); err != nil {
		t.Fatalf("call bounded tenant discovery: %v", err)
	}
	if tenantID != "tenant-bootstrap" {
		t.Fatalf("bounded tenant = %q", tenantID)
	}
	if _, err := runtimeDatabase.ExecContext(t.Context(), `SELECT * FROM provider_control_runnable_tenants`); err == nil {
		t.Fatal("bootstrapped runtime role can read the raw tenant directory")
	}

	assertRuntimeRoleAttributes(t, runtimeDatabase)

	migrationDatabase := stdlib.OpenDB(*migrationConfig)
	t.Cleanup(func() { _ = migrationDatabase.Close() })
	if err := verifyProviderControlRuntimeRolePosture(t.Context(), runtimeDatabase, migrationDatabase); err != nil {
		t.Fatalf("verify non-superuser migration/runtime posture: %v", err)
	}

	assertProviderControlPostureRejectsRoleSwitch(t, adminConfig, runtimeDatabase, migrationDatabase)
	assertProviderControlPostureRejectsCrossSchemaAuthority(t, adminDatabase, runtimeDatabase, migrationDatabase)
	assertProviderControlBootstrapRemovesDelegableAndColumnAuthority(t, runtimeDatabase, migrationDatabase)
	assertProviderControlPostureRejectsIncidentCapabilityDrift(t, migrationDatabase, runtimeDatabase)
	assertProviderControlRuntimeRolePersistsIncidentsAndWorkflowRuns(t, runtimeDatabase, migrationDatabase)
	assertProviderControlPostureRejectsMissingFunctionSearchPath(t, adminDatabase, runtimeDatabase, migrationDatabase)
}

func assertProviderControlPostureRejectsRoleSwitch(
	t *testing.T,
	adminConfig *pgx.ConnConfig,
	runtimeDatabase *sql.DB,
	migrationDatabase *sql.DB,
) {
	t.Helper()
	switched := adminConfig.Copy()
	switched.RuntimeParams = map[string]string{
		"search_path": "public",
		"role":        providerControlRuntimeRoleName,
	}
	switchedDatabase := stdlib.OpenDB(*switched)
	t.Cleanup(func() { _ = switchedDatabase.Close() })
	if err := switchedDatabase.PingContext(t.Context()); err != nil {
		t.Fatalf("open privileged session with runtime current role: %v", err)
	}
	if err := verifyProviderControlRuntimeRolePosture(t.Context(), switchedDatabase, migrationDatabase); err == nil ||
		!strings.Contains(err.Error(), "authenticated session role") {
		t.Fatalf("role-switched runtime posture error = %v", err)
	}
	if err := verifyProviderControlRuntimeRolePosture(t.Context(), runtimeDatabase, migrationDatabase); err != nil {
		t.Fatalf("canonical runtime posture after role-switch probe: %v", err)
	}
}

func assertProviderControlPostureRejectsCrossSchemaAuthority(
	t *testing.T,
	adminDatabase *sql.DB,
	runtimeDatabase *sql.DB,
	migrationDatabase *sql.DB,
) {
	t.Helper()
	quotedRole := pgx.Identifier{providerControlRuntimeRoleName}.Sanitize()
	if _, err := adminDatabase.ExecContext(t.Context(), fmt.Sprintf(`
		CREATE SCHEMA provider_control_shadow;
		CREATE TABLE provider_control_shadow.runtime_escape (value text);
		CREATE FUNCTION provider_control_shadow.runtime_escape()
		RETURNS text LANGUAGE sql SECURITY DEFINER AS 'SELECT current_user::text';
		GRANT USAGE ON SCHEMA provider_control_shadow TO %s;
		GRANT SELECT ON provider_control_shadow.runtime_escape TO %s;
		GRANT EXECUTE ON FUNCTION provider_control_shadow.runtime_escape() TO %s;
	`, quotedRole, quotedRole, quotedRole)); err != nil {
		t.Fatalf("seed cross-schema excess authority: %v", err)
	}
	if err := verifyProviderControlRuntimeRolePosture(t.Context(), runtimeDatabase, migrationDatabase); err == nil ||
		!strings.Contains(err.Error(), "outside the exact table, sequence, or function allowlist") {
		t.Fatalf("cross-schema posture error = %v", err)
	}
	if _, err := adminDatabase.ExecContext(t.Context(), `DROP SCHEMA provider_control_shadow CASCADE`); err != nil {
		t.Fatalf("drop cross-schema authority fixture: %v", err)
	}
	if err := verifyProviderControlRuntimeRolePosture(t.Context(), runtimeDatabase, migrationDatabase); err != nil {
		t.Fatalf("runtime posture after cross-schema cleanup: %v", err)
	}
}

func assertProviderControlPostureRejectsMissingFunctionSearchPath(
	t *testing.T,
	adminDatabase *sql.DB,
	runtimeDatabase *sql.DB,
	migrationDatabase *sql.DB,
) {
	t.Helper()
	if _, err := adminDatabase.ExecContext(t.Context(), `
		ALTER FUNCTION public.provider_execution_claim_current_head() RESET search_path
	`); err != nil {
		t.Fatalf("remove one SECURITY DEFINER search path: %v", err)
	}
	if err := verifyProviderControlRuntimeRolePosture(t.Context(), runtimeDatabase, migrationDatabase); err == nil ||
		!strings.Contains(err.Error(), "SECURITY DEFINER") {
		t.Fatalf("missing SECURITY DEFINER search-path posture error = %v", err)
	}
	if _, err := adminDatabase.ExecContext(t.Context(), `
		ALTER FUNCTION public.provider_execution_claim_current_head()
		SET search_path TO pg_catalog, public, pg_temp
	`); err != nil {
		t.Fatalf("restore SECURITY DEFINER search path: %v", err)
	}
	if err := verifyProviderControlRuntimeRolePosture(t.Context(), runtimeDatabase, migrationDatabase); err != nil {
		t.Fatalf("runtime posture after search-path repair: %v", err)
	}
}

func assertProviderControlBootstrapRemovesDelegableAndColumnAuthority(
	t *testing.T,
	runtimeDatabase *sql.DB,
	migrationDatabase *sql.DB,
) {
	t.Helper()
	quotedRole := pgx.Identifier{providerControlRuntimeRoleName}.Sanitize()
	if _, err := migrationDatabase.ExecContext(t.Context(),
		"GRANT SELECT ON TABLE public.provider_catalog_versions TO "+quotedRole+" WITH GRANT OPTION"); err != nil {
		t.Fatalf("seed delegable runtime authority: %v", err)
	}
	if err := verifyProviderControlRuntimeRolePosture(t.Context(), runtimeDatabase, migrationDatabase); err == nil ||
		!strings.Contains(err.Error(), "outside the exact table, sequence, or function allowlist") {
		t.Fatalf("delegable authority posture error = %v", err)
	}
	if err := installProviderControlRuntimeGrants(t.Context(), migrationDatabase, providerControlRuntimeRoleName); err != nil {
		t.Fatalf("reconcile delegable runtime authority: %v", err)
	}
	if err := verifyProviderControlRuntimeRolePosture(t.Context(), runtimeDatabase, migrationDatabase); err != nil {
		t.Fatalf("runtime posture after delegable grant cleanup: %v", err)
	}

	if _, err := migrationDatabase.ExecContext(t.Context(),
		"GRANT UPDATE (status) ON TABLE public.provider_catalog_versions TO "+quotedRole); err != nil {
		t.Fatalf("seed column-level runtime authority: %v", err)
	}
	if err := verifyProviderControlRuntimeRolePosture(t.Context(), runtimeDatabase, migrationDatabase); err == nil ||
		!strings.Contains(err.Error(), "outside the exact table, sequence, or function allowlist") {
		t.Fatalf("column authority posture error = %v", err)
	}
	if err := installProviderControlRuntimeGrants(t.Context(), migrationDatabase, providerControlRuntimeRoleName); err != nil {
		t.Fatalf("reconcile column-level runtime authority: %v", err)
	}
	if err := verifyProviderControlRuntimeRolePosture(t.Context(), runtimeDatabase, migrationDatabase); err != nil {
		t.Fatalf("runtime posture after column grant cleanup: %v", err)
	}
}

func assertProviderControlPostureRejectsIncidentCapabilityDrift(
	t *testing.T,
	migrationDatabase *sql.DB,
	runtimeDatabase *sql.DB,
) {
	t.Helper()
	quotedRole := pgx.Identifier{providerControlRuntimeRoleName}.Sanitize()
	if _, err := migrationDatabase.ExecContext(t.Context(),
		"REVOKE INSERT ON TABLE public.provider_incidents FROM "+quotedRole); err != nil {
		t.Fatalf("revoke provider incident insert capability: %v", err)
	}
	if err := verifyProviderControlRuntimeRolePosture(t.Context(), runtimeDatabase, migrationDatabase); err == nil ||
		!strings.Contains(err.Error(), "missing an exact provider-control runtime capability") {
		t.Fatalf("missing incident insert capability posture error = %v", err)
	}
	if err := installProviderControlRuntimeGrants(t.Context(), migrationDatabase, providerControlRuntimeRoleName); err != nil {
		t.Fatalf("restore missing provider incident insert capability: %v", err)
	}
	if err := verifyProviderControlRuntimeRolePosture(t.Context(), runtimeDatabase, migrationDatabase); err != nil {
		t.Fatalf("runtime posture after incident capability restore: %v", err)
	}

	if _, err := migrationDatabase.ExecContext(t.Context(),
		"GRANT UPDATE ON TABLE public.provider_incidents TO "+quotedRole); err != nil {
		t.Fatalf("grant excess provider incident update capability: %v", err)
	}
	if err := verifyProviderControlRuntimeRolePosture(t.Context(), runtimeDatabase, migrationDatabase); err == nil ||
		!strings.Contains(err.Error(), "outside the exact table, sequence, or function allowlist") {
		t.Fatalf("excess incident update capability posture error = %v", err)
	}
	if err := installProviderControlRuntimeGrants(t.Context(), migrationDatabase, providerControlRuntimeRoleName); err != nil {
		t.Fatalf("reconcile excess provider incident update capability: %v", err)
	}
	if err := verifyProviderControlRuntimeRolePosture(t.Context(), runtimeDatabase, migrationDatabase); err != nil {
		t.Fatalf("runtime posture after incident excess capability cleanup: %v", err)
	}
}

func assertProviderControlRuntimeRolePersistsIncidentsAndWorkflowRuns(t *testing.T, runtimeDatabase, migrationDatabase *sql.DB) {
	t.Helper()
	incidentKey := "incident-bootstrap"
	incidentTx, err := runtimeDatabase.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatalf("begin runtime incident transaction: %v", err)
	}
	defer func() { _ = incidentTx.Rollback() }()
	if _, err := incidentTx.ExecContext(t.Context(), `
		SELECT set_config('app.tenant_id', $1, true)
	`, "tenant-bootstrap"); err != nil {
		t.Fatalf("set runtime tenant authority: %v", err)
	}
	var createdAt time.Time
	if err := incidentTx.QueryRowContext(t.Context(), `
		INSERT INTO provider_incidents
			(tenant_id, incident_key, lease_id, operation_id, resource_generation_id,
			 provider_id, adapter_id, stage, receipt_sequence, receipt_digest,
			 reason_code, retryable, correlation_id, classification, advisor_state,
			 evidence_json, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,'unknown','pending',$14::jsonb,clock_timestamp(),clock_timestamp())
		ON CONFLICT (tenant_id, incident_key) DO NOTHING
		RETURNING created_at
	`, "tenant-bootstrap", incidentKey, "lease-bootstrap", "operation-bootstrap", "generation-bootstrap",
		"ionos", "ionos-v1", "provision", 1, "digest-bootstrap", "unknown_terminal_failure", true,
		"operation-bootstrap", `{"tenant_id":"tenant-bootstrap"}`).Scan(&createdAt); err != nil {
		t.Fatalf("runtime role incident upsert insert: %v", err)
	}
	if err := incidentTx.Commit(); err != nil {
		t.Fatalf("commit runtime incident transaction: %v", err)
	}

	runID := "provider-incident-" + incidentKey
	if _, err := migrationDatabase.ExecContext(t.Context(), `
		INSERT INTO ril_workflow_runs
			(run_id, type, status, current_step, input, context, owner_id, server_id,
			 card_id, awaiting_signal, error, created, updated)
		VALUES ($1,$2,'pending',0,$3::jsonb,'{}'::jsonb,$4,'','','','',now(),now())
		ON CONFLICT (run_id) DO NOTHING
	`, runID, "provider_incident_advisory", `{"evidence":{"tenant_id":"tenant-bootstrap"}}`, "tenant-bootstrap"); err != nil {
		t.Fatalf("seed workflow run prerequisite: %v", err)
	}
	_, execErr := runtimeDatabase.ExecContext(t.Context(), `
		UPDATE provider_incidents
		SET advisor_state='completed'
		WHERE tenant_id=$1 AND incident_key=$2
	`, "tenant-bootstrap", incidentKey)
	if denialErr := requireRuntimePermissionDenied(execErr); denialErr != nil {
		t.Fatalf("runtime incident advisory projection denial: %v", denialErr)
	}
	_, execErr = runtimeDatabase.ExecContext(t.Context(), `
		INSERT INTO provider_incident_advisory_attempts
		(tenant_id, incident_key, attempt, state, advisory_json,
		 error_classification, workflow_run_id, created_at)
		VALUES ($1,$2,1,'completed','{}'::jsonb,'',$3,clock_timestamp())
	`, "tenant-bootstrap", incidentKey, runID)
	if denialErr := requireRuntimePermissionDenied(execErr); denialErr != nil {
		t.Fatalf("runtime advisory attempt denial: %v", denialErr)
	}
	_, execErr = runtimeDatabase.ExecContext(t.Context(), `
		INSERT INTO ril_workflow_steps
		(id, run_id, step_index, name, status, attempt, idempotency_key, created, updated)
		VALUES ($1,$2,0,'request_advisory','pending',0,$3,now(),now())
	`, runID+":0", runID, runID+":0")
	if denialErr := requireRuntimePermissionDenied(execErr); denialErr != nil {
		t.Fatalf("runtime workflow step denial: %v", denialErr)
	}
	_, execErr = runtimeDatabase.ExecContext(t.Context(), `
		INSERT INTO ril_workflow_timers
		(id, run_id, kind, fire_at, signal_key, created)
		VALUES ($1,$2,'escalation',now(),'incident-signal',now())
	`, runID+":timer", runID)
	if denialErr := requireRuntimePermissionDenied(execErr); denialErr != nil {
		t.Fatalf("runtime workflow timer denial: %v", denialErr)
	}
	_, execErr = runtimeDatabase.ExecContext(t.Context(), `
		SELECT tenant_id
		FROM provider_incident_list_tenant_ids()
	`)
	if denialErr := requireRuntimePermissionDenied(execErr); denialErr != nil {
		t.Fatalf("runtime incident tenant enumerator denial: %v", denialErr)
	}
}

func requireRuntimePermissionDenied(err error) error {
	if err == nil {
		return errors.New("operation unexpectedly succeeded")
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "42501" {
		return fmt.Errorf("expected SQLSTATE 42501, got %w", err)
	}
	return nil
}

func assertRuntimeRoleAttributes(t *testing.T, database *sql.DB) {
	t.Helper()
	var (
		roleName                                                               string
		superuser, inherit, createRole, createDatabase, replication, bypassRLS bool
		temporary, schemaCreate                                                bool
	)
	if err := database.QueryRowContext(context.Background(), `
		SELECT
			current_user,
			role.rolsuper,
			role.rolinherit,
			role.rolcreaterole,
			role.rolcreatedb,
			role.rolreplication,
			role.rolbypassrls,
			pg_catalog.has_database_privilege(current_user, current_database(), 'TEMP'),
			pg_catalog.has_schema_privilege(current_user, current_schema(), 'CREATE')
		FROM pg_catalog.pg_roles AS role
		WHERE role.rolname = current_user
	`).Scan(
		&roleName,
		&superuser,
		&inherit,
		&createRole,
		&createDatabase,
		&replication,
		&bypassRLS,
		&temporary,
		&schemaCreate,
	); err != nil {
		t.Fatalf("inspect runtime role: %v", err)
	}
	if roleName != providerControlRuntimeRoleName || superuser || inherit || createRole || createDatabase || replication || bypassRLS || temporary || schemaCreate {
		t.Fatalf(
			"runtime posture = role:%q super:%v inherit:%v createRole:%v createDB:%v replication:%v bypass:%v temp:%v schemaCreate:%v",
			roleName, superuser, inherit, createRole, createDatabase, replication, bypassRLS, temporary, schemaCreate,
		)
	}
}
