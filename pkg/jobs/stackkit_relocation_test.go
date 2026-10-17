package jobs

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/kombifyio/techstack/internal/stackkitrelease"
	"github.com/kombifyio/techstack/pkg/api/agentpb"
	"github.com/kombifyio/techstack/pkg/stackkitcommand"
)

// relocationNodes answers like the two nodes of a move on the pinned CLI; the
// destination's restore fails.
type relocationNodes struct {
	mu       sync.Mutex
	commands map[string][]string
}

func (nodes *relocationNodes) SendStackKitCommand(_ context.Context, agentID string, command *agentpb.StackKitCommand) (*agentpb.StackKitResult, error) {
	if err := stackkitcommand.ValidateCommand(command); err != nil {
		return nil, err
	}
	nodes.mu.Lock()
	nodes.commands[agentID] = append(nodes.commands[agentID], strings.TrimPrefix(command.Operation.String(), "STACKKIT_OPERATION_"))
	nodes.mu.Unlock()
	result := &agentpb.StackKitResult{CommandId: command.CommandId, Success: true, Release: command.Release, CommandResultSchemaVersion: stackkitcommand.CommandResultVersion}
	status := "success"
	var data any
	switch command.Operation {
	case agentpb.StackKitOperation_STACKKIT_OPERATION_HOST_DELIVERY_RECIPIENT:
		data = map[string]any{"siteRef": "home", "nodeRef": agentID, "challenge": command.RecipientChallenge, "ownerPublicKey": "owner-public"}
	case agentpb.StackKitOperation_STACKKIT_OPERATION_WALLET_ITEM_IMPORT:
		data = map[string]any{"tenantRef": command.DeliveryTenantRef, "itemRef": "itm_" + strings.Repeat("a", 26), "version": 1}
	case agentpb.StackKitOperation_STACKKIT_OPERATION_RELOCATE_PLAN:
		data = map[string]any{"format": stackkitcommand.RelocationPlanFormat, "projects": command.RelocationProjects}
	case agentpb.StackKitOperation_STACKKIT_OPERATION_RELOCATE_CHECK:
		data = map[string]any{"ready": true, "blockers": []string{}, "warnings": []string{}}
	case agentpb.StackKitOperation_STACKKIT_OPERATION_RELOCATE_DRAIN:
		data = map[string]any{"format": stackkitcommand.RelocationDrainFormat, "resources": []map[string]any{{"uuid": "app-1", "was_running": true, "stopped": true}}}
	case agentpb.StackKitOperation_STACKKIT_OPERATION_RELOCATE_UNDRAIN:
		data = map[string]any{"format": stackkitcommand.RelocationUndrainFormat}
	case agentpb.StackKitOperation_STACKKIT_OPERATION_RELOCATE_SEND:
		data = map[string]any{"delivered": true}
	case agentpb.StackKitOperation_STACKKIT_OPERATION_RELOCATE_RECEIVE:
		result.Success, result.ExitCode, result.Stderr, status = false, 1, "coolify-mirror restore failed (exit 1)", "failed"
	}
	result.CommandResultJson, _ = json.Marshal(map[string]any{
		"schemaVersion": stackkitcommand.CommandResultVersion, "command": stackkitcommand.ResultCommandName(command.Operation), "status": status, "data": data,
	})
	result.EventsSchemaVersion = stackkitcommand.RolloutEventVersion
	return result, stackkitcommand.ValidateResult(result, command)
}

// A move whose restore fails on the destination starts the drained workloads
// on the source again: the source never stays stopped behind a failed move.
func TestWorkloadRelocationRestartsTheSourceWhenTheDestinationFails(t *testing.T) {
	nodes := &relocationNodes{commands: map[string][]string{}}
	release := releaseWithAdvancedCatalogVersion(t, stackkitcommand.MinWorkloadRelocationRelease)
	handler := WorkloadRelocationHandler(StackKitLifecycleConfig{
		Sender: nodes, WalletHostDelivery: &fakeWalletHostDelivery{},
		releaseResolver: func() (*stackkitrelease.Release, error) { return &release, nil },
	})
	payload, err := WorkloadRelocationPayload(WorkloadRelocationRequest{
		TenantID: "tenant-1", OwnerID: "owner-1", OwnerApproved: true,
		Source:      WorkloadRelocationNode{StackID: "stack-a", AgentID: "agent-a", StackKit: "basement-kit"},
		Destination: WorkloadRelocationNode{StackID: "stack-b", AgentID: "agent-b", StackKit: "basement-kit"},
		Projects:    []string{"shop"}, SourceAddress: "10.0.0.5", WalletItemID: "itm_" + strings.Repeat("a", 26),
		DomainSwitches: []string{"shop.example.com=shop.10.0.0.6.sslip.io"},
	})
	if err != nil {
		t.Fatal(err)
	}
	job := &Job{ID: "job-move-1", TargetID: "stack-a", Payload: payload}
	err = handler(context.Background(), job, NewQueue(0, nil))
	if err == nil || !strings.Contains(err.Error(), "the source runs again") {
		t.Fatalf("err = %v, want the failed move rolled back", err)
	}
	if got, want := strings.Join(nodes.commands["agent-a"], ","), "HOST_DELIVERY_RECIPIENT,WALLET_ITEM_IMPORT,RELOCATE_PLAN,RELOCATE_DRAIN,RELOCATE_SEND,RELOCATE_UNDRAIN"; got != want {
		t.Fatalf("source ran %s, want %s", got, want)
	}
	if got, want := strings.Join(nodes.commands["agent-b"], ","), "HOST_DELIVERY_RECIPIENT,WALLET_ITEM_IMPORT,RELOCATE_CHECK,RELOCATE_RECEIVE"; got != want {
		t.Fatalf("destination ran %s, want %s", got, want)
	}
	outcome, _ := job.Snapshot().Result[WorkloadRelocationResultField].(map[string]interface{})
	if outcome["state"] != "rolled_back" {
		t.Fatalf("result = %+v, want state rolled_back", job.Snapshot().Result)
	}
}

// A move switches restored domains only to test domains until real-host
// acceptance; a production name is refused before any node is asked.
func TestWorkloadRelocationRefusesAProductionDomainSwitch(t *testing.T) {
	_, err := NormalizeWorkloadRelocationRequest(WorkloadRelocationRequest{
		TenantID: "tenant-1", OwnerID: "owner-1", OwnerApproved: true,
		Source:      WorkloadRelocationNode{StackID: "stack-a", AgentID: "agent-a"},
		Destination: WorkloadRelocationNode{StackID: "stack-b", AgentID: "agent-b"},
		Projects:    []string{"shop"}, SourceAddress: "10.0.0.5", WalletItemID: "itm_" + strings.Repeat("a", 26),
		DomainSwitches: []string{"shop.example.com=shop.kombify.me"},
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "test domains") {
		t.Fatalf("err = %v, want the production domain refused", err)
	}
}
