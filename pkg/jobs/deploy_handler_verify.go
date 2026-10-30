package jobs

import (
	"context"
	"crypto/sha256"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/kombifyio/techstack/internal/runtimeproduct/runtimeaction"
	"github.com/kombifyio/techstack/pkg/api/agentpb"
	"github.com/kombifyio/techstack/pkg/grpcserver"
	"github.com/kombifyio/techstack/pkg/stackkitcommand"
)

// runVerify verifies login-protected services through the pinned StackKits
// Verify. Managed and self-hosted rollouts both require a verified result;
// a rollout without a verifier fails instead of passing silently.
func (r *deployRollout) runVerify(ctx context.Context) error {
	if r.cfg.StackKitCommander == nil {
		r.job.setStep(StepVerifyRollout)
		return wrapProvisionError(StepVerifyRollout,
			"StackKits rollout verifier is not configured",
			"Every rollout requires verified login-protected services from the pinned StackKits Verify before success is shown.")
	}
	startRuntimeLifecyclePhase(r.job, runtimePhaseVerify, "Verifying the deployed StackKit services")
	r.job.setStep(StepVerifyRollout)
	r.q.UpdateProgress(r.job.ID, 88, "Verifying login-protected services...")
	breadcrumbStep(ctx, StepVerifyRollout, "verifying login-protected services", nil)
	result, err := r.runTypedStackKitVerify(ctx)
	if err != nil {
		captureJobError(ctx, err, map[string]interface{}{stepField: StepVerifyRollout, stackIDField: r.job.TargetID})
		return wrapProvisionCause(StepVerifyRollout, fmt.Errorf("rollout verification failed: %w", err),
			"Immich/Vaultwarden verification failed or auth protection is not in place.")
	}
	proof := runtimeActionProof(string(runtimeaction.ActionVerifyRollout), result, string(runtimeaction.StatusVerified))
	r.runtimeProof["verification"] = proof
	r.e2eProof["verification_result"] = runtimeActionProofStatus(proof)
	r.persistRuntimeProgress()
	if !runtimeActionStatusAllowed(runtimeActionProofStatus(proof), string(runtimeaction.StatusVerified)) {
		return wrapProvisionError(StepVerifyRollout,
			fmt.Sprintf("rollout verifier returned non-verified status %q", runtimeActionProofStatus(proof)),
			"Runtime listener claims activate only after the exact StackKit rollout reports verified services.")
	}
	mergeStackKitOutputs(r.stackKitOutputs, result)
	r.persistRuntimeProgress()
	completeRuntimeLifecyclePhase(r.job, runtimePhaseVerify, "StackKit verification completed", map[string]interface{}{resultStatusField: runtimeActionProofStatus(proof)})
	r.e2eProof["auth_checks"] = []string{"login-protected-services"}
	r.e2eProof["monitoring_checks"] = []string{"otlp-health-visible"}
	r.persistRuntimeProgress()
	if err := r.activatePortClaims(ctx); err != nil {
		return wrapProvisionCause(StepPortAdmission, fmt.Errorf("activate verified runtime listeners: %w", err),
			"The StackKit rollout verified, but Techstack could not activate its exact host-listener generation. The reservation remains fail-closed for reconciliation.")
	}
	return nil
}

// runRestoreDrill runs the Advanced restore drill. Managed and self-hosted
// rollouts both require a verified drill; a rollout without a drill
// dispatcher fails instead of passing silently. A verified drill promotes the
// final runtime phase to "verified" and records runtime metrics.
func (r *deployRollout) runRestoreDrill(ctx context.Context) error {
	if r.cfg.StackKitCommander == nil {
		r.job.setStep(StepRestoreDrill)
		return wrapProvisionError(StepRestoreDrill,
			"Advanced restore drill dispatcher is not configured",
			"Every rollout requires a verified Advanced restore drill before success is shown.")
	}
	r.job.setStep(StepRestoreDrill)
	r.q.UpdateProgress(r.job.ID, 92, "Running backup restore drill...")
	breadcrumbStep(ctx, StepRestoreDrill, "running backup restore drill", nil)
	result, err := r.runTypedStackKitRestoreDrill(ctx)
	if err != nil {
		captureJobError(ctx, err, map[string]interface{}{stepField: StepRestoreDrill, stackIDField: r.job.TargetID})
		return wrapProvisionCause(StepRestoreDrill, fmt.Errorf("restore drill failed: %w", err),
			"Backup restore verification failed.")
	}
	proof := runtimeActionProof(string(runtimeaction.ActionRestoreDrill), result, string(runtimeaction.StatusVerified))
	r.runtimeProof["restore"] = proof
	mergeStackKitOutputs(r.stackKitOutputs, result)
	restoreStatus := runtimeActionProofStatus(proof)
	r.e2eProof["restore_result"] = restoreStatus
	r.persistRuntimeProgress()
	if !runtimeActionStatusAllowed(restoreStatus, string(runtimeaction.StatusVerified)) {
		return wrapProvisionError(StepRestoreDrill,
			fmt.Sprintf("restore drill returned non-verified status %q", restoreStatus),
			"Every rollout requires a verified Advanced restore drill before success is shown.")
	}
	if runtimeActionStatusCountsAsVerified(restoreStatus) {
		r.finalRuntimePhase = RuntimePhaseVerified
		r.recordPhase(r.finalRuntimePhase)
	}
	mergeRuntimeMetrics(r.runtimeMetrics, result)
	return nil
}

// runTypedStackKitRestoreDrill configures the owner-bound backup repository
// for the node plan and runs the pinned release's Advanced restore.drill
// through the enrolled node's closed command channel. The pinned CLI remains
// the sole authority for local Owner custody, repository and the locally
// verified drill receipt. Missing Advanced prerequisites fail before the
// first command reaches the node.
//
//nolint:gocyclo // moved unchanged from deploy_handler.go
func (r *deployRollout) runTypedStackKitRestoreDrill(ctx context.Context) (map[string]interface{}, error) {
	if r == nil || r.cfg == nil || r.cfg.StackKitCommander == nil || r.actionReq.TechStackEnrollment == nil || r.job == nil || r.unifiedSpec == nil {
		return nil, fmt.Errorf("typed StackKits restore dispatcher is not configured")
	}
	release, err := TargetStackKitReleaseForStack(ctx, r.actionReq.TenantID, r.actionReq.StackID)
	if err != nil {
		return nil, err
	}
	if release == nil {
		return nil, fmt.Errorf("pinned published StackKits release is not configured")
	}
	if err := r.requireAdvancedRestoreDrill(*release); err != nil { //nolint:govet // moved unchanged from deploy_handler.go
		return nil, err
	}
	canonical, err := canonicalStackSpecFor(r.actionReq.StackSpecPath, r.unifiedSpec.StackKit, r.actionReq.StackName)
	if err != nil {
		return nil, err
	}
	stackName, _, _, err := stackKitInitFacts(canonical.Path)
	if err != nil {
		return nil, err
	}
	request, err := NormalizeStackKitLifecycleRequest(StackKitLifecycleRequest{
		StackID:            r.job.TargetID,
		StackKitInstanceID: stackName,
		TenantID:           r.actionReq.TenantID,
		OwnerID:            r.actionReq.OwnerID,
		AgentID:            r.actionReq.TechStackEnrollment.RuntimeAgentID,
		NodeRef:            PrimaryStackKitNodeRef(r.unifiedSpec.StackKit),
		Operation:          StackKitLifecyclePlan,
		WorkingDirectory:   defaultStackKitNodeWorkspace,
		SpecPath:           filepath.Base(canonical.Path),
		StackKit:           r.unifiedSpec.StackKit,
	})
	if err != nil {
		return nil, err
	}
	planHash, err := planStackKitLifecycleApply(ctx, r.cfg.StackKitCommander, r.job.ID+"-restore", request, *release)
	if err != nil {
		return nil, fmt.Errorf("bind restore drill to node plan: %w", err)
	}
	binding, err := localExecutionBindingForNode(request.StackKit, request.NodeRef)
	if err != nil {
		return nil, err
	}
	// StackKits requires the owner-bound backup repository to be configured
	// for the applied plan before its first snapshot ("run stackkit backup
	// configure first"); Apply does not configure it. Configure is idempotent
	// for an unchanged owner, authority and policy, so every drill runs it.
	configureCommand := &agentpb.StackKitCommand{
		CommandId:        nativeRestoreDrillOperationID("configure", request.StackKitInstanceID, planHash),
		Operation:        agentpb.StackKitOperation_STACKKIT_OPERATION_BACKUP_CONFIGURE,
		WorkingDirectory: defaultStackKitNodeWorkspace,
		SpecPath:         request.SpecPath, TimeoutSeconds: int32((15 * time.Minute) / time.Second),
		Release:      grpcserver.StackKitReleasePinFor(*release),
		LocalSiteRef: binding.SiteRef, LocalNodeRef: binding.NodeRef,
		LocalExecutionChannelRef: binding.ExecutionChannelRef,
		Stackkit:                 request.StackKit, StackName: stackName,
		StackkitInstanceId: stackName, ExpectedPlanHash: planHash,
	}
	if err := stackkitcommand.ValidateCommand(configureCommand); err != nil { //nolint:govet // moved unchanged from deploy_handler.go
		return nil, fmt.Errorf("build restore-drill repository configuration command: %w", err)
	}
	configureResult, err := sendStackKitCommandBoundedForTenant(ctx, r.cfg.StackKitCommander, request.TenantID, request.AgentID, configureCommand)
	if err != nil {
		return nil, fmt.Errorf("restore-drill repository configuration dispatch failed: %w", err)
	}
	if err := stackkitcommand.ValidateResult(configureResult, configureCommand); err != nil {
		return nil, fmt.Errorf("admit restore-drill repository configuration: %w", err)
	}
	if err := requireSuccessfulNativeBackupStep("backup configure", configureResult); err != nil {
		return nil, err
	}
	if err := stackkitcommand.ParseBackupConfigurationEvidence(configureCommand, configureResult); err != nil {
		return nil, fmt.Errorf("admit restore-drill repository configuration: %w", err)
	}

	return r.runAdvancedRestoreDrill(ctx, request, *release, planHash)
}

// requireSuccessfulNativeBackupStep keeps the pinned CLI's own failure: a
// failed step otherwise surfaced only as missing evidence, which hid the
// node-side cause of the drill failure.
func requireSuccessfulNativeBackupStep(step string, result *agentpb.StackKitResult) error {
	if result != nil && result.Success {
		return nil
	}
	exitCode, stderr := int32(-1), ""
	if result != nil {
		exitCode, stderr = result.ExitCode, strings.TrimSpace(result.Stderr)
	}
	const maxStderr = 1024
	if len(stderr) > maxStderr {
		stderr = stderr[:maxStderr] + "..."
	}
	return fmt.Errorf("StackKits %s failed with exit code %d: %s", step, exitCode, stderr)
}

func nativeRestoreDrillOperationID(kind, stackKitInstanceID, planHash string) string {
	digest := sha256.Sum256([]byte(strings.Join([]string{
		strings.TrimSpace(stackKitInstanceID), strings.TrimSpace(planHash), strings.TrimSpace(kind),
	}, "\x00")))
	return fmt.Sprintf("techstack-restore-drill-%s-%x", kind, digest[:16])
}
