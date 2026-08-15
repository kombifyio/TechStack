package managedstackkit

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestBuildInventoryProjectsOnlyOpaqueCustodyAndExactProcessBinding(t *testing.T) {
	plan := testResolvedPlan(t)
	inventory, err := BuildInventory(plan, CustodyReceipt{
		BindingEvidence: []byte("tenant/stack/binding"), TargetEvidence: []byte("r2-target-observation"),
		AttestationEvidence: []byte("verified read-write-delete restore sentinel"),
		ObservedAt:          time.Date(2026, 8, 6, 9, 0, 0, 0, time.UTC),
	}, OperationsProcess{
		ChannelRef: "host-channel-cloud-main", SiteRef: "cloud", NodeRef: "cloud-main",
		Executable: "/usr/local/libexec/techstack-stackkit-operations", ExecutableSHA256: testDigest("e"),
	}, Candidate{StackKitsVersion: "v0.14.13", Digest: testDigest("f"), ValidFor: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(inventory, &document); err != nil {
		t.Fatal(err)
	}
	encoded := string(inventory)
	for _, forbidden := range []string{"bucket", "endpoint", "credential", "account", "region", "secret"} {
		if strings.Contains(strings.ToLower(encoded), forbidden) {
			t.Fatalf("Inventory leaked forbidden provider/custody detail %q: %s", forbidden, encoded)
		}
	}
	bindings := document["externalBackupTargetBindings"].(map[string]any)
	binding := bindings["cloud"].(map[string]any)["offsite-object-backup"].(map[string]any)
	if binding["requirementsHash"] != testDigest("c") || binding["siteRef"] != "cloud" {
		t.Fatalf("binding lost exact requirement identity: %#v", binding)
	}
	channels := document["executionChannels"].(map[string]any)
	if len(channels) != 1 || channels["host-channel-cloud-main"] == nil {
		t.Fatalf("execution channels = %#v", channels)
	}
}

func TestBuildInventoryWithoutBackupProjectsOnlyExecutionChannel(t *testing.T) {
	inventory, err := BuildInventory(testResolvedPlanWithoutBackup(t), CustodyReceipt{}, OperationsProcess{
		ChannelRef: "host-channel-cloud-main", SiteRef: "cloud", NodeRef: "cloud-main",
		Executable: "/usr/local/libexec/techstack-stackkit-operations", ExecutableSHA256: testDigest("e"),
	}, Candidate{StackKitsVersion: "v0.14.13", Digest: testDigest("f"), ValidFor: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(inventory, &document); err != nil {
		t.Fatal(err)
	}
	if _, exists := document["externalBackupTargetBindings"]; exists {
		t.Fatalf("channel-only Inventory carried a backup binding: %#v", document)
	}
	channels, ok := document["executionChannels"].(map[string]any)
	if !ok || channels["host-channel-cloud-main"] == nil {
		t.Fatalf("execution channels = %#v", document["executionChannels"])
	}
}

func TestBuildInventoryFailsClosedOnProcessOrCustodyDrift(t *testing.T) {
	baseCustody := CustodyReceipt{
		BindingEvidence: []byte("binding"), TargetEvidence: []byte("target"), AttestationEvidence: []byte("attestation"),
		ObservedAt: time.Date(2026, 8, 6, 9, 0, 0, 0, time.UTC),
	}
	baseProcess := OperationsProcess{ChannelRef: "host-channel-cloud-main", SiteRef: "cloud", NodeRef: "cloud-main", Executable: "/opt/operations", ExecutableSHA256: testDigest("e")}
	baseCandidate := Candidate{StackKitsVersion: "v0.14.13", Digest: testDigest("f"), ValidFor: time.Hour}
	for name, execute := range map[string]func() error{
		"wrong-node": func() error {
			process := baseProcess
			process.NodeRef = "other"
			_, err := BuildInventory(testResolvedPlan(t), baseCustody, process, baseCandidate)
			return err
		},
		"relative-executable": func() error {
			process := baseProcess
			process.Executable = "operations"
			_, err := BuildInventory(testResolvedPlan(t), baseCustody, process, baseCandidate)
			return err
		},
		"missing-attestation": func() error {
			custody := baseCustody
			custody.AttestationEvidence = nil
			_, err := BuildInventory(testResolvedPlan(t), custody, baseProcess, baseCandidate)
			return err
		},
		"long-validity": func() error {
			candidate := baseCandidate
			candidate.ValidFor = 25 * time.Hour
			_, err := BuildInventory(testResolvedPlan(t), baseCustody, baseProcess, candidate)
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := execute(); err == nil {
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
