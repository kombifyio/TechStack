package db

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"path"
)

type forwardMigration struct {
	version, digest string
	verify          func(context.Context, *sql.Tx) error
}

// Called only after the existing runner holds its migration advisory lock,
// before any schema effect. An applied/uncertain migration is inspected rather
// than replayed; a fresh database cannot use this forward-only purpose.
func verifyForwardMigration(ctx context.Context, conn *sql.Conn, expected forwardMigration) error {
	if path.Base(expected.version) != expected.version || len(expected.digest) != 64 {
		return fmt.Errorf("exact forward migration identity required")
	}
	body, err := migrationsFS.ReadFile(path.Join("migrations", expected.version))
	if err != nil {
		return fmt.Errorf("forward migration is not embedded")
	}
	digest := sha256.Sum256(body)
	if hex.EncodeToString(digest[:]) != expected.digest {
		return fmt.Errorf("forward migration artifact digest differs")
	}
	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		return err
	}
	pending := ""
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		var applied bool
		if err := conn.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version=$1)", entry.Name()).Scan(&applied); err != nil {
			return fmt.Errorf("forward migration requires the existing ledger")
		}
		if !applied {
			if pending != "" || entry.Name() != expected.version {
				return fmt.Errorf("forward migration has an unexpected pending batch")
			}
			pending = entry.Name()
		}
	}
	if pending != expected.version {
		return fmt.Errorf("forward migration already applied; inspect its original receipt")
	}
	return nil
}
