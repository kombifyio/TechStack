package managedstackkit

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/kombifyio/techstack/pkg/backupstore"
)

const (
	cloudOperationsExecutable = "/usr/local/libexec/techstack-stackkit-operations"
	cloudOperationsChannel    = "host-channel-cloud-main"
	cloudOperationsSite       = "cloud"
	cloudOperationsNode       = "cloud-main"
)

type RolloutInventoryRequest struct {
	TenantID         string
	StackID          string
	ResolvedPlan     []byte
	StackKitsVersion string
	CandidateDigest  string
	ValidFor         time.Duration
}

type rolloutCustodian interface {
	Ensure(context.Context, string, string) (backupstore.CustodyEvidence, error)
}

type rolloutCustody interface {
	Get(context.Context, string, string) (backupstore.Credentials, error)
	RecordAttestation(context.Context, string, string, backupstore.CustodyEvidence) (backupstore.CustodyEvidence, error)
}

type RolloutInventory struct {
	custodian        rolloutCustodian
	custody          rolloutCustody
	executableSHA256 string
	attest           targetAttestor
}

func NewRolloutInventory(custodian rolloutCustodian, custody rolloutCustody, executableSHA256 string) (*RolloutInventory, error) {
	if (custodian == nil) != (custody == nil) || !validDigest(executableSHA256) {
		return nil, errors.New("managed rollout Inventory requires paired backup custody and an exact Operations executable digest")
	}
	return &RolloutInventory{
		custodian: custodian, custody: custody, executableSHA256: executableSHA256,
		attest: func(ctx context.Context, credentials backupstore.Credentials, evidence backupstore.CustodyEvidence) (backupstore.CustodyEvidence, error) {
			verifier, err := backupstore.NewTargetVerifier(ctx, credentials)
			if err != nil {
				return backupstore.CustodyEvidence{}, err
			}
			return verifier.Verify(ctx, credentials, evidence)
		},
	}, nil
}

func (builder *RolloutInventory) Build(ctx context.Context, request RolloutInventoryRequest) ([]byte, error) {
	request.TenantID, request.StackID = strings.TrimSpace(request.TenantID), strings.TrimSpace(request.StackID)
	if ctx == nil || builder == nil || builder.attest == nil || request.TenantID == "" || request.StackID == "" {
		return nil, errors.New("managed rollout Inventory requires exact tenant and stack identity")
	}
	process := OperationsProcess{
		ChannelRef: cloudOperationsChannel, SiteRef: cloudOperationsSite, NodeRef: cloudOperationsNode,
		Executable: cloudOperationsExecutable, ExecutableSHA256: builder.executableSHA256,
	}
	candidate := Candidate{StackKitsVersion: request.StackKitsVersion, Digest: request.CandidateDigest, ValidFor: request.ValidFor}
	requirement, _, _, err := validateInventoryAuthority(request.ResolvedPlan, process, candidate)
	if err != nil {
		return nil, err
	}
	if requirement == nil {
		return BuildInventory(request.ResolvedPlan, CustodyReceipt{}, process, candidate)
	}
	if builder.custodian == nil || builder.custody == nil {
		return nil, errors.New("managed backup custody is unavailable for the selected backup capability")
	}
	evidence, err := builder.custodian.Ensure(ctx, request.TenantID, request.StackID)
	if err != nil {
		return nil, fmt.Errorf("ensure managed backup custody: %w", err)
	}
	credentials, err := builder.custody.Get(ctx, request.TenantID, request.StackID)
	if err != nil {
		return nil, fmt.Errorf("load managed backup custody: %w", err)
	}
	evidence.AttestationEvidence = nil
	verified, err := builder.attest(ctx, credentials, evidence)
	if err != nil {
		return nil, fmt.Errorf("attest managed backup target: %w", err)
	}
	verified, err = builder.custody.RecordAttestation(ctx, request.TenantID, request.StackID, verified)
	if err != nil {
		return nil, fmt.Errorf("persist managed backup target attestation: %w", err)
	}
	return BuildInventory(request.ResolvedPlan, verified, process, candidate)
}
