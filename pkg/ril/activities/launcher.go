package activities

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	rilaction "github.com/kombifyio/techstack/pkg/ril/actioncontract"
	"github.com/kombifyio/techstack/pkg/ril/actions"
	"github.com/kombifyio/techstack/pkg/ril/workflow"
	"github.com/kombifyio/techstack/pkg/ril/workflows"
)

var ErrWorkflowExecutionUnavailable = errors.New("governed action workflow execution unavailable")

type remediationEngine interface {
	StartOrResumeRun(context.Context, *workflow.Run) (string, error)
	GetRun(context.Context, string) (*workflow.Run, error)
}

// RemediationLauncher is the HTTP-facing adapter for one complete durable
// action-card execution. The deterministic run identity makes retries adopt
// the existing run instead of creating parallel work.
type RemediationLauncher struct {
	Engine    remediationEngine
	Authority actions.Authority
}

func (l *RemediationLauncher) Execute(ctx context.Context, input actions.BeginExecution) (*actions.GovernedCard, error) {
	if l == nil || l.Engine == nil || l.Authority == nil {
		return nil, ErrWorkflowExecutionUnavailable
	}
	card, err := l.Authority.Get(ctx, input.TenantID, input.OwnerSubjectID, input.CardID)
	if err != nil {
		return nil, err
	}
	if card.Status == string(actions.StatusCompleted) || card.Status == string(actions.StatusFailed) {
		if card.IdempotencyKey == input.IdempotencyKey && card.ExecutionID == input.ExecutionID && card.TraceID == input.TraceID {
			return card, nil
		}
		return nil, actions.ErrCardConflict
	}
	isVerify := card.Template.Primitive.ID == "verify-stackkit-state" && card.Template.Primitive.OperationClass == "product-verify" && card.Template.Target.Scope == rilaction.TargetScopeStack
	canonicalVerify := card.Status == "awaiting_approval" && isVerify
	if !canonicalVerify && card.Status != string(actions.StatusApproved) && card.Status != "executing" && card.Status != "verifying" {
		return nil, actions.ErrApprovalRequired
	}
	if (card.Status == "executing" || card.Status == "verifying") &&
		(card.IdempotencyKey != input.IdempotencyKey || card.ExecutionID != input.ExecutionID || card.TraceID != input.TraceID) {
		return nil, actions.ErrCardConflict
	}
	// An unanswered Verify must not create a terminal workflow. Admit the
	// real Gateway decision first; Begin commits the exact lease and fenced
	// execution together. A lost workflow-start response can then safely
	// resume that same admitted execution without minting another lease.
	if canonicalVerify || card.Status == "executing" || card.Status == "verifying" {
		begun, beginErr := l.Authority.Begin(ctx, input)
		if beginErr != nil {
			return nil, beginErr
		}
		if begun.Card == nil || begun.Card.IdempotencyKey != input.IdempotencyKey || begun.Card.ExecutionID != input.ExecutionID || begun.Card.TraceID != input.TraceID {
			return nil, actions.ErrCardConflict
		}
		if begun.Disposition == actions.BeginReplay {
			return begun.Card, nil
		}
		if begun.Disposition != actions.BeginAcquired || (begun.Card.Status != "executing" && begun.Card.Status != "verifying") {
			return nil, actions.ErrCardConflict
		}
		card = begun.Card
		// A changed current template cannot downgrade an already admitted
		// Verify to the legacy workflow identity.
		if begun.Request.Primitive.ID == "verify-stackkit-state" {
			isVerify = true
		}
	}
	if card.Status == string(actions.StatusApproved) {
		if err := actions.RefuseUnentitledStart(card, input); err != nil {
			return nil, err
		}
	}

	ctx = workflow.WithTenant(ctx, input.TenantID)
	runInput := map[string]any{
		workflows.InputTenantID:       input.TenantID,
		workflows.InputOwnerSubjectID: input.OwnerSubjectID,
		workflows.InputCardID:         input.CardID,
		workflows.InputExecutionID:    input.ExecutionID,
		workflows.InputTraceID:        input.TraceID,
		workflows.InputIdempotencyKey: input.IdempotencyKey,
	}
	if input.ConnectorProjection != nil {
		projection, mapErr := objectMap(input.ConnectorProjection)
		if mapErr != nil {
			return nil, mapErr
		}
		runInput[workflows.InputConnectorProjection] = projection
	}
	identityInput := runInput
	if isVerify {
		// Connector projection stays in the immutable workflow input for exact
		// conflict checks, but cannot select a second run for this execution.
		identityInput = map[string]any{
			workflows.InputTenantID: input.TenantID, workflows.InputOwnerSubjectID: input.OwnerSubjectID,
			workflows.InputCardID: input.CardID, workflows.InputExecutionID: input.ExecutionID,
			workflows.InputTraceID: input.TraceID, workflows.InputIdempotencyKey: input.IdempotencyKey,
		}
	}
	runID, err := remediationRunID(identityInput)
	if err != nil {
		return nil, err
	}
	_, err = l.Engine.StartOrResumeRun(ctx, &workflow.Run{
		TenantID: input.TenantID, RunID: runID, Type: workflow.TypeActionCardRemediation,
		OwnerID: input.OwnerSubjectID, ServerID: card.ServerID, CardID: input.CardID,
		Input: runInput,
	})
	if err != nil {
		return nil, err
	}
	card, err = l.Authority.Get(ctx, input.TenantID, input.OwnerSubjectID, input.CardID)
	if err != nil {
		return nil, err
	}
	run, runErr := l.Engine.GetRun(ctx, runID)
	if runErr != nil {
		return nil, runErr
	}
	if run.Status == workflow.RunFailed {
		if card.Status == string(actions.StatusFailed) && card.Evidence != nil {
			return card, nil
		}
		if reason, _ := run.Context["failure_reason"].(string); reason != "" {
			return nil, failureFromReason(reason)
		}
		return nil, fmt.Errorf("%w: %s", ErrWorkflowExecutionUnavailable, run.RunID)
	}
	return card, nil
}

func remediationRunID(input map[string]any) (string, error) {
	canonical, err := json.Marshal(input)
	if err != nil {
		return "", fmt.Errorf("encode governed action workflow identity: %w", err)
	}
	digest := sha256.Sum256(canonical)
	return "action-remediation-" + hex.EncodeToString(digest[:]), nil
}

func failureFromReason(reason string) error {
	switch reason {
	case "card_not_found":
		return actions.ErrCardNotFound
	case "approval_required":
		return actions.ErrApprovalRequired
	case "grant_required":
		return actions.ErrGrantRequired
	case "connector_binding_required":
		return actions.ErrConnectorBindingRequired
	case "connector_grant_insufficient":
		return actions.ErrConnectorGrantInsufficient
	case "execution_admission_rejected":
		return actions.ErrExecutionAdmission
	case "execution_in_progress":
		return actions.ErrExecutionInProgress
	default:
		return actions.ErrCardConflict
	}
}
