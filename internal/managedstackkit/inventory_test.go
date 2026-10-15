package managedstackkit

import (
	"encoding/json"
	"strings"
	"testing"
)

// The Inventory the control plane hands the node binds the Operations process
// and its channel, and nothing else, even when the plan selects the off-site
// backup capability: the node issues its own backup-target binding.
func TestBuildInventoryNeverCarriesABackupBindingEvenForASelectedCapability(t *testing.T) {
	process := OperationsProcess{
		ChannelRef: "host-channel-cloud-main", SiteRef: "cloud", NodeRef: "cloud-main",
		Executable: "/usr/local/libexec/techstack-stackkit-operations", ExecutableSHA256: testDigest("e"),
	}
	for name, plan := range map[string][]byte{"selected": testResolvedPlan(t), "not selected": testResolvedPlanWithoutBackup(t)} {
		inventory, err := BuildInventory(plan, process)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		var document map[string]any
		if err := json.Unmarshal(inventory, &document); err != nil {
			t.Fatal(err)
		}
		if _, exists := document["externalBackupTargetBindings"]; exists {
			t.Fatalf("%s: the control plane built a backup binding: %s", name, inventory)
		}
		channels, ok := document["executionChannels"].(map[string]any)
		if !ok || len(channels) != 1 || channels["host-channel-cloud-main"] == nil {
			t.Fatalf("%s: execution channels = %#v", name, document["executionChannels"])
		}
	}
}

func TestBuildInventoryFailsClosedOnProcessDrift(t *testing.T) {
	base := OperationsProcess{ChannelRef: "host-channel-cloud-main", SiteRef: "cloud", NodeRef: "cloud-main", Executable: "/opt/operations", ExecutableSHA256: testDigest("e")}
	for name, mutate := range map[string]func(*OperationsProcess){
		"wrong-node":          func(p *OperationsProcess) { p.NodeRef = "other" },
		"relative-executable": func(p *OperationsProcess) { p.Executable = "operations" },
		"unpinned-executable": func(p *OperationsProcess) { p.ExecutableSHA256 = "latest" },
	} {
		t.Run(name, func(t *testing.T) {
			process := base
			mutate(&process)
			if _, err := BuildInventory(testResolvedPlan(t), process); err == nil {
				t.Fatal("invalid managed Inventory input was accepted")
			}
		})
	}
}

func testResolvedPlan(t *testing.T) []byte {
	t.Helper()
	plan := map[string]any{"apiVersion": "stackkit.resolved-plan/v1", "backupTargetRequirements": map[string]any{"cloud": map[string]any{"offsite-object-backup": map[string]any{
		"apiVersion": "stackkit.backup-target-requirement/v1", "kind": "BackupTargetRequirement",
		"stackId": "stack-a", "siteRef": "cloud", "capabilityRef": "offsite-object-backup",
		"contractOwnerRef": "stackkits-cloud-offsite-backup", "capabilityContractHash": testDigest("a"),
		"targetNodeRefs": []string{"cloud-main"}, "policy": map[string]any{
			"scope": "governed-data-only", "encryptionRequired": true, "credentialCustody": "external",
			"targetLifecycle": "external", "restoreVerificationRequired": true, "providerSelection": "external",
		}, "specHash": testDigest("b"), "requirementsHash": testDigest("c"),
	}}}}
	encoded, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func testResolvedPlanWithoutBackup(t *testing.T) []byte {
	t.Helper()
	encoded, err := json.Marshal(map[string]any{
		"apiVersion":               "stackkit.resolved-plan/v1",
		"backupTargetRequirements": map[string]any{},
	})
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func testDigest(character string) string { return "sha256:" + strings.Repeat(character, 64) }
