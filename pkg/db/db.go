// Package db provides Techstack's PostgreSQL control-plane connection,
// migrations, and tenant-scoped transaction primitives.
package db

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"path"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"

	"github.com/kombifyio/techstack/internal/gocommon/dataprofile"

	"github.com/kombifyio/techstack/pkg/fulllocal"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

const (
	// EnvDatabaseURL is the PostgreSQL connection URL used when the SQL store is enabled.
	EnvDatabaseURL = "DATABASE_URL"
	// EnvStoreBackend selects the configured store backend.
	EnvStoreBackend = "TECHSTACK_STORE_BACKEND"

	// PostgresDriverName is the database/sql driver name registered by pgx stdlib.
	PostgresDriverName = "pgx"
	// migrationAdvisoryLockKey serializes schema migration runners across
	// concurrently starting control-plane instances in the same database.
	migrationAdvisoryLockKey int64 = 0x4b54534d49475241
	migrationExecutionLimit        = 5 * time.Minute
)

// StoreBackend identifies which persistence backend is authoritative.
type StoreBackend string

const (
	// StoreBackendPocketBase is retained as a legacy compatibility marker.
	// Production startup requires Postgres and fails closed on this selection.
	StoreBackendPocketBase StoreBackend = "pocketbase"
	// StoreBackendPostgres selects the active PostgreSQL control-plane store.
	StoreBackendPostgres StoreBackend = "postgres"
)

// ErrSQLStoreDisabled is returned when callers try to open a non-SQL backend.
var ErrSQLStoreDisabled = errors.New("sql store disabled")

// DB wraps a SQL database connection with kombifyTechstack-specific metadata.
type DB struct {
	*sql.DB
	backend StoreBackend
	// probe is a separate one-connection handle for health probes. A ping
	// through the shared pool waits behind every busy request, so a reachable
	// database behind a saturated pool would look dead and readiness would
	// restart the only instance.
	probe *sql.DB
}

// Config holds database configuration.
type Config struct {
	Backend         StoreBackend
	DSN             string
	DriverName      string
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime time.Duration
	ConnMaxIdleTime time.Duration
}

// DefaultConfig returns the connection defaults. ConfigFromEnv selects
// Postgres when DATABASE_URL is present; production startup rejects the legacy
// compatibility backend.
//
// The control-plane pool is sized for the SaaS worker fleet, not for a single
// operator. Measured on 2026-10-20 (13 Guards, about 800 requests a minute):
// Guard inventory projections alone kept 9 to 10 transactions open for 10 to
// 14 s each, so the former 10-connection pool was already full before a
// dashboard, a command poll or a readiness ping asked for one. 40 leaves room
// for the bounded inventory budget, worker polls, dashboard bursts and the
// background loops; the runtime database admits 500 connections, shared by the
// stable and Preview services.
func DefaultConfig() Config {
	return Config{
		Backend:         StoreBackendPocketBase,
		MaxOpenConns:    40,
		MaxIdleConns:    20,
		ConnMaxLifetime: 30 * time.Minute,
		ConnMaxIdleTime: 5 * time.Minute,
	}
}

// ConfigFromEnv builds database configuration from environment variables.
func ConfigFromEnv() (Config, error) {
	cfg := DefaultConfig()
	databaseURL := strings.TrimSpace(os.Getenv(EnvDatabaseURL))
	backend := StoreBackend(strings.ToLower(strings.TrimSpace(os.Getenv(EnvStoreBackend))))
	if backend == "" {
		if databaseURL != "" {
			backend = StoreBackendPostgres
		} else {
			backend = StoreBackendPocketBase
		}
	}

	cfg.Backend = backend

	switch backend {
	case StoreBackendPocketBase:
		return cfg, nil
	case StoreBackendPostgres:
		if databaseURL == "" {
			return Config{}, fmt.Errorf("%s is required when %s=%s", EnvDatabaseURL, EnvStoreBackend, StoreBackendPostgres)
		}
		cfg.DSN = databaseURL
		cfg.DriverName = PostgresDriverName
		return cfg, nil
	default:
		return Config{}, fmt.Errorf("unsupported %s value %q", EnvStoreBackend, backend)
	}
}

// Open creates a PostgreSQL database handle when the SQL store is explicitly enabled.
func Open(cfg Config) (*DB, error) {
	return OpenContext(context.Background(), cfg)
}

// OpenContext uses the same driver/config authority with a caller-bound
// connection deadline for governed administrative and fixture commands.
func OpenContext(ctx context.Context, cfg Config) (*DB, error) {
	if cfg.Backend != StoreBackendPostgres {
		return nil, fmt.Errorf("%w for backend %q", ErrSQLStoreDisabled, cfg.Backend)
	}
	if strings.TrimSpace(cfg.DSN) == "" {
		return nil, fmt.Errorf("%s is required for postgres backend", EnvDatabaseURL)
	}
	driverName := cfg.DriverName
	if driverName == "" {
		driverName = PostgresDriverName
	}

	db, err := openSQLDatabase(driverName, cfg.DSN)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	maxOpenConns := cfg.MaxOpenConns
	if maxOpenConns <= 0 {
		maxOpenConns = 10
	}
	maxIdleConns := cfg.MaxIdleConns
	if maxIdleConns <= 0 || maxIdleConns > maxOpenConns {
		maxIdleConns = maxOpenConns
	}

	db.SetMaxOpenConns(maxOpenConns)
	db.SetMaxIdleConns(maxIdleConns)
	db.SetConnMaxLifetime(cfg.ConnMaxLifetime)
	db.SetConnMaxIdleTime(cfg.ConnMaxIdleTime)

	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}

	// sql.Open connects lazily: the probe holds a connection only once a
	// health check uses it.
	probe, err := openSQLDatabase(driverName, cfg.DSN)
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("failed to open database probe: %w", err)
	}
	probe.SetMaxOpenConns(1)
	probe.SetMaxIdleConns(1)
	probe.SetConnMaxLifetime(cfg.ConnMaxLifetime)
	probe.SetConnMaxIdleTime(cfg.ConnMaxIdleTime)

	return &DB{
		DB:      db,
		backend: cfg.Backend,
		probe:   probe,
	}, nil
}

// openSQLDatabase consumes only the existing operator pg-egress routing map.
// The parsed DSN, TLS server name/CA and credentials stay unchanged; only DNS
// resolution of mapped hosts enters the canonical loopback SSH listeners.
func openSQLDatabase(driverName, dsn string) (*sql.DB, error) {
	if driverName == PostgresDriverName {
		config, err := operatorEgressConfig(dsn, os.Getenv("PG_EGRESS_MAP"))
		if err != nil {
			return nil, err
		}
		if config == nil && fulllocal.Active() {
			if config, err = pgx.ParseConfig(dsn); err != nil {
				return nil, errors.New("invalid PostgreSQL config")
			}
		}
		if config != nil {
			if fulllocal.Active() {
				// Under Full Local the database must be a homelab address; pgx
				// dials with its own dialer, not the shared HTTP transport.
				config.DialFunc = dataprofile.GuardDial(config.DialFunc)
			}
			return stdlib.OpenDB(*config), nil
		}
	}
	return sql.Open(driverName, dsn)
}

func operatorEgressConfig(dsn, raw string) (*pgx.ConnConfig, error) {
	if raw == "" {
		return nil, nil
	}
	// A duplicate key or trailing JSON must not silently select another route.
	decoder := json.NewDecoder(strings.NewReader(raw))
	start, err := decoder.Token()
	if err != nil || start != json.Delim('{') {
		return nil, errors.New("invalid operator PG_EGRESS_MAP")
	}
	mapping := map[string]string{}
	for decoder.More() {
		key, err := decoder.Token()
		host, ok := key.(string)
		if err != nil || !ok || !operatorEgressHostname(host) {
			return nil, errors.New("invalid operator PG_EGRESS_MAP hostname")
		}
		if _, duplicate := mapping[host]; duplicate {
			return nil, errors.New("duplicate operator PG_EGRESS_MAP hostname")
		}
		var address string
		if decoder.Decode(&address) != nil {
			return nil, errors.New("invalid operator PG_EGRESS_MAP address")
		}
		ip, err := netip.ParseAddr(address)
		// These are the listeners owned by scripts/pg-egress.mjs, never a
		// new remote destination, wildcard listener or TLS fallback.
		if err != nil || !ip.Is4() || address != ip.String() ||
			(address != "127.0.0.2" && address != "127.0.0.3" && address != "127.0.0.4" && address != "127.0.0.5") {
			return nil, errors.New("unsafe operator PG_EGRESS_MAP address")
		}
		mapping[host] = address
	}
	if end, err := decoder.Token(); err != nil || end != json.Delim('}') {
		return nil, errors.New("invalid operator PG_EGRESS_MAP")
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, errors.New("trailing operator PG_EGRESS_MAP data")
	}
	if len(mapping) == 0 {
		return nil, nil
	}
	config, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, errors.New("invalid PostgreSQL config for operator egress")
	}
	_, mapped := mapping[strings.ToLower(config.Host)]
	for _, fallback := range config.Fallbacks {
		if _, ok := mapping[strings.ToLower(fallback.Host)]; ok {
			mapped = true
		}
	}
	if !mapped {
		return nil, nil // Retain sql.Open semantics for every unmapped connection.
	}
	if config.TLSConfig == nil || config.TLSConfig.InsecureSkipVerify || config.TLSConfig.ServerName != config.Host {
		return nil, errors.New("operator egress requires verified PostgreSQL TLS hostname")
	}
	for _, fallback := range config.Fallbacks {
		if fallback.TLSConfig == nil || fallback.TLSConfig.InsecureSkipVerify || fallback.TLSConfig.ServerName != fallback.Host {
			return nil, errors.New("operator egress refuses unverified PostgreSQL fallback")
		}
	}
	originalLookup := config.LookupFunc
	config.LookupFunc = func(ctx context.Context, host string) ([]string, error) {
		if address, ok := mapping[strings.ToLower(host)]; ok {
			return []string{address}, nil
		}
		return originalLookup(ctx, host)
	}
	return config, nil
}

func operatorEgressHostname(host string) bool {
	if len(host) == 0 || len(host) > 253 || net.ParseIP(host) != nil {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, char := range label {
			if !(char >= 'a' && char <= 'z' || char >= '0' && char <= '9' || char == '-') {
				return false
			}
		}
	}
	return true
}

// Probe returns the handle health checks use to prove the database answers,
// independent of how busy the shared pool is. A DB built without one falls
// back to the shared pool.
func (db *DB) Probe() *sql.DB {
	if db.probe != nil {
		return db.probe
	}
	return db.DB
}

// Close closes the shared pool and the health probe.
func (db *DB) Close() error {
	if db.probe != nil {
		_ = db.probe.Close()
	}
	return db.DB.Close()
}

// Migrate applies governed migrations, or verifies the existing schema in verify mode.
func (db *DB) Migrate(ctx context.Context) error {
	return db.migrate(ctx, nil)
}

func (db *DB) migrate(ctx context.Context, forward *forwardMigration) error {
	if err := refusePreviewMigration(); err != nil {
		return err
	}
	if forward != nil && os.Getenv(EnvDatabaseMigrationMode) != "apply" {
		return fmt.Errorf("forward migration requires explicit apply mode")
	}
	switch mode := strings.TrimSpace(os.Getenv(EnvDatabaseMigrationMode)); mode {
	case "verify":
		return db.VerifyMigrations(ctx)
	case "", "apply":
	default:
		return fmt.Errorf("unsupported %s=%q: expected apply or verify", EnvDatabaseMigrationMode, mode)
	}
	migrationCtx, cancelMigration := context.WithTimeout(ctx, migrationExecutionLimit)
	defer cancelMigration()
	ctx = migrationCtx
	lockCtx, cancelLock := context.WithTimeout(ctx, 30*time.Second)
	defer cancelLock()
	lockConn, connErr := db.Conn(lockCtx)
	if connErr != nil {
		return fmt.Errorf("failed to reserve migration lock connection within 30s: %w", connErr)
	}
	defer func() { _ = lockConn.Close() }()
	if _, err := lockConn.ExecContext(lockCtx, "SELECT pg_advisory_lock($1)", migrationAdvisoryLockKey); err != nil {
		// The server may have acquired the session lock even when the client did
		// not receive the result (timeout/network ambiguity). Never return that
		// physical session to the pool unless acquisition is known to have failed.
		discardSQLConnection(lockConn)
		return fmt.Errorf("failed to acquire migration advisory lock within 30s: %w", err)
	}
	defer func() {
		unlockCtx, cancelUnlock := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancelUnlock()
		var unlocked bool
		if err := lockConn.QueryRowContext(unlockCtx, "SELECT pg_advisory_unlock($1)", migrationAdvisoryLockKey).Scan(&unlocked); err != nil || !unlocked {
			discardSQLConnection(lockConn)
		}
	}()
	if _, timeoutErr := lockConn.ExecContext(ctx, `
		SELECT
			set_config('lock_timeout', '10s', false),
			set_config('statement_timeout', '2min', false)
	`); timeoutErr != nil {
		// The server may have applied either session setting even when the
		// response was lost. Do not return an ambiguously configured session to
		// the pool.
		discardSQLConnection(lockConn)
		return fmt.Errorf("failed to bound migration database execution: %w", timeoutErr)
	}
	defer func() {
		resetCtx, cancelReset := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancelReset()
		if _, resetErr := lockConn.ExecContext(resetCtx, `
			SELECT
				set_config('lock_timeout', '0', false),
				set_config('statement_timeout', '0', false)
		`); resetErr != nil {
			discardSQLConnection(lockConn)
		}
	}()

	// Expected-identity pin and no-implicit-genesis guard (incident
	// 2026-08-12): a stale DSN must never let the boot chain silently rebuild
	// the schema in the wrong, freshly-emptied database. Both checks are
	// read-only and run before any DDL below.
	if err := VerifyExpectedDatabaseIdentity(ctx, lockConn); err != nil {
		return err
	}
	if err := verifyGenesisAllowed(ctx, lockConn); err != nil {
		return err
	}
	if forward != nil {
		// MigrateForwardBatch refuses an empty batch before this point.
		if len(forward.batch) == 0 {
			return fmt.Errorf("exact forward migration identity required")
		}
		return applyForwardBatch(ctx, lockConn, forward.batch, forward.verify)
	} else {

		// Self-heal a legacy schema_migrations table left by an older, integer-keyed
		// migration framework. This tracker keys migrations by their filename
		// (TEXT), so a pre-existing `version` column of a non-text type (e.g.
		// bigint) makes the status query below fail with SQLSTATE 22P02 and the
		// whole control-plane startup aborts. Drop the incompatible table so it is
		// recreated with the correct schema; the migrations themselves re-apply
		// idempotently (CREATE TABLE IF NOT EXISTS).
		if _, err := lockConn.ExecContext(ctx, `
		DO $$
		BEGIN
			IF EXISTS (
				SELECT 1 FROM information_schema.columns
				WHERE table_schema = current_schema()
				  AND table_name = 'schema_migrations'
				  AND column_name = 'version'
				  AND data_type <> 'text'
			) THEN
				DROP TABLE schema_migrations;
			END IF;
		END $$;
	`); err != nil {
			return fmt.Errorf("failed to reconcile legacy schema_migrations table: %w", err)
		}

		// Self-heal legacy PocketBase tables that share names with pkg/db migration
		// targets but have incompatible schemas (no tenant_id column). These were
		// created by the PocketBase framework before the PB-retirement merge and
		// block migration 006 which tries to create RLS policies using tenant_id.
		// All three tables are empty in the control-plane DB; drop them so the
		// migrations can recreate them with the correct schema.
		// Also purge the PocketBase _collections metadata for each dropped table so
		// that PocketBase no longer references tables whose schema pkg/db now owns.
		// Without this, app.Bootstrap() would find the collection in _collections,
		// discover it has no tenant_id field, and the startup tenant-backfill check
		// would abort as a fatal issue.
		if _, err := lockConn.ExecContext(ctx, `
		DO $$
		DECLARE tbl text;
		BEGIN
			FOREACH tbl IN ARRAY ARRAY['auth_sessions', 'nodes', 'feature_audit'] LOOP
				IF EXISTS (
					SELECT 1 FROM information_schema.tables
					WHERE table_schema = current_schema()
					  AND table_name = tbl
				) AND NOT EXISTS (
					SELECT 1 FROM information_schema.columns
					WHERE table_schema = current_schema()
					  AND table_name = tbl
					  AND column_name = 'tenant_id'
				) THEN
					EXECUTE format('DROP TABLE %I CASCADE', tbl);
					-- Remove PocketBase collection metadata so PB no longer
					-- references this table after pkg/db takes ownership.
					IF EXISTS (
						SELECT 1 FROM information_schema.tables
						WHERE table_schema = current_schema()
						  AND table_name = '_collections'
					) THEN
						EXECUTE format('DELETE FROM "_collections" WHERE name = %L', tbl);
					END IF;
				END IF;
			END LOOP;
		END $$;
	`); err != nil {
			return fmt.Errorf("failed to drop legacy PocketBase tables: %w", err)
		}

		_, createTrackerErr := lockConn.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version TEXT PRIMARY KEY,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)
	`)
		if createTrackerErr != nil {
			return fmt.Errorf("failed to create migrations table: %w", createTrackerErr)
		}
	}

	// Read migrations
	entries, readDirErr := migrationsFS.ReadDir("migrations")
	if readDirErr != nil {
		return fmt.Errorf("failed to read migrations: %w", readDirErr)
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		version := entry.Name()

		var count int
		statusErr := lockConn.QueryRowContext(ctx, "SELECT COUNT(*) FROM schema_migrations WHERE version = $1", version).Scan(&count)
		if statusErr != nil {
			return fmt.Errorf("failed to check migration status: %w", statusErr)
		}
		if count > 0 {
			continue // Already applied
		}

		// Read and execute migration
		content, readMigrationErr := migrationsFS.ReadFile(path.Join("migrations", entry.Name()))
		if readMigrationErr != nil {
			return fmt.Errorf("failed to read migration %s: %w", version, readMigrationErr)
		}

		tx, beginErr := lockConn.BeginTx(ctx, nil)
		if beginErr != nil {
			return fmt.Errorf("failed to begin transaction: %w", beginErr)
		}

		if err := executePendingMigration(ctx, tx, version, content); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("failed to execute migration %s: %w", version, err)
		}

		if _, err := tx.ExecContext(ctx, "INSERT INTO schema_migrations (version) VALUES ($1)", version); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("failed to record migration %s: %w", version, err)
		}

		if err := tx.Commit(); err != nil {
			return fmt.Errorf("failed to commit migration %s: %w", version, err)
		}

		fmt.Printf("Applied migration: %s\n", version)
	}

	if err := verifyRequiredControlPlaneTables(ctx, lockConn); err != nil {
		return err
	}

	return nil
}

func discardSQLConnection(conn *sql.Conn) {
	if conn == nil {
		return
	}
	// Returning driver.ErrBadConn tells database/sql not to put this physical
	// session back into the pool. That is required when advisory unlock cannot
	// be proven; closing a healthy *sql.Conn alone would preserve a leaked
	// session-level lock in the pool.
	_ = conn.Raw(func(any) error { return driver.ErrBadConn })
}

type migrationTableQuerier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func verifyRequiredControlPlaneTables(ctx context.Context, querier migrationTableQuerier) error {
	required := []string{
		"schema_migrations",
		"techstack_tenants",
		"stacks",
		"jobs",
		"workers",
		"client_pairing_codes",
		"nodes",
		"services",
		"breakglass_admin",
		"ril_workflow_runs",
		"ril_workflow_timers",
		"techstack_notification_outbox",
		"provider_desired_spec_revisions",
		"provider_operations",
		"provider_operation_receipts",
		"provider_operation_resources",
		"provider_operation_evidence",
		"provider_operation_execution_claims",
		"provider_provision_dispatch_guards",
		"managed_runtime_server_slots",
		"managed_runtime_server_slot_generations",
	}
	for _, table := range required {
		var exists bool
		if err := querier.QueryRowContext(ctx, `
			SELECT EXISTS (
				SELECT 1
				FROM pg_catalog.pg_class c
				JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
				WHERE n.nspname = current_schema()
				  AND c.relname = $1
				  AND c.relkind IN ('r', 'p')
			)
		`, table).Scan(&exists); err != nil {
			return fmt.Errorf("failed to verify migration table %s: %w", table, err)
		}
		if !exists {
			return fmt.Errorf("control-plane migration missing required table %s", table)
		}
	}
	return nil
}

// Backend returns the selected store backend.
func (db *DB) Backend() StoreBackend {
	return db.backend
}

// WithTx executes a function within a transaction.
func (db *DB) WithTx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}

	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}

	return tx.Commit()
}
