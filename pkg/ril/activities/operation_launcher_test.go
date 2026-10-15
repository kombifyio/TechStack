package activities

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	rilaction "github.com/kombifyio/techstack/pkg/ril/actioncontract"
	"github.com/kombifyio/techstack/pkg/ril/actions"
	"github.com/kombifyio/techstack/pkg/ril/workflow"
)

var verifyDriftPersistentRetry = func(*testing.T, *workflow.Run) {}

// Interface-only DTOs, with no database implementation or node response.
type operationPairAuthority struct {
	stubActionAuthority
	recovery *actions.GovernedCard
}

func (a operationPairAuthority) Get(ctx context.Context, tenant, owner, id string) (*actions.GovernedCard, error) {
	if id == a.recovery.ID {
		return a.recovery, nil
	}
	return a.stubActionAuthority.Get(ctx, tenant, owner, id)
}

type failedOperationEngine struct{ recordingRemediationEngine }

func (*failedOperationEngine) GetRun(context.Context, string) (*workflow.Run, error) {
	return &workflow.Run{Status: workflow.RunFailed, Error: "private SQL/transport detail"}, nil
}

// Pure scope/identity admission, with no database or node execution substitute.
func TestDriftOperationRetryRetainsRunAndRejectsForeignRecovery(t *testing.T) {
	now := time.Now().UTC()
	input := actions.BeginExecution{TenantID: "tenant-scope", OwnerSubjectID: "subject-scope", CardID: "drift-card", ExecutionID: "execution-scope", TraceID: "trace-scope", IdempotencyKey: "retry-scope", Now: now}
	card := &actions.GovernedCard{ID: input.CardID, TenantID: input.TenantID, OwnerSubjectID: input.OwnerSubjectID, ServerID: "server-scope", Status: "approved",
		Template: actions.ActionTemplate{StackID: "stack-scope", Primitive: rilaction.PrimitiveBinding{OperationClass: "drift-correction"}, Target: rilaction.TargetBinding{RuntimeInstanceRef: "agent-scope"}},
		Approval: &actions.ApprovalRecord{ReceiptRef: "approval:drift-card", ActorSubjectID: input.OwnerSubjectID, ValidUntil: now.Add(time.Hour)}}
	recovery := *card
	recovery.ID = "recovery-card"
	recovery.Template.Primitive.OperationClass = "coordinated-rollback"
	first, err := driftOperationRun(input, card, &recovery)
	if err != nil {
		t.Fatal(err)
	}
	card.Status = "executing"
	resumed, err := driftOperationRun(input, card, &recovery)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.RunID != first.RunID || resumed.ServerID != first.ServerID || resumed.Type != workflow.TypeDriftCorrection {
		t.Fatal("retry lost its original drift run and node scope")
	}
	encoded, err := json.Marshal(first.Input)
	if err != nil {
		t.Fatal(err)
	}
	var persisted map[string]any
	if err := json.Unmarshal(encoded, &persisted); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(persisted, resumed.Input) {
		t.Fatal("persisted JSON changes retry identity")
	}
	persisted["operation"] = "drift-correction"
	runnerKey := first.RunID + ":observe"
	admitted, err := PrepareOperationInput(workflow.WithIdempotencyKey(context.Background(), runnerKey), persisted)
	if err != nil || admitted.IdempotencyKey != runnerKey {
		t.Fatalf("runner effect identity denied: %v", err)
	}
	if err := driftOperationOutcome(&workflow.Run{Status: workflow.RunFailed, Error: "private SQL/transport detail"}, card); !errors.Is(err, ErrWorkflowExecutionUnavailable) {
		t.Fatal("failed durable run returned approved card or raw error")
	}
	if err := driftOperationOutcome(&workflow.Run{Status: workflow.RunFailed, Error: "approval_required"}, card); !errors.Is(err, actions.ErrApprovalRequired) {
		t.Fatal("stable authority failure lost")
	}
	launcher := RemediationLauncher{Engine: &failedOperationEngine{}, Authority: operationPairAuthority{stubActionAuthority: stubActionAuthority{card: card}, recovery: &recovery}}
	if returned, err := launcher.ExecuteOperation(context.Background(), input, recovery.ID); returned != nil || !errors.Is(err, ErrWorkflowExecutionUnavailable) {
		t.Fatal("ordinary launcher returned approved card after durable failure")
	}
	verifyDriftPersistentRetry(t, first)
	recovery.ServerID = "foreign-server"
	if _, err := driftOperationRun(input, card, &recovery); !errors.Is(err, ErrOperationTargets) {
		t.Fatalf("foreign recovery admitted: %v", err)
	}
}
