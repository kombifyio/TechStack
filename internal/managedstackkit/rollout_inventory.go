package managedstackkit

import (
	"context"
	"errors"
	"strings"
)

const (
	cloudOperationsExecutable = "/usr/local/libexec/techstack-stackkit-operations"
	cloudOperationsChannel    = "host-channel-cloud-main"
	cloudOperationsSite       = "cloud"
	cloudOperationsNode       = "cloud-main"
)

type RolloutInventoryRequest struct {
	TenantID     string
	StackID      string
	ResolvedPlan []byte
	// Nodes is set for a multi-node kit: one channel per node.
	Nodes []NodeChannel
}

// RolloutInventory builds the Inventory a managed rollout hands the node: the
// enrolled Operations process, its execution channels and nothing else. Backup
// custody and the backup-target binding belong to the node (see BackupDelivery).
type RolloutInventory struct {
	executableSHA256 string
}

func NewRolloutInventory(executableSHA256 string) (*RolloutInventory, error) {
	if !validDigest(executableSHA256) {
		return nil, errors.New("managed rollout Inventory requires an exact Operations executable digest")
	}
	return &RolloutInventory{executableSHA256: executableSHA256}, nil
}

func (builder *RolloutInventory) Build(ctx context.Context, request RolloutInventoryRequest) ([]byte, error) {
	request.TenantID, request.StackID = strings.TrimSpace(request.TenantID), strings.TrimSpace(request.StackID)
	if ctx == nil || builder == nil || request.TenantID == "" || request.StackID == "" {
		return nil, errors.New("managed rollout Inventory requires exact tenant and stack identity")
	}
	if len(request.Nodes) > 0 {
		return BuildNodeInventory(request.ResolvedPlan, request.Nodes, builder.executableSHA256)
	}
	return BuildInventory(request.ResolvedPlan, OperationsProcess{
		ChannelRef: cloudOperationsChannel, SiteRef: cloudOperationsSite, NodeRef: cloudOperationsNode,
		Executable: cloudOperationsExecutable, ExecutableSHA256: builder.executableSHA256,
	})
}
