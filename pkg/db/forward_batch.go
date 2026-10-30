package db

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"os"
	"path"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// ForwardMigrationArtifact identifies an embedded migration, never caller SQL.
type ForwardMigrationArtifact struct {
	Version string `json:"version"`
	SHA256  string `json:"sha256"`
}

type ForwardBatchState struct {
	AppliedPrefix int                        `json:"applied_prefix"`
	Pending       []ForwardMigrationArtifact `json:"pending"`
	LedgerCount   int                        `json:"ledger_count"`
	EmbeddedCount int                        `json:"embedded_count"`
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
	// The manifest is the entire ordered tail of this binary, not an arbitrary
	// set whose omitted migrations could be mistaken for a committed baseline.
	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		return nil, err
	}
	tail := []string{}
	for _, entry := range entries {
		if !entry.IsDir() && entry.Name() >= batch[0].Version {
			tail = append(tail, entry.Name())
		}
	}
	if len(tail) != len(batch) {
		return nil, ErrForwardBatchLedger
	}
	for i, version := range tail {
		if version != batch[i].Version {
			return nil, ErrForwardBatchLedger
		}
	}
	return bodies, nil
}

type migrationLedgerReader interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func forwardBatchPrefix(ctx context.Context, q migrationLedgerReader, batch []ForwardMigrationArtifact) (int, error) {
	if _, err := forwardBatchBodies(batch); err != nil {
		return 0, err
	}
	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		return 0, err
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
		return 0, ErrForwardBatchLedger
	}
	rows, err := q.QueryContext(ctx, "SELECT version FROM schema_migrations ORDER BY version")
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	applied := []string{}
	for rows.Next() {
		var version string
		if err := rows.Scan(&version); err != nil {
			return 0, err
		}
		applied = append(applied, version)
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	return compareForwardBatchLedger(expected, applied, batch)
}

func compareForwardBatchLedger(expected map[string]bool, applied []string, batch []ForwardMigrationArtifact) (int, error) {
	seen := map[string]bool{}
	for _, version := range applied {
		if seen[version] {
			return 0, ErrForwardBatchLedger
		}
		seen[version] = true
	}
	for version := range expected {
		if !seen[version] {
			return 0, ErrForwardBatchLedger
		}
		delete(seen, version)
	}
	prefix := 0
	for _, artifact := range batch {
		if !seen[artifact.Version] {
			break
		}
		prefix++
		delete(seen, artifact.Version)
	}
	// A newer deploy may already have applied migrations this binary does not
	// embed. Rows numbered above the batch pass; any other row is drift.
	newest, _ := migrationNumber(batch[len(batch)-1].Version)
	for version := range seen {
		if number, ok := migrationNumber(version); !ok || number <= newest || prefix != len(batch) {
			return 0, ErrForwardBatchLedger
		}
	}
	return prefix, nil
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
	_, err := db.InspectForwardBatch(ctx, batch)
	return err
}

// InspectForwardBatch retains the actual, identity-qualified ledger admission
// used by the native operation receipt. No caller-supplied state selects SQL.
func (db *DB) InspectForwardBatch(ctx context.Context, batch []ForwardMigrationArtifact) (ForwardBatchState, error) {
	var state ForwardBatchState
	if err := requireForwardPhysicalPin(); err != nil {
		return state, err
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return state, err
	}
	defer tx.Rollback()
	if err := VerifyExpectedDatabaseIdentity(ctx, tx); err != nil {
		return state, err
	}
	if err := verifyGenesisAllowed(ctx, tx); err != nil {
		return state, err
	}
	prefix, err := forwardBatchPrefix(ctx, tx, batch)
	if err != nil {
		return state, err
	}
	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		return state, err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			state.EmbeddedCount++
		}
	}
	state.AppliedPrefix = prefix
	state.Pending = append([]ForwardMigrationArtifact{}, batch[prefix:]...)
	state.LedgerCount = state.EmbeddedCount - len(batch) + prefix
	return state, nil
}

// Production carries continuous short transactions on the tables a batch must
// hold exclusively (kombify-Techstack-qhrf: 168 and 173 create policies on
// servers, services, workers and the other inventory tables while 166 and 167
// already hold their own tables). One attempt therefore takes every existing
// table the pending bodies name before any migration SQL runs, in one sorted
// order, so no migration statement waits while it holds earlier exclusive
// locks. The first lock drains while the attempt holds nothing else, so that
// wait cannot be part of a cycle. Later locks wait only briefly, so a
// transaction queued behind an earlier lock that holds a later table costs a
// short wait and a rollback. A lock timeout or deadlock (either side may be the
// deadlock victim) rolls the whole attempt back, which releases every lock to
// the application, and the same batch is retried with backoff, with the
// refusing table moved to the front. The batch still commits in one transaction with
// its capabilities; a commit failure is never retried (inspect without replay).
var (
	forwardPreLock      = true
	forwardDrainTimeout = 8 * time.Second
	forwardLockTimeout  = 800 * time.Millisecond
	forwardPreLockLimit = 15 * time.Second
	// Inside migrationExecutionLimit and the delivery child's five minutes.
	forwardRetryWindow           = 180 * time.Second
	forwardMaxAttempts           = 200
	forwardBackoffBase           = 100 * time.Millisecond
	forwardBackoffMax            = 2 * time.Second
	forwardRetryLog    io.Writer = os.Stderr
)

var errForwardPreLockLimit = errors.New("forward batch locks not acquired within the attempt limit")

type forwardCommitError struct{ err error }

func (e forwardCommitError) Error() string { return "forward batch commit: " + e.err.Error() }
func (e forwardCommitError) Unwrap() error { return e.err }

// retryableForwardLock admits only waits the attempt lost before its commit.
func retryableForwardLock(err error) bool {
	var commit forwardCommitError
	if errors.As(err, &commit) {
		return false
	}
	if errors.Is(err, errForwardPreLockLimit) {
		return true
	}
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && (pgErr.Code == "55P03" || pgErr.Code == "40P01")
}

var (
	sqlLineComment  = regexp.MustCompile(`--[^\n]*`)
	sqlBlockComment = regexp.MustCompile(`(?s)/\*.*?\*/`)
	sqlName         = regexp.MustCompile(`"(?:[^"]|"")+"|[A-Za-z_][A-Za-z0-9_$]*`)
)

// forwardLockCandidates lists every name, and every dotted pair, in the bodies
// outside comments, string literals included (a FOREACH list of tables).
// The catalog resolves them; only existing tables become locks.
func forwardLockCandidates(bodies [][]byte) []string {
	seen := map[string]bool{}
	for _, body := range bodies {
		text := sqlBlockComment.ReplaceAllString(sqlLineComment.ReplaceAllString(string(body), " "), " ")
		tokens := sqlName.FindAllStringIndex(text, -1)
		for i, token := range tokens {
			name := text[token[0]:token[1]]
			seen[name] = true
			if i+1 < len(tokens) && text[token[1]:tokens[i+1][0]] == "." {
				seen[name+"."+text[tokens[i+1][0]:tokens[i+1][1]]] = true
			}
		}
	}
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// forwardLockName folds one candidate the way the parser does: an unquoted
// name to lower case, a quoted one exactly.
func forwardLockName(token string) string {
	if len(token) >= 2 && token[0] == '"' && token[len(token)-1] == '"' {
		return strings.ReplaceAll(token[1:len(token)-1], `""`, `"`)
	}
	return strings.ToLower(token)
}

// forwardLockSet resolves the candidates in the catalog as the bodies would
// (a dotted pair in its schema, a single name along the search path) without
// a lookup that could raise, and keeps only tables the migration role may lock:
// a usable schema and UPDATE, DELETE or TRUNCATE. A name it may only read or
// grant never becomes a lock the old runner would not have taken.
func forwardLockSet(ctx context.Context, tx *sql.Tx, bodies [][]byte) ([]string, error) {
	schemas, names := []string{}, []string{}
	for _, candidate := range forwardLockCandidates(bodies) {
		parts := sqlName.FindAllString(candidate, -1)
		switch len(parts) {
		case 1:
			schemas, names = append(schemas, ""), append(names, forwardLockName(parts[0]))
		case 2:
			schemas, names = append(schemas, forwardLockName(parts[0])), append(names, forwardLockName(parts[1]))
		}
	}
	if len(names) == 0 {
		return nil, nil
	}
	rows, err := tx.QueryContext(ctx, `
		WITH candidate AS (
			SELECT * FROM unnest($1::text[], $2::text[]) AS candidate(schema_name, rel_name)
		), search AS (
			SELECT path.nspname, path.position
			FROM pg_catalog.unnest(pg_catalog.current_schemas(false)) WITH ORDINALITY AS path(nspname, position)
		), resolved AS (
			SELECT c.oid FROM candidate k
			JOIN pg_catalog.pg_namespace n ON n.nspname = k.schema_name
			JOIN pg_catalog.pg_class c ON c.relnamespace = n.oid AND c.relname = k.rel_name
			WHERE k.schema_name <> ''
			UNION
			SELECT (SELECT c.oid FROM search s
				JOIN pg_catalog.pg_namespace n ON n.nspname = s.nspname
				JOIN pg_catalog.pg_class c ON c.relnamespace = n.oid AND c.relname = k.rel_name
				ORDER BY s.position LIMIT 1)
			FROM candidate k WHERE k.schema_name = ''
		)
		SELECT DISTINCT pg_catalog.format('%I.%I', n.nspname, c.relname)
		FROM resolved r
		JOIN pg_catalog.pg_class c ON c.oid = r.oid
		JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
		WHERE c.relkind IN ('r', 'p') AND c.relname <> 'schema_migrations'
		  AND n.nspname NOT LIKE 'pg\_%' AND n.nspname <> 'information_schema'
		  AND pg_catalog.has_schema_privilege(n.oid, 'USAGE')
		  AND pg_catalog.has_table_privilege(c.oid, 'UPDATE, DELETE, TRUNCATE')
		ORDER BY 1`, schemas, names)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	tables := []string{}
	for rows.Next() {
		var table string
		if err := rows.Scan(&table); err != nil {
			return nil, err
		}
		tables = append(tables, table)
	}
	return tables, rows.Err()
}

// forwardLockOrder keeps the sorted order and moves tables that refused an
// earlier attempt to the front, in the order they refused.
func forwardLockOrder(tables, first []string) []string {
	order := make([]string, 0, len(tables))
	for _, table := range first {
		if slices.Contains(tables, table) && !slices.Contains(order, table) {
			order = append(order, table)
		}
	}
	for _, table := range tables {
		if !slices.Contains(order, table) {
			order = append(order, table)
		}
	}
	return order
}

func forwardBackoff(attempt int) time.Duration {
	wait := forwardBackoffBase << min(attempt-1, 16)
	if wait <= 0 || wait > forwardBackoffMax {
		wait = forwardBackoffMax
	}
	return wait/2 + rand.N(wait/2+1)
}

func setForwardLockTimeout(ctx context.Context, tx *sql.Tx, timeout time.Duration) error {
	_, err := tx.ExecContext(ctx, "SELECT pg_catalog.set_config('lock_timeout', $1, true)", fmt.Sprintf("%dms", timeout.Milliseconds()))
	return err
}

func applyForwardBatch(ctx context.Context, conn *sql.Conn, batch []ForwardMigrationArtifact, verify func(context.Context, *sql.Tx) error) error {
	bodies, err := forwardBatchBodies(batch)
	if err != nil {
		return err
	}
	deadline := time.Now().Add(forwardRetryWindow)
	var first []string
	for attempt := 1; ; attempt++ {
		refused, err := applyForwardBatchAttempt(ctx, conn, batch, bodies, verify, first)
		if err == nil || !retryableForwardLock(err) {
			return err
		}
		wait := forwardBackoff(attempt)
		if attempt >= forwardMaxAttempts || time.Now().Add(wait+forwardPreLockLimit+forwardDrainTimeout).After(deadline) {
			return fmt.Errorf("forward batch refused its locks in %d attempts: %w", attempt, err)
		}
		if refused != "" && !slices.Contains(first, refused) {
			first = append(first, refused)
		}
		_, _ = fmt.Fprintf(forwardRetryLog, "forward batch attempt %d rolled back (%v); retrying in %s\n", attempt, err, wait.Round(time.Millisecond))
		select {
		case <-ctx.Done():
			return fmt.Errorf("forward batch retry stopped after %d attempts: %w", attempt, errors.Join(err, ctx.Err()))
		case <-time.After(wait):
		}
	}
}

// applyForwardBatchAttempt returns the table whose lock it lost, if any.
func applyForwardBatchAttempt(ctx context.Context, conn *sql.Conn, batch []ForwardMigrationArtifact, bodies [][]byte,
	verify func(context.Context, *sql.Tx) error, first []string) (string, error) {
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	if err := setForwardLockTimeout(ctx, tx, forwardLockTimeout); err != nil {
		return "", err
	}
	// Cooperating runners use the advisory lock; the table lock also excludes
	// another ledger writer between this admission and the atomic commit.
	if _, err := tx.ExecContext(ctx, "LOCK TABLE schema_migrations IN EXCLUSIVE MODE"); err != nil {
		return "", err
	}
	prefix, err := forwardBatchPrefix(ctx, tx, batch)
	if err != nil {
		return "", err
	}
	if forwardPreLock {
		tables, err := forwardLockSet(ctx, tx, bodies[prefix:])
		if err != nil {
			return "", err
		}
		started := time.Now()
		for i, table := range forwardLockOrder(tables, first) {
			if time.Since(started) > forwardPreLockLimit {
				return table, errForwardPreLockLimit
			}
			timeout := forwardLockTimeout
			if i == 0 {
				timeout = forwardDrainTimeout
			}
			if err := setForwardLockTimeout(ctx, tx, timeout); err != nil {
				return "", err
			}
			// The name is the catalog's own quoted identifier, never body text.
			if _, err := tx.ExecContext(ctx, "LOCK TABLE "+table+" IN ACCESS EXCLUSIVE MODE"); err != nil {
				return table, fmt.Errorf("forward lock %s: %w", table, err)
			}
		}
		// A body that sets no lock_timeout of its own still waits only briefly.
		if err := setForwardLockTimeout(ctx, tx, forwardLockTimeout); err != nil {
			return "", err
		}
	}
	for i, artifact := range batch {
		if i < prefix {
			continue // Never execute SQL or insert a ledger row for a committed prefix.
		}
		if err := executePendingMigration(ctx, tx, artifact.Version, bodies[i]); err != nil {
			return "", fmt.Errorf("forward migration %s: %w", artifact.Version, err)
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO schema_migrations(version) VALUES($1)", artifact.Version); err != nil {
			return "", err
		}
	}
	if err := verifyRequiredControlPlaneTables(ctx, tx); err != nil {
		return "", err
	}
	if err := verify(ctx, tx); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", forwardCommitError{err}
	}
	return "", nil
}
