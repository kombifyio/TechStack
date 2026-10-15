package orchestrator

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"

	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/jobs"
	"github.com/kombifyio/techstack/pkg/specv2"
)

const rolloutRetryCandidateField = jobs.RolloutRetryCandidateSHA256Field

func (o *Orchestrator) repairRolloutCandidate(ctx context.Context, req RolloutRetryRequest, stack *orchestratorStack, opts ProvisionStackOptions) error {
	if len(req.CandidateSpec) == 0 {
		return nil
	}
	store := o.effectiveStackStore()
	if !req.OwnerApproved || store == nil {
		return fmt.Errorf("%w: approved canonical repair authority is required", ErrRolloutRetryInvalid)
	}
	current, err := store.GetStack(ctx, req.TenantID, req.StackID)
	if err != nil {
		return fmt.Errorf("%w: canonical repair stack is unavailable", ErrRolloutRetryUnavailable)
	}
	if current.OwnerSubjectID != req.OwnerID || current.TenantID != req.TenantID {
		return fmt.Errorf("%w: canonical repair ownership changed", ErrRolloutRetryInvalid)
	}
	if current.Status != stack.status {
		return fmt.Errorf("%w: canonical repair stack state changed", ErrRolloutRetryInvalid)
	}
	if bound := stringFromAny(current.Config[rolloutRetryCandidateField]); bound != "" && bound != opts.rolloutRetryCandidateSHA {
		return fmt.Errorf("%w: canonical repair candidate is already bound", ErrRolloutRetryInvalid)
	}
	baseline, _ := current.Config["stack_spec_v2"].(map[string]any)
	if _, applied := jobs.DecodeStackKitRolloutBinding(current.Config[jobs.StackKitRolloutBindingResultField]); applied {
		return fmt.Errorf("%w: applied installations require their existing change-set authority", ErrRolloutRetryInvalid)
	}
	if err := specv2.RequireRolloutRepairIntent(baseline, req.CandidateSpec); err != nil {
		return fmt.Errorf("%w: %v", ErrRolloutRetryInvalid, err)
	}
	validator := o.cfg.RolloutRetrySpecValidator
	if validator == nil {
		cli, resolveErr := specv2.NewCLIValidatorFromEnv()
		if resolveErr != nil || cli == nil {
			return fmt.Errorf("%w: pinned canonical repair validator is unavailable", ErrRolloutRetryUnavailable)
		}
		validator = cli
	}
	if err := validator.ValidateSpec(ctx, req.CandidateSpec); err != nil {
		return fmt.Errorf("%w: canonical repair validation failed", ErrRolloutRetryInvalid)
	}
	if err := o.requireExactManagedRuntimeDeploy(ctx, stack, req.LeaseID, opts.RequiredServerID); err != nil {
		return err
	}
	config := cloneProvisionSpec(current.Config)
	config["stack_spec_v2"] = req.CandidateSpec
	config[rolloutRetryCandidateField] = opts.rolloutRetryCandidateSHA
	updated, err := store.CompareAndSwapStackConfig(ctx, controlplane.StackConfigCAS{
		TenantID: req.TenantID, StackID: req.StackID, ExpectedUpdatedAt: current.UpdatedAt, Config: config,
	})
	if err != nil {
		return fmt.Errorf("%w: canonical repair revision changed", ErrRolloutRetryInvalid)
	}
	stack.config = updated.Config
	return nil
}

func rolloutCandidateDigest(candidate map[string]any) (string, error) {
	if len(candidate) == 0 {
		return "", nil
	}
	data, err := json.Marshal(candidate)
	if err != nil {
		return "", fmt.Errorf("%w: candidate is not a canonical document", ErrRolloutRetryInvalid)
	}
	return fmt.Sprintf("sha256:%x", sha256.Sum256(data)), nil
}
