package db

import (
	"context"
	"database/sql"
)

// forwardMigration carries one exact embedded forward batch and its in-transaction
// capability verification into the existing locked migration runner.
type forwardMigration struct {
	verify func(context.Context, *sql.Tx) error
	batch  []ForwardMigrationArtifact
}
