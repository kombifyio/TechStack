package rilactionexecution

import (
	"context"
	"fmt"
	"time"

	"github.com/kombifyio/techstack/pkg/agentcontrol"
	"github.com/kombifyio/techstack/pkg/api/agentpb"
	"github.com/kombifyio/techstack/pkg/grpcserver"
	"github.com/kombifyio/techstack/pkg/guardreach"
	"github.com/kombifyio/techstack/pkg/jobs"
	"github.com/kombifyio/techstack/pkg/ril/actioncontract"
	"github.com/kombifyio/techstack/pkg/stackkitcommand"
)

const pinnedCLIExecutorRef = "stackkits-pinned-cli-verify-v1"

// PinnedCLIConfig binds the only beta-core action to the typed StackKits CLI
// channel. The request cannot select a binary, command, provider, or transport.
type PinnedCLIConfig struct {
	Sender       jobs.StackKitCommandSender
	Now          func() time.Time
	Authority    DispatchAuthority
	ProofGateway ProofGateway
}

type PinnedCLIDispatcher struct {
	sender       jobs.StackKitCommandSender
	now          func() time.Time
	authority    DispatchAuthority
	proofGateway ProofGateway
}

func NewPinnedCLIDispatcher(config PinnedCLIConfig) (*PinnedCLIDispatcher, error) {
	if config.Sender == nil || config.Authority == nil || config.ProofGateway == nil {
		return nil, fmt.Errorf("rilactionexecution: typed sender, dispatch authority, and Gateway proof authority are required")
	}
	if config.Now == nil {
		config.Now = func() time.Time { return time.Now().UTC() }
	}
	return &PinnedCLIDispatcher{sender: config.Sender, now: config.Now, authority: config.Authority, proofGateway: config.ProofGateway}, nil
}

// DispatchAuthority resolves and locks the approved owner/server/lease tuple
// through the complete dispatch; it never derives a worker from the target.
type DispatchAuthority interface {
	OwnerFor(context.Context, rilaction.Request, string) (string, error)
	IssuedLease(context.Context, rilaction.Request, string) (guardreach.RILAuthorityResult, error)
	WithWorker(context.Context, rilaction.Request, string, *agentpb.RILExecutionProof, func(context.Context, string) error) error
}
type ProofGateway interface {
	RILAuthority(context.Context, string, string, string, string, string, string, string) (guardreach.RILAuthorityResult, error)
}

// Execute sends the catalog's stack-scoped verifier only after its exact pin is
// qualified and the admitted RuntimeLease still authorizes the current worker.
func (d *PinnedCLIDispatcher) Execute(ctx context.Context, request rilaction.Request, admissionDigest string) (rilaction.Evidence, error) {
	if request.Primitive.ID != "verify-stackkit-state" || request.Primitive.OperationClass != "product-verify" || request.Target.Scope != rilaction.TargetScopeStack {
		return rilaction.Evidence{}, fmt.Errorf("rilactionexecution: unsupported governed primitive profile")
	}
	scoped, ok := d.sender.(interface {
		SendStackKitCommandForTenant(context.Context, string, string, *agentpb.StackKitCommand) (*agentpb.StackKitResult, error)
	})
	if !ok {
		return d.failedEvidence(request, fmt.Errorf("RIL execution requires tenant-scoped transient Guard transport"))
	}
	if err := agentcontrol.ValidateRILQueueCustody(); err != nil {
		return d.failedEvidence(request, err)
	}
	owner, err := d.authority.OwnerFor(ctx, request, admissionDigest)
	if err != nil {
		return d.failedEvidence(request, err)
	}
	issued, err := d.authority.IssuedLease(ctx, request, admissionDigest)
	if err != nil || !issued.Allowed || issued.Lease == "" || len(issued.Receipt) == 0 || len(issued.Effect) == 0 {
		return d.failedEvidence(request, fmt.Errorf("Gateway issue denied or unavailable: %v", err))
	}
	claimed, err := d.proofGateway.RILAuthority(ctx, owner, request.TenantID, "claim", request.ActionCardID, issued.Lease, request.ExecutionID, admissionDigest)
	if err != nil || !claimed.Allowed || claimed.Claim == "" || claimed.ExecutionID != request.ExecutionID || claimed.Fence != admissionDigest || claimed.LeaseID == "" {
		return d.failedEvidence(request, fmt.Errorf("Gateway claim denied or mismatched: %v", err))
	}
	proof := &agentpb.RILExecutionProof{LeaseJws: issued.Lease, ClaimJws: claimed.Claim, ReceiptJson: issued.Receipt, EffectJson: issued.Effect}
	proof.ProofDigest, err = stackkitcommand.RILProofDigest(proof)
	if err != nil {
		return d.failedEvidence(request, err)
	}
	err = d.authority.WithWorker(ctx, request, admissionDigest, proof, func(ctx context.Context, worker string) error {
		release, err := jobs.TargetStackKitReleaseForStack(ctx, request.TenantID, request.StackID)
		if err != nil {
			return err
		}
		if release == nil {
			return fmt.Errorf("StackKits release unavailable")
		}
		pin := grpcserver.StackKitReleasePinFor(*release)
		if !release.VerifyExpectedPlanSupported() || !release.VerifyRILPrimitiveSupported() {
			return fmt.Errorf("pinned StackKits release lacks qualified plan and RIL primitive verifiers")
		}
		command := &agentpb.StackKitCommand{CommandId: request.ExecutionID, Operation: agentpb.StackKitOperation_STACKKIT_OPERATION_VERIFY,
			WorkingDirectory: "/opt/stackkit", SpecPath: "stack-spec.yaml", OutputDirectory: "deploy", TimeoutSeconds: 240,
			Release: pin, Offline: true, ExpectedPlanHash: request.ResolvedPlanHash, RilExecutionProof: proof}
		jobs.AddressNodeStackKitRelease(ctx, request.TenantID, worker, command)
		if err := stackkitcommand.ValidateCommand(command); err != nil {
			return err
		}
		result, err := scoped.SendStackKitCommandForTenant(ctx, request.TenantID, worker, command)
		if err != nil {
			return err
		}
		if err := stackkitcommand.ValidateResult(result, command); err != nil {
			return err
		}
		_, err = stackkitcommand.ParseVerifyPlanEvidence(command, result)
		return err
	})
	evidence, evidenceErr := d.evidence(request, err == nil)
	if evidenceErr != nil {
		return rilaction.Evidence{}, evidenceErr
	}
	return evidence, err
}

func (d *PinnedCLIDispatcher) failedEvidence(request rilaction.Request, cause error) (rilaction.Evidence, error) {
	evidence, err := d.evidence(request, false)
	if err != nil {
		return rilaction.Evidence{}, err
	}
	return evidence, cause
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
		Verification: rilaction.VerificationEvidence{Kind: "stackkit-pinned-cli-verify", Status: check, RuntimeStateObserved: false, Checks: []rilaction.VerificationCheck{{ID: "stackkit-verify", Status: check}}},
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
