package workflow

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// PgStore is a Postgres-backed RunStore (epic kombify-Techstack-a7x.1). It
// mirrors the semantics of the PocketBase *Store: run_id is the stable key,
// status changes are validated against the run/step state machines, and the
// JSON map fields round-trip as jsonb. The workflow tables are global (no tenant
// RLS) so the worker can poll runnable runs and due timers across all tenants.
//
// Schema: pkg/db/migrations/008_ril_workflows.sql.
type PgStore struct {
	db *sql.DB
}

var _ RunStore = (*PgStore)(nil)

// NewPgStore builds a Postgres-backed workflow store over the given *sql.DB.
func NewPgStore(db *sql.DB) *PgStore { return &PgStore{db: db} }

const runColumns = `run_id, type, status, current_step, input, context, ` +
	`owner_id, server_id, card_id, awaiting_signal, error, started_at, finished_at, created, updated`

const stepColumns = `id, run_id, step_index, name, status, attempt, input, output, ` +
	`error, idempotency_key, started_at, finished_at`

// ---------------------------------------------------------------------------
// Runs
// ---------------------------------------------------------------------------

func (s *PgStore) CreateRun(run *Run) error {
	if s == nil || s.db == nil {
		return errors.New("workflow: postgres store not configured")
	}
	if run.RunID == "" {
		run.RunID = uuid.New().String()
	}
	run.Status = RunPending
	const q = `INSERT INTO ril_workflow_runs
		(run_id, type, status, current_step, input, context, owner_id, server_id,
		 card_id, awaiting_signal, error, created, updated)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11, now(), now())
		RETURNING created, updated`
	if err := s.db.QueryRowContext(context.Background(), q,
		run.RunID, string(run.Type), string(run.Status), run.CurrentStep,
		marshalJSONMap(run.Input), marshalJSONMap(run.Context),
		run.OwnerID, run.ServerID, run.CardID, run.AwaitingSignal, run.Error,
	).Scan(&run.Created, &run.Updated); err != nil {
		return fmt.Errorf("create run: %w", err)
	}
	run.ID = run.RunID
	return nil
}

func (s *PgStore) GetRun(runID string) (*Run, error) {
	row := s.db.QueryRowContext(context.Background(),
		`SELECT `+runColumns+` FROM ril_workflow_runs WHERE run_id = $1`, runID)
	run, err := scanRun(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: run %s", ErrNotFound, runID)
	}
	if err != nil {
		return nil, fmt.Errorf("get run: %w", err)
	}
	return run, nil
}

func (s *PgStore) UpdateRun(run *Run) error {
	var current string
	err := s.db.QueryRowContext(context.Background(),
		`SELECT status FROM ril_workflow_runs WHERE run_id = $1`, run.RunID).Scan(&current)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: run %s", ErrNotFound, run.RunID)
	}
	if err != nil {
		return fmt.Errorf("update run (load): %w", err)
	}
	if RunStatus(current) != run.Status {
		if verr := ValidateRunTransition(RunStatus(current), run.Status); verr != nil {
			return verr
		}
	}
	const q = `UPDATE ril_workflow_runs
		SET status = $1, current_step = $2, context = $3, awaiting_signal = $4,
		    error = $5, updated = now()
		WHERE run_id = $6
		RETURNING updated`
	if err = s.db.QueryRowContext(context.Background(), q,
		string(run.Status), run.CurrentStep, marshalJSONMap(run.Context),
		run.AwaitingSignal, run.Error, run.RunID,
	).Scan(&run.Updated); err != nil {
		return fmt.Errorf("update run: %w", err)
	}
	// started_at / finished_at are write-once: only set when non-nil so an
	// already-recorded timestamp is never cleared (mirrors the PocketBase store).
	if run.StartedAt != nil {
		if _, err = s.db.ExecContext(context.Background(),
			`UPDATE ril_workflow_runs SET started_at = $1 WHERE run_id = $2`,
			run.StartedAt.UTC(), run.RunID); err != nil {
			return fmt.Errorf("update run started_at: %w", err)
		}
	}
	if run.FinishedAt != nil {
		if _, err = s.db.ExecContext(context.Background(),
			`UPDATE ril_workflow_runs SET finished_at = $1 WHERE run_id = $2`,
			run.FinishedAt.UTC(), run.RunID); err != nil {
			return fmt.Errorf("update run finished_at: %w", err)
		}
	}
	return nil
}

func (s *PgStore) ListRunsByStatus(status RunStatus, limit int) ([]*Run, error) {
	const q = `SELECT ` + runColumns + ` FROM ril_workflow_runs WHERE status = $1 ORDER BY updated ASC LIMIT $2`
	rows, err := s.db.QueryContext(context.Background(), q, string(status), limitArg(limit))
	if err != nil {
		return nil, fmt.Errorf("list runs by status: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return scanRuns(rows)
}

func (s *PgStore) FindSuspendedBySignal(signalKey string) (*Run, error) {
	row := s.db.QueryRowContext(context.Background(),
		`SELECT `+runColumns+` FROM ril_workflow_runs
		 WHERE status = $1 AND awaiting_signal = $2 ORDER BY updated ASC LIMIT 1`,
		string(RunSuspended), signalKey)
	run, err := scanRun(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: suspended run awaiting %s", ErrNotFound, signalKey)
	}
	if err != nil {
		return nil, fmt.Errorf("find suspended by signal: %w", err)
	}
	return run, nil
}

// ---------------------------------------------------------------------------
// Steps
// ---------------------------------------------------------------------------

func (s *PgStore) CreateStep(step *Step) error {
	if step.ID == "" {
		step.ID = uuid.New().String()
	}
	if step.Status == "" {
		step.Status = StepPending
	}
	const q = `INSERT INTO ril_workflow_steps
		(id, run_id, step_index, name, status, attempt, input, output, error,
		 idempotency_key, created, updated)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10, now(), now())`
	if _, err := s.db.ExecContext(context.Background(), q,
		step.ID, step.RunID, step.StepIndex, step.Name, string(step.Status),
		step.Attempt, marshalJSONMap(step.Input), marshalJSONMap(step.Output),
		step.Error, step.IdempotencyKey,
	); err != nil {
		return fmt.Errorf("create step: %w", err)
	}
	return nil
}

func (s *PgStore) UpdateStep(step *Step) error {
	var current string
	err := s.db.QueryRowContext(context.Background(),
		`SELECT status FROM ril_workflow_steps WHERE run_id = $1 AND step_index = $2`,
		step.RunID, step.StepIndex).Scan(&current)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: step %s/%d", ErrNotFound, step.RunID, step.StepIndex)
	}
	if err != nil {
		return fmt.Errorf("update step (load): %w", err)
	}
	if StepStatus(current) != step.Status {
		if verr := ValidateStepTransition(StepStatus(current), step.Status); verr != nil {
			return verr
		}
	}
	const q = `UPDATE ril_workflow_steps
		SET status = $1, attempt = $2, output = $3, error = $4,
		    idempotency_key = CASE WHEN $5 <> '' THEN $5 ELSE idempotency_key END,
		    updated = now()
		WHERE run_id = $6 AND step_index = $7`
	if _, err = s.db.ExecContext(context.Background(), q,
		string(step.Status), step.Attempt, marshalJSONMap(step.Output), step.Error,
		step.IdempotencyKey, step.RunID, step.StepIndex,
	); err != nil {
		return fmt.Errorf("update step: %w", err)
	}
	if step.StartedAt != nil {
		if _, err = s.db.ExecContext(context.Background(),
			`UPDATE ril_workflow_steps SET started_at = $1 WHERE run_id = $2 AND step_index = $3`,
			step.StartedAt.UTC(), step.RunID, step.StepIndex); err != nil {
			return fmt.Errorf("update step started_at: %w", err)
		}
	}
	if step.FinishedAt != nil {
		if _, err = s.db.ExecContext(context.Background(),
			`UPDATE ril_workflow_steps SET finished_at = $1 WHERE run_id = $2 AND step_index = $3`,
			step.FinishedAt.UTC(), step.RunID, step.StepIndex); err != nil {
			return fmt.Errorf("update step finished_at: %w", err)
		}
	}
	return nil
}

func (s *PgStore) GetStep(runID string, idx int) (*Step, error) {
	row := s.db.QueryRowContext(context.Background(),
		`SELECT `+stepColumns+` FROM ril_workflow_steps WHERE run_id = $1 AND step_index = $2`,
		runID, idx)
	step, err := scanStep(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: step %s/%d", ErrNotFound, runID, idx)
	}
	if err != nil {
		return nil, fmt.Errorf("get step: %w", err)
	}
	return step, nil
}

func (s *PgStore) ListSteps(runID string) ([]*Step, error) {
	rows, err := s.db.QueryContext(context.Background(),
		`SELECT `+stepColumns+` FROM ril_workflow_steps WHERE run_id = $1 ORDER BY step_index ASC`,
		runID)
	if err != nil {
		return nil, fmt.Errorf("list steps: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []*Step{}
	for rows.Next() {
		step, serr := scanStep(rows)
		if serr != nil {
			return nil, fmt.Errorf("list steps scan: %w", serr)
		}
		out = append(out, step)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------------
// Timers
// ---------------------------------------------------------------------------

func (s *PgStore) CreateTimer(timer *Timer) error {
	if timer.ID == "" {
		timer.ID = uuid.New().String()
	}
	const q = `INSERT INTO ril_workflow_timers
		(id, run_id, kind, fire_at, fired, signal_key, created)
		VALUES ($1,$2,$3,$4,$5,$6, now())`
	if _, err := s.db.ExecContext(context.Background(), q,
		timer.ID, timer.RunID, string(timer.Kind), timer.FireAt.UTC(),
		timer.Fired, timer.SignalKey,
	); err != nil {
		return fmt.Errorf("create timer: %w", err)
	}
	return nil
}

func (s *PgStore) ListDueTimers(now time.Time, limit int) ([]*Timer, error) {
	const q = `SELECT id, run_id, kind, fire_at, fired, signal_key
		FROM ril_workflow_timers WHERE fired = false AND fire_at <= $1 ORDER BY fire_at ASC LIMIT $2`
	rows, err := s.db.QueryContext(context.Background(), q, now.UTC(), limitArg(limit))
	if err != nil {
		return nil, fmt.Errorf("list due timers: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []*Timer{}
	for rows.Next() {
		var (
			t    Timer
			kind string
		)
		if serr := rows.Scan(&t.ID, &t.RunID, &kind, &t.FireAt, &t.Fired, &t.SignalKey); serr != nil {
			return nil, fmt.Errorf("list due timers scan: %w", serr)
		}
		t.Kind = TimerKind(kind)
		out = append(out, &t)
	}
	return out, rows.Err()
}

func (s *PgStore) MarkTimerFired(timerID string) error {
	res, err := s.db.ExecContext(context.Background(),
		`UPDATE ril_workflow_timers SET fired = true WHERE id = $1`, timerID)
	if err != nil {
		return fmt.Errorf("mark timer fired: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("mark timer fired (rows): %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// ---------------------------------------------------------------------------
// scanning helpers
// ---------------------------------------------------------------------------

type rowScanner interface {
	Scan(dest ...any) error
}

func scanRun(sc rowScanner) (*Run, error) {
	var (
		run         Run
		typ, status string
		input, ctx  []byte
		startedAt   sql.NullTime
		finishedAt  sql.NullTime
	)
	if err := sc.Scan(
		&run.RunID, &typ, &status, &run.CurrentStep, &input, &ctx,
		&run.OwnerID, &run.ServerID, &run.CardID, &run.AwaitingSignal, &run.Error,
		&startedAt, &finishedAt, &run.Created, &run.Updated,
	); err != nil {
		return nil, err
	}
	run.ID = run.RunID
	run.Type = RunType(typ)
	run.Status = RunStatus(status)
	run.Input = unmarshalJSONMap(input)
	run.Context = unmarshalJSONMap(ctx)
	if startedAt.Valid {
		t := startedAt.Time
		run.StartedAt = &t
	}
	if finishedAt.Valid {
		t := finishedAt.Time
		run.FinishedAt = &t
	}
	return &run, nil
}

func scanRuns(rows *sql.Rows) ([]*Run, error) {
	out := []*Run{}
	for rows.Next() {
		run, err := scanRun(rows)
		if err != nil {
			return nil, fmt.Errorf("scan run: %w", err)
		}
		out = append(out, run)
	}
	return out, rows.Err()
}

func scanStep(sc rowScanner) (*Step, error) {
	var (
		step          Step
		status        string
		input, output []byte
		startedAt     sql.NullTime
		finishedAt    sql.NullTime
	)
	if err := sc.Scan(
		&step.ID, &step.RunID, &step.StepIndex, &step.Name, &status, &step.Attempt,
		&input, &output, &step.Error, &step.IdempotencyKey, &startedAt, &finishedAt,
	); err != nil {
		return nil, err
	}
	step.Status = StepStatus(status)
	step.Input = unmarshalJSONMap(input)
	step.Output = unmarshalJSONMap(output)
	if startedAt.Valid {
		t := startedAt.Time
		step.StartedAt = &t
	}
	if finishedAt.Valid {
		t := finishedAt.Time
		step.FinishedAt = &t
	}
	return &step, nil
}

// limitArg maps a limit to a bound parameter for `LIMIT $n`: a positive limit
// is passed as-is, while <= 0 becomes NULL (Postgres treats LIMIT NULL as no
// limit). Keeping the limit a bound parameter avoids SQL string building (gosec
// G202).
func limitArg(limit int) any {
	if limit > 0 {
		return limit
	}
	return nil
}

// marshalJSONMap encodes a map for a jsonb column; nil/empty becomes "{}" so the
// NOT NULL DEFAULT '{}' columns stay valid. Returns a string (not []byte) so the
// pgx driver sends it as text → jsonb (a []byte arg can be encoded as bytea,
// which has no implicit cast to jsonb).
func marshalJSONMap(m map[string]any) string {
	if len(m) == 0 {
		return "{}"
	}
	b, err := json.Marshal(m)
	if err != nil {
		return "{}"
	}
	return string(b)
}

// unmarshalJSONMap decodes a jsonb column into a map; empty/null/{} yields nil so
// callers treat it as "no data" (mirrors the PocketBase store's decodeJSONMap).
func unmarshalJSONMap(b []byte) map[string]any {
	if len(b) == 0 || string(b) == "null" || string(b) == "{}" {
		return nil
	}
	var m map[string]any
	if json.Unmarshal(b, &m) != nil {
		return nil
	}
	return m
}
