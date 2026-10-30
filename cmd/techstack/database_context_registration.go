package main

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"regexp"
)

// Only database-delivery apply calls this adapter after its existing compiled
// source, migration principal and physical database admission. It writes the
// canonical Common key table; there is no product-local registry or signer.
type databaseContextKeyRegistration struct{ id, secret string }

func databaseContextKeyFromMigrationEnv() (databaseContextKeyRegistration, error) {
	key := databaseContextKeyRegistration{
		id: os.Getenv("TECHSTACK_DB_CONTEXT_KEY_ID"), secret: os.Getenv("TECHSTACK_DB_CONTEXT_KEY"),
	}
	if !regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`).MatchString(key.id) || len([]byte(key.secret)) < 32 {
		return databaseContextKeyRegistration{}, errors.New("database context issuer custody is required for migration apply")
	}
	return key, nil
}

func registerDatabaseContextKey(ctx context.Context, tx *sql.Tx, key databaseContextKeyRegistration) error {
	// Parameter binding keeps key material out of SQL text and diagnostics.
	// Create-only registration cannot rotate, overwrite or revive a retired key.
	if _, err := tx.ExecContext(ctx, `INSERT INTO kombify_ctx.keys (key_id,secret)
		VALUES ($1,$2) ON CONFLICT (key_id) DO NOTHING`, key.id, []byte(key.secret)); err != nil {
		return errors.New("database context key registration failed")
	}
	var matches bool
	if err := tx.QueryRowContext(ctx, `SELECT secret=$2 AND not_before<=statement_timestamp()
		AND (not_after IS NULL OR not_after>statement_timestamp())
		FROM kombify_ctx.keys WHERE key_id=$1 FOR SHARE`, key.id, []byte(key.secret)).Scan(&matches); err != nil || !matches {
		return errors.New("database context key registration readback refused")
	}
	return nil
}
