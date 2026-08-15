package stackkitcommand

import (
	"strings"
	"testing"

	"github.com/kombifyio/techstack/pkg/api/agentpb"
	"google.golang.org/protobuf/proto"
)

func validationCommand() *agentpb.StackKitCommand {
	digest := strings.Repeat("a", 64)
	return &agentpb.StackKitCommand{
		CommandId: "service-command-1", Operation: agentpb.StackKitOperation_STACKKIT_OPERATION_SERVICE_RESTART,
		WorkingDirectory: "/srv/stack", OwnerApproved: true, ServiceKey: "coolify",
		Release: &agentpb.StackKitReleasePin{Version: "v0.16.0", PlatformOs: "linux", PlatformArch: "amd64", ArchiveSha256: digest, ReleaseIndexSha256: digest},
	}
}

func validationResult(command *agentpb.StackKitCommand) *agentpb.StackKitResult {
	return &agentpb.StackKitResult{
		CommandId: command.CommandId, Success: true, Release: proto.Clone(command.Release).(*agentpb.StackKitReleasePin),
		CommandResultSchemaVersion: CommandResultVersion,
		CommandResultJson:          []byte(`{"schemaVersion":"stackkit.command-result/v1","command":"service_restart","status":"success"}`),
		EventsSchemaVersion:        RolloutEventVersion,
	}
}

func TestValidateCommandRequiresBoundServiceMutation(t *testing.T) {
	command := validationCommand()
	if err := ValidateCommand(command); err != nil {
		t.Fatalf("ValidateCommand() error = %v", err)
	}
	command.OwnerApproved = false
	if err := ValidateCommand(command); err == nil {
		t.Fatal("ValidateCommand() accepted a service mutation without Owner approval")
	}
}

func TestValidateCommandAllowsFreshInitWithoutReplacementHash(t *testing.T) {
	command := validationCommand()
	command.Operation = agentpb.StackKitOperation_STACKKIT_OPERATION_INIT
	command.OwnerApproved = true
	command.Stackkit = "cloud-kit"
	command.StackName = "fresh-cloud"
	command.ExpectedSpecHash = ""
	if err := ValidateCommand(command); err != nil {
		t.Fatalf("ValidateCommand() fresh init error = %v", err)
	}

	command.ExpectedSpecHash = "not-a-spec-hash"
	if err := ValidateCommand(command); err == nil {
		t.Fatal("ValidateCommand() accepted an invalid replacement hash")
	}
}

func TestValidateCommandRequiresExpectedPlanHashForApply(t *testing.T) {
	command := validationCommand()
	command.Operation = agentpb.StackKitOperation_STACKKIT_OPERATION_APPLY
	command.LocalSiteRef = "cloud"
	command.LocalNodeRef = "cloud-main"
	command.LocalExecutionChannelRef = "host-channel-cloud-main"
	if err := ValidateCommand(command); err == nil {
		t.Fatal("ValidateCommand() accepted Apply without expected_plan_hash")
	}
	command.ExpectedPlanHash = "sha256:" + strings.Repeat("b", 64)
	if err := ValidateCommand(command); err != nil {
		t.Fatalf("ValidateCommand() hash-bound Apply error = %v", err)
	}
}

func TestValidateResultBindsReleaseEnvelopeStatusAndEvents(t *testing.T) {
	command := validationCommand()
	if err := ValidateResult(validationResult(command), command); err != nil {
		t.Fatalf("ValidateResult() error = %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*agentpb.StackKitResult)
	}{
		{name: "release substitution", mutate: func(result *agentpb.StackKitResult) { result.Release.ArchiveSha256 = strings.Repeat("b", 64) }},
		{name: "transport status conflict", mutate: func(result *agentpb.StackKitResult) { result.Success = false; result.ExitCode = 1 }},
		{name: "operation substitution", mutate: func(result *agentpb.StackKitResult) {
			result.CommandResultJson = []byte(`{"schemaVersion":"stackkit.command-result/v1","command":"stackkit apply","status":"success"}`)
		}},
		{name: "malformed event", mutate: func(result *agentpb.StackKitResult) { result.EventsJsonl = [][]byte{[]byte(`{"phase":"service"}`)} }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := validationResult(command)
			test.mutate(result)
			if err := ValidateResult(result, command); err == nil {
				t.Fatal("ValidateResult() accepted substituted or malformed evidence")
			}
		})
	}
}
