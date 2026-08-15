package db

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

func TestIntegrationMigrateSerializesConcurrentStarters(t *testing.T) {
	dsn := integrationDSN()
	if dsn == "" {
		t.Skip("TECHSTACK_TEST_POSTGRES_URL not set; skipping Postgres integration test")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()

	adminConfig, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse integration DSN: %v", err)
	}
	adminDB := stdlib.OpenDB(*adminConfig)
	t.Cleanup(func() { _ = adminDB.Close() })
	if err := adminDB.PingContext(ctx); err != nil {
		t.Fatalf("ping integration PostgreSQL: %v", err)
	}

	schema := "techstack_migrate_concurrent_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	quotedSchema := pgx.Identifier{schema}.Sanitize()
	if _, err := adminDB.ExecContext(ctx, "CREATE SCHEMA "+quotedSchema); err != nil {
		t.Fatalf("create isolated schema: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cancelCleanup := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancelCleanup()
		_, _ = adminDB.ExecContext(cleanupCtx, "DROP SCHEMA IF EXISTS "+quotedSchema+" CASCADE")
	})

	newScopedDB := func(t *testing.T) *DB {
		t.Helper()
		cfg := adminConfig.Copy()
		cfg.RuntimeParams = make(map[string]string, len(adminConfig.RuntimeParams)+1)
		for key, value := range adminConfig.RuntimeParams {
			cfg.RuntimeParams[key] = value
		}
		cfg.RuntimeParams["search_path"] = schema + ",public"
		sqlDB := stdlib.OpenDB(*cfg)
		sqlDB.SetMaxOpenConns(1)
		if err := sqlDB.PingContext(ctx); err != nil {
			_ = sqlDB.Close()
			t.Fatalf("ping scoped integration PostgreSQL: %v", err)
		}
		t.Cleanup(func() { _ = sqlDB.Close() })
		return &DB{DB: sqlDB, backend: StoreBackendPostgres, dsn: dsn}
	}

	starters := []*DB{newScopedDB(t), newScopedDB(t)}
	start := make(chan struct{})
	errs := make(chan error, len(starters))
	var wg sync.WaitGroup
	for index, starter := range starters {
		wg.Add(1)
		go func(index int, starter *DB) {
			defer wg.Done()
			<-start
			if err := starter.Migrate(ctx); err != nil {
				errs <- fmt.Errorf("starter %d: %w", index+1, err)
			}
		}(index, starter)
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if t.Failed() {
		return
	}

	var applied, distinct int
	if err := starters[0].QueryRowContext(ctx, `
		SELECT COUNT(*), COUNT(DISTINCT version)
		FROM schema_migrations
	`).Scan(&applied, &distinct); err != nil {
		t.Fatalf("read migration tracker: %v", err)
	}
	if applied == 0 || applied != distinct {
		t.Fatalf("migration tracker rows = %d, distinct versions = %d", applied, distinct)
	}
	assertMigrationCountMatchesEmbeddedFiles(t, applied)
}

func assertMigrationCountMatchesEmbeddedFiles(t *testing.T, applied int) {
	t.Helper()
	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		t.Fatalf("read embedded migrations: %v", err)
	}
	want := 0
	for _, entry := range entries {
		if !entry.IsDir() {
			want++
		}
	}
	if applied != want {
		t.Fatalf("applied migrations = %d, want %d embedded files", applied, want)
	}
}
