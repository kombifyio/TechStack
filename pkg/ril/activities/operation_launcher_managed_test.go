//go:build managed_postgres

package activities

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/db"
	rilaction "github.com/kombifyio/techstack/pkg/ril/actioncontract"
	"github.com/kombifyio/techstack/pkg/ril/actions"
	"github.com/kombifyio/techstack/pkg/ril/workflow"
	"github.com/kombifyio/techstack/pkg/ril/workflows"
	"github.com/google/uuid"
)

func init() {
	verifyDriftPersistentRetry = verifyManagedDriftPersistentRetry
}

// A persistence-boundary probe on the assigned actual database. It creates a
// scope-only owned run, cancels it, and retries through the real Engine/PgStore;
// it does not create a server, card approval, job, activity response or node.
func verifyManagedDriftPersistentRetry(t *testing.T, candidate *workflow.Run) {
	if os.Getenv("TECHSTACK_DRIFT_LAUNCHER_MANAGED_PROBE") != "true" {
		t.Fatal("managed drift persistence probe requires explicit assignment")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	a, err := db.LoadManagedTestAssignment()
	if err != nil {
		t.Fatal(err)
	}
	if err := a.VerifyPin(); err != nil {
		t.Fatal(err)
	}
	cfg, err := db.ConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	app, err := db.OpenContext(ctx, cfg)
	if err != nil {
		t.Fatal("assigned app database connection unavailable")
	}
	defer app.Close()
	if err := db.VerifyManagedTestSession(ctx, app.DB, *a, db.ManagedTestApp); err != nil {
		t.Fatal(err)
	}
	m, err := controlplane.NewPostgresStore(app.DB).GetMembership(ctx, a.TenantID, a.SubjectID)
	if err != nil || m == nil || m.Status != "active" {
		t.Fatal("assigned shared membership unavailable")
	}
	now := time.Now().UTC()
	input := actions.BeginExecution{TenantID: a.TenantID, OwnerSubjectID: a.SubjectID, CardID: candidate.CardID, ExecutionID: "scope-execution", TraceID: "scope-trace", IdempotencyKey: "managed-retry-" + uuid.NewString(), Now: now}
	card := &actions.GovernedCard{ID: input.CardID, TenantID: a.TenantID, OwnerSubjectID: a.SubjectID, ServerID: candidate.ServerID, Status: "approved",
		Template: actions.ActionTemplate{StackID: "scope-only-stack", Primitive: rilaction.PrimitiveBinding{OperationClass: "drift-correction"}, Target: rilaction.TargetBinding{RuntimeInstanceRef: "scope-only-agent"}},
		Approval: &actions.ApprovalRecord{ReceiptRef: "approval:" + input.CardID, ActorSubjectID: a.SubjectID, ValidUntil: now.Add(time.Hour)}}
	recovery := *card
	recovery.ID = "scope-only-recovery"
	recovery.Template.Primitive.OperationClass = "coordinated-rollback"
	planned, err := driftOperationRun(input, card, &recovery)
	if err != nil {
		t.Fatal(err)
	}
	run := *planned
	pool := workflow.NewPgStore(app.DB)
	store, err := pool.ForTenant(ctx, a.TenantID)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateRun(&run); err != nil {
		t.Fatal(err)
	}
	defer func() {
		r, e := store.GetRun(run.RunID)
		if e != nil {
			t.Error(e)
			return
		}
		if !workflow.IsTerminal(r.Status) {
			r.Status = workflow.RunCancelled
			now := time.Now().UTC()
			r.FinishedAt = &now
			if e := store.UpdateRun(r); e != nil {
				t.Error(e)
			}
		}
	}()
	stored, err := store.GetRun(run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	stored.Status = workflow.RunCancelled
	now = time.Now().UTC()
	stored.FinishedAt = &now
	if err := store.UpdateRun(stored); err != nil {
		t.Fatal(err)
	}
	fresh := workflow.NewEngine(pool, workflow.NewActivityRunner())
	fresh.Register(workflows.NewDriftCorrectionWorkflow())
	id, err := fresh.StartOrResumeRun(workflow.WithTenant(ctx, a.TenantID), &run)
	if err != nil || id != run.RunID {
		t.Fatalf("actual persisted JSON retry rejected: %v", err)
	}
	read, err := fresh.GetRun(workflow.WithTenant(ctx, a.TenantID), id)
	if err != nil || read.Status != workflow.RunCancelled {
		t.Fatal("terminal run identity/state changed")
	}
	if err := driftOperationOutcome(read, &actions.GovernedCard{Status: "approved"}); err == nil {
		t.Fatal("cancelled durable run reported success")
	}
	// Authority is a pure scope DTO; Engine/PgStore below are the real live
	// database. Neither terminal call can run any activity or node command.
	card.Status = "failed"
	launcher := RemediationLauncher{Engine: fresh, Authority: operationPairAuthority{stubActionAuthority: stubActionAuthority{card: card}, recovery: &recovery}}
	if returned, err := launcher.ExecuteOperation(ctx, input, recovery.ID); returned != nil || !errors.Is(err, ErrWorkflowExecutionUnavailable) {
		t.Fatal("actual terminal readback reported approved success")
	}
	input.IdempotencyKey += "-conflict"
	if returned, err := launcher.ExecuteOperation(ctx, input, recovery.ID); returned != nil || !errors.Is(err, actions.ErrCardConflict) {
		t.Fatal("terminal card started a new conflict run")
	}
	json.NewEncoder(os.Stdout).Encode(map[string]any{"schema": "techstack.drift-launcher-persistence/v1", "run_id": id, "tenant_id": a.TenantID, "database_identity": a.Database + "@" + a.SystemIdentifier, "status": read.Status, "serialization_retry": "verified", "node_or_queue_effect": false})
}
