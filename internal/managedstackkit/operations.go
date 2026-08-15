package managedstackkit

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/kombifyio/go-common/runtimeexecutor"
	"github.com/kombifyio/techstack/pkg/backupstore"
	"github.com/kombifyio/stackkits/pkg/backupbinding"
)

const (
	managedOperationsChannel = "host-channel-cloud-main"
	managedOperationsSite    = "cloud"
	managedOperationsNode    = "cloud-main"
	cloudBackupProviderRef   = "stackkits-cloud-offsite-backup"
	cloudBackupModuleRef     = "stackkits-cloud-offsite-backup-runtime"
	cloudBackupUnitRef       = "executor-contract"
)

type OperationsRequest struct {
	TenantID       string
	StackID        string
	RuntimeAgentID string
	Request        runtimeexecutor.ExecutionRequest
}

type OperationRejectionDetails struct {
	ReasonCode   string `json:"reason_code"`
	Retryable    bool   `json:"retryable"`
	UserGuidance string `json:"user_guidance"`
}

type operationRejection struct {
	details OperationRejectionDetails
	cause   error
}

func (rejection *operationRejection) Error() string { return rejection.cause.Error() }
func (rejection *operationRejection) Unwrap() error { return rejection.cause }

func rejectOperation(reasonCode string, retryable bool, guidance string, cause error) error {
	return &operationRejection{details: OperationRejectionDetails{
		ReasonCode: reasonCode, Retryable: retryable, UserGuidance: guidance,
	}, cause: cause}
}

func RejectionDetails(err error) OperationRejectionDetails {
	var rejection *operationRejection
	if errors.As(err, &rejection) {
		return rejection.details
	}
	return OperationRejectionDetails{
		ReasonCode:   "managed_stackkit_operation_rejected",
		Retryable:    false,
		UserGuidance: "Review the enrolled runtime and retry with a freshly generated StackKits Inventory.",
	}
}

type operationsCustody interface {
	Get(context.Context, string, string) (backupstore.Credentials, error)
	Evidence(context.Context, string, string) (backupstore.CustodyEvidence, error)
	RecordAttestation(context.Context, string, string, backupstore.CustodyEvidence) (backupstore.CustodyEvidence, error)
}

type targetAttestor func(context.Context, backupstore.Credentials, backupstore.CustodyEvidence) (backupstore.CustodyEvidence, error)

type Operations struct {
	custody  operationsCustody
	attest   targetAttestor
	now      func() time.Time
	validate func() error
}

func NewOperations(custody operationsCustody) (*Operations, error) {
	if custody == nil {
		return nil, errors.New("managed StackKits operations require backup custody")
	}
	return &Operations{
		custody: custody,
		attest: func(ctx context.Context, credentials backupstore.Credentials, evidence backupstore.CustodyEvidence) (backupstore.CustodyEvidence, error) {
			verifier, err := backupstore.NewTargetVerifier(ctx, credentials)
			if err != nil {
				return backupstore.CustodyEvidence{}, err
			}
			evidence.AttestationEvidence = nil
			return verifier.Verify(ctx, credentials, evidence)
		},
		now: time.Now,
	}, nil
}

func (o *Operations) Execute(ctx context.Context, input OperationsRequest) (runtimeexecutor.ExecutionOutcome, error) {
	input.TenantID, input.StackID, input.RuntimeAgentID = strings.TrimSpace(input.TenantID), strings.TrimSpace(input.StackID), strings.TrimSpace(input.RuntimeAgentID)
	if ctx == nil || o == nil || o.custody == nil || o.attest == nil || o.now == nil || input.TenantID == "" || input.StackID == "" || input.RuntimeAgentID == "" {
		return runtimeexecutor.ExecutionOutcome{}, rejectOperation(
			"managed_stackkit_identity_invalid", false,
			"Re-enroll the runtime against the exact tenant and stack before retrying.",
			errors.New("managed StackKits operations require exact tenant, stack, and runtime-agent identity"),
		)
	}
	if o.validate != nil {
		if err := o.validate(); err != nil {
			return runtimeexecutor.ExecutionOutcome{}, rejectOperation("managed_stackkit_request_rejected", false, "Generate a fresh StackKits rollout from the current released product manifest.", err)
		}
	} else if err := input.Request.Validate(); err != nil {
		return runtimeexecutor.ExecutionOutcome{}, rejectOperation("managed_stackkit_request_rejected", false, "Generate a fresh StackKits rollout from the current released product manifest.", fmt.Errorf("validate managed StackKits execution request: %w", err))
	}
	target, health, binding, err := validateManagedOperationsRequest(input.StackID, input.Request, o.now().UTC())
	if err != nil {
		return runtimeexecutor.ExecutionOutcome{}, rejectOperation("managed_stackkit_binding_rejected", true, "Regenerate the managed StackKits Inventory and retry before its binding expires.", err)
	}
	credentials, err := o.custody.Get(ctx, input.TenantID, input.StackID)
	if err != nil {
		return runtimeexecutor.ExecutionOutcome{}, rejectOperation("backup_custody_unavailable", true, "Restore the encrypted backup custody for this tenant and stack, then retry.", fmt.Errorf("load managed StackKits backup custody: %w", err))
	}
	evidence, err := o.custody.Evidence(ctx, input.TenantID, input.StackID)
	if err != nil {
		return runtimeexecutor.ExecutionOutcome{}, rejectOperation("backup_evidence_unavailable", true, "Re-attest the managed backup target and regenerate the StackKits Inventory.", fmt.Errorf("load managed StackKits backup evidence: %w", err))
	}
	if err := validateManagedBindingEvidence(binding, evidence); err != nil {
		return runtimeexecutor.ExecutionOutcome{}, rejectOperation("backup_binding_stale", true, "Regenerate the StackKits Inventory from current durable backup custody and retry.", err)
	}
	freshInput := evidence
	freshInput.AttestationEvidence = nil
	fresh, err := o.attest(ctx, credentials, freshInput)
	if err != nil {
		return runtimeexecutor.ExecutionOutcome{}, rejectOperation("backup_target_unhealthy", true, "Verify the managed backup endpoint and credentials, then re-attest the target.", fmt.Errorf("verify managed StackKits backup target: %w", err))
	}
	if len(fresh.AttestationEvidence) == 0 || fresh.ObservedAt.IsZero() {
		return runtimeexecutor.ExecutionOutcome{}, rejectOperation("backup_target_attestation_missing", true, "Re-attest the managed backup target before retrying the rollout.", errors.New("managed StackKits backup target returned no fresh operational attestation"))
	}
	fresh, err = o.custody.RecordAttestation(ctx, input.TenantID, input.StackID, fresh)
	if err != nil {
		return runtimeexecutor.ExecutionOutcome{}, rejectOperation("backup_attestation_persist_failed", true, "Restore the encrypted custody store and retry the backup target attestation.", fmt.Errorf("persist managed StackKits backup target attestation: %w", err))
	}
	observationBody, err := json.Marshal(struct {
		RequestDigest       string `json:"requestDigest"`
		BindingHash         string `json:"bindingHash"`
		BackupTargetRef     string `json:"backupTargetRef"`
		CustodyAttestation  string `json:"custodyAttestationRef"`
		FreshTargetEvidence string `json:"freshTargetEvidence"`
		ObservedAt          string `json:"observedAt"`
	}{input.Request.RequestDigest, binding.BindingHash, binding.BackupTargetRef, binding.CustodyAttestationRef,
		string(fresh.AttestationEvidence), fresh.ObservedAt.UTC().Format(time.RFC3339Nano)})
	if err != nil {
		return runtimeexecutor.ExecutionOutcome{}, err
	}
	digest := sha256.Sum256(observationBody)
	observationDigest := "sha256:" + hex.EncodeToString(digest[:])
	refSuffix := strings.TrimPrefix(observationDigest, "sha256:")
	return runtimeexecutor.ExecutionOutcome{
		Runtime: []runtimeexecutor.RuntimeOutcome{{RequirementID: target.RequirementID, InstanceRef: target.InstanceRef,
			Status: runtimeexecutor.RuntimeStatusApplied, ObservationRef: "runtime-observation://techstack-cloud-backup/" + refSuffix, ObservationDigest: observationDigest}},
		Health: []runtimeexecutor.HealthOutcome{{RequirementID: health.RequirementID, TargetRef: health.TargetRef,
			Status: runtimeexecutor.HealthStatusHealthy, ObservationRef: "health-observation://techstack-cloud-backup/" + refSuffix, ObservationDigest: observationDigest}},
	}, nil
}

func validateManagedOperationsRequest(stackID string, request runtimeexecutor.ExecutionRequest, now time.Time) (runtimeexecutor.RuntimeTarget, runtimeexecutor.HealthTarget, runtimeexecutor.BackupTargetBinding, error) {
	emptyTarget, emptyHealth, emptyBinding := runtimeexecutor.RuntimeTarget{}, runtimeexecutor.HealthTarget{}, runtimeexecutor.BackupTargetBinding{}
	if len(request.RuntimeTargets) != 1 || len(request.HealthTargets) != 1 || len(request.BackupTargetBindings) != 1 || len(request.Artifacts) != 1 || len(request.AccessBindings) != 0 {
		return emptyTarget, emptyHealth, emptyBinding, errors.New("managed StackKits backup operation requires exactly one runtime, health, binding, and artifact")
	}
	target, health, binding, artifact := request.RuntimeTargets[0], request.HealthTargets[0], request.BackupTargetBindings[0], request.Artifacts[0]
	if target.OwnerKind != "module" || target.OwnerRef != cloudBackupModuleRef || target.ProviderRef != cloudBackupProviderRef ||
		target.ModuleRef != cloudBackupModuleRef || target.UnitRef != cloudBackupUnitRef || target.RuntimeKind != "host" ||
		target.RuntimeDelivery != "stackkit" || target.ExecutionChannelRef != managedOperationsChannel ||
		!slices.Equal(target.SiteRefs, []string{managedOperationsSite}) || !slices.Equal(target.NodeRefs, []string{managedOperationsNode}) ||
		len(target.BackupTargetBindingRefs) != 1 || target.BackupTargetBindingRefs[0] != binding.ID {
		return emptyTarget, emptyHealth, emptyBinding, errors.New("managed StackKits runtime target escaped the Cloud backup owner contract")
	}
	if binding.RuntimeRequirementID != target.RequirementID || binding.StackID != stackID || binding.Kind != "backup-target" ||
		binding.SiteRef != managedOperationsSite || !slices.Equal(binding.TargetNodeRefs, []string{managedOperationsNode}) ||
		binding.CapabilityRef != backupbinding.Capability || binding.ContractOwnerRef != cloudBackupProviderRef {
		return emptyTarget, emptyHealth, emptyBinding, errors.New("managed StackKits backup binding does not match the enrolled stack target")
	}
	validUntil, err := time.Parse(time.RFC3339Nano, binding.ValidUntil)
	if err != nil || now.IsZero() || !now.Before(validUntil) {
		return emptyTarget, emptyHealth, emptyBinding, errors.New("managed StackKits backup binding is expired or invalid")
	}
	if health.RuntimeRequirementID != target.RequirementID || !slices.Equal(health.SiteRefs, target.SiteRefs) || !slices.Equal(health.NodeRefs, target.NodeRefs) {
		return emptyTarget, emptyHealth, emptyBinding, errors.New("managed StackKits backup health target does not match the runtime target")
	}
	if artifact.ID == "" || len(target.ArtifactRefs) != 1 || target.ArtifactRefs[0] != artifact.ID || artifact.Format != "json" ||
		artifact.OwnerRef != target.InstanceRef || artifact.ProviderRef != cloudBackupProviderRef || artifact.ModuleRef != cloudBackupModuleRef ||
		artifact.UnitRef != cloudBackupUnitRef || !slices.Equal(artifact.SiteRefs, target.SiteRefs) || !slices.Equal(artifact.NodeRefs, target.NodeRefs) {
		return emptyTarget, emptyHealth, emptyBinding, errors.New("managed StackKits backup artifact does not match the exact runtime target")
	}
	if err := validateManagedOperationsArtifact(artifact.Content, stackID, binding); err != nil {
		return emptyTarget, emptyHealth, emptyBinding, err
	}
	return target, health, binding, nil
}

func validateManagedBindingEvidence(binding runtimeexecutor.BackupTargetBinding, evidence backupstore.CustodyEvidence) error {
	if len(evidence.BindingEvidence) == 0 || len(evidence.TargetEvidence) == 0 || len(evidence.AttestationEvidence) == 0 {
		return errors.New("managed StackKits backup custody has no durable operational attestation")
	}
	bindingRef, err := backupbinding.OpaqueReference("backup-target-binding", evidence.BindingEvidence)
	if err != nil {
		return err
	}
	targetRef, err := backupbinding.OpaqueReference("backup-target", evidence.TargetEvidence)
	if err != nil {
		return err
	}
	attestationRef, err := backupbinding.OpaqueReference("backup-custody-attestation", evidence.AttestationEvidence)
	if err != nil {
		return err
	}
	if binding.BindingRef != bindingRef || binding.BackupTargetRef != targetRef || binding.CustodyAttestationRef != attestationRef {
		return errors.New("managed StackKits backup binding does not match current durable custody")
	}
	return nil
}

func validateManagedOperationsArtifact(content []byte, stackID string, binding runtimeexecutor.BackupTargetBinding) error {
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.UseNumber()
	var document map[string]any
	if err := decoder.Decode(&document); err != nil {
		return fmt.Errorf("decode managed StackKits backup artifact: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("managed StackKits backup artifact contains trailing JSON")
	}
	module, _ := document["module"].(map[string]any)
	inputs, _ := document["planInputs"].(map[string]any)
	backup, _ := inputs["cloudOffsiteBackup"].(map[string]any)
	bindings, _ := backup["bindings"].(map[string]any)
	site, _ := bindings[managedOperationsSite].(map[string]any)
	projected, _ := site[backupbinding.Capability].(map[string]any)
	if document["apiVersion"] != "stackkit.executor-contract-bundle/v1" || document["kind"] != "ExecutorContractBundle" ||
		module["id"] != cloudBackupModuleRef || inputs["stackId"] != stackID || projected["bindingRef"] != binding.BindingRef ||
		projected["bindingHash"] != binding.BindingHash || projected["backupTargetRef"] != binding.BackupTargetRef ||
		projected["custodyAttestationRef"] != binding.CustodyAttestationRef {
		return errors.New("managed StackKits backup artifact differs from the admitted external binding")
	}
	return nil
}
