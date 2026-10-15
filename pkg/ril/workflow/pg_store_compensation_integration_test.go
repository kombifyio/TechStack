//go:build managed_postgres

package workflow_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/kombifyio/techstack/internal/gocommon/oidcclient"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/db"
	"github.com/kombifyio/techstack/pkg/ril/workflow"
	"github.com/kombifyio/techstack/pkg/ril/workflows"
	"github.com/google/uuid"
)

// Exercises the production workflow/engine/worker against live managed PG.
// Activity responses are controlled at the module boundary: no card approval,
// dispatcher, provider or successful node execution is fabricated or claimed.
func TestManagedPostgresPendingCompensationSurvivesRestart(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	a, err := db.LoadManagedTestAssignment()
	if err != nil {
		t.Fatal(err)
	}
	if err := a.VerifyPin(); err != nil {
		t.Fatal(err)
	}
	v, err := oidcclient.NewVerifier(oidcclient.VerifierConfig{Issuer: "https://login.kombify.io/", Audience: a.AuthAudience, JWKSURL: "https://login.kombify.io/.well-known/jwks.json", ClockSkew: 2 * time.Second, HTTPClient: &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}})
	if err != nil {
		t.Fatal(err)
	}
	claims, err := v.Verify(ctx, os.Getenv("TECHSTACK_MANAGED_TEST_ID_TOKEN"))
	if err != nil || claims.Subject != a.SubjectID || claims.Email != a.Email || claims.Raw["email_verified"] != true || a.TenantID != "usr:"+claims.Subject {
		t.Fatal("actual shared Auth0 identity required")
	}
	cfg, err := db.ConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	app, err := db.OpenContext(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	if err := db.VerifyManagedTestSession(ctx, app.DB, *a, db.ManagedTestApp); err != nil {
		t.Fatal(err)
	}
	if err := app.VerifyMigrations(ctx); err != nil {
		t.Fatal(err)
	}
	m, err := controlplane.NewPostgresStore(app.DB).GetMembership(ctx, a.TenantID, a.SubjectID)
	if err != nil || m == nil || m.Status != "active" || m.SubjectID != a.SubjectID {
		t.Fatal("active shared test membership required")
	}
	pool := workflow.NewPgStore(app.DB)
	store, err := pool.ForTenant(ctx, a.TenantID)
	if err != nil {
		t.Fatal(err)
	}
	runID := "managed-ril-comp-" + uuid.NewString()
	jobID := "module-boundary-recovery-" + uuid.NewString()
	key := runID + ":compensate"
	emit := func(stage string, r *workflow.Run) {
		_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"schema": "techstack.compensation-regression/v1", "stage": stage, "run_id": runID, "status": r.Status, "job_id": jobID, "activity_key": key, "provider_effects": false, "activity_boundary": "controlled responses; no node acceptance"})
	}
	defer func() {
		clean, stop := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
		defer stop()
		s, err := pool.ForTenant(clean, a.TenantID)
		if err != nil {
			t.Error(err)
			return
		}
		r, err := s.GetRun(runID)
		if errors.Is(err, workflow.ErrNotFound) {
			return
		}
		if err != nil || r.TenantID != a.TenantID || r.OwnerID != a.SubjectID {
			t.Error("owned cleanup custody unverified")
			return
		}
		if !workflow.IsTerminal(r.Status) {
			// Compensation has no cancellation edge; resolve only this isolated
			// module probe as failed, without dispatching any external activity.
			r.Status = workflow.RunFailed
			now := time.Now().UTC()
			r.FinishedAt = &now
			if err := s.UpdateRun(r); err != nil {
				t.Error(err)
				return
			}
		}
		r, err = s.GetRun(runID)
		if err != nil || !workflow.IsTerminal(r.Status) {
			t.Error("owned terminal cleanup unverified")
			return
		}
		emit("terminal-cleanup", r)
	}()
	run := &workflow.Run{RunID: runID, TenantID: a.TenantID, OwnerID: a.SubjectID, Type: workflow.TypeRollingUpdate, Input: map[string]any{"tenant_id": a.TenantID, "authorization_ref": "approval:module-boundary", "operation_ref": "action:module-boundary", "compensation_ref": "action:module-boundary-recovery", "targets": []string{"module-boundary"}}, Context: map[string]any{"operation_result": map[string]any{"job_id": "module-boundary-original"}}, CurrentStep: 1}
	if err := store.CreateRun(run); err != nil {
		t.Fatal(err)
	}
	run.Status = workflow.RunRunning
	if err := store.UpdateRun(run); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateStep(&workflow.Step{RunID: runID, StepIndex: 0, Name: "dispatch", Status: workflow.StepCompleted, IdempotencyKey: runID + ":0"}); err != nil {
		t.Fatal(err)
	}
	completed := false
	transportFailure := false
	called := false
	newEngine := func() *workflow.Engine {
		acts := workflow.NewActivityRunner()
		acts.Register(workflows.ActDispatchGovernedOperation, func(context.Context, map[string]any) (map[string]any, error) {
			t.Error("forward dispatch replayed")
			return nil, errors.New("forward replay")
		})
		acts.Register(workflows.ActVerifyGovernedOperation, func(context.Context, map[string]any) (map[string]any, error) {
			return nil, errors.New("module-boundary forward failure")
		})
		acts.Register(workflows.ActExecuteGovernedCompensation, func(c context.Context, input map[string]any) (map[string]any, error) {
			if workflow.IdempotencyKeyFromContext(c) != key {
				t.Fatal("recovery idempotency changed")
			}
			if called {
				checkpoint, _ := input["compensation_result"].(map[string]any)
				if checkpoint["job_id"] != jobID {
					t.Fatal("recovery job checkpoint lost on restart")
				}
			}
			called = true
			if transportFailure {
				return nil, errors.New("module-boundary transport failure")
			}
			return map[string]any{"pending": !completed, "compensated": completed, "job_id": jobID}, nil
		})
		e := workflow.NewEngine(compensationProbeStore{RunStore: pool, pool: pool, tenant: a.TenantID, runID: runID}, acts)
		e.Register(compensationProbeDefinition{workflows.NewRollingUpdateWorkflow()})
		return e
	}
	scoped := workflow.WithTenant(ctx, a.TenantID)
	if err := newEngine().Advance(scoped, runID); err != nil {
		t.Fatal(err)
	}
	assertPending := func(stage string) {
		r, err := store.GetRun(runID)
		if err != nil {
			t.Fatal(err)
		}
		emit(stage, r)
		checkpoint, _ := r.Context["compensation_result"].(map[string]any)
		st, err := store.GetStep(runID, 0)
		if err != nil || r.Status != workflow.RunCompensating || r.FinishedAt != nil || st.Status != workflow.StepCompleted || checkpoint["job_id"] != jobID {
			t.Fatal("pending recovery was lost or prematurely finalized")
		}
	}
	assertPending("pending-checkpoint")
	transportFailure = true
	workflow.NewWorker(newEngine(), workflow.WorkerConfig{}).PollRuns(ctx)
	assertPending("transport-error-restart")
	transportFailure = false
	workflow.NewWorker(newEngine(), workflow.WorkerConfig{}).PollRuns(ctx)
	assertPending("pending-restart")
	completed = true
	workflow.NewWorker(newEngine(), workflow.WorkerConfig{}).PollRuns(ctx)
	r, err := store.GetRun(runID)
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.GetStep(runID, 0)
	if err != nil || r.Status != workflow.RunFailed || r.FinishedAt == nil || st.Status != workflow.StepCompensated || r.Error != "module-boundary forward failure" {
		t.Fatal("confirmed recovery did not finalize the original failed run")
	}
	emit("confirmed-recovery", r)
	called = false
	workflow.NewWorker(newEngine(), workflow.WorkerConfig{}).PollRuns(ctx)
	if called {
		t.Fatal("terminal recovery replayed")
	}
}

type compensationProbeDefinition struct{ workflow.WorkflowDefinition }

func (compensationProbeDefinition) RetryPolicy() workflow.RetryPolicy {
	return workflow.RetryPolicy{MaxAttempts: 1}
}

// Isolation filters actual reads to the owned probe. All mutations and scope
// admission delegate to the real PgStore; no other customer's run is advanced.
type compensationProbeStore struct {
	workflow.RunStore
	pool          *workflow.PgStore
	tenant, runID string
}

func (s compensationProbeStore) ForTenant(ctx context.Context, tenant string) (workflow.RunStore, error) {
	actual, err := s.pool.ForTenant(ctx, tenant)
	if err != nil {
		return nil, err
	}
	s.RunStore = actual
	return s, nil
}
func (s compensationProbeStore) ListWorkerTenants(ctx context.Context, after string, limit int) ([]string, error) {
	actual, err := s.pool.ListWorkerTenants(ctx, after, limit)
	if err != nil {
		return nil, err
	}
	for _, tenant := range actual {
		if tenant == s.tenant {
			return []string{tenant}, nil
		}
	}
	return nil, nil
}
func (s compensationProbeStore) ListRunsByStatus(status workflow.RunStatus, _ int) ([]*workflow.Run, error) {
	r, err := s.RunStore.GetRun(s.runID)
	if err != nil {
		return nil, err
	}
	if r.Status == status {
		return []*workflow.Run{r}, nil
	}
	return nil, nil
}
