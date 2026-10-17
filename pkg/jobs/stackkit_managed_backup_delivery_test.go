package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/kombifyio/techstack/internal/stackkitrelease"
	"github.com/kombifyio/techstack/pkg/api/agentpb"
	"github.com/kombifyio/techstack/pkg/stackkitcommand"
	"google.golang.org/protobuf/encoding/protojson"
)

const deliverySecretMarker = "SECRET-MARKER-ACCESS-KEY"

// fakeBackupDelivery is the control plane's delivery authority. Its custody
// holds a secret that must never appear in anything dispatched to the node.
type fakeBackupDelivery struct {
	substituted bool
	sealedFor   []string
	recorded    ManagedBackupNodeBinding
	sealed      bool
}

func (*fakeBackupDelivery) NewChallenge() (string, error)              { return "challenge-1", nil }
func (*fakeBackupDelivery) DeliveryAuthority() (string, string, error) { return "", "", nil }
func (d *fakeBackupDelivery) AdmitRecipient(context.Context, string, string, string, []byte) (ManagedBackupRecipient, error) {
	if d.substituted {
		return ManagedBackupRecipient{}, errors.New("managed backup recipient differs from the pinned enrollment")
	}
	return ManagedBackupRecipient{Public: []byte("recipient-public"), Fingerprint: "sha256:" + strings.Repeat("a", 64), Generation: 1, SiteRef: "cloud", NodeRef: "cloud-main"}, nil
}
func (d *fakeBackupDelivery) Seal(_ context.Context, _, _, _ string, _ ManagedBackupRecipient, operation string) ([]byte, error) {
	d.sealedFor = append(d.sealedFor, operation)
	d.sealed = true
	_ = deliverySecretMarker // sealed: only ciphertext leaves the control plane
	return []byte(`{"apiVersion":"stackkit.sealed-host-delivery/v1","sealed":"b3BhcXVl"}`), nil
}
func (d *fakeBackupDelivery) RecordNodeBinding(_ context.Context, _, _ string, binding ManagedBackupNodeBinding) error {
	d.recorded = binding
	return nil
}
func (d *fakeBackupDelivery) NodeBinding(context.Context, string, string) (ManagedBackupNodeBinding, error) {
	return d.recorded, nil
}
func (*fakeBackupDelivery) AttestTarget(context.Context, string, string) (string, error) {
	return "backup-custody-attestation://sha256/" + strings.Repeat("c", 64), nil
}

// deliveringInventory is an Inventory authority that can also deliver backup.
type deliveringInventory struct {
	delivery *fakeBackupDelivery
	built    *bool
}

func (inventory deliveringInventory) Build(context.Context, ManagedStackKitInventoryRequest) ([]byte, error) {
	if inventory.built != nil {
		*inventory.built = true
	}
	return []byte(`{"schemaVersion":"stackkit.inventory/v1","executionChannels":{"host-channel-cloud-main":{}}}`), nil
}
func (inventory deliveringInventory) BackupDelivery() ManagedBackupTargetDelivery {
	return inventory.delivery
}

// deliveryNode answers like the enrolled node: it proves its recipient key for a
// challenge and issues its own backup binding when it opens a delivery.
type deliveryNode struct {
	*recordingStackKitCommandSender
	operations         []string
	planWithBackup     bool
	commandIDs         map[string]bool
	preparationMode    *bool
	historicalPlan     bool
	renewed, generated bool
}

var nodeIssued = struct{ target, custody string }{
	"backup-target://sha256/" + strings.Repeat("1", 64), "backup-custody-attestation://sha256/" + strings.Repeat("2", 64),
}

func (node *deliveryNode) SendStackKitCommand(ctx context.Context, agent string, command *agentpb.StackKitCommand) (*agentpb.StackKitResult, error) {
	// Like the agent hub, a node refuses a command ID it already holds.
	if node.commandIDs == nil {
		node.commandIDs = map[string]bool{}
	}
	if node.commandIDs[command.CommandId] {
		return nil, fmt.Errorf("typed HTTPS StackKits command_id %q already exists", command.CommandId)
	}
	node.commandIDs[command.CommandId] = true
	node.operations = append(node.operations, strings.TrimPrefix(command.Operation.String(), "STACKKIT_OPERATION_"))
	if node.preparationMode != nil && (command.Operation == agentpb.StackKitOperation_STACKKIT_OPERATION_PLAN || command.Operation == agentpb.StackKitOperation_STACKKIT_OPERATION_BACKUP_TARGET_RECIPIENT) && command.SourceGenerationInspection != *node.preparationMode {
		return nil, fmt.Errorf("preparation transport is unavailable for the admitted release")
	}
	var data map[string]any
	switch command.Operation {
	case agentpb.StackKitOperation_STACKKIT_OPERATION_BACKUP_TARGET_RECIPIENT:
		data = map[string]any{"challenge": command.RecipientChallenge}
	case agentpb.StackKitOperation_STACKKIT_OPERATION_BACKUP_TARGET_IMPORT, agentpb.StackKitOperation_STACKKIT_OPERATION_BACKUP_TARGET_REBIND:
		node.renewed = true
		data = map[string]any{"state": "custodied", "backupTargetRef": nodeIssued.target, "custodyAttestationRef": nodeIssued.custody}
	case agentpb.StackKitOperation_STACKKIT_OPERATION_PLAN:
		result, err := node.recordingStackKitCommandSender.SendStackKitCommand(ctx, agent, command)
		if err == nil {
			result.CommandId = command.CommandId
		}
		if err == nil && node.planWithBackup {
			var envelope map[string]any
			_ = json.Unmarshal(result.CommandResultJson, &envelope)
			if command.SourceGenerationInspection || node.preparationMode != nil {
				var inspection map[string]any
				data := envelope["data"].(map[string]any)
				_ = json.Unmarshal([]byte(data["output"].(string)), &inspection)
				binding := inspection["binding"].(map[string]any)
				current := strings.TrimPrefix(command.Release.Version, "v")
				if node.historicalPlan {
					current = "0.50.6"
				}
				binding["compilerVersion"] = "stackkits-resolver/" + current
				binding["renderer"] = map[string]any{"version": current}
				if node.historicalPlan && command.SourceGenerationInspection {
					data["source_generation_authority"] = map[string]any{
						"apiVersion": "stackkit.source-generation-inspection/v1", "inspection": inspection,
						"sourceRelease":   map[string]any{"schemaVersion": "stackkit.release-receipt/v1", "kit": "cloud-kit", "version": "v0.50.6", "channel": "stable", "archiveSha256": strings.Repeat("a", 64), "indexSha256": strings.Repeat("b", 64)},
						"owner":           map[string]any{"ownerRef": "owner-1", "keyId": "key-1", "pocketIdSubject": "subject-1", "ownerBindingDigest": "sha256:" + strings.Repeat("c", 64)},
						"applyResultHash": "sha256:" + strings.Repeat("d", 64), "evidenceBundleHash": "sha256:" + strings.Repeat("e", 64),
					}
				}
				output, _ := json.Marshal(inspection)
				data["output"] = string(output)
			}
			envelope["data"].(map[string]any)["managed_backup_plan"] = map[string]any{
				"apiVersion": "stackkit.resolved-plan/v1", "kind": "ResolvedPlan", "stackId": "cloud-stack", "planHash": testResolvedPlanHash,
				"backupTargetRequirements": map[string]any{"cloud": map[string]any{"offsite-object-backup": map[string]any{}}},
			}
			result.CommandResultJson, _ = json.Marshal(envelope)
		}
		return result, err
	default:
		if node.preparationMode != nil && command.Operation == agentpb.StackKitOperation_STACKKIT_OPERATION_GENERATE {
			if !node.renewed || command.SourceGenerationInspection {
				return nil, fmt.Errorf("generation did not follow ordinary same-custody renewal")
			}
			node.generated = true
		}
		return node.recordingStackKitCommandSender.SendStackKitCommand(ctx, agent, command)
	}
	node.commands = append(node.commands, command)
	envelope, _ := json.Marshal(map[string]any{
		"schemaVersion": stackkitcommand.CommandResultVersion, "command": stackkitcommand.ResultCommandName(command.Operation), "status": "success", "data": data,
	})
	return &agentpb.StackKitResult{CommandId: command.CommandId, Success: true, CommandResultJson: envelope, Release: command.Release}, nil
}

func (node *deliveryNode) inventoryCarryingCommands() []string {
	carrying := []string{}
	for index, command := range node.commands {
		if len(command.InventoryJson) != 0 {
			carrying = append(carrying, strings.TrimPrefix(command.Operation.String(), "STACKKIT_OPERATION_")+"#"+string(rune('0'+index)))
		}
	}
	return carrying
}

// The managed rollout follows the documented sequence: generate with a
// channel-only Inventory, deliver the sealed credentials so the node issues its
// own binding, then generate, plan and apply without replacing the node's
// Inventory. Nothing dispatched carries a credential or a Core-built binding.
func TestManagedRolloutLetsTheNodeIssueItsOwnBackupBinding(t *testing.T) {
	delivery := &fakeBackupDelivery{}
	node := &deliveryNode{recordingStackKitCommandSender: &recordingStackKitCommandSender{}}
	request := typedStackKitApplyRequest()
	request.InventoryJSON = []byte(`{"schemaVersion":"stackkit.inventory/v1","executionChannels":{"host-channel-cloud-main":{}}}`)
	request.managedBackupDelivery = delivery

	if _, err := runTypedStackKitApplySequence(t.Context(), node, "job-1", request, releaseWithAdvancedCatalog(t)); err != nil {
		t.Fatal(err)
	}
	want := "INIT,ADVANCED_TRUST_IMPORT,GENERATE,BACKUP_TARGET_RECIPIENT,BACKUP_TARGET_IMPORT,GENERATE,PLAN,APPLY"
	if got := strings.Join(node.operations, ","); got != want {
		t.Fatalf("operations = %s, want %s", got, want)
	}
	if carrying := node.inventoryCarryingCommands(); len(carrying) != 1 || !strings.HasPrefix(carrying[0], "GENERATE") {
		t.Fatalf("only the first generate may carry the channel-only Inventory, got %v", carrying)
	}
	if delivery.recorded.BackupTargetRef != nodeIssued.target || delivery.recorded.CustodyAttestationRef != nodeIssued.custody {
		t.Fatalf("Core did not record the binding the node issued: %+v", delivery.recorded)
	}
	for _, command := range node.commands {
		encoded, _ := protojson.Marshal(command)
		if strings.Contains(string(encoded), deliverySecretMarker) || strings.Contains(string(encoded), "externalBackupTargetBindings") {
			t.Fatalf("a dispatched command carries a credential or a Core-built binding: %s", encoded)
		}
	}
}

// A recipient that is not the one pinned at enrollment is never sealed to, and
// the rollout stops before the node's Inventory is generated again.
func TestManagedRolloutStopsBeforeApplyWhenTheRecipientIsSubstituted(t *testing.T) {
	delivery := &fakeBackupDelivery{substituted: true}
	node := &deliveryNode{recordingStackKitCommandSender: &recordingStackKitCommandSender{}}
	request := typedStackKitApplyRequest()
	request.InventoryJSON = []byte(`{"schemaVersion":"stackkit.inventory/v1"}`)
	request.managedBackupDelivery = delivery

	if _, err := runTypedStackKitApplySequence(t.Context(), node, "job-1", request, releaseWithAdvancedCatalog(t)); err == nil {
		t.Fatal("a substituted recipient did not stop the rollout")
	}
	if len(delivery.sealedFor) != 0 {
		t.Fatalf("credentials were sealed to a substituted recipient: %v", delivery.sealedFor)
	}
	if got := strings.Join(node.operations, ","); strings.Contains(got, "BACKUP_TARGET_IMPORT") || strings.Contains(got, "APPLY") {
		t.Fatalf("the rollout went on after the refusal: %s", got)
	}
}

// A managed generation renews the node's binding over identical custody before
// it regenerates, and the Inventory it sends stays channel-only.
func TestManagedGenerationRenewsTheNodeIssuedBindingBeforeGenerating(t *testing.T) {
	for _, scenario := range []struct {
		name, version                  string
		inspection, historical, denied bool
	}{
		{name: "ordinary-current", version: "v0.51.1"},
		{name: "ordinary-history-denied", version: "v0.51.1", historical: true, denied: true},
		{name: "supporting-current", version: "v0.51.3", inspection: true},
		{name: "supporting-history", version: "v0.51.3", inspection: true, historical: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			delivery := &fakeBackupDelivery{recorded: ManagedBackupNodeBinding{BackupTargetRef: nodeIssued.target, CustodyAttestationRef: nodeIssued.custody}}
			release := releaseWithAdvancedCatalogVersion(t, scenario.version)
			node := &deliveryNode{recordingStackKitCommandSender: &recordingStackKitCommandSender{}, planWithBackup: true, preparationMode: &scenario.inspection, historicalPlan: scenario.historical}
			built := false
			cfg := StackKitLifecycleConfig{
				Sender: node, ManagedStackKitInventory: deliveringInventory{delivery: delivery, built: &built},
				releaseResolver: func() (*stackkitrelease.Release, error) { return &release, nil },
				ManagedAddressAuthority: func(context.Context, StackKitLifecycleRequest) (ManagedAddressRuntime, error) {
					return ManagedAddressRuntime{LeaseID: "lease-1", ServerID: "server-1", Generation: 1, Rollout: StackKitRolloutBinding{StackKit: "cloud-kit", SpecPath: "bound.json"}}, nil
				},
			}
			req := StackKitLifecycleRequest{StackID: "deployment-1", StackKitInstanceID: "cloud-stack", TenantID: "tenant-1", OwnerID: "owner-1", AgentID: "agent-1", NodeID: "cloud-main", ManagedRuntimeLeaseID: "lease-1", ManagedRuntimeServerID: "server-1", Operation: StackKitLifecycleGenerate, StackKit: "cloud-kit", SpecPath: "bound.json"}
			job := &Job{ID: "job-generate-1", TargetID: req.StackID, Payload: StackKitLifecyclePayload(req)}
			err := StackKitLifecycleHandler(cfg)(t.Context(), job, NewQueue(0, nil))
			if scenario.denied {
				if err == nil || node.renewed || node.generated || built || delivery.sealed {
					t.Fatal("ordinary historical denial allowed credential or Inventory effects")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !delivery.sealed || !node.renewed || !node.generated || !built || delivery.recorded.BackupTargetRef != nodeIssued.target || delivery.recorded.CustodyAttestationRef != nodeIssued.custody {
				t.Fatal("generation failed to retain and renew the node-issued custody")
			}
			for _, operation := range delivery.sealedFor {
				if operation != StackKitLifecycleBackupTargetRebind {
					t.Fatal("generation sealed credentials for a different operation")
				}
			}
		})
	}
}
