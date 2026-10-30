package platformprojection

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/kombifyio/techstack/internal/gocommon/tenantdb"
	"github.com/kombifyio/techstack/internal/systemdb"
	"github.com/kombifyio/techstack/pkg/db"
	"github.com/kombifyio/techstack/pkg/serverregistry"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

const readerRole = "techstack_platform_projection_system"
const writerRole = "kombify_cloud_projection_system"

// Runtime is private projection custody. Inert construction never opens a pool.
type Runtime struct {
	Enabled        bool
	ready          atomic.Bool
	projector      *serverregistry.PlatformProjector
	reader, writer *sql.DB
}

func (r *Runtime) Ready() bool { return r.ready.Load() }
func (r *Runtime) Run(ctx context.Context) {
	if r.projector != nil {
		r.projector.Run(ctx)
	}
}
func (r *Runtime) Close() {
	if r.reader != nil {
		_ = r.reader.Close()
	}
	if r.writer != nil {
		_ = r.writer.Close()
	}
}

func OpenFromEnv(ctx context.Context) (*Runtime, error) {
	channel, enabled, err := validateEnvironment()
	if err != nil {
		return nil, err
	}
	r := &Runtime{Enabled: enabled == "true"}
	if !r.Enabled {
		return r, nil
	}
	r.reader, err = openDatabase(ctx, "TECHSTACK_PLATFORM_PROJECTION_DATABASE_URL", "TECHSTACK_PLATFORM_PROJECTION_EXPECTED_DATABASE_IDENTITY", "kombify_runtime", readerRole, channel)
	if err != nil {
		return nil, err
	}
	refuse := func(err error) (*Runtime, error) { r.Close(); return nil, err }
	r.writer, err = openDatabase(ctx, "TECHSTACK_PLATFORM_PROJECTION_CLOUD_DATABASE_URL", "TECHSTACK_PLATFORM_PROJECTION_CLOUD_EXPECTED_DATABASE_IDENTITY", "kombify", writerRole, channel)
	if err != nil {
		return refuse(err)
	}
	functions := map[string]bool{"kombify_ctx.verified_context()": true, "kombify_ctx.subject()": true, "kombify_ctx.org_id()": true}
	if err = systemdb.VerifyPrivileges(ctx, r.reader, map[string]string{"kombify_ctx.role_class": "SELECT", "public.schema_migrations": "SELECT"}, map[string]string{
		"public.servers":  "id,tenant_id,stack_id,owner_subject_id,name,lifecycle_state,connection_state,health_state,revision,metadata_json,decommissioned_at,updated_at,node_id",
		"public.services": "tenant_id,id,stack_id,server_id,service_key,service_instance,name,node_id",
		"public.server_registry_outbox_prune_tenants": "tenant_id",
	}, functions); err != nil {
		return refuse(err)
	}
	if err = systemdb.VerifyPrivileges(ctx, r.writer, map[string]string{"kombify_ctx.role_class": "SELECT", "public.kombify_db_migrations": "SELECT", "public.infra_user_servers": "SELECT,INSERT,UPDATE", "public.infra_projection_marks": "SELECT,INSERT,UPDATE"}, nil, functions); err != nil {
		return refuse(err)
	}
	name, system, _ := strings.Cut(strings.TrimSpace(os.Getenv("TECHSTACK_PLATFORM_PROJECTION_EXPECTED_DATABASE_IDENTITY")), "@")
	if err = (&db.DB{DB: r.reader}).VerifyMigrationsAtFloor(ctx, db.ExpectedDatabaseIdentity{DatabaseName: name, SystemIdentifier: system}, db.PlatformProjectionSchemaFloor); err != nil {
		return refuse(errors.New("platform projection Techstack migration verification failed"))
	}
	var migrated bool
	if r.writer.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM public.kombify_db_migrations WHERE version='000277_infra_projection_system_role.up.sql' AND NOT dirty AND applied_at IS NOT NULL AND checksum=$1)`, cloudMigrationSHA256).Scan(&migrated) != nil || !migrated {
		return refuse(errors.New("platform projection Cloud migration binding unavailable"))
	}
	r.projector, err = serverregistry.NewPlatformProjector(serverregistry.PlatformProjectorConfig{Source: source{r.reader}, Publisher: publisher{r.writer}, OnPass: func(result serverregistry.ProjectionResult, err error) {
		r.ready.Store(err == nil && result.Failures == 0)
	}})
	if err != nil {
		return refuse(err)
	}
	return r, nil
}

func openDatabase(ctx context.Context, dsnEnv, pinEnv, database, role, channel string) (*sql.DB, error) {
	cfg, expected, err := databaseConfig(dsnEnv, pinEnv, database, role, channel)
	if err != nil {
		return nil, err
	}
	cfg.RuntimeParams["statement_timeout"] = "10000"
	pool := stdlib.OpenDB(*cfg)
	pool.SetMaxOpenConns(2)
	pool.SetMaxIdleConns(1)
	pool.SetConnMaxLifetime(30 * time.Minute)
	refuse := func() (*sql.DB, error) {
		_ = pool.Close()
		return nil, errors.New("platform projection database identity or principal denied")
	}
	var actual string
	if pool.QueryRowContext(ctx, `SELECT current_database()||'@'||system_identifier::text FROM pg_catalog.pg_control_system()`).Scan(&actual) != nil || actual != expected {
		return refuse()
	}
	if tenantdb.AssertSystemRole(ctx, pool, role) != nil {
		return refuse()
	}
	return pool, nil
}

const cloudMigrationSHA256 = "e24b748f459d096287fd97ca96174187f7787b26936eb60398f90283791c86ab"

func validateEnvironment() (string, string, error) {
	channel := strings.TrimSpace(os.Getenv("TECHSTACK_RELEASE_CHANNEL"))
	if channel != "preview" && channel != "stable" {
		return "", "", errors.New("platform projection requires explicit preview or stable release channel")
	}
	if os.Getenv(db.EnvDatabaseMigrationMode) != "verify" || os.Getenv(db.EnvAllowDBGenesis) != "false" {
		return "", "", errors.New("platform projection requires verify-only custody without genesis")
	}
	if "" != "" {
		return "", "", errors.New("platform projection received unrelated custody")
	}
	for _, name := range []string{"DATABASE_URL", db.EnvMigrationDatabaseURL, "TECHSTACK_MANAGED_TEST_ADMIN_DATABASE_URL", "TECHSTACK_PROVIDER_CONTROL_RUNTIME_DATABASE_URL", "TECHSTACK_ADVANCED_ISSUER_DATABASE_URL", "TECHSTACK_ADVANCED_ISSUER_KEY", "TECHSTACK_ENCRYPTION_KEY", "TECHSTACK_ADVANCED_ISSUER_CALLER_GRANTS", "TECHSTACK_DB_CONTEXT_KEY", "SERVICE_AUTH_SECRET", "SERVICE_AUTH_SECRET_NEXT", "TECHSTACK_HOUSEHOLD_WORKER_DATABASE_URL", "TECHSTACK_HOUSEHOLD_CLOUD_DATABASE_URL", "TECHSTACK_BACKUP_WORKER_DATABASE_URL", "TECHSTACK_BACKUP_ENTITLEMENTS_DATABASE_URL", "TECHSTACK_BACKUP_WORKER_MODE", "TECHSTACK_HOUSEHOLD_WORKER_MODE"} {
		if strings.TrimSpace(os.Getenv(name)) != "" {
			return "", "", errors.New("platform projection received unrelated custody")
		}
	}
	enabled := os.Getenv("TECHSTACK_PLATFORM_PROJECTION_ENABLED")
	if enabled != "true" && enabled != "false" {
		return "", "", errors.New("platform projection requires explicit enablement")
	}
	return channel, enabled, nil
}

func databaseConfig(dsnEnv, pinEnv, database, role, channel string) (*pgx.ConnConfig, string, error) {
	raw := strings.TrimSpace(os.Getenv(dsnEnv))
	cfg, err := pgx.ParseConfig(raw)
	if raw == "" || err != nil || cfg.User != role || cfg.Password == "" || cfg.Database != database {
		return nil, "", errors.New("platform projection requires exact separate role and verify-full TLS")
	}
	if cfg.TLSConfig == nil || cfg.TLSConfig.InsecureSkipVerify || cfg.TLSConfig.ServerName == "" || len(cfg.Fallbacks) != 0 {
		return nil, "", errors.New("platform projection requires verify-full TLS without fallback")
	}
	if !safeRuntimeParams(cfg.RuntimeParams) {
		return nil, "", errors.New("platform projection refuses session authority overrides")
	}
	expected, err := databasePin(pinEnv, database, cfg.Host, channel)
	if err != nil {
		return nil, "", err
	}
	return cfg, expected, nil
}

func databasePin(pinEnv, database, host, channel string) (string, error) {
	expected := strings.TrimSpace(os.Getenv(pinEnv))
	name, system, ok := strings.Cut(expected, "@")
	if !ok || name != database || len(system) < 10 || strings.Trim(system, "0123456789") != "" {
		return "", errors.New("platform projection requires physical database identity")
	}
	stageHost := "kombify-stg-pg-8146d168.pgv20v39ze9j4j3d3dwbj6p979.c0.eu-central-1.aws.pg.clickhouse.cloud"
	stage := system == "7699436836349883199" || host == stageHost
	if (channel == "stable" && stage) || (channel == "preview" && (system != "7699436836349883199" || host != stageHost)) {
		return "", errors.New("platform projection database release channel mismatch")
	}
	return expected, nil
}

func safeRuntimeParams(params map[string]string) bool {
	for key := range params {
		if key != "application_name" && key != "connect_timeout" && key != "channel_binding" {
			return false
		}
	}
	return true
}
