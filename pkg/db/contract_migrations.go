package db

import (
	"bytes"
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strconv"
)

// Contract migrations (D-2026-10-27-06, platform-0bd32) remove or narrow
// objects an older binary may still use. They live apart from the expand
// migrations, with their own ledger, so a held contract step never blocks an
// expand migration. A contract file is named NNN_name.sql, where NNN is the
// newest expand migration number at which no code uses the objects any more,
// and starts with the expand-lint marker
// `-- kombify:contract objects=<schema.table[.column]>,...`. It applies only
// when every live revision reports health schema.embedded_max >= NNN.
//
//go:embed all:migrations/contract
var contractMigrationsFS embed.FS

var (
	contractNamePattern   = regexp.MustCompile(`^([0-9]+)_[a-z0-9_]+\.sql$`)
	contractMarkerPattern = regexp.MustCompile(`^-- kombify:contract objects=[A-Za-z0-9_.]+(?:,[A-Za-z0-9_.]+)*\r?\n`)
)

// ContractMigration is one embedded contract step.
type ContractMigration struct {
	Version string
	Number  int
	body    []byte
}

// ContractMigrationResult is the native receipt of one contract pass.
type ContractMigrationResult struct {
	LiveEmbeddedMin int      `json:"live_embedded_min"`
	Applied         []string `json:"applied"`
	AlreadyApplied  []string `json:"already_applied"`
	Held            []string `json:"held"`
}

// ContractMigrations lists the embedded contract steps in number order.
func ContractMigrations() ([]ContractMigration, error) {
	entries, err := contractMigrationsFS.ReadDir("migrations/contract")
	if err != nil {
		return nil, err
	}
	out := []ContractMigration{}
	for _, entry := range entries {
		if entry.IsDir() || entry.Name()[0] == '.' {
			continue
		}
		match := contractNamePattern.FindStringSubmatch(entry.Name())
		if match == nil {
			return nil, fmt.Errorf("contract migration %s needs a NNN_name.sql name", entry.Name())
		}
		number, _ := strconv.Atoi(match[1])
		body, err := contractMigrationsFS.ReadFile(path.Join("migrations/contract", entry.Name()))
		if err != nil {
			return nil, err
		}
		if !contractMarkerPattern.Match(body) {
			return nil, fmt.Errorf("contract migration %s must start with the kombify:contract objects marker", entry.Name())
		}
		out = append(out, ContractMigration{Version: entry.Name(), Number: number, body: bytes.ReplaceAll(body, []byte("\r\n"), []byte("\n"))})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Version < out[j].Version })
	return out, nil
}

// ApplyContractMigrations applies each pending contract step whose number
// every live revision already embeds (liveEmbeddedMin) and holds the rest.
// Each step commits with its ledger row in one transaction under the shared
// migration advisory lock. A held step is no failure: the next deploy retries.
func (db *DB) ApplyContractMigrations(ctx context.Context, liveEmbeddedMin int) (ContractMigrationResult, error) {
	result := ContractMigrationResult{LiveEmbeddedMin: liveEmbeddedMin, Applied: []string{}, AlreadyApplied: []string{}, Held: []string{}}
	steps, err := ContractMigrations()
	if err != nil || len(steps) == 0 {
		return result, err
	}
	if err := requireForwardPhysicalPin(); err != nil {
		return result, err
	}
	ctx, cancel := context.WithTimeout(ctx, migrationExecutionLimit)
	defer cancel()
	conn, err := db.Conn(ctx)
	if err != nil {
		return result, err
	}
	defer func() { _ = conn.Close() }()
	if err := VerifyExpectedDatabaseIdentity(ctx, conn); err != nil {
		return result, err
	}
	for _, step := range steps {
		applied, err := applyContractStep(ctx, conn, step, liveEmbeddedMin)
		if err != nil {
			return result, fmt.Errorf("contract migration %s: %w", step.Version, err)
		}
		switch applied {
		case contractApplied:
			result.Applied = append(result.Applied, step.Version)
		case contractAlreadyApplied:
			result.AlreadyApplied = append(result.AlreadyApplied, step.Version)
		default:
			result.Held = append(result.Held, step.Version)
		}
	}
	return result, nil
}

type contractOutcome int

const (
	contractHeld contractOutcome = iota
	contractApplied
	contractAlreadyApplied
)

func applyContractStep(ctx context.Context, conn *sql.Conn, step ContractMigration, liveEmbeddedMin int) (contractOutcome, error) {
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return contractHeld, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, migrationAdvisoryLockKey); err != nil {
		return contractHeld, err
	}
	if _, err := tx.ExecContext(ctx, `SET LOCAL lock_timeout = '10s'; SET LOCAL statement_timeout = '2min'`); err != nil {
		return contractHeld, err
	}
	if _, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_contract_migrations (
		version TEXT PRIMARY KEY,
		applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
	)`); err != nil {
		return contractHeld, err
	}
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_contract_migrations WHERE version = $1`, step.Version).Scan(&count); err != nil {
		return contractHeld, err
	}
	if count > 0 {
		return contractAlreadyApplied, tx.Commit()
	}
	if step.Number > liveEmbeddedMin {
		// A live revision may still use the objects. Keep only the ledger table.
		return contractHeld, tx.Commit()
	}
	if _, err := tx.ExecContext(ctx, string(step.body)); err != nil {
		return contractHeld, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO schema_contract_migrations (version) VALUES ($1)`, step.Version); err != nil {
		return contractHeld, err
	}
	if err := tx.Commit(); err != nil {
		return contractHeld, errors.Join(errors.New("commit outcome unknown; inspect the ledger without replay"), err)
	}
	return contractApplied, nil
}
