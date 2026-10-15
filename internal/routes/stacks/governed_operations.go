package stacks

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/jobs"
	"github.com/kombifyio/techstack/pkg/orchestrator"
	"github.com/kombifyio/techstack/pkg/ril/actioncontract"
	"github.com/kombifyio/techstack/pkg/ril/actions"
	"github.com/kombifyio/techstack/pkg/ril/activities"
	"github.com/kombifyio/techstack/pkg/ril/workflows"
)

var ErrGovernedOperationPending = errors.New("governed node operation has no terminal result")
var ErrGovernedNodeFailed = errors.New("governed node operation failed")

// GovernedOperationBackend is an adapter over the existing card authority,
// canonical stores, route guardrails and StackKits lifecycle dispatcher.
// One card admits one exact server; fleet orchestration cannot widen it.
type GovernedOperationBackend struct {
	Authority actions.Authority
	Store     *controlplane.PostgresStore
	Orch      *orchestrator.Orchestrator
}

func (b *GovernedOperationBackend) RunOperationActivity(ctx context.Context, activity string, input activities.OperationInput, state map[string]any) (map[string]any, error) {
	if b == nil || b.Authority == nil || b.Store == nil || b.Orch == nil {
		return nil, orchestrator.ErrStackKitLifecycleUnavailable
	}
	card, err := b.card(ctx, input, input.CardID, false)
	if err != nil {
		return nil, err
	}
	switch activity {
	case workflows.ActQueryMonitoringSignal:
		if _, err := b.prepare(ctx, input, card, false); err != nil {
			return nil, err
		}
		stack, err := b.Store.GetStack(ctx, input.TenantID, card.Template.StackID)
		if err != nil || stack == nil || stack.OwnerSubjectID != input.OwnerSubjectID {
			return nil, controlplane.ErrNotFound
		}
		return map[string]any{"active": stack.DriftStatus == "drifted"}, nil
	case workflows.ActDispatchGovernedOperation:
		// Resolve the separately approved recovery before consuming the primary
		// approval. Its scope must be the same exact stack, server and worker.
		compensation, err := b.card(ctx, input, input.CompensationID, true)
		if err != nil || !sameOperationTarget(card, compensation) {
			return nil, actions.ErrApprovalRequired
		}
		return b.dispatch(ctx, input, card)
	case workflows.ActVerifyGovernedOperation:
		return b.verify(ctx, input, card, state)
	case workflows.ActRecordGovernedOperationAudit:
		return b.audit(ctx, input, card, state)
	case workflows.ActExecuteGovernedCompensation:
		return b.compensate(ctx, input, card, state)
	default:
		return nil, activities.ErrOperationReference
	}
}

func (b *GovernedOperationBackend) card(ctx context.Context, input activities.OperationInput, cardID string, compensation bool) (*actions.GovernedCard, error) {
	card, err := b.Authority.Get(ctx, input.TenantID, input.OwnerSubjectID, cardID)
	if err != nil {
		return nil, err
	}
	if card == nil || card.ID != cardID || card.TenantID != input.TenantID || card.OwnerSubjectID != input.OwnerSubjectID ||
		len(input.Targets) != 1 || input.Targets[0] != card.ServerID || card.Template.StackID == "" || card.Template.Target.RuntimeInstanceRef == "" {
		return nil, activities.ErrOperationTargets
	}
	class := input.Operation
	if compensation {
		class = "coordinated-rollback"
	}
	if card.Template.Primitive.OperationClass != class || card.Approval == nil || card.Approval.ActorSubjectID != input.OwnerSubjectID {
		return nil, actions.ErrApprovalRequired
	}
	if !compensation && card.Approval.ReceiptRef != input.AuthorizationRef {
		return nil, activities.ErrOperationAuthorization
	}
	if card.Status != "approved" && card.Status != "executing" && card.Status != "verifying" && card.Status != "completed" && card.Status != "failed" {
		return nil, actions.ErrApprovalRequired
	}
	return card, nil
}

func sameOperationTarget(left, right *actions.GovernedCard) bool {
	return left != nil && right != nil && left.Template.StackID == right.Template.StackID && left.ServerID == right.ServerID &&
		left.Template.Target == right.Template.Target
}

func lifecycleOperation(class string) (string, string, error) {
	switch class {
	case "drift-correction":
		return jobs.StackKitLifecycleDriftReconcile, "stackkit-drift-reconcile", nil
	case "certificate-rotation":
		return jobs.StackKitLifecyclePublicTLSRepair, "stackkit-public-tls-repair", nil
	case "rolling-update":
		return jobs.StackKitLifecycleUpgrade, "stackkit-upgrade", nil
	case "coordinated-rollback":
		return jobs.StackKitLifecycleRollback, "stackkit-rollback", nil
	default:
		return "", "", activities.ErrOperationReference
	}
}

func requireOperationGrant(card *actions.GovernedCard, scope string) error {
	if card.Template.Grant == nil {
		return actions.ErrGrantRequired
	}
	for _, admitted := range card.Template.Grant.Scopes {
		if admitted == scope {
			return nil
		}
	}
	return actions.ErrGrantRequired
}

func (b *GovernedOperationBackend) prepare(ctx context.Context, input activities.OperationInput, card *actions.GovernedCard, verify bool) (jobs.StackKitLifecycleRequest, error) {
	operation, scope, err := lifecycleOperation(card.Template.Primitive.OperationClass)
	if err != nil {
		return jobs.StackKitLifecycleRequest{}, err
	}
	if verify {
		operation, scope = jobs.StackKitLifecycleVerify, "stackkit-verify"
	}
	if err := requireOperationGrant(card, scope); err != nil {
		return jobs.StackKitLifecycleRequest{}, err
	}
	if card.Approval == nil || !time.Now().UTC().Before(card.Approval.ValidUntil) {
		return jobs.StackKitLifecycleRequest{}, actions.ErrApprovalRequired
	}
	if err := actions.RefuseUnentitledStart(card, actions.BeginExecution{Now: time.Now().UTC()}); err != nil {
		return jobs.StackKitLifecycleRequest{}, err
	}
	request := jobs.StackKitLifecycleRequest{
		TenantID: input.TenantID, OwnerID: input.OwnerSubjectID, StackID: card.Template.StackID,
		AgentID: card.Template.Target.RuntimeInstanceRef, Operation: operation, OwnerApproved: true,
		ExpectedSpecHash: card.Template.ResolvedPlanHash,
	}
	for _, parameter := range card.Template.Inputs {
		switch parameter.ID {
		case "target-release":
			if parameter.Type != rilaction.InputTypeStringEnum {
				return request, activities.ErrOperationReference
			}
			request.TargetRelease = parameter.Enum
		case "rollback-target":
			if parameter.Type != rilaction.InputTypeOpaqueReference || !strings.HasPrefix(parameter.OpaqueRef, "change:") {
				return request, activities.ErrOperationReference
			}
			request.RollbackTargetRef = "sha256:" + strings.TrimPrefix(parameter.OpaqueRef, "change:")
		default:
			return request, activities.ErrOperationReference
		}
	}
	if operation == jobs.StackKitLifecycleUpgrade && request.TargetRelease == "" {
		return request, activities.ErrOperationReference
	}
	if operation == jobs.StackKitLifecyclePublicTLSRepair {
		stack, err := b.Store.GetStack(ctx, request.TenantID, request.StackID)
		if err != nil || stack == nil || stack.OwnerSubjectID != request.OwnerID {
			return request, controlplane.ErrNotFound
		}
		request.OriginHosts = jobs.DegradedPublicTLSHosts(stack.RuntimeSummary)
		if len(request.OriginHosts) == 0 {
			return request, orchestrator.ErrPublicTLSRepairNotNeeded
		}
	}
	request, err = jobs.NormalizeStackKitLifecycleRequest(request)
	if err != nil {
		return request, err
	}
	h := crudRouteHandlers{serviceStore: b.Store, serverStore: b.Store}
	if locked, err := h.lockedStackServicesContext(ctx, request.TenantID, request.StackID, request.Operation); err != nil {
		return request, err
	} else if len(locked) != 0 {
		return request, controlplane.ErrConflict
	}
	if _, err := h.activeStackMaintenanceContext(ctx, request.TenantID, request.StackID, request.AgentID); !errors.Is(err, controlplane.ErrNotFound) {
		if err != nil {
			return request, err
		}
		return request, controlplane.ErrConflict
	}
	return request, nil
}

func (b *GovernedOperationBackend) dispatch(ctx context.Context, input activities.OperationInput, card *actions.GovernedCard) (map[string]any, error) {
	request, err := b.prepare(ctx, input, card, false)
	if err != nil {
		return nil, err
	}
	identity := sha256.Sum256([]byte(input.TenantID + "\x00" + card.ID + "\x00" + input.IdempotencyKey))
	begin, err := b.Authority.Begin(ctx, actions.BeginExecution{
		TenantID: input.TenantID, OwnerSubjectID: input.OwnerSubjectID, CardID: card.ID,
		ExecutionID: fmt.Sprintf("ril-operation-%x", identity), TraceID: fmt.Sprintf("ril-trace-%x", identity),
		IdempotencyKey: input.IdempotencyKey, Now: time.Now().UTC(),
	})
	if err != nil {
		return nil, err
	}
	if begin.Disposition == actions.BeginReplay {
		return nil, actions.ErrCardConflict
	}
	request, err = jobs.BindRILStackKitOperation(request, begin.Request, begin.Admission.Digest, input.IdempotencyKey)
	if err != nil {
		return nil, err
	}
	jobID, err := b.Orch.EnqueueStackKitLifecycle(ctx, request)
	if err != nil {
		return nil, err
	}
	return map[string]any{"job_id": jobID, "card_id": card.ID, "execution_id": begin.Request.ExecutionID}, nil
}

func (b *GovernedOperationBackend) admitted(ctx context.Context, input activities.OperationInput, card *actions.GovernedCard) (actions.BeginResult, error) {
	if (card.Status != "executing" && card.Status != "verifying") || card.ExecutionAdmission == nil || card.IdempotencyKey == "" {
		return actions.BeginResult{}, actions.ErrExecutionAdmission
	}
	return b.Authority.Begin(ctx, actions.BeginExecution{
		TenantID: input.TenantID, OwnerSubjectID: input.OwnerSubjectID, CardID: card.ID,
		ExecutionID: card.ExecutionID, TraceID: card.TraceID, IdempotencyKey: card.IdempotencyKey, Now: time.Now().UTC(),
	})
}

func operationState(state map[string]any, key string) map[string]any {
	value, _ := state[key].(map[string]any)
	return value
}

func operationString(state map[string]any, key string) string {
	value, _ := state[key].(string)
	return value
}

func (b *GovernedOperationBackend) result(ctx context.Context, input activities.OperationInput, card *actions.GovernedCard, jobID, operation string, admitted actions.BeginResult) (*controlplane.Job, bool, error) {
	job, err := b.Store.GetJob(ctx, input.TenantID, jobID)
	if err != nil || job == nil || job.StackID != card.Template.StackID || job.Type != string(jobs.JobTypeStackKitLifecycle) {
		return nil, false, controlplane.ErrNotFound
	}
	request, err := jobs.RILStackKitOperationFromJob(&jobs.Job{ID: job.ID, TargetID: job.StackID, Payload: job.Payload, Result: job.Result})
	if err != nil || request.TenantID != input.TenantID || request.OwnerID != input.OwnerSubjectID || request.Operation != operation {
		return nil, false, actions.ErrExecutionAdmission
	}
	receipt, _ := job.Result[jobs.RILOperationReceiptKey].(map[string]any)
	// RequestDigest is not a persisted admission column. Recompute it from
	// the stored admitted request when an executing card is resumed.
	requestDigest, err := rilaction.ComputeRequestDigest(admitted.Request)
	if err != nil {
		return nil, false, actions.ErrExecutionAdmission
	}
	if err := validateGovernedOperationReceipt(input, card.ID, operation, admitted, requestDigest, receipt); err != nil {
		return nil, false, err
	}
	if job.State == "pending" || job.State == "running" || job.State == "waiting" {
		return job, true, nil
	}
	if job.State != "completed" || job.CompletedAt == nil || job.Result["schema_version"] != "techstack.stackkit-lifecycle-result/v1" ||
		job.Result["techstack_id"] != card.Template.StackID || job.Result["agent_id"] != admitted.Request.Target.RuntimeInstanceRef ||
		job.Result["operation"] != operation || job.Result["success"] != true {
		return job, false, ErrGovernedNodeFailed
	}
	return job, false, nil
}

func validateGovernedOperationReceipt(input activities.OperationInput, cardID, operation string, admitted actions.BeginResult, requestDigest string, receipt map[string]any) error {
	if requestDigest == "" || operationString(receipt, "card_id") != cardID || operationString(receipt, "execution_id") != admitted.Request.ExecutionID ||
		operationString(receipt, "request_digest") != requestDigest || operationString(receipt, "admission_digest") != admitted.Admission.Digest ||
		operationString(receipt, "tenant_id") != input.TenantID || operationString(receipt, "owner_id") != input.OwnerSubjectID ||
		operationString(receipt, "agent_id") != admitted.Request.Target.RuntimeInstanceRef || operationString(receipt, "operation") != operation {
		return actions.ErrExecutionAdmission
	}
	return nil
}

func (b *GovernedOperationBackend) verify(ctx context.Context, input activities.OperationInput, card *actions.GovernedCard, state map[string]any) (map[string]any, error) {
	admitted, err := b.admitted(ctx, input, card)
	if err != nil {
		return nil, err
	}
	operation, _, _ := lifecycleOperation(input.Operation)
	original := operationState(state, "operation_result")
	jobID := operationString(original, "job_id")
	if operationString(original, "card_id") != card.ID || operationString(original, "execution_id") != admitted.Request.ExecutionID {
		return nil, actions.ErrExecutionAdmission
	}
	_, pending, err := b.result(ctx, input, card, jobID, operation, admitted)
	if err != nil {
		return nil, err
	}
	if pending {
		return map[string]any{"pending": true, "job_id": jobID}, nil
	}
	request, err := b.prepare(ctx, input, card, true)
	if err != nil {
		return nil, err
	}
	request, err = jobs.BindRILStackKitOperation(request, admitted.Request, admitted.Admission.Digest, input.IdempotencyKey)
	if err != nil {
		return nil, err
	}
	verifyID, err := b.Orch.EnqueueStackKitLifecycle(ctx, request)
	if err != nil {
		return nil, err
	}
	_, pending, err = b.result(ctx, input, card, verifyID, jobs.StackKitLifecycleVerify, admitted)
	if err != nil {
		return nil, err
	}
	return map[string]any{"verified": !pending, "pending": pending, "job_id": jobID, "verify_job_id": verifyID}, nil
}

func (b *GovernedOperationBackend) audit(ctx context.Context, input activities.OperationInput, card *actions.GovernedCard, state map[string]any) (map[string]any, error) {
	admitted, err := b.admitted(ctx, input, card)
	if err != nil {
		return nil, err
	}
	operation, _, _ := lifecycleOperation(input.Operation)
	result := operationState(state, "operation_result")
	original, pending, err := b.result(ctx, input, card, operationString(result, "job_id"), operation, admitted)
	if err != nil {
		return nil, err
	}
	if pending {
		return nil, ErrGovernedOperationPending
	}
	verification := operationState(state, "verification_result")
	verified, pending, err := b.result(ctx, input, card, operationString(verification, "verify_job_id"), jobs.StackKitLifecycleVerify, admitted)
	if err != nil {
		return nil, err
	}
	if pending {
		return nil, ErrGovernedOperationPending
	}
	if verified.CreatedAt.Before(*original.CompletedAt) {
		return nil, actions.ErrExecutionAdmission
	}
	evidence, err := operationEvidence(admitted.Request, *verified.CompletedAt)
	if err != nil {
		return nil, err
	}
	if _, err := b.Authority.Complete(ctx, input.TenantID, card.ID, evidence, "", time.Now().UTC()); err != nil {
		return nil, err
	}
	return map[string]any{"evidence_id": evidence.EvidenceID}, nil
}

func operationEvidence(request rilaction.Request, at time.Time) (rilaction.Evidence, error) {
	digest, err := rilaction.ComputeRequestDigest(request)
	if err != nil {
		return rilaction.Evidence{}, err
	}
	const executor = "stackkits-governed-lifecycle-v1"
	id, err := rilaction.ComputeEvidenceID(digest, executor)
	if err != nil {
		return rilaction.Evidence{}, err
	}
	target, err := rilaction.TargetReference(request)
	if err != nil {
		return rilaction.Evidence{}, err
	}
	evidence := rilaction.Evidence{
		APIVersion: rilaction.EvidenceAPIVersionV1, EvidenceID: id, EvidenceSinkRef: request.EvidenceSinkRef,
		ActionCardID: request.ActionCardID, ExecutionID: request.ExecutionID, TraceID: request.TraceID, TenantID: request.TenantID, StackID: request.StackID,
		PrimitiveID: request.Primitive.ID, PrimitiveContractHash: request.Primitive.ContractHash, ResolvedPlanHash: request.ResolvedPlanHash,
		RequestDigest: digest, ExecutorRef: executor, TargetRef: target, Status: "succeeded",
		Verification: rilaction.VerificationEvidence{Kind: "stackkit-pinned-cli-verify", Status: "passed", RuntimeStateObserved: true,
			Checks: []rilaction.VerificationCheck{{ID: "stackkit-verify", Status: "passed"}}},
		Recovery:     rilaction.RecoveryEvidence{Kind: "none", Status: "not-required"},
		SummaryCodes: []string{"stackkit-verify-passed"}, EvaluatedAt: at.UTC().Format(time.RFC3339Nano),
	}
	return evidence, rilaction.ValidateEvidenceForRequest(request, evidence)
}

func (b *GovernedOperationBackend) compensate(ctx context.Context, input activities.OperationInput, card *actions.GovernedCard, state map[string]any) (map[string]any, error) {
	original := operationState(state, "operation_result")
	if operationString(original, "card_id") != card.ID || operationString(original, "execution_id") != card.ExecutionID || card.ExecutionAdmission == nil {
		return nil, actions.ErrExecutionAdmission
	}
	if card.Status == "failed" {
		// Fail may have committed immediately before the workflow checkpoint.
		// Revalidate the exact recovery below; never reacquire a failed card.
		if card.ErrorCode != "stackkit_execution_failed" {
			return nil, actions.ErrCardConflict
		}
	} else {
		admitted, err := b.admitted(ctx, input, card)
		if err != nil {
			return nil, err
		}
		operation, _, _ := lifecycleOperation(input.Operation)
		_, pending, resultErr := b.result(ctx, input, card, operationString(original, "job_id"), operation, admitted)
		if pending {
			return map[string]any{"pending": true, "waiting_for_original": true}, nil
		}
		if resultErr != nil && !errors.Is(resultErr, ErrGovernedNodeFailed) {
			return nil, resultErr
		}
	}
	compensation, err := b.card(ctx, input, input.CompensationID, true)
	if err != nil || !sameOperationTarget(card, compensation) {
		return nil, actions.ErrApprovalRequired
	}
	jobID := jobs.RILStackKitOperationJobID(input.TenantID, input.OwnerSubjectID, compensation.ID, input.IdempotencyKey)
	checkpoint := operationState(state, "compensation_result")
	if saved := operationString(checkpoint, "job_id"); saved != "" &&
		(saved != jobID || operationString(checkpoint, "card_id") != compensation.ID || operationString(checkpoint, "execution_id") != compensation.ExecutionID) {
		return nil, actions.ErrExecutionAdmission
	}
	_, err = b.Store.GetJob(ctx, input.TenantID, jobID)
	if errors.Is(err, controlplane.ErrNotFound) {
		// With no workflow checkpoint, the deterministic reservation also
		// closes a crash after enqueue but before the pending result was saved.
		if operationString(checkpoint, "job_id") != "" || card.Status == "failed" {
			return nil, controlplane.ErrNotFound
		}
		result, err := b.dispatch(ctx, input, compensation)
		if err != nil {
			return nil, err
		}
		if operationString(result, "job_id") != jobID {
			return nil, actions.ErrExecutionAdmission
		}
	} else if err != nil {
		return nil, err
	}
	compensation, err = b.card(ctx, input, input.CompensationID, true)
	if err != nil {
		return nil, err
	}
	compensated, err := b.admitted(ctx, input, compensation)
	if err != nil {
		return nil, err
	}
	if compensation.IdempotencyKey != input.IdempotencyKey {
		return nil, actions.ErrExecutionAdmission
	}
	result := map[string]any{"job_id": jobID, "card_id": compensation.ID, "execution_id": compensation.ExecutionID}
	job, pending, err := b.result(ctx, input, compensation, jobID, jobs.StackKitLifecycleRollback, compensated)
	if err != nil {
		return result, err
	}
	if pending {
		if job.State == "pending" && !b.Orch.StackKitLifecycleJobQueued(jobID) {
			// A restart may lose only the in-memory offer. Re-offer the exact
			// validated persisted intent through the canonical reservation;
			// its durable execution claim prevents a second executor.
			request, err := jobs.RILStackKitOperationFromJob(&jobs.Job{ID: job.ID, TargetID: job.StackID, Payload: job.Payload, Result: job.Result})
			if err != nil {
				return result, err
			}
			resumedID, err := b.Orch.EnqueueStackKitLifecycle(ctx, request)
			if err != nil {
				return result, err
			}
			if resumedID != jobID {
				return result, actions.ErrExecutionAdmission
			}
		}
		result["pending"] = true
		return result, nil
	}
	// The original card remains failed, never "completed" by a recovery job.
	if _, err := b.Authority.Fail(ctx, input.TenantID, card.ID, card.ExecutionID, "stackkit_execution_failed", time.Now().UTC()); err != nil {
		return result, err
	}
	result["compensated"] = true
	return result, nil
}
