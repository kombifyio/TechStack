package jobs

import (
	"strings"
	"testing"

	"github.com/kombifyio/techstack/internal/stackkitrelease"
)

// Techstack sent the basement Site/node/channel for every kit, so a cloud-kit
// rollout named a binding its own resolved plan does not contain. The plan for
// cloud-kit resolves sites ["cloud"] and nodes ["cloud-main"], and StackKits
// refuses to guess: "Apply never infers that a planned target is this machine:
// the owner names the exact Site, node, and channel this process owns, and
// anything else stays unadmitted" (cmd/stackkit/commands/apply.go:71).
func TestLocalExecutionBindingFollowsTheKit(t *testing.T) {
	for kit, want := range map[string]localExecutionBinding{
		"basement-kit": {SiteRef: "home", NodeRef: "main", ExecutionChannelRef: "local-home-main"},
		"cloud-kit":    {SiteRef: "cloud", NodeRef: "cloud-main", ExecutionChannelRef: "host-channel-cloud-main"},
	} {
		kit, want := kit, want
		t.Run(kit, func(t *testing.T) {
			got, err := localExecutionBindingFor(kit)
			if err != nil {
				t.Fatalf("localExecutionBindingFor(%q): %v", kit, err)
			}
			if got != want {
				t.Fatalf("binding = %+v, want %+v", got, want)
			}
		})
	}
}

// Inheriting the basement binding for an unrecognised kit is exactly the defect
// this replaced, so an unknown kit must fail closed rather than default.
func TestLocalExecutionBindingFailsClosedForAnUnknownKit(t *testing.T) {
	for _, kit := range []string{"", "modern-homelab", "not-a-kit"} {
		if _, err := localExecutionBindingFor(kit); err == nil {
			t.Fatalf("localExecutionBindingFor(%q) succeeded, want a refusal", kit)
		}
	}
}

// The command the agent receives must carry the kit's binding, not a default.
func TestStackKitLifecycleCommandCarriesTheKitBinding(t *testing.T) {
	command, err := stackKitLifecycleCommand("cmd-1", StackKitLifecycleRequest{
		StackID:   "stack-1",
		AgentID:   "agent-1",
		Operation: "apply",
		StackKit:  "cloud-kit",
	}, stackkitrelease.Release{})
	if err != nil {
		t.Fatalf("stackKitLifecycleCommand: %v", err)
	}
	if command.LocalSiteRef != "cloud" ||
		command.LocalNodeRef != "cloud-main" ||
		command.LocalExecutionChannelRef != "host-channel-cloud-main" {
		t.Fatalf("binding = %q/%q/%q, want the cloud-kit triple",
			command.LocalSiteRef, command.LocalNodeRef, command.LocalExecutionChannelRef)
	}
}

func TestStackKitLifecycleCommandCarriesExplicitCanonicalSpecPath(t *testing.T) {
	inventory := []byte(`{"schemaVersion":"stackkit.inventory/v1","nodes":{}}`)
	request, err := NormalizeStackKitLifecycleRequest(StackKitLifecycleRequest{
		StackID:          "stack-1",
		TenantID:         "tenant-1",
		OwnerID:          "owner-1",
		AgentID:          "agent-1",
		Operation:        StackKitLifecycleApply,
		OwnerApproved:    true,
		WorkingDirectory: "/data/stacks/stack-1",
		SpecPath:         "stack-spec.v2.json",
		StackKit:         "basement-kit",
		InventoryJSON:    inventory,
	})
	if err != nil {
		t.Fatalf("NormalizeStackKitLifecycleRequest: %v", err)
	}
	command, err := stackKitLifecycleCommand("cmd-v2", request, stackkitrelease.Release{})
	if err != nil {
		t.Fatalf("stackKitLifecycleCommand: %v", err)
	}
	if command.SpecPath != "stack-spec.v2.json" {
		t.Fatalf("SpecPath = %q, want stack-spec.v2.json", command.SpecPath)
	}
	if string(command.InventoryJson) != string(inventory) {
		t.Fatalf("InventoryJson = %q, want exact typed Inventory", command.InventoryJson)
	}
	inventory[0] = 'x'
	if command.InventoryJson[0] == 'x' {
		t.Fatal("command retained mutable caller Inventory bytes")
	}
}

func TestStackKitLifecycleInitCarriesExactCustodyInputs(t *testing.T) {
	request, err := NormalizeStackKitLifecycleRequest(StackKitLifecycleRequest{
		StackID:          "stack-1",
		TenantID:         "tenant-1",
		OwnerID:          "owner-1",
		AgentID:          "agent-1",
		Operation:        StackKitLifecycleInit,
		OwnerApproved:    true,
		WorkingDirectory: "/data/stacks/stack-1",
		SpecPath:         "stack-spec.v2.json",
		StackKit:         "basement-kit",
		StackName:        "home-stack",
		Domain:           "home.localhost",
		ExpectedSpecHash: "sha256:" + strings.Repeat("a", 64),
	})
	if err != nil {
		t.Fatalf("NormalizeStackKitLifecycleRequest: %v", err)
	}
	command, err := stackKitLifecycleCommand("cmd-init", request, stackkitrelease.Release{})
	if err != nil {
		t.Fatalf("stackKitLifecycleCommand: %v", err)
	}
	if command.Operation.String() != "STACKKIT_OPERATION_INIT" || command.Stackkit != "basement-kit" ||
		command.StackName != "home-stack" || command.Domain != "home.localhost" ||
		command.ExpectedSpecHash != request.ExpectedSpecHash || !command.OwnerApproved {
		t.Fatalf("init command = %+v, want exact approved custody inputs", command)
	}
}

func TestStackKitLifecyclePayloadRoundTripsInitAuthority(t *testing.T) {
	req := StackKitLifecycleRequest{
		StackID: "stack-1", TenantID: "tenant-1", OwnerID: "owner-1", AgentID: "agent-1",
		Operation: StackKitLifecycleInit, OwnerApproved: true, StackKit: "cloud-kit",
		StackName: "fresh-cloud", ExpectedSpecHash: "sha256:" + strings.Repeat("a", 64),
	}
	job := &Job{TargetID: req.StackID, Payload: StackKitLifecyclePayload(req)}
	got, err := stackKitLifecycleRequestFromJob(job)
	if err != nil {
		t.Fatalf("stackKitLifecycleRequestFromJob() error = %v", err)
	}
	if got.StackKit != req.StackKit || got.StackName != req.StackName || got.ExpectedSpecHash != req.ExpectedSpecHash {
		t.Fatalf("rehydrated init authority = %#v, want kit/name/hash from request", got)
	}
}

func TestStackKitLifecycleCommandRefusesAnUnnamedKit(t *testing.T) {
	_, err := stackKitLifecycleCommand("cmd-1", StackKitLifecycleRequest{
		StackID:   "stack-1",
		AgentID:   "agent-1",
		Operation: "apply",
	}, stackkitrelease.Release{})
	if err == nil || !strings.Contains(err.Error(), "local execution binding") {
		t.Fatalf("error = %v, want a refusal naming the missing binding", err)
	}
}
