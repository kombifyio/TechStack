package jobs

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/kombifyio/techstack/internal/stackkitrelease"
	"github.com/kombifyio/techstack/pkg/api/agentpb"
	"github.com/kombifyio/techstack/pkg/stackkitcommand"
)

// sshTrustNode answers like a node on the pinned CLI.
type sshTrustNode struct{ commands []*agentpb.StackKitCommand }

func (node *sshTrustNode) SendStackKitCommand(_ context.Context, _ string, command *agentpb.StackKitCommand) (*agentpb.StackKitResult, error) {
	node.commands = append(node.commands, command)
	var data any
	switch command.Operation {
	case agentpb.StackKitOperation_STACKKIT_OPERATION_HOST_DELIVERY_RECIPIENT:
		data = map[string]any{"apiVersion": "stackkit.host-recipient-statement/v1", "siteRef": "home", "nodeRef": "main",
			"challenge": command.RecipientChallenge, "ownerPublicKey": "owner-public", "signature": map[string]any{"value": "signed"}}
	case agentpb.StackKitOperation_STACKKIT_OPERATION_HOST_SSH_APPLY:
		data = map[string]any{"operation": "ssh-ca-trust", "state": "trusted", "trustedCaKeys": 1, "users": []string{"ubuntu"}}
	}
	result := &agentpb.StackKitResult{CommandId: command.CommandId, Success: true, Release: command.Release, CommandResultSchemaVersion: stackkitcommand.CommandResultVersion}
	result.CommandResultJson, _ = json.Marshal(map[string]any{
		"schemaVersion": stackkitcommand.CommandResultVersion, "command": stackkitcommand.ResultCommandName(command.Operation), "status": "success", "data": data,
	})
	return result, nil
}

func sshTrustJob(t *testing.T, node StackKitCommandSender, req StackKitLifecycleRequest) (map[string]interface{}, error) {
	t.Helper()
	release := releaseWithAdvancedCatalogVersion(t, stackkitcommand.MinWalletAIProviderRelease)
	handler := StackKitLifecycleHandler(StackKitLifecycleConfig{
		Sender: node, WalletHostDelivery: &fakeWalletHostDelivery{},
		releaseResolver: func() (*stackkitrelease.Release, error) { return &release, nil },
	})
	req.StackID, req.TenantID, req.OwnerID, req.AgentID, req.OwnerApproved = "stack-1", "tenant-1", "owner-1", "agent-1", true
	req.StackKit, req.SpecPath = "basement-kit", "stack-spec.yaml"
	normalized, err := NormalizeStackKitLifecycleRequest(req)
	if err != nil {
		return nil, err
	}
	job := &Job{ID: "job-ssh-trust-1", TargetID: req.StackID, Payload: StackKitLifecyclePayload(normalized)}
	err = handler(context.Background(), job, NewQueue(0, nil))
	outcome, _ := job.Snapshot().Result[SSHTrustResultField].(map[string]interface{})
	return outcome, err
}

// The owner's client gets the node's statement and host target for its own
// challenge, then hands back the Wallet's sealed ssh-ca-trust delivery, which
// reaches the node through the job payload and is applied for the tenant.
func TestSSHTrustRecipientThenApplyReachTheNode(t *testing.T) {
	node := &sshTrustNode{}
	challenge := strings.Repeat("c", 32)
	outcome, err := sshTrustJob(t, node, StackKitLifecycleRequest{Operation: StackKitLifecycleSSHTrustRecipient, RecipientChallenge: challenge})
	if err != nil {
		t.Fatalf("ssh_trust_recipient: %v", err)
	}
	target, _ := outcome["host_target"].(map[string]interface{})
	statement, _ := outcome["statement"].(map[string]interface{})
	tenant := stackkitcommand.DeliveryTenantRef("tenant-1")
	if len(node.commands) != 1 || node.commands[0].DeliveryAuthorityKeyId != "wallet-delivery-1" || node.commands[0].RecipientChallenge != challenge ||
		target["node_ref"] != "main" || target["tenant_ref"] != tenant || target["owner_public_key"] != "owner-public" || statement["challenge"] != challenge {
		t.Fatalf("recipient outcome = %+v after %d commands", outcome, len(node.commands))
	}

	envelope := `{"apiVersion":"stackkit.sealed-host-delivery/v1","sealed":"b3BhcXVl"}`
	outcome, err = sshTrustJob(t, node, StackKitLifecycleRequest{Operation: StackKitLifecycleSSHTrustApply, SealedDelivery: []byte(envelope)})
	if err != nil {
		t.Fatalf("ssh_trust_apply: %v", err)
	}
	applied := node.commands[len(node.commands)-1]
	if applied.Operation != agentpb.StackKitOperation_STACKKIT_OPERATION_HOST_SSH_APPLY || string(applied.SealedDelivery) != envelope ||
		applied.DeliveryTenantRef != tenant || applied.RecipientChallenge != "" || outcome["state"] != "trusted" {
		t.Fatalf("apply command = %+v, outcome = %+v", applied, outcome)
	}
	args, err := stackkitcommand.WalletItemDeliveryArgs(applied)
	if err != nil || strings.Join(args, " ") != "host ssh apply --tenant-ref "+tenant+" --owner-approve" {
		t.Fatalf("argv = %v, %v", args, err)
	}

	if _, err := NormalizeStackKitLifecycleRequest(StackKitLifecycleRequest{
		StackID: "stack-1", TenantID: "tenant-1", OwnerID: "owner-1", OwnerApproved: true,
		Operation: StackKitLifecycleSSHTrustApply, SealedDelivery: []byte("not json"),
	}); err == nil {
		t.Fatal("a sealed delivery that is not a JSON envelope was admitted")
	}
}
