package workflow

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// newPgTestStore stands up a Postgres-backed RunStore against the integration
// database named by TECHSTACK_TEST_POSTGRES_URL (skipped when unset, mirroring
// pkg/db/tenant_integration_test.go). It applies the idempotent workflow schema
// (migration 008) and truncates the workflow tables for per-test isolation.
//
// These tests cover the *active runtime store* (PgStore is what
// cmd/techstack/workflow_boot.go wires the engine on); the PocketBase-backed
// *Store in store.go is dead code retained only for the legacy store tests.
func newPgTestStore(t *testing.T) *PgStore {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("TECHSTACK_TEST_POSTGRES_URL"))
	if dsn == "" {
		t.Skip("TECHSTACK_TEST_POSTGRES_URL not set; skipping Postgres integration test")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if pingErr := db.Ping(); pingErr != nil {
		t.Fatalf("ping: %v", pingErr)
	}
	schemaPath := filepath.Join("..", "..", "db", "migrations", "008_ril_workflows.sql")
	schema, err := os.ReadFile(schemaPath)
	if err != nil {
		t.Fatalf("read %s: %v", schemaPath, err)
	}
	if _, execErr := db.Exec(string(schema)); execErr != nil {
		t.Fatalf("apply workflow schema: %v", execErr)
	}
	if _, truncErr := db.Exec("TRUNCATE ril_workflow_timers, ril_workflow_steps, ril_workflow_runs CASCADE"); truncErr != nil {
		t.Fatalf("truncate workflow tables: %v", truncErr)
	}
	return NewPgStore(db)
}

func TestPgStore_RunRoundTrip(t *testing.T) {
	s := newPgTestStore(t)

	run := &Run{
		Type:     TypeActionCardRemediation,
		OwnerID:  "user-1",
		ServerID: "srv-1",
		CardID:   "card-1",
		Input:    map[string]any{"plan_steps": float64(3)},
	}
	if err := s.CreateRun(run); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if run.RunID == "" || run.ID == "" {
		t.Fatal("CreateRun did not assign RunID/ID")
	}
	if run.Status != RunPending {
		t.Errorf("new run status = %s, want pending", run.Status)
	}

	got, err := s.GetRun(run.RunID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if got.OwnerID != "user-1" || got.ServerID != "srv-1" || got.CardID != "card-1" {
		t.Errorf("round-trip mismatch: %+v", got)
	}
	if got.Type != TypeActionCardRemediation {
		t.Errorf("type = %s", got.Type)
	}
	if got.Input["plan_steps"] != float64(3) {
		t.Errorf("input round-trip failed: %+v", got.Input)
	}

	if _, err := s.GetRun("does-not-exist"); err == nil {
		t.Error("GetRun(missing) expected error")
	}
}

func TestPgStore_UpdateRun_TransitionGuard(t *testing.T) {
	s := newPgTestStore(t)
	run := &Run{Type: TypeDriftCorrection, OwnerID: "u"}
	if err := s.CreateRun(run); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	// pending -> running: allowed
	run.Status = RunRunning
	now := time.Now().UTC()
	run.StartedAt = &now
	run.CurrentStep = 1
	if err := s.UpdateRun(run); err != nil {
		t.Fatalf("valid transition rejected: %v", err)
	}

	// running -> completed: allowed
	run.Status = RunCompleted
	if err := s.UpdateRun(run); err != nil {
		t.Fatalf("running->completed rejected: %v", err)
	}

	// completed -> running: rejected (terminal)
	run.Status = RunRunning
	if err := s.UpdateRun(run); err == nil {
		t.Error("expected completed->running to be rejected")
	}

	got, _ := s.GetRun(run.RunID)
	if got.Status != RunCompleted {
		t.Errorf("persisted status = %s, want completed", got.Status)
	}
	if got.CurrentStep != 1 {
		t.Errorf("current_step = %d, want 1", got.CurrentStep)
	}
}

func TestPgStore_ListRunsByStatus(t *testing.T) {
	s := newPgTestStore(t)
	for range 3 {
		if err := s.CreateRun(&Run{Type: TypeRollingUpdate, OwnerID: "u"}); err != nil {
			t.Fatalf("CreateRun: %v", err)
		}
	}
	pending, err := s.ListRunsByStatus(RunPending, 0)
	if err != nil {
		t.Fatalf("ListRunsByStatus: %v", err)
	}
	if len(pending) != 3 {
		t.Errorf("pending runs = %d, want 3", len(pending))
	}
	running, err := s.ListRunsByStatus(RunRunning, 0)
	if err != nil {
		t.Fatalf("ListRunsByStatus running: %v", err)
	}
	if len(running) != 0 {
		t.Errorf("running runs = %d, want 0", len(running))
	}
}

func TestPgStore_StepRoundTripAndTransition(t *testing.T) {
	s := newPgTestStore(t)
	run := &Run{Type: TypeActionCardRemediation, OwnerID: "u"}
	if err := s.CreateRun(run); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	step := &Step{
		RunID:          run.RunID,
		StepIndex:      0,
		Name:           "dispatch_agent_command",
		IdempotencyKey: "idem-0",
		Input:          map[string]any{"cmd": "systemctl restart nginx"},
	}
	if err := s.CreateStep(step); err != nil {
		t.Fatalf("CreateStep: %v", err)
	}
	if step.Status != StepPending {
		t.Errorf("new step status = %s, want pending", step.Status)
	}

	step.Status = StepRunning
	if err := s.UpdateStep(step); err != nil {
		t.Fatalf("pending->running: %v", err)
	}
	step.Status = StepCompleted
	step.Output = map[string]any{"exit_code": float64(0)}
	if err := s.UpdateStep(step); err != nil {
		t.Fatalf("running->completed: %v", err)
	}

	steps, err := s.ListSteps(run.RunID)
	if err != nil {
		t.Fatalf("ListSteps: %v", err)
	}
	if len(steps) != 1 {
		t.Fatalf("steps = %d, want 1", len(steps))
	}
	if steps[0].Status != StepCompleted || steps[0].Output["exit_code"] != float64(0) {
		t.Errorf("step round-trip failed: %+v", steps[0])
	}

	// invalid: completed -> running
	step.Status = StepRunning
	if err := s.UpdateStep(step); err == nil {
		t.Error("expected completed->running step transition rejected")
	}
}

func TestPgStore_DueTimers(t *testing.T) {
	s := newPgTestStore(t)
	run := &Run{Type: TypeActionCardRemediation, OwnerID: "u"}
	if err := s.CreateRun(run); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	past := &Timer{RunID: run.RunID, Kind: TimerEscalation, FireAt: time.Now().Add(-time.Minute).UTC(), SignalKey: "esc-1"}
	future := &Timer{RunID: run.RunID, Kind: TimerReminder, FireAt: time.Now().Add(time.Hour).UTC(), SignalKey: "rem-1"}
	if err := s.CreateTimer(past); err != nil {
		t.Fatalf("CreateTimer past: %v", err)
	}
	if err := s.CreateTimer(future); err != nil {
		t.Fatalf("CreateTimer future: %v", err)
	}

	due, err := s.ListDueTimers(time.Now().UTC(), 0)
	if err != nil {
		t.Fatalf("ListDueTimers: %v", err)
	}
	if len(due) != 1 {
		t.Fatalf("due timers = %d, want 1 (only the past one)", len(due))
	}
	if due[0].SignalKey != "esc-1" {
		t.Errorf("due timer signal = %s, want esc-1", due[0].SignalKey)
	}

	// After firing, it's no longer due.
	if err := s.MarkTimerFired(due[0].ID); err != nil {
		t.Fatalf("MarkTimerFired: %v", err)
	}
	due2, _ := s.ListDueTimers(time.Now().UTC(), 0)
	if len(due2) != 0 {
		t.Errorf("due timers after fire = %d, want 0", len(due2))
	}
}

func TestPgStore_FindSuspendedBySignal(t *testing.T) {
	s := newPgTestStore(t)
	run := &Run{Type: TypeActionCardRemediation, OwnerID: "u"}
	if err := s.CreateRun(run); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	// pending -> running -> suspended, awaiting a signal
	run.Status = RunRunning
	if err := s.UpdateRun(run); err != nil {
		t.Fatalf("->running: %v", err)
	}
	run.Status = RunSuspended
	run.AwaitingSignal = "approval-42"
	if err := s.UpdateRun(run); err != nil {
		t.Fatalf("->suspended: %v", err)
	}

	got, err := s.FindSuspendedBySignal("approval-42")
	if err != nil {
		t.Fatalf("FindSuspendedBySignal: %v", err)
	}
	if got == nil || got.RunID != run.RunID {
		t.Errorf("FindSuspendedBySignal = %+v, want run %s", got, run.RunID)
	}
}
