package db

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"

	"github.com/kombifyio/techstack/internal/gocommon/migrationclass"
)

// verifyForwardExpandPosture is deliberately fixed and read-only. The merge
// lane must never reuse the release callback that installs capabilities or
// registers context keys.
func verifyForwardExpandPosture(ctx context.Context, tx *sql.Tx) error {
	var safe bool
	if err := tx.QueryRowContext(ctx, `SELECT NOT rolsuper AND NOT rolbypassrls
		FROM pg_catalog.pg_roles WHERE rolname=current_user`).Scan(&safe); err != nil {
		return err
	}
	if !safe {
		return errors.New("database migration owner bypasses tenant authority")
	}
	if err := tx.QueryRowContext(ctx, `SELECT NOT EXISTS (
		SELECT 1 FROM pg_catalog.pg_proc p,
		LATERAL pg_catalog.aclexplode(COALESCE(p.proacl,pg_catalog.acldefault('f',p.proowner))) a
		WHERE p.oid='public.techstack_subject_ref(text)'::pg_catalog.regprocedure
		AND a.grantee=0 AND a.privilege_type='EXECUTE')`).Scan(&safe); err != nil {
		return err
	}
	if !safe {
		return errors.New("subject helper retains public execution")
	}
	owner, err := ApplicationAuthorityOwner(ctx, tx)
	if err != nil {
		return err
	}
	for _, role := range []string{HostedApplicationRole, BackupWorkerRole} {
		if err := VerifyApplicationPrivileges(ctx, tx, ApplicationPrincipal{Role: role, AuthorityOwner: owner}, true); err != nil {
			return err
		}
	}
	return nil
}

type ForwardExpandInspection struct {
	State         ForwardBatchState `json:"state"`
	AdmissibleEnd int               `json:"admissible_end"`
	Held          string            `json:"held,omitempty"`
}

// InspectForwardExpand independently reads the same identity-qualified
// ledger and posture after an expand attempt. It never applies migrations.
func (db *DB) InspectForwardExpand(ctx context.Context, batch []ForwardMigrationArtifact) (ForwardExpandInspection, error) {
	var state ForwardBatchState
	var result ForwardExpandInspection
	ctx, cancel := context.WithTimeout(ctx, migrationExecutionLimit)
	defer cancel()
	manifest, err := DeliveryForwardBatch()
	if err != nil {
		return result, err
	}
	if !slices.Equal(batch, manifest) {
		return result, errors.New("forward expand requires the complete embedded delivery manifest")
	}
	if err := requireForwardPhysicalPin(); err != nil {
		return result, err
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	if err := qualifyForwardExpandTransaction(ctx, tx); err != nil {
		return result, err
	}
	prefix, err := forwardBatchPrefix(ctx, tx, batch)
	if err != nil {
		return result, err
	}
	end, err := embeddedForwardExpandLimit(batch, prefix)
	if err != nil {
		return result, err
	}
	if err := verifyRequiredControlPlaneTables(ctx, tx); err != nil {
		return result, err
	}
	if err := verifyForwardExpandPosture(ctx, tx); err != nil {
		return result, err
	}
	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		return result, err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			state.EmbeddedCount++
		}
	}
	state.AppliedPrefix = prefix
	state.Pending = append([]ForwardMigrationArtifact{}, batch[prefix:]...)
	state.LedgerCount = state.EmbeddedCount - len(batch) + prefix
	result.State, result.AdmissibleEnd = state, end
	if end < len(batch) {
		result.Held = batch[end].Version
	}
	return result, nil
}

func qualifyForwardExpandTransaction(ctx context.Context, tx *sql.Tx) error {
	// pg_catalog is implicitly first, public stays the CREATE target and the
	// explicitly listed temporary schema cannot shadow the public ledger.
	if _, err := tx.ExecContext(ctx, "SET LOCAL search_path = public, pg_temp"); err != nil {
		return err
	}
	var target, first string
	if err := tx.QueryRowContext(ctx, "SELECT pg_catalog.current_schema(), (pg_catalog.current_schemas(true))[1]").Scan(&target, &first); err != nil {
		return err
	}
	if target != "public" || first != "pg_catalog" {
		return errors.New("forward expand search path is not qualified")
	}
	if err := VerifyExpectedDatabaseIdentity(ctx, tx); err != nil {
		return err
	}
	return verifyGenesisAllowed(ctx, tx)
}

func embeddedForwardExpandLimit(batch []ForwardMigrationArtifact, prefix int) (int, error) {
	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		return 0, err
	}
	history := make([]forwardExpandInput, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		body, err := migrationsFS.ReadFile(path.Join("migrations", entry.Name()))
		if err != nil {
			return 0, err
		}
		history = append(history, forwardExpandInput{name: entry.Name(), body: bytes.ReplaceAll(body, []byte("\r\n"), []byte("\n"))})
	}
	base := len(history) - len(batch)
	if base < 0 || prefix < 0 || prefix > len(batch) {
		return 0, ErrForwardBatchLedger
	}
	limit, err := forwardExpandLimit(history, base+prefix)
	if err != nil {
		return 0, err
	}
	return limit - base, nil
}

// forwardExpandInput comes only from the binary's embedded migration history.
// The complete history gives the shared classifier its prior relation and
// routine names; the database ledger determines the first pending entry.
type forwardExpandInput struct {
	name string
	body []byte
}

// forwardExpandLimit returns the exclusive end of the consecutive safe
// pending prefix. The first contract and every later migration remain held.
func forwardExpandLimit(history []forwardExpandInput, firstPending int) (int, error) {
	if firstPending < 0 || firstPending > len(history) {
		return 0, fmt.Errorf("invalid forward expand ledger prefix")
	}
	migrations := make([]migrationclass.Migration, 0, len(history))
	previous := ""
	for _, entry := range history {
		number, ok := migrationNumber(entry.name)
		if !ok || entry.name <= previous {
			return 0, fmt.Errorf("invalid embedded migration order")
		}
		migrations = append(migrations, migrationclass.Migration{
			ID: strings.TrimSuffix(entry.name, ".sql"), Name: entry.name,
			Version: int64(number), Body: entry.body, SQL: true,
		})
		previous = entry.name
	}
	rules := migrationclass.Rules{Format: migrationclass.FormatPlain,
		ExpandSuffix: AdditiveMigrationSuffix, RuntimeRoles: []string{HostedApplicationRole}}
	verdicts := rules.ClassifyAll(migrations)
	for index := firstPending; index < len(history); index++ {
		if verdicts[index].Class != migrationclass.Expand || ValidateAdditiveMigrationSQL(string(history[index].body)) != nil {
			return index, nil
		}
	}
	return len(history), nil
}
