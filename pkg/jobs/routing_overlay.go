package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/kombifyio/techstack/pkg/core"
	"github.com/kombifyio/techstack/pkg/stackrouting"
	"github.com/kombifyio/techstack/pkg/unifier"
)

const (
	routingDispatchKindField   = "routing_dispatch_kind"
	routingDispatchKindExact   = "exact-stack-routing"
	routingIdempotencyKeyField = "routing_idempotency_key"
	routingRevisionField       = "routing_revision"
	routingServerIDField       = "routing_server_id"
	routingLeaseIDField        = "routing_lease_id"
)

type exactRoutingJobBinding struct {
	revision int64
	serverID string
	leaseID  string
}

// deployApplyRoutingOverlay loads routing only after the immutable intent has
// been parsed. It mutates the derived in-memory spec and StackKits handoff, not
// kombination.yaml.
func deployApplyRoutingOverlay(ctx context.Context, cfg *ProvisionConfig, job *Job, persister *unifier.SpecPersister, spec *core.KombinationSpec) (string, error) {
	if cfg == nil || cfg.RoutingStore == nil || job == nil {
		return "", nil
	}
	state, err := loadDeployRoutingState(ctx, cfg, job, spec)
	if err != nil || state == nil {
		return "", err
	}
	if bindingErr := validateDeployRoutingBinding(job, spec, state); bindingErr != nil {
		return "", bindingErr
	}
	if applyErr := stackrouting.ApplyToKombination(spec, state); applyErr != nil {
		return "", wrapProvisionError(StepPrepareRollout, applyErr.Error(),
			"The desired custom-domain overlay is invalid; no rollout artifacts were generated.")
	}
	path, hash, persistErr := stackrouting.ApplyToPersistedStackSpec(persister, state)
	if persistErr != nil {
		return "", wrapProvisionError(StepPrepareRollout, persistErr.Error(),
			"The StackKits routing handoff could not be derived from desired state.")
	}
	recordDeployRoutingEvidence(job, state, path, hash)
	return path, nil
}

func loadDeployRoutingState(ctx context.Context, cfg *ProvisionConfig, job *Job, spec *core.KombinationSpec) (*stackrouting.DesiredState, error) {
	tenantID := managedRuntimeTenantID(job, spec)
	ownerID := managedRuntimeOwnerID(job, spec)
	if tenantID == "" || ownerID == "" {
		return nil, wrapProvisionError(StepPrepareRollout, "routing identity is incomplete",
			"The persisted routing overlay cannot be applied without the exact tenant and owner identity.")
	}
	state, err := cfg.RoutingStore.Get(ctx, tenantID, job.TargetID)
	if errors.Is(err, stackrouting.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, wrapProvisionError(StepPrepareRollout, fmt.Sprintf("load routing overlay: %v", err),
			"The desired routing state could not be loaded; rollout was stopped before generating artifacts.")
	}
	if state.StackID != job.TargetID || state.OwnerSubjectID != ownerID || state.ServerID == "" {
		return nil, wrapProvisionError(StepPrepareRollout, "routing overlay target mismatch",
			"The desired routing state is not bound to this exact stack owner and server.")
	}
	return state, nil
}

func validateDeployRoutingBinding(job *Job, spec *core.KombinationSpec, state *stackrouting.DesiredState) error {
	if binding, exact, bindingErr := routingBindingFromJob(job); bindingErr != nil {
		return wrapProvisionError(StepPrepareRollout, bindingErr.Error(),
			"The routing rollout job is missing its immutable revision or exact target receipt; no artifacts were generated.")
	} else if exact && (state.Revision != binding.revision || state.ServerID != binding.serverID || state.LeaseID != binding.leaseID) {
		return wrapProvisionError(StepPrepareRollout, "routing overlay immutable receipt mismatch",
			"The desired routing revision or exact server lease changed after this job was dispatched; this stale job was stopped before generating artifacts.")
	}
	if state.LeaseID == "" {
		return nil
	}
	jobLeaseID := strings.TrimSpace(firstNonEmpty(
		stringFromMap(job.Payload, leaseIDField),
		stringFromMap(job.Result, leaseIDField),
		metadataString(spec, leaseIDField),
	))
	if jobLeaseID == "" || jobLeaseID != state.LeaseID {
		return wrapProvisionError(StepPrepareRollout, "routing overlay lease mismatch",
			"The desired custom domain is bound to a different managed server lease; no rollout was dispatched.")
	}
	return nil
}

func recordDeployRoutingEvidence(job *Job, state *stackrouting.DesiredState, path, hash string) {
	job.mutateResult(func(result map[string]interface{}) {
		result["routing_revision"] = state.Revision
		result["routing_server_id"] = state.ServerID
		result["routing_domain"] = state.Domain
		result["routing_mode"] = state.Mode
		if state.LeaseID != "" {
			result["routing_lease_id"] = state.LeaseID
		}
		if path != "" {
			result["stack_spec_path"] = path
			result["stack_spec_sha256"] = hash
		}
	})
}

func routingBindingFromJob(job *Job) (exactRoutingJobBinding, bool, error) {
	if job == nil {
		return exactRoutingJobBinding{}, false, nil
	}
	dispatchKind, err := matchingRoutingString(job.Payload, job.Result, routingDispatchKindField)
	if dispatchKind == "" && err == nil {
		return exactRoutingJobBinding{}, false, nil
	}
	if err != nil || dispatchKind != routingDispatchKindExact {
		return exactRoutingJobBinding{}, true, fmt.Errorf("routing dispatch marker is invalid")
	}
	revision, err := matchingRoutingRevision(job.Payload, job.Result)
	if err != nil || revision <= 0 {
		return exactRoutingJobBinding{}, true, fmt.Errorf("routing overlay revision receipt is invalid")
	}
	serverID, err := matchingRoutingString(job.Payload, job.Result, routingServerIDField)
	if err != nil || serverID == "" {
		return exactRoutingJobBinding{}, true, fmt.Errorf("routing overlay server receipt is invalid")
	}
	leaseID, err := matchingRoutingString(job.Payload, job.Result, routingLeaseIDField)
	if err != nil || leaseID == "" {
		return exactRoutingJobBinding{}, true, fmt.Errorf("routing overlay lease receipt is invalid")
	}
	return exactRoutingJobBinding{revision: revision, serverID: serverID, leaseID: leaseID}, true, nil
}

func reconcileRoutingRolloutOutcome(ctx context.Context, cfg *ProvisionConfig, job *Job, rolloutErr error) error {
	if isJobWaitError(rolloutErr) {
		return nil
	}
	binding, exact, err := routingBindingFromJob(job)
	if !exact {
		return nil
	}
	if err != nil {
		return err
	}
	if cfg == nil || cfg.RoutingStore == nil {
		return fmt.Errorf("routing store is unavailable for exact rollout reconciliation")
	}
	tenantID := firstNonEmpty(stringFromMap(job.Payload, tenantIDField), stringFromMap(job.Result, tenantIDField))
	if tenantID == "" || job == nil || strings.TrimSpace(job.ID) == "" || strings.TrimSpace(job.TargetID) == "" {
		return fmt.Errorf("routing rollout reconciliation identity is incomplete")
	}
	status := stackrouting.RolloutCompleted
	reasonCode := ""
	if rolloutErr != nil {
		status = stackrouting.RolloutFailed
		reasonCode = stackrouting.ReasonRolloutFailed
	}
	_, err = cfg.RoutingStore.MarkRolloutFinished(ctx, tenantID, job.TargetID, binding.revision, job.ID, status, reasonCode)
	return err
}

func copyRoutingDispatchReceipt(dst, payload, result map[string]interface{}) {
	if dst == nil {
		return
	}
	for _, key := range []string{routingDispatchKindField, routingIdempotencyKeyField, routingRevisionField, routingServerIDField, routingLeaseIDField} {
		if payload != nil {
			if value, ok := payload[key]; ok {
				dst[key] = value
				continue
			}
		}
		if result != nil {
			if value, ok := result[key]; ok {
				dst[key] = value
			}
		}
	}
}

func matchingRoutingString(left, right map[string]interface{}, key string) (string, error) {
	leftValue := stringFromMap(left, key)
	rightValue := stringFromMap(right, key)
	if leftValue != "" && rightValue != "" && leftValue != rightValue {
		return "", fmt.Errorf("routing receipt %s differs between payload and result", key)
	}
	return firstNonEmpty(leftValue, rightValue), nil
}

func matchingRoutingRevision(left, right map[string]interface{}) (int64, error) {
	leftValue, leftOK := routingRevisionFromMap(left)
	rightValue, rightOK := routingRevisionFromMap(right)
	if leftOK && rightOK && leftValue != rightValue {
		return 0, fmt.Errorf("routing revision differs between payload and result")
	}
	if leftOK {
		return leftValue, nil
	}
	if rightOK {
		return rightValue, nil
	}
	return 0, fmt.Errorf("routing revision is missing")
}

func routingRevisionFromMap(values map[string]interface{}) (int64, bool) {
	if values == nil {
		return 0, false
	}
	raw, ok := values[routingRevisionField]
	if !ok || raw == nil {
		return 0, false
	}
	switch value := raw.(type) {
	case int:
		return int64(value), true
	case int64:
		return value, true
	case float64:
		if value != float64(int64(value)) {
			return 0, false
		}
		return int64(value), true
	case json.Number:
		revision, err := value.Int64()
		return revision, err == nil
	case string:
		revision, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
		return revision, err == nil
	default:
		return 0, false
	}
}
