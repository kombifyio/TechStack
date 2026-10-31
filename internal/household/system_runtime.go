package household

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strings"

	"github.com/kombifyio/techstack/internal/gocommon/tenantdb"
	"github.com/kombifyio/techstack/pkg/agentcontrol"
	"github.com/kombifyio/techstack/pkg/config"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/db"
	"github.com/kombifyio/techstack/pkg/releasechannel"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

// Exact reviewed Cloud source prerequisite; update only with its owned migration.
// Cloud #2137 revised 000279 before any governed database applied it (adds the
// sequence revoke and exact function grantees); kombify-stg-pg records this one.
const householdCloudMigrationSHA256 = "d8dc0585d02d3996bd9230b7e25808996a39d577c0d7f8aa1a7fe581c9a3b174"

const cloudDSNEnv = "TECHSTACK_HOUSEHOLD_CLOUD_DATABASE_URL"
const workerDSNEnv = "TECHSTACK_HOUSEHOLD_WORKER_DATABASE_URL"

// SystemRuntime is composed in the existing private Execution process on
// Stage. Its app-facing relay holds neither of these database credentials.
type SystemRuntime struct {
	Worker           *SystemWorker
	cloud, techstack *sql.DB
}

func (r *SystemRuntime) Close() {
	if r != nil {
		r.Worker.Close()
		r.cloud.Close()
		r.techstack.Close()
	}
}

// StartupRefusal names the startup stage that refused, plus the package's own
// fixed reason where it has one; never a credential, DSN or driver message.
type StartupRefusal struct{ Stage, Reason string }

func (e *StartupRefusal) Error() string {
	if e.Reason == "" {
		return "household startup refused at " + e.Stage
	}
	return "household startup refused at " + e.Stage + ": " + e.Reason
}

// refusedAt keeps a SystemRoleRefusal (names only) and otherwise records the
// stage, with the reason text only where this package wrote it (fixed).
func refusedAt(stage string, err error, fixed bool) error {
	var role *SystemRoleRefusal
	if errors.As(err, &role) {
		return err
	}
	if fixed {
		return &StartupRefusal{Stage: stage, Reason: err.Error()}
	}
	return &StartupRefusal{Stage: stage}
}

func OpenSystemWorkerFromEnv(ctx context.Context, inExecution bool) (*SystemRuntime, error) {
	mode := strings.TrimSpace(os.Getenv("TECHSTACK_HOUSEHOLD_WORKER_MODE"))
	if (inExecution && mode != "execution") || (!inExecution && mode != "standalone") {
		return nil, errors.New("household worker requires its explicit private process profile")
	}
	if config.ReleaseChannel() != "stable" && (!inExecution || config.ReleaseChannel() != "preview" || !releasechannel.StageRuntimePrepared()) {
		return nil, errors.New("Preview household work requires the existing prepared private Execution owner")
	}
	if os.Getenv(db.EnvDatabaseMigrationMode) != "verify" || os.Getenv(db.EnvAllowDBGenesis) != "false" || os.Getenv(db.EnvMigrationDatabaseURL) != "" || "" != "" {
		return nil, errors.New("household worker requires finite verify-only custody")
	}
	if !inExecution {
		for _, name := range []string{"DATABASE_URL", "TECHSTACK_ADVANCED_ISSUER_DATABASE_URL", "TECHSTACK_PROVIDER_CONTROL_RUNTIME_DATABASE_URL", "TECHSTACK_ADVANCED_ISSUER_KEY", "TECHSTACK_DB_CONTEXT_KEY", "SERVICE_AUTH_SECRET", "SERVICE_AUTH_SECRET_NEXT"} {
			if strings.TrimSpace(os.Getenv(name)) != "" {
				return nil, errors.New("standalone household process received unrelated custody")
			}
		}
	}
	cloud, err := openHouseholdDatabase(ctx, cloudDSNEnv, "TECHSTACK_HOUSEHOLD_CLOUD_EXPECTED_DATABASE_IDENTITY", "kombify", CloudHouseholdSystemRole)
	if err != nil {
		return nil, refusedAt("cloud-database", err, true)
	}
	tech, err := openHouseholdDatabase(ctx, workerDSNEnv, db.EnvExpectedDatabaseIdentity, "kombify_runtime", controlplane.HouseholdSystemRole)
	if err != nil {
		cloud.Close()
		return nil, refusedAt("techstack-database", err, true)
	}
	refuse := func(err error) (*SystemRuntime, error) { cloud.Close(); tech.Close(); return nil, err }
	var migrated bool
	if cloud.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM public.kombify_db_migrations WHERE version='000279_household_app_projection.up.sql' AND NOT dirty AND applied_at IS NOT NULL AND checksum=$1)`, householdCloudMigrationSHA256).Scan(&migrated) != nil || !migrated {
		return refuse(&StartupRefusal{Stage: "cloud-migration", Reason: "household Cloud migration binding unavailable"})
	}
	if err := verifyHouseholdPrivileges(ctx, cloud, true); err != nil {
		return refuse(refusedAt("cloud-privileges", err, true))
	}
	if err := verifyHouseholdPrivileges(ctx, tech, false); err != nil {
		return refuse(refusedAt("techstack-privileges", err, true))
	}
	// Reuse the canonical embedded migration ledger verifier; never apply SQL.
	if err := (&db.DB{DB: tech}).VerifyMigrations(ctx); err != nil {
		return refuse(&StartupRefusal{Stage: "techstack-migrations", Reason: "household Techstack migration verification failed"})
	}
	relay, err := agentcontrol.NewHouseholdRelayClient(os.Getenv("TECHSTACK_HOUSEHOLD_RELAY_ORIGIN"), os.Getenv("TECHSTACK_HOUSEHOLD_RELAY_SIGNING_KEY"))
	if err != nil {
		return refuse(refusedAt("relay-client", err, false))
	}
	delivery, err := NewActivationDelivery(os.Getenv("TECHSTACK_HOUSEHOLD_NOTIFICATIONS_ORIGIN"), os.Getenv("TECHSTACK_HOUSEHOLD_NOTIFICATIONS_SIGNING_KEY"))
	if err != nil {
		return refuse(refusedAt("notifications-client", err, false))
	}
	worker, err := NewSystemWorker(ctx, cloud, tech, relay, delivery)
	if err != nil {
		return refuse(refusedAt("worker", err, false))
	}
	return &SystemRuntime{Worker: worker, cloud: cloud, techstack: tech}, nil
}

func openHouseholdDatabase(ctx context.Context, dsnEnv, pinEnv, database, role string) (*sql.DB, error) {
	raw := strings.TrimSpace(os.Getenv(dsnEnv))
	cfg, err := pgx.ParseConfig(raw)
	if raw == "" || err != nil || cfg.User != role || cfg.Password == "" || cfg.Database != database || cfg.TLSConfig == nil || cfg.TLSConfig.InsecureSkipVerify || len(cfg.Fallbacks) != 0 {
		return nil, errors.New("household database requires exact separate role and verified TLS")
	}
	for key := range cfg.RuntimeParams {
		if key != "application_name" && key != "connect_timeout" && key != "channel_binding" {
			return nil, errors.New("household database refuses session authority overrides")
		}
	}
	expected := strings.TrimSpace(os.Getenv(pinEnv))
	name, system, ok := strings.Cut(expected, "@")
	if !ok || name != database || len(system) < 10 {
		return nil, errors.New("household database physical identity required")
	}
	// A Stage DSN must never execute under a Stable-labelled private process.
	stageHost := "kombify-stg-pg-8146d168.pgv20v39ze9j4j3d3dwbj6p979.c0.eu-central-1.aws.pg.clickhouse.cloud"
	stage := system == "7699436836349883199" || cfg.Host == stageHost
	if stage && (config.ReleaseChannel() != "preview" || !releasechannel.StageRuntimePrepared()) {
		return nil, errors.New("Stage household database requires the prepared private Execution owner")
	}
	if config.ReleaseChannel() == "preview" && (system != "7699436836349883199" || cfg.Host != stageHost) {
		return nil, errors.New("Stage household process refuses other database custody")
	}
	cfg.RuntimeParams["statement_timeout"] = "10000"
	pool := stdlib.OpenDB(*cfg)
	pool.SetMaxOpenConns(4)
	pool.SetMaxIdleConns(2)
	refuse := func() (*sql.DB, error) {
		pool.Close()
		return nil, errors.New("household database physical identity or principal denied")
	}
	var actual string
	if pool.QueryRowContext(ctx, `SELECT current_database()||'@'||system_identifier::text FROM pg_catalog.pg_control_system()`).Scan(&actual) != nil || actual != expected {
		return refuse()
	}
	if err := assertHouseholdSystemRole(ctx, pool, role); err != nil {
		pool.Close()
		return nil, err
	}
	return pool, nil
}

// SystemRoleRefusal names the refused principal facts (role, table and policy
// names only), so a refused start is diagnosable without any credential.
type SystemRoleRefusal struct{ Violations []string }

func (e *SystemRoleRefusal) Error() string {
	return "household system role denied: " + strings.Join(e.Violations, "; ")
}

const wideningViolation = "RLS policies read a widening setting such as app.service_scope: "

// assertHouseholdSystemRole is tenantdb.AssertSystemRole with one admitted
// neighbour. Production Cloud keeps the two infra projection policies that read
// app.service_scope until Cloud 000277 retires them together with the live
// projection ingest (platform-ld78c.4.4), and tenantdb refuses any such policy
// anywhere in the database. The household role starts beside them only when
// that is its sole violation and it holds no table or column privilege on any
// table such a policy guards, so no widening setting can reach its rows. Views,
// materialized views and SECURITY DEFINER routines cannot reach them either:
// verifyHouseholdPrivileges, run right after this in the same startup, refuses
// every relation privilege and every definer routine outside the expected set.
func assertHouseholdSystemRole(ctx context.Context, pool *sql.DB, role string) error {
	err := tenantdb.AssertSystemRole(ctx, pool, role)
	if err == nil {
		return nil
	}
	var assertion *tenantdb.RoleAssertionError
	if !errors.As(err, &assertion) {
		return errors.New("household database physical identity or principal denied")
	}
	refusal := &SystemRoleRefusal{Violations: assertion.Violations}
	if len(assertion.Violations) != 1 || !strings.HasPrefix(assertion.Violations[0], wideningViolation) {
		return refusal
	}
	var reachable bool
	if pool.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_catalog.pg_policies p
  JOIN pg_catalog.pg_namespace n ON n.nspname=p.schemaname JOIN pg_catalog.pg_class c ON c.relnamespace=n.oid AND c.relname=p.tablename
  WHERE (COALESCE(p.qual,'') ~* 'app\.service_scope' OR COALESCE(p.with_check,'') ~* 'app\.service_scope')
   AND (has_table_privilege(c.oid,'SELECT,INSERT,UPDATE,DELETE,TRUNCATE,REFERENCES,TRIGGER,MAINTAIN')
    OR has_any_column_privilege(c.oid,'SELECT,INSERT,UPDATE,REFERENCES')))`).Scan(&reachable) != nil || reachable {
		return refusal
	}
	return nil
}
