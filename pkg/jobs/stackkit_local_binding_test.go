package jobs

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/kombifyio/techstack/internal/stackkitrelease"
	"github.com/kombifyio/techstack/pkg/api/agentpb"
	"google.golang.org/protobuf/proto"
)

// The typed command must carry each kit's own Site/node/channel rather than a
// basement default that the resolved plan cannot admit.
func TestStackKitLifecycleCommandCarriesTheKitBinding(t *testing.T) {
	for kit, want := range map[string]localExecutionBinding{
		"basement-kit": {SiteRef: "home", NodeRef: "main", ExecutionChannelRef: "local-home-main"},
		"cloud-kit":    {SiteRef: "cloud", NodeRef: "cloud-main", ExecutionChannelRef: "host-channel-cloud-main"},
	} {
		kit, want := kit, want
		t.Run(kit, func(t *testing.T) {
			command, err := stackKitLifecycleCommand("cmd-1", StackKitLifecycleRequest{
				StackID: "stack-1", AgentID: "agent-1", Operation: "apply", StackKit: kit,
			}, stackkitrelease.Release{})
			if err != nil {
				t.Fatalf("stackKitLifecycleCommand: %v", err)
			}
			got := localExecutionBinding{SiteRef: command.LocalSiteRef, NodeRef: command.LocalNodeRef, ExecutionChannelRef: command.LocalExecutionChannelRef}
			if got != want {
				t.Fatalf("command binding = %+v, want %+v", got, want)
			}
		})
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
		StackID:           "stack-1",
		TenantID:          "tenant-1",
		OwnerID:           "owner-1",
		AgentID:           "agent-1",
		Operation:         StackKitLifecycleInit,
		OwnerApproved:     true,
		WorkingDirectory:  "/data/stacks/stack-1",
		SpecPath:          "stack-spec.v2.json",
		StackKit:          "basement-kit",
		StackName:         "home-stack",
		Domain:            "home",
		ExpectedSpecHash:  "sha256:" + strings.Repeat("a", 64),
		CandidateSpecJSON: []byte(`{"metadata":{"name":"home-stack"},"kit":{"slug":"basement-kit"}}`),
	})
	if err != nil {
		t.Fatalf("NormalizeStackKitLifecycleRequest: %v", err)
	}
	command, err := stackKitLifecycleCommand("cmd-init", request, stackkitrelease.Release{})
	if err != nil {
		t.Fatalf("stackKitLifecycleCommand: %v", err)
	}
	if command.Operation.String() != "STACKKIT_OPERATION_INIT" || command.Stackkit != "basement-kit" ||
		command.StackName != "home-stack" || command.Domain != "home" ||
		command.ExpectedSpecHash != request.ExpectedSpecHash || !command.OwnerApproved {
		t.Fatalf("init command = %+v, want exact approved custody inputs", command)
	}
}

func TestStackKitLifecyclePayloadRoundTripsInitAuthority(t *testing.T) {
	req := StackKitLifecycleRequest{
		StackID: "stack-1", TenantID: "tenant-1", OwnerID: "owner-1", AgentID: "agent-1",
		NodeID:    "node-1",
		Operation: StackKitLifecycleInit, OwnerApproved: true, StackKit: "cloud-kit",
		StackName: "fresh-cloud", ExpectedSpecHash: "sha256:" + strings.Repeat("a", 64),
		CandidateSpecJSON: []byte(`{"apiVersion":"stackkit/v2alpha2","metadata":{"name":"fresh-cloud"},"kit":{"slug":"cloud-kit"},"workloads":{"cloud-core":{"alternative":"standalone"}},"modules":{"stackkits-cloud-core-runtime":{"computeProfile":"standard"}}}`),
	}
	job := &Job{TargetID: req.StackID, Payload: StackKitLifecyclePayload(req)}
	payload, err := json.Marshal(job.Payload)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(payload, &job.Payload); err != nil {
		t.Fatal(err)
	}
	got, err := stackKitLifecycleRequestFromJob(job)
	if err != nil {
		t.Fatalf("stackKitLifecycleRequestFromJob() error = %v", err)
	}
	if got.StackKit != req.StackKit || got.NodeID != req.NodeID || got.StackName != req.StackName || got.ExpectedSpecHash != req.ExpectedSpecHash {
		t.Fatalf("rehydrated init authority = %#v, want kit/name/hash from request", got)
	}
	command, err := stackKitLifecycleCommand("cmd-init", got, stackkitrelease.Release{})
	if err != nil {
		t.Fatal(err)
	}
	wire, err := proto.Marshal(command)
	if err != nil {
		t.Fatal(err)
	}
	var received agentpb.StackKitCommand
	if err := proto.Unmarshal(wire, &received); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(received.CandidateSpecJson, req.CandidateSpecJSON) {
		t.Fatal("durable payload or agent transport changed approved intent")
	}
}

// Inheriting the basement binding for an unrecognised kit is exactly the defect
// this replaced, so the typed command must fail closed rather than default.
func TestStackKitLifecycleCommandFailsClosedForUnknownKits(t *testing.T) {
	for _, kit := range []string{"", "not-a-kit"} {
		if _, err := stackKitLifecycleCommand("cmd-1", StackKitLifecycleRequest{
			StackID: "stack-1", AgentID: "agent-1", Operation: "apply", StackKit: kit,
		}, stackkitrelease.Release{}); err == nil {
			t.Fatalf("stackKitLifecycleCommand accepted unknown kit %q", kit)
		}
	}
}

// A Modern Homelab command carries the Site, node and channel of the node its
// agent owns, and an agent that owns none of the kit's nodes dispatches nothing.
func TestStackKitLifecycleCommandBindsTheAddressedModernNode(t *testing.T) {
	for node, want := range map[string][3]string{
		"home-main":  {"home", "home-main", "local-home-main"},
		"cloud-edge": {"cloud", "cloud-edge", "local-cloud-edge"},
	} {
		command, err := stackKitLifecycleCommand("cmd-1", StackKitLifecycleRequest{
			StackID: "stack-1", AgentID: "agent-" + node, Operation: "plan", StackKit: "modern-homelab", NodeRef: node,
		}, stackkitrelease.Release{})
		if err != nil {
			t.Fatalf("node %s: %v", node, err)
		}
		if got := [3]string{command.LocalSiteRef, command.LocalNodeRef, command.LocalExecutionChannelRef}; got != want {
			t.Fatalf("node %s bound %v, want %v", node, got, want)
		}
	}
	for _, node := range []string{"", "main", "cloud-main"} {
		if _, err := stackKitLifecycleCommand("cmd-1", StackKitLifecycleRequest{
			StackID: "stack-1", AgentID: "agent-1", Operation: "apply", StackKit: "modern-homelab", NodeRef: node,
		}, stackkitrelease.Release{}); err == nil {
			t.Fatalf("modern-homelab dispatched to unbound node %q", node)
		}
	}
}
