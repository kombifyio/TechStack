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
	"slices"
	"strings"
	"time"

	"github.com/kombifyio/techstack/internal/advancedissuer"
	"github.com/kombifyio/techstack/internal/portinventory"
	"github.com/kombifyio/techstack/internal/runtimeproduct/runtimeaction"
	"github.com/kombifyio/techstack/internal/stackkitrelease"
	"github.com/kombifyio/techstack/pkg/api/agentpb"
	"github.com/kombifyio/techstack/pkg/core"
	"github.com/kombifyio/techstack/pkg/grpcserver"
	"github.com/kombifyio/techstack/pkg/identity"
	"github.com/kombifyio/techstack/pkg/originca"
	"github.com/kombifyio/techstack/pkg/runtimeidentity"
	"github.com/kombifyio/techstack/pkg/secrets"
	"github.com/kombifyio/techstack/pkg/stackkitcommand"
	"github.com/kombifyio/techstack/pkg/unifier"
	"github.com/kombifyio/techstack/pkg/workerauth"
)

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
				handlerErr = wrapProvisionCause(StepFinalize, fmt.Errorf("finish routing rollout: %w", reconcileErr),
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
				return wrapProvisionCause(StepPortAdmission, rolloutErr,
					"The selected worker no longer matches the requested RuntimeServer. No port admission or host mutation was started.")
			}
			return wrapProvisionCause(StepTelemetryHandshake, rolloutErr, "Managed runtime enrollment could not be securely initialized. Configure the worker agent signing secret and retry before any runtime action.")
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
	retrySpecPath  string
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
		return nil, wrapProvisionCause(StepPrepareRollout, fmt.Errorf("failed to init persister: %w", err),
			"Could not initialize spec persistence.")
	}
	retrySpecPath, retryErr := materializeRolloutRetryProjection(persister, job)
	if retryErr != nil {
		return nil, wrapProvisionCause(StepPrepareRollout, retryErr, "The exact retry configuration no longer matches its approved dispatch.")
	}

	if !persister.IntentExists() {
		return nil, wrapProvisionError(StepPrepareRollout, "missing intent", "kombination.yaml not found for this stack; run the preparation step first")
	}
	if !persister.RequirementsSpecExists() {
		return nil, wrapProvisionError(StepPrepareRollout, "missing requirements", "requirements-spec.yaml not found for this stack; run the preparation step first")
	}

	intentBytes, err := persister.LoadIntentBytes()
	if err != nil {
		return nil, wrapProvisionCause(StepPrepareRollout, fmt.Errorf("failed to load intent: %w", err),
			"Could not load persisted configuration.")
	}

	reqSpec, err := persister.LoadRequirementsSpec()
	if err != nil {
		return nil, wrapProvisionCause(StepPrepareRollout, fmt.Errorf("failed to load requirements: %w", err),
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
		return nil, wrapProvisionCause(StepGenerateUnified, fmt.Errorf("failed to parse intent: %w", err),
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
		return nil, wrapProvisionCause(StepPrepareRollout, restoreErr,
			"Could not rebuild the StackKits handoff spec from your saved configuration.")
	}
	if projectionErr := rematerializeDeployProjection(persister, job); projectionErr != nil {
		return nil, wrapProvisionCause(StepPrepareRollout, projectionErr,
			"Could not rebuild the native v2 StackKits projection from the saved stack configuration.")
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
		retrySpecPath:  retrySpecPath,
	}, nil
}

// rematerializeDeployProjection restores the process-local projection from the
// immutable deploy snapshot. Presence of stack_spec_v2 identifies a native-v2
// dispatch, so a malformed or empty value must fail closed rather than remove
// the sibling and silently fall back to a build-time kit template.
func rematerializeDeployProjection(persister *unifier.SpecPersister, job *Job) error {
	if persister == nil || job == nil {
		return nil
	}
	if _, nativeV2 := job.Payload[payloadKeyStackSpecV2]; !nativeV2 {
		return nil
	}
	path, err := persistProjectedStackSpec(persister, job.Payload)
	if err != nil {
		return err
	}
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("native-v2 deploy snapshot has no %s projection", payloadKeyStackSpecV2)
	}
	return nil
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
		if errors.Is(err, ErrManagedRuntimeEnrollmentFailed) {
			collectManagedRuntimeEnrollmentDiagnostics(ctx, cfg, job, q, kombSpec, nil, "managed_runtime_enrollment_failed", err)
		}
		captureJobError(ctx, err, map[string]interface{}{
			stepField:     StepPrepareRollout,
			stackIDField:  job.TargetID,
			leaseIDField:  stringFromMap(job.Result, leaseIDField),
			tenantIDField: managedRuntimeTenantID(job, kombSpec),
		})
		return nil, "", wrapProvisionCause(StepPrepareRollout, err,
			"Managed Runtime rollout requires an SSH host or public IP from the VM lease before StackKits can generate and apply the rollout.")
	}
	hydrateManagedRuntimeSpec(kombSpec, managedTarget)
	copyManagedRuntimeTargetToJob(job, managedTarget)
	collectManagedRuntimeEnrollmentDiagnostics(ctx, cfg, job, q, kombSpec, managedTarget, "managed_runtime_enrollment_succeeded", nil)
	hydratedStackSpecPath, hydratedStackSpecHash, hydrateErr := hydratePersistedStackSpecTarget(persister, managedTarget)
	if hydrateErr != nil {
		return nil, "", wrapProvisionCause(StepPrepareRollout, hydrateErr,
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

func collectManagedRuntimeEnrollmentDiagnostics(
	ctx context.Context,
	cfg *ProvisionConfig,
	job *Job,
	q *Queue,
	spec *core.KombinationSpec,
	target *ManagedRuntimeTarget,
	reason string,
	enrollmentErr error,
) {
	if cfg == nil || job == nil || cfg.RuntimeActions.DiagnosticsCollector == nil {
		return
	}
	snapshot := job.Snapshot()
	leaseID := firstNonEmpty(stringFromMap(snapshot.Result, leaseIDField), stringFromMap(snapshot.Payload, leaseIDField), metadataString(spec, leaseIDField))
	tenantID := managedRuntimeTenantIDFromSnapshot(snapshot, spec)
	provider := firstNonEmpty(stringFromMap(snapshot.Result, providerField), stringFromMap(snapshot.Payload, providerField), metadataString(spec, metadataKeyProviderID))
	if target == nil {
		target = cachedManagedRuntimeDiagnosticsTarget(job, snapshot, spec, provider)
	} else {
		target = attachManagedProviderCredential(target, provider)
	}
	elapsed := time.Duration(0)
	if startedAt, ok := managedRuntimeEnrollmentWaitStartedAt(snapshot.Result); ok {
		elapsed = time.Since(startedAt)
	}
	rollout := &deployRollout{cfg: cfg, job: job, q: q, managedRuntime: true, targetKind: "cloud"}
	rollout.collectRuntimeDiagnostics(ctx, nil, RuntimeActionRequest{
		StackID: job.TargetID, TenantID: tenantID,
		RuntimeTarget: stackKitsRuntimeActionTargetFromManagedRuntimeTarget(target),
		TechStackEnrollment: &TechStackEnrollment{
			TenantID: tenantID, StackID: job.TargetID, LeaseID: leaseID,
			ServerID: runtimeidentity.LeaseServerID(leaseID), RuntimeAgentID: runtimeidentity.LeaseRuntimeAgentID(tenantID, leaseID),
		},
	}, "managed_runtime_enrollment", reason, elapsed, enrollmentErr)
}

func cachedManagedRuntimeDiagnosticsTarget(job *Job, snapshot JobSnapshot, spec *core.KombinationSpec, provider string) *ManagedRuntimeTarget {
	var target *ManagedRuntimeTarget
	job.mu.RLock()
	target = cloneManagedRuntimeTarget(job.managedRuntimeTarget)
	job.mu.RUnlock()
	if target == nil {
		target = managedRuntimeTargetFromRuntimeMap(snapshot.Result, "job-result")
	}
	if target == nil {
		target = managedRuntimeTargetFromRuntimeMap(snapshot.Payload, "job-payload")
	}
	if target == nil && spec != nil {
		target = ManagedRuntimeTargetFromMetadata(spec.Metadata)
	}
	return attachManagedProviderCredential(target, provider)
}

// deployValidateWorkers parses approved workers from the payload and enforces
// worker requirements for non-managed stacks.
func deployValidateWorkers(job *Job, q *Queue, prep *deployPreparation) ([]core.Worker, error) {
	workers, err := parseWorkersFromPayload(job.Payload["workers"])
	if err != nil {
		return nil, wrapProvisionCause(StepValidateWorkers, fmt.Errorf("invalid workers payload: %w", err),
			"Could not read approved workers for rollout.")
	}
	if prep.managedRuntime && len(workers) == 0 {
		// The telemetry handoff precedes worker validation in the task order.
		job.setStep(StepTelemetryHandshake)
		q.UpdateProgress(job.ID, 15, "Preparing runtime telemetry handoff...")
	}
	job.setStep(StepValidateWorkers)
	q.UpdateProgress(job.ID, 18, "Checking workers...")

	if !prep.managedRuntime {
		if ok, msg := workersSatisfyRequirements(prep.reqSpec.RequiredWorkers, workers); !ok {
			return nil, wrapProvisionError(StepValidateWorkers, "requirements not satisfied", msg)
		}
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
		return nil, wrapProvisionCause(
			StepGenerateUnified,
			fmt.Errorf("failed to prepare StackSpec proposal: %w", err),
			"Could not prepare the reviewed configuration for StackKits.",
		)
	}

	job.setStep(StepPersistUnified)
	q.UpdateProgress(job.ID, 45, "Persisting StackSpec review proposal...")

	proposalPath, err := prep.persister.SaveUnifiedSpec(unifiedSpec, prep.persister.GetRequirementsSpecPath())
	if err != nil {
		return nil, wrapProvisionCause(StepPersistUnified, fmt.Errorf("failed to persist StackSpec proposal: %w", err),
			"Could not persist the reviewed StackSpec proposal.")
	}

	tofuDir := prep.persister.GetTofuDir()
	if err := os.MkdirAll(tofuDir, 0755); err != nil {
		return nil, wrapProvisionCause(StepGenerateIaC, fmt.Errorf("failed to create tofu dir: %w", err),
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
			return nil, wrapProvisionCause(StepGenerateIaC, hydrateErr,
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
	if prep.retrySpecPath != "" {
		// The verified native intent cannot be shadowed by a cached handoff.
		// Apply the same managed target transformation used by node INIT before
		// controller rendering, retaining both snapshots outside that handoff.
		var retryErr error
		stackSpecPath, retryErr = managedRolloutRetryProjection(prep.persister, prep.retrySpecPath, job.ID)
		if retryErr != nil {
			return nil, wrapProvisionCause(StepGenerateIaC, retryErr, "Could not prepare the exact retry configuration for Advanced Mode.")
		}
		// ADDRESS_BIND persists native intent too. Keep its fixed basename and
		// governed deploy output in this exact dispatch's preparation workspace,
		// leaving earlier controller artifacts untouched on the persistent disk.
		tofuDir = filepath.Join(filepath.Dir(stackSpecPath), "tofu")
	}
	artifactResult, err := generateStackKitArtifacts(ctx, cfg, job, unifiedSpec, stackSpecPath, tofuDir, prep.managedTarget,
		managedRuntimeTenantID(job, prep.kombSpec), managedRuntimeOwnerID(job, prep.kombSpec),
		func(message string) { q.UpdateProgress(job.ID, 60, message) })
	if err != nil {
		return nil, wrapProvisionCause(StepGenerateIaC, fmt.Errorf("StackKits artifact generation failed: %w", err),
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
	if artifactResult != nil && strings.TrimSpace(artifactResult.StackSpecPath) != "" {
		stackSpecPath = artifactResult.StackSpecPath
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
			captureJobError(ctx, err, map[string]interface{}{stepField: StepStackKitPrepare, stackIDField: r.job.TargetID, "stack_kit": r.unifiedSpec.StackKit})
			return wrapProvisionCause(StepStackKitPrepare, fmt.Errorf("managed runtime target bootstrap failed: %w", err),
				"TechStack could not prepare the managed VPS for the StackKits rollout.")
		}
		if r.managedRuntime {
			completeRuntimeLifecyclePhase(r.job, runtimePhaseReachability, "Managed runtime target passed the StackKit bootstrap boundary", nil)
		}
		r.updateJobStepProgress(StepRolloutRunner, 82, "Applying StackKit services on the managed VPS...")
		result, err := r.runTypedStackKitApply(ctx)
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
	if err := bindRolloutRetryCurrentIntent(r.job, &baseRequest, release.Receipt().Version,
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

// managedInventoryKit reports whether the kit's managed rollout hands the host
// a Core-built Inventory: Cloud Kit binds its one Cloud channel, Modern
// Homelab binds one channel per node.
func managedInventoryKit(stackKit string) bool {
	switch strings.ToLower(strings.TrimSpace(stackKit)) {
	case "cloud-kit", "modern-homelab":
		return true
	}
	return false
}

// managedInventoryNodes lists the channel of every node of a multi-node kit,
// so Home's Apply finds a channel for each plan node instead of refusing the
// Cloud node as `execution-channel-unbound`. A one-node kit has none: its
// Inventory is the single Cloud channel.
func managedInventoryNodes(stackKit string) ([]ManagedStackKitNodeChannel, error) {
	if !managedInventoryKit(stackKit) {
		return nil, nil
	}
	nodes, err := localExecutionNodes(stackKit)
	if err != nil {
		return nil, err
	}
	if len(nodes) < 2 {
		return nil, nil
	}
	channels := make([]ManagedStackKitNodeChannel, 0, len(nodes))
	for _, node := range nodes {
		channels = append(channels, ManagedStackKitNodeChannel{ChannelRef: node.ExecutionChannelRef, SiteRef: node.SiteRef, NodeRef: node.NodeRef})
	}
	return channels, nil
}

const managedBackupTargetInventoryResultField = "managed_backup_target_inventory"

// buildManagedStackKitInventory always binds a managed Cloud rollout to the
// enrolled Operations process. The Inventory it returns is channel-only: when
// the plan selects the off-site backup capability, the node issues its own
// backup-target binding from sealed credentials the returned delivery authority
// moves to it, so this helper never builds, signs or attests a binding. A
// missing delivery authority for a selected capability fails closed.
func (r *deployRollout) buildManagedStackKitInventory(
	ctx context.Context,
	resolvedPlan []byte,
) ([]byte, ManagedBackupTargetDelivery, error) {
	required, err := managedBackupTargetInventoryRequired(resolvedPlan)
	if err != nil {
		r.recordManagedBackupTargetInventory("failed", "backup_target_requirements_invalid", false)
		return nil, nil, fmt.Errorf("inspect managed Cloud backup target requirements: %w", err)
	}
	if !required {
		r.recordManagedBackupTargetInventory("not_applicable", "optional_backup_not_selected", false)
	}
	if r == nil || r.cfg == nil || r.job == nil || r.cfg.ManagedStackKitInventory == nil {
		if required {
			r.recordManagedBackupTargetInventory("failed", "managed_backup_inventory_unavailable", false)
			return nil, nil, fmt.Errorf("managed Cloud StackKits Inventory authority is not configured for the selected backup capability")
		}
		return nil, nil, fmt.Errorf("managed Cloud StackKits Operations channel Inventory authority is not configured")
	}
	var delivery ManagedBackupTargetDelivery
	if required {
		if delivery = managedBackupDeliveryOf(r.cfg.ManagedStackKitInventory); delivery == nil {
			r.recordManagedBackupTargetInventory("failed", "managed_backup_delivery_unavailable", false)
			return nil, nil, fmt.Errorf("managed Cloud backup delivery authority is not configured for the selected backup capability")
		}
	}
	var nodes []ManagedStackKitNodeChannel
	if r.unifiedSpec != nil {
		if nodes, err = managedInventoryNodes(r.unifiedSpec.StackKit); err != nil {
			return nil, nil, err
		}
	}
	inventory, err := r.cfg.ManagedStackKitInventory.Build(ctx, ManagedStackKitInventoryRequest{
		TenantID: r.actionReq.TenantID, StackID: r.job.TargetID, ResolvedPlan: resolvedPlan,
		Nodes: nodes,
	})
	if err != nil {
		if !required {
			return nil, nil, fmt.Errorf("build managed Cloud StackKits Operations channel Inventory: %w", err)
		}
		r.recordManagedBackupTargetInventory("failed", managedBackupTargetInventoryFailureReason(err), managedBackupTargetInventoryRetryable(err))
		return nil, nil, fmt.Errorf("build managed Cloud StackKits Inventory: %w", err)
	}
	if required {
		r.recordManagedBackupTargetInventory("ready", "", false)
		r.projectBackupSchedule(ctx, resolvedPlan)
	}
	return inventory, delivery, nil
}

// projectBackupSchedule records the cadence the plan selected, so the control
// plane can later tell that this stack is due. It runs only after the inventory
// bound successfully: projecting a schedule for a stack whose backup target
// failed to attest would queue runs against a repository that does not exist.
//
// A projection failure never fails the deploy. The rollout already succeeded
// and the backup target is bound; losing the cadence delays backups until the
// next deploy, while failing here would roll back a healthy deployment over a
// scheduling record.
func (r *deployRollout) projectBackupSchedule(ctx context.Context, resolvedPlan []byte) {
	if r == nil || r.cfg == nil || r.cfg.BackupScheduleProjector == nil || r.job == nil {
		return
	}
	projection, found, err := backupScheduleFromResolvedPlan(resolvedPlan)
	if err != nil {
		r.recordBackupScheduleProjection("failed", "backup_schedule_invalid")
		return
	}
	if !found {
		// A backup target without a schedule is a manual-only repository, not
		// a defect: nothing is due until someone asks.
		r.recordBackupScheduleProjection("not_applicable", "no_schedule_selected")
		return
	}
	projection.TenantID = strings.TrimSpace(r.actionReq.TenantID)
	projection.StackID = strings.TrimSpace(r.job.TargetID)
	projection.OwnerID = strings.TrimSpace(r.actionReq.OwnerID)
	projection.StackName = strings.TrimSpace(r.job.TargetName)
	if err := r.cfg.BackupScheduleProjector(ctx, projection); err != nil {
		r.recordBackupScheduleProjection("failed", "backup_schedule_not_persisted")
		return
	}
	r.recordBackupScheduleProjection("ready", "")
}

func (r *deployRollout) recordBackupScheduleProjection(status, reasonCode string) {
	if r == nil || r.job == nil {
		return
	}
	r.job.mutateResult(func(result map[string]interface{}) {
		result["managed_backup_schedule"] = map[string]interface{}{
			"status": status, "reason_code": reasonCode,
		}
	})
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
	return runTypedStackKitApplySequenceWithBudget(ctx, sender, jobID, baseRequest, release, 0, nil)
}

// advancedTrustAdmission admits and records the host's trust-import evidence.
// A nil admission still requires evidence that pins the dispatched bundle.
type advancedTrustAdmission func(context.Context, *agentpb.StackKitCommand, *agentpb.StackKitResult) error

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
			originCertificate, err = nil, nil
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
		if err := bindManagedStackKitCommandBudget(ctx, command, request.Operation); err != nil {
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
	planHash, _, err := typedStackKitPlanBinding(result, false)
	return planHash, err
}

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
		// Only the pinned published release prepares a target; there is no
		// source-built StackKits CLI fallback.
		if release != nil {
			prepRunner = NewPinnedStackKitCLIPrepRunner(*release)
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
	if err != nil && r.cfg.StackKitCommander != nil &&
		classifyRuntimeTargetBootstrapError(err, "") == RuntimeTargetBootstrapSSHAuth {
		// Host-security disables ubuntu/root. Guard HTTPS remains the Apply
		// channel. A live Auth0 Centron retry failed here while Coolify was
		// already running and the agent was connected.
		if r.q != nil && r.job != nil {
			r.q.addLog(r.job, "warn", "SSH bootstrap unavailable after host-security; continuing typed Apply over Guard")
		}
		return &RuntimeTargetBootstrapResult{
			Status:  RuntimeTargetBootstrapReady,
			Message: "SSH bootstrap skipped because Guard is connected",
		}, nil
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
	currentStep, currentProgress := r.job.stepProgress()
	if rolloutStepBefore(step, currentStep) {
		// Prepare progress events may name an earlier task (the CLI connects to
		// the target again, Docker pre-bootstrap precedes the CLI prepare).
		// The reported position only moves forward; the message still lands.
		step, progress = currentStep, currentProgress
	}
	if strings.TrimSpace(step) != "" {
		r.job.setStep(step)
	}
	if r.q != nil {
		r.q.UpdateProgress(r.job.ID, progress, message)
	}
}

// rolloutTaskOrder is the creation page's rollout task order.
var rolloutTaskOrder = []string{
	StepCreateLease, StepPrepareRollout, StepRuntimeConnected, StepTelemetryHandshake,
	StepValidateWorkers, StepGenerateUnified, StepPersistUnified, StepGenerateIaC,
	StepSimulationGate, StepStackKitPrepare, StepDockerReady, StepOpenTofuReady,
	StepTerramateReady, StepTelemetryReady, StepRolloutRunner, StepServiceInventory,
	StepVerifyRollout, StepRestoreDrill,
}

// rolloutStepBefore reports whether next is an earlier rollout task than
// current. Steps outside the task order never count as earlier.
func rolloutStepBefore(next, current string) bool {
	nextIndex, currentIndex := -1, -1
	for index, step := range rolloutTaskOrder {
		if step == next {
			nextIndex = index
		}
		if step == current {
			currentIndex = index
		}
	}
	return nextIndex >= 0 && currentIndex >= 0 && nextIndex < currentIndex
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

func (r *deployRollout) collectRuntimeDiagnostics(ctx context.Context, runner RuntimeActionRunner, req RuntimeActionRequest, action, reason string, elapsed time.Duration, runtimeErr error) *RuntimeDiagnosticsBundle {
	collector := r.cfg.RuntimeActions.DiagnosticsCollector
	if collector == nil {
		return nil
	}
	diagnosticCtx, cancel := context.WithTimeout(ctx, defaultRuntimeDiagnosticsTimeout+5*time.Second)
	defer cancel()

	snapshot := r.job.Snapshot()
	leaseID := firstNonEmpty(stringFromMap(snapshot.Result, leaseIDField), stringFromMap(snapshot.Payload, leaseIDField))
	serverID := runtimeidentity.LeaseServerID(leaseID)
	runtimeAgentID := runtimeidentity.LeaseRuntimeAgentID(req.TenantID, leaseID)
	if req.TechStackEnrollment != nil {
		serverID = firstNonEmpty(req.TechStackEnrollment.ServerID, serverID)
		runtimeAgentID = firstNonEmpty(req.TechStackEnrollment.RuntimeAgentID, runtimeAgentID)
	}
	request := RuntimeDiagnosticsRequest{
		Action:         action,
		Reason:         reason,
		JobID:          r.job.ID,
		StackID:        r.job.TargetID,
		TenantID:       strings.TrimSpace(req.TenantID),
		LeaseID:        leaseID,
		OperationID:    firstNonEmpty(stringFromMap(snapshot.Result, "operation_id"), stringFromMap(snapshot.Payload, "operation_id")),
		ServerID:       serverID,
		RuntimeAgentID: runtimeAgentID,
		Provider:       firstNonEmpty(stringFromMap(snapshot.Result, providerField), stringFromMap(snapshot.Payload, providerField)),
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
func (r *deployRollout) runTypedStackKitRestoreDrill(ctx context.Context) (map[string]interface{}, error) {
	if r == nil || r.cfg == nil || r.cfg.StackKitCommander == nil || r.actionReq.TechStackEnrollment == nil || r.job == nil || r.unifiedSpec == nil {
		return nil, fmt.Errorf("typed StackKits restore dispatcher is not configured")
	}
	release, err := configuredTargetStackKitRelease()
	if err != nil {
		return nil, err
	}
	if release == nil {
		return nil, fmt.Errorf("pinned published StackKits release is not configured")
	}
	if err := r.requireAdvancedRestoreDrill(*release); err != nil {
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
	if err := stackkitcommand.ValidateCommand(configureCommand); err != nil {
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

func generateStackKitArtifacts(ctx context.Context, cfg *ProvisionConfig, job *Job, proposal *core.UnifiedSpec, stackSpecPath, outputDir string, runtimeTarget *ManagedRuntimeTarget, tenantID, ownerID string, progress func(string)) (*StackKitArtifactGenerateResult, error) {
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
		TenantID:      tenantID,
		OwnerID:       ownerID,
		StackName:     proposal.Name,
		StackKit:      proposal.StackKit,
		WorkDir:       filepath.Dir(stackSpecPath),
		StackSpecPath: stackSpecPath,
		OutputDir:     outputDir,
		RuntimeTarget: cloneManagedRuntimeTarget(runtimeTarget),
		Progress:      progress,
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
		metadataKeyKombifyMeAddressPrefix,
		metadataKeyKombifyMeAddressLayout,
		metadataKeyKombifyMeAddressZone,
	} {
		if value := strings.TrimSpace(metadata[key]); value != "" {
			result[key] = value
		}
	}
}
