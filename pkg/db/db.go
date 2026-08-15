// Package db provides the dormant SQL store scaffold for kombifyTechstack.
package db

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"embed"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

const (
	// EnvDatabaseURL is the PostgreSQL connection URL used when the SQL store is enabled.
	EnvDatabaseURL = "DATABASE_URL"
	// EnvStoreBackend selects the authoritative store backend for future wiring.
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
	// StoreBackendPocketBase keeps the current self-hosted runtime on PocketBase.
	StoreBackendPocketBase StoreBackend = "pocketbase"
	// StoreBackendPostgres enables the PostgreSQL/sqlc scaffold.
	StoreBackendPostgres StoreBackend = "postgres"
	// StoreBackendSQLite is retained only as a future migration source marker.
	StoreBackendSQLite StoreBackend = "sqlite"
)

// ErrSQLStoreDisabled is returned when callers try to open a non-SQL backend.
var ErrSQLStoreDisabled = errors.New("sql store disabled")

// DB wraps a SQL database connection with kombifyTechstack-specific metadata.
type DB struct {
	*sql.DB
	backend StoreBackend
	dsn     string
	path    string
}

// Config holds database configuration.
type Config struct {
	Backend         StoreBackend
	DSN             string
	DriverName      string
	Path            string // Legacy SQLite path retained for later migration tooling.
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime time.Duration
	ConnMaxIdleTime time.Duration
}

// DefaultConfig returns safe defaults without enabling the dormant SQL store.
func DefaultConfig(dataDir string) Config {
	return Config{
		Backend:         StoreBackendPocketBase,
		Path:            filepath.Join(dataDir, "techstack.db"),
		MaxOpenConns:    10,
		MaxIdleConns:    5,
		ConnMaxLifetime: 30 * time.Minute,
		ConnMaxIdleTime: 5 * time.Minute,
	}
}

// ConfigFromEnv builds database configuration from environment variables.
func ConfigFromEnv(dataDir string) (Config, error) {
	cfg := DefaultConfig(dataDir)
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
	case StoreBackendSQLite:
		// TODO(storage-migration): wire read-only legacy SQLite migration support
		// when PostgreSQL becomes the active store.
		cfg.DriverName = "sqlite"
		cfg.DSN = cfg.Path
		return cfg, nil
	default:
		return Config{}, fmt.Errorf("unsupported %s value %q", EnvStoreBackend, backend)
	}
}

// SQLEnabled reports whether the selected backend is intended to use database/sql.
func (cfg Config) SQLEnabled() bool {
	return cfg.Backend == StoreBackendPostgres || cfg.Backend == StoreBackendSQLite
}

// Open creates a PostgreSQL database handle when the SQL store is explicitly enabled.
func Open(cfg Config) (*DB, error) {
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

	db, err := sql.Open(driverName, cfg.DSN)
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

	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}

	return &DB{
		DB:      db,
		backend: cfg.Backend,
		dsn:     cfg.DSN,
		path:    cfg.Path,
	}, nil
}

// Migrate runs all pending migrations.
func (db *DB) Migrate(ctx context.Context) error {
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

		if _, err := tx.ExecContext(ctx, string(content)); err != nil {
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
				FROM information_schema.tables
				WHERE table_schema = current_schema()
				  AND table_name = $1
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

// DSN returns the configured SQL data source name. Do not log this value.
func (db *DB) DSN() string {
	return db.dsn
}

// Path returns the legacy database file path retained for migration tooling.
func (db *DB) Path() string {
	return db.path
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
