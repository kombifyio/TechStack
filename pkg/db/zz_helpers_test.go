package db

// Helpers of retired unit tests that the remaining boundary tests of this package still use.

import (
	"context"
	"fmt"
	"os"
	"database/sql"
	"strings"
	"testing"
	"net/url"
)

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

func readDBFile(t *testing.T, name string) string {
	t.Helper()
	content, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return strings.ReplaceAll(string(content), "\r\n", "\n")
}

func assertStatementRejected(t *testing.T, ctx context.Context, tx *sql.Tx, savepoint, statement string, args ...any) {
	t.Helper()
	if _, err := tx.ExecContext(ctx, "SAVEPOINT "+savepoint); err != nil {
		t.Fatalf("create savepoint %s: %v", savepoint, err)
	}
	if _, err := tx.ExecContext(ctx, statement, args...); err == nil {
		_, _ = tx.ExecContext(ctx, "ROLLBACK TO SAVEPOINT "+savepoint)
		t.Fatalf("database accepted forbidden statement at savepoint %s", savepoint)
	}
	if _, err := tx.ExecContext(ctx, "ROLLBACK TO SAVEPOINT "+savepoint); err != nil {
		t.Fatalf("rollback constraint savepoint %s: %v", savepoint, err)
	}
}

func readEmbeddedMigrationForIntegration(t *testing.T, name string) string {
	t.Helper()
	content, err := migrationsFS.ReadFile(name)
	if err != nil {
		t.Fatalf("read embedded migration %s: %v", name, err)
	}
	return string(content)
}

func quotePostgresTestIdentifier(identifier string) string {
	return `"` + strings.ReplaceAll(identifier, `"`, `""`) + `"`
}

