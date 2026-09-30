package managedstackkit

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/kombifyio/techstack/pkg/backupstore"
	"github.com/kombifyio/stackkits/pkg/backupbinding"
)

// AttestBackupRenewal proves the currently applied repository still has the
// same encrypted credentials and can read/write/delete its probe object. The
// fresh Inventory is signing input only: it must never replace the applied
// node Inventory or its original durable attestation.
func (builder *RolloutInventory) AttestBackupRenewal(ctx context.Context, request RolloutInventoryRequest) ([]byte, error) {
	if builder == nil || builder.custody == nil || builder.attest == nil || request.TenantID == "" || request.StackID == "" {
		return nil, fmt.Errorf("managed backup renewal custody is unavailable")
	}
	reader, ok := builder.custody.(interface {
		Evidence(context.Context, string, string) (backupstore.CustodyEvidence, error)
	})
	if !ok {
		return nil, fmt.Errorf("managed backup renewal evidence is unavailable")
	}
	process := OperationsProcess{ChannelRef: cloudOperationsChannel, SiteRef: cloudOperationsSite, NodeRef: cloudOperationsNode, Executable: cloudOperationsExecutable, ExecutableSHA256: builder.executableSHA256}
	candidate := Candidate{StackKitsVersion: request.StackKitsVersion, Digest: request.CandidateDigest, ValidFor: 15 * time.Minute}
	if _, _, _, err := validateInventoryAuthority(request.ResolvedPlan, process, candidate); err != nil {
		return nil, err
	}
	var plan struct {
		External map[string]map[string]map[string]string `json:"externalBackupTargetBindings"`
	}
	if json.Unmarshal(request.ResolvedPlan, &plan) != nil {
		return nil, fmt.Errorf("managed backup renewal plan is invalid")
	}
	binding := plan.External[cloudOperationsSite][backupbinding.Capability]
	evidence, err := reader.Evidence(ctx, request.TenantID, request.StackID)
	if err != nil {
		return nil, err
	}
	bindingRef, err := backupbinding.OpaqueReference("backup-target-binding", evidence.BindingEvidence)
	if err != nil {
		return nil, err
	}
	targetRef, err := backupbinding.OpaqueReference("backup-target", evidence.TargetEvidence)
	if err != nil {
		return nil, err
	}
	if binding["bindingRef"] != bindingRef || binding["backupTargetRef"] != targetRef {
		return nil, fmt.Errorf("managed backup renewal cannot replace the applied repository or credentials")
	}
	credentials, err := builder.custody.Get(ctx, request.TenantID, request.StackID)
	if err != nil {
		return nil, err
	}
	if credentials.TenantID != request.TenantID || credentials.StackID != request.StackID {
		return nil, fmt.Errorf("managed backup renewal custody identity differs")
	}
	freshInput := evidence
	freshInput.AttestationEvidence = nil
	fresh, err := builder.attest(ctx, credentials, freshInput)
	if err != nil {
		return nil, fmt.Errorf("managed backup renewal target attestation failed")
	}
	if fresh.ObservedAt.IsZero() || time.Since(fresh.ObservedAt) > time.Minute || fresh.ObservedAt.After(time.Now().Add(time.Minute)) || !bytes.Equal(fresh.TargetEvidence, evidence.TargetEvidence) || !bytes.Equal(fresh.BindingEvidence, evidence.BindingEvidence) {
		return nil, fmt.Errorf("managed backup renewal target evidence differs")
	}
	after, err := reader.Evidence(ctx, request.TenantID, request.StackID)
	if err != nil || !bytes.Equal(after.TargetEvidence, evidence.TargetEvidence) || !bytes.Equal(after.BindingEvidence, evidence.BindingEvidence) {
		return nil, fmt.Errorf("managed backup custody changed during renewal")
	}
	return BuildInventory(request.ResolvedPlan, fresh, process, candidate)
}
