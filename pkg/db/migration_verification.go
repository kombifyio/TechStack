package db

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
)

// EnvDatabaseMigrationMode selects governed apply (default) or read-only verify.
const EnvDatabaseMigrationMode = "TECHSTACK_DATABASE_MIGRATION_MODE"

// VerifyMigrations admits only an existing, identity-pinned schema whose ledger
// exactly matches this binary. It never repairs or applies schema or data.
func (db *DB) VerifyMigrations(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, migrationExecutionLimit)
	defer cancel()
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return fmt.Errorf("begin read-only migration verification: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, pinned, err := ExpectedDatabaseIdentityFromEnv(); err != nil {
		return err
	} else if !pinned {
		return fmt.Errorf("read-only migration verification requires %s", EnvExpectedDatabaseIdentity)
	}
	if err := VerifyExpectedDatabaseIdentity(ctx, tx); err != nil {
		return err
	}
	if err := verifyGenesisAllowed(ctx, tx); err != nil {
		return err
	}
	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		return fmt.Errorf("read embedded migrations: %w", err)
	}
	expected := make(map[string]bool)
	for _, entry := range entries {
		if !entry.IsDir() {
			expected[entry.Name()] = true
		}
	}
	rows, err := tx.QueryContext(ctx, "SELECT version FROM schema_migrations ORDER BY version")
	if err != nil {
		return fmt.Errorf("read migration ledger: %w", err)
	}
	applied := make(map[string]bool)
	for rows.Next() {
		var version string
		if err := rows.Scan(&version); err != nil {
			_ = rows.Close()
			return fmt.Errorf("scan migration ledger: %w", err)
		}
		applied[version] = true
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("iterate migration ledger: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close migration ledger: %w", err)
	}
	if len(applied) == 0 {
		return fmt.Errorf("%w: read-only verification requires an existing populated ledger", ErrImplicitGenesisRefused)
	}
	missing, ahead := []string{}, []string{}
	for version := range expected {
		if !applied[version] {
			missing = append(missing, version)
		}
	}
	for version := range applied {
		if !expected[version] {
			ahead = append(ahead, version)
		}
	}
	sort.Strings(missing)
	sort.Strings(ahead)
	if len(missing) != 0 || len(ahead) != 0 {
		return fmt.Errorf("migration ledger incompatible: missing [%s]; ahead [%s]", strings.Join(missing, ", "), strings.Join(ahead, ", "))
	}
	return verifyRequiredControlPlaneTables(ctx, tx)
}
