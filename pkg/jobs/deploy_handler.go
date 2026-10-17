package jobs

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/kombifyio/techstack/pkg/core"
	"github.com/kombifyio/techstack/pkg/runtimeidentity"
	"github.com/kombifyio/techstack/pkg/unifier"
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
