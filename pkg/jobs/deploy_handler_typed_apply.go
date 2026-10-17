//nolint:goconst // moved unchanged from deploy_handler.go; repeated field names predate the split
package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kombifyio/techstack/internal/advancedissuer"
	"github.com/kombifyio/techstack/internal/runtimeproduct/runtimeaction"
	"github.com/kombifyio/techstack/internal/stackkitrelease"
	"github.com/kombifyio/techstack/pkg/api/agentpb"
	"github.com/kombifyio/techstack/pkg/identity"
	"github.com/kombifyio/techstack/pkg/originca"
	"github.com/kombifyio/techstack/pkg/stackkitcommand"
)

//nolint:gocyclo // moved unchanged from deploy_handler.go
func (r *deployRollout) runTypedStackKitApply(ctx context.Context) (map[string]interface{}, error) {
	if r == nil || r.cfg == nil || r.cfg.StackKitCommander == nil || r.actionReq.TechStackEnrollment == nil {
		return nil, fmt.Errorf("typed StackKits deploy dispatcher is not configured")
	}
	release, err := configuredTargetStackKitRelease()
	if err != nil {
		return nil, err
	}
	if release == nil {
		return nil, fmt.Errorf("pinned published StackKits release is not configured")
	}
	canonical, err := canonicalStackSpecFor(r.actionReq.StackSpecPath, r.unifiedSpec.StackKit, r.actionReq.StackName)
	if err != nil {
		return nil, err
	}
	workingDirectory := defaultStackKitNodeWorkspace
	stackName, domain, candidateSpec, err := stackKitInitFacts(canonical.Path)
	if err != nil {
		return nil, err
	}
	specPath := filepath.Base(canonical.Path)
	boundSpecPath := ""
	if r.addressPrefix != "" {
		boundSpecPath = specPath
		specPath = "stack-spec.yaml"
	}
	baseRequest := StackKitLifecycleRequest{
		StackID:           r.job.TargetID,
		TenantID:          r.actionReq.TenantID,
		OwnerID:           r.actionReq.OwnerID,
		OwnerEmail:        stackOwnerEmail(r.job, r.actionReq.OwnerID),
		AgentID:           r.actionReq.TechStackEnrollment.RuntimeAgentID,
		NodeRef:           PrimaryStackKitNodeRef(r.unifiedSpec.StackKit),
		OwnerApproved:     true,
		WorkingDirectory:  workingDirectory,
		SpecPath:          specPath,
		AddressPrefix:     r.addressPrefix,
		BoundSpecPath:     boundSpecPath,
		StackKit:          r.unifiedSpec.StackKit,
		StackName:         stackName,
		Domain:            domain,
		CandidateSpecJSON: candidateSpec,
		// A fresh managed workspace has no current intent to compare-and-swap.
		// StackKits creates it atomically without this field and treats an exact
		// retry as already applied; a differing existing intent still fails
		// closed because replacement requires its current normalized hash.
		ExpectedSpecHash: "",
	}
	if err := bindRolloutRetryCurrentIntent(r.job, &baseRequest, release.Receipt().Version, //nolint:govet // moved unchanged from deploy_handler.go
		r.actionReq.TechStackEnrollment.LeaseID, r.actionReq.TechStackEnrollment.ServerID); err != nil {
		return nil, err
	}
	var trustAdmission advancedTrustAdmission
	if issuer := r.cfg.AdvancedIssuer; issuer != nil {
		// Every managed deployment runs Advanced Mode from its first rollout:
		// the host must trust this installation's issuer before generation.
		baseRequest.AdvancedTrustBundle = issuer.TrustBundle()
		trustAdmission = func(ctx context.Context, command *agentpb.StackKitCommand, result *agentpb.StackKitResult) error {
			binding, admitErr := admitAdvancedTrustImport(ctx, issuer, baseRequest, stackName, command, result)
			if admitErr != nil {
				return admitErr
			}
			r.job.mutateResult(func(jobResult map[string]interface{}) { jobResult["advanced_trust"] = binding })
			return nil
		}
	}
	if strings.EqualFold(strings.TrimSpace(r.unifiedSpec.StackKit), "modern-homelab") {
		seam, seamErr := r.modernRolloutSeam(ctx, release.Receipt())
		if seamErr != nil {
			return nil, seamErr
		}
		baseRequest.modern = seam
	}
	if managedInventoryKit(r.unifiedSpec.StackKit) {
		resolvedPlan, readErr := readManagedResolvedPlan(r.actionReq.UnifiedPath)
		if readErr != nil {
			return nil, readErr
		}
		inventory, delivery, buildErr := r.buildManagedStackKitInventory(ctx, resolvedPlan)
		if buildErr != nil {
			return nil, buildErr
		}
		baseRequest.InventoryJSON = inventory
		baseRequest.managedBackupDelivery = delivery
	}
	originCertificate, originErr := r.managedOriginCertificate(*release)
	if originErr != nil {
		return nil, &typedStackKitOperationError{Operation: originCertificateStep, CommandID: r.job.ID + "-oca", Err: originErr}
	}
	baseRequest.originCertificate = originCertificate
	started := time.Now()
	budget := managedStackKitSequenceBudget(baseRequest.modern != nil, baseRequest.originCertificate != nil)
	result, err := runTypedStackKitApplySequenceWithBudget(ctx, r.cfg.StackKitCommander, r.job.ID, baseRequest, *release, budget, trustAdmission)
	if err != nil {
		reason := "typed_stackkit_rollout_failed"
		var operationErr *typedStackKitOperationError
		if errors.As(err, &operationErr) {
			reason = "typed_stackkit_" + operationErr.Operation + "_failed"
			if operationErr.TimedOut() {
				reason = "typed_stackkit_" + operationErr.Operation + "_timeout"
			}
			if operationErr.Stalled() {
				reason = "typed_stackkit_" + operationErr.Operation + "_stalled"
			}
			r.job.mutateResult(func(jobResult map[string]interface{}) {
				failure := map[string]interface{}{
					"status":      "failed",
					"reason_code": reason,
					"operation":   operationErr.Operation,
					"command_id":  operationErr.CommandID,
					"release":     release.Receipt().Version,
					"elapsed_ms":  time.Since(started).Milliseconds(),
				}
				if operationErr.TimedOut() || operationErr.Stalled() {
					failure["retryable"] = true
				}
				if stall := operationErr.AgentStall; stall != nil {
					failure["last_owner"] = stall.Owner
					failure["last_position"] = stall.Position
					failure["last_total"] = stall.Total
					failure["idle_seconds"] = int64(stall.Idle / time.Second)
				}
				if typed := operationErr.Failure; typed.ReasonCode != "" {
					failure["stackkit_reason_code"] = typed.ReasonCode
					failure["retryable"] = typed.Retryable || operationErr.TimedOut() || operationErr.Stalled()
					if !typed.RetryAfter.IsZero() {
						failure["retry_after"] = typed.RetryAfter.UTC().Format(time.RFC3339)
					}
				}
				jobResult["typed_stackkit_rollout"] = failure
			})
		}
		r.collectRuntimeDiagnostics(ctx, nil, r.actionReq, "stackkit_rollout", reason, time.Since(started), err)
		return result, err
	}
	// Later operator operations (verify, drift, backup, ...) must address the
	// StackSpec and kit this rollout applied, not the CLI default path.
	r.job.mutateResult(func(jobResult map[string]interface{}) {
		jobResult[StackKitRolloutBindingResultField] = stackKitRolloutBindingFor(baseRequest).Map()
	})
	return result, nil
}

// managedOriginCertificate returns the origin certificate hook of a managed
// kombify.me rollout, nil for every other rollout. A managed kombify.me
// rollout without the certificate service fails closed: Advanced Techstack
// rollouts never fall back to an ACME order for these routes. A node whose
// release predates ADR-0047 renders its own ACME router and has nothing to
// install, so the hook is skipped there instead of dispatching an operation
// that release cannot serve.
func (r *deployRollout) managedOriginCertificate(release stackkitrelease.Release) (*managedOriginCertificate, error) {
	if len(r.routeHosts) == 0 {
		return nil, nil
	}
	if !releaseRendersOriginCertificateRouter(release) {
		r.job.mutateResult(func(jobResult map[string]interface{}) {
			jobResult[OriginCertificateResultField] = map[string]interface{}{
				resultStatusField: "not_applicable", "reason_code": "release_renders_node_acme",
				"release": release.Receipt().Version,
			}
		})
		r.q.addLog(r.job, "info", fmt.Sprintf("StackKits %s serves kombify.me TLS through its own ACME router; no origin certificate is installed", release.Receipt().Version))
		return nil, nil
	}
	if r.cfg == nil || r.cfg.OriginCertificates == nil {
		return nil, &OriginCertificateError{Stage: "configuration", Err: originca.ErrUnavailable}
	}
	if r.actionReq.RuntimeTarget == nil {
		return nil, &OriginCertificateError{Stage: "hostnames", Err: fmt.Errorf("managed rollout has no runtime target for its origin record")}
	}
	hosts, err := managedOriginCertificateHosts(r.routeHosts, firstNonEmpty(r.actionReq.RuntimeTarget.PublicIP, r.actionReq.RuntimeTarget.Host))
	if err != nil {
		return nil, &OriginCertificateError{Stage: "hostnames", Err: err}
	}
	return &managedOriginCertificate{service: r.cfg.OriginCertificates, issuer: r.cfg.AdvancedIssuer, hosts: hosts}, nil
}

func splitMetadataList(value string) []string {
	var out []string
	for _, item := range strings.Split(value, ",") {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}

// StackOwnerEmailPayloadKey carries the stack Owner's signed-in email into a
// rollout job. StackKits init needs a real Owner email for a fresh node.
const StackOwnerEmailPayloadKey = "stack_owner_email"

// CaptureStackOwnerEmail records the requesting principal's email only when
// that principal is the stack Owner. An email from anyone else (support, a
// system job, another member) is never adopted as the Owner identity.
func CaptureStackOwnerEmail(ctx context.Context, payload map[string]interface{}, ownerID string) {
	if ctx == nil || payload == nil || strings.TrimSpace(ownerID) == "" {
		return
	}
	principal := identity.FromContext(ctx)
	if principal == nil || strings.TrimSpace(principal.UserID) != strings.TrimSpace(ownerID) {
		return
	}
	if email := strings.TrimSpace(principal.Email); stackkitcommand.ValidOwnerEmail(email) {
		payload[StackOwnerEmailPayloadKey] = email
	}
}

// stackOwnerEmail returns the Owner email captured at dispatch, or the email
// of a provision actor who is the Owner.
func stackOwnerEmail(job *Job, ownerID string) string {
	if job == nil || strings.TrimSpace(ownerID) == "" {
		return ""
	}
	job.mu.RLock()
	captured := strings.TrimSpace(stringFromMap(job.Payload, StackOwnerEmailPayloadKey))
	actor := jobActorFromPayload(job.Payload)
	job.mu.RUnlock()
	if stackkitcommand.ValidOwnerEmail(captured) {
		return captured
	}
	if strings.TrimSpace(actor.UserID) != strings.TrimSpace(ownerID) {
		return ""
	}
	if email := strings.TrimSpace(actor.Email); stackkitcommand.ValidOwnerEmail(email) {
		return email
	}
	return ""
}

// modernRolloutSeam resolves the Cloud node's agent before any command is
// sent, so a Modern rollout without an enrolled Cloud node fails at once and
// leaves the Home host untouched.
func (r *deployRollout) modernRolloutSeam(ctx context.Context, receipt stackkitrelease.Receipt) (*modernRolloutSeam, error) {
	if r.cfg.StackKitNodeAgents == nil {
		return nil, errors.New("the Modern Cloud node cannot be resolved: no node agent authority is configured")
	}
	cloudAgent, err := r.cfg.StackKitNodeAgents(ctx, r.actionReq.TenantID, r.actionReq.OwnerID, r.job.TargetID, modernCloudNodeRef)
	if err != nil {
		return nil, fmt.Errorf("resolve the Modern Cloud node agent: %w", err)
	}
	if strings.TrimSpace(cloudAgent) == "" {
		return nil, fmt.Errorf("the Modern Cloud node %q has no approved agent for this stack; enroll the Cloud host first", modernCloudNodeRef)
	}
	return &modernRolloutSeam{
		CloudAgentID: cloudAgent, Fabric: r.cfg.FederationFabric,
		StackKitsVersion: receipt.Version, CandidateDigest: "sha256:" + receipt.ArchiveSHA256,
	}, nil
}

// runTypedStackKitApplySequence preserves the StackKits lifecycle contract on
// the managed node. Init persists the canonical intent, generate resolves it
// and transactionally renders the authoritative ResolvedPlan and artifacts,
// plan inspects that exact generation, and only then may apply mutate the host.
// Control-plane artifacts live in a different filesystem and cannot substitute
// for this node-local generation boundary.
func runTypedStackKitApplySequence(ctx context.Context, sender StackKitCommandSender, jobID string, baseRequest StackKitLifecycleRequest, release stackkitrelease.Release) (map[string]interface{}, error) {
	return runTypedStackKitApplySequenceWithBudget(ctx, sender, jobID, baseRequest, release, 0, nil)
}

// advancedTrustAdmission admits and records the host's trust-import evidence.
// A nil admission still requires evidence that pins the dispatched bundle.
type advancedTrustAdmission func(context.Context, *agentpb.StackKitCommand, *agentpb.StackKitResult) error

//nolint:gocyclo // moved unchanged from deploy_handler.go
func runTypedStackKitApplySequenceWithBudget(ctx context.Context, sender StackKitCommandSender, jobID string, baseRequest StackKitLifecycleRequest, release stackkitrelease.Release, sequenceBudget time.Duration, admitTrust advancedTrustAdmission) (map[string]interface{}, error) {
	maxBudget := managedStackKitSequenceBudget(baseRequest.modern != nil, baseRequest.originCertificate != nil)
	if sequenceBudget <= 0 || sequenceBudget > maxBudget {
		sequenceBudget = maxBudget
	}
	if len(baseRequest.AdvancedTrustBundle) == 0 {
		// A managed rollout without an Advanced issuer would be a Standard-only
		// rollout. Refuse before init touches the host.
		return nil, &typedStackKitOperationError{
			Operation: StackKitLifecycleAdvancedTrustImport, CommandID: jobID + "-" + StackKitLifecycleAdvancedTrustImport,
			Err: fmt.Errorf("managed rollout requires the installation's Advanced trust bundle: %w", advancedissuer.ErrUnavailable),
		}
	}
	// Every managed deployment runs Advanced Mode: the node generates the
	// Terramate target from its first rollout so every later mutation is a
	// change set. StackKits change-set create needs a generated baseline and
	// apply needs a configured backup checkpoint, so the very first rollout
	// stays generate/plan/apply with the Terramate target. A release without
	// its Advanced operations catalog fails here before init touches the host.
	candidate, candidateErr := advancedGenerationCandidate(baseRequest.CandidateSpecJSON, release)
	if candidateErr != nil {
		return nil, &typedStackKitOperationError{Operation: StackKitLifecycleInit, CommandID: jobID + "-" + StackKitLifecycleInit, Err: candidateErr}
	}
	baseRequest.CandidateSpecJSON = candidate
	const generationTarget = "terramate"
	// Everything before apply, convergence waits and retries included, draws
	// from ONE shared deadline: what the sequence budget leaves after the apply
	// allowances. Apply and the Cloud node's apply run under the job context
	// with their own full budget, so no amount of pre-apply time can reduce
	// them; only a closer job deadline does.
	preApplyCtx, cancel := context.WithTimeout(ctx, sequenceBudget-managedStackKitApplyAllowances(baseRequest.modern != nil))
	defer cancel()
	nodePlanHash, err := runTypedStackKitPreApplySequence(preApplyCtx, sender, jobID, baseRequest, release, admitTrust)
	if err != nil {
		return nil, err
	}
	// ADR-0047: the managed kombify.me origin certificate is installed before
	// apply, which verifies TLS on the node. The node holds the key; only a CSR
	// and the public certificate cross the control plane. There is no ACME
	// fallback. A certificate that cannot be issued or installed does not stop
	// the stack: apply continues, public TLS is reported as a retryable
	// degraded component and the automatic repair installs it on this node.
	var originCertificate map[string]interface{}
	var degradedComponents []interface{}
	if baseRequest.originCertificate != nil {
		originCertificate, err = baseRequest.originCertificate.run(preApplyCtx, sender, jobID, baseRequest, release)
		if err != nil {
			if preApplyCtx.Err() != nil {
				return nil, &typedStackKitOperationError{Operation: originCertificateStep, CommandID: jobID + "-oca", Err: err}
			}
			degradedComponents = append(degradedComponents, publicTLSDegradedComponent(err, baseRequest.originCertificate.hosts, publicTLSRepairScheduled))
			originCertificate, err = nil, nil //nolint:ineffassign // moved unchanged from deploy_handler.go
		}
	}
	var cloudNode *modernCloudNode
	if baseRequest.modern != nil {
		// The Cloud member is certified by Home before Home applies, so Home
		// never runs the Cloud node's local owners in-process.
		if cloudNode, nodePlanHash, err = bringUpModernCloudNode(preApplyCtx, sender, jobID, baseRequest, release, nodePlanHash); err != nil {
			return nil, err
		}
	}

	request := baseRequest
	request.Operation = StackKitLifecycleApply
	request.ExpectedSpecHash = ""
	if request.managedBackupDelivery != nil {
		// The node's own Inventory, with the binding it issued, governs Apply.
		request.InventoryJSON = nil
	}
	if request.BoundSpecPath != "" {
		request.SpecPath = request.BoundSpecPath
	}
	request, err = NormalizeStackKitLifecycleRequest(request)
	if err != nil {
		return nil, err
	}
	result, _, err := dispatchTypedStackKitOperation(ctx, sender, jobID, request, release, func(command *agentpb.StackKitCommand) {
		command.ExpectedPlanHash = nodePlanHash
	})
	if result == nil {
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("typed StackKits apply result is missing")
	}
	normalized, normErr := normalizeStackKitLifecycleResult(request, result)
	if normErr != nil {
		if err != nil {
			return nil, err
		}
		return nil, normErr
	}
	normalized[StackKitModeField] = StackKitModeAdvanced
	normalized["generation_target"] = generationTarget
	if originCertificate != nil {
		normalized[OriginCertificateResultField] = originCertificate
	}
	if err != nil && strings.TrimSpace(stringFromInterface(normalized["status"])) == string(runtimeaction.StatusCompletedDegraded) {
		// StackKits exits non-zero while a unit is incomplete, but its ledger
		// says the core applied: the stack is usable and its degraded units are
		// named and retryable, so the rollout completes degraded.
		var hosts []string
		if baseRequest.originCertificate != nil {
			hosts = baseRequest.originCertificate.hosts
		}
		degradedComponents = appendApplyDegradedComponents(degradedComponents, normalized, hosts)
		err = nil
	}
	if len(degradedComponents) > 0 {
		normalized[DegradedComponentsResultField] = degradedComponents
		if status := strings.TrimSpace(stringFromInterface(normalized["status"])); status == "" || status == string(runtimeaction.StatusApplied) {
			normalized["status"] = string(runtimeaction.StatusCompletedDegraded)
		}
	}
	if err != nil {
		return normalized, err
	}
	if cloudNode != nil {
		cloudResult, cloudErr := cloudNode.apply(ctx, sender, jobID, release)
		if cloudResult != nil {
			normalized["modern_cloud_node_apply"] = cloudResult
		}
		if cloudErr != nil {
			return normalized, cloudErr
		}
	}
	return normalized, nil
}

// stackKitStepBackupTargetDelivery is the managed rollout's sealed delivery of
// the off-site backup target between its two generates.
const stackKitStepBackupTargetDelivery = "backup_target_delivery"

// runTypedStackKitPreApplySequence runs init, the Advanced trust import,
// the optional address bind, generate and plan, and returns the node-local
// plan hash that Apply must match.
//
//nolint:gocyclo // moved unchanged from deploy_handler.go
func runTypedStackKitPreApplySequence(ctx context.Context, sender StackKitCommandSender, jobID string, baseRequest StackKitLifecycleRequest, release stackkitrelease.Release, admitTrust advancedTrustAdmission) (string, error) {
	if baseRequest.inspectCurrentIntent {
		request := baseRequest
		request.Operation = StackKitLifecyclePlan
		// Inspect the existing intent with its persisted inventory, before the
		// replacement's inventory or INIT can change the node's authority.
		request.InventoryJSON = nil
		request, err := NormalizeStackKitLifecycleRequest(request)
		if err != nil {
			return "", err
		}
		result, commandID, err := dispatchTypedStackKitOperation(ctx, sender, jobID+"-current-intent", request, release, nil)
		if err != nil {
			return "", err
		}
		_, specHash, err := typedStackKitPlanBinding(result, true)
		if err != nil {
			return "", &typedStackKitOperationError{Operation: StackKitLifecyclePlan, CommandID: commandID, Err: err}
		}
		baseRequest.ExpectedSpecHash = specHash
	}
	nodePlanHash := ""
	operations := []string{StackKitLifecycleInit, StackKitLifecycleAdvancedTrustImport}
	if baseRequest.AddressPrefix != "" {
		operations = append(operations, StackKitLifecycleAddressBind)
	}
	operations = append(operations, StackKitLifecycleGenerate)
	if baseRequest.managedBackupDelivery != nil {
		// The documented sequence: generate with the channel-only Inventory,
		// import the node's custody from the sealed delivery, then generate
		// again. Nothing replaces the node's Inventory afterwards, so the binding
		// the node issued stays the one Apply and Plan see.
		operations = append(operations, stackKitStepBackupTargetDelivery, StackKitLifecycleGenerate)
	}
	operations = append(operations, StackKitLifecyclePlan)
	// Command IDs are unique per node: the steps after the custody import get
	// their own scope, so the second generate never reuses the first one's ID.
	commandScope := jobID
	for _, operation := range operations {
		if operation == stackKitStepBackupTargetDelivery {
			if _, err := deliverManagedBackupTarget(ctx, sender, jobID, baseRequest, release, baseRequest.managedBackupDelivery, StackKitLifecycleBackupTargetImport); err != nil {
				return "", err
			}
			baseRequest.InventoryJSON = nil
			commandScope = jobID + "-custodied"
			continue
		}
		request := baseRequest
		request.Operation = operation
		if operation != StackKitLifecycleInit {
			request.ExpectedSpecHash = ""
		}
		if operation != StackKitLifecycleInit && operation != StackKitLifecycleAdvancedTrustImport &&
			operation != StackKitLifecycleAddressBind && request.BoundSpecPath != "" {
			request.SpecPath = request.BoundSpecPath
		}
		request, err := NormalizeStackKitLifecycleRequest(request)
		if err != nil {
			return "", err
		}
		var dispatched *agentpb.StackKitCommand
		result, commandID, err := dispatchTypedStackKitOperation(ctx, sender, commandScope, request, release, func(command *agentpb.StackKitCommand) {
			dispatched = command
		})
		if err != nil {
			return "", err
		}
		switch operation {
		case StackKitLifecycleAdvancedTrustImport:
			if admitErr := admitTypedAdvancedTrustImport(ctx, admitTrust, dispatched, result); admitErr != nil {
				return "", &typedStackKitOperationError{Operation: operation, CommandID: commandID, Err: admitErr}
			}
		case StackKitLifecyclePlan:
			// The node-local generation is authoritative for the node-local apply.
			// Controller generation includes controller-only inventory inputs and
			// therefore cannot be used as an equality gate for this workspace.
			nodePlanHash, err = typedStackKitPlanHash(result)
			if err != nil {
				return "", err
			}
		}
	}
	return nodePlanHash, nil
}

func admitTypedAdvancedTrustImport(ctx context.Context, admitTrust advancedTrustAdmission, command *agentpb.StackKitCommand, result *agentpb.StackKitResult) error {
	if _, err := stackkitcommand.ParseAdvancedTrustImportEvidence(command, result); err != nil {
		return err
	}
	if admitTrust == nil {
		return nil
	}
	return admitTrust(ctx, command, result)
}

const (
	typedStackKitRuntimePendingMaxAttempts   = 8
	typedStackKitRuntimePendingRetryInterval = 15 * time.Second
)

// typedStackKitRuntimePendingRetryDelay is the wait between convergence retries;
// tests shorten it.
var typedStackKitRuntimePendingRetryDelay = typedStackKitRuntimePendingRetryInterval

func dispatchTypedStackKitOperation(
	ctx context.Context,
	sender StackKitCommandSender,
	jobID string,
	request StackKitLifecycleRequest,
	release stackkitrelease.Release,
	prepare func(*agentpb.StackKitCommand),
) (*agentpb.StackKitResult, string, error) {
	var lastID string
	var lastResult *agentpb.StackKitResult
	for attempt := 1; attempt <= typedStackKitRuntimePendingMaxAttempts; attempt++ {
		commandID := jobID + "-" + request.Operation
		if attempt > 1 {
			commandID = fmt.Sprintf("%s-%s-%d", jobID, request.Operation, attempt)
		}
		lastID = commandID
		command, err := stackKitLifecycleCommand(commandID, request, release)
		if err != nil {
			return nil, commandID, err
		}
		if prepare != nil {
			prepare(command)
		}
		if err := bindManagedStackKitCommandBudget(ctx, command, request.Operation); err != nil { //nolint:govet // moved unchanged from deploy_handler.go
			return nil, commandID, &typedStackKitOperationError{Operation: request.Operation, CommandID: commandID, Err: err}
		}
		result, err := sendStackKitCommandBoundedWithGraceForTenant(ctx, sender, request.TenantID, request.AgentID, command, managedStackKitResultGrace)
		if err != nil {
			return nil, commandID, &typedStackKitOperationError{Operation: request.Operation, CommandID: commandID, Err: err}
		}
		if result == nil {
			return nil, commandID, fmt.Errorf("typed StackKits %s result is missing", request.Operation)
		}
		if result.Success {
			return result, commandID, nil
		}
		lastResult = result
		if !stackKitRuntimeConvergencePending(result) || attempt == typedStackKitRuntimePendingMaxAttempts {
			err := fmt.Errorf("StackKits %s failed with exit code %d: %s", request.Operation, result.ExitCode, strings.TrimSpace(result.Stderr))
			return result, commandID, &typedStackKitOperationError{
				Operation: request.Operation, CommandID: commandID, Err: err,
				AgentTimedOut: stackKitResultTimedOut(result), AgentStall: stackKitResultStall(result),
				Failure: typedStackKitFailureFromResult(result),
			}
		}
		if waitErr := waitForTypedStackKitRuntimeRetry(ctx); waitErr != nil {
			err := fmt.Errorf("StackKits %s failed with exit code %d: %s", request.Operation, result.ExitCode, strings.TrimSpace(result.Stderr))
			return result, commandID, &typedStackKitOperationError{Operation: request.Operation, CommandID: commandID, Err: err}
		}
	}
	return lastResult, lastID, fmt.Errorf("typed StackKits %s exhausted runtime-convergence retries", request.Operation)
}

func stackKitRuntimeConvergencePending(result *agentpb.StackKitResult) bool {
	if result == nil || result.Success {
		return false
	}
	return strings.Contains(strings.ToLower(result.Stderr), "runtime convergence is pending")
}

func waitForTypedStackKitRuntimeRetry(ctx context.Context) error {
	delay := typedStackKitRuntimePendingRetryDelay
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func bindManagedStackKitCommandBudget(ctx context.Context, command *agentpb.StackKitCommand, operation string) error {
	if command == nil {
		return errors.New("typed StackKits command is required")
	}
	budget := managedStackKitOperationTimeout(operation)
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline) - managedStackKitResultGrace
		if remaining <= 0 {
			return context.DeadlineExceeded
		}
		if remaining < budget {
			budget = remaining
		}
	}
	seconds := clampInt64ToInt32(int64(budget / time.Second))
	if seconds < 1 {
		seconds = 1
	}
	command.TimeoutSeconds = seconds
	return nil
}

type typedStackKitOperationError struct {
	Operation     string
	CommandID     string
	Err           error
	AgentTimedOut bool
	// AgentStall is set when the node ended the command because its apply
	// stopped reporting progress. That is not a timeout: the budget was not
	// used up, the run went silent.
	AgentStall *stackkitcommand.ApplyStall
	// Failure is the closed StackKits recovery fact, when the command
	// reported one.
	Failure typedStackKitFailure
}

// Stalled reports that the node ended the command because it went silent.
func (e *typedStackKitOperationError) Stalled() bool { return e.AgentStall != nil }

// stackKitResultStall reads the stall a node reported in a failed command.
func stackKitResultStall(result *agentpb.StackKitResult) *stackkitcommand.ApplyStall {
	if result == nil || result.Success {
		return nil
	}
	if stall, ok := stackkitcommand.ParseApplyStall(result.Stderr); ok {
		return &stall
	}
	return nil
}

func (e *typedStackKitOperationError) TimedOut() bool {
	return e.AgentTimedOut || errors.Is(e.Err, context.DeadlineExceeded)
}

func stackKitResultTimedOut(result *agentpb.StackKitResult) bool {
	if result == nil || result.Success {
		return false
	}
	message := strings.ToLower(strings.TrimSpace(result.Stderr))
	return strings.Contains(message, "stackkits command timed out") && strings.Contains(message, "context deadline exceeded")
}

func typedStackKitPlanHash(result *agentpb.StackKitResult) (string, error) {
	planHash, _, err := typedStackKitPlanBinding(result, false)
	return planHash, err
}

//nolint:gocyclo // moved unchanged from deploy_handler.go
func typedStackKitPlanBinding(result *agentpb.StackKitResult, requireSpecHash bool) (string, string, error) {
	var commandResult struct {
		SchemaVersion string `json:"schemaVersion"`
		Command       string `json:"command"`
		Status        string `json:"status"`
		Data          struct {
			Output string `json:"output"`
		} `json:"data"`
	}
	if result == nil || len(result.CommandResultJson) == 0 || json.Unmarshal(result.CommandResultJson, &commandResult) != nil {
		return "", "", errors.New("typed StackKits plan result is not a valid command receipt")
	}
	if commandResult.SchemaVersion != "stackkit.command-result/v1" || commandResult.Command != "stackkit plan" || commandResult.Status != "success" || strings.TrimSpace(commandResult.Data.Output) == "" {
		return "", "", errors.New("typed StackKits plan command receipt identity or status is invalid")
	}
	var inspection struct {
		APIVersion      string `json:"apiVersion"`
		Kind            string `json:"kind"`
		VerifiedPhase   string `json:"verifiedPhase"`
		ExecutorInvoked bool   `json:"executorInvoked"`
		Binding         struct {
			PlanHash string `json:"planHash"`
			SpecHash string `json:"specHash"`
		} `json:"binding"`
		Readiness struct {
			Generation struct {
				Status   string   `json:"status"`
				Blockers []string `json:"blockers"`
			} `json:"generation"`
		} `json:"readiness"`
	}
	if err := json.Unmarshal([]byte(commandResult.Data.Output), &inspection); err != nil {
		return "", "", fmt.Errorf("decode typed StackKits plan inspection: %w", err)
	}
	inspection.Binding.PlanHash = strings.TrimSpace(inspection.Binding.PlanHash)
	inspection.Binding.SpecHash = strings.TrimSpace(inspection.Binding.SpecHash)
	if inspection.APIVersion != "stackkit.plan-inspection/v1" || inspection.Kind != "PlanInspection" ||
		inspection.VerifiedPhase != "generation" || inspection.ExecutorInvoked ||
		inspection.Readiness.Generation.Status != "ready" || len(inspection.Readiness.Generation.Blockers) != 0 ||
		!resolvedPlanHashPattern.MatchString(inspection.Binding.PlanHash) ||
		(requireSpecHash && !resolvedPlanHashPattern.MatchString(inspection.Binding.SpecHash)) {
		return "", "", errors.New("typed StackKits node-local plan is not a ready canonical ResolvedPlan generation")
	}
	return inspection.Binding.PlanHash, inspection.Binding.SpecHash, nil
}

func (r *deployRollout) runTypedStackKitVerify(ctx context.Context) (map[string]interface{}, error) {
	if r == nil || r.cfg == nil || r.cfg.StackKitCommander == nil || r.actionReq.TechStackEnrollment == nil {
		return nil, fmt.Errorf("typed StackKits verify dispatcher is not configured")
	}
	release, err := configuredTargetStackKitRelease()
	if err != nil {
		return nil, err
	}
	if release == nil {
		return nil, fmt.Errorf("pinned published StackKits release is not configured")
	}
	canonical, err := canonicalStackSpecFor(r.actionReq.StackSpecPath, r.unifiedSpec.StackKit, r.actionReq.StackName)
	if err != nil {
		return nil, err
	}
	request, err := NormalizeStackKitLifecycleRequest(StackKitLifecycleRequest{
		StackID:          r.job.TargetID,
		TenantID:         r.actionReq.TenantID,
		OwnerID:          r.actionReq.OwnerID,
		AgentID:          r.actionReq.TechStackEnrollment.RuntimeAgentID,
		NodeRef:          PrimaryStackKitNodeRef(r.unifiedSpec.StackKit),
		Operation:        StackKitLifecycleVerify,
		WorkingDirectory: defaultStackKitNodeWorkspace,
		SpecPath:         filepath.Base(canonical.Path),
		StackKit:         r.unifiedSpec.StackKit,
	})
	if err != nil {
		return nil, err
	}
	command, err := stackKitLifecycleCommand(r.job.ID+"-verify", request, *release)
	if err != nil {
		return nil, err
	}
	result, err := sendStackKitCommandBoundedForTenant(ctx, r.cfg.StackKitCommander, request.TenantID, request.AgentID, command)
	if err != nil {
		return nil, fmt.Errorf("typed StackKits verify dispatch failed: %w", err)
	}
	normalized, err := normalizeStackKitLifecycleResult(request, result)
	if err != nil {
		return nil, err
	}
	if !result.Success {
		return normalized, fmt.Errorf("StackKits verify failed with exit code %d: %s", result.ExitCode, strings.TrimSpace(result.Stderr))
	}
	return normalized, nil
}

func stackKitInitFacts(specPath string) (string, string, []byte, error) {
	file, err := os.Open(filepath.Clean(specPath))
	if err != nil {
		return "", "", nil, fmt.Errorf("open canonical StackSpec for init: %w", err)
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > stackkitcommand.MaxInitCandidateBytes {
		return "", "", nil, fmt.Errorf("init candidate must be a bounded regular StackSpec")
	}
	canonical, err := io.ReadAll(io.LimitReader(file, stackkitcommand.MaxInitCandidateBytes+1))
	if err != nil || len(canonical) > stackkitcommand.MaxInitCandidateBytes {
		return "", "", nil, fmt.Errorf("read canonical StackSpec within init candidate limit")
	}
	document, err := decodeStackSpecDocument(canonical)
	if err != nil {
		return "", "", nil, err
	}
	if !json.Valid(canonical) {
		canonical, err = json.Marshal(document)
		if err != nil {
			return "", "", nil, fmt.Errorf("encode approved StackSpec for typed init: %w", err)
		}
	}
	metadata := mapFromInterface(document["metadata"])
	name := strings.TrimSpace(stringFromInterface(metadata["name"]))
	network := mapFromInterface(document["network"])
	domainConfig := mapFromInterface(network["domain"])
	domain := strings.TrimSpace(stringFromInterface(domainConfig["base"]))
	if name == "" {
		return "", "", nil, fmt.Errorf("canonical StackSpec metadata.name is required for init")
	}
	return name, domain, canonical, nil
}
