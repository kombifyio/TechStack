package rilactionexecution

import (
	"context"
	"fmt"
	"time"

	"github.com/kombifyio/techstack/internal/selfhostcontracts/stackaction"
	"github.com/kombifyio/techstack/pkg/api/agentpb"
	"github.com/kombifyio/techstack/pkg/grpcserver"
	"github.com/kombifyio/techstack/pkg/jobs"
	"github.com/kombifyio/techstack/pkg/ril/actioncontract"
)

const pinnedCLIExecutorRef = "stackkits-pinned-cli-verify-v1"

// PinnedCLIConfig binds the only beta-core action to the typed StackKits CLI
// channel. The request cannot select a binary, command, provider, or transport.
type PinnedCLIConfig struct {
	Sender jobs.StackKitCommandSender
	Now    func() time.Time
}

type PinnedCLIDispatcher struct {
	sender jobs.StackKitCommandSender
	now    func() time.Time
}

func NewPinnedCLIDispatcher(config PinnedCLIConfig) (*PinnedCLIDispatcher, error) {
	if config.Sender == nil {
		return nil, fmt.Errorf("rilactionexecution: typed StackKits sender is required")
	}
	if config.Now == nil {
		config.Now = func() time.Time { return time.Now().UTC() }
	}
	return &PinnedCLIDispatcher{sender: config.Sender, now: config.Now}, nil
}

func (d *PinnedCLIDispatcher) Execute(ctx context.Context, request rilaction.Request) (rilaction.Evidence, error) {
	if request.Primitive.ID != "verify-stackkit-state" || request.Primitive.OperationClass != "verification" || request.Target.Scope != rilaction.TargetScopeRuntimeInstance {
		return rilaction.Evidence{}, fmt.Errorf("rilactionexecution: unsupported governed primitive")
	}
	release, err := jobs.ConfiguredPinnedStackKitRelease()
	if err != nil || release == nil {
		evidence, evidenceErr := d.evidence(request, false)
		if evidenceErr != nil {
			return rilaction.Evidence{}, evidenceErr
		}
		return evidence, fmt.Errorf("rilactionexecution: pinned published StackKits release is unavailable")
	}
	operation, err := pinnedCLIStackActionOperation(stackaction.ActionVerifyRollout)
	if err != nil {
		return rilaction.Evidence{}, err
	}
	command := &agentpb.StackKitCommand{
		CommandId: request.ExecutionID, Operation: operation,
		WorkingDirectory: "/opt/stackkit", SpecPath: "stack-spec.yaml", OutputDirectory: "deploy",
		TimeoutSeconds: 240, Release: grpcserver.StackKitReleasePinFor(*release), Offline: true,
	}
	var result *agentpb.StackKitResult
	var dispatchErr error
	if scoped, ok := d.sender.(interface {
		SendStackKitCommandForTenant(context.Context, string, string, *agentpb.StackKitCommand) (*agentpb.StackKitResult, error)
	}); ok {
		result, dispatchErr = scoped.SendStackKitCommandForTenant(ctx, request.TenantID, request.Target.RuntimeInstanceRef, command)
	} else {
		result, dispatchErr = d.sender.SendStackKitCommand(ctx, request.Target.RuntimeInstanceRef, command)
	}
	succeeded := dispatchErr == nil && result != nil && result.Success
	evidence, evidenceErr := d.evidence(request, succeeded)
	if evidenceErr != nil {
		return rilaction.Evidence{}, evidenceErr
	}
	if !succeeded {
		return evidence, fmt.Errorf("rilactionexecution: typed StackKits verification failed")
	}
	return evidence, nil
}

func pinnedCLIStackActionOperation(action stackaction.Action) (agentpb.StackKitOperation, error) {
	if action != stackaction.ActionVerifyRollout {
		return agentpb.StackKitOperation_STACKKIT_OPERATION_UNSPECIFIED, fmt.Errorf("rilactionexecution: unsupported StackAction")
	}
	return agentpb.StackKitOperation_STACKKIT_OPERATION_VERIFY, nil
}

//nolint:goconst // Evidence status literals are the closed public rilaction wire vocabulary.
func (d *PinnedCLIDispatcher) evidence(request rilaction.Request, succeeded bool) (rilaction.Evidence, error) {
	requestDigest, err := rilaction.ComputeRequestDigest(request)
	if err != nil {
		return rilaction.Evidence{}, err
	}
	evidenceID, err := rilaction.ComputeEvidenceID(requestDigest, pinnedCLIExecutorRef)
	if err != nil {
		return rilaction.Evidence{}, err
	}
	targetRef, err := rilaction.TargetReference(request)
	if err != nil {
		return rilaction.Evidence{}, err
	}
	status, check, summary := "failed", "failed", "stackkit-verify-failed"
	if succeeded {
		status, check, summary = "succeeded", "passed", "stackkit-verify-passed"
	}
	evidence := rilaction.Evidence{
		APIVersion: rilaction.EvidenceAPIVersionV1, EvidenceID: evidenceID, EvidenceSinkRef: request.EvidenceSinkRef,
		ActionCardID: request.ActionCardID, ExecutionID: request.ExecutionID, TraceID: request.TraceID,
		TenantID: request.TenantID, StackID: request.StackID, PrimitiveID: request.Primitive.ID,
		PrimitiveContractHash: request.Primitive.ContractHash, ResolvedPlanHash: request.ResolvedPlanHash,
		RequestDigest: requestDigest, ExecutorRef: pinnedCLIExecutorRef, TargetRef: targetRef, Status: status,
		Verification: rilaction.VerificationEvidence{Kind: "stackkit-pinned-cli-verify", Status: check, RuntimeStateObserved: true, Checks: []rilaction.VerificationCheck{{ID: "stackkit-verify", Status: check}}},
		Recovery:     rilaction.RecoveryEvidence{Kind: "none", Status: "not-required"}, SummaryCodes: []string{summary},
		EvaluatedAt: d.now().UTC().Format(time.RFC3339Nano),
	}
	if !succeeded {
		evidence.ProtectedDiagnosticRef = "diagnostic:" + request.ExecutionID
	}
	if err := rilaction.ValidateEvidenceForRequest(request, evidence); err != nil {
		return rilaction.Evidence{}, err
	}
	return evidence, nil
}

var _ Dispatcher = (*PinnedCLIDispatcher)(nil)
