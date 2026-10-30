package jobs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/kombifyio/techstack/internal/stackkitrelease"
	"github.com/kombifyio/techstack/pkg/stackkitcommand"
	"github.com/google/uuid"
)

const WizardExtendReceiptKey = "wizard_extend_receipt"

// WizardExtendBinding joins the existing wizard ledger to an exact node-local
// Advanced change set. It contains no browser key or credential.
type WizardUseCaseConfigurationBinding struct {
	WorkloadRef      string `json:"workload_ref"`
	PreviousSpecHash string `json:"previous_spec_hash"`
	SpecHash         string `json:"spec_hash"`
}

type WizardExtendBinding struct {
	Configuration    *WizardUseCaseConfigurationBinding    `json:"configuration,omitempty"`
	Removal          *WizardUseCaseRemovalBinding          `json:"removal,omitempty"`
	RunID            string                                `json:"run_id"`
	RequestDigest    string                                `json:"request_digest"`
	CandidateDigest  string                                `json:"candidate_digest"`
	ReleaseVersion   string                                `json:"release_version"`
	ArchiveSHA256    string                                `json:"archive_sha256"`
	JobIDs           []string                              `json:"job_ids"`
	ExecutionBinding *stackkitcommand.PlanExecutionBinding `json:"execution_binding,omitempty"`
}

func WizardExtendJobID(req StackKitLifecycleRequest) string {
	if req.WizardExtend == nil {
		return ""
	}
	value := strings.Join([]string{"techstack.wizard-extend/v1", req.TenantID, req.OwnerID, req.StackID, req.WizardExtend.RunID, req.AgentID}, "\x00")
	return fmt.Sprintf("wizard-extend-%x", sha256.Sum256([]byte(value)))
}

func validateWizardExtend(req StackKitLifecycleRequest) error {
	b := req.WizardExtend
	if b == nil || strings.TrimSpace(b.RunID) == "" || !bareRILDigest(b.RequestDigest) || !bareRILDigest(b.CandidateDigest) || !bareRILDigest(b.ArchiveSHA256) || b.ReleaseVersion == "" ||
		req.Operation != StackKitLifecycleAdvancedChangeSet || !req.OwnerApproved || req.ServiceID != "" || req.ServiceActionDigest != "" || req.RILOperation != nil ||
		req.DurableJobID != WizardExtendJobID(req) || req.DryRun || req.Offline {
		return fmt.Errorf("wizard extension requires its exact approved Advanced change set and durable identity")
	}
	digest := sha256.Sum256(req.CandidateSpecJSON)
	if hex.EncodeToString(digest[:]) != b.CandidateDigest {
		return fmt.Errorf("wizard extension candidate differs from its accepted projection")
	}
	if b.ExecutionBinding != nil {
		if err := b.ExecutionBinding.Validate(); err != nil {
			return err
		}
		if b.ExecutionBinding.NodeRef != req.NodeRef {
			return fmt.Errorf("wizard execution binding differs from the admitted canonical node")
		}
	}
	if c := b.Configuration; c != nil {
		if b.Removal != nil {
			return fmt.Errorf("a use-case change cannot configure and retire workloads together")
		}
		if strings.TrimSpace(c.WorkloadRef) == "" || !stackkitcommand.NativeSpecHashValid(c.PreviousSpecHash) || !stackkitcommand.NativeSpecHashValid(c.SpecHash) {
			return fmt.Errorf("configuration requires the native selected workload and spec hashes")
		}
	}
	if r := b.Removal; r != nil {
		if err := r.Intent.Validate(); err != nil {
			return err
		}
		if !stackkitcommand.NativeSpecHashValid(r.SpecHash) || r.SpecHash == r.Intent.ExpectedSpecHash {
			return fmt.Errorf("removal requires its changed native candidate hash")
		}
	}
	return nil
}

func WizardExtendReceipt(req StackKitLifecycleRequest) map[string]any {
	if req.WizardExtend == nil {
		return nil
	}
	receipt := map[string]any{
		"schema_version": "techstack.wizard-extend-receipt/v1",
		"run_id":         req.WizardExtend.RunID, "request_digest": req.WizardExtend.RequestDigest,
		"candidate_digest": req.WizardExtend.CandidateDigest, "release_version": req.WizardExtend.ReleaseVersion,
		"archive_sha256": req.WizardExtend.ArchiveSHA256,
		"group_job_ids":  strings.Join(req.WizardExtend.JobIDs, ","),
		"tenant_id":      req.TenantID, "owner_id": req.OwnerID, "stack_id": req.StackID,
		"agent_id": req.AgentID, "node_id": req.NodeID, "node_ref": req.NodeRef,
		"stackkit_instance_id": req.StackKitInstanceID, "stackkit": req.StackKit, "spec_path": req.SpecPath,
	}
	if binding := req.WizardExtend.ExecutionBinding; binding != nil {
		receipt["local_site_ref"], receipt["local_node_ref"] = binding.SiteRef, binding.NodeRef
		receipt["execution_channel_ref"], receipt["baseline_plan_hash"] = binding.ExecutionChannelRef, binding.PlanHash
		receipt["execution_binding_source"] = binding.Source
		receipt["execution_owner_ref"], receipt["execution_owner_key_id"] = binding.OwnerRef, binding.OwnerKeyID
	}
	if c := req.WizardExtend.Configuration; c != nil {
		receipt["configuration_workload_ref"] = c.WorkloadRef
		receipt["configuration_previous_spec_hash"] = c.PreviousSpecHash
		receipt["configuration_spec_hash"] = c.SpecHash
	}
	if r := req.WizardExtend.Removal; r != nil {
		receipt["removal_selector_kind"] = string(r.Intent.Kind)
		receipt["removal_selector_ref"] = r.Intent.Ref
		receipt["removal_workload_refs"] = strings.Join(r.Intent.WorkloadRefs(), ",")
		receipt["removal_previous_spec_hash"] = r.Intent.ExpectedSpecHash
		receipt["removal_spec_hash"] = r.SpecHash
		receipt["data_disposition"] = "retain"
	}
	return receipt
}

func MatchesWizardExtendReceipt(result map[string]any, req StackKitLifecycleRequest) bool {
	want := WizardExtendReceipt(req)
	got, ok := result[WizardExtendReceiptKey].(map[string]any)
	if want == nil || !ok {
		return false
	}
	for key, value := range want {
		if got[key] != value {
			return false
		}
	}
	return true
}

// PrepareWizardExtend checks the actual pin and enrolled Advanced trust before
// a wizard saves desired state. Dispatch still revalidates both at execution.
func PrepareWizardExtend(ctx context.Context, sender StackKitCommandSender, issuer AdvancedIssuer, req StackKitLifecycleRequest) (StackKitLifecycleRequest, error) {
	release, err := TargetStackKitReleaseForStack(ctx, req.TenantID, req.StackID)
	if err != nil {
		return req, fmt.Errorf("wizard extension requires the pinned StackKits release: %w", err)
	}
	if release == nil {
		return req, fmt.Errorf("wizard extension requires the pinned StackKits release")
	}
	if req.WizardExtend == nil {
		return req, fmt.Errorf("wizard extension binding is required")
	}
	req.WizardExtend.ReleaseVersion = release.Receipt().Version
	req.WizardExtend.ArchiveSHA256 = release.Receipt().ArchiveSHA256
	digest := sha256.Sum256(req.CandidateSpecJSON)
	req.WizardExtend.CandidateDigest = hex.EncodeToString(digest[:])
	req.DurableJobID = WizardExtendJobID(req)
	req, err = NormalizeStackKitLifecycleRequest(req)
	if err != nil {
		return req, err
	}
	dispatcher, err := newAdvancedDispatcher(ctx, sender, issuer, *release, req)
	if err != nil {
		return req, err
	}
	binding, err := readWizardExecutionBinding(ctx, sender, *release, req, dispatcher.binding.OwnerRef, "wizard-binding-"+uuid.NewString())
	if err != nil {
		return req, err
	}
	req.WizardExtend.ExecutionBinding = &binding
	_, err = dispatcher.require(stackKitStepChangeSetCreate, stackKitStepChangeSetApply)
	return req, err
}

func readWizardExecutionBinding(ctx context.Context, sender StackKitCommandSender, release stackkitrelease.Release, req StackKitLifecycleRequest, ownerRef, commandID string) (stackkitcommand.PlanExecutionBinding, error) {
	// PLAN is the existing read-only command. It needs no fabricated local
	// tuple, and cannot generate, apply or grant execution authority.
	plan := req
	plan.Operation, plan.OwnerApproved = StackKitLifecyclePlan, false
	command, err := stackKitLifecycleCommand(commandID, plan, release)
	if err != nil {
		return stackkitcommand.PlanExecutionBinding{}, err
	}
	if err := stackkitcommand.ValidateCommand(command); err != nil {
		return stackkitcommand.PlanExecutionBinding{}, err
	}
	result, err := sendStackKitCommandBoundedForTenant(ctx, sender, req.TenantID, req.AgentID, command)
	if err != nil {
		return stackkitcommand.PlanExecutionBinding{}, err
	}
	if err := stackkitcommand.ValidateResult(result, command); err != nil {
		return stackkitcommand.PlanExecutionBinding{}, err
	}
	binding, err := stackkitcommand.ManagedPlanExecutionBinding(result, req.StackKitInstanceID, req.NodeRef, ownerRef)
	if err == nil {
		return binding, nil
	}
	if !errors.Is(err, stackkitcommand.ErrPlanExecutionBindingMissing) {
		return binding, err
	}
	// Backward compatibility is only the old exact, known tuple. A new
	// arbitrary joined node cannot inherit the primary node's channel.
	known, knownErr := localExecutionBindingForNode(req.StackKit, req.NodeRef)
	if knownErr != nil || known.NodeRef != req.NodeRef || (binding.SiteRef != "" && binding.SiteRef != known.SiteRef) {
		return binding, stackkitcommand.ErrPlanExecutionBindingMissing
	}
	binding.SiteRef, binding.ExecutionChannelRef, binding.Source = known.SiteRef, known.ExecutionChannelRef, "known-kit-binding"
	binding.OwnerRef = ownerRef
	return binding, binding.Validate()
}

func revalidateWizardExecutionBinding(ctx context.Context, sender StackKitCommandSender, release stackkitrelease.Release, req StackKitLifecycleRequest, ownerRef, commandID string) error {
	if req.WizardExtend == nil || req.WizardExtend.ExecutionBinding == nil {
		return nil // Already accepted requests retain their original contract.
	}
	fresh, err := readWizardExecutionBinding(ctx, sender, release, req, ownerRef, commandID)
	if err != nil {
		return err
	}
	if fresh != *req.WizardExtend.ExecutionBinding {
		return fmt.Errorf("wizard execution binding or baseline plan changed after acceptance")
	}
	return nil
}
