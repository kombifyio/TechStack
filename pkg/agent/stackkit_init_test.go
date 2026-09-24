package agent

import (
	"slices"
	"strings"
	"testing"

	"github.com/kombifyio/techstack/pkg/api/agentpb"
)

func TestStackKitInitArgsAreClosedAndOptionalHashBound(t *testing.T) {
	command := &agentpb.StackKitCommand{
		Operation:         agentpb.StackKitOperation_STACKKIT_OPERATION_INIT,
		OwnerApproved:     true,
		Stackkit:          "basement-kit",
		StackName:         "home-stack",
		Domain:            "home",
		ExpectedSpecHash:  "sha256:" + strings.Repeat("a", 64),
		CandidateSpecJson: []byte(`{"metadata":{"name":"home-stack"},"kit":{"slug":"basement-kit"}}`),
	}
	args, err := stackKitOperationArgs(command)
	if err != nil {
		t.Fatalf("stackKitOperationArgs: %v", err)
	}
	for _, required := range []string{"init", "basement-kit", "--owner-source=local", "--non-interactive", command.ExpectedSpecHash} {
		if !slices.Contains(args, required) {
			t.Fatalf("args = %v, want %q", args, required)
		}
	}
}

// A fresh managed node has no Owner custody, and StackKits (since v0.32.1)
// refuses a local Owner without a real email: managed centron Apply
// 2026-09-23 stopped at init with "local owner email is required before init".
func TestStackKitFreshInitArgsCarryOwnerEmailWithoutReplacementHash(t *testing.T) {
	command := &agentpb.StackKitCommand{Operation: agentpb.StackKitOperation_STACKKIT_OPERATION_INIT, OwnerApproved: true, Stackkit: "cloud-kit", StackName: "cloud-stack", OwnerEmail: "owner@example.com"}
	command.CandidateSpecJson = []byte(`{"metadata":{"name":"cloud-stack"},"kit":{"slug":"cloud-kit"}}`)
	args, err := stackKitOperationArgs(command)
	if err != nil {
		t.Fatalf("stackKitOperationArgs: %v", err)
	}
	if slices.Contains(args, "--expected-spec-hash") {
		t.Fatalf("fresh init args contain replacement CAS flag: %v", args)
	}
	if index := slices.Index(args, "--owner-email"); index < 0 || index+1 >= len(args) || args[index+1] != command.OwnerEmail {
		t.Fatalf("fresh init args = %v, want --owner-email %s", args, command.OwnerEmail)
	}
}

func TestStackKitInitArgsRejectMalformedHash(t *testing.T) {
	command := &agentpb.StackKitCommand{
		Operation:        agentpb.StackKitOperation_STACKKIT_OPERATION_INIT,
		Stackkit:         "basement-kit",
		StackName:        "home-stack",
		ExpectedSpecHash: strings.Repeat("a", 64),
	}
	if _, err := stackKitOperationArgs(command); err == nil || !strings.Contains(err.Error(), "expected_spec_hash") {
		t.Fatalf("unbound hash error = %v, want exact hash refusal", err)
	}
}

func TestStackKitAddressBindArgsAreTypedAndPathBound(t *testing.T) {
	command := &agentpb.StackKitCommand{Operation: agentpb.StackKitOperation_STACKKIT_OPERATION_ADDRESS_BIND, AddressPrefix: "sh-demo-ab12", BoundSpecPath: "stack-spec.address-bound.v2.json"}
	args, err := stackKitOperationArgs(command)
	if err != nil {
		t.Fatalf("stackKitOperationArgs: %v", err)
	}
	if got := strings.Join(args, " "); got != "address bind --prefix sh-demo-ab12 --output stack-spec.address-bound.v2.json" {
		t.Fatalf("args = %q", got)
	}
}
