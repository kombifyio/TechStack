package agent

import (
	"slices"
	"strings"
	"testing"

	"github.com/kombifyio/techstack/pkg/api/agentpb"
)

func TestStackKitInitArgsAreClosedAndOptionalHashBound(t *testing.T) {
	command := &agentpb.StackKitCommand{
		Operation:        agentpb.StackKitOperation_STACKKIT_OPERATION_INIT,
		OwnerApproved:    true,
		Stackkit:         "basement-kit",
		StackName:        "home-stack",
		Domain:           "home.localhost",
		ExpectedSpecHash: "sha256:" + strings.Repeat("a", 64),
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

func TestStackKitInitArgsCreateWithoutReplacementHash(t *testing.T) {
	command := &agentpb.StackKitCommand{Operation: agentpb.StackKitOperation_STACKKIT_OPERATION_INIT, OwnerApproved: true, Stackkit: "cloud-kit", StackName: "cloud-stack"}
	args, err := stackKitOperationArgs(command)
	if err != nil {
		t.Fatalf("stackKitOperationArgs: %v", err)
	}
	if slices.Contains(args, "--expected-spec-hash") {
		t.Fatalf("fresh init args contain replacement CAS flag: %v", args)
	}
}

func TestStackKitInitArgsRequireApprovalAndExactHash(t *testing.T) {
	base := &agentpb.StackKitCommand{
		Operation:        agentpb.StackKitOperation_STACKKIT_OPERATION_INIT,
		Stackkit:         "basement-kit",
		StackName:        "home-stack",
		ExpectedSpecHash: "sha256:" + strings.Repeat("a", 64),
	}
	if _, err := stackKitOperationArgs(base); err == nil || !strings.Contains(err.Error(), "Owner approval") {
		t.Fatalf("unapproved error = %v, want Owner approval refusal", err)
	}
	base.OwnerApproved = true
	base.ExpectedSpecHash = strings.Repeat("a", 64)
	if _, err := stackKitOperationArgs(base); err == nil || !strings.Contains(err.Error(), "expected_spec_hash") {
		t.Fatalf("unbound hash error = %v, want exact hash refusal", err)
	}
}
