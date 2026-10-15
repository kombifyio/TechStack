package activities

import (
	"context"
	"strings"

	"github.com/kombifyio/techstack/pkg/ril/actions"
	"github.com/kombifyio/techstack/pkg/ril/workflow"
	"github.com/kombifyio/techstack/pkg/ril/workflows"
)

// ExecuteOperation connects the ordinary action-card surface to the registered
// drift workflow. Product activities still resolve fresh grants, node binding,
// entitlement and the separately approved recovery before dispatch.
func (l *RemediationLauncher) ExecuteOperation(ctx context.Context, input actions.BeginExecution, compensationID string) (*actions.GovernedCard, error) {
	if l == nil || l.Engine == nil || l.Authority == nil {
		return nil, ErrWorkflowExecutionUnavailable
	}
	card, err := l.Authority.Get(ctx, input.TenantID, input.OwnerSubjectID, input.CardID)
	if err != nil {
		return nil, err
	}
	recovery, err := l.Authority.Get(ctx, input.TenantID, input.OwnerSubjectID, compensationID)
	if err != nil {
		return nil, err
	}
	run, err := driftOperationRun(input, card, recovery)
	if err != nil {
		return nil, err
	}
	ctx = workflow.WithTenant(ctx, input.TenantID)
	if card.Status == "completed" || card.Status == "failed" {
		// Terminal cards can only read their existing exact run. A new caller
		// key or recovery reference must not start another workflow for them.
		if existing, err := l.Engine.GetRun(ctx, run.RunID); err != nil || existing == nil || existing.RunID != run.RunID {
			return nil, actions.ErrCardConflict
		}
	}
	if _, err := l.Engine.StartOrResumeRun(ctx, run); err != nil {
		return nil, ErrWorkflowExecutionUnavailable
	}
	stored, err := l.Engine.GetRun(ctx, run.RunID)
	if err != nil {
		return nil, ErrWorkflowExecutionUnavailable
	}
	card, err = l.Authority.Get(ctx, input.TenantID, input.OwnerSubjectID, input.CardID)
	if err != nil {
		return nil, err
	}
	if err := driftOperationOutcome(stored, card); err != nil {
		return nil, err
	}
	return card, nil
}

func driftOperationOutcome(run *workflow.Run, card *actions.GovernedCard) error {
	if run == nil || card == nil {
		return ErrWorkflowExecutionUnavailable
	}
	if run.Status == workflow.RunFailed {
		if card.Status == "failed" && card.Evidence != nil {
			return nil
		}
		// Run.Error may contain transport or SQL text. Only stable authority
		// reasons are exposed; every other failure uses unavailable guidance.
		for _, reason := range []string{"card_not_found", "approval_required", "grant_required", "connector_binding_required", "connector_grant_insufficient", "execution_admission_rejected", "execution_in_progress"} {
			if run.Error == reason || run.Context["failure_reason"] == reason {
				return failureFromReason(reason)
			}
		}
		return ErrWorkflowExecutionUnavailable
	}
	if run.Status == workflow.RunCancelled || (run.Status == workflow.RunCompleted && (card.Status != "completed" || card.Evidence == nil)) {
		return ErrWorkflowExecutionUnavailable
	}
	return nil
}

func driftOperationRun(input actions.BeginExecution, card, recovery *actions.GovernedCard) (*workflow.Run, error) {
	if card == nil || recovery == nil || card.ID != input.CardID || card.ID == recovery.ID ||
		card.TenantID != input.TenantID || recovery.TenantID != input.TenantID ||
		card.OwnerSubjectID != input.OwnerSubjectID || recovery.OwnerSubjectID != input.OwnerSubjectID ||
		card.ServerID == "" || card.ServerID != recovery.ServerID || card.Template.StackID == "" ||
		card.Template.StackID != recovery.Template.StackID || card.Template.Target != recovery.Template.Target ||
		card.Template.Target.RuntimeInstanceRef == "" ||
		card.Template.Primitive.OperationClass != "drift-correction" || recovery.Template.Primitive.OperationClass != "coordinated-rollback" {
		return nil, ErrOperationTargets
	}
	terminal := card.Status == "completed" || card.Status == "failed"
	if card.Approval == nil || recovery.Approval == nil || card.Approval.ActorSubjectID != input.OwnerSubjectID ||
		recovery.Approval.ActorSubjectID != input.OwnerSubjectID || card.Approval.ReceiptRef != "approval:"+card.ID ||
		(!terminal && (!input.Now.Before(card.Approval.ValidUntil) || !input.Now.Before(recovery.Approval.ValidUntil))) ||
		(recovery.Status != "approved" && recovery.Status != "executing" && recovery.Status != "verifying" && recovery.Status != "completed") {
		return nil, actions.ErrApprovalRequired
	}
	if strings.TrimSpace(input.ExecutionID) == "" || strings.TrimSpace(input.TraceID) == "" || strings.TrimSpace(input.IdempotencyKey) == "" {
		return nil, ErrOperationIdentity
	}
	if card.Status != "approved" && card.Status != "executing" && card.Status != "verifying" && card.Status != "completed" && card.Status != "failed" {
		return nil, actions.ErrApprovalRequired
	}
	data := map[string]any{workflows.InputTenantID: input.TenantID, workflows.InputOwnerSubjectID: input.OwnerSubjectID,
		workflows.InputAuthorizationRef: card.Approval.ReceiptRef, workflows.InputOperationRef: "action:" + card.ID,
		workflows.InputCompensationRef: "action:" + recovery.ID, workflows.InputTargets: []any{card.ServerID},
		workflows.InputExecutionID: input.ExecutionID, workflows.InputTraceID: input.TraceID, workflows.InputIdempotencyKey: input.IdempotencyKey}
	id, err := remediationRunID(data)
	if err != nil {
		return nil, err
	}
	// The caller key binds the run identity, while each effect is admitted
	// with the existing ActivityRunner's distinct step key.
	delete(data, workflows.InputIdempotencyKey)
	return &workflow.Run{RunID: "drift-" + id, TenantID: input.TenantID, OwnerID: input.OwnerSubjectID, ServerID: card.ServerID, CardID: card.ID, Type: workflow.TypeDriftCorrection, Input: data}, nil
}
