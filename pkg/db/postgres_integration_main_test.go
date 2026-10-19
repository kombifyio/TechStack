package db

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kombifyio/techstack/internal/localdb"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

const envDBEmbeddedPostgres = "TECHSTACK_DB_EMBEDDED_POSTGRES"

// TestMain makes the real PostgreSQL migration/RLS suite an explicit opt-in
// gate. Ordinary short unit tests keep skipping without a dedicated test DSN;
// check:ci enables this path against a disposable embedded PostgreSQL process.
func TestMain(m *testing.M) {
	cleanup, err := configureIntegrationPostgres()
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "configure PostgreSQL integration suite: %v\n", err)
		os.Exit(1)
	}

	code := m.Run()
	if err := cleanup(); err != nil && code == 0 {
		_, _ = fmt.Fprintf(os.Stderr, "clean PostgreSQL integration suite: %v\n", err)
		code = 1
	}
	os.Exit(code)
}

func configureIntegrationPostgres() (func() error, error) {
	baseDSN := strings.TrimSpace(os.Getenv("TECHSTACK_TEST_POSTGRES_URL"))
	var embedded *localdb.EmbeddedPostgres
	var tempDir string
	var adminDBClose func() error
	var suiteSchema string

	cleanup := func() error {
		var cleanupErrors []error
		if adminDBClose != nil {
			if err := adminDBClose(); err != nil {
				cleanupErrors = append(cleanupErrors, err)
			}
		}
		if embedded != nil {
			if err := embedded.Stop(); err != nil {
				cleanupErrors = append(cleanupErrors, fmt.Errorf("stop embedded PostgreSQL: %w", err))
			}
		}
		if tempDir != "" {
			if err := os.RemoveAll(tempDir); err != nil {
				cleanupErrors = append(cleanupErrors, fmt.Errorf("remove embedded PostgreSQL test dir: %w", err))
			}
		}
		return errors.Join(cleanupErrors...)
	}
	fail := func(err error) (func() error, error) {
		_ = cleanup()
		return nil, err
	}

	// The explicit embedded gate always wins over an inherited DSN. This keeps
	// CI/dev runs disposable even when a shell happens to export a test URL.
	if strings.TrimSpace(os.Getenv(envDBEmbeddedPostgres)) == "1" {
		createdTempDir, mkdirErr := os.MkdirTemp("", "techstack-db-postgres-")
		if mkdirErr != nil {
			return fail(fmt.Errorf("create embedded PostgreSQL test dir: %w", mkdirErr))
		}
		tempDir = createdTempDir
		if setDirErr := os.Setenv(localdb.EnvEmbeddedPostgresDir, filepath.Join(tempDir, "postgres")); setDirErr != nil {
			return fail(fmt.Errorf("configure embedded PostgreSQL test dir: %w", setDirErr))
		}
		startedEmbedded, startErr := localdb.StartEmbeddedPostgres(tempDir)
		if startErr != nil {
			return fail(fmt.Errorf("start embedded PostgreSQL: %w", startErr))
		}
		embedded = startedEmbedded
		// The disposable instance belongs to this suite alone, so it runs the
		// production chain the way a hosted database does: in public, as the
		// non-bypass role that owns the database and public schema. Migrations
		// 151 to 155 name public explicitly and refuse a superuser owner.
		ownerDSN, ownerErr := admitEmbeddedSuiteOwner(embedded)
		if ownerErr != nil {
			return fail(ownerErr)
		}
		if setDSNErr := os.Setenv("TECHSTACK_TEST_POSTGRES_URL", ownerDSN); setDSNErr != nil {
			return fail(fmt.Errorf("configure embedded PostgreSQL integration DSN: %w", setDSNErr))
		}
		return cleanup, nil
	}
	if baseDSN == "" {
		return cleanup, nil
	}

	adminConfig, parseErr := pgx.ParseConfig(baseDSN)
	if parseErr != nil {
		return fail(fmt.Errorf("parse integration DSN: %w", parseErr))
	}
	adminDB := stdlib.OpenDB(*adminConfig)
	setupCtx, cancelSetup := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelSetup()
	if pingErr := adminDB.PingContext(setupCtx); pingErr != nil {
		_ = adminDB.Close()
		return fail(fmt.Errorf("ping integration PostgreSQL: %w", pingErr))
	}

	suiteSchema = "techstack_suite_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	quotedSchema := pgx.Identifier{suiteSchema}.Sanitize()
	if _, createSchemaErr := adminDB.ExecContext(setupCtx, "CREATE SCHEMA "+quotedSchema); createSchemaErr != nil {
		_ = adminDB.Close()
		return fail(fmt.Errorf("create isolated integration schema: %w", createSchemaErr))
	}
	adminDBClose = func() error {
		cleanupCtx, cancelCleanup := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancelCleanup()
		_, dropErr := adminDB.ExecContext(cleanupCtx, "DROP SCHEMA IF EXISTS "+quotedSchema+" CASCADE")
		closeErr := adminDB.Close()
		return errors.Join(dropErr, closeErr)
	}

	scopedDSN, scopeErr := postgresDSNWithSearchPath(baseDSN, suiteSchema+",public")
	if scopeErr != nil {
		return fail(scopeErr)
	}
	if setDSNErr := os.Setenv("TECHSTACK_TEST_POSTGRES_URL", scopedDSN); setDSNErr != nil {
		return fail(fmt.Errorf("configure isolated PostgreSQL integration DSN: %w", setDSNErr))
	}
	return cleanup, nil
}

// admitEmbeddedSuiteOwner lets the embedded runtime owner create and drop the
// scoped roles the RLS tests connect as. CREATEROLE grants neither SUPERUSER
// nor BYPASSRLS, so the owner posture the migrations check stays exact.
func admitEmbeddedSuiteOwner(embedded *localdb.EmbeddedPostgres) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	ownerDSN := embedded.DSN()
	owner, err := pgx.Connect(ctx, ownerDSN)
	if err != nil {
		return "", fmt.Errorf("connect embedded PostgreSQL runtime owner: %w", err)
	}
	var ownerRole string
	err = owner.QueryRow(ctx, "SELECT current_user").Scan(&ownerRole)
	_ = owner.Close(context.Background())
	if err != nil {
		return "", fmt.Errorf("resolve embedded PostgreSQL runtime owner: %w", err)
	}
	bootstrap, err := pgx.Connect(ctx, embedded.BootstrapDSN())
	if err != nil {
		return "", fmt.Errorf("connect embedded PostgreSQL bootstrap role: %w", err)
	}
	defer func() { _ = bootstrap.Close(context.Background()) }()
	quotedOwner := pgx.Identifier{ownerRole}.Sanitize()
	// The self-grant keeps the owner a member of the roles it creates, so test
	// cleanup can DROP OWNED BY them (PostgreSQL 16 grants ADMIN alone).
	for _, statement := range []string{
		"ALTER ROLE " + quotedOwner + " CREATEROLE",
		"ALTER ROLE " + quotedOwner + " SET createrole_self_grant TO 'set, inherit'",
	} {
		if _, err := bootstrap.Exec(ctx, statement); err != nil {
			return "", fmt.Errorf("admit embedded PostgreSQL runtime owner to manage test roles: %w", err)
		}
	}
	return ownerDSN, nil
}

func postgresDSNWithSearchPath(dsn, searchPath string) (string, error) {
	lowerDSN := strings.ToLower(strings.TrimSpace(dsn))
	if strings.HasPrefix(lowerDSN, "postgres://") || strings.HasPrefix(lowerDSN, "postgresql://") {
		parsed, err := url.Parse(dsn)
		if err != nil {
			return "", fmt.Errorf("parse PostgreSQL URL: %w", err)
		}
		query := parsed.Query()
		query.Set("search_path", searchPath)
		parsed.RawQuery = query.Encode()
		return parsed.String(), nil
	}
	// Generated schema names contain only [a-z0-9_], so they are safe as an
	// unquoted libpq keyword value. Appending makes this value authoritative if
	// the dedicated external test DSN already supplied a search_path.
	return strings.TrimSpace(dsn) + " search_path=" + searchPath, nil
}
