// Package jobs provides async job processing for kombifyTechstack.
// This file holds the job-handler config, the DestroyHandler, default-handler
// registration, and the StackKit runtime-action/identity-handoff helpers.
// The provision and deploy handlers live in focused sibling files:
//   - provision_handler.go: ProvisionHandler + its phase helpers
//   - deploy_handler.go: DeployHandler + rollout phases + artifact generation
//   - payload.go: job-payload parsing + worker requirement checks
//   - managed_runtime.go: managed VM lease target resolution + spec hydration
//   - stackkit_spec.go: StackKits handoff-spec serialization + hydration
//   - ui_converter.go, terraform.go, errors.go: conversion + error helpers
package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kombifyio/techstack/internal/portinventory"
	"github.com/kombifyio/techstack/internal/runtimeproduct/runtimeaction"
	"github.com/kombifyio/techstack/pkg/stackrouting"
	"github.com/kombifyio/techstack/pkg/tofu"
	"github.com/kombifyio/techstack/pkg/unifier"
	"github.com/google/uuid"
)

// Step IDs that match the frontend task list in tasks.ts
// IMPORTANT: These are UNIFIER steps - they run BEFORE any server exists!
const (
	StepValidate      = "validate"       // Validating configuration
	StepSaveConfig    = "save_config"    // Saving your configuration
	StepFindStackKit  = "find_stackkit"  // Finding the best StackKit
	StepUnifyServices = "unify_services" // Identifying services & best practices
	StepUnifyNetwork  = "unify_network"  // Configuring network settings
	StepUnifySecurity = "unify_security" // Setting up security configuration
	StepUnifyAuth     = "unify_auth"     // Configuring authentication
	StepCreateSpec    = "create_spec"    // Creating deployment spec

	// Rollout (deploy) steps
	StepValidateWorkers    = "validate_workers"                          // Validating worker availability
	StepGenerateUnified    = "generate_unified"                          // Creating final UnifiedSpec
	StepPersistUnified     = "persist_unified"                           // Persisting unified-spec.yaml
	StepGenerateIaC        = "generate_iac"                              // Generating IaC files
	StepCreateLease        = "create_lease"                              // Creating or binding managed VM lease
	StepPrepareRollout     = "prepare_rollout"                           // Preparing rollout from persisted specs
	StepRuntimeConnected   = "runtime_connected"                         // Managed runtime target is reachable enough for orchestration
	StepTelemetryHandshake = "telemetry_handshake"                       // Initial runtime telemetry handshake
	StepSimulationGate     = string(runtimeaction.ActionSimulateUpdate)  // Validating rollout against simulation gate
	StepPortAdmission      = "port_admission"                            // Reserving compiler-declared runtime listeners
	StepStackKitPrepare    = "stackkit_prepare"                          // Running StackKits CLI prepare contract
	StepDockerReady        = "docker_ready"                              // Docker runtime prepared by StackKits CLI
	StepOpenTofuReady      = "opentofu_ready"                            // OpenTofu ready for StackKits CLI
	StepTerramateReady     = "terramate_ready"                           // Terramate ready when the StackKit lifecycle requires it
	StepTelemetryReady     = "telemetry_ready"                           // Runtime telemetry ready for Day-2/RIL operations
	StepRolloutRunner      = string(runtimeaction.ActionStackKitRollout) // Running StackKits-owned rollout operation
	StepServiceInventory   = "service_inventory"                         // Reading service inventory after rollout
	StepVerifyRollout      = string(runtimeaction.ActionVerifyRollout)   // Verifying login-protected services
	StepRestoreDrill       = string(runtimeaction.ActionRestoreDrill)    // Verifying backup restore

	// Legacy steps for DestroyHandler (actual infrastructure operations)
	StepProvision = "provision" // Running OpenTofu operations
	StepFinalize  = "finalize"  // Cleanup and finalization

	leaseIDField                  = "lease_id"
	resourceGenerationDigestField = "resource_generation_digest"
	providerField                 = "provider"
	reasonField                   = "reason"
	resultStatusField             = "status"
	stackIDField                  = "stack_id"
	stepField                     = "step"
	tenantIDField                 = "tenant_id"

	// DestroyWorkspaceStateResultField records the exact terminal condition of
	// a destroy handler. A missing workspace is a valid local outcome, but the
	// control plane still needs to retire its own stack projection.
	DestroyWorkspaceStateResultField = "destroy_workspace_state"
	DestroyWorkspaceStateAbsent      = "absent"
	// DestroyProjectionReconciledResultField is durable evidence that the
	// configured projection reconciler retired the exact requested stack.
	DestroyProjectionReconciledResultField = "destroy_projection_reconciled"
)

// ProvisionConfig holds configuration for provisioning operations.
type ProvisionConfig struct {
	// WorkDir is the base directory for OpenTofu workspaces
	WorkDir string
	// StackKitsDir is the directory containing StackKit templates
	StackKitsDir string
	// SpecBaseDir overrides where persisted specs are written.
	// When empty, defaults to ~/.techstack/stacks/<stack-id>/.
	// Primarily used for tests.
	SpecBaseDir string
	// RuntimeActions wires managed VM leases, simulation gates, and rollout verification.
	RuntimeActions RuntimeActions
	// StackKitCommander dispatches closed lifecycle commands to the enrolled
	// node Agent. When configured, deploy apply uses this product boundary
	// instead of the retired embedded StackKits HTTP action server.
	StackKitCommander StackKitCommandSender
	// ManagedStackKitInventory binds managed Cloud rollouts to control-plane
	// custody and the exact node-side Operations process. Cloud-kit apply is
	// unavailable when this authority is absent.
	ManagedStackKitInventory ManagedStackKitInventoryBuilder
	// PortInventory is the provider-neutral host-listener authority. It resolves
	// the current RuntimeServer generation internally; jobs never supply one.
	PortInventory portinventory.CurrentAuthority
	// RuntimeActionTimeout bounds a single StackKits runtime action.
	// Empty defaults to the production HTTP action budget; tests may lower it.
	RuntimeActionTimeout time.Duration
	// ManagedRuntimeTargetWaitTimeout bounds the cumulative enrollment wait.
	// Each observation has a smaller bounded attempt timeout; a pending target
	// yields the worker and resumes through the queue instead of polling inside
	// the handler.
	ManagedRuntimeTargetWaitTimeout time.Duration
	// ManagedRuntimeTargetPollInterval controls the delay between queue-backed
	// resolution attempts while enrollment remains pending.
	ManagedRuntimeTargetPollInterval time.Duration
	// RoutingStore supplies a revisioned desired-state overlay. Deploy applies
	// it after loading immutable intent and before deriving rollout artifacts.
	RoutingStore stackrouting.Store
	// AutoDeployAdmission is the canonical control-plane gate used before a
	// provision job may chain into DeployHandler. The hook must prove the exact
	// tenant/owner/stack/lease binding and a fresh Guard runtime. A missing hook
	// fails closed; provider allocation or an approved Worker is never enough.
	AutoDeployAdmission AutoDeployAdmission
	// NoWorkspaceDestroyReconciler retires the exact control-plane stack
	// projection after an explicitly requested destroy finds no local workspace.
	// It is intentionally a narrow callback: the jobs package never selects a
	// provider, deletes a provider resource, or touches legacy projections.
	NoWorkspaceDestroyReconciler NoWorkspaceDestroyReconciler
}

type ManagedStackKitInventoryRequest struct {
	TenantID         string
	StackID          string
	ResolvedPlan     []byte
	StackKitsVersion string
	CandidateDigest  string
	ValidFor         time.Duration
}

type ManagedStackKitInventoryBuilder interface {
	Build(context.Context, ManagedStackKitInventoryRequest) ([]byte, error)
}

// AutoDeployAdmissionRequest is the immutable identity envelope checked before
// a provision job can invoke rollout side effects.
type AutoDeployAdmissionRequest struct {
	StackID  string
	TenantID string
	OwnerID  string
	LeaseID  string
}

// AutoDeployAdmission proves that a prepared managed runtime is currently safe
// to hand to DeployHandler. Implementations must be read-only.
type AutoDeployAdmission func(context.Context, AutoDeployAdmissionRequest) error

// NoWorkspaceDestroyReconcileRequest is the immutable ownership envelope for
// retiring a no-workspace destroy projection. The handler derives every field
// from the durable job; callers cannot substitute a different stack.
type NoWorkspaceDestroyReconcileRequest struct {
	StackID  string
	TenantID string
	OwnerID  string
}

// NoWorkspaceDestroyReconciler reconciles the control-plane projection of an
// already requested, no-workspace destroy. It must be idempotent and may return
// JobWaitError when its own durable authority is temporarily unavailable.
type NoWorkspaceDestroyReconciler func(context.Context, NoWorkspaceDestroyReconcileRequest) error

// DefaultProvisionConfig returns a default configuration.
func DefaultProvisionConfig() *ProvisionConfig {
	return &ProvisionConfig{
		WorkDir:      filepath.Join(os.TempDir(), "techstack-provision"),
		StackKitsDir: unifier.DefaultStackKitsDir(),
		SpecBaseDir:  defaultSpecBaseDir(),
	}
}

func normalizeProvisionConfig(cfg *ProvisionConfig) *ProvisionConfig {
	if cfg == nil {
		return DefaultProvisionConfig()
	}

	normalized := *cfg
	if normalized.WorkDir == "" {
		normalized.WorkDir = filepath.Join(os.TempDir(), "techstack-provision")
	}
	if normalized.StackKitsDir == "" {
		normalized.StackKitsDir = unifier.DefaultStackKitsDir()
	}
	if normalized.SpecBaseDir == "" {
		normalized.SpecBaseDir = defaultSpecBaseDir()
	}
	return &normalized
}

func defaultSpecBaseDir() string {
	if dir := strings.TrimSpace(os.Getenv("TECHSTACK_SPEC_BASE_DIR")); dir != "" {
		return filepath.Clean(dir)
	}
	if dataDir := strings.TrimSpace(os.Getenv("TECHSTACK_DATA_DIR")); dataDir != "" {
		return filepath.Join(filepath.Clean(dataDir), "stacks")
	}
	return ""
}

func newSpecPersister(cfg *ProvisionConfig, stackID string) (*unifier.SpecPersister, error) {
	if cfg != nil && cfg.SpecBaseDir != "" {
		p, err := unifier.NewSpecPersisterWithPath(filepath.Join(cfg.SpecBaseDir, stackID))
		if err != nil {
			return nil, err
		}
		// Ensure tofu dir exists for later phases.
		if err := os.MkdirAll(filepath.Join(p.BaseDir, "tofu"), 0755); err != nil {
			return nil, fmt.Errorf("create tofu directory: %w", err)
		}
		return p, nil
	}
	return unifier.NewSpecPersister(stackID)
}

// missingStackKitIdentityHandoffFields reports which required handoff fields
// are absent from a StackKit verify/rollout response. The contract is:
//   - identity.owner.username (or .user / .login)
//   - login_gateway.url (or .login_url)
//   - identity.recovery.bundle_ref OR identity.recovery.passphrase_hash_present
//
// Returns an empty slice when the handoff is complete.
func missingStackKitIdentityHandoffFields(outputs map[string]interface{}) []string {
	missing := []string{}

	identity := mapFromInterface(outputs["identity"])
	owner := mapFromInterface(identity["owner"])
	if firstNonEmpty(
		stringFromInterface(owner["username"]),
		stringFromInterface(owner["user"]),
		stringFromInterface(owner["login"]),
	) == "" {
		missing = append(missing, "identity.owner.username")
	}

	loginGateway := mapFromInterface(outputs["login_gateway"])
	if loginGateway == nil {
		loginGateway = mapFromInterface(outputs["loginGateway"])
	}
	if loginGateway == nil {
		loginGateway = mapFromInterface(outputs["login"])
	}
	if firstNonEmpty(
		stringFromInterface(loginGateway["url"]),
		stringFromInterface(loginGateway["login_url"]),
		stringFromInterface(loginGateway["loginUrl"]),
	) == "" {
		missing = append(missing, "login_gateway.url")
	}

	recovery := mapFromInterface(identity["recovery"])
	if recovery == nil {
		recovery = mapFromInterface(outputs["recovery"])
	}
	if recovery == nil {
		recovery = mapFromInterface(outputs["recovery_bundle"])
	}
	bundleRef := firstNonEmpty(
		stringFromInterface(recovery["bundle_ref"]),
		stringFromInterface(recovery["bundleRef"]),
		stringFromInterface(recovery["recovery_bundle_ref"]),
		stringFromInterface(recovery["secret_ref"]),
		stringFromInterface(recovery["machine_secret_ref"]),
	)
	hashPresent := boolFromInterface(recovery["passphrase_hash_present"]) ||
		boolFromInterface(recovery["passphraseHashPresent"])
	if bundleRef == "" && !hashPresent {
		missing = append(missing, "identity.recovery.bundle_ref|passphrase_hash_present")
	}

	return missing
}

func mapFromInterface(value interface{}) map[string]interface{} {
	switch typed := value.(type) {
	case map[string]interface{}:
		return typed
	case map[interface{}]interface{}:
		out := make(map[string]interface{}, len(typed))
		for key, item := range typed {
			if str, ok := key.(string); ok {
				out[str] = item
			}
		}
		return out
	default:
		return map[string]interface{}{}
	}
}

func stringFromInterface(value interface{}) string {
	if s, ok := value.(string); ok {
		return strings.TrimSpace(s)
	}
	return ""
}

func boolFromInterface(value interface{}) bool {
	switch v := value.(type) {
	case bool:
		return v
	case string:
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "1", "true", "yes", "on":
			return true
		}
	}
	return false
}

func int32FromInterface(value interface{}) int32 {
	switch v := value.(type) {
	case int32:
		return v
	case int:
		return int32(v)
	case int64:
		return int32(v)
	case float64:
		return int32(v)
	case json.Number:
		n, _ := v.Int64()
		return int32(n)
	}
	return 0
}

func runRuntimeAction(ctx context.Context, runner RuntimeActionRunner, req RuntimeActionRequest) (map[string]interface{}, error) {
	if withResult, ok := runner.(RuntimeActionResultRunner); ok {
		return withResult.RunWithResult(ctx, req)
	}
	if err := runner.Run(ctx, req); err != nil {
		return nil, err
	}
	return nil, nil
}

//nolint:goconst // Runtime action response keys mirror the shared runtimeaction wire contract.
func runtimeActionProof(action string, result map[string]interface{}, defaultStatus string) map[string]interface{} {
	proof := map[string]interface{}{
		"action": action,
		"status": firstNonEmpty(resultString(result, "status"), defaultStatus),
	}
	for _, key := range []string{"mode", stackIDField, "stack_name", "stackkit", "tenant_id", "owner_id", "tofu_dir", "unified_path", "simulation_id", "deployment_id", "preview_url", "expires_at"} {
		if value := resultString(result, key); value != "" {
			proof[key] = value
		}
	}
	for _, key := range []string{"node_ids", "install_command_release"} {
		if value, ok := result[key]; ok && value != nil {
			proof[key] = value
		}
	}
	if checks, ok := result["checks"]; ok && checks != nil {
		proof["checks"] = checks
	}
	if observation := sanitizedRuntimeObservation(result); len(observation) > 0 {
		proof["observation"] = observation
	}
	return proof
}

func runtimeActionProofStatus(proof map[string]interface{}) string {
	if proof == nil {
		return ""
	}
	return resultString(proof, "status")
}

func runtimeActionStatusCountsAsVerified(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case string(runtimeaction.StatusVerified), string(runtimeaction.StatusApplied), string(runtimeaction.StatusReady), string(runtimeaction.StatusCompletedDegraded):
		return true
	default:
		return false
	}
}

func runtimeActionStatusAllowed(status string, allowed ...string) bool {
	normalized := strings.ToLower(strings.TrimSpace(status))
	for _, value := range allowed {
		if normalized == strings.ToLower(strings.TrimSpace(value)) {
			return true
		}
	}
	return false
}

func resultString(result map[string]interface{}, key string) string {
	if result == nil {
		return ""
	}
	switch value := result[key].(type) {
	case string:
		return strings.TrimSpace(value)
	case fmt.Stringer:
		return strings.TrimSpace(value.String())
	default:
		return ""
	}
}

func hasRequiredStackKitIdentityHandoff(outputs map[string]interface{}) bool {
	if outputs == nil {
		return false
	}
	identity := resultMap(outputs, "identity")
	owner := firstResultMap(resultMap(identity, "owner"), resultMap(outputs, "owner"))
	loginGateway := firstResultMap(
		resultMap(outputs, "login_gateway"),
		resultMap(outputs, "loginGateway"),
		resultMap(outputs, "login"),
	)
	recovery := firstResultMap(
		resultMap(identity, "recovery"),
		resultMap(outputs, "recovery"),
		resultMap(outputs, "recovery_bundle"),
	)

	ownerLogin := firstNonEmpty(
		resultString(owner, "username"),
		resultString(owner, "user"),
		resultString(owner, "login"),
	)
	loginURL := firstNonEmpty(
		resultString(loginGateway, "url"),
		resultString(loginGateway, "login_url"),
		resultString(loginGateway, "loginUrl"),
	)
	recoveryRef := firstNonEmpty(
		resultString(recovery, "bundle_ref"),
		resultString(recovery, "bundleRef"),
		resultString(recovery, "recovery_bundle_ref"),
		resultString(recovery, "recoveryBundleRef"),
		resultString(recovery, "secret_ref"),
		resultString(recovery, "secretRef"),
		resultString(recovery, "machine_secret_ref"),
		resultString(recovery, "machineSecretRef"),
	)
	return ownerLogin != "" && loginURL != "" && (recoveryRef != "" || resultBool(recovery, "passphrase_hash_present") || resultBool(recovery, "passphraseHashPresent"))
}

func firstResultMap(values ...map[string]interface{}) map[string]interface{} {
	for _, value := range values {
		if len(value) > 0 {
			return value
		}
	}
	return nil
}

func resultMap(result map[string]interface{}, key string) map[string]interface{} {
	if result == nil {
		return nil
	}
	switch value := result[key].(type) {
	case map[string]interface{}:
		return value
	default:
		return nil
	}
}

func resultBool(result map[string]interface{}, key string) bool {
	if result == nil {
		return false
	}
	value, _ := result[key].(bool)
	return value
}

//nolint:goconst // StackKit output keys are external response wire fields.
func mergeStackKitOutputs(dst map[string]interface{}, result map[string]interface{}) {
	if dst == nil || result == nil {
		return
	}
	if nested, ok := result[metadataKeyStackKitOutputs].(map[string]interface{}); ok {
		for key, value := range nested {
			if key != "observation" {
				dst[key] = value
			}
		}
	}
	if data, ok := result["data"].(map[string]interface{}); ok {
		mergeStackKitOutputs(dst, data)
	}
	for _, key := range []string{"identity", "login_gateway", "recovery", "services", "metadata"} {
		if value, ok := result[key]; ok {
			dst[key] = value
		}
	}
	if observation := sanitizedRuntimeObservation(result); len(observation) > 0 {
		dst["observation"] = observation
	}
}

const runtimeObservationMaxStringLength = 4096

// sanitizedRuntimeObservation keeps the versioned, measured StackKits runtime
// observation across action proof and rollout output persistence. The action
// result can cross a service boundary, so obvious credentials and unbounded
// diagnostic values are excluded before they reach job/read-model storage.
func sanitizedRuntimeObservation(result map[string]interface{}) map[string]interface{} {
	if result == nil {
		return nil
	}
	if observation, ok := result["observation"].(map[string]interface{}); ok {
		return sanitizeRuntimeObservationMap(observation, 0)
	}
	if nested, ok := result[metadataKeyStackKitOutputs].(map[string]interface{}); ok {
		if observation := sanitizedRuntimeObservation(nested); len(observation) > 0 {
			return observation
		}
	}
	if data, ok := result["data"].(map[string]interface{}); ok {
		return sanitizedRuntimeObservation(data)
	}
	return nil
}

func sanitizeRuntimeObservationMap(input map[string]interface{}, depth int) map[string]interface{} {
	if len(input) == 0 || depth > 8 {
		return nil
	}
	out := make(map[string]interface{}, len(input))
	for key, value := range input {
		key = strings.TrimSpace(key)
		if key == "" || runtimeObservationSensitiveKey(key) {
			continue
		}
		if sanitized, ok := sanitizeRuntimeObservationValue(value, depth+1); ok {
			out[key] = sanitized
		}
	}
	return out
}

func sanitizeRuntimeObservationValue(value interface{}, depth int) (interface{}, bool) {
	switch typed := value.(type) {
	case nil:
		return nil, false
	case string:
		value := strings.TrimSpace(typed)
		if len(value) > runtimeObservationMaxStringLength {
			value = value[:runtimeObservationMaxStringLength]
		}
		return value, true
	case bool, float64, float32, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, json.Number:
		return typed, true
	case map[string]interface{}:
		value := sanitizeRuntimeObservationMap(typed, depth)
		return value, len(value) > 0
	case []interface{}:
		if depth > 8 {
			return nil, false
		}
		limit := len(typed)
		if limit > 128 {
			limit = 128
		}
		out := make([]interface{}, 0, limit)
		for _, item := range typed[:limit] {
			if sanitized, ok := sanitizeRuntimeObservationValue(item, depth+1); ok {
				out = append(out, sanitized)
			}
		}
		return out, true
	default:
		return nil, false
	}
}

func runtimeObservationSensitiveKey(key string) bool {
	key = strings.ToLower(strings.TrimSpace(key))
	for _, marker := range []string{"token", "secret", "password", "credential", "authorization", "api_key", "private_key"} {
		if strings.Contains(key, marker) {
			return true
		}
	}
	return false
}

// DestroyHandler creates a job handler for destroying stacks.
func DestroyHandler(cfg *ProvisionConfig) JobHandler {
	cfg = normalizeProvisionConfig(cfg)

	return func(ctx context.Context, job *Job, q *Queue) error {
		if _, err := destroyManagedRuntimeLeases(ctx, cfg, job, q); err != nil {
			if isJobWaitError(err) {
				return err
			}
			return wrapProvisionError(StepCreateLease, fmt.Sprintf("managed runtime decommission failed: %v", err),
				"Could not decommission the managed runtime VPS. Provider resources were not marked destroyed.")
		}

		// Step 1: Locate work directory
		job.setStep(StepValidate)
		q.UpdateProgress(job.ID, 10, "Locating stack workspace...")

		workDir := filepath.Join(cfg.WorkDir, job.TargetID)
		if _, err := os.Stat(workDir); os.IsNotExist(err) {
			// No workspace found - it may have been cleaned up or never created.
			// Record that exact outcome before reconciling the separately-owned
			// control-plane projection. Returning success without this handoff
			// used to leave fresh failed stacks visible indefinitely.
			job.mutateResult(func(result map[string]interface{}) {
				result[DestroyWorkspaceStateResultField] = DestroyWorkspaceStateAbsent
			})
			if cfg.NoWorkspaceDestroyReconciler != nil {
				snapshot := job.Snapshot()
				err := cfg.NoWorkspaceDestroyReconciler(ctx, NoWorkspaceDestroyReconcileRequest{
					StackID:  snapshot.TargetID,
					TenantID: payloadString(snapshot.Payload, tenantIDField),
					OwnerID:  payloadString(snapshot.Payload, "owner_id"),
				})
				if err != nil {
					if isJobWaitError(err) {
						return err
					}
					return wrapProvisionError(StepFinalize,
						fmt.Sprintf("no-workspace stack projection reconciliation failed: %v", err),
						"Could not retire this stack entry after its no-workspace destroy. No provider resource or legacy record was changed.")
				}
				job.mutateResult(func(result map[string]interface{}) {
					result[DestroyProjectionReconciledResultField] = true
				})
			}
			q.UpdateProgress(job.ID, 100, "No workspace found, stack already destroyed")
			return nil
		}

		q.UpdateProgress(job.ID, 20, "Workspace found")

		// Step 2: Run OpenTofu destroy
		job.setStep(StepProvision)
		q.UpdateProgress(job.ID, 40, "Running OpenTofu destroy...")

		runner := &tofu.DefaultRunner{}
		if err := runner.Destroy(workDir); err != nil {
			return wrapProvisionError(StepProvision, fmt.Sprintf("tofu destroy failed: %v", err),
				"Could not destroy infrastructure. Manual cleanup may be required.")
		}

		q.UpdateProgress(job.ID, 80, "Infrastructure destroyed")

		// Step 3: Cleanup workspace (optional - keep state for audit)
		job.setStep(StepFinalize)
		q.UpdateProgress(job.ID, 90, "Cleaning up workspace...")

		// We keep the workspace for audit purposes, but mark as destroyed
		destroyedMarker := filepath.Join(workDir, ".destroyed")
		if err := os.WriteFile(destroyedMarker, []byte("destroyed"), 0644); err != nil {
			// Non-fatal: just log it
			q.UpdateProgress(job.ID, 95, "Warning: Could not mark workspace as destroyed")
		}

		q.UpdateProgress(job.ID, 100, "Destroy complete")

		return nil
	}
}

func destroyManagedRuntimeLeases(ctx context.Context, cfg *ProvisionConfig, job *Job, q *Queue) (*ManagedLeaseDecommissionResult, error) {
	if job == nil {
		return nil, fmt.Errorf("%w: destroy job is missing", ErrManagedLeaseDecommissionProofRequired)
	}
	required := job.Type == JobTypeReconcileLease
	if !required {
		rawRequired, classified := job.Payload[ManagedRuntimeDecommissionRequiredField]
		if !classified {
			return nil, fmt.Errorf("%w: destroy job has no managed-runtime classification", ErrManagedLeaseDecommissionProofRequired)
		}
		required = boolFromInterface(rawRequired)
	}
	if !required {
		return &ManagedLeaseDecommissionResult{}, nil
	}
	if cfg == nil || cfg.RuntimeActions.LeaseDecommissioner == nil {
		return nil, ErrManagedLeaseDecommissionUnavailable
	}
	tenantID := payloadString(job.Payload, tenantIDField)
	ownerID := payloadString(job.Payload, "owner_id")
	if tenantID == "" || ownerID == "" {
		return nil, fmt.Errorf("%w: managed destroy requires tenant_id and owner_id", ErrManagedLeaseDecommissionProofRequired)
	}
	q.UpdateProgress(job.ID, 5, "Decommissioning managed runtime leases...")
	result, err := cfg.RuntimeActions.LeaseDecommissioner.DecommissionManagedLeases(ctx, ManagedLeaseDecommissionRequest{
		StackID:                  job.TargetID,
		TenantID:                 tenantID,
		OwnerID:                  ownerID,
		LeaseID:                  payloadString(job.Payload, leaseIDField),
		ResourceGenerationDigest: payloadString(job.Payload, resourceGenerationDigestField),
	})
	if err != nil {
		return result, err
	}
	if err := validateManagedLeaseDecommissionProofs(ManagedLeaseDecommissionRequest{
		StackID:                  job.TargetID,
		TenantID:                 tenantID,
		OwnerID:                  ownerID,
		LeaseID:                  payloadString(job.Payload, leaseIDField),
		ResourceGenerationDigest: payloadString(job.Payload, resourceGenerationDigestField),
	}, result); err != nil {
		return result, err
	}
	if result != nil && result.Decommissioned > 0 {
		q.UpdateProgress(job.ID, 15, fmt.Sprintf("Decommissioned %d managed runtime lease(s)", result.Decommissioned))
	}
	return result, nil
}

func validateManagedLeaseDecommissionProofs(req ManagedLeaseDecommissionRequest, result *ManagedLeaseDecommissionResult) error {
	if result == nil || len(result.Proofs) == 0 {
		return ErrManagedLeaseDecommissionProofRequired
	}
	if result.Skipped != 0 {
		return fmt.Errorf("%w: decommission skipped %d authoritative candidate(s)", ErrManagedLeaseDecommissionProofRequired, result.Skipped)
	}
	leaseIDs, err := validatedManagedLeaseResultIDs(result)
	if err != nil {
		return err
	}

	decommissioned := 0
	proofLeaseIDs := make(map[string]struct{}, len(result.Proofs))
	for _, proof := range result.Proofs {
		terminalDecommission, proofErr := validateManagedLeaseDecommissionProof(
			req,
			normalizeManagedLeaseDecommissionProof(proof),
			leaseIDs,
			proofLeaseIDs,
		)
		if proofErr != nil {
			return proofErr
		}
		if terminalDecommission {
			decommissioned++
		}
	}
	if decommissioned != result.Decommissioned {
		return fmt.Errorf("%w: decommission count does not match terminal proofs", ErrManagedLeaseDecommissionProofRequired)
	}
	if len(proofLeaseIDs) != len(leaseIDs) {
		return fmt.Errorf("%w: terminal proofs do not cover every result lease", ErrManagedLeaseDecommissionProofRequired)
	}
	return nil
}

func validatedManagedLeaseResultIDs(result *ManagedLeaseDecommissionResult) (map[string]struct{}, error) {
	if len(result.LeaseIDs) != len(result.Proofs) {
		return nil, fmt.Errorf("%w: lease/proof cardinality mismatch", ErrManagedLeaseDecommissionProofRequired)
	}
	leaseIDs := make(map[string]struct{}, len(result.LeaseIDs))
	for _, leaseID := range result.LeaseIDs {
		leaseID = strings.TrimSpace(leaseID)
		if leaseID == "" {
			return nil, fmt.Errorf("%w: empty result lease_id", ErrManagedLeaseDecommissionProofRequired)
		}
		if _, duplicate := leaseIDs[leaseID]; duplicate {
			return nil, fmt.Errorf("%w: duplicate result lease_id %q", ErrManagedLeaseDecommissionProofRequired, leaseID)
		}
		leaseIDs[leaseID] = struct{}{}
	}
	return leaseIDs, nil
}

type normalizedManagedLeaseDecommissionProof struct {
	stackID          string
	tenantID         string
	leaseID          string
	providerID       string
	generationID     string
	generationDigest string
	receiptRef       string
	receiptDigest    string
	observedState    string
	verifiedAt       time.Time
}

func normalizeManagedLeaseDecommissionProof(proof ManagedLeaseDecommissionProof) normalizedManagedLeaseDecommissionProof {
	return normalizedManagedLeaseDecommissionProof{
		stackID:          strings.TrimSpace(proof.StackID),
		tenantID:         strings.TrimSpace(proof.TenantID),
		leaseID:          strings.TrimSpace(proof.LeaseID),
		providerID:       strings.ToLower(strings.TrimSpace(proof.ProviderID)),
		generationID:     strings.TrimSpace(proof.ResourceGenerationID),
		generationDigest: strings.TrimSpace(proof.ResourceGenerationDigest),
		receiptRef:       strings.TrimSpace(proof.ReceiptRef),
		receiptDigest:    strings.TrimSpace(proof.ReceiptDigest),
		observedState:    strings.ToLower(strings.TrimSpace(proof.ObservedState)),
		verifiedAt:       proof.VerifiedAt,
	}
}

func validateManagedLeaseDecommissionProof(
	req ManagedLeaseDecommissionRequest,
	proof normalizedManagedLeaseDecommissionProof,
	leaseIDs map[string]struct{},
	proofLeaseIDs map[string]struct{},
) (bool, error) {
	if !validManagedLeaseDecommissionProofIdentity(req, proof) {
		return false, fmt.Errorf("%w: incomplete or mismatched proof for lease %q", ErrManagedLeaseDecommissionProofRequired, proof.leaseID)
	}
	if requestedLeaseID := strings.TrimSpace(req.LeaseID); requestedLeaseID != "" && proof.leaseID != requestedLeaseID {
		return false, fmt.Errorf("%w: proof lease %q does not match requested lease %q", ErrManagedLeaseDecommissionProofRequired, proof.leaseID, requestedLeaseID)
	}
	if _, duplicate := proofLeaseIDs[proof.leaseID]; duplicate {
		return false, fmt.Errorf("%w: duplicate proof lease_id %q", ErrManagedLeaseDecommissionProofRequired, proof.leaseID)
	}
	proofLeaseIDs[proof.leaseID] = struct{}{}
	if requestedDigest := strings.TrimSpace(req.ResourceGenerationDigest); requestedDigest != "" && proof.generationDigest != requestedDigest {
		return false, fmt.Errorf("%w: proof generation digest does not match the claimed generation", ErrManagedLeaseDecommissionProofRequired)
	}
	if _, listed := leaseIDs[proof.leaseID]; !listed {
		return false, fmt.Errorf("%w: proof lease %q is absent from result lease IDs", ErrManagedLeaseDecommissionProofRequired, proof.leaseID)
	}
	switch proof.observedState {
	case ManagedLeaseDecommissionObservedDecommissioned:
		return true, nil
	case ManagedLeaseDecommissionObservedNotFound:
		return false, nil
	default:
		return false, fmt.Errorf("%w: provider state %q is not terminal", ErrManagedLeaseDecommissionProofRequired, proof.observedState)
	}
}

func validManagedLeaseDecommissionProofIdentity(req ManagedLeaseDecommissionRequest, proof normalizedManagedLeaseDecommissionProof) bool {
	if proof.stackID == "" || proof.stackID != strings.TrimSpace(req.StackID) {
		return false
	}
	if proof.tenantID == "" || proof.tenantID != strings.TrimSpace(req.TenantID) {
		return false
	}
	if proof.leaseID == "" || proof.providerID == "" {
		return false
	}
	parsedGeneration, err := uuid.Parse(proof.generationID)
	if err != nil || parsedGeneration.String() != proof.generationID {
		return false
	}
	if !validLowerHexDigest(proof.generationDigest) || proof.receiptRef == "" {
		return false
	}
	if !validLowerHexDigest(proof.receiptDigest) || proof.verifiedAt.IsZero() {
		return false
	}
	return true
}

func validLowerHexDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

// ReconcileLeaseHandler decommissions the single managed runtime lease named in
// the job payload (lease_id/tenant_id/owner_id) via the provider control plane,
// WITHOUT running the stack's OpenTofu destroy. It is enqueued after a forced
// decommission of an unreachable runtime so the provider VM is freed out-of-band
// and does not keep billing. Reaching DecommissionManagedLeases is the
// leak-closing step; if no lease matches it is a safe no-op.
func ReconcileLeaseHandler(cfg *ProvisionConfig) JobHandler {
	cfg = normalizeProvisionConfig(cfg)
	return func(ctx context.Context, job *Job, q *Queue) error {
		job.setStep(StepCreateLease)
		result, err := destroyManagedRuntimeLeases(ctx, cfg, job, q)
		if err != nil {
			if isJobWaitError(err) {
				return err
			}
			return wrapProvisionError(StepCreateLease, fmt.Sprintf("managed runtime lease reconciliation failed: %v", err),
				"Could not decommission the managed runtime VPS. Provider resources were not marked destroyed; a retry will re-attempt cleanup.")
		}
		if result == nil || result.Decommissioned == 0 {
			q.UpdateProgress(job.ID, 100, "No managed runtime lease to reconcile")
			return nil
		}
		q.UpdateProgress(job.ID, 100, "Managed runtime lease reconciled")
		return nil
	}
}

// RegisterDefaultHandlers registers the default job handlers on a queue.
// This includes provision, destroy, and drift detection handlers.
func RegisterDefaultHandlers(q *Queue, cfg *ProvisionConfig) {
	cfg = normalizeProvisionConfig(cfg)

	// Core provisioning handlers
	q.RegisterHandler(JobTypeProvision, ProvisionHandler(cfg))
	q.RegisterHandler(JobTypeDeploy, DeployHandler(cfg))
	q.RegisterHandler(JobTypeDestroy, DestroyHandler(cfg))
	q.RegisterHandler(JobTypeReconcileLease, ReconcileLeaseHandler(cfg))

	// Drift detection handlers
	driftCfg := &DriftCheckConfig{
		WorkDir: cfg.WorkDir,
	}
	RegisterDriftHandlers(q, driftCfg)
}
