package agent

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/api/agentpb"
)

func TestStackKitApplyRequestsStructuredResult(t *testing.T) {
	args, err := stackKitOperationArgs(&agentpb.StackKitCommand{
		Operation:        agentpb.StackKitOperation_STACKKIT_OPERATION_APPLY,
		ExpectedPlanHash: "sha256:" + strings.Repeat("a", 64),
		LocalSiteRef:     "home", LocalNodeRef: "main", LocalExecutionChannelRef: "local-home-main",
	})
	if err != nil || !slices.Contains(args, "--json") {
		t.Fatalf("StackKit Apply args = %v, %v; structured result is required", args, err)
	}
}

func TestStackKitResultCannotCarryAnotherCommandIdentity(t *testing.T) {
	command := &agentpb.StackKitCommand{CommandId: "apply-1", Operation: agentpb.StackKitOperation_STACKKIT_OPERATION_APPLY}
	result := finishStackKitResult(&agentpb.StackKitResult{CommandId: command.CommandId}, command, []byte(`{"schemaVersion":"stackkit.command-result/v1","command":"stackkit plan","status":"success"}`), nil, nil, time.Now())
	var envelope struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal(result.CommandResultJson, &envelope); err != nil || envelope.Command != "stackkit apply" {
		t.Fatalf("fallback command identity = %q, %v", envelope.Command, err)
	}
}
