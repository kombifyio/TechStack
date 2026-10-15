package workflow

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/kombifyio/techstack/pkg/db"

	"github.com/google/uuid"
)

// PgStore is the Postgres-backed RunStore. run_id is the stable key, status
// changes are validated against the run/step state machines, and JSON map
// fields round-trip as jsonb. Each operation is tenant-bound before SQL; worker
// discovery uses only the protected secret-free tenant directory.
//
// Schema: migrations008 plus the governed forward tenant-scope migration151.
type PgStore struct {
	db       *sql.DB
	ctx      context.Context
	tenantID string
}

var _ RunStore = (*PgStore)(nil)

// NewPgStore builds a Postgres-backed workflow store over the given *sql.DB.
func NewPgStore(db *sql.DB) *PgStore { return &PgStore{db: db} }

const runColumns = `tenant_id, run_id, type, status, current_step, input, context, ` +
	`owner_id, server_id, card_id, awaiting_signal, error, started_at, finished_at, created, updated`

const stepColumns = `id, run_id, step_index, name, status, attempt, input, output, ` +
	`error, idempotency_key, started_at, finished_at`

// ---------------------------------------------------------------------------
// Runs
// ---------------------------------------------------------------------------

func (s *PgStore) CreateRun(run *Run) error {
	if s == nil || run == nil || run.TenantID != s.tenantID {
		return fmt.Errorf("workflow: tenant conflict")
	}
	if id, ok := run.Input["tenant_id"].(string); ok && id != s.tenantID {
		return fmt.Errorf("workflow: tenant input conflict")
	}
	if s == nil || s.db == nil {
		return errors.New("workflow: postgres store not configured")
	}
	if run.RunID == "" {
		run.RunID = uuid.New().String()
	}
	run.Status = RunPending
	ctx := s.ctx
	tx, err := s.beginTenant()
	if err != nil {
		return fmt.Errorf("create run transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	const q = `INSERT INTO ril_workflow_runs
		(tenant_id, run_id, type, status, current_step, input, context, owner_id, server_id,
		 card_id, awaiting_signal, error, created, updated)
		VALUES ($12,$1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11, now(), now())
		RETURNING created, updated`
	if err := tx.QueryRowContext(ctx, q,
		run.RunID, string(run.Type), string(run.Status), run.CurrentStep,
		marshalJSONMap(run.Input), marshalJSONMap(run.Context),
		run.OwnerID, run.ServerID, run.CardID, run.AwaitingSignal, run.Error, s.tenantID,
	).Scan(&run.Created, &run.Updated); err != nil {
		return fmt.Errorf("create run: %w", err)
	}
	if err := appendWorkflowAudit(ctx, tx, run.RunID, "ril.workflow.run.created", map[string]any{
		"type": string(run.Type), "status": string(run.Status), "current_step": run.CurrentStep,
	}); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("create run commit: %w", err)
	}
	run.ID = run.RunID
	return nil
}

func (s *PgStore) GetRun(runID string) (*Run, error) {
	ctx := s.ctx
	tx, err := s.beginTenant()
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	row := tx.QueryRowContext(ctx,
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
	if s == nil || run == nil || run.TenantID != s.tenantID {
		return fmt.Errorf("workflow: tenant conflict")
	}
	tx, err := s.beginTenant()
	if err != nil {
		return fmt.Errorf("update run transaction: %w", err)
	}
	defer tx.Rollback()
	updated := *run
	if err := s.updateRunTx(s.ctx, tx, &updated); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("update run commit: %w", err)
	}
	run.Updated = updated.Updated
	return nil
}

func (s *PgStore) updateRunTx(ctx context.Context, tx *sql.Tx, run *Run) error {
	var currentStatus, currentSignal, currentError string
	var currentStep int
	err := tx.QueryRowContext(ctx,
		`SELECT status, current_step, awaiting_signal, error FROM ril_workflow_runs WHERE run_id = $1 FOR UPDATE`, run.RunID,
	).Scan(&currentStatus, &currentStep, &currentSignal, &currentError)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: run %s", ErrNotFound, run.RunID)
	}
	if err != nil {
		return fmt.Errorf("update run (load): %w", err)
	}
	if RunStatus(currentStatus) != run.Status {
		if verr := ValidateRunTransition(RunStatus(currentStatus), run.Status); verr != nil {
			return verr
		}
	}
	const q = `UPDATE ril_workflow_runs
		SET status = $1, current_step = $2, context = $3, awaiting_signal = $4,
		    error = $5, started_at = COALESCE(started_at, $6),
		    finished_at = COALESCE(finished_at, $7), updated = now()
		WHERE run_id = $8
		RETURNING updated`
	if err = tx.QueryRowContext(ctx, q,
		string(run.Status), run.CurrentStep, marshalJSONMap(run.Context),
		run.AwaitingSignal, run.Error, run.StartedAt, run.FinishedAt, run.RunID,
	).Scan(&run.Updated); err != nil {
		return fmt.Errorf("update run: %w", err)
	}
	if currentStatus != string(run.Status) || currentStep != run.CurrentStep ||
		currentSignal != run.AwaitingSignal || currentError != run.Error {
		if err := appendWorkflowAudit(ctx, tx, run.RunID, "ril.workflow.run.checkpoint", map[string]any{
			"from_status": currentStatus, "to_status": string(run.Status),
			"from_step": currentStep, "to_step": run.CurrentStep,
			"awaiting_signal": run.AwaitingSignal != "", "has_error": run.Error != "",
		}); err != nil {
			return err
		}
	}
	return nil
}

func (s *PgStore) ListRunsByStatus(status RunStatus, limit int) ([]*Run, error) {
	ctx := s.ctx
	tx, err := s.beginTenant()
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	const q = `SELECT ` + runColumns + ` FROM ril_workflow_runs WHERE status = $1 ORDER BY updated ASC LIMIT $2`
	rows, err := tx.QueryContext(ctx, q, string(status), limitArg(limit))
	if err != nil {
		return nil, fmt.Errorf("list runs by status: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return scanRuns(rows)
}

func (s *PgStore) FindSuspendedBySignal(signalKey string) (*Run, error) {
	ctx := s.ctx
	tx, err := s.beginTenant()
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	row := tx.QueryRowContext(ctx,
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
	ctx := s.ctx
	tx, err := s.beginTenant()
	if err != nil {
		return fmt.Errorf("create step transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	const q = `INSERT INTO ril_workflow_steps
		(tenant_id, id, run_id, step_index, name, status, attempt, input, output, error,
		 idempotency_key, created, updated)
		VALUES ($11,$1,$2,$3,$4,$5,$6,$7,$8,$9,$10, now(), now())`
	if _, err := tx.ExecContext(ctx, q,
		step.ID, step.RunID, step.StepIndex, step.Name, string(step.Status),
		step.Attempt, marshalJSONMap(step.Input), marshalJSONMap(step.Output),
		step.Error, step.IdempotencyKey, s.tenantID,
	); err != nil {
		return fmt.Errorf("create step: %w", err)
	}
	if err := appendWorkflowAudit(ctx, tx, step.RunID, "ril.workflow.step.created", map[string]any{
		"step_index": step.StepIndex, "name": step.Name, "status": string(step.Status),
	}); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("create step commit: %w", err)
	}
	return nil
}

func (s *PgStore) UpdateStep(step *Step) error {
	ctx := s.ctx
	tx, err := s.beginTenant()
	if err != nil {
		return fmt.Errorf("update step transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var currentStatus string
	var currentAttempt int
	err = tx.QueryRowContext(ctx,
		`SELECT status, attempt FROM ril_workflow_steps WHERE run_id = $1 AND step_index = $2 FOR UPDATE`,
		step.RunID, step.StepIndex).Scan(&currentStatus, &currentAttempt)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: step %s/%d", ErrNotFound, step.RunID, step.StepIndex)
	}
	if err != nil {
		return fmt.Errorf("update step (load): %w", err)
	}
	if StepStatus(currentStatus) != step.Status {
		if verr := ValidateStepTransition(StepStatus(currentStatus), step.Status); verr != nil {
			return verr
		}
	}
	const q = `UPDATE ril_workflow_steps
		SET status = $1, attempt = $2, output = $3, error = $4,
		    idempotency_key = CASE WHEN $5 <> '' THEN $5 ELSE idempotency_key END,
		    started_at = COALESCE(started_at, $6),
		    finished_at = COALESCE(finished_at, $7), updated = now()
		WHERE run_id = $8 AND step_index = $9`
	if _, err = tx.ExecContext(ctx, q,
		string(step.Status), step.Attempt, marshalJSONMap(step.Output), step.Error,
		step.IdempotencyKey, step.StartedAt, step.FinishedAt, step.RunID, step.StepIndex,
	); err != nil {
		return fmt.Errorf("update step: %w", err)
	}
	if currentStatus != string(step.Status) || currentAttempt != step.Attempt {
		if err := appendWorkflowAudit(ctx, tx, step.RunID, "ril.workflow.step.checkpoint", map[string]any{
			"step_index": step.StepIndex, "name": step.Name,
			"from_status": currentStatus, "to_status": string(step.Status), "attempt": step.Attempt,
		}); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("update step commit: %w", err)
	}
	return nil
}

func (s *PgStore) GetStep(runID string, idx int) (*Step, error) {
	ctx := s.ctx
	tx, err := s.beginTenant()
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	row := tx.QueryRowContext(ctx,
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
	ctx := s.ctx
	tx, err := s.beginTenant()
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.QueryContext(ctx,
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
	if timer == nil {
		return fmt.Errorf("workflow: timer required")
	}
	if timer.ID == "" {
		timer.ID = uuid.NewString()
	}
	tx, err := s.beginTenant()
	if err != nil {
		return fmt.Errorf("create timer transaction: %w", err)
	}
	defer tx.Rollback()
	if err := s.createTimerTx(s.ctx, tx, timer); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("create timer commit: %w", err)
	}
	return nil
}

func (s *PgStore) createTimerTx(ctx context.Context, tx *sql.Tx, timer *Timer) error {
	const q = `INSERT INTO ril_workflow_timers
		(tenant_id, id, run_id, kind, fire_at, fired, signal_key, created)
		VALUES ($7,$1,$2,$3,$4,$5,$6, now())`
	if _, err := tx.ExecContext(ctx, q,
		timer.ID, timer.RunID, string(timer.Kind), timer.FireAt.UTC(),
		timer.Fired, timer.SignalKey, s.tenantID,
	); err != nil {
		return fmt.Errorf("create timer: %w", err)
	}
	if err := appendWorkflowAudit(ctx, tx, timer.RunID, "ril.workflow.timer.created", map[string]any{
		"timer_id": timer.ID, "kind": string(timer.Kind), "fire_at": timer.FireAt.UTC(),
	}); err != nil {
		return err
	}
	return nil
}

func (s *PgStore) ListDueTimers(now time.Time, limit int) ([]*Timer, error) {
	ctx := s.ctx
	tx, err := s.beginTenant()
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	const q = `SELECT id, run_id, kind, fire_at, fired, signal_key
		FROM ril_workflow_timers WHERE fired = false AND fire_at <= $1 ORDER BY fire_at ASC LIMIT $2`
	rows, err := tx.QueryContext(ctx, q, now.UTC(), limitArg(limit))
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

// GetTimer reads one exact timer through the same tenant-bound transaction.
func (s *PgStore) GetTimer(timerID string) (*Timer, error) {
	tx, err := s.beginTenant()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var timer Timer
	var kind string
	err = tx.QueryRowContext(s.ctx, `SELECT id,run_id,kind,fire_at,fired,signal_key FROM ril_workflow_timers WHERE id=$1`, timerID).Scan(&timer.ID, &timer.RunID, &kind, &timer.FireAt, &timer.Fired, &timer.SignalKey)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get timer: %w", err)
	}
	timer.Kind = TimerKind(kind)
	return &timer, nil
}

func (s *PgStore) MarkTimerFired(timerID string) error {
	ctx := s.ctx
	tx, err := s.beginTenant()
	if err != nil {
		return fmt.Errorf("mark timer transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var runID, kind string
	var fired bool
	if err := tx.QueryRowContext(ctx,
		`SELECT run_id, kind, fired FROM ril_workflow_timers WHERE id = $1 FOR UPDATE`, timerID,
	).Scan(&runID, &kind, &fired); errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	} else if err != nil {
		return fmt.Errorf("mark timer fired (load): %w", err)
	}
	if fired {
		return tx.Commit()
	}
	if _, err := tx.ExecContext(ctx, `UPDATE ril_workflow_timers SET fired = true WHERE id = $1`, timerID); err != nil {
		return fmt.Errorf("mark timer fired: %w", err)
	}
	if err := appendWorkflowAudit(ctx, tx, runID, "ril.workflow.timer.fired", map[string]any{
		"timer_id": timerID, "kind": kind,
	}); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("mark timer fired commit: %w", err)
	}
	return nil
}

// ListAudit returns one run's append-only workflow events from the central
// tenant-scoped audit authority. Callers must first authorize the run itself.
func (s *PgStore) ListAudit(tenantID, runID string, limit int) ([]AuditEvent, error) {
	if tenantID != s.tenantID {
		return nil, fmt.Errorf("workflow: tenant conflict")
	}
	ctx := s.ctx
	tx, err := s.beginTenant()
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	const q = `SELECT id, action, resource_type, resource_id, details_json, created_at
		FROM audit_events
		WHERE tenant_id = $1 AND resource_type = 'ril_workflow_run'
		  AND resource_id = $2 AND action LIKE 'ril.workflow.%'
		ORDER BY id ASC LIMIT $3`
	rows, err := tx.QueryContext(ctx, q, tenantID, runID, limitArg(limit))
	if err != nil {
		return nil, fmt.Errorf("list workflow audit: %w", err)
	}
	defer func() { _ = rows.Close() }()
	events := []AuditEvent{}
	for rows.Next() {
		var event AuditEvent
		var details []byte
		if err := rows.Scan(&event.ID, &event.Action, &event.ResourceType, &event.ResourceID, &details, &event.CreatedAt); err != nil {
			return nil, fmt.Errorf("list workflow audit scan: %w", err)
		}
		event.Details = unmarshalJSONMap(details)
		events = append(events, event)
	}
	return events, rows.Err()
}

func appendWorkflowAudit(ctx context.Context, tx *sql.Tx, runID, action string, details map[string]any) error {
	var id int64
	err := tx.QueryRowContext(ctx, `INSERT INTO audit_events
		(tenant_id, actor_subject_id, action, resource_type, resource_id, details_json)
		SELECT tenant_id,
		       NULLIF(owner_id,''), $2, 'ril_workflow_run', run_id, $3::jsonb
		FROM ril_workflow_runs WHERE run_id = $1 RETURNING id`,
		runID, action, marshalJSONMap(details),
	).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: run %s", ErrNotFound, runID)
	}
	if err != nil {
		return fmt.Errorf("append workflow audit: %w", err)
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
		&run.TenantID, &run.RunID, &typ, &status, &run.CurrentStep, &input, &ctx,
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

// unmarshalJSONMap decodes a jsonb column into a map; empty/null/{} yields nil
// so callers treat it as "no data".
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

// ForTenant returns a request/worker-bound view over the same central pool.
func (s *PgStore) ForTenant(ctx context.Context, tenantID string) (RunStore, error) {
	if ctx == nil || strings.TrimSpace(tenantID) == "" {
		return nil, db.ErrNoTenant
	}
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("workflow: postgres store unavailable")
	}
	return &PgStore{db: s.db, ctx: ctx, tenantID: tenantID}, nil
}
func (s *PgStore) beginTenant() (*sql.Tx, error) {
	if s == nil {
		return nil, db.ErrNoTenant
	}
	return db.BeginTenantTransaction(s.ctx, s.db, s.tenantID)
}

// ListWorkerTenants returns no workflow payload, owner, token or signal material.
func (s *PgStore) ListWorkerTenants(ctx context.Context, after string, limit int) ([]string, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("workflow: postgres store unavailable")
	}
	if limit < 1 || limit > 500 {
		return nil, fmt.Errorf("workflow: bounded tenant limit required")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT tenant_id FROM techstack_list_ril_workflow_tenants($1,$2)`, after, limit)
	if err != nil {
		return nil, fmt.Errorf("workflow worker tenants: %w", err)
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// SuspendRun commits the checkpoint and its optional timer as one tenant-bound
// effect. A failed timer or audit insertion rolls the whole checkpoint back.
func (s *PgStore) SuspendRun(run *Run, timer *Timer) error {
	if s == nil || run == nil || run.TenantID != s.tenantID || run.Status != RunSuspended {
		return fmt.Errorf("workflow: invalid scoped suspension")
	}
	if timer != nil {
		if timer.RunID != run.RunID || timer.SignalKey != run.AwaitingSignal || timer.Fired {
			return fmt.Errorf("workflow: suspension timer custody differs")
		}
		if timer.ID == "" {
			timer.ID = uuid.NewString()
		}
	}
	tx, err := s.beginTenant()
	if err != nil {
		return fmt.Errorf("suspend run transaction: %w", err)
	}
	defer tx.Rollback()
	updated := *run
	if err := s.updateRunTx(s.ctx, tx, &updated); err != nil {
		return err
	}
	if timer != nil {
		if err := s.createTimerTx(s.ctx, tx, timer); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("suspend run commit: %w", err)
	}
	run.Updated = updated.Updated
	return nil
}
