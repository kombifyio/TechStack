package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// EnvDatabaseMigrationMode selects governed apply (default) or read-only verify.
const EnvDatabaseMigrationMode = "TECHSTACK_DATABASE_MIGRATION_MODE"

// SchemaFloor is the highest migration number this binary's serving code needs.
// Bump it in the same pull request that adds a migration the code depends on;
// an expand migration the code does not read yet may stay above it.
const SchemaFloor = 172

// PlatformProjectionSchemaFloor is what the private platform projection process
// of the same binary needs: the role, grants and policies of migration 173. The
// serving process never reads them, so a rollout that has not applied 173 yet
// still boots; only the projection process refuses until it is applied.
const PlatformProjectionSchemaFloor = 173

// ErrSchemaFloorUnmet means a migration at or below SchemaFloor is not applied.
var ErrSchemaFloorUnmet = errors.New("schema floor unmet")

// VerifyMigrations admits an existing, identity-pinned schema whose ledger
// meets this binary's schema floor. Newer or not yet applied migrations above
// the floor pass. It never repairs or applies schema or data.
func (db *DB) VerifyMigrations(ctx context.Context) error {
	pin, pinned, err := ExpectedDatabaseIdentityFromEnv()
	if err != nil {
		return err
	}
	if !pinned {
		return fmt.Errorf("read-only migration verification requires %s", EnvExpectedDatabaseIdentity)
	}
	return db.VerifyMigrationsWithIdentity(ctx, pin)
}

// VerifyMigrationsWithIdentity reuses the embedded ledger verifier for a separately pinned private role.
func (db *DB) VerifyMigrationsWithIdentity(ctx context.Context, pin ExpectedDatabaseIdentity) error {
	return db.VerifyMigrationsAtFloor(ctx, pin, SchemaFloor)
}

// VerifyMigrationsAtFloor is VerifyMigrationsWithIdentity for a private process
// whose own schema floor is higher than the serving process's.
func (db *DB) VerifyMigrationsAtFloor(ctx context.Context, pin ExpectedDatabaseIdentity, floor int) error {
	ctx, cancel := context.WithTimeout(ctx, migrationExecutionLimit)
	defer cancel()
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return fmt.Errorf("begin read-only migration verification: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err = verifyMigrationIdentity(ctx, tx, pin); err != nil {
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
	if err := verifySchemaFloor(expected, applied, floor); err != nil {
		return err
	}

	return verifyRequiredControlPlaneTables(ctx, tx)
}

func verifyMigrationIdentity(ctx context.Context, tx *sql.Tx, pin ExpectedDatabaseIdentity) error {
	if pin.DatabaseName == "" {
		return fmt.Errorf("migration verification requires a database identity")
	}
	var name, system string
	if err := tx.QueryRowContext(ctx, `SELECT current_database()`).Scan(&name); err != nil {
		return err
	}
	if pin.SystemIdentifier != "" {
		if err := tx.QueryRowContext(ctx, `SELECT system_identifier::text FROM pg_catalog.pg_control_system()`).Scan(&system); err != nil {
			return err
		}
	}
	if err := pin.Verify(name, system); err != nil {
		return err
	}
	return nil
}

// verifySchemaFloor applies a schema floor (D-2026-10-27-06, platform-dtyn6):
// every embedded migration numbered at or below the floor must be in the
// ledger. Each migration and its ledger row commit in one transaction, so the
// ledger has no dirty rows. A newer or unknown ledger row and an embedded
// migration above the floor that is not applied yet both pass and are only
// logged, so this binary and its schema roll out in any order.
func verifySchemaFloor(expected, applied map[string]bool, floor int) error {
	missing, pending, ahead := []string{}, []string{}, []string{}
	for version := range expected {
		if applied[version] {
			continue
		}
		// An unnumbered embedded file cannot be placed against the floor and
		// stays required.
		if number, ok := migrationNumber(version); ok && number > floor {
			pending = append(pending, version)
			continue
		}
		missing = append(missing, version)
	}
	for version := range applied {
		if !expected[version] {
			ahead = append(ahead, version)
		}
	}
	sort.Strings(missing)
	sort.Strings(pending)
	sort.Strings(ahead)
	if len(missing) != 0 {
		return fmt.Errorf("%w: floor %d needs [%s]", ErrSchemaFloorUnmet, floor, strings.Join(missing, ", "))
	}
	if len(pending) != 0 {
		fmt.Printf("Schema floor %d met; embedded migrations above the floor not applied yet: %s\n", floor, strings.Join(pending, ", "))
	}
	if len(ahead) != 0 {
		fmt.Printf("Schema floor %d met; ledger carries migrations this binary does not embed: %s\n", floor, strings.Join(ahead, ", "))
	}
	return nil
}

// WaitForSchemaFloor repeats the read-only verification until this binary's
// schema floor is applied. The Render pre-deploy step uses it so a service
// activation waits only for its own Delivery's database leg, never for
// another environment. Any refusal other than an unmet floor returns at once.
func (db *DB) WaitForSchemaFloor(ctx context.Context, limit, interval time.Duration) error {
	deadline := time.Now().Add(limit)
	for {
		err := db.VerifyMigrations(ctx)
		if err == nil || !errors.Is(err, ErrSchemaFloorUnmet) || time.Now().Add(interval).After(deadline) {
			return err
		}
		fmt.Printf("Waiting for this deploy's database leg: %v\n", err)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(interval):
		}
	}
}

// SchemaState is the health endpoint's schema block.
type SchemaState struct {
	Floor       int  `json:"floor"`
	EmbeddedMax int  `json:"embedded_max"`
	AppliedMax  *int `json:"applied_max"`
}

// EmbeddedSchemaState reports the floor and the newest embedded migration.
func EmbeddedSchemaState() SchemaState {
	state := SchemaState{Floor: SchemaFloor}
	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		return state
	}
	for _, entry := range entries {
		if number, ok := migrationNumber(entry.Name()); ok && !entry.IsDir() && number > state.EmbeddedMax {
			state.EmbeddedMax = number
		}
	}
	return state
}

// AppliedSchemaMax reads the highest numbered ledger row.
func AppliedSchemaMax(ctx context.Context, q migrationLedgerReader) (int, error) {
	rows, err := q.QueryContext(ctx, "SELECT version FROM schema_migrations")
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	highest := 0
	for rows.Next() {
		var version string
		if err := rows.Scan(&version); err != nil {
			return 0, err
		}
		if number, ok := migrationNumber(version); ok && number > highest {
			highest = number
		}
	}
	return highest, rows.Err()
}
