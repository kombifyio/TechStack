package db

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/kombifyio/techstack/pkg/config"
)

const (
	// EnvExpectedDatabaseIdentity pins the database identity the migration
	// runner must observe before executing any DDL. Format:
	// "<database_name>" or "<database_name>@<postgres system_identifier>".
	EnvExpectedDatabaseIdentity = "TECHSTACK_EXPECTED_DATABASE_IDENTITY"
	// EnvAllowDBGenesis is the deliberate one-shot opt-in that lets a SaaS
	// boot apply the migration chain to a database whose schema_migrations
	// ledger is absent or empty. Only the exact value "true" opts in.
	EnvAllowDBGenesis = "TECHSTACK_ALLOW_DB_GENESIS"
)

var (
	// ErrDatabaseIdentityMismatch reports that the connected database does not
	// match the TECHSTACK_EXPECTED_DATABASE_IDENTITY pin.
	ErrDatabaseIdentityMismatch = errors.New("database identity mismatch")
	// ErrImplicitGenesisRefused reports that a SaaS boot found an absent or
	// empty schema_migrations ledger without the explicit genesis opt-in.
	ErrImplicitGenesisRefused = errors.New("implicit database genesis refused")
)

// ExpectedDatabaseIdentity is the parsed TECHSTACK_EXPECTED_DATABASE_IDENTITY
// pin. It anchors the runtime database env contract (AGENTS.md, incident
// 2026-08-12): a reverted DSN must fail closed instead of letting the boot
// chain rebuild the schema in the wrong, freshly-emptied database.
type ExpectedDatabaseIdentity struct {
	// DatabaseName must equal current_database() on the migration connection.
	DatabaseName string
	// SystemIdentifier, when non-empty, must equal the connected cluster's
	// pg_control_system().system_identifier.
	SystemIdentifier string
}

// ExpectedDatabaseIdentityFromEnv parses the expected-identity pin from the
// environment. It returns pinned=false when the variable is unset or blank
// and an error when the value is malformed.
func ExpectedDatabaseIdentityFromEnv() (pin ExpectedDatabaseIdentity, pinned bool, err error) {
	raw := strings.TrimSpace(os.Getenv(EnvExpectedDatabaseIdentity))
	if raw == "" {
		return ExpectedDatabaseIdentity{}, false, nil
	}
	name, systemIdentifier, hasSystemIdentifier := strings.Cut(raw, "@")
	name = strings.TrimSpace(name)
	systemIdentifier = strings.TrimSpace(systemIdentifier)
	if name == "" || (hasSystemIdentifier && systemIdentifier == "") {
		return ExpectedDatabaseIdentity{}, false, fmt.Errorf(
			"%s must be <database_name> or <database_name>@<postgres system_identifier>, got %q",
			EnvExpectedDatabaseIdentity, raw,
		)
	}
	return ExpectedDatabaseIdentity{DatabaseName: name, SystemIdentifier: systemIdentifier}, true, nil
}

// Verify compares an observed database identity against the pin, failing
// closed with a reason that distinguishes a stale env contract (the DSN
// targets a different database) from a moved database (the pinned database
// name resolved on a different physical cluster).
func (pin ExpectedDatabaseIdentity) Verify(databaseName, systemIdentifier string) error {
	if databaseName != pin.DatabaseName {
		return fmt.Errorf(
			"%w: env contract stale: connection targets database %q but %s pins %q; repair the DSN at its env-contract source (deployment secret store) instead of migrating",
			ErrDatabaseIdentityMismatch, databaseName, EnvExpectedDatabaseIdentity, pin.DatabaseName,
		)
	}
	if pin.SystemIdentifier != "" && systemIdentifier != pin.SystemIdentifier {
		return fmt.Errorf(
			"%w: database moved: database %q matches but PostgreSQL system_identifier %s differs from pinned %s; the physical cluster behind the DSN changed (restore/recreate) — re-verify the database before deliberately updating the pin",
			ErrDatabaseIdentityMismatch, databaseName, systemIdentifier, pin.SystemIdentifier,
		)
	}
	return nil
}

// VerifyExpectedDatabaseIdentity enforces the expected-identity pin against a
// live connection. It is a no-op when TECHSTACK_EXPECTED_DATABASE_IDENTITY is
// unset and fails closed when the pin cannot be verified.
func VerifyExpectedDatabaseIdentity(ctx context.Context, querier migrationTableQuerier) error {
	pin, pinned, err := ExpectedDatabaseIdentityFromEnv()
	if err != nil || !pinned {
		return err
	}
	var databaseName string
	if err := querier.QueryRowContext(ctx, "SELECT current_database()").Scan(&databaseName); err != nil {
		return fmt.Errorf("failed to load current database identity: %w", err)
	}
	systemIdentifier := ""
	if pin.SystemIdentifier != "" {
		if err := querier.QueryRowContext(
			ctx,
			"SELECT system_identifier::text FROM pg_catalog.pg_control_system()",
		).Scan(&systemIdentifier); err != nil {
			return fmt.Errorf(
				"%s pins a system_identifier but pg_control_system() is not readable on this connection; refusing to migrate an unverified database: %w",
				EnvExpectedDatabaseIdentity, err,
			)
		}
	}
	return pin.Verify(databaseName, systemIdentifier)
}

// verifyGenesisAllowed refuses implicit first-boot provisioning in SaaS
// deployment modes: when the schema_migrations ledger is absent or empty,
// applying the migration chain requires the explicit one-shot opt-in
// TECHSTACK_ALLOW_DB_GENESIS=true. Self-host/dev/local modes keep today's
// permissive behavior because fresh-database genesis is their normal path.
func verifyGenesisAllowed(ctx context.Context, querier migrationTableQuerier) error {
	mode, err := config.BootDeploymentModeFromEnv()
	if err != nil {
		return fmt.Errorf("failed to resolve deployment mode for the migration genesis guard: %w", err)
	}
	if !mode.IsSaaS() {
		return nil
	}

	var ledgerExists bool
	if err := querier.QueryRowContext(ctx, `
		SELECT pg_catalog.to_regclass(
			pg_catalog.format('%I.schema_migrations', pg_catalog.current_schema())
		) IS NOT NULL
	`).Scan(&ledgerExists); err != nil {
		return fmt.Errorf("failed to inspect schema_migrations ledger presence: %w", err)
	}
	ledgerState := "table is absent"
	if ledgerExists {
		var appliedCount int
		if err := querier.QueryRowContext(ctx, "SELECT COUNT(*) FROM schema_migrations").Scan(&appliedCount); err != nil {
			return fmt.Errorf("failed to count applied migrations in schema_migrations: %w", err)
		}
		if appliedCount > 0 {
			return nil
		}
		ledgerState = "table has zero applied migrations"
	}

	if genesisOptInFromEnv() {
		fmt.Printf("Database genesis explicitly allowed by %s=true (%s)\n", EnvAllowDBGenesis, ledgerState)
		return nil
	}
	return fmt.Errorf(
		"%w: production ledger empty (schema_migrations %s in deployment mode %q); refusing implicit genesis; set %s=true only for deliberate first-boot provisioning",
		ErrImplicitGenesisRefused, ledgerState, mode, EnvAllowDBGenesis,
	)
}

func genesisOptInFromEnv() bool {
	return strings.EqualFold(strings.TrimSpace(os.Getenv(EnvAllowDBGenesis)), "true")
}
