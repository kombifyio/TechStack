//nolint:goconst // moved unchanged from deploy_handler.go; repeated field names predate the split
package jobs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/kombifyio/techstack/internal/portinventory"
	"github.com/kombifyio/techstack/internal/runtimeproduct/runtimeaction"
	"github.com/kombifyio/techstack/pkg/config"
	"github.com/kombifyio/techstack/pkg/core"
	"github.com/kombifyio/techstack/pkg/runtimeidentity"
	"github.com/kombifyio/techstack/pkg/workerauth"
)

// deployRollout holds the mutable proof/output state shared across the runtime
// action phases (simulation gate, rollout, verify, restore drill).
type deployRollout struct {
	cfg            *ProvisionConfig
	job            *Job
	q              *Queue
	managedRuntime bool
	targetKind     string
	unifiedSpec    *core.UnifiedSpec
	actionReq      RuntimeActionRequest

	e2eProof          map[string]any
	runtimeProof      map[string]interface{}
	stackKitOutputs   map[string]interface{}
	runtimeMetrics    map[string]string
	finalRuntimePhase RuntimePhase
	portGeneration    *portinventory.GenerationRef
	portRequirements  []portinventory.Requirement
	generatedPlanHash string
	addressPrefix     string
	// routeHosts are the registered kombify.me route hostnames of a managed
	// install; non-empty marks a managed kombify.me rollout.
	routeHosts          []string
	portMutationStarted bool
	portActivated       bool
	// baselineProof is the listener observation that passed the host baseline
	// at admission and when it passed.
	baselineProof *hostBaselineProof
}

// newDeployRollout builds the runtime-action request and the initial e2e proof
// scaffold from the prepared artifacts.
func newDeployRollout(cfg *ProvisionConfig, job *Job, q *Queue, prep *deployPreparation, art *deployArtifacts) (*deployRollout, error) {
	targetKind := firstNonEmpty(payloadString(job.Payload, "target_kind"), "unknown")
	tenantID := managedRuntimeTenantID(job, prep.kombSpec)
	ownerID := managedRuntimeOwnerID(job, prep.kombSpec)
	enrollment, enrollmentErr := techStackEnrollmentForRollout(job, prep, tenantID, ownerID)
	if enrollmentErr != nil {
		return nil, enrollmentErr
	}
	actionReq := RuntimeActionRequest{
		StackID:             job.TargetID,
		StackName:           prep.kombSpec.Name,
		StackKit:            art.unifiedSpec.StackKit,
		TenantID:            tenantID,
		OwnerID:             ownerID,
		StackSpecPath:       art.stackSpecPath,
		TofuDir:             art.tofuDir,
		UnifiedPath:         art.unifiedPath,
		OwnerSpecBootstrap:  ownerSpecBootstrapFromPayload(job.Payload),
		RuntimeTarget:       stackKitsRuntimeActionTargetFromManagedRuntimeTarget(prep.managedTarget),
		PlatformNodes:       art.platformNodes,
		TechStackEnrollment: enrollment,
	}
	e2eProof := map[string]any{
		stackIDField:          job.TargetID,
		"stack_name":          prep.kombSpec.Name,
		"stackkit_ref":        art.unifiedSpec.StackKit,
		"target_kind":         targetKind,
		"stack_spec_path":     art.stackSpecPath,
		"unified_path":        art.unifiedPath,
		"phases_completed":    []string{},
		"simulation_result":   string(JobStatePending),
		"rollout_result":      string(JobStatePending),
		"verification_result": string(JobStatePending),
		"auth_checks":         []string{string(JobStatePending)},
		"monitoring_checks":   []string{string(JobStatePending)},
		"restore_result":      string(JobStatePending),
	}
	if enrollment != nil {
		e2eProof["server_id"] = enrollment.ServerID
		e2eProof["runtime_agent_id"] = enrollment.RuntimeAgentID
		e2eProof["lease_id"] = enrollment.LeaseID
	}
	return &deployRollout{
		cfg:               cfg,
		job:               job,
		q:                 q,
		managedRuntime:    prep.managedRuntime,
		targetKind:        targetKind,
		unifiedSpec:       art.unifiedSpec,
		actionReq:         actionReq,
		generatedPlanHash: strings.TrimSpace(art.metadata["resolved_plan_hash"]),
		addressPrefix:     strings.TrimSpace(art.metadata[metadataKeyKombifyMeAddressPrefix]),
		routeHosts:        splitMetadataList(art.metadata[metadataKeyKombifyMeRouteHosts]),
		e2eProof:          e2eProof,
		runtimeProof:      map[string]interface{}{},
		stackKitOutputs:   map[string]interface{}{},
		runtimeMetrics:    map[string]string{},
		finalRuntimePhase: RuntimePhaseDeployed,
	}, nil
}

func techStackEnrollmentForRollout(job *Job, prep *deployPreparation, tenantID, ownerID string) (*TechStackEnrollment, error) {
	if job == nil {
		return nil, nil
	}
	// Both lanes mint a signed agent token, so both need a non-empty identity for
	// workerauth.Issue (which requires tenant_id). Managed leases always carry a
	// tenant; a self-hosted/BYOS stack may not, so default to the same synthetic
	// identity the managed-runtime path uses (runtime.go). Without this, flipping
	// the mint fail-closed below would hard-break every tenant-less BYOS rollout.
	ownerID = firstNonEmpty(strings.TrimSpace(ownerID), "system")
	tenantID = firstNonEmpty(strings.TrimSpace(tenantID), ownerID, "self-hosted")
	stackID := strings.TrimSpace(job.TargetID)
	leaseID := firstNonEmpty(stringFromMap(job.Result, leaseIDField), stringFromMap(job.Payload, leaseIDField), metadataString(prep.kombSpec, leaseIDField))
	serverID := ""
	if leaseID != "" {
		// A managed lease owns its server identity. Never propagate a
		// caller-provided server_id into the signed bootstrap contract.
		serverID = runtimeidentity.LeaseServerID(leaseID)
	} else {
		workerServerID, workerServerErr := firstWorkerServerIDFromPayload(job.Payload["workers"])
		if workerServerErr != nil {
			return nil, workerServerErr
		}
		explicitServerID := firstNonEmpty(
			stringFromMap(job.Result, "server_id"),
			stringFromMap(job.Payload, "server_id"),
			metadataString(prep.kombSpec, "server_id"),
		)
		if workerServerID != "" && explicitServerID != "" && explicitServerID != workerServerID {
			return nil, &RuntimeServerIdentityMismatchError{CanonicalServerID: workerServerID, RequestedServerID: explicitServerID}
		}
		serverID = firstNonEmpty(workerServerID, explicitServerID)
		if serverID == "" {
			serverID = stableRuntimeIdentifier("server", tenantID, stackID)
		}
	}
	runtimeAgentID := ""
	if leaseID != "" {
		// Cloud-init enrollment and rollout delivery must target the same Guard.
		runtimeAgentID = runtimeidentity.LeaseRuntimeAgentID(tenantID, leaseID)
	} else {
		runtimeAgentID = firstNonEmpty(
			stringFromMap(job.Result, "runtime_agent_id"),
			stringFromMap(job.Payload, "runtime_agent_id"),
			metadataString(prep.kombSpec, "runtime_agent_id"),
			firstWorkerIDFromPayload(job.Payload["workers"]),
		)
		if runtimeAgentID == "" {
			runtimeAgentID = stableRuntimeIdentifier("runtime", tenantID, serverID)
		}
	}
	serverURL := publicTechStackServerURL()
	heartbeatPath := "/api/v1/workers/" + runtimeAgentID + "/heartbeat"
	inventoryPath := "/api/v1/workers/" + runtimeAgentID + "/inventory"
	token, tokenErr := workerauth.Issue(workerauth.SecretFromEnv(), workerauth.Claims{
		TenantID:       tenantID,
		OwnerID:        ownerID,
		StackID:        stackID,
		LeaseID:        leaseID,
		ServerID:       serverID,
		RuntimeAgentID: runtimeAgentID,
	}, time.Now().UTC(), 24*time.Hour)
	if tokenErr != nil || token == "" {
		// Fail closed on BOTH lanes: an unsigned agent token means the node's
		// phone-home heartbeat/inventory cannot be authenticated, so the
		// dashboard would be blind to the server the rollout just provisioned.
		// The managed and BYOS/self-hosted lanes are treated equally here (both
		// now have a guaranteed identity above). A signing secret must be
		// configured — an unsigned opt-out lives behind an explicit env below,
		// never an implicit lane check.
		if allowUnsignedWorkerToken() {
			token, _ = workerauth.OpaqueToken()
		} else {
			return nil, fmt.Errorf("runtime agent enrollment requires a signed worker agent token (set TECHSTACK_WORKER_AGENT_TOKEN_SECRET or, for local/dev only, %s=1): %w", envAllowUnsignedWorkerToken, firstEnrollmentTokenError(tokenErr))
		}
	}
	if token == "" {
		return nil, fmt.Errorf("runtime agent enrollment token could not be generated")
	}
	return &TechStackEnrollment{
		TenantID:       tenantID,
		OwnerID:        ownerID,
		StackID:        stackID,
		LeaseID:        leaseID,
		ServerURL:      serverURL,
		ServerID:       serverID,
		RuntimeAgentID: runtimeAgentID,
		AgentToken:     token,
		HeartbeatURL:   absoluteTechStackURL(serverURL, heartbeatPath),
		InventoryURL:   absoluteTechStackURL(serverURL, inventoryPath),
		ChannelBootstrap: map[string]any{
			"tenant_id":         tenantID,
			"owner_id":          ownerID,
			"stack_id":          stackID,
			"lease_id":          leaseID,
			"server_id":         serverID,
			"runtime_agent_id":  runtimeAgentID,
			"heartbeat_url":     absoluteTechStackURL(serverURL, heartbeatPath),
			"inventory_url":     absoluteTechStackURL(serverURL, inventoryPath),
			"grpc_hint":         "mtls-if-http2-network-allows",
			"ssh_liveness_role": "bootstrap-diagnostics-only",
			"telemetry": map[string]any{
				"sentry_configured":  strings.TrimSpace(os.Getenv("SENTRY_DSN")) != "",
				"posthog_configured": firstNonEmpty(os.Getenv("PUBLIC_POSTHOG_KEY"), os.Getenv("POSTHOG_KEY")) != "",
			},
		},
	}, nil
}

var ErrRuntimeServerIdentityMismatch = errors.New("runtime server identity does not match the canonical control-plane worker")

// RuntimeServerIdentityMismatchError prevents a caller-provided server ID from
// redirecting admission away from the canonical RuntimeServer selected by the
// authenticated control-plane worker.
type RuntimeServerIdentityMismatchError struct {
	CanonicalServerID string
	RequestedServerID string
}

func (e *RuntimeServerIdentityMismatchError) Error() string {
	return ErrRuntimeServerIdentityMismatch.Error()
}

func (e *RuntimeServerIdentityMismatchError) Unwrap() error {
	return ErrRuntimeServerIdentityMismatch
}

// envAllowUnsignedWorkerToken opts a local/dev deploy out of fail-closed agent
// token signing. Production and any secret-configured environment must never
// set it — an unsigned token cannot authenticate the node phone-home.
const envAllowUnsignedWorkerToken = "TECHSTACK_ALLOW_UNSIGNED_WORKER_TOKEN" // #nosec G101 -- env var name, not a credential value.

const (
	// simulationGateStatusField records on the job whether the optional
	// simulated-update gate actually ran, so an absent preview is visible
	// evidence rather than a silent skip.
	simulationGateStatusField   = "simulation_gate_status"
	simulationGateNotConfigured = "not_configured"
)

func allowUnsignedWorkerToken() bool {
	return truthyRuntimeActionEnv(envAllowUnsignedWorkerToken)
}

func firstEnrollmentTokenError(err error) error {
	if err != nil {
		return err
	}
	return workerauth.ErrInvalidToken
}

func publicTechStackServerURL() string {
	return strings.TrimRight(firstNonEmpty(
		config.AgentOriginOverride(),
		os.Getenv("TECHSTACK_PUBLIC_ORIGIN"),
		os.Getenv("TECHSTACK_PUBLIC_URL"),
		os.Getenv("TECHSTACK_SERVER_URL"),
		os.Getenv("RENDER_EXTERNAL_URL"),
	), "/")
}

func absoluteTechStackURL(serverURL, path string) string {
	path = "/" + strings.TrimLeft(strings.TrimSpace(path), "/")
	if strings.TrimSpace(serverURL) == "" {
		return path
	}
	return strings.TrimRight(serverURL, "/") + path
}

func stableRuntimeIdentifier(prefix string, values ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(values, "\x00")))
	return strings.TrimSpace(prefix) + "_" + hex.EncodeToString(sum[:])[:24]
}

func (r *deployRollout) recordPhase(phase RuntimePhase) {
	r.e2eProof["phases_completed"] = append(r.e2eProof["phases_completed"].([]string), string(phase))
	r.persistRuntimeProgress()
}

func (r *deployRollout) persistRuntimeProgress() {
	if r == nil || r.job == nil {
		return
	}
	r.job.mutateResult(func(result map[string]interface{}) {
		if len(r.e2eProof) > 0 {
			result["e2e_proof"] = r.e2eProof
		}
		if len(r.runtimeProof) > 0 {
			result["runtime_proof"] = r.runtimeProof
		}
		if len(r.stackKitOutputs) > 0 {
			result[metadataKeyStackKitOutputs] = r.stackKitOutputs
		}
	})
}

// runSimulationGate runs the simulated-update gate when one is configured, and
// records its verdict. A configured gate that fails still stops the rollout.
//
// It used to hard-require the gate for managed cloud rollouts, which made every
// one of them impossible: the simulate bridge was retired in #265, the same
// change that set out to enable the demo managed runtime, and
// scripts/render-environment-contract.mjs now lists TECHSTACK_KOMBISIM_URL under
// retiredLiveEnvironmentKeys and removes it on every reconciliation. So the
// runtime demanded a variable the deployment contract deletes by design, and
// every rollout died with "managed cloud simulation gate is not configured".
//
// Simulate is a credential-free, fake-only harness whose capability is surfaced
// as the optional "Simulation Preview" feature; provisionRunOneLinerSimulationPreview
// already treats an absent gate as a missing preview rather than a failure.
// Requiring a fake-only harness to pass before a real rollout was backwards, so
// this path now agrees with that one.
func (r *deployRollout) runSimulationGate(ctx context.Context) error {
	if r.cfg.RuntimeActions.SimulationGate == nil {
		r.job.mutateResult(func(result map[string]interface{}) {
			result[simulationGateStatusField] = simulationGateNotConfigured
		})
		return nil
	}
	r.job.setStep(StepSimulationGate)
	r.q.UpdateProgress(r.job.ID, 70, "Running simulated update gate...")
	breadcrumbStep(ctx, StepSimulationGate, "running simulation gate", map[string]interface{}{
		"stack_kit":   r.unifiedSpec.StackKit,
		"unified":     r.actionReq.UnifiedPath,
		"target_kind": r.targetKind,
	})
	result, err := runRuntimeAction(ctx, r.cfg.RuntimeActions.SimulationGate, r.actionReq)
	if err != nil {
		captureJobError(ctx, err, map[string]interface{}{stepField: StepSimulationGate, stackIDField: r.job.TargetID})
		return wrapProvisionCause(StepSimulationGate, fmt.Errorf("simulation gate failed: %w", err),
			"Could not validate the rollout against the simulation gate.")
	}
	proof := runtimeActionProof(runtimeActionSimulateUpdate, result, "passed")
	r.runtimeProof["simulation"] = proof
	r.e2eProof["simulation_result"] = runtimeActionProofStatus(proof)
	r.persistRuntimeProgress()
	if r.managedRuntime && !runtimeActionStatusAllowed(runtimeActionProofStatus(proof), "accepted", "passed", string(runtimeaction.StatusReady), string(runtimeaction.StatusVerified)) {
		return wrapProvisionError(StepSimulationGate,
			fmt.Sprintf("simulation gate returned non-success status %q", runtimeActionProofStatus(proof)),
			"Managed kombify Cloud rollouts require a passing simulation gate.")
	}
	r.recordPhase(RuntimePhaseSimulationPassed)
	return nil
}

// runRollout runs the StackKits-owned rollout operation (default on) and records
// the deployed phase.
func (r *deployRollout) runRollout(ctx context.Context) error {
	// Optional: run the StackKits-owned rollout operation (default true).
	apply := deployApplyEnabled(r.job)

	r.job.setStep(StepProvision)
	if apply {
		startRuntimeLifecyclePhase(r.job, runtimePhasePrepareApply, "Preparing and applying the StackKit rollout")
		if r.cfg.StackKitCommander == nil {
			r.updateJobStepProgress(StepRolloutRunner, 75, "Running StackKits rollout...")
			return wrapProvisionError(StepRolloutRunner,
				"typed StackKits dispatcher is not configured",
				"Every rollout runs Advanced Mode through the pinned StackKits CLI on the enrolled node. Connect the node's agent before starting deploy.")
		}
		if r.managedRuntime {
			r.updateJobStepProgress(StepStackKitPrepare, 72, "Preparing managed VPS for StackKits rollout...")
		} else {
			r.updateJobStepProgress(StepRolloutRunner, 75, "Running StackKits rollout...")
		}
		breadcrumbStep(ctx, StepRolloutRunner, "applying StackKits rollout", map[string]interface{}{
			"stack_kit":   r.unifiedSpec.StackKit,
			"target_kind": r.targetKind,
		})
		if err := r.markPortMutationStarted(ctx); err != nil {
			return wrapProvisionCause(StepPortAdmission, fmt.Errorf("runtime listener mutation fence failed: %w", err),
				"Techstack could not bind this rollout to its reserved host listeners. No host mutation was started.")
		}
		if err := r.bootstrapManagedRuntimeTarget(ctx); err != nil {
			if isJobWaitError(err) || ctx.Err() == context.Canceled {
				return err
			}
			captureJobError(ctx, err, map[string]interface{}{stepField: StepStackKitPrepare, stackIDField: r.job.TargetID, "stack_kit": r.unifiedSpec.StackKit})
			return wrapProvisionCause(StepStackKitPrepare, fmt.Errorf("managed runtime target bootstrap failed: %w", err),
				"TechStack could not prepare the managed VPS for the StackKits rollout.")
		}
		if r.managedRuntime {
			completeRuntimeLifecyclePhase(r.job, runtimePhaseReachability, "Managed runtime target passed the StackKit bootstrap boundary", nil)
		}
		r.updateJobStepProgress(StepRolloutRunner, 82, "Applying StackKit services on the managed VPS...")
		result, err := r.runTypedStackKitApply(ctx)
		// A refusal the owner's own server reports about itself fails the job
		// as before but is not a Techstack fault to report.
		err = classifyOwnerHostRefusal(err, r.managedRuntime)
		if err != nil {
			if result != nil {
				proof := runtimeActionProof(string(runtimeaction.ActionStackKitRollout), result, "failed")
				proof["status"] = "failed"
				if r.runtimeProof == nil {
					r.runtimeProof = map[string]interface{}{}
				}
				r.runtimeProof["rollout"] = proof
				r.e2eProof["rollout_result"] = "failed"
				r.persistRuntimeProgress()
			}
			captureJobError(ctx, err, map[string]interface{}{stepField: StepRolloutRunner, stackIDField: r.job.TargetID, "stack_kit": r.unifiedSpec.StackKit})
			return wrapProvisionCause(StepRolloutRunner, fmt.Errorf("StackKits rollout failed: %w", err), rolloutFailureDetails(err))
		}
		proof := runtimeActionProof(string(runtimeaction.ActionStackKitRollout), result, string(runtimeaction.StatusApplied))
		r.runtimeProof["rollout"] = proof
		r.e2eProof["rollout_result"] = runtimeActionProofStatus(proof)
		r.persistRuntimeProgress()
		rolloutStatus := runtimeActionProofStatus(proof)
		if r.managedRuntime && !runtimeActionStatusAllowed(rolloutStatus, string(runtimeaction.StatusApplied), string(runtimeaction.StatusCompletedDegraded)) {
			return wrapProvisionError(StepRolloutRunner,
				fmt.Sprintf("StackKits rollout returned non-applied status %q", runtimeActionProofStatus(proof)),
				"Managed kombify Cloud rollouts require an applied StackKits rollout result.")
		}
		mergeStackKitOutputs(r.stackKitOutputs, result)
		if components, ok := result[DegradedComponentsResultField]; ok {
			r.job.mutateResult(func(jobResult map[string]interface{}) { jobResult[DegradedComponentsResultField] = components })
			r.q.addLog(r.job, "warn", "Secure public access through the kombify.me address is not active yet; kombify installs its certificate automatically on this server")
		}
		r.persistRuntimeProgress()
		if runtimeActionStatusAllowed(rolloutStatus, string(runtimeaction.StatusCompletedDegraded)) {
			completeDegradedRuntimeLifecyclePhase(r.job, runtimePhasePrepareApply, "StackKit core applied with retryable degraded components", map[string]interface{}{resultStatusField: rolloutStatus})
			r.q.addLog(r.job, "warn", "StackKit core is available; one or more independent applications remain degraded and retryable")
		} else {
			completeRuntimeLifecyclePhase(r.job, runtimePhasePrepareApply, "StackKit prepare/apply completed", map[string]interface{}{resultStatusField: rolloutStatus})
		}
		r.updateJobStepProgress(StepServiceInventory, 86, "Reading StackKit service inventory...")
	}
	r.recordPhase(RuntimePhaseDeployed)
	return nil
}

const (
	rolloutFailureDetailsDefault = "StackKits could not apply the selected StackKit rollout."
	// rolloutApplyTimeoutDetails is what the owner reads when the node's apply
	// ran out of its budget: the command was stopped mid-change, so the server
	// is partly set up, and a restart picks up on that same server.
	rolloutApplyTimeoutDetails = "Setting up the server took longer than allowed and was stopped, so it may be partly set up; restart the rollout to continue on the same server."
	// rolloutApplyStalledDetails is what the owner reads when the node ended an
	// apply that had stopped reporting progress. It names no owner or position:
	// those travel in the failure record, not in customer prose.
	rolloutApplyStalledDetails = "Setting up the server stopped responding and was ended, so it may be partly set up; restart the rollout to continue on the same server."
)

// rolloutFailureDetails is the stored, customer-readable failure message of a
// failed typed rollout.
func rolloutFailureDetails(err error) string {
	var operationErr *typedStackKitOperationError
	if errors.As(err, &operationErr) && operationErr.Operation == StackKitLifecycleApply {
		switch {
		case operationErr.Stalled():
			return rolloutApplyStalledDetails
		case operationErr.TimedOut():
			return rolloutApplyTimeoutDetails
		}
	}
	return rolloutFailureDetailsDefault
}

func firstWorkerIDFromPayload(payload any) string {
	workers, err := parseWorkersFromPayload(payload)
	if err != nil {
		return ""
	}
	for _, worker := range workers {
		if id := strings.TrimSpace(worker.ID); id != "" {
			return id
		}
	}
	return ""
}

func firstWorkerServerIDFromPayload(payload any) (string, error) {
	var items []interface{}
	switch typed := payload.(type) {
	case []interface{}:
		items = typed
	case []map[string]interface{}:
		items = make([]interface{}, 0, len(typed))
		for _, item := range typed {
			items = append(items, item)
		}
	default:
		return "", nil
	}
	serverID := ""
	for _, item := range items {
		candidate := strings.TrimSpace(stringFromMap(mapFromInterface(item), "server_id"))
		if candidate == "" {
			continue
		}
		if serverID != "" && candidate != serverID {
			return "", &RuntimeServerIdentityMismatchError{CanonicalServerID: serverID, RequestedServerID: candidate}
		}
		serverID = candidate
	}
	return serverID, nil
}

func (e *typedStackKitOperationError) Error() string {
	return fmt.Sprintf("typed StackKits %s command %s failed: %v", e.Operation, e.CommandID, e.Err)
}

func (e *typedStackKitOperationError) Unwrap() error { return e.Err }

// finalize enforces any explicitly requested identity handoff, persists
// managed runtime lease metadata, and builds the final deploy job result.
func (r *deployRollout) finalize(ctx context.Context, prep *deployPreparation, art *deployArtifacts) error {
	r.job.setStep(StepFinalize)
	r.q.UpdateProgress(r.job.ID, 95, "Finalizing rollout...")
	jobSnapshot := r.job.Snapshot()
	leaseID := firstNonEmpty(
		stringFromMap(jobSnapshot.Result, leaseIDField),
		stringFromMap(jobSnapshot.Payload, leaseIDField),
		metadataString(prep.kombSpec, leaseIDField),
	)
	leaseTenantID := managedRuntimeTenantID(r.job, prep.kombSpec)
	if r.managedRuntime {
		if err := updateManagedRuntimeLeaseMetadata(ctx, r.cfg.RuntimeActions.LeaseManager, leaseTenantID, leaseID, prep.managedTarget, r.runtimeMetrics); err != nil {
			r.e2eProof["lease_metadata_warning"] = err.Error()
			r.recordPhase(RuntimePhaseDeployed)
		}
	}

	ownerHandoffRequired := ownerSpecBootstrapFromPayload(jobSnapshot.Payload) != nil
	if ownerHandoffRequired {
		r.completeTypedIdentityHandoff(art.metadata)
	}

	runtimeLifecycle := copyRuntimeLifecycle(jobSnapshot.Result)
	result := map[string]interface{}{
		stackIDField:                  jobSnapshot.TargetID,
		"stack_spec_path":             art.stackSpecPath,
		"unified_path":                art.unifiedPath,
		"tofu_dir":                    art.tofuDir,
		resultStatusField:             string(RuntimePhaseDeployed),
		"runtime_phase":               string(r.finalRuntimePhase),
		"verification_status":         string(r.finalRuntimePhase),
		metadataKeyStackKitCatalogRef: art.unifiedSpec.StackKit,
		"e2e_proof":                   r.e2eProof,
		// Techstack dispatches Advanced Mode only.
		StackKitModeField: StackKitModeAdvanced,
	}
	copyRoutingDispatchReceipt(result, jobSnapshot.Payload, jobSnapshot.Result)
	if len(runtimeLifecycle) > 0 {
		result[runtimeLifecycleResultKey] = runtimeLifecycle
	}
	if len(r.runtimeProof) > 0 {
		result["runtime_proof"] = r.runtimeProof
		if proof, exists := r.runtimeProof["target_bootstrap"]; exists {
			result["target_bootstrap"] = proof
		}
	}
	for _, key := range []string{autoDeployGuardWaitStartedAtField, "auto_deploy_guard_verified_at", "managed_runtime_target_verified_at", "guard_admission_reason", "guard_admission_elapsed_ms", "guard_admission_phase", "guard_admission_deadline_at"} {
		if value, exists := jobSnapshot.Result[key]; exists {
			result[key] = value
		}
	}
	if len(r.stackKitOutputs) > 0 {
		result[metadataKeyStackKitOutputs] = r.stackKitOutputs
	}
	if len(r.runtimeMetrics) > 0 {
		result["runtime_metrics"] = stringInterfaceMap(r.runtimeMetrics)
	}
	copyDeployArtifactMetadataToResult(result, art.metadata)
	// A managed auto-deploy starts inside ProvisionHandler, but an enrollment
	// wait yields back to the queue after prepareDeployPayload has changed the
	// job type to deploy. The resumed attempt therefore enters DeployHandler
	// directly and cannot reach ProvisionHandler's post-deploy defaults merge.
	// Preserve the prepared lease, requirements, registration, and billing
	// fields here as defaults so the queued-resume path produces the same final
	// result as an uninterrupted auto-deploy.
	for key, value := range jobSnapshot.Result {
		if _, exists := result[key]; !exists {
			result[key] = value
		}
	}
	r.job.replaceResult(result)
	copyManagedRuntimeTargetToJob(r.job, prep.managedTarget)

	// When the orchestrator issued an owner-spec bootstrap token, StackKit
	// is required to call back with identity, login_gateway, and recovery
	// outputs. Silent omission means the user has no way to log into the
	// freshly provisioned stack, so refuse to mark the deploy "completed".
	if ownerHandoffRequired {
		missing := missingStackKitIdentityHandoffFields(r.stackKitOutputs)
		// The typed v2 lifecycle has no recovery bundle: StackKits' Owner
		// bootstrap is not part of its production commands, so there is no
		// recovery reference for Techstack to report. Only the Owner login and
		// login gateway are required.
		missing = slices.DeleteFunc(missing, func(field string) bool { return field == identityHandoffRecoveryField })
		if len(missing) > 0 {
			r.job.setStep(StepVerifyRollout)
			return wrapProvisionError(
				StepVerifyRollout,
				"StackKit did not return the required identity handoff",
				"The runtime action response is missing: "+strings.Join(missing, ", ")+
					". Without these fields the user has no owner login, no login gateway, and no recovery reference - the rollout is not usable.",
			)
		}
	}

	if observation := freshRuntimeObservation(r.stackKitOutputs, time.Now().UTC()); len(observation) > 0 {
		serviceCount := 0
		if services, ok := observation["services"].([]interface{}); ok {
			serviceCount = len(services)
		}
		completeRuntimeLifecyclePhase(r.job, runtimePhaseEnrollment, "Guard enrollment is backed by a fresh runtime observation", nil)
		completeRuntimeLifecyclePhase(r.job, runtimePhaseInventoryDelta, "Runtime inventory observation is persisted", map[string]interface{}{"service_count": serviceCount})
		completeRuntimeLifecyclePhase(r.job, runtimePhaseServiceInventory, "Measured service inventory is available", map[string]interface{}{"service_count": serviceCount})
		completeRuntimeLifecyclePhase(r.job, runtimePhaseMonitoringReady, "Fresh runtime evidence is available for monitoring", map[string]interface{}{
			"observed_at": resultString(observation, "observed_at"),
		})
	} else {
		startRuntimeLifecyclePhase(r.job, runtimePhaseMonitoringReady, "Waiting for a fresh StackKit runtime observation")
	}

	r.q.UpdateProgress(r.job.ID, 100, "Rollout complete")
	return nil
}

// completeTypedIdentityHandoff fills the Owner identity handoff of a typed
// StackKits rollout from facts Techstack owns. The stackkit.apply-result/v2
// data carries no identity outputs, so the Owner login is the Owner email
// Techstack sent to StackKits, and the login gateway is the managed address
// Techstack registered for the StackKit's login service. Fields StackKits
// did return are kept; fields Techstack cannot derive stay missing.
func (r *deployRollout) completeTypedIdentityHandoff(metadata map[string]string) {
	for _, field := range missingStackKitIdentityHandoffFields(r.stackKitOutputs) {
		switch field {
		case identityHandoffOwnerField:
			email := stackOwnerEmail(r.job, r.actionReq.OwnerID)
			if email == "" {
				continue
			}
			identity := mapFromInterface(r.stackKitOutputs["identity"])
			if identity == nil {
				identity = map[string]interface{}{}
			}
			owner := mapFromInterface(identity["owner"])
			if owner == nil {
				owner = map[string]interface{}{}
			}
			owner["username"] = email
			if stringFromInterface(owner["email"]) == "" {
				owner["email"] = email
			}
			identity["owner"] = owner
			r.stackKitOutputs["identity"] = identity
		case identityHandoffLoginField:
			host := strings.TrimSpace(metadata[metadataKeyKombifyMeLoginHost])
			if host == "" {
				continue
			}
			r.stackKitOutputs["login_gateway"] = map[string]interface{}{
				"url":   "https://" + host,
				"label": "Open first login",
			}
		}
	}
}
