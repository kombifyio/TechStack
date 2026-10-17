package db

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path"
)

// ForwardMigrationArtifact identifies an embedded migration, never caller SQL.
type ForwardMigrationArtifact struct {
	Version string `json:"version"`
	SHA256  string `json:"sha256"`
}

var ErrForwardBatchLedger = errors.New("forward batch ledger changed; inspect without replay")

// MigrateForwardBatch uses the normal runner's physical identity, advisory lock,
// timeouts and genesis guard. The exact pending batch commits in one transaction.
func (db *DB) MigrateForwardBatch(ctx context.Context, batch []ForwardMigrationArtifact, verify func(context.Context, *sql.Tx) error) error {
	if os.Getenv(EnvDatabaseMigrationMode) != "apply" || os.Getenv(EnvAllowDBGenesis) != "false" || verify == nil {
		return errors.New("forward batch requires explicit apply, no genesis and capability verification")
	}
	if _, err := forwardBatchBodies(batch); err != nil {
		return err
	}
	if err := requireForwardPhysicalPin(); err != nil {
		return err
	}
	return db.migrate(ctx, &forwardMigration{batch: append([]ForwardMigrationArtifact(nil), batch...), verify: verify})
}

func forwardBatchBodies(batch []ForwardMigrationArtifact) ([][]byte, error) {
	if len(batch) == 0 {
		return nil, errors.New("empty forward batch")
	}
	bodies := make([][]byte, 0, len(batch))
	previous := ""
	for _, artifact := range batch {
		if path.Base(artifact.Version) != artifact.Version || artifact.Version <= previous || len(artifact.SHA256) != 64 {
			return nil, errors.New("ordered exact forward migration identities required")
		}
		body, err := migrationsFS.ReadFile(path.Join("migrations", artifact.Version))
		if err != nil {
			return nil, errors.New("forward migration not embedded")
		}
		// Git's canonical SQL is LF. Windows checkout conversion must not select
		// a different migration artifact or modify the shared source files.
		body = bytes.ReplaceAll(body, []byte("\r\n"), []byte("\n"))
		digest := sha256.Sum256(body)
		if hex.EncodeToString(digest[:]) != artifact.SHA256 {
			return nil, errors.New("forward migration artifact differs")
		}
		bodies = append(bodies, body)
		previous = artifact.Version
	}
	return bodies, nil
}

type migrationLedgerReader interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func verifyForwardBatchLedger(ctx context.Context, q migrationLedgerReader, batch []ForwardMigrationArtifact) error {
	if _, err := forwardBatchBodies(batch); err != nil {
		return err
	}
	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		return err
	}
	expected := map[string]bool{}
	for _, entry := range entries {
		if !entry.IsDir() {
			expected[entry.Name()] = true
		}
	}
	for _, artifact := range batch {
		delete(expected, artifact.Version)
	}
	if len(expected) == 0 {
		return ErrForwardBatchLedger
	}
	rows, err := q.QueryContext(ctx, "SELECT version FROM schema_migrations ORDER BY version")
	if err != nil {
		return err
	}
	defer rows.Close()
	applied := []string{}
	for rows.Next() {
		var version string
		if err := rows.Scan(&version); err != nil {
			return err
		}
		applied = append(applied, version)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return compareForwardBatchLedger(expected, applied)
}

func compareForwardBatchLedger(expected map[string]bool, applied []string) error {
	for _, version := range applied {
		if !expected[version] {
			return ErrForwardBatchLedger
		}
		delete(expected, version)
	}
	if len(expected) != 0 {
		return ErrForwardBatchLedger
	}
	return nil
}

func requireForwardPhysicalPin() error {
	pin, pinned, err := ExpectedDatabaseIdentityFromEnv()
	if err != nil || !pinned || pin.SystemIdentifier == "" {
		return errors.New("forward batch requires the complete physical database pin")
	}
	return nil
}

// VerifyForwardBatch reads the same admission predicate without schema effects.
func (db *DB) VerifyForwardBatch(ctx context.Context, batch []ForwardMigrationArtifact) error {
	if err := requireForwardPhysicalPin(); err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := VerifyExpectedDatabaseIdentity(ctx, tx); err != nil {
		return err
	}
	if err := verifyGenesisAllowed(ctx, tx); err != nil {
		return err
	}
	return verifyForwardBatchLedger(ctx, tx, batch)
}

func applyForwardBatch(ctx context.Context, conn *sql.Conn, batch []ForwardMigrationArtifact, verify func(context.Context, *sql.Tx) error) error {
	bodies, err := forwardBatchBodies(batch)
	if err != nil {
		return err
	}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Cooperating runners use the advisory lock; the table lock also excludes
	// another ledger writer between this admission and the atomic commit.
	if _, err := tx.ExecContext(ctx, "LOCK TABLE schema_migrations IN EXCLUSIVE MODE"); err != nil {
		return err
	}
	if err := verifyForwardBatchLedger(ctx, tx, batch); err != nil {
		return err
	}
	for i, artifact := range batch {
		if _, err := tx.ExecContext(ctx, string(bodies[i])); err != nil {
			return fmt.Errorf("forward migration %s: %w", artifact.Version, err)
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO schema_migrations(version) VALUES($1)", artifact.Version); err != nil {
			return err
		}
	}
	if err := verifyRequiredControlPlaneTables(ctx, tx); err != nil {
		return err
	}
	if err := verify(ctx, tx); err != nil {
		return err
	}
	return tx.Commit()
}
