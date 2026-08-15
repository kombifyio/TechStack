package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/kombifyio/go-common/identity"
	"github.com/kombifyio/techstack/internal/providercatalog"
	"github.com/kombifyio/techstack/internal/runtimeproduct/runtimeaction"
	"github.com/kombifyio/techstack/pkg/core"
	"github.com/kombifyio/techstack/pkg/monthlyruntime"
	"github.com/kombifyio/techstack/pkg/unifier"
)

var oneLinerSimulationPreviewTimeout = 5 * time.Second

const autoDeployGuardWaitStartedAtField = "auto_deploy_guard_wait_started_at"

// ProvisionHandler creates a job handler for preparing StackKits rollouts.
// It structurally parses the incoming proposal, persists Techstack's internal
// intent, and stores the reviewable stack-spec.yaml handoff for StackKits.
//
// Important: This handler works WITHOUT requiring servers to be connected first.
// kombifyTechstack can run as SaaS - servers/workers are added later.
// The Unifier prepares a review proposal. The pinned StackKits CLI remains the
// only final validation, resolution, and generation authority.
//
// The closure orchestrates these phases (each in its own helper below):
// validate spec -> resolve StackKit -> persist artifacts -> build result ->
// (optional) create managed lease -> (optional) auto-deploy.
//
//nolint:goconst // Job result keys are persisted API/UI wire fields.
func ProvisionHandler(cfg *ProvisionConfig) JobHandler {
	cfg = normalizeProvisionConfig(cfg)

	return func(ctx context.Context, job *Job, q *Queue) (handlerErr error) {
		defer func() {
			if handlerErr != nil && !isJobWaitError(handlerErr) {
				failCurrentRuntimeLifecyclePhase(job)
			}
		}()
		preparedLeaseRequest, err := preparedManagedLeaseRequest(job)
		if err != nil {
			return wrapProvisionError(StepValidate, err.Error(), "The admitted managed server request could not be restored.")
		}
		specData, spec, err := provisionValidateSpec(job, q)
		if err != nil {
			return err
		}
		if err := validateFreshManagedProviderSpec(mapFromInterface(specData), spec); err != nil {
			return wrapProvisionError(StepValidate, err.Error(),
				"Select a supported managed provider again. Fresh requests require provider_id centron or ionos; composite provider aliases are not executable.")
		}

		requirementsSpec, err := provisionResolveStackKit(cfg, job, q, spec)
		if err != nil {
			return err
		}

		persisted, err := provisionPersistArtifacts(cfg, job, q, spec, specData, requirementsSpec)
		if err != nil {
			return err
		}

		runtimePhase := RuntimePhasePrepared
		serverMode := serverModeForSpec(spec)
		providerID := ""
		if serverMode == serverModeMonthlyRuntime {
			providerID = providerIDForSpec(spec)
		}

		previousResult := cloneJobResult(job.Snapshot().Result)
		result := buildProvisionResult(job, spec, persisted, serverMode, providerID, runtimePhase)
		applyProvisionRequirements(result, requirementsSpec)
		if preparedLeaseRequest != nil {
			// The control-plane job projection persists Result, not the in-memory
			// Payload. Keep the exact request that crossed native admission there so
			// a provider wait can be resumed by another process without rebuilding a
			// different request from the mutable StackKit spec.
			result[PreparedManagedLeaseRequestResultKey] = ManagedLeaseRequestPayload(*preparedLeaseRequest)
		}
		preparedManagedLease := restorePreparedManagedLeaseCheckpoint(result, previousResult, job, providerID)
		job.replaceResult(result)

		if isManagedCloudSpec(spec) && !isInstallCommandSpec(spec) {
			startRuntimeLifecyclePhase(job, runtimePhaseServerAllocate, "Allocating or binding the managed server")
			if !preparedManagedLease {
				if err := provisionCreateManagedLease(ctx, cfg, job, q, spec, requirementsSpec, providerID, runtimePhase, preparedLeaseRequest); err != nil {
					return err
				}
			}
			if stringFromMap(job.Snapshot().Result, "runtime_phase") == string(RuntimePhaseLeasePending) {
				q.UpdateProgress(job.ID, 82, "Managed provider operation is still provisioning...")
				return &JobWaitError{
					Reason:      WaitReasonManagedRuntimeProvider,
					Message:     "The managed provider operation is still provisioning its durable server resources.",
					ResumeAfter: 15 * time.Second,
				}
			}
			completeRuntimeLifecyclePhase(job, runtimePhaseServerAllocate, "Managed server allocation is persisted", map[string]interface{}{
				leaseIDField: stringFromMap(job.Result, leaseIDField),
			})
		} else {
			completeRuntimeLifecyclePhase(job, runtimePhaseServerAllocate, "Existing server intent is persisted", nil)
		}
		if isInstallCommandSpec(spec) {
			if err := provisionRunOneLinerSimulationPreview(ctx, cfg, job, q, spec, persisted); err != nil {
				return err
			}
		}

		if handled, err := provisionMaybeAutoDeploy(ctx, cfg, job, q); handled {
			return err
		}

		q.UpdateProgress(job.ID, 90, "Requirements ready")
		q.UpdateProgress(job.ID, 100, "Requirements generated - ready for rollout")

		return nil
	}
}

func preparedManagedLeaseRequest(job *Job) (*ManagedLeaseRequest, error) {
	if job == nil {
		return nil, nil
	}
	snapshot := job.Snapshot()
	if value, exists := snapshot.Result[PreparedManagedLeaseRequestResultKey]; exists {
		return managedLeaseRequestFromPayload(value)
	}
	if value, exists := snapshot.Payload[PreparedManagedLeaseRequestPayloadKey]; exists {
		return managedLeaseRequestFromPayload(value)
	}
	specData := mapFromInterface(snapshot.Payload["spec"])
	value, exists := specData[PreparedManagedLeaseRequestPayloadKey]
	if !exists {
		return nil, nil
	}
	return managedLeaseRequestFromPayload(value)
}

// restorePreparedManagedLeaseCheckpoint carries the immutable result of a
// successful managed-server allocation across a queue wait. Provision jobs are
// intentionally resumed through ProvisionHandler until Guard admission is
// fresh. Re-running the provider create after that wait is both unnecessary and
// unsafe: the exact lease already exists and is the authority for the rollout.
//
// The checkpoint is accepted only when its stack/provider/tenant/owner binding
// still matches the current job. Unknown or partial state is ignored and falls
// back to the normal idempotent provider path.
func restorePreparedManagedLeaseCheckpoint(result, previous map[string]interface{}, job *Job, providerID string) bool {
	if result == nil || previous == nil || job == nil {
		return false
	}
	snapshot := job.Snapshot()
	leaseID := strings.TrimSpace(stringFromMap(previous, leaseIDField))
	if leaseID == "" || strings.TrimSpace(stringFromMap(previous, "runtime_phase")) != string(RuntimePhaseLeaseReady) ||
		strings.TrimSpace(stringFromMap(previous, metadataKeyStackID)) != strings.TrimSpace(snapshot.TargetID) ||
		strings.TrimSpace(stringFromMap(previous, metadataKeyProviderID)) != strings.TrimSpace(providerID) {
		return false
	}
	for _, binding := range []struct {
		key      string
		expected string
	}{
		{tenantIDField, firstNonEmpty(stringFromMap(snapshot.Payload, tenantIDField), stringFromMap(result, tenantIDField))},
		{"owner_id", firstNonEmpty(stringFromMap(snapshot.Payload, "owner_id"), stringFromMap(result, "owner_id"))},
	} {
		observed := strings.TrimSpace(stringFromMap(previous, binding.key))
		if observed == "" || strings.TrimSpace(binding.expected) == "" || observed != strings.TrimSpace(binding.expected) {
			return false
		}
	}

	for _, key := range []string{
		leaseIDField,
		"runtime_phase",
		metadataKeyProviderID,
		metadataKeyDesiredState,
		metadataKeyBillingMode,
		metadataKeyScenarioID,
		metadataKeyProviderRegion,
		metadataKeyIONOSDatacenter,
		tenantIDField,
		"owner_id",
		"registration_token",
		metadataKeyRuntimePublicIP,
		metadataKeyRuntimePrivateIP,
		metadataKeyRuntimeSSHHost,
		metadataKeyRuntimeSSHUser,
		metadataKeyRuntimeSSHPort,
		metadataKeyRuntimeSSHPrivateKey,
		metadataKeyRuntimeClientKey,
		metadataKeyRuntimeSSHPassword,
		metadataKeyRuntimeEnrollState,
		metadataKeyRuntimeEnrollError,
		autoDeployGuardWaitStartedAtField,
		runtimeLifecycleResultKey,
	} {
		if value, ok := previous[key]; ok {
			result[key] = value
		}
	}
	return true
}

func isInstallCommandSpec(spec *core.KombinationSpec) bool {
	if spec == nil {
		return false
	}
	return strings.TrimSpace(spec.Metadata[metadataKeyServerProvisionMode]) == serverProvisionModeInstall ||
		strings.TrimSpace(spec.Metadata[metadataKeyServerConnectionMode]) == "agent-oneliner" ||
		strings.EqualFold(strings.TrimSpace(spec.Metadata["server_install_command_required"]), "true")
}

func provisionRunOneLinerSimulationPreview(ctx context.Context, cfg *ProvisionConfig, job *Job, q *Queue, spec *core.KombinationSpec, persisted *provisionArtifacts) error {
	job.setStep(StepSimulationGate)
	q.UpdateProgress(job.ID, 82, "Preparing temporary Simulate demo preview...")
	provisionReleaseOneLinerInstallCommand(job)
	if cfg == nil || cfg.RuntimeActions.SimulationGate == nil {
		recordOneLinerSimulationPreviewUnavailable(job, "not_configured", "Simulate preview is not configured. Configure TECHSTACK_SIMULATE_ACTIONS_URL or TECHSTACK_KOMBISIM_URL and SERVICE_AUTH_SECRET to show the one-hour demo preview.")
		return nil
	}

	req := RuntimeActionRequest{
		Action:        runtimeaction.ActionSimulateUpdate,
		StackID:       job.TargetID,
		StackName:     spec.Name,
		StackKit:      spec.Kit,
		TenantID:      managedRuntimeTenantID(job, spec),
		OwnerID:       managedRuntimeOwnerID(job, spec),
		StackSpecPath: strings.TrimSpace(persisted.stackSpecPath),
		UnifiedPath:   strings.TrimSpace(persisted.intentPath),
		PreviewPolicy: &PreviewPolicy{
			Required:          false,
			Runtime:           "provider-backed",
			Audience:          "staff",
			Visibility:        "private",
			TTLSeconds:        3600,
			StaffOnly:         true,
			PublicBetaPreview: true,
		},
	}
	actionCtx := provisionSimulationPreviewContext(ctx, job, spec)
	previewCtx, cancel := context.WithTimeout(actionCtx, oneLinerSimulationPreviewTimeout)
	defer cancel()
	result, err := runRuntimeAction(previewCtx, cfg.RuntimeActions.SimulationGate, req)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(previewCtx.Err(), context.DeadlineExceeded) {
			recordOneLinerSimulationPreviewUnavailable(job, "timeout", "The temporary Simulate preview did not finish within 5 seconds. The install command is still ready to use.")
			return nil
		}
		recordOneLinerSimulationPreviewUnavailable(job, "failed", fmt.Sprintf("Could not create the temporary Simulate preview: %v", err))
		return nil
	}
	proof := runtimeActionProof(runtimeActionSimulateUpdate, result, string(runtimeaction.StatusReady))
	status := runtimeActionProofStatus(proof)
	if !runtimeActionStatusAllowed(status, "passed", string(runtimeaction.StatusReady), string(runtimeaction.StatusVerified)) {
		recordOneLinerSimulationPreviewProof(job, proof, status)
		job.mutateResult(func(result map[string]interface{}) {
			result["simulation_preview_error"] = fmt.Sprintf("Simulate preview returned status %q.", status)
		})
		return nil
	}
	recordOneLinerSimulationPreviewProof(job, proof, status)
	return nil
}

func provisionReleaseOneLinerInstallCommand(job *Job) {
	job.mutateResult(func(result map[string]interface{}) {
		result["server_install_command_released"] = true
		result["install_command_release_state"] = "released"
		result["simulation_preview_ttl_seconds"] = 3600
	})
}

func recordOneLinerSimulationPreviewUnavailable(job *Job, status, message string) {
	provisionReleaseOneLinerInstallCommand(job)
	job.mutateResult(func(result map[string]interface{}) {
		result["simulation_preview_status"] = status
		result["simulation_preview_error"] = message
	})
}

func recordOneLinerSimulationPreviewProof(job *Job, proof map[string]interface{}, status string) {
	provisionReleaseOneLinerInstallCommand(job)
	job.mutateResult(func(result map[string]interface{}) {
		runtimeProof, _ := result["runtime_proof"].(map[string]interface{})
		if runtimeProof == nil {
			runtimeProof = map[string]interface{}{}
			result["runtime_proof"] = runtimeProof
		}
		runtimeProof["simulation"] = proof
		result["simulation_preview_status"] = status
		copyRuntimeActionString(result, proof, "simulation_id", "simulation_preview_id")
		copyRuntimeActionString(result, proof, "deployment_id", "simulation_preview_deployment_id")
		copyRuntimeActionString(result, proof, "preview_url", "simulation_preview_url")
		copyRuntimeActionString(result, proof, "expires_at", "simulation_preview_expires_at")
		if nodeIDs, ok := proof["node_ids"]; ok {
			result["simulation_preview_node_ids"] = nodeIDs
		}
		if release, ok := proof["install_command_release"].(map[string]interface{}); ok {
			result["install_command_release"] = release
			if state := resultString(release, "state"); state == "released" {
				result["install_command_release_state"] = state
			}
		}
		result["server_install_command_released"] = true
	})
}

func provisionSimulationPreviewContext(ctx context.Context, job *Job, spec *core.KombinationSpec) context.Context {
	if identity.FromContext(ctx) != nil || job == nil {
		return ctx
	}
	actor := jobActorFromPayload(job.Payload)
	roles := compactStrings(actor.Roles)
	if len(roles) == 0 {
		return ctx
	}
	userID := firstNonEmpty(actor.UserID, managedRuntimeOwnerID(job, spec))
	tenantID := firstNonEmpty(actor.TenantID, managedRuntimeTenantID(job, spec))
	return identity.NewContext(ctx, &identity.Identity{
		UserID: userID,
		OrgID:  tenantID,
		Email:  actor.Email,
		Roles:  roles,
	})
}

func copyRuntimeActionString(target map[string]interface{}, proof map[string]interface{}, sourceKey, targetKey string) {
	if value := resultString(proof, sourceKey); value != "" {
		target[targetKey] = value
	}
}

// provisionValidateSpec extracts the spec from the job payload and parses it
// into a KombinationSpec, handling the StackKits, full-spec, and wizard formats.
func provisionValidateSpec(job *Job, q *Queue) (any, *core.KombinationSpec, error) {
	// ============================================================
	// STEP 1: VALIDATE - Extract and validate spec (PreValidation)
	// ============================================================
	job.setStep(StepValidate)
	q.UpdateProgress(job.ID, 5, "Pre-validating configuration...")

	specData, ok := job.Payload["spec"]
	if !ok {
		return nil, nil, wrapProvisionError(StepValidate, "missing 'spec' in job payload",
			"The stack creation request did not include a configuration. Please try again from the wizard.")
	}

	// Convert spec from UI format to KombinationSpec
	specJSON, err := json.Marshal(specData)
	if err != nil {
		return nil, nil, wrapProvisionError(StepValidate, fmt.Sprintf("failed to marshal spec: %v", err),
			"The configuration format is invalid. This is likely a bug in the UI.")
	}

	// Load spec - handle both full KombinationSpec and UI wizard format.
	dataMap, ok := specData.(map[string]interface{})
	if !ok {
		return nil, nil, wrapProvisionError(StepValidate, "spec must be a JSON object",
			"The stack creation request did not include a valid configuration object.")
	}

	var spec *core.KombinationSpec
	if dataMap["stackkit"] != nil {
		parsed, parseErr := convertUIConfigToSpec(specData)
		if parseErr != nil {
			return nil, nil, wrapProvisionError(StepValidate, fmt.Sprintf("failed to parse StackKits spec: %v", parseErr),
				"Could not parse the StackKits stack-spec. Please check your wizard selections and try again.")
		}
		spec = parsed
	} else if _, hasNodes := dataMap["nodes"]; hasNodes || dataMap["kit"] != nil {
		// Full spec format
		var parsed core.KombinationSpec
		if err := json.Unmarshal(specJSON, &parsed); err != nil {
			return nil, nil, wrapProvisionError(StepValidate, fmt.Sprintf("failed to parse spec JSON: %v", err),
				"The configuration format is invalid. This is likely a bug in the UI.")
		}
		spec = &parsed
	} else {
		// Wizard UI format -> map to spec with defaults
		parsed, parseErr := convertUIConfigToSpec(specData)
		if parseErr != nil {
			return nil, nil, wrapProvisionError(StepValidate, fmt.Sprintf("failed to parse spec: %v", parseErr),
				"Could not parse the configuration. Please check your wizard selections and try again.")
		}
		spec = parsed
	}

	q.UpdateProgress(job.ID, 8, "Pre-validation passed")
	return specData, spec, nil
}

// provisionResolveStackKit creates review metadata for the proposed StackSpec.
// It deliberately does not validate or resolve the StackKits-owned contract.
func provisionResolveStackKit(_ *ProvisionConfig, job *Job, q *Queue, spec *core.KombinationSpec) (*core.RequirementsSpec, error) {
	// ============================================================
	// STEP 2: SAVE_CONFIG - Saving configuration
	// ============================================================
	job.setStep(StepSaveConfig)
	q.UpdateProgress(job.ID, 10, "Saving configuration...")

	q.UpdateProgress(job.ID, 12, "Preparing reviewable StackSpec proposal...")

	// ============================================================
	// STEP 3: FIND_STACKKIT - Review proposed StackKit
	// ============================================================
	job.setStep(StepFindStackKit)
	q.UpdateProgress(job.ID, 18, "Preparing StackKit choice for operator review...")

	requirementsSpec, err := buildReviewableRequirements(spec)
	if err != nil {
		return nil, wrapProvisionError(
			StepFindStackKit,
			fmt.Sprintf("failed to prepare StackSpec proposal: %v", err),
			"Could not prepare the configuration proposal for review.",
		)
	}
	q.UpdateProgress(job.ID, 25, "StackSpec proposal ready for review")
	return requirementsSpec, nil
}

// provisionArtifacts holds the persisted-spec paths and hashes produced during
// the provision STEP 4 persistence phase.
type provisionArtifacts struct {
	intentPath    string
	intentHash    string
	stackSpecPath string
	stackSpecHash string
	reqPath       string
}

// provisionPersistArtifacts persists the byte-exact intent, the optional
// StackKits handoff spec, and the derived requirements-spec.yaml.
func provisionPersistArtifacts(cfg *ProvisionConfig, job *Job, q *Queue, spec *core.KombinationSpec, specData any, requirementsSpec *core.RequirementsSpec) (*provisionArtifacts, error) {
	// ============================================================
	// STEP 4: Persist Intent + Requirements (system-owned outputs)
	// ============================================================
	job.setStep(StepCreateSpec)
	q.UpdateProgress(job.ID, 60, "Persisting intent + requirements...")

	persister, err := newSpecPersister(cfg, job.TargetID)
	if err != nil {
		return nil, wrapProvisionError(StepCreateSpec, fmt.Sprintf("failed to init persister: %v", err),
			"Could not initialize spec persistence.")
	}

	stackSpecBytes, stackSpecBytesErr := stackKitSpecBytesForPayload(specData)
	if stackSpecBytesErr != nil {
		return nil, wrapProvisionError(StepCreateSpec, fmt.Sprintf("failed to serialize StackKits spec: %v", stackSpecBytesErr),
			"Could not serialize the StackKits stack-spec for the rollout handoff.")
	}

	// Persist byte-exact intent.
	var intentBytes []byte
	if raw, ok := job.Payload["intent_raw"].(string); ok && strings.TrimSpace(raw) != "" {
		intentBytes = []byte(raw)
	} else {
		loader := unifier.NewLoader()
		yamlBytes, yErr := loader.ToYAML(spec)
		if yErr != nil {
			return nil, wrapProvisionError(StepCreateSpec, fmt.Sprintf("failed to serialize intent: %v", yErr),
				"Could not serialize your configuration for persistence.")
		}
		intentBytes = yamlBytes
	}

	intentPath, intentHash, err := persister.SaveIntentBytes(intentBytes)
	if err != nil {
		return nil, wrapProvisionError(StepCreateSpec, fmt.Sprintf("failed to persist intent: %v", err),
			"Could not persist your configuration.")
	}

	stackSpecPath := ""
	stackSpecHash := ""
	if len(stackSpecBytes) > 0 {
		stackSpecPath, stackSpecHash, err = persister.SaveStackSpecBytes(stackSpecBytes)
		if err != nil {
			return nil, wrapProvisionError(StepCreateSpec, fmt.Sprintf("failed to persist StackKits spec: %v", err),
				"Could not persist the StackKits stack-spec handoff.")
		}
	}

	// Wizard-run projections (payload stack_spec_v2) persist beside the v1
	// handoff; artifact generation executes them instead of a template-derived
	// canonical document.
	if _, projErr := persistProjectedStackSpec(persister, specData); projErr != nil {
		return nil, wrapProvisionError(StepCreateSpec, fmt.Sprintf("failed to persist projected StackSpec: %v", projErr),
			"Could not persist the projected Architecture v2 spec.")
	}

	// Persist requirements-spec.yaml
	var reqPath string
	if requirementsSpec != nil {
		p, saveErr := persister.SaveRequirementsSpec(requirementsSpec, intentPath)
		if saveErr != nil {
			return nil, wrapProvisionError(StepCreateSpec, fmt.Sprintf("failed to persist requirements: %v", saveErr),
				"Could not persist derived requirements.")
		}
		reqPath = p
	}

	return &provisionArtifacts{
		intentPath:    intentPath,
		intentHash:    intentHash,
		stackSpecPath: stackSpecPath,
		stackSpecHash: stackSpecHash,
		reqPath:       reqPath,
	}, nil
}

// buildProvisionResult assembles the job.Result map for the prepared stack,
// including the managed-runtime billing/lane fields and server metadata flags.
func buildProvisionResult(job *Job, spec *core.KombinationSpec, persisted *provisionArtifacts, serverMode, providerID string, runtimePhase RuntimePhase) map[string]interface{} {
	// Generate a registration token for worker enrollment
	registrationToken := generateRegistrationToken(job.TargetID)
	serverProvisioningMode := strings.TrimSpace(spec.Metadata[metadataKeyServerProvisionMode])
	serverConnectionMode := strings.TrimSpace(spec.Metadata[metadataKeyServerConnectionMode])

	result := map[string]interface{}{
		metadataKeyStackID:            job.TargetID,
		"intent_path":                 persisted.intentPath,
		"intent_sha256":               persisted.intentHash,
		"stack_spec_path":             persisted.stackSpecPath,
		"stack_spec_sha256":           persisted.stackSpecHash,
		"requirements_path":           persisted.reqPath,
		"status":                      "requirements_ready",
		"runtime_phase":               string(runtimePhase),
		metadataKeyServerMode:         serverMode,
		metadataKeyDesiredState:       desiredStateRunning,
		metadataKeyStackKitCatalogRef: spec.Kit,
		metadataKeyVerificationStatus: verificationStatusPending,
		"registration_token":          registrationToken,
	}
	if payloadBool(job.Payload, "auto_deploy") {
		// Keep the orchestration intent recoverable when a provider wait is
		// rehydrated from the canonical stack projection after a local fence
		// loss. This is a control flag, never provider authority.
		result["auto_deploy"] = true
	}
	if serverMode == serverModeMonthlyRuntime {
		result[metadataKeyRuntimeLane] = runtimeLaneFromProvider(providerID)
		result[metadataKeyRuntimeOfferingID] = firstNonEmpty(spec.Metadata[metadataKeyRuntimeOfferingID], defaultRuntimeOfferingID)
		result[metadataKeyProviderID] = providerID
		result[metadataKeySimulateLifecycle] = simulateLifecycleFromProvider(providerID)
		result[metadataKeyBillingMode] = billingModeSubscription
		result[metadataKeyBillingCadence] = billingCadenceFromProvider(providerID)
		result[metadataKeyScenarioID] = firstNonEmpty(spec.Metadata[metadataKeyScenarioID], job.TargetID+":"+providerID)
		result[metadataKeyServerProvisionMode] = firstNonEmpty(serverProvisioningMode, serverProvisionModeKombifyCloud)
		result[metadataKeyServerConnectionMode] = firstNonEmpty(serverConnectionMode, serverConnectionManagedSub)
	} else {
		if serverProvisioningMode != "" {
			result[metadataKeyServerProvisionMode] = serverProvisioningMode
		}
		if serverConnectionMode != "" {
			result[metadataKeyServerConnectionMode] = serverConnectionMode
		}
	}
	copyBoolMetadata(result, spec.Metadata, "server_remote_host_present")
	copyBoolMetadata(result, spec.Metadata, "server_remote_user_present")
	copyBoolMetadata(result, spec.Metadata, "server_remote_use_sudo")
	copyBoolMetadata(result, spec.Metadata, "server_install_command_required")
	copyStringMetadata(result, spec.Metadata, "server_remote_auth_method")
	if credentialRef := firstNonEmpty(spec.Metadata["server_remote_credential_ref"], spec.Metadata["server_remote_ssh_key_label"]); credentialRef != "" {
		result["server_remote_credential_ref"] = credentialRef
	}
	return result
}

// applyProvisionRequirements writes the RequirementsSpec summary into the job
// result (with a fallback when Analyze did not produce a requirements spec).
func applyProvisionRequirements(result map[string]interface{}, requirementsSpec *core.RequirementsSpec) {
	// Add real RequirementsSpec from Unifier.Analyze() if available
	if requirementsSpec != nil {
		result["requirements"] = map[string]interface{}{
			"stackKit":            requirementsSpec.StackKit,
			"detectedAddons":      requirementsSpec.DetectedAddons,
			"minCloudServers":     requirementsSpec.RequiredWorkers.MinCloudServers,
			"minLocalServers":     requirementsSpec.RequiredWorkers.MinLocalServers,
			"minTotalServers":     requirementsSpec.RequiredWorkers.MinCloudServers + requirementsSpec.RequiredWorkers.MinLocalServers,
			"minRAM":              requirementsSpec.RequiredWorkers.MinRAM,
			"minCPU":              requirementsSpec.RequiredWorkers.MinCPU,
			"specialRequirements": requirementsSpec.RequiredWorkers.SpecialRequirements,
			"requiredCredentials": requirementsSpec.RequiredCredentials,
			"requiredPreChecks":   requirementsSpec.RequiredPreChecks,
			"description":         requirementsSpec.Description,
			"appliedDefaults":     requirementsSpec.AppliedDefaults,
		}
	} else {
		// Fallback for when Analyze fails
		result["requirements"] = map[string]interface{}{
			"minTotalServers": 1,
			"description":     "Connect at least 1 worker to deploy this stack",
			"details": []string{
				"Run the install command on your server(s)",
				"Workers will automatically connect and receive deployment instructions",
			},
		}
	}

	// Keep backwards-compatible summary in result for the UI.
	if requirementsSpec != nil {
		result["stack_name"] = requirementsSpec.IntentName
		result["stack_kit"] = requirementsSpec.StackKit
		result["requirements"] = map[string]interface{}{
			"stackKit":            requirementsSpec.StackKit,
			"detectedAddons":      requirementsSpec.DetectedAddons,
			"minCloudServers":     requirementsSpec.RequiredWorkers.MinCloudServers,
			"minLocalServers":     requirementsSpec.RequiredWorkers.MinLocalServers,
			"minTotalServers":     requirementsSpec.RequiredWorkers.MinCloudServers + requirementsSpec.RequiredWorkers.MinLocalServers,
			"minRAM":              requirementsSpec.RequiredWorkers.MinRAM,
			"minCPU":              requirementsSpec.RequiredWorkers.MinCPU,
			"specialRequirements": requirementsSpec.RequiredWorkers.SpecialRequirements,
			"requiredCredentials": requirementsSpec.RequiredCredentials,
			"requiredPreChecks":   requirementsSpec.RequiredPreChecks,
			"description":         requirementsSpec.Description,
			"appliedDefaults":     requirementsSpec.AppliedDefaults,
		}
	}
}

// provisionCreateManagedLease creates or binds the managed VM lease for managed
// cloud stacks and copies the resulting runtime target onto the job result.
func provisionCreateManagedLease(ctx context.Context, cfg *ProvisionConfig, job *Job, q *Queue, spec *core.KombinationSpec, requirementsSpec *core.RequirementsSpec, providerID string, runtimePhase RuntimePhase, prepared *ManagedLeaseRequest) error {
	job.setStep(StepCreateLease)
	q.UpdateProgress(job.ID, 82, "Creating managed VM lease...")
	breadcrumbStep(ctx, StepCreateLease, "creating managed VM lease", map[string]interface{}{
		providerField: providerID,
		"stack_kit":   spec.Kit,
	})
	if cfg.RuntimeActions.LeaseManager == nil {
		return wrapProvisionError(StepCreateLease,
			"managed VM lease manager is not configured",
			"Managed cloud stacks require a VM lease manager before rollout preparation can finish.")
	}
	canonicalProviderID, err := providercatalog.ResolveCanonicalProviderID(providerID, spec.Metadata[metadataKeyProviderID])
	if err != nil {
		return wrapProvisionError(StepCreateLease, fmt.Sprintf("invalid managed provider identity: %v", err),
			"Select provider_id centron or ionos before creating a managed server.")
	}
	if err := providercatalog.ValidateNoLegacyProviderFields(
		spec.Metadata[metadataKeyLeaseProvider],
		spec.Metadata[metadataKeySimulateProviderID],
	); err != nil {
		return wrapProvisionError(StepCreateLease, fmt.Sprintf("invalid managed provider identity: %v", err),
			"Remove legacy provider fields and select a canonical provider_id.")
	}
	if err := applyManagedRuntimeOfferingRequirements(job, spec, requirementsSpec); err != nil {
		return err
	}
	leaseOwnerID := managedRuntimeOwnerID(job, spec)
	leaseTenantID := managedRuntimeTenantID(job, spec)
	leaseRequest := primaryManagedLeaseRequest(spec, job.TargetID, leaseTenantID, leaseOwnerID, canonicalProviderID)
	if prepared != nil {
		if strings.TrimSpace(prepared.StackID) != strings.TrimSpace(job.TargetID) ||
			strings.TrimSpace(prepared.TenantID) != strings.TrimSpace(leaseTenantID) ||
			strings.TrimSpace(prepared.OwnerID) != strings.TrimSpace(leaseOwnerID) ||
			strings.TrimSpace(prepared.Provider) != canonicalProviderID ||
			strings.TrimSpace(prepared.OperationKey) != PrimaryManagedLeaseOperationKey ||
			strings.TrimSpace(prepared.RuntimeSlotKey) != PrimaryManagedRuntimeSlotKey ||
			prepared.RuntimeSlotGeneration != 1 {
			return wrapProvisionError(StepCreateLease, "prepared managed lease request does not match the provision job", "The admitted managed server identity is inconsistent.")
		}
		leaseRequest = *prepared
	}
	leaseResult, leaseErr := cfg.RuntimeActions.LeaseManager.CreateOrBindLease(ctx, leaseRequest)
	if leaseErr != nil {
		captureJobError(ctx, leaseErr, map[string]interface{}{
			stepField:     StepCreateLease,
			providerField: canonicalProviderID,
			stackIDField:  job.TargetID,
			tenantIDField: leaseTenantID,
			"owner_id":    leaseOwnerID,
		})
		return wrapProvisionError(StepCreateLease, leaseErr.Error(),
			"Could not create or bind the managed cloud VM lease.")
	}
	if leaseResult != nil {
		leaseMetadata, normalizeErr := monthlyruntime.NormalizeFreshMetadata(spec.Metadata, monthlyruntime.OfferingIDFromMetadata(spec.Metadata))
		if normalizeErr != nil {
			return wrapProvisionError(StepCreateLease, normalizeErr.Error(), "Managed provider metadata is not canonical.")
		}
		runtimePhase = leaseResult.Phase
		if runtimePhase == "" {
			runtimePhase = RuntimePhaseLeaseReady
		}
		job.mutateResult(func(result map[string]interface{}) {
			result["runtime_phase"] = string(runtimePhase)
			result[leaseIDField] = leaseResult.LeaseID
			if operationID := strings.TrimSpace(leaseResult.OperationID); operationID != "" {
				// The durable operation identity is the only provider-neutral
				// correlation needed to rehydrate this wait after a local fence
				// loss. It never authorizes a new provider create.
				result["operation_id"] = operationID
			}
			result[metadataKeyProviderID] = firstNonEmpty(leaseResult.Provider, canonicalProviderID)
			result[metadataKeyDesiredState] = firstNonEmpty(leaseResult.DesiredState, desiredStateRunning)
			result[metadataKeyBillingMode] = firstNonEmpty(leaseResult.BillingMode, billingModeSubscription)
			result[metadataKeyScenarioID] = firstNonEmpty(spec.Metadata[metadataKeyScenarioID], job.TargetID+":"+firstNonEmpty(leaseResult.Provider, canonicalProviderID))
			if providerRegion := firstNonEmpty(leaseMetadata[metadataKeyProviderRegion], leaseMetadata[metadataKeyIONOSDatacenter]); providerRegion != "" {
				result[metadataKeyProviderRegion] = providerRegion
			}
			if ionosDatacenter := strings.TrimSpace(leaseMetadata[metadataKeyIONOSDatacenter]); ionosDatacenter != "" {
				result[metadataKeyIONOSDatacenter] = ionosDatacenter
			}
			if leaseTenantID != "" {
				result[tenantIDField] = leaseTenantID
			}
			if leaseOwnerID != "" {
				result["owner_id"] = leaseOwnerID
			}
		})
		copyManagedRuntimeTargetToJob(job, leaseResult.Target)
	}
	return nil
}

// PrimaryManagedLeaseRequestFromUIConfig builds the exact native admission
// request used by the provision worker from the same UI-to-KombinationSpec
// conversion. HTTP admission can therefore reserve the primary server before
// a provider-capable job becomes claimable without maintaining a second
// interpretation of StackKit, services, offering, or provider metadata.
func PrimaryManagedLeaseRequestFromUIConfig(
	data map[string]interface{}, stackID, stackName, tenantID, ownerID string,
) (ManagedLeaseRequest, error) {
	spec, err := convertUIConfigToSpec(data)
	if err != nil {
		return ManagedLeaseRequest{}, err
	}
	if strings.TrimSpace(stackName) != "" {
		spec.Name = strings.TrimSpace(stackName)
	}
	providerID, err := providercatalog.CanonicalProviderID(providerIDForSpec(spec))
	if err != nil || !isManagedCloudSpec(spec) {
		return ManagedLeaseRequest{}, fmt.Errorf("managed runtime provider spec is required")
	}
	return primaryManagedLeaseRequest(spec, stackID, tenantID, ownerID, providerID), nil
}

func primaryManagedLeaseRequest(
	spec *core.KombinationSpec, stackID, tenantID, ownerID, providerID string,
) ManagedLeaseRequest {
	requestedServices := make([]string, 0, len(spec.Services))
	for _, service := range spec.Services {
		requestedServices = append(requestedServices, service.Name)
	}
	return ManagedLeaseRequest{
		StackID: stackID, StackName: spec.Name, StackKit: spec.Kit,
		TenantID: tenantID, OwnerID: ownerID, Provider: providerID,
		OperationKey:   PrimaryManagedLeaseOperationKey,
		RuntimeSlotKey: PrimaryManagedRuntimeSlotKey, RuntimeSlotGeneration: 1,
		NodeRole: "foundation", Services: requestedServices, Metadata: spec.Metadata,
	}
}

func applyManagedRuntimeOfferingRequirements(job *Job, spec *core.KombinationSpec, requirementsSpec *core.RequirementsSpec) error {
	if spec == nil || requirementsSpec == nil {
		return nil
	}
	required := requirementsSpec.RequiredWorkers
	if required.MinCPU <= 0 && required.MinRAM <= 0 {
		return nil
	}
	capacityStatus := "satisfies_requirements"
	selected, ok := monthlyruntime.OfferingForMinimumResources(required.MinCPU, required.MinRAM)
	if !ok {
		selected, ok = monthlyruntime.LargestOffering()
		if !ok {
			return wrapProvisionError(StepCreateLease,
				fmt.Sprintf("no managed runtime offering available for requirements: minCPU=%d minRAM=%dMB", required.MinCPU, required.MinRAM),
				"Die Managed-VM kann nicht bereitgestellt werden, weil kein monatliches Runtime-Angebot im aktuellen Katalog verfuegbar ist.")
		}
		capacityStatus = "below_requirements"
	}
	if spec.Metadata == nil {
		spec.Metadata = map[string]string{}
	}
	spec.Metadata[metadataKeyRuntimeOfferingID] = string(selected.ID)
	spec.Metadata["runtime_offering_capacity_status"] = capacityStatus
	spec.Metadata["runtime_required_min_cpu"] = fmt.Sprintf("%d", required.MinCPU)
	spec.Metadata["runtime_required_min_ram_mb"] = fmt.Sprintf("%d", required.MinRAM)
	job.mutateResult(func(result map[string]interface{}) {
		result[metadataKeyRuntimeOfferingID] = string(selected.ID)
		result["runtime_offering_capacity_status"] = capacityStatus
		result["runtime_required_min_cpu"] = required.MinCPU
		result["runtime_required_min_ram_mb"] = required.MinRAM
	})
	return nil
}

// provisionMaybeAutoDeploy chains into DeployHandler only after the canonical
// control plane proves an exact, fresh Guard runtime. The bool result reports
// whether the auto-deploy path owns the caller's return value.
func provisionMaybeAutoDeploy(ctx context.Context, cfg *ProvisionConfig, job *Job, q *Queue) (bool, error) {
	if !payloadBool(job.Payload, "auto_deploy") {
		return false, nil
	}
	if cfg == nil || cfg.AutoDeployAdmission == nil {
		return true, ErrAutoDeployAdmissionUnavailable
	}
	snapshot := job.Snapshot()
	request := AutoDeployAdmissionRequest{
		StackID:  strings.TrimSpace(snapshot.TargetID),
		TenantID: firstNonEmpty(stringFromMap(snapshot.Payload, tenantIDField), stringFromMap(snapshot.Result, tenantIDField)),
		OwnerID:  firstNonEmpty(stringFromMap(snapshot.Payload, "owner_id"), stringFromMap(snapshot.Result, "owner_id")),
		LeaseID:  firstNonEmpty(stringFromMap(snapshot.Result, leaseIDField), stringFromMap(snapshot.Payload, leaseIDField)),
	}
	if err := cfg.AutoDeployAdmission(ctx, request); err != nil {
		return true, autoDeployGuardWaitError(cfg, job, q, err)
	}
	clearAutoDeployGuardWaitStart(job)
	job.mutateResult(func(result map[string]interface{}) {
		result["auto_deploy_guard_verified_at"] = time.Now().UTC().Format(time.RFC3339Nano)
	})
	preparedResult := cloneJobResult(job.Snapshot().Result)
	q.UpdateProgress(job.ID, 90, "Requirements ready - starting rollout...")
	job.prepareDeployPayload()
	if err := DeployHandler(cfg)(ctx, job, q); err != nil {
		return true, err
	}
	mergeJobResultDefaults(job, preparedResult)
	return true, nil
}

func autoDeployGuardWaitError(cfg *ProvisionConfig, job *Job, q *Queue, cause error) error {
	now := time.Now().UTC()
	timeout, interval := managedRuntimeTargetWaitConfig(cfg)
	startedAt := now
	snapshot := job.Snapshot()
	if raw := firstNonEmpty(
		strings.TrimSpace(stringFromMap(snapshot.Result, autoDeployGuardWaitStartedAtField)),
		strings.TrimSpace(stringFromMap(snapshot.Payload, autoDeployGuardWaitStartedAtField)),
	); raw != "" {
		if parsed, err := time.Parse(time.RFC3339Nano, raw); err == nil && !parsed.IsZero() {
			startedAt = parsed.UTC()
		}
	} else {
		setAutoDeployGuardWaitStart(job, now)
	}
	if timeout <= 0 || now.Sub(startedAt) >= timeout {
		return fmt.Errorf("%w after %s: %v", ErrAutoDeployAdmissionTimeout, timeout, cause)
	}
	remaining := timeout - now.Sub(startedAt)
	resumeAfter := managedRuntimeEnrollmentBoundedDelay(managedRuntimeEnrollmentResumeDelay(interval), remaining)
	q.UpdateProgress(job.ID, 90, "Managed runtime prepared; waiting for fresh canonical Guard evidence...")
	return &JobWaitError{
		Reason:      WaitReasonCanonicalGuardEvidence,
		Message:     "Managed runtime is prepared; rollout is waiting for a fresh canonical Guard heartbeat.",
		ResumeAfter: resumeAfter,
		Cause:       cause,
	}
}

func setAutoDeployGuardWaitStart(job *Job, startedAt time.Time) {
	if job == nil {
		return
	}
	job.mutateResult(func(result map[string]interface{}) {
		result[autoDeployGuardWaitStartedAtField] = startedAt.UTC().Format(time.RFC3339Nano)
	})
}

func clearAutoDeployGuardWaitStart(job *Job) {
	if job == nil {
		return
	}
	job.mu.Lock()
	defer job.mu.Unlock()
	delete(job.Result, autoDeployGuardWaitStartedAtField)
	delete(job.Payload, autoDeployGuardWaitStartedAtField)
	job.Result = cloneJobMap(job.Result)
	job.Payload = cloneJobMap(job.Payload)
}
