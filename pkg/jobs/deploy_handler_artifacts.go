package jobs

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/kombifyio/techstack/pkg/core"
	"github.com/kombifyio/techstack/pkg/unifier"
)

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
//
//nolint:gocyclo // moved unchanged from deploy_handler.go
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
	if err := os.MkdirAll(tofuDir, 0755); err != nil { //nolint:gosec,govet // moved unchanged from deploy_handler.go
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
