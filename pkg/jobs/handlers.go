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
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kombifyio/techstack/internal/portinventory"
	"github.com/kombifyio/techstack/internal/runtimeproduct/runtimeaction"
	"github.com/kombifyio/techstack/pkg/secrets"
	"github.com/kombifyio/techstack/pkg/stackrouting"
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
	// PortTeardownSnapshotResultField contains the server-generated immutable
	// release batch captured before a destroy enters process-local execution.
	PortTeardownSnapshotResultField = "port_teardown_snapshot"
	// PortTeardownReleasedResultField binds completed claim release to the exact
	// durable snapshot digest consumed after terminal absence proof.
	PortTeardownReleasedResultField = "port_teardown_released"
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
	PortInventory portinventory.LifecycleAuthority
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
	// BackupScheduleProjector records the cadence a managed rollout selected so
	// the due-stack scanner can find it. Without it the projection stays empty
	// and no backup is ever due, however correct the rest of the chain is.
	BackupScheduleProjector BackupScheduleProjector
	// BackupAgentResolver resolves the enrolled agent that owns a stack's
	// runtime, at dispatch time rather than at scan time.
	BackupAgentResolver BackupAgentResolver
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
	// RemoteEnrollment drives the durable connect-remote enrollment job. When
	// nil, the job type fails closed instead of pretending the SSH lane ran.
	RemoteEnrollment RemoteEnrollmentExecutor
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
		return clampInt64ToInt32(int64(v))
	case int64:
		return clampInt64ToInt32(v)
	case float64:
		return int32(v)
	case json.Number:
		n, _ := v.Int64()
		return clampInt64ToInt32(n)
	}
	return 0
}

func clampInt64ToInt32(value int64) int32 {
	if value > math.MaxInt32 {
		return math.MaxInt32
	}
	if value < math.MinInt32 {
		return math.MinInt32
	}
	// #nosec G115 -- value is clamped to the signed 32-bit range above.
	return int32(value)
}

func runRuntimeAction(ctx context.Context, runner RuntimeActionRunner, req RuntimeActionRequest) (map[string]interface{}, error) {
	if runner == nil {
		return nil, fmt.Errorf("runtime action %s is not configured", req.Action)
	}
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
	for _, key := range []string{
		"mode", stackIDField, "stack_name", "stackkit", "tenant_id", "owner_id", "tofu_dir", "unified_path",
		"simulation_id", "deployment_id", "preview_url", "expires_at",
		"stackkit_instance_id", "plan_hash", "snapshot_anchor_id", "backup_operation_id", "retention_mode",
		"restore_result_id", "restore_operation_id", "verified_at",
	} {
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
	if apply := sanitizedStackKitApplySummary(result); len(apply) > 0 {
		proof["apply"] = apply
	}
	if outcomes := sanitizedStackKitApplyOutcomes(result); len(outcomes) > 0 {
		proof["outcomes"] = outcomes
	}
	if observation := sanitizedRuntimeObservation(result); len(observation) > 0 {
		proof["observation"] = observation
	}
	if observations := sanitizedRuntimeObservations(result); len(observations) > 0 {
		proof["observations"] = observations
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

//nolint:goconst // StackKit output keys are external response wire fields.
func mergeStackKitOutputs(dst map[string]interface{}, result map[string]interface{}) {
	if dst == nil || result == nil {
		return
	}
	if nested, ok := result[metadataKeyStackKitOutputs].(map[string]interface{}); ok {
		for key, value := range nested {
			if key != "apply" && key != "observation" && key != "observations" {
				dst[key] = value
			}
		}
	}
	if commandResult, ok := result["command_result"].(map[string]interface{}); ok {
		mergeStackKitOutputs(dst, commandResult)
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
	if observations := sanitizedRuntimeObservations(result); len(observations) > 0 {
		dst["observations"] = observations
	}
	if apply := sanitizedStackKitApplySummary(result); len(apply) > 0 {
		dst["apply"] = apply
	}
}

const stackKitEvidenceMaxStringLength = 4096

// sanitizedRuntimeObservation keeps the versioned, measured StackKits runtime
// observation across action proof and rollout output persistence. The action
// result can cross a service boundary, so obvious credentials and unbounded
// diagnostic values are excluded before they reach job/read-model storage.
func sanitizedRuntimeObservation(result map[string]interface{}) map[string]interface{} {
	if result == nil {
		return nil
	}
	if observation, ok := result["observation"].(map[string]interface{}); ok {
		return sanitizeStackKitEvidenceMap(observation, 0)
	}
	for _, nested := range nestedStackKitEvidenceMaps(result) {
		if observation := sanitizedRuntimeObservation(nested); len(observation) > 0 {
			return observation
		}
	}
	return nil
}

func sanitizedRuntimeObservations(result map[string]interface{}) []interface{} {
	if result == nil {
		return nil
	}
	if observations, ok := result["observations"].([]interface{}); ok {
		sanitized := make([]interface{}, 0, len(observations))
		for _, value := range observations {
			observation, ok := value.(map[string]interface{})
			if !ok {
				continue
			}
			if observation = sanitizeStackKitEvidenceMap(observation, 0); len(observation) > 0 {
				sanitized = append(sanitized, observation)
			}
		}
		if len(sanitized) > 0 {
			return sanitized
		}
	}
	for _, nested := range nestedStackKitEvidenceMaps(result) {
		if observations := sanitizedRuntimeObservations(nested); len(observations) > 0 {
			return observations
		}
	}
	return nil
}

func sanitizedStackKitApplyOutcomes(result map[string]interface{}) map[string]interface{} {
	if outcomes, ok := result["outcomes"].(map[string]interface{}); ok {
		return sanitizeStackKitEvidenceMap(outcomes, 0)
	}
	for _, nested := range nestedStackKitEvidenceMaps(result) {
		if outcomes := sanitizedStackKitApplyOutcomes(nested); len(outcomes) > 0 {
			return outcomes
		}
	}
	return nil
}

func sanitizedStackKitApplySummary(result map[string]interface{}) map[string]interface{} {
	if result == nil {
		return nil
	}
	if apply, ok := result["apply"].(map[string]interface{}); ok {
		return sanitizeStackKitEvidenceMap(apply, 0)
	}
	for _, nested := range nestedStackKitEvidenceMaps(result) {
		if apply := sanitizedStackKitApplySummary(nested); len(apply) > 0 {
			return apply
		}
	}
	return nil
}

func nestedStackKitEvidenceMaps(result map[string]interface{}) []map[string]interface{} {
	nested := make([]map[string]interface{}, 0, 3)
	for _, key := range []string{metadataKeyStackKitOutputs, "command_result", "data"} {
		if value, ok := result[key].(map[string]interface{}); ok {
			nested = append(nested, value)
		}
	}
	return nested
}

func sanitizeStackKitEvidenceMap(input map[string]interface{}, depth int) map[string]interface{} {
	if len(input) == 0 || depth > 8 {
		return nil
	}
	out := make(map[string]interface{}, len(input))
	for key, value := range input {
		key = strings.TrimSpace(key)
		if key == "" || secrets.SensitiveKey(key) {
			continue
		}
		if sanitized, ok := sanitizeStackKitEvidenceValue(value, depth+1); ok {
			out[key] = sanitized
		}
	}
	return out
}

func sanitizeStackKitEvidenceValue(value interface{}, depth int) (interface{}, bool) {
	switch typed := value.(type) {
	case nil:
		return nil, false
	case string:
		value := strings.TrimSpace(typed)
		if len(value) > stackKitEvidenceMaxStringLength {
			return nil, false
		}
		return secrets.Redact(value), true
	case bool, float64, float32, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, json.Number:
		return typed, true
	case map[string]interface{}:
		value := sanitizeStackKitEvidenceMap(typed, depth)
		return value, len(value) > 0
	case []interface{}:
		if depth > 8 || len(typed) > 128 {
			return nil, false
		}
		out := make([]interface{}, 0, len(typed))
		for _, item := range typed {
			if sanitized, ok := sanitizeStackKitEvidenceValue(item, depth+1); ok {
				out = append(out, sanitized)
			}
		}
		return out, true
	default:
		return nil, false
	}
}

// DestroyHandler creates a job handler for destroying stacks.
func DestroyHandler(cfg *ProvisionConfig) JobHandler {
	cfg = normalizeProvisionConfig(cfg)

	return func(ctx context.Context, job *Job, q *Queue) error {
		// Addresses go first: a kombify.me route must never outlive the server
		// it points at, because a released provider IP can be reassigned and
		// then pass ACME HTTP-01 for the stack's hostnames.
		if err := deregisterStackManagedAddresses(ctx, job, q); err != nil {
			return wrapProvisionCause(StepFinalize, err,
				"Could not remove this stack's kombify.me addresses. The decommission stays incomplete so no address keeps routing to a released server; retry the destroy.")
		}
		if _, err := destroyManagedRuntimeLeases(ctx, cfg, job, q); err != nil {
			if isJobWaitError(err) {
				return err
			}
			return wrapProvisionCause(StepCreateLease, err,
				"Could not decommission the managed runtime VPS. Provider resources were not marked destroyed.")
		}
		managedRuntime, err := managedRuntimeDecommissionRequired(job)
		if err != nil {
			return wrapProvisionCause(StepCreateLease, err,
				"Could not verify whether provider-owned runtime teardown was required. Port claims were retained.")
		}
		if managedRuntime {
			if err := releasePortTeardownSnapshotForJob(ctx, cfg, job); err != nil {
				return wrapProvisionCause(StepFinalize, fmt.Errorf("port teardown release failed: %w", err),
					"Provider absence was verified, but port claims were retained because the exact teardown snapshot could not be released.")
			}
		} else if cfg.PortInventory != nil {
			snapshot, err := boundPortTeardownSnapshot(job)
			if err != nil {
				return wrapProvisionCause(StepFinalize, fmt.Errorf("local port teardown admission failed: %w", err),
					"Port claims were retained because the exact teardown snapshot could not be verified.")
			}
			if len(snapshot.Generations) > 0 {
				if err := removeLocalStackKitWorkloads(ctx, cfg, job, snapshot); err != nil {
					return wrapProvisionCause(StepProvision, fmt.Errorf("typed local StackKits removal failed: %w", err),
						"Port claims were retained because StackKits did not prove every applied workload absent.")
				}
			}
			if err := releasePortTeardownSnapshot(ctx, cfg, job, snapshot); err != nil {
				return wrapProvisionCause(StepFinalize, fmt.Errorf("local port teardown release failed: %w", err),
					"StackKits absence was verified, but port claims were retained because the exact teardown snapshot could not be released.")
			}
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
					return wrapProvisionCause(StepFinalize,
						fmt.Errorf("no-workspace stack projection reconciliation failed: %w", err),
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

		job.setStep(StepProvision)
		q.UpdateProgress(job.ID, 40, "Removing stack with StackKits...")
		workload := firstNonEmpty(job.TargetName, job.TargetID)
		_, typedLocalRemoval := job.Snapshot().Result[LocalStackKitRemovalEvidenceResultField]
		if !typedLocalRemoval {
			if err := destroyStackKitWorkspace(ctx, workDir, workload); err != nil {
				return wrapProvisionCause(StepProvision, err,
					"Could not remove the stack with the pinned StackKits CLI.")
			}
		}

		q.UpdateProgress(job.ID, 80, "Stack removed")

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

// deregisterStackManagedAddresses removes every managed kombify.me address of
// the stack being destroyed and records the absence evidence. It resolves the
// owner exactly as the rollout did when it registered the addresses.
func deregisterStackManagedAddresses(ctx context.Context, job *Job, q *Queue) error {
	snapshot := job.Snapshot()
	tenantID := managedRuntimeTenantIDFromSnapshot(snapshot, nil)
	ownerID := managedRuntimeOwnerIDFromSnapshot(snapshot, nil)
	if tenantID == "" || ownerID == "" || strings.TrimSpace(snapshot.TargetID) == "" {
		// Addresses are only registered for stacks with a complete identity.
		job.mutateResult(func(result map[string]interface{}) {
			result[ManagedAddressDeregistrationResultField] = map[string]any{"status": "not_bound"}
		})
		return nil
	}
	q.UpdateProgress(job.ID, 2, "Removing kombify.me addresses...")
	evidence, err := DeregisterManagedAddresses(ctx, ManagedAddressDeregistration{
		TenantID: tenantID,
		OwnerID:  ownerID,
		StackID:  snapshot.TargetID,
	})
	if err != nil {
		return err
	}
	job.mutateResult(func(result map[string]interface{}) {
		result[ManagedAddressDeregistrationResultField] = mergeManagedAddressEvidence(
			result[ManagedAddressDeregistrationResultField], evidence)
	})
	return nil
}

// mergeManagedAddressEvidence keeps what an earlier pass of the same destroy
// proved released. A destroy waiting on the provider resumes from the top, and
// each pass repeats the idempotent deregistration, whose later receipts list
// nothing. Overwriting lost the deleted routes and the install zone release
// proof (live 2026-09-23: five passes, the recorded last one empty).
func mergeManagedAddressEvidence(prior any, next map[string]any) map[string]any {
	previous, ok := prior.(map[string]any)
	if !ok || previous["status"] != "absent" || next["status"] != "absent" ||
		previous["owner_ref"] != next["owner_ref"] || previous["stack_ref"] != next["stack_ref"] ||
		previous["target_origin"] != next["target_origin"] {
		return next
	}
	merged := make(map[string]any, len(next))
	for key, value := range next {
		merged[key] = value
	}
	for _, key := range []string{"deleted", "origin_records", "zone_records"} {
		if combined := unionEvidenceItems(previous[key], next[key]); len(combined) > 0 {
			merged[key] = combined
		}
	}
	return merged
}

func unionEvidenceItems(values ...any) []any {
	out := []any{}
	seen := map[string]bool{}
	for _, value := range values {
		encoded, err := json.Marshal(value)
		if err != nil {
			continue
		}
		var items []any
		if json.Unmarshal(encoded, &items) != nil {
			continue
		}
		for _, item := range items {
			identity, _ := json.Marshal(item)
			if !seen[string(identity)] {
				seen[string(identity)] = true
				out = append(out, item)
			}
		}
	}
	return out
}

func boundPortTeardownSnapshot(job *Job) (portinventory.TeardownSnapshot, error) {
	if job == nil {
		return portinventory.TeardownSnapshot{}, portinventory.ErrTeardownSnapshotMismatch
	}
	snapshotValue, ok := job.Snapshot().Result[PortTeardownSnapshotResultField]
	if !ok {
		return portinventory.TeardownSnapshot{}, fmt.Errorf("%w: durable teardown snapshot is missing", portinventory.ErrTeardownSnapshotMismatch)
	}
	snapshot, err := portinventory.DecodeTeardownSnapshot(snapshotValue)
	if err != nil {
		return portinventory.TeardownSnapshot{}, err
	}
	if snapshot.TenantID != payloadString(job.Payload, tenantIDField) ||
		snapshot.OwnerSubjectID != payloadString(job.Payload, "owner_id") ||
		snapshot.TechstackID != job.TargetID {
		return portinventory.TeardownSnapshot{}, portinventory.ErrTeardownSnapshotMismatch
	}
	return snapshot, nil
}

func releasePortTeardownSnapshotForJob(ctx context.Context, cfg *ProvisionConfig, job *Job) error {
	if cfg == nil || cfg.PortInventory == nil {
		return nil
	}
	snapshot, err := boundPortTeardownSnapshot(job)
	if err != nil {
		return err
	}
	return releasePortTeardownSnapshot(ctx, cfg, job, snapshot)
}

func releasePortTeardownSnapshot(ctx context.Context, cfg *ProvisionConfig, job *Job, snapshot portinventory.TeardownSnapshot) error {
	if err := cfg.PortInventory.ReleaseTeardownSnapshot(ctx, snapshot); err != nil {
		return err
	}
	job.mutateResult(func(result map[string]interface{}) {
		result[PortTeardownReleasedResultField] = snapshot.SnapshotDigest
	})
	return nil
}

func managedRuntimeDecommissionRequired(job *Job) (bool, error) {
	if job == nil {
		return false, fmt.Errorf("%w: destroy job is missing", ErrManagedLeaseDecommissionProofRequired)
	}
	if job.Type == JobTypeReconcileLease {
		return true, nil
	}
	rawRequired, classified := job.Payload[ManagedRuntimeDecommissionRequiredField]
	if !classified {
		return false, fmt.Errorf("%w: destroy job has no managed-runtime classification", ErrManagedLeaseDecommissionProofRequired)
	}
	return boolFromInterface(rawRequired), nil
}

func destroyManagedRuntimeLeases(ctx context.Context, cfg *ProvisionConfig, job *Job, q *Queue) (*ManagedLeaseDecommissionResult, error) {
	required, err := managedRuntimeDecommissionRequired(job)
	if err != nil {
		return nil, err
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
			proof,
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

func validateManagedLeaseDecommissionProof(
	req ManagedLeaseDecommissionRequest,
	proof ManagedLeaseDecommissionProof,
	leaseIDs map[string]struct{},
	proofLeaseIDs map[string]struct{},
) (bool, error) {
	proof.StackID = strings.TrimSpace(proof.StackID)
	proof.TenantID = strings.TrimSpace(proof.TenantID)
	proof.LeaseID = strings.TrimSpace(proof.LeaseID)
	proof.ProviderID = strings.ToLower(strings.TrimSpace(proof.ProviderID))
	proof.ResourceGenerationID = strings.TrimSpace(proof.ResourceGenerationID)
	proof.ResourceGenerationDigest = strings.TrimSpace(proof.ResourceGenerationDigest)
	proof.ReceiptRef = strings.TrimSpace(proof.ReceiptRef)
	proof.ReceiptDigest = strings.TrimSpace(proof.ReceiptDigest)
	proof.ObservedState = strings.ToLower(strings.TrimSpace(proof.ObservedState))

	if !validManagedLeaseDecommissionProofIdentity(req, proof) {
		return false, fmt.Errorf("%w: incomplete or mismatched proof for lease %q", ErrManagedLeaseDecommissionProofRequired, proof.LeaseID)
	}
	if requestedLeaseID := strings.TrimSpace(req.LeaseID); requestedLeaseID != "" && proof.LeaseID != requestedLeaseID {
		return false, fmt.Errorf("%w: proof lease %q does not match requested lease %q", ErrManagedLeaseDecommissionProofRequired, proof.LeaseID, requestedLeaseID)
	}
	if _, duplicate := proofLeaseIDs[proof.LeaseID]; duplicate {
		return false, fmt.Errorf("%w: duplicate proof lease_id %q", ErrManagedLeaseDecommissionProofRequired, proof.LeaseID)
	}
	proofLeaseIDs[proof.LeaseID] = struct{}{}
	if requestedDigest := strings.TrimSpace(req.ResourceGenerationDigest); requestedDigest != "" && proof.ResourceGenerationDigest != requestedDigest {
		return false, fmt.Errorf("%w: proof generation digest does not match the claimed generation", ErrManagedLeaseDecommissionProofRequired)
	}
	if _, listed := leaseIDs[proof.LeaseID]; !listed {
		return false, fmt.Errorf("%w: proof lease %q is absent from result lease IDs", ErrManagedLeaseDecommissionProofRequired, proof.LeaseID)
	}
	switch proof.ObservedState {
	case ManagedLeaseDecommissionObservedDecommissioned:
		return true, nil
	case ManagedLeaseDecommissionObservedNotFound:
		return false, nil
	default:
		return false, fmt.Errorf("%w: provider state %q is not terminal", ErrManagedLeaseDecommissionProofRequired, proof.ObservedState)
	}
}

func validManagedLeaseDecommissionProofIdentity(req ManagedLeaseDecommissionRequest, proof ManagedLeaseDecommissionProof) bool {
	if proof.StackID == "" || proof.StackID != strings.TrimSpace(req.StackID) {
		return false
	}
	if proof.TenantID == "" || proof.TenantID != strings.TrimSpace(req.TenantID) {
		return false
	}
	if proof.LeaseID == "" || proof.ProviderID == "" {
		return false
	}
	parsedGeneration, err := uuid.Parse(proof.ResourceGenerationID)
	if err != nil || parsedGeneration.String() != proof.ResourceGenerationID {
		return false
	}
	if !validLowerHexDigest(proof.ResourceGenerationDigest) || proof.ReceiptRef == "" {
		return false
	}
	if !validLowerHexDigest(proof.ReceiptDigest) || proof.VerifiedAt.IsZero() {
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
			return wrapProvisionCause(StepCreateLease, fmt.Errorf("managed runtime lease reconciliation failed: %w", err),
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
	q.RegisterHandler(JobTypeRemoteEnrollment, RemoteEnrollmentHandler(cfg))
	q.RegisterHandler(JobTypeBackup, BackupHandler(cfg))

	// Drift detection handlers
	driftCfg := &DriftCheckConfig{
		WorkDir: cfg.WorkDir,
	}
	RegisterDriftHandlers(q, driftCfg)
}
