package jobs

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/kombifyio/techstack/internal/stackkitrelease"
	"github.com/kombifyio/techstack/pkg/api/agentpb"
	"github.com/kombifyio/techstack/pkg/stackkitcommand"
	"google.golang.org/protobuf/encoding/protojson"
)

const aiProviderKeyMarker = "sk-PROVIDER-KEY-MARKER"

// fakeWalletHostDelivery seals an item only for the node whose statement it
// was handed; its custody holds the provider key, which never leaves sealed.
type fakeWalletHostDelivery struct {
	target    WalletHostTarget
	statement string
	challenge string
	item      string
}

func (*fakeWalletHostDelivery) Authority() (string, string, error) {
	return "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8", "wallet-delivery-1", nil
}

func (w *fakeWalletHostDelivery) DeliverItem(_ context.Context, owner, itemID string, rotate bool, target WalletHostTarget, statement []byte, challenge string, expiresAt time.Time) (WalletHostEnvelope, error) {
	w.target, w.statement, w.challenge, w.item = target, string(statement), challenge, owner+"/"+itemID
	_ = aiProviderKeyMarker // sealed: only ciphertext leaves the Wallet
	return WalletHostEnvelope{Envelope: []byte(`{"apiVersion":"stackkit.sealed-host-delivery/v1","sealed":"b3BhcXVl"}`), Version: "3"}, nil
}

// aiProviderNode answers like a node on the pinned CLI.
type aiProviderNode struct{ commands []*agentpb.StackKitCommand }

func (node *aiProviderNode) SendStackKitCommand(_ context.Context, _ string, command *agentpb.StackKitCommand) (*agentpb.StackKitResult, error) {
	node.commands = append(node.commands, command)
	changeSet := "sha256:" + strings.Repeat("e", 64)
	var data any
	result := &agentpb.StackKitResult{CommandId: command.CommandId, Success: true, Release: command.Release, CommandResultSchemaVersion: stackkitcommand.CommandResultVersion}
	switch command.Operation {
	case agentpb.StackKitOperation_STACKKIT_OPERATION_HOST_DELIVERY_RECIPIENT:
		data = map[string]any{"apiVersion": "stackkit.host-delivery-recipient/v1", "siteRef": "home", "nodeRef": "home-main",
			"challenge": command.RecipientChallenge, "ownerPublicKey": "owner-public", "signature": "signed"}
	case agentpb.StackKitOperation_STACKKIT_OPERATION_WALLET_ITEM_IMPORT:
		data = map[string]any{"tenantRef": command.DeliveryTenantRef, "itemRef": "itm_" + strings.Repeat("a", 26), "version": 3}
	case agentpb.StackKitOperation_STACKKIT_OPERATION_AI_PROVIDER_SETUP:
		data = map[string]any{"workloadRef": "ai", "provider": "enabled", "baseUrl": command.AiProviderBaseUrl, "itemRef": command.WalletItemRef, "specHash": "sha256:" + strings.Repeat("d", 64)}
	case agentpb.StackKitOperation_STACKKIT_OPERATION_ADVANCED_CHANGE_SET_CREATE:
		result.AdvancedChangeSetSha256 = "sha256:" + strings.Repeat("f", 64)
		data = map[string]any{"schemaVersion": "stackkit.advanced-change-set/v2", "changeSetId": changeSet, "path": ".stackkit/advanced/change-sets/e.json"}
	case agentpb.StackKitOperation_STACKKIT_OPERATION_ADVANCED_DRIFT_RECONCILE:
		data = map[string]any{"schemaVersion": "stackkit.advanced-mutation/v1", "changeSetId": changeSet, "driftReport": map[string]any{"schemaVersion": "stackkit.drift-report/v1", "status": "clean"}}
	}
	result.CommandResultJson, _ = json.Marshal(map[string]any{
		"schemaVersion": stackkitcommand.CommandResultVersion, "command": stackkitcommand.ResultCommandName(command.Operation), "status": "success", "data": data,
	})
	return result, nil
}

func aiProviderJob(t *testing.T, version string, node StackKitCommandSender, wallet WalletHostDelivery) (*Job, error) {
	t.Helper()
	release := releaseWithAdvancedCatalogVersion(t, version)
	handler := StackKitLifecycleHandler(StackKitLifecycleConfig{
		Sender: node, AdvancedIssuer: staticAdvancedIssuer{}, WalletHostDelivery: wallet,
		releaseResolver: func() (*stackkitrelease.Release, error) { return &release, nil },
	})
	req := StackKitLifecycleRequest{
		StackID: "stack-1", TenantID: "tenant-1", OwnerID: "owner-1", AgentID: "agent-1", OwnerApproved: true,
		Operation: StackKitLifecycleAIProvider, StackKit: "basement-kit", SpecPath: "stack-spec.yaml",
		AIProviderBaseURL: "https://api.openai.com/v1/", WalletItemID: "itm_" + strings.Repeat("a", 26),
	}
	job := &Job{ID: "job-ai-provider-1", TargetID: req.StackID, Payload: StackKitLifecyclePayload(req)}
	return job, handler(context.Background(), job, NewQueue(0, nil))
}

// The Wallet seals the provider key to the recipient the node proved, the node
// opens and binds it, and the stack is reconciled. Nothing dispatched carries
// the key: only the Wallet's ciphertext reaches the node.
func TestAIProviderDeliversTheWalletItemSealedAndReconciles(t *testing.T) {
	node, wallet := &aiProviderNode{}, &fakeWalletHostDelivery{}
	if _, err := aiProviderJob(t, stackkitcommand.MinWalletAIProviderRelease, node, wallet); err != nil {
		t.Fatalf("ai_provider: %v", err)
	}
	operations := make([]string, 0, len(node.commands))
	for _, command := range node.commands {
		operations = append(operations, strings.TrimPrefix(command.Operation.String(), "STACKKIT_OPERATION_"))
		encoded, _ := protojson.Marshal(command)
		if strings.Contains(string(encoded), aiProviderKeyMarker) {
			t.Fatalf("a dispatched command carries the provider key: %s", encoded)
		}
	}
	if got, want := strings.Join(operations, ","), "HOST_DELIVERY_RECIPIENT,WALLET_ITEM_IMPORT,AI_PROVIDER_SETUP,ADVANCED_CHANGE_SET_CREATE,ADVANCED_DRIFT_RECONCILE"; got != want {
		t.Fatalf("operations = %s, want %s", got, want)
	}
	recipient, imported, setup := node.commands[0], node.commands[1], node.commands[2]
	if recipient.DeliveryAuthorityKeyId != "wallet-delivery-1" || wallet.challenge != recipient.RecipientChallenge ||
		!strings.Contains(wallet.statement, `"nodeRef":"home-main"`) || wallet.item != "owner-1/itm_"+strings.Repeat("a", 26) {
		t.Fatalf("the Wallet sealed for statement %s, challenge %q, item %q; the node pinned %q for challenge %q",
			wallet.statement, wallet.challenge, wallet.item, recipient.DeliveryAuthorityKeyId, recipient.RecipientChallenge)
	}
	tenant := stackkitcommand.DeliveryTenantRef("tenant-1")
	if wallet.target != (WalletHostTarget{SiteRef: "home", NodeRef: "home-main", TenantRef: tenant, OwnerPublicKey: "owner-public"}) {
		t.Fatalf("host target = %+v", wallet.target)
	}
	if !strings.Contains(string(imported.SealedDelivery), "sealed-host-delivery") || imported.DeliveryTenantRef != tenant {
		t.Fatalf("the node did not receive the Wallet's envelope for its tenant: %+v", imported)
	}
	if setup.AiProviderBaseUrl != "https://api.openai.com/v1" || setup.WalletItemRef != "itm_"+strings.Repeat("a", 26) || setup.DeliveryTenantRef != tenant {
		t.Fatalf("setup = %+v, want the normalized provider bound to the delivered item", setup)
	}
}

// A pin older than the release that ships `stackkit setup ai-provider` fails
// closed before the node is asked for anything or the Wallet discloses.
func TestAIProviderFailsClosedOnAnOlderStackKitsPin(t *testing.T) {
	node, wallet := &aiProviderNode{}, &fakeWalletHostDelivery{}
	job, err := aiProviderJob(t, "v0.52.6", node, wallet)
	if err == nil || len(node.commands) != 0 || wallet.item != "" {
		t.Fatalf("err = %v, commands = %d, delivered = %q; want a closed failure before any effect", err, len(node.commands), wallet.item)
	}
	outcome, _ := job.Snapshot().Result[AIProviderResultField].(map[string]interface{})
	if outcome["reason_code"] != "stackkits_release_too_old" {
		t.Fatalf("result = %+v, want the release reason", job.Snapshot().Result)
	}
}
