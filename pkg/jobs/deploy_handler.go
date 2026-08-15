package jobs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/kombifyio/techstack/internal/portinventory"
	"github.com/kombifyio/techstack/internal/runtimeproduct/runtimeaction"
	"github.com/kombifyio/techstack/internal/stackkitrelease"
	"github.com/kombifyio/techstack/pkg/api/agentpb"
	"github.com/kombifyio/techstack/pkg/core"
	"github.com/kombifyio/techstack/pkg/runtimeidentity"
	"github.com/kombifyio/techstack/pkg/secrets"
	"github.com/kombifyio/techstack/pkg/unifier"
	"github.com/kombifyio/techstack/pkg/workerauth"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
)

var stackKitsCoolifyBootstrapRetryDelays = []time.Duration{
	10 * time.Second,
	30 * time.Second,
	30 * time.Second,
	30 * time.Second,
	30 * time.Second,
}

var stackKitsRestoreReadinessRetryDelays = []time.Duration{
	30 * time.Second,
	45 * time.Second,
	60 * time.Second,
}

var runtimeDiagnosticsSlowProbeAfter = 90 * time.Second

func runtimeActionExecutionTimeout(cfg *ProvisionConfig) time.Duration {
	timeout := runtimeActionHTTPTimeout
	if cfg != nil && cfg.RuntimeActionTimeout > 0 {
		timeout = cfg.RuntimeActionTimeout
	}
	if timeout <= 0 || timeout > runtimeActionHTTPTimeout {
		return runtimeActionHTTPTimeout
	}
	return timeout
}

func runtimeActionTimedOutError(action string, timeout time.Duration, err error) error {
	if err == nil {
		err = context.DeadlineExceeded
	}
	return fmt.Errorf("runtime action %s timed out after %s: %w", action, timeout, err)
}

// DeployHandler creates a job handler for "rollout" / deployment.
// It requires that requirements-spec.yaml exists and that worker requirements
// are satisfied. It then asks StackKits to generate rollout artifacts and
// delegates apply/verify/restore to StackKits runtime actions.
//
// The closure orchestrates these phases (each in its own helper below):
// prepare -> validate workers -> generate artifacts -> simulation gate ->
// rollout -> verify -> restore drill -> finalize.
//
//nolint:goconst // Rollout proof/result keys are persisted API/UI wire fields.
func DeployHandler(cfg *ProvisionConfig) JobHandler {
	cfg = normalizeProvisionConfig(cfg)

	return func(ctx context.Context, job *Job, q *Queue) (handlerErr error) {
		defer func() {
			if isJobWaitError(handlerErr) {
				return
			}
			if reconcileErr := reconcileRoutingRolloutOutcome(ctx, cfg, job, handlerErr); reconcileErr != nil && handlerErr == nil {
				handlerErr = wrapProvisionError(StepFinalize, fmt.Sprintf("finish routing rollout: %v", reconcileErr),
					"The runtime rollout finished, but its routing result could not be persisted. Retry reconciliation before changing the route.")
			}
		}()
		defer func() {
			if handlerErr != nil && !isJobWaitError(handlerErr) {
				failCurrentRuntimeLifecyclePhase(job)
			}
		}()
		prep, err := deployPrepare(ctx, cfg, job, q)
		if err != nil {
			return err
		}

		workers, err := deployValidateWorkers(job, q, prep)
		if err != nil {
			return err
		}
		if len(workers) > 0 {
			completeRuntimeLifecyclePhase(job, runtimePhaseReachability, "Authenticated worker connection is available", map[string]interface{}{"worker_count": len(workers)})
			completeRuntimeLifecyclePhase(job, runtimePhaseEnrollment, "Authenticated worker enrollment is available", map[string]interface{}{"worker_count": len(workers)})
			completeRuntimeLifecyclePhase(job, runtimePhaseInventoryDelta, "Worker inventory satisfies the rollout input", map[string]interface{}{"worker_count": len(workers)})
		} else {
			startRuntimeLifecyclePhase(job, runtimePhaseReachability, "Waiting for the managed runtime target")
			startRuntimeLifecyclePhase(job, runtimePhaseEnrollment, "Preparing authenticated Guard enrollment")
		}

		startRuntimeLifecyclePhase(job, runtimePhaseGenerate, "Generating pinned StackKit rollout artifacts")
		art, err := deployGenerateArtifacts(ctx, cfg, job, q, prep, workers)
		if err != nil {
			return err
		}
		completeRuntimeLifecyclePhase(job, runtimePhaseGenerate, "Pinned StackKit rollout artifacts are ready", map[string]interface{}{"stackkit": art.unifiedSpec.StackKit})

		rollout, rolloutErr := newDeployRollout(cfg, job, q, prep, art)
		if rolloutErr != nil {
			var identityMismatch *RuntimeServerIdentityMismatchError
			if errors.As(rolloutErr, &identityMismatch) {
				job.setStep(StepPortAdmission)
				job.mutateResult(func(result map[string]interface{}) {
					result["port_admission_error"] = map[string]interface{}{
						"error_code": "runtime_server_identity_mismatch", "reason_code": "canonical_runtime_server_mismatch",
						"retryable": false,
						"user_guidance": map[string]interface{}{
							"title": "Refresh the runtime target",
							"body":  "The selected worker no longer matches the requested RuntimeServer. Refresh server inventory and retry before applying the StackKit.",
						},
					}
				})
				return wrapProvisionError(StepPortAdmission, rolloutErr.Error(),
					"The selected worker no longer matches the requested RuntimeServer. No port admission or host mutation was started.")
			}
			return wrapProvisionError(StepTelemetryHandshake, rolloutErr.Error(), "Managed runtime enrollment could not be securely initialized. Configure the worker agent signing secret and retry before any runtime action.")
		}
		applied, err := rollout.runPortGovernedLifecycle(ctx)
		if err != nil {
			return err
		}
		if !applied {
			return nil
		}
		startRuntimeLifecyclePhase(job, runtimePhaseServiceInventory, "Waiting for measured service inventory")
		if err := rollout.runRestoreDrill(ctx); err != nil {
			return err
		}

		return rollout.finalize(ctx, prep, art)
	}
}

// deployPreparation carries the persisted specs and managed-runtime context
// resolved during the prepare-rollout phase.
type deployPreparation struct {
	persister      *unifier.SpecPersister
	kombSpec       *core.KombinationSpec
	reqSpec        *core.RequirementsSpec
	managedTarget  *ManagedRuntimeTarget
	managedRuntime bool
	stackSpecPath  string
}

// deployPrepare loads the persisted intent + requirements, validates they are
// current, and (for managed cloud stacks) resolves and hydrates the runtime
// target into the spec and StackKits handoff spec.
func deployPrepare(ctx context.Context, cfg *ProvisionConfig, job *Job, q *Queue) (*deployPreparation, error) {
	job.setStep(StepPrepareRollout)
	q.UpdateProgress(job.ID, 5, "Preparing rollout...")
	breadcrumbStep(ctx, StepPrepareRollout, "loading persisted intent + requirements", nil)

	persister, err := newSpecPersister(cfg, job.TargetID)
	if err != nil {
		return nil, wrapProvisionError(StepPrepareRollout, fmt.Sprintf("failed to init persister: %v", err),
			"Could not initialize spec persistence.")
	}

	if !persister.IntentExists() {
		return nil, wrapProvisionError(StepPrepareRollout, "missing intent", "kombination.yaml not found for this stack; run the preparation step first")
	}
	if !persister.RequirementsSpecExists() {
		return nil, wrapProvisionError(StepPrepareRollout, "missing requirements", "requirements-spec.yaml not found for this stack; run the preparation step first")
	}

	intentBytes, err := persister.LoadIntentBytes()
	if err != nil {
		return nil, wrapProvisionError(StepPrepareRollout, fmt.Sprintf("failed to load intent: %v", err),
			"Could not load persisted configuration.")
	}

	reqSpec, err := persister.LoadRequirementsSpec()
	if err != nil {
		return nil, wrapProvisionError(StepPrepareRollout, fmt.Sprintf("failed to load requirements: %v", err),
			"Could not load persisted requirements.")
	}
	stackSpecPath := ""

	// Ensure requirements are up-to-date with the current intent.
	currentIntentHash := unifier.ComputeDataHash(intentBytes)
	if reqSpec.Metadata.SourceHash != "" && reqSpec.Metadata.SourceHash != currentIntentHash {
		return nil, wrapProvisionError(StepPrepareRollout,
			"requirements are out of date",
			"requirements-spec.yaml does not match current kombination.yaml; rerun preparation to regenerate requirements")
	}

	loader := unifier.NewLoader()
	kombSpec, err := loader.LoadBytes(intentBytes)
	if err != nil {
		return nil, wrapProvisionError(StepGenerateUnified, fmt.Sprintf("failed to parse intent: %v", err),
			"kombination.yaml could not be parsed.")
	}
	// The StackKits CLI handoff spec lives on the instance's local disk, and
	// that disk does not survive a deploy. Any stack older than the running
	// container therefore lost it, and the rollout died at artifact generation
	// with "StackKits CLI artifact generation requires persisted
	// stack-spec.yaml" -- permanently, because nothing regenerated it. Observed
	// live on 2026-07-27 against a stack created the previous day.
	//
	// The spec is a pure projection of the persisted intent, which this function
	// has already loaded, so it can simply be rebuilt.
	if restoreErr := restoreStackSpecFromIntent(persister, intentBytes); restoreErr != nil {
		return nil, wrapProvisionError(StepPrepareRollout, restoreErr.Error(),
			"Could not rebuild the StackKits handoff spec from your saved configuration.")
	}
	stackSpecPath, err = deployApplyRoutingOverlay(ctx, cfg, job, persister, kombSpec)
	if err != nil {
		return nil, err
	}
	managedRuntime := isManagedCloudSpec(kombSpec)
	var managedTarget *ManagedRuntimeTarget
	if managedRuntime {
		var hydratedPath string
		managedTarget, hydratedPath, err = deployResolveManagedTarget(ctx, cfg, job, q, persister, kombSpec)
		if err != nil {
			return nil, err
		}
		if hydratedPath != "" {
			stackSpecPath = hydratedPath
		}
	}

	return &deployPreparation{
		persister:      persister,
		kombSpec:       kombSpec,
		reqSpec:        reqSpec,
		managedTarget:  managedTarget,
		managedRuntime: managedRuntime,
		stackSpecPath:  stackSpecPath,
	}, nil
}

// deployResolveManagedTarget resolves the managed VM lease runtime target,
// hydrates it into the spec + StackKits handoff spec, and returns the (possibly
// updated) handoff stack-spec path.
func deployResolveManagedTarget(ctx context.Context, cfg *ProvisionConfig, job *Job, q *Queue, persister *unifier.SpecPersister, kombSpec *core.KombinationSpec) (*ManagedRuntimeTarget, string, error) {
	managedTarget, err := resolveManagedRuntimeTargetWithWait(ctx, cfg, job, kombSpec, q)
	if err != nil {
		if isJobWaitError(err) {
			return nil, "", err
		}
		captureJobError(ctx, err, map[string]interface{}{
			stepField:     StepPrepareRollout,
			stackIDField:  job.TargetID,
			leaseIDField:  stringFromMap(job.Result, leaseIDField),
			tenantIDField: managedRuntimeTenantID(job, kombSpec),
		})
		return nil, "", wrapProvisionError(StepPrepareRollout, err.Error(),
			"Managed Runtime rollout requires an SSH host or public IP from the VM lease before StackKits can generate and apply the rollout.")
	}
	hydrateManagedRuntimeSpec(kombSpec, managedTarget)
	copyManagedRuntimeTargetToJob(job, managedTarget)
	hydratedStackSpecPath, hydratedStackSpecHash, hydrateErr := hydratePersistedStackSpecTarget(persister, managedTarget)
	if hydrateErr != nil {
		return nil, "", wrapProvisionError(StepPrepareRollout, hydrateErr.Error(),
			"Could not update the StackKits handoff spec with the managed runtime target.")
	}
	stackSpecPath := ""
	if hydratedStackSpecPath != "" {
		stackSpecPath = hydratedStackSpecPath
		job.mutateResult(func(result map[string]interface{}) {
			result["stack_spec_path"] = hydratedStackSpecPath
			result["stack_spec_sha256"] = hydratedStackSpecHash
		})
	}
	job.setStep(StepRuntimeConnected)
	q.UpdateProgress(job.ID, 12, "Resolved managed VM lease target")
	breadcrumbStep(ctx, StepPrepareRollout, "managed VM lease target resolved", map[string]interface{}{
		"host":     managedTarget.Host,
		"ssh_port": managedTarget.SSHPort,
		"source":   managedTarget.Source,
	})
	return managedTarget, stackSpecPath, nil
}

// deployValidateWorkers parses approved workers from the payload and enforces
// worker requirements for non-managed stacks.
func deployValidateWorkers(job *Job, q *Queue, prep *deployPreparation) ([]core.Worker, error) {
	job.setStep(StepValidateWorkers)
	q.UpdateProgress(job.ID, 15, "Checking workers...")

	workers, err := parseWorkersFromPayload(job.Payload["workers"])
	if err != nil {
		return nil, wrapProvisionError(StepValidateWorkers, fmt.Sprintf("invalid workers payload: %v", err),
			"Could not read approved workers for rollout.")
	}

	if !prep.managedRuntime {
		if ok, msg := workersSatisfyRequirements(prep.reqSpec.RequiredWorkers, workers); !ok {
			return nil, wrapProvisionError(StepValidateWorkers, "requirements not satisfied", msg)
		}
	} else if len(workers) == 0 {
		job.setStep(StepTelemetryHandshake)
		q.UpdateProgress(job.ID, 18, "Preparing runtime telemetry handoff...")
	}
	return workers, nil
}

// deployArtifacts carries the unified spec and generated rollout artifact paths.
type deployArtifacts struct {
	unifiedSpec   *core.UnifiedSpec
	unifiedPath   string
	tofuDir       string
	stackSpecPath string
	metadata      map[string]string
	platformNodes []PlatformNode
}

// deployGenerateArtifacts persists Techstack's review proposal, then delegates
// final validation, resolution, and rendering to the pinned StackKits CLI.
func deployGenerateArtifacts(ctx context.Context, cfg *ProvisionConfig, job *Job, q *Queue, prep *deployPreparation, _ []core.Worker) (*deployArtifacts, error) {
	job.setStep(StepGenerateUnified)
	q.UpdateProgress(job.ID, 30, "Preparing reviewed StackSpec proposal...")

	// Preserve the StackKit choice recorded in the reviewed requirements proposal.
	if strings.TrimSpace(prep.reqSpec.StackKit) != "" {
		prep.kombSpec.Kit = prep.reqSpec.StackKit
	}

	unifiedSpec, err := buildReviewableStackSpecProposal(prep.kombSpec)
	if err != nil {
		return nil, wrapProvisionError(
			StepGenerateUnified,
			fmt.Sprintf("failed to prepare StackSpec proposal: %v", err),
			"Could not prepare the reviewed configuration for StackKits.",
		)
	}

	job.setStep(StepPersistUnified)
	q.UpdateProgress(job.ID, 45, "Persisting StackSpec review proposal...")

	proposalPath, err := prep.persister.SaveUnifiedSpec(unifiedSpec, prep.persister.GetRequirementsSpecPath())
	if err != nil {
		return nil, wrapProvisionError(StepPersistUnified, fmt.Sprintf("failed to persist StackSpec proposal: %v", err),
			"Could not persist the reviewed StackSpec proposal.")
	}

	tofuDir := prep.persister.GetTofuDir()
	if err := os.MkdirAll(tofuDir, 0755); err != nil {
		return nil, wrapProvisionError(StepGenerateIaC, fmt.Sprintf("failed to create tofu dir: %v", err),
			"Could not prepare workspace directory.")
	}

	job.setStep(StepGenerateIaC)
	q.UpdateProgress(job.ID, 60, "Generating StackKits rollout artifacts...")

	stackSpecPath := prep.stackSpecPath
	if stackSpecPath == "" && prep.persister.StackSpecExists() {
		stackSpecPath = prep.persister.GetStackSpecPath()
	}
	platformNodes := runtimeActionPlatformNodesFromPayload(job.Payload)
	if len(platformNodes) > 0 {
		if hydratedPath, hydratedHash, hydrateErr := hydratePersistedStackSpecPlatformNodes(prep.persister, platformNodes); hydrateErr != nil {
			return nil, wrapProvisionError(StepGenerateIaC, hydrateErr.Error(),
				"Could not update the StackKits handoff spec with supplemental node targets.")
		} else if hydratedPath != "" {
			stackSpecPath = hydratedPath
			job.mutateResult(func(result map[string]interface{}) {
				result["stack_spec_path"] = hydratedPath
				result["stack_spec_sha256"] = hydratedHash
			})
		}
	}
	artifactMetadata := map[string]string{}
	artifactResult, err := generateStackKitArtifacts(ctx, cfg, job, unifiedSpec, stackSpecPath, tofuDir)
	if err != nil {
		return nil, wrapProvisionError(StepGenerateIaC, fmt.Sprintf("StackKits artifact generation failed: %v", err),
			"Could not generate StackKits rollout artifacts.")
	}
	for key, value := range artifactResultMetadata(artifactResult) {
		if _, exists := artifactMetadata[key]; !exists {
			artifactMetadata[key] = value
		}
	}
	if resolvedKit := strings.TrimSpace(artifactMetadata["stackkit"]); resolvedKit != "" {
		unifiedSpec.StackKit = resolvedKit
	}
	if artifactResult != nil && strings.TrimSpace(artifactResult.OutputDir) != "" {
		tofuDir = artifactResult.OutputDir
	}
	unifiedPath := strings.TrimSpace(artifactResult.ResolvedPlanPath)
	if unifiedPath == "" {
		if cfg.RuntimeActions.StackKitGenerator == nil {
			return nil, wrapProvisionError(
				StepGenerateIaC,
				"pinned StackKits CLI returned no canonical ResolvedPlan",
				"StackKits did not return its authoritative resolved plan.",
			)
		}
		// Explicitly injected generators exist only as bounded test seams.
		unifiedPath = proposalPath
	}
	artifactMetadata["stack_spec_proposal_path"] = proposalPath
	artifactMetadata["resolved_plan_path"] = unifiedPath
	if restored, restoreErr := restoreLegacyStackKitsRetryLocalArtifacts(cfg != nil && cfg.StackKitCommander != nil, tofuDir); restoreErr != nil {
		return nil, wrapProvisionError(StepGenerateIaC, fmt.Sprintf("failed to restore StackKits local artifacts: %v", restoreErr),
			"Could not prepare required StackKits local file artifacts.")
	} else if len(restored) > 0 {
		artifactMetadata["stackkits_local_artifacts_restored"] = strings.Join(restored, ",")
		q.addLog(job, "info", fmt.Sprintf("Prepared missing StackKits local artifacts: %s", strings.Join(restored, ", ")))
	}

	return &deployArtifacts{
		unifiedSpec:   unifiedSpec,
		unifiedPath:   unifiedPath,
		tofuDir:       tofuDir,
		stackSpecPath: stackSpecPath,
		metadata:      artifactMetadata,
		platformNodes: platformNodes,
	}, nil
}

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

	e2eProof            map[string]any
	runtimeProof        map[string]interface{}
	stackKitOutputs     map[string]interface{}
	runtimeMetrics      map[string]string
	finalRuntimePhase   RuntimePhase
	portGeneration      *portinventory.GenerationRef
	generatedPlanHash   string
	portMutationStarted bool
	portActivated       bool
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
		Mode:                deployRuntimeActionMode(job, prep),
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
		e2eProof:          e2eProof,
		runtimeProof:      map[string]interface{}{},
		stackKitOutputs:   map[string]interface{}{},
		runtimeMetrics:    map[string]string{},
		finalRuntimePhase: RuntimePhaseDeployed,
	}, nil
}

// deployRuntimeActionMode returns the StackKits install mode announced with
// the runtime action. One mode value must hold across spec, stackkit prepare,
// and the runtime action: the previous managed hardcode to "advanced"
// split-brained the rollout — prepare ran the spec's bootstrapped path while
// the action claimed advanced, an install mode that is still scaffolding in
// the StackKits mode matrix (2026-07-08 review, hybrid decision). Managed
// rollouts therefore follow the spec and normalize onto the canonical
// install-mode vocabulary, defaulting to bootstrapped until Advanced
// graduates with a live E2E cell.
func deployRuntimeActionMode(job *Job, prep *deployPreparation) string {
	mode := ""
	if prep != nil && prep.kombSpec != nil {
		mode = metadataString(prep.kombSpec, "mode")
	}
	if mode == "" && job != nil {
		mode = payloadString(job.Payload, "mode")
	}
	if prep != nil && prep.managedRuntime {
		return normalizeManagedInstallMode(mode)
	}
	return mode
}

// normalizeManagedInstallMode folds legacy aliases and non-install-mode
// values (e.g. wizard modes like "easy"/"techie") onto the canonical
// StackKits install-mode vocabulary for managed rollouts.
func normalizeManagedInstallMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "bare":
		return "bare"
	case "advanced", "terramate", "advanced-terramate":
		return "advanced"
	default:
		return "bootstrapped"
	}
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
	controlPath := "/api/v1/workers/" + runtimeAgentID + "/control/ws"
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
		ControlURLs: []string{
			absoluteTechStackURL(websocketTechStackServerURL(serverURL), controlPath),
		},
		ChannelBootstrap: map[string]any{
			"tenant_id":         tenantID,
			"owner_id":          ownerID,
			"stack_id":          stackID,
			"lease_id":          leaseID,
			"server_id":         serverID,
			"runtime_agent_id":  runtimeAgentID,
			"heartbeat_url":     absoluteTechStackURL(serverURL, heartbeatPath),
			"inventory_url":     absoluteTechStackURL(serverURL, inventoryPath),
			"websocket_url":     absoluteTechStackURL(websocketTechStackServerURL(serverURL), controlPath),
			"grpc_hint":         "mtls-if-http2-network-allows",
			"ssh_liveness_role": "bootstrap-diagnostics-only",
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
		os.Getenv("TECHSTACK_PUBLIC_ORIGIN"),
		os.Getenv("TECHSTACK_PUBLIC_URL"),
		os.Getenv("TECHSTACK_SERVER_URL"),
		os.Getenv("RENDER_EXTERNAL_URL"),
	), "/")
}

func websocketTechStackServerURL(serverURL string) string {
	switch {
	case strings.HasPrefix(serverURL, "https://"):
		return "wss://" + strings.TrimPrefix(serverURL, "https://")
	case strings.HasPrefix(serverURL, "http://"):
		return "ws://" + strings.TrimPrefix(serverURL, "http://")
	default:
		return serverURL
	}
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
		return wrapProvisionError(StepSimulationGate, fmt.Sprintf("simulation gate failed: %v", err),
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
			if _, legacyHTTP := r.cfg.RuntimeActions.RolloutRunner.(*HTTPRuntimeActionRunner); legacyHTTP {
				return wrapProvisionError(StepRolloutRunner,
					"legacy HTTP StackKits rollout cannot bind the admitted ResolvedPlan hash",
					"Deploy a typed StackKits agent that advertises expected-plan-hash admission before applying this rollout.")
			}
		}
		if r.cfg.StackKitCommander == nil && r.cfg.RuntimeActions.RolloutRunner == nil {
			r.updateJobStepProgress(StepRolloutRunner, 75, "Running StackKits rollout...")
			return wrapProvisionError(StepRolloutRunner,
				"StackKits rollout runner is not configured",
				"TechStack no longer runs OpenTofu directly for StackKit rollouts. Configure a StackKits rollout runner before starting deploy.")
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
			return wrapProvisionError(StepPortAdmission, fmt.Sprintf("runtime listener mutation fence failed: %v", err),
				"Techstack could not bind this rollout to its reserved host listeners. No host mutation was started.")
		}
		if err := r.bootstrapManagedRuntimeTarget(ctx); err != nil {
			captureJobError(ctx, err, map[string]interface{}{stepField: StepStackKitPrepare, stackIDField: r.job.TargetID, "stack_kit": r.unifiedSpec.StackKit})
			return wrapProvisionError(StepStackKitPrepare, fmt.Sprintf("Managed runtime target bootstrap failed: %v", err),
				"TechStack could not prepare the managed VPS for the StackKits rollout.")
		}
		if r.managedRuntime {
			completeRuntimeLifecyclePhase(r.job, runtimePhaseReachability, "Managed runtime target passed the StackKit bootstrap boundary", nil)
		}
		r.updateJobStepProgress(StepRolloutRunner, 82, "Applying StackKit services on the managed VPS...")
		var result map[string]interface{}
		var err error
		if r.cfg.StackKitCommander != nil {
			result, err = r.runTypedStackKitApply(ctx)
		} else {
			result, err = r.runStackKitsRolloutWithBoundedRetry(ctx)
		}
		if err != nil {
			captureJobError(ctx, err, map[string]interface{}{stepField: StepRolloutRunner, stackIDField: r.job.TargetID, "stack_kit": r.unifiedSpec.StackKit})
			return wrapProvisionError(StepRolloutRunner, fmt.Sprintf("StackKits rollout failed: %v", err),
				"StackKits could not apply the selected StackKit rollout.")
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

func (r *deployRollout) runTypedStackKitApply(ctx context.Context) (map[string]interface{}, error) {
	if r == nil || r.cfg == nil || r.cfg.StackKitCommander == nil || r.actionReq.TechStackEnrollment == nil {
		return nil, fmt.Errorf("typed StackKits deploy dispatcher is not configured")
	}
	release, err := configuredPinnedStackKitRelease()
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
	stackName, domain, _, err := stackKitInitFacts(canonical.Path)
	if err != nil {
		return nil, err
	}
	baseRequest := StackKitLifecycleRequest{
		StackID:          r.job.TargetID,
		TenantID:         r.actionReq.TenantID,
		OwnerID:          r.actionReq.OwnerID,
		AgentID:          r.actionReq.TechStackEnrollment.RuntimeAgentID,
		OwnerApproved:    true,
		WorkingDirectory: workingDirectory,
		SpecPath:         filepath.Base(canonical.Path),
		StackKit:         r.unifiedSpec.StackKit,
		StackName:        stackName,
		Domain:           domain,
		// A fresh managed workspace has no current intent to compare-and-swap.
		// StackKits creates it atomically without this field and treats an exact
		// retry as already applied; a differing existing intent still fails
		// closed because replacement requires its current normalized hash.
		ExpectedSpecHash: "",
	}
	if strings.EqualFold(strings.TrimSpace(r.unifiedSpec.StackKit), "cloud-kit") {
		resolvedPlan, readErr := readManagedResolvedPlan(r.actionReq.UnifiedPath)
		if readErr != nil {
			return nil, readErr
		}
		receipt := release.Receipt()
		inventory, buildErr := r.buildManagedStackKitInventory(ctx, resolvedPlan, receipt.Version, "sha256:"+receipt.ArchiveSHA256)
		if buildErr != nil {
			return nil, buildErr
		}
		baseRequest.InventoryJSON = inventory
	}
	started := time.Now()
	result, err := runTypedStackKitApplySequence(ctx, r.cfg.StackKitCommander, r.job.ID, baseRequest, *release)
	if err != nil {
		var operationErr *typedStackKitOperationError
		if errors.As(err, &operationErr) && operationErr.TimedOut() {
			reason := "typed_stackkit_" + operationErr.Operation + "_timeout"
			r.job.mutateResult(func(jobResult map[string]interface{}) {
				jobResult["typed_stackkit_rollout"] = map[string]interface{}{
					"status":      "failed",
					"reason_code": reason,
					"retryable":   true,
					"operation":   operationErr.Operation,
					"command_id":  operationErr.CommandID,
					"release":     release.Receipt().Version,
					"elapsed_ms":  time.Since(started).Milliseconds(),
				}
			})
			r.collectRuntimeDiagnostics(ctx, nil, r.actionReq, "stackkit_rollout", reason, time.Since(started), err)
		}
	}
	return result, err
}

const managedBackupTargetInventoryResultField = "managed_backup_target_inventory"

// buildManagedStackKitInventory always binds a managed Cloud rollout to the
// enrolled Operations process. A cloud ResolvedPlan with no backup target
// requirement must not touch S3 or demand the managed backup authority, but it
// still needs a channel-only Inventory for remote owners such as Public TLS.
//
// Conversely, once the plan explicitly contains a backup target requirement,
// the generated binding is security authority for that selected capability.
// Its custody/attestation failures deliberately remain fail-closed; this
// helper never invents a binding or silently substitutes an older receipt.
func (r *deployRollout) buildManagedStackKitInventory(
	ctx context.Context,
	resolvedPlan []byte,
	stackKitsVersion string,
	candidateDigest string,
) ([]byte, error) {
	required, err := managedBackupTargetInventoryRequired(resolvedPlan)
	if err != nil {
		r.recordManagedBackupTargetInventory("failed", "backup_target_requirements_invalid", false)
		return nil, fmt.Errorf("inspect managed Cloud backup target requirements: %w", err)
	}
	if !required {
		r.recordManagedBackupTargetInventory("not_applicable", "optional_backup_not_selected", false)
	}
	if r == nil || r.cfg == nil || r.job == nil || r.cfg.ManagedStackKitInventory == nil {
		if required {
			r.recordManagedBackupTargetInventory("failed", "managed_backup_inventory_unavailable", false)
			return nil, fmt.Errorf("managed Cloud StackKits Inventory authority is not configured for the selected backup capability")
		}
		return nil, fmt.Errorf("managed Cloud StackKits Operations channel Inventory authority is not configured")
	}
	inventory, err := r.cfg.ManagedStackKitInventory.Build(ctx, ManagedStackKitInventoryRequest{
		TenantID: r.actionReq.TenantID, StackID: r.job.TargetID, ResolvedPlan: resolvedPlan,
		StackKitsVersion: stackKitsVersion, CandidateDigest: candidateDigest, ValidFor: 30 * time.Minute,
	})
	if err != nil {
		if !required {
			return nil, fmt.Errorf("build managed Cloud StackKits Operations channel Inventory: %w", err)
		}
		r.recordManagedBackupTargetInventory("failed", managedBackupTargetInventoryFailureReason(err), managedBackupTargetInventoryRetryable(err))
		return nil, fmt.Errorf("build managed Cloud StackKits Inventory: %w", err)
	}
	if required {
		r.recordManagedBackupTargetInventory("ready", "", false)
	}
	return inventory, nil
}

func managedBackupTargetInventoryRequired(resolvedPlan []byte) (bool, error) {
	if len(resolvedPlan) == 0 || len(resolvedPlan) > 32<<20 {
		return false, errors.New("managed Cloud ResolvedPlan must be a bounded document")
	}
	var plan map[string]interface{}
	if err := json.Unmarshal(resolvedPlan, &plan); err != nil || plan == nil {
		if err != nil {
			return false, fmt.Errorf("decode managed Cloud ResolvedPlan: %w", err)
		}
		return false, errors.New("managed Cloud ResolvedPlan must be an object")
	}
	requirements, exists := plan["backupTargetRequirements"]
	if !exists || requirements == nil {
		return false, nil
	}
	sites, ok := requirements.(map[string]interface{})
	if !ok {
		return false, errors.New("managed Cloud backup target requirements must be an object")
	}
	return len(sites) != 0, nil
}

func (r *deployRollout) recordManagedBackupTargetInventory(status, reasonCode string, retryable bool) {
	if r == nil || r.job == nil {
		return
	}
	r.job.mutateResult(func(result map[string]interface{}) {
		result[managedBackupTargetInventoryResultField] = map[string]interface{}{
			"status": status, "reason_code": reasonCode, "retryable": retryable,
		}
	})
}

func managedBackupTargetInventoryFailureReason(err error) string {
	message := strings.ToLower(fmt.Sprint(err))
	if strings.Contains(message, "putobject") || strings.Contains(message, "attest managed backup target") {
		return "backup_target_attestation_failed"
	}
	return "managed_backup_inventory_unavailable"
}

func managedBackupTargetInventoryRetryable(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return true
	}
	message := strings.ToLower(fmt.Sprint(err))
	return strings.Contains(message, "timeout") || strings.Contains(message, "temporarily unavailable") || strings.Contains(message, "connection refused")
}

func readManagedResolvedPlan(path string) ([]byte, error) {
	const maxBytes = 32 << 20
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, errors.New("managed Cloud StackKits Inventory requires the canonical ResolvedPlan")
	}
	file, err := os.Open(path) // #nosec G304 -- path is the pinned generator's canonical ResolvedPlan output.
	if err != nil {
		return nil, fmt.Errorf("open managed Cloud ResolvedPlan: %w", err)
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maxBytes {
		return nil, errors.New("managed Cloud ResolvedPlan must be a non-empty bounded regular file")
	}
	data, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil || len(data) == 0 || len(data) > maxBytes {
		return nil, errors.New("read managed Cloud ResolvedPlan within the closed limit")
	}
	return data, nil
}

// runTypedStackKitApplySequence preserves the StackKits lifecycle contract on
// the managed node. Init persists the canonical intent, generate resolves it
// and transactionally renders the authoritative ResolvedPlan and artifacts,
// plan inspects that exact generation, and only then may apply mutate the host.
// Control-plane artifacts live in a different filesystem and cannot substitute
// for this node-local generation boundary.
func runTypedStackKitApplySequence(ctx context.Context, sender StackKitCommandSender, jobID string, baseRequest StackKitLifecycleRequest, release stackkitrelease.Release) (map[string]interface{}, error) {
	return runTypedStackKitApplySequenceWithBudget(ctx, sender, jobID, baseRequest, release, typedStackKitSequenceTimeout)
}

func runTypedStackKitApplySequenceWithBudget(ctx context.Context, sender StackKitCommandSender, jobID string, baseRequest StackKitLifecycleRequest, release stackkitrelease.Release, sequenceBudget time.Duration) (map[string]interface{}, error) {
	if sequenceBudget <= 0 || sequenceBudget > typedStackKitSequenceTimeout {
		sequenceBudget = typedStackKitSequenceTimeout
	}
	sequenceCtx, cancel := context.WithTimeout(ctx, sequenceBudget)
	defer cancel()
	nodePlanHash := ""
	for _, operation := range []string{StackKitLifecycleInit, StackKitLifecycleGenerate, StackKitLifecyclePlan} {
		request := baseRequest
		request.Operation = operation
		request, err := NormalizeStackKitLifecycleRequest(request)
		if err != nil {
			return nil, err
		}
		command, err := stackKitLifecycleCommand(jobID+"-"+operation, request, release)
		if err != nil {
			return nil, err
		}
		if err := bindManagedStackKitCommandBudget(sequenceCtx, command, operation); err != nil {
			return nil, &typedStackKitOperationError{Operation: operation, CommandID: command.CommandId, Err: err}
		}
		result, err := sendStackKitCommandBoundedWithGraceForTenant(sequenceCtx, sender, request.TenantID, request.AgentID, command, managedStackKitResultGrace)
		if err != nil {
			return nil, &typedStackKitOperationError{Operation: operation, CommandID: command.CommandId, Err: err}
		}
		if result == nil {
			return nil, fmt.Errorf("typed StackKits %s result is missing", operation)
		}
		if !result.Success {
			err := fmt.Errorf("StackKits %s failed with exit code %d: %s", operation, result.ExitCode, strings.TrimSpace(result.Stderr))
			return nil, &typedStackKitOperationError{Operation: operation, CommandID: command.CommandId, Err: err, AgentTimedOut: stackKitResultTimedOut(result)}
		}
		if operation == StackKitLifecyclePlan {
			// The node-local generation is authoritative for the node-local apply.
			// Controller generation includes controller-only inventory inputs and
			// therefore cannot be used as an equality gate for this workspace.
			var err error
			nodePlanHash, err = typedStackKitPlanHash(result)
			if err != nil {
				return nil, err
			}
		}
	}

	request := baseRequest
	request.Operation = StackKitLifecycleApply
	request, err := NormalizeStackKitLifecycleRequest(request)
	if err != nil {
		return nil, err
	}
	command, err := stackKitLifecycleCommand(jobID+"-apply", request, release)
	if err != nil {
		return nil, err
	}
	command.ExpectedPlanHash = nodePlanHash
	if err := bindManagedStackKitCommandBudget(sequenceCtx, command, StackKitLifecycleApply); err != nil {
		return nil, &typedStackKitOperationError{Operation: StackKitLifecycleApply, CommandID: command.CommandId, Err: err}
	}
	result, err := sendStackKitCommandBoundedWithGraceForTenant(sequenceCtx, sender, request.TenantID, request.AgentID, command, managedStackKitResultGrace)
	if err != nil {
		return nil, &typedStackKitOperationError{Operation: StackKitLifecycleApply, CommandID: command.CommandId, Err: err}
	}
	if result == nil {
		return nil, fmt.Errorf("typed StackKits apply result is missing")
	}
	normalized, err := normalizeStackKitLifecycleResult(request, result)
	if err != nil {
		return nil, err
	}
	if !result.Success {
		err := fmt.Errorf("StackKits apply failed with exit code %d: %s", result.ExitCode, strings.TrimSpace(result.Stderr))
		return normalized, &typedStackKitOperationError{Operation: StackKitLifecycleApply, CommandID: command.CommandId, Err: err, AgentTimedOut: stackKitResultTimedOut(result)}
	}
	return normalized, nil
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
	seconds := int32(budget / time.Second)
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
}

func (e *typedStackKitOperationError) Error() string {
	return fmt.Sprintf("typed StackKits %s command %s failed: %v", e.Operation, e.CommandID, e.Err)
}

func (e *typedStackKitOperationError) Unwrap() error { return e.Err }

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
	var commandResult struct {
		SchemaVersion string `json:"schemaVersion"`
		Command       string `json:"command"`
		Status        string `json:"status"`
		Data          struct {
			Output string `json:"output"`
		} `json:"data"`
	}
	if result == nil || len(result.CommandResultJson) == 0 || json.Unmarshal(result.CommandResultJson, &commandResult) != nil {
		return "", errors.New("typed StackKits plan result is not a valid command receipt")
	}
	if commandResult.SchemaVersion != "stackkit.command-result/v1" || commandResult.Command != "stackkit plan" || commandResult.Status != "success" || strings.TrimSpace(commandResult.Data.Output) == "" {
		return "", errors.New("typed StackKits plan command receipt identity or status is invalid")
	}
	var inspection struct {
		APIVersion      string `json:"apiVersion"`
		Kind            string `json:"kind"`
		VerifiedPhase   string `json:"verifiedPhase"`
		ExecutorInvoked bool   `json:"executorInvoked"`
		Binding         struct {
			PlanHash string `json:"planHash"`
		} `json:"binding"`
		Readiness struct {
			Generation struct {
				Status   string   `json:"status"`
				Blockers []string `json:"blockers"`
			} `json:"generation"`
		} `json:"readiness"`
	}
	if err := json.Unmarshal([]byte(commandResult.Data.Output), &inspection); err != nil {
		return "", fmt.Errorf("decode typed StackKits plan inspection: %w", err)
	}
	inspection.Binding.PlanHash = strings.TrimSpace(inspection.Binding.PlanHash)
	if inspection.APIVersion != "stackkit.plan-inspection/v1" || inspection.Kind != "PlanInspection" ||
		inspection.VerifiedPhase != "generation" || inspection.ExecutorInvoked ||
		inspection.Readiness.Generation.Status != "ready" || len(inspection.Readiness.Generation.Blockers) != 0 ||
		!resolvedPlanHashPattern.MatchString(inspection.Binding.PlanHash) {
		return "", errors.New("typed StackKits node-local plan is not a ready canonical ResolvedPlan generation")
	}
	return inspection.Binding.PlanHash, nil
}

func (r *deployRollout) runTypedStackKitVerify(ctx context.Context) (map[string]interface{}, error) {
	if r == nil || r.cfg == nil || r.cfg.StackKitCommander == nil || r.actionReq.TechStackEnrollment == nil {
		return nil, fmt.Errorf("typed StackKits verify dispatcher is not configured")
	}
	release, err := configuredPinnedStackKitRelease()
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

func stackKitInitFacts(specPath string) (string, string, string, error) {
	canonical, err := os.ReadFile(filepath.Clean(specPath))
	if err != nil {
		return "", "", "", fmt.Errorf("read canonical StackSpec for init: %w", err)
	}
	document, err := decodeStackSpecDocument(canonical)
	if err != nil {
		return "", "", "", err
	}
	metadata := mapFromInterface(document["metadata"])
	name := strings.TrimSpace(stringFromInterface(metadata["name"]))
	network := mapFromInterface(document["network"])
	domainConfig := mapFromInterface(network["domain"])
	domain := strings.TrimSpace(stringFromInterface(domainConfig["base"]))
	if name == "" {
		return "", "", "", fmt.Errorf("canonical StackSpec metadata.name is required for init")
	}
	digest := sha256.Sum256(canonical)
	return name, domain, "sha256:" + hex.EncodeToString(digest[:]), nil
}

func (r *deployRollout) bootstrapManagedRuntimeTarget(ctx context.Context) error {
	if !r.managedRuntime || r.cfg == nil {
		return nil
	}
	target := normalizeRuntimeActionTarget(r.actionReq.RuntimeTarget)
	if !runtimeActionTargetHasSSHCredential(target) {
		return nil
	}
	started := time.Now()
	r.q.UpdateProgress(r.job.ID, 72, "Preparing managed VPS with StackKits CLI...")
	result, err := r.runStackKitPrepare(ctx, target)
	if err != nil {
		reason := runtimeTargetBootstrapReason(result, err)
		r.recordTargetBootstrapProof(result, reason, "failed", err)
		bundle := r.collectRuntimeDiagnostics(ctx, nil, r.actionReq, "target_bootstrap", reason, time.Since(started), err)
		if bundle != nil {
			r.q.addLog(r.job, "warn", fmt.Sprintf("Managed VPS bootstrap diagnostics %s for %s", bundle.Status, reason))
		}
		return err
	}
	if result == nil {
		return nil
	}
	r.recordTargetBootstrapProof(result, runtimeTargetBootstrapReason(result, nil), "ready", nil)
	r.e2eProof["target_bootstrap"] = result.Status
	r.q.addLog(r.job, "info", fmt.Sprintf("Managed VPS bootstrap %s: %s", firstNonEmpty(result.Status, "completed"), result.Message))
	return nil
}

func (r *deployRollout) runStackKitPrepare(ctx context.Context, target *RuntimeActionTarget) (*RuntimeTargetBootstrapResult, error) {
	prepRunner := r.cfg.RuntimeActions.StackKitPrepRunner
	if prepRunner == nil &&
		strings.TrimSpace(r.actionReq.StackSpecPath) != "" &&
		!truthyRuntimeActionEnv("TECHSTACK_STACKKIT_PREP_DISABLED") {
		release, err := configuredPinnedStackKitRelease()
		if err != nil {
			return nil, err
		}
		switch {
		case release != nil:
			prepRunner = NewPinnedStackKitCLIPrepRunner(*release)
		case stackKitCLIWorkspaceReady(r.cfg.StackKitsDir, firstNonEmpty(strings.TrimSpace(r.actionReq.StackKit), DefaultBaseKitRef)):
			prepRunner = NewStackKitCLIPrepRunner(r.cfg.StackKitsDir)
		}
	}
	prepRunner = r.stackKitPrepRunnerWithProgress(prepRunner)
	if prepRunner != nil {
		preBootstrap, bootstrapErr := r.preBootstrapManagedRuntimeTarget(ctx, target)
		if bootstrapErr != nil {
			return preBootstrap, bootstrapErr
		}
		r.updateJobStepProgress(StepStackKitPrepare, 72, "Preparing managed VPS with StackKits CLI...")
		result, err := prepRunner.PrepareStackKitRuntimeTarget(ctx, r.actionReq)
		if result != nil || err != nil {
			return preferExecutedBootstrapProof(preBootstrap, result), err
		}
	}
	if r.cfg.RuntimeActions.TargetBootstrapper == nil {
		return nil, nil
	}
	r.updateJobStepProgress(StepStackKitPrepare, 72, "Preparing managed VPS with SSH bootstrap fallback...")
	return r.cfg.RuntimeActions.TargetBootstrapper.BootstrapRuntimeTarget(ctx, target)
}

func (r *deployRollout) preBootstrapManagedRuntimeTarget(ctx context.Context, target *RuntimeActionTarget) (*RuntimeTargetBootstrapResult, error) {
	if r == nil || r.cfg == nil || r.cfg.RuntimeActions.TargetBootstrapper == nil ||
		truthyRuntimeActionEnv("TECHSTACK_STACKKIT_PREP_PREBOOTSTRAP_DISABLED") {
		return nil, nil
	}
	r.updateJobStepProgress(StepDockerReady, 74, "Preparing managed VPS Docker runtime before StackKits...")
	var result *RuntimeTargetBootstrapResult
	var err error
	if bootstrapper, ok := r.cfg.RuntimeActions.TargetBootstrapper.(interface {
		BootstrapRuntimeTargetWithProgress(context.Context, *RuntimeActionTarget, func(stackKitCLIProgressEvent)) (*RuntimeTargetBootstrapResult, error)
	}); ok {
		result, err = bootstrapper.BootstrapRuntimeTargetWithProgress(ctx, target, r.handleStackKitPrepProgress)
	} else {
		result, err = r.cfg.RuntimeActions.TargetBootstrapper.BootstrapRuntimeTarget(ctx, target)
	}
	if r.q != nil && r.job != nil && result != nil {
		r.q.addLog(r.job, "info", fmt.Sprintf("Managed VPS pre-bootstrap %s: %s", firstNonEmpty(result.Status, "completed"), result.Message))
	}
	return result, err
}

func (r *deployRollout) stackKitPrepRunnerWithProgress(prepRunner StackKitPrepRunner) StackKitPrepRunner {
	cliRunner, ok := prepRunner.(*StackKitCLIPrepRunner)
	if !ok || cliRunner == nil {
		return prepRunner
	}
	clone := *cliRunner
	clone.Progress = r.handleStackKitPrepProgress
	return &clone
}

func (r *deployRollout) handleStackKitPrepProgress(event stackKitCLIProgressEvent) {
	step, progress := stackKitPrepProgressStep(event)
	r.updateJobStepProgress(step, progress, stackKitPrepProgressMessage(event))
}

func (r *deployRollout) updateJobStepProgress(step string, progress int, message string) {
	if r == nil || r.job == nil {
		return
	}
	if strings.TrimSpace(step) != "" {
		r.job.setStep(step)
	}
	if r.q != nil {
		r.q.UpdateProgress(r.job.ID, progress, message)
	}
}

func stackKitPrepProgressStep(event stackKitCLIProgressEvent) (string, int) {
	phase := strings.ToLower(strings.TrimSpace(event.Phase))
	switch {
	case strings.Contains(phase, "docker"), phase == "apt_wait":
		return StepDockerReady, 74
	case strings.Contains(phase, "opentofu"), strings.Contains(phase, "tofu"):
		return StepOpenTofuReady, 76
	case strings.Contains(phase, "terramate"):
		return StepTerramateReady, 78
	case strings.Contains(phase, "telemetry"), strings.Contains(phase, "otel"), strings.Contains(phase, "metrics"):
		return StepTelemetryReady, 80
	case strings.Contains(phase, "target.connect"), strings.Contains(phase, "target.inspect"), strings.Contains(phase, "ssh"):
		return StepRuntimeConnected, 73
	default:
		return StepStackKitPrepare, 72
	}
}

func stackKitPrepProgressMessage(event stackKitCLIProgressEvent) string {
	message := strings.TrimSpace(event.Message)
	if message != "" {
		return message
	}
	phase := strings.TrimSpace(event.Phase)
	if phase == "" {
		return "Preparing managed VPS with StackKits CLI..."
	}
	status := strings.TrimSpace(event.Status)
	if status == "" {
		return fmt.Sprintf("StackKits prepare: %s", phase)
	}
	return fmt.Sprintf("StackKits prepare: %s %s", phase, status)
}

func runtimeTargetBootstrapReason(result *RuntimeTargetBootstrapResult, err error) string {
	if result != nil && strings.TrimSpace(result.ReasonCode) != "" {
		return strings.TrimSpace(result.ReasonCode)
	}
	return classifyRuntimeTargetBootstrapError(err, "")
}

func (r *deployRollout) recordTargetBootstrapProof(result *RuntimeTargetBootstrapResult, reason, fallbackStatus string, err error) {
	status := fallbackStatus
	message := ""
	var durationMS int64
	var attempts int
	output := ""
	if result != nil {
		status = firstNonEmpty(result.Status, status)
		message = result.Message
		durationMS = result.DurationMS
		attempts = result.Attempts
		output = result.Output
	}
	if message == "" && err != nil {
		message = err.Error()
	}
	if reason == "" {
		reason = RuntimeTargetBootstrapUnknown
	}
	proof := map[string]interface{}{
		"status":      status,
		"reason_code": reason,
		"message":     secrets.Redact(message),
		"duration_ms": durationMS,
	}
	if attempts > 0 {
		proof["attempts"] = attempts
	}
	if output != "" {
		proof["output_snippet"] = truncateRuntimeDiagnosticsOutput(secrets.Redact(output), 4096)
	}
	if result != nil && len(result.Events) > 0 {
		proof["events"] = sanitizeStackKitPrepEvents(result.Events)
	}
	if r.runtimeProof == nil {
		r.runtimeProof = map[string]interface{}{}
	}
	if r.e2eProof == nil {
		r.e2eProof = map[string]any{}
	}
	r.runtimeProof["target_bootstrap"] = proof
	r.e2eProof["target_bootstrap"] = status
	r.e2eProof["target_bootstrap_reason"] = reason

	r.job.mutateResult(func(result map[string]interface{}) {
		result["target_bootstrap"] = proof
	})
}

func sanitizeStackKitPrepEvents(events []stackKitCLIProgressEvent) []map[string]interface{} {
	out := make([]map[string]interface{}, 0, len(events))
	for _, event := range events {
		entry := map[string]interface{}{
			"phase":  strings.TrimSpace(event.Phase),
			"status": strings.TrimSpace(event.Status),
		}
		if !event.Time.IsZero() {
			entry["time"] = event.Time.Format(time.RFC3339Nano)
		}
		if message := strings.TrimSpace(event.Message); message != "" {
			entry["message"] = secrets.Redact(message)
		}
		if failureClass := strings.TrimSpace(event.FailureClass); failureClass != "" {
			entry["failure_class"] = failureClass
		}
		if len(event.Attributes) > 0 {
			attrs := make(map[string]string, len(event.Attributes))
			for key, value := range event.Attributes {
				attrs[key] = secrets.Redact(value)
			}
			entry["attributes"] = attrs
		}
		out = append(out, entry)
	}
	return out
}

func (r *deployRollout) runStackKitsRolloutWithBoundedRetry(ctx context.Context) (map[string]interface{}, error) {
	result, err := r.runRuntimeActionWithDiagnostics(ctx, r.cfg.RuntimeActions.RolloutRunner, r.actionReq, StepRolloutRunner, 84, "StackKits rollout still running; collecting target diagnostics...")
	if err == nil || !isStackKitsRolloutRetryable(err) {
		return result, err
	}

	lastErr := err
	for attempt, delay := range stackKitsCoolifyBootstrapRetryDelays {
		retryDetail := redactedStackKitsRolloutRetryError(lastErr)
		r.q.addLog(r.job, "warn", fmt.Sprintf(
			"StackKits rollout retryable failure before retry %d/%d: %s",
			attempt+1,
			len(stackKitsCoolifyBootstrapRetryDelays),
			retryDetail,
		))
		captureJobError(ctx, lastErr, map[string]interface{}{
			stepField:       StepRolloutRunner,
			stackIDField:    r.job.TargetID,
			"stack_kit":     r.unifiedSpec.StackKit,
			"retry_attempt": attempt + 1,
			"retry_total":   len(stackKitsCoolifyBootstrapRetryDelays),
			"retry_reason":  retryDetail,
		})
		r.q.UpdateProgress(
			r.job.ID,
			84,
			fmt.Sprintf("Retrying StackKits rollout after transient target-side readiness or pull failure (%d/%d)...", attempt+1, len(stackKitsCoolifyBootstrapRetryDelays)),
		)
		if delay > 0 {
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil, ctx.Err()
			case <-timer.C:
			}
		}
		restored, restoreErr := restoreStackKitsRetryLocalArtifacts(r.actionReq.TofuDir)
		if restoreErr != nil {
			r.q.addLog(r.job, "warn", fmt.Sprintf("Could not restore StackKits retry artifacts: %v", restoreErr))
		} else if len(restored) > 0 {
			r.q.addLog(r.job, "info", fmt.Sprintf("Restored missing StackKits retry artifacts: %s", strings.Join(restored, ", ")))
		}

		result, err = r.runRuntimeActionWithDiagnostics(ctx, r.cfg.RuntimeActions.RolloutRunner, r.actionReq, StepRolloutRunner, 84, "StackKits rollout retry still running; collecting target diagnostics...")
		if err == nil {
			return result, nil
		}
		lastErr = err
		if !isStackKitsRolloutRetryable(err) {
			return nil, err
		}
	}
	return nil, lastErr
}

func (r *deployRollout) runRuntimeActionWithDiagnostics(ctx context.Context, runner RuntimeActionRunner, req RuntimeActionRequest, step string, progress int, slowMessage string) (map[string]interface{}, error) {
	action := strings.TrimSpace(string(req.Action))
	if action == "" {
		action = step
	}
	tracer := otel.Tracer("github.com/kombifyio/techstack/pkg/jobs")
	ctx, span := tracer.Start(ctx, "runtime_action."+action)
	span.SetAttributes(
		attribute.String("techstack.job_id", r.job.ID),
		attribute.String("techstack.stack_id", r.job.TargetID),
		attribute.String("techstack.step", step),
		attribute.String("techstack.runtime_action", action),
		attribute.String("techstack.target_kind", r.targetKind),
	)
	if req.RuntimeTarget != nil {
		span.SetAttributes(
			attribute.String("techstack.runtime_host", req.RuntimeTarget.Host),
			attribute.Int("techstack.runtime_ssh_port", req.RuntimeTarget.Port),
		)
	}
	defer span.End()

	type runtimeActionResult struct {
		result map[string]interface{}
		err    error
	}
	started := time.Now()
	timeout := runtimeActionExecutionTimeout(r.cfg)
	actionCtx, actionCancel := context.WithTimeout(ctx, timeout)
	defer actionCancel()
	span.SetAttributes(attribute.Int64("techstack.runtime_action_timeout_ms", timeout.Milliseconds()))
	done := make(chan runtimeActionResult, 1)
	go func() {
		result, err := runRuntimeAction(actionCtx, runner, req)
		done <- runtimeActionResult{result: result, err: err}
	}()

	var collectOnce sync.Once
	collect := func(reason string, err error) {
		collectOnce.Do(func() {
			bundle := r.collectRuntimeDiagnostics(ctx, runner, req, action, reason, time.Since(started), err)
			if bundle != nil {
				span.SetAttributes(
					attribute.String("techstack.runtime_diagnostics.status", bundle.Status),
					attribute.String("techstack.runtime_diagnostics.reason", bundle.Reason),
				)
			}
		})
	}

	timer := time.NewTimer(runtimeDiagnosticsSlowProbeAfter)
	defer timer.Stop()
	for {
		select {
		case <-actionCtx.Done():
			if err := ctx.Err(); err != nil {
				collect("runtime_action_context_done", err)
				span.RecordError(err)
				return nil, err
			}
			err := runtimeActionTimedOutError(action, timeout, actionCtx.Err())
			collect("runtime_action_timeout", err)
			span.RecordError(err)
			return nil, err
		case outcome := <-done:
			if outcome.err != nil {
				if actionCtx.Err() == context.DeadlineExceeded && ctx.Err() == nil {
					err := runtimeActionTimedOutError(action, timeout, outcome.err)
					collect("runtime_action_timeout", err)
					span.RecordError(err)
					return nil, err
				}
				collect("runtime_action_failed", outcome.err)
				span.RecordError(outcome.err)
			}
			return outcome.result, outcome.err
		case <-timer.C:
			if slowMessage != "" {
				r.q.UpdateProgress(r.job.ID, progress, slowMessage)
			}
			breadcrumbWait(ctx, step, time.Since(started), slowMessage)
			collect("runtime_action_no_progress", nil)
		}
	}
}

func (r *deployRollout) collectRuntimeDiagnostics(ctx context.Context, runner RuntimeActionRunner, req RuntimeActionRequest, action, reason string, elapsed time.Duration, runtimeErr error) *RuntimeDiagnosticsBundle {
	collector := r.cfg.RuntimeActions.DiagnosticsCollector
	if collector == nil {
		return nil
	}
	diagnosticCtx, cancel := context.WithTimeout(ctx, defaultRuntimeDiagnosticsTimeout+5*time.Second)
	defer cancel()

	request := RuntimeDiagnosticsRequest{
		Action:         action,
		Reason:         reason,
		JobID:          r.job.ID,
		StackID:        r.job.TargetID,
		LeaseID:        firstNonEmpty(stringFromMap(r.job.Result, leaseIDField), stringFromMap(r.job.Payload, leaseIDField)),
		Provider:       firstNonEmpty(stringFromMap(r.job.Result, providerField), stringFromMap(r.job.Payload, providerField)),
		TargetKind:     r.targetKind,
		RuntimeTarget:  req.RuntimeTarget,
		ActionEndpoint: runtimeActionDescriptor(runner),
		Elapsed:        elapsed,
		Err:            runtimeErr,
	}
	bundle, err := collector.CollectRuntimeDiagnostics(diagnosticCtx, request)
	if err != nil {
		r.q.addLog(r.job, "warn", fmt.Sprintf("Runtime diagnostics collection failed: %s", secrets.Redact(err.Error())))
		captureJobError(ctx, err, map[string]interface{}{
			stepField:    action,
			stackIDField: r.job.TargetID,
			reasonField:  reason,
		})
		return nil
	}
	if bundle == nil {
		return nil
	}
	r.recordRuntimeDiagnostics(bundle, elapsed)
	breadcrumbStep(ctx, action, "runtime diagnostics collected", map[string]interface{}{
		"status":     bundle.Status,
		reasonField:  bundle.Reason,
		"elapsed_ms": elapsed.Milliseconds(),
	})
	return bundle
}

func (r *deployRollout) recordRuntimeDiagnostics(bundle *RuntimeDiagnosticsBundle, elapsed time.Duration) {
	if bundle == nil {
		return
	}
	entry := runtimeDiagnosticsBundleMap(bundle)
	if entry == nil {
		return
	}
	entry["job_id"] = r.job.ID
	entry[stackIDField] = r.job.TargetID
	entry["elapsed_before_collection_ms"] = elapsed.Milliseconds()

	r.job.mutateResult(func(result map[string]interface{}) {
		result["runtime_diagnostics"] = entry
	})

	switch bundle.Status {
	case "collected":
		r.q.addLog(r.job, "info", fmt.Sprintf("Runtime diagnostics collected for %s (%d commands)", bundle.Action, len(bundle.Commands)))
	case "skipped":
		r.q.addLog(r.job, "warn", fmt.Sprintf("Runtime diagnostics skipped for %s: %s", bundle.Action, bundle.Error))
	default:
		r.q.addLog(r.job, "warn", fmt.Sprintf("Runtime diagnostics %s for %s: %s", bundle.Status, bundle.Action, bundle.Error))
	}
}

func runtimeActionDescriptor(runner RuntimeActionRunner) RuntimeActionDescriptor {
	if describer, ok := runner.(RuntimeActionDescriber); ok {
		return describer.RuntimeActionDescriptor()
	}
	return RuntimeActionDescriptor{}
}

func redactedStackKitsRolloutRetryError(err error) string {
	if err == nil {
		return "unknown retryable error"
	}
	const limit = 1800
	text := strings.TrimSpace(secrets.Redact(err.Error()))
	if text == "" {
		return "empty retryable error"
	}
	if len(text) > limit {
		return text[:limit] + "... (truncated)"
	}
	return text
}

func isStackKitsRolloutRetryable(err error) bool {
	return isStackKitsCoolifyBootstrapReadinessRace(err) ||
		isStackKitsDockerImagePullTransient(err) ||
		isStackKitsLocalFileArtifactMissing(err) ||
		isStackKitsPlatformAppReadinessTransient(err)
}

func isStackKitsCoolifyBootstrapReadinessRace(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	if strings.Contains(text, "stackkit coolify bootstrap") {
		return strings.Contains(text, "email_notification_settings") ||
			strings.Contains(text, "coolify api token bootstrap did not return platform config json") ||
			strings.Contains(text, "could not find a coolify server") ||
			strings.Contains(text, "opentofu_apply_failed")
	}
	return strings.Contains(text, "opentofu_apply_failed") &&
		(strings.Contains(text, "coolify proxy") ||
			strings.Contains(text, "stackkit-managed coolify proxy") ||
			strings.Contains(text, "proxy fallback") ||
			strings.Contains(text, "docker daemon restarted successfully") ||
			strings.Contains(text, "checking and updating environment variables") ||
			strings.Contains(text, "setting up environment variable file"))
}

func isStackKitsDockerImagePullTransient(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	if !strings.Contains(text, "opentofu_apply_failed") {
		return false
	}
	if !(strings.Contains(text, "unable to read docker image into resource") ||
		strings.Contains(text, "unable to find or pull image") ||
		strings.Contains(text, "unable to pull image") ||
		strings.Contains(text, "error pulling image") ||
		strings.Contains(text, "failed to copy")) {
		return false
	}
	return strings.Contains(text, "failed to send write: eof") ||
		strings.Contains(text, "tls handshake timeout") ||
		strings.Contains(text, "client.timeout exceeded") ||
		strings.Contains(text, "client connection is closing") ||
		strings.Contains(text, "context canceled") ||
		strings.Contains(text, "connection reset by peer") ||
		strings.Contains(text, "temporary failure") ||
		strings.Contains(text, "unexpected eof") ||
		strings.Contains(text, "net/http: tls handshake timeout")
}

func isStackKitsLocalFileArtifactMissing(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "opentofu_apply_failed") &&
		strings.Contains(text, "invalid mount config") &&
		strings.Contains(text, "bind source path does not exist") &&
		strings.Contains(text, "/tofu/.homepage-") &&
		strings.Contains(text, ".yaml")
}

func isStackKitsPlatformAppReadinessTransient(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	if !strings.Contains(text, "platform_apps_deploy_failed") {
		return false
	}
	if strings.Contains(text, "wait for platform api readiness") &&
		strings.Contains(text, "coolify api did not become ready") {
		return true
	}
	return strings.Contains(text, "coolify service create") &&
		(strings.Contains(text, "connect: connection refused") ||
			strings.Contains(text, "connection refused") ||
			strings.Contains(text, "127.0.0.1:8000"))
}

type stackKitsRetryLocalArtifact struct {
	name    string
	content string
}

var stackKitsRetryLocalArtifacts = []stackKitsRetryLocalArtifact{
	{name: ".homepage-settings.yaml", content: "title: StackKits\nshortTitle: StackKits\ndescription: StackKits Homelab\n"},
	{name: ".homepage-services.yaml", content: "[]\n"},
	{name: ".homepage-widgets.yaml", content: "[]\n"},
	{name: ".homepage-docker.yaml", content: "{}\n"},
}

func restoreLegacyStackKitsRetryLocalArtifacts(typedAgentRollout bool, tofuDir string) ([]string, error) {
	if typedAgentRollout {
		return nil, nil
	}
	return restoreStackKitsRetryLocalArtifacts(tofuDir)
}

func restoreStackKitsRetryLocalArtifacts(tofuDir string) ([]string, error) {
	dir := strings.TrimSpace(tofuDir)
	if dir == "" {
		return nil, nil
	}
	info, err := os.Stat(dir)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("tofu dir %q is not a directory", dir)
	}
	restored := make([]string, 0, len(stackKitsRetryLocalArtifacts))
	for _, artifact := range stackKitsRetryLocalArtifacts {
		path := filepath.Join(dir, artifact.name)
		if _, err := os.Stat(path); err == nil {
			continue
		} else if !os.IsNotExist(err) {
			return restored, err
		}
		if err := os.WriteFile(path, []byte(artifact.content), 0600); err != nil {
			return restored, err
		}
		restored = append(restored, artifact.name)
	}
	return restored, nil
}

// runVerify verifies login-protected services. Managed cloud rollouts require a
// configured verifier returning a verified status.
func (r *deployRollout) runVerify(ctx context.Context) error {
	if r.managedRuntime && r.cfg.StackKitCommander == nil && r.cfg.RuntimeActions.RolloutVerifier == nil {
		r.job.setStep(StepVerifyRollout)
		return wrapProvisionError(StepVerifyRollout,
			"managed cloud rollout verifier is not configured",
			"Managed kombify Cloud rollouts require verified login-protected services before success is shown.")
	}
	if r.cfg.StackKitCommander == nil && r.cfg.RuntimeActions.RolloutVerifier == nil {
		if r.portGeneration != nil {
			return wrapProvisionError(StepVerifyRollout,
				"runtime listener activation requires a configured StackKits verifier",
				"Reserved host listeners stay non-active until the exact StackKits rollout is verified.")
		}
		return nil
	}
	startRuntimeLifecyclePhase(r.job, runtimePhaseVerify, "Verifying the deployed StackKit services")
	r.job.setStep(StepVerifyRollout)
	r.q.UpdateProgress(r.job.ID, 88, "Verifying login-protected services...")
	breadcrumbStep(ctx, StepVerifyRollout, "verifying login-protected services", nil)
	var result map[string]interface{}
	var err error
	if r.cfg.StackKitCommander != nil {
		result, err = r.runTypedStackKitVerify(ctx)
	} else {
		result, err = runRuntimeAction(ctx, r.cfg.RuntimeActions.RolloutVerifier, r.actionReq)
	}
	if err != nil {
		captureJobError(ctx, err, map[string]interface{}{stepField: StepVerifyRollout, stackIDField: r.job.TargetID})
		return wrapProvisionError(StepVerifyRollout, fmt.Sprintf("rollout verification failed: %v", err),
			"Immich/Vaultwarden verification failed or auth protection is not in place.")
	}
	proof := runtimeActionProof(string(runtimeaction.ActionVerifyRollout), result, string(runtimeaction.StatusVerified))
	r.runtimeProof["verification"] = proof
	r.e2eProof["verification_result"] = runtimeActionProofStatus(proof)
	r.persistRuntimeProgress()
	if (r.managedRuntime || r.portGeneration != nil) && !runtimeActionStatusAllowed(runtimeActionProofStatus(proof), string(runtimeaction.StatusVerified)) {
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
		return wrapProvisionError(StepPortAdmission, fmt.Sprintf("activate verified runtime listeners: %v", err),
			"The StackKit rollout verified, but Techstack could not activate its exact host-listener generation. The reservation remains fail-closed for reconciliation.")
	}
	return nil
}

// runRestoreDrill runs the backup restore drill. A verified drill promotes the
// final runtime phase to "verified" and records runtime metrics.
func (r *deployRollout) runRestoreDrill(ctx context.Context) error {
	if r.managedRuntime && r.cfg.RuntimeActions.RestoreDrill == nil {
		r.job.setStep(StepRestoreDrill)
		return wrapProvisionError(StepRestoreDrill,
			"managed cloud restore drill is not configured",
			"Managed kombify Cloud rollouts require a verified restore drill before success is shown.")
	}
	if r.cfg.RuntimeActions.RestoreDrill == nil {
		return nil
	}
	r.job.setStep(StepRestoreDrill)
	r.q.UpdateProgress(r.job.ID, 92, "Running backup restore drill...")
	breadcrumbStep(ctx, StepRestoreDrill, "running backup restore drill", nil)
	result, err := r.runRestoreDrillAction(ctx)
	if err != nil {
		// The native Architecture v2 line retires v1 backup execution and owes
		// its own contract ("backup execution requires a future native v2
		// contract"). A retired surface is a platform gap, not a failed drill:
		// record the honest receipt, keep the runtime phase at deployed (never
		// verified), and let the rollout finish.
		if strings.Contains(err.Error(), "legacy_runtime_action_retired") {
			r.runtimeProof["restore"] = map[string]interface{}{
				"action":      string(runtimeaction.ActionRestoreDrill),
				"status":      "not_applicable",
				"reason_code": "restore_drill_retired_pending_native_v2_contract",
			}
			r.e2eProof["restore_result"] = "not_applicable"
			r.persistRuntimeProgress()
			r.q.addLog(r.job, "warn", "Restore drill is retired on the native Architecture v2 line; runtime stays deployed (not verified) until the native backup contract lands")
			return nil
		}
		captureJobError(ctx, err, map[string]interface{}{stepField: StepRestoreDrill, stackIDField: r.job.TargetID})
		return wrapProvisionError(StepRestoreDrill, fmt.Sprintf("restore drill failed: %v", err),
			"Backup restore verification failed.")
	}
	proof := runtimeActionProof(string(runtimeaction.ActionRestoreDrill), result, string(runtimeaction.StatusVerified))
	r.runtimeProof["restore"] = proof
	mergeStackKitOutputs(r.stackKitOutputs, result)
	restoreStatus := runtimeActionProofStatus(proof)
	r.e2eProof["restore_result"] = restoreStatus
	r.persistRuntimeProgress()
	if r.managedRuntime && !runtimeActionStatusAllowed(restoreStatus, string(runtimeaction.StatusVerified)) {
		return wrapProvisionError(StepRestoreDrill,
			fmt.Sprintf("restore drill returned non-verified status %q", restoreStatus),
			"Managed kombify Cloud rollouts require a verified restore drill before success is shown.")
	}
	if runtimeActionStatusCountsAsVerified(restoreStatus) {
		r.finalRuntimePhase = RuntimePhaseVerified
		r.recordPhase(r.finalRuntimePhase)
	}
	mergeRuntimeMetrics(r.runtimeMetrics, result)
	return nil
}

func (r *deployRollout) runRestoreDrillAction(ctx context.Context) (map[string]interface{}, error) {
	result, err := r.runRuntimeActionWithDiagnostics(ctx, r.cfg.RuntimeActions.RestoreDrill, r.actionReq, StepRestoreDrill, 92, "Restore drill is still waiting for StackKit containers; collecting target diagnostics...")
	if err == nil || !r.managedRuntime || !isTransientRestoreReadinessError(err) {
		return result, err
	}
	var lastErr error = err
	for attempt, delay := range stackKitsRestoreReadinessRetryDelays {
		if delay > 0 {
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil, ctx.Err()
			case <-timer.C:
			}
		}
		r.q.UpdateProgress(
			r.job.ID,
			92,
			fmt.Sprintf("Waiting for StackKit containers before restore drill retry %d...", attempt+1),
		)
		result, lastErr = r.runRuntimeActionWithDiagnostics(ctx, r.cfg.RuntimeActions.RestoreDrill, r.actionReq, StepRestoreDrill, 92, "Restore drill retry is still waiting for StackKit containers; collecting target diagnostics...")
		if lastErr == nil || !isTransientRestoreReadinessError(lastErr) {
			return result, lastErr
		}
	}
	return nil, lastErr
}

func isTransientRestoreReadinessError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "no running docker containers") ||
		strings.Contains(msg, "coolify container is not running") ||
		strings.Contains(msg, "docker_runtime")
}

// finalize enforces the identity handoff contract, persists managed runtime
// lease metadata, and builds the final deploy job result.
func (r *deployRollout) finalize(ctx context.Context, prep *deployPreparation, art *deployArtifacts) error {
	if r.managedRuntime && !hasRequiredStackKitIdentityHandoff(r.stackKitOutputs) {
		r.job.setStep(StepVerifyRollout)
		return wrapProvisionError(StepVerifyRollout,
			"StackKit identity handoff is missing: required stackkit_outputs.identity.owner.username, stackkit_outputs.login_gateway.url, and stackkit_outputs.identity.recovery",
			"The StackKit rollout did not return owner login, login gateway, and recovery outputs. The UI must not show a successful managed rollout until stackkit_outputs includes those values.")
	}

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
	}
	copyRoutingDispatchReceipt(result, jobSnapshot.Payload, jobSnapshot.Result)
	if len(runtimeLifecycle) > 0 {
		result[runtimeLifecycleResultKey] = runtimeLifecycle
	}
	if len(r.runtimeProof) > 0 {
		result["runtime_proof"] = r.runtimeProof
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
	if ownerSpecBootstrapFromPayload(jobSnapshot.Payload) != nil {
		if missing := missingStackKitIdentityHandoffFields(r.stackKitOutputs); len(missing) > 0 {
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

func generateStackKitArtifacts(ctx context.Context, cfg *ProvisionConfig, job *Job, proposal *core.UnifiedSpec, stackSpecPath, outputDir string) (*StackKitArtifactGenerateResult, error) {
	if strings.TrimSpace(stackSpecPath) == "" {
		return nil, fmt.Errorf("StackKits CLI artifact generation requires persisted %s", unifier.StackSpecFilename)
	}
	generator := cfg.RuntimeActions.StackKitGenerator
	if generator == nil {
		release, err := configuredPinnedStackKitRelease()
		if err != nil {
			return nil, err
		}
		if release == nil {
			return nil, fmt.Errorf(
				"productive StackKits generation requires %s and %s",
				stackKitReleasePinEnv,
				stackKitReleaseCacheEnv,
			)
		}
		generator = NewPinnedStackKitCLIGenerator(*release)
	}
	result, err := generator.GenerateStackKitArtifacts(ctx, StackKitArtifactGenerateRequest{
		StackID:       job.TargetID,
		StackName:     proposal.Name,
		StackKit:      proposal.StackKit,
		WorkDir:       filepath.Dir(stackSpecPath),
		StackSpecPath: stackSpecPath,
		OutputDir:     outputDir,
	})
	if err != nil {
		return nil, err
	}
	if result == nil {
		return nil, fmt.Errorf("StackKits artifact generator returned no result")
	}
	return result, nil
}

func artifactResultMetadata(result *StackKitArtifactGenerateResult) map[string]string {
	if result == nil || len(result.Metadata) == 0 {
		return nil
	}
	out := make(map[string]string, len(result.Metadata))
	for key, value := range result.Metadata {
		if strings.TrimSpace(key) == "" {
			continue
		}
		out[key] = value
	}
	return out
}

func copyDeployArtifactMetadataToResult(result map[string]interface{}, metadata map[string]string) {
	if result == nil || len(metadata) == 0 {
		return
	}
	for _, key := range []string{
		metadataKeyAddressMode,
		metadataKeyRequestedAddressMode,
		metadataKeyKombifyMeAddressStatus,
		metadataKeyKombifyMeAddressWarn,
	} {
		if value := strings.TrimSpace(metadata[key]); value != "" {
			result[key] = value
		}
	}
}

func runtimeTargetOrNil(target *ManagedRuntimeTarget) *ManagedRuntimeTarget {
	if target == nil {
		return nil
	}
	copy := *target
	return &copy
}
