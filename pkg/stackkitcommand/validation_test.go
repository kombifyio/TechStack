package stackkitcommand

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/api/agentpb"
	"google.golang.org/protobuf/proto"
)

func validationCommand() *agentpb.StackKitCommand {
	digest := strings.Repeat("a", 64)
	return &agentpb.StackKitCommand{
		CommandId: "service-command-1", Operation: agentpb.StackKitOperation_STACKKIT_OPERATION_SERVICE_RESTART,
		WorkingDirectory: "/srv/stack", OwnerApproved: true, ServiceKey: "coolify", StackkitInstanceId: "cloud-main",
		Release: &agentpb.StackKitReleasePin{Version: "v0.16.0", PlatformOs: "linux", PlatformArch: "amd64", ArchiveSha256: digest, ReleaseIndexSha256: digest},
	}
}

func TestRestoreActivationReceiptBindsOriginalResultPlanAndOwner(t *testing.T) {
	command := validationCommand()
	command.Operation = agentpb.StackKitOperation_STACKKIT_OPERATION_BACKUP_RESTORE_RECOVER
	command.Release.Version = MinRestoreActivationRelease
	command.Stackkit = "basement-kit"
	command.LocalSiteRef, command.LocalNodeRef, command.LocalExecutionChannelRef = "home", "node-a", "channel-a"
	command.RestoreResultId = "sha256:" + strings.Repeat("b", 64)
	command.ExpectedPlanHash = "sha256:" + strings.Repeat("c", 64)
	command.ActivationOperationId = "restore-original-job"
	if err := ValidateCommand(command); err != nil {
		t.Fatal(err)
	}
	data := map[string]any{"apiVersion": "stackkit.restore-activation-result/v1", "operationId": command.ActivationOperationId, "restoreResultId": command.RestoreResultId, "safetySnapshotId": "sha256:" + strings.Repeat("d", 64), "planHash": command.ExpectedPlanHash, "managedVolumeSetHash": "sha256:" + strings.Repeat("e", 64), "status": "recovered",
		"verification": map[string]any{"apiVersion": "stackkit.local-backup-restore-verification/v1", "ownerRef": "owner/node", "ownerBindingDigest": "sha256:" + strings.Repeat("f", 64), "pocketIdSubject": "owner-subject", "planHash": command.ExpectedPlanHash, "servicesVerified": true, "verifiedAt": time.Now().UTC()},
		"signature":    map[string]any{"ownerRef": "owner/node", "keyId": "node-key", "value": "node-verified-signature"}}
	for _, scenario := range []string{"recovered", "activated", "result", "operation", "plan", "owner", "unattested", "foreign-release", "foreign-command", "unsupported-pin", "published-old-pin", "no-approval", "managed-kit", "original-release", "original-foreign-index", "original-missing-proof", "original-unsupported-A", "original-unsupported-B", "original-new-activation"} {
		t.Run(scenario, func(t *testing.T) {
			c := proto.Clone(command).(*agentpb.StackKitCommand)
			raw, _ := json.Marshal(data)
			var d map[string]any
			_ = json.Unmarshal(raw, &d)
			if strings.HasPrefix(scenario, "original-") {
				c.Release.Version = MinOriginalRecoveryRelease
				c.OriginalRecoveryRelease = proto.Clone(c.Release).(*agentpb.StackKitReleasePin)
				c.OriginalRecoveryRelease.Version = "v0.52.5"
				d["originalRecoveryRelease"] = map[string]any{"version": "v0.52.5", "platform": map[string]any{"os": c.Release.PlatformOs, "arch": c.Release.PlatformArch}, "archiveSha256": c.OriginalRecoveryRelease.ArchiveSha256, "releaseIndexSha256": c.OriginalRecoveryRelease.ReleaseIndexSha256}
			}
			switch scenario {
			case "original-foreign-index":
				d["originalRecoveryRelease"].(map[string]any)["releaseIndexSha256"] = strings.Repeat("e", 64)
			case "original-missing-proof":
				delete(d, "originalRecoveryRelease")
			case "original-unsupported-A":
				c.OriginalRecoveryRelease.Version = "v0.52.4"
			case "original-unsupported-B":
				c.Release.Version = "v0.52.5"
			case "original-new-activation":
				c.Operation = agentpb.StackKitOperation_STACKKIT_OPERATION_BACKUP_RESTORE_ACTIVATE
			case "activated":
				d["status"] = "activated"
			case "result":
				d["restoreResultId"] = "sha256:" + strings.Repeat("a", 64)
			case "operation":
				d["operationId"] = "restore-foreign-job"
			case "plan":
				d["planHash"] = "sha256:" + strings.Repeat("a", 64)
			case "owner":
				d["signature"].(map[string]any)["ownerRef"] = "owner/foreign"
			case "unsupported-pin":
				c.Release.Version = "v0.51.7"
			case "published-old-pin":
				c.Release.Version = "v0.51.8"
			case "no-approval":
				c.OwnerApproved = false
			case "managed-kit":
				c.Stackkit = "cloud-kit"
			}
			body, _ := json.Marshal(map[string]any{"schemaVersion": CommandResultVersion, "command": ResultCommandName(c.Operation), "status": "success", "data": d})
			r := validationResult(c)
			r.CommandResultJson = body
			r.PinnedCliEvidenceVerified = true
			r.PinnedCliEvidenceSha256 = fmt.Sprintf("sha256:%x", sha256.Sum256(body))
			if scenario == "unattested" {
				r.PinnedCliEvidenceVerified = false
			}
			if scenario == "foreign-release" {
				r.Release.ArchiveSha256 = strings.Repeat("c", 64)
			}
			if scenario == "foreign-command" {
				r.CommandId = "other-command"
			}
			err := ValidateCommand(c)
			if err == nil {
				err = ValidateResult(r, c)
			}
			allowed := scenario == "activated" || scenario == "recovered" || scenario == "original-release"
			if allowed != (err == nil) {
				t.Fatalf("receipt admission=%v error=%v", allowed, err)
			}
		})
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
	command.OwnerApproved = true
	command.StackkitInstanceId = ""
	if err := ValidateCommand(command); err == nil {
		t.Fatal("ValidateCommand() accepted a service mutation without StackKit instance identity")
	}
}

func TestValidateCommandAllowsFreshInitWithoutReplacementHash(t *testing.T) {
	command := validationCommand()
	command.Operation = agentpb.StackKitOperation_STACKKIT_OPERATION_INIT
	command.OwnerApproved = true
	command.Stackkit = "cloud-kit"
	command.StackName = "fresh-cloud"
	command.CandidateSpecJson = []byte(`{"metadata":{"name":"fresh-cloud"},"kit":{"slug":"cloud-kit"}}`)
	command.ExpectedSpecHash = ""
	if err := ValidateCommand(command); err != nil {
		t.Fatalf("ValidateCommand() fresh init error = %v", err)
	}

	command.ExpectedSpecHash = "not-a-spec-hash"
	if err := ValidateCommand(command); err == nil {
		t.Fatal("ValidateCommand() accepted an invalid replacement hash")
	}
	command.ExpectedSpecHash = ""
	for _, candidate := range [][]byte{nil, []byte(`{}`), []byte(`{"metadata":{"name":"other-stack"},"kit":{"slug":"cloud-kit"}}`)} {
		command.CandidateSpecJson = candidate
		if err := ValidateCommand(command); err == nil {
			t.Fatal("init admitted absent or mismatched approved intent")
		}
	}
}

func TestValidateCommandRequiresCompleteAddressBinding(t *testing.T) {
	command := validationCommand()
	command.Operation, command.AddressPrefix, command.BoundSpecPath = agentpb.StackKitOperation_STACKKIT_OPERATION_ADDRESS_BIND, "sh-demo-ab12", "stack-spec.address-bound.v2.json"
	if err := ValidateCommand(command); err != nil {
		t.Fatalf("ValidateCommand() address bind error = %v", err)
	}
	command.AddressPrefix = "invalid.prefix"
	if err := ValidateCommand(command); err == nil {
		t.Fatal("ValidateCommand() accepted an invalid address prefix")
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
	command.SourceGenerationInspection = true
	if err := ValidateCommand(command); err == nil {
		t.Fatal("Apply admitted historical preparation authority")
	}
}

func TestRequiredAgentCapabilitiesFollowMutationAuthority(t *testing.T) {
	command := validationCommand()
	if got := RequiredAgentCapabilities(command); len(got) != 1 || got[0] != WorkspaceInstanceCapability {
		t.Fatalf("service capabilities = %v", got)
	}
	command.Operation = agentpb.StackKitOperation_STACKKIT_OPERATION_BACKUP_RESTORE
	if got := RequiredAgentCapabilities(command); len(got) != 1 || got[0] != WorkspaceInstanceCapability {
		t.Fatalf("restore capabilities = %v", got)
	}
}

func TestValidateCommandRequiresBoundRemoveAuthority(t *testing.T) {
	command := validationCommand()
	command.Operation, command.WorkloadRef = agentpb.StackKitOperation_STACKKIT_OPERATION_REMOVE, "photos"
	command.LocalSiteRef, command.LocalNodeRef, command.LocalExecutionChannelRef = "home", "node-a", "channel-a"
	if err := ValidateCommand(command); err != nil {
		t.Fatalf("ValidateCommand() remove error = %v", err)
	}
	command.StackkitInstanceId = ""
	if err := ValidateCommand(command); err == nil {
		t.Fatal("ValidateCommand() accepted remove without workspace identity")
	}
	command.StackkitInstanceId, command.LocalNodeRef = "cloud-main", ""
	if err := ValidateCommand(command); err == nil {
		t.Fatal("ValidateCommand() accepted remove without exact local placement")
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

func TestValidateResultAcceptsCompletedAdvancedMutation(t *testing.T) {
	command := validationCommand()
	result := validationResult(command)
	result.EventsJsonl = [][]byte{[]byte(`{"time":"2026-09-27T16:00:00Z","phase":"advanced.mutation.target-apply","status":"completed"}`)}
	if err := ValidateResult(result, command); err != nil {
		t.Fatalf("ValidateResult() rejected completed Advanced mutation: %v", err)
	}
}

func TestValidateCommandConfinesTheAdvancedTrustBundleToTrustImport(t *testing.T) {
	bundle := []byte(`{"keys":[],"schemaVersion":"stackkit.advanced-trust-bundle/v1"}`)
	command := validationCommand()
	command.Operation = agentpb.StackKitOperation_STACKKIT_OPERATION_ADVANCED_TRUST_IMPORT
	command.AdvancedTrustBundle = bundle
	if err := ValidateCommand(command); err != nil {
		t.Fatalf("ValidateCommand() trust import error = %v", err)
	}
	command.AdvancedTrustBundle = nil
	if err := ValidateCommand(command); err == nil {
		t.Fatal("ValidateCommand() accepted a trust import without a bundle")
	}
	command.AdvancedTrustBundle = make([]byte, MaxAdvancedTrustBundleBytes+1)
	if err := ValidateCommand(command); err == nil {
		t.Fatal("ValidateCommand() accepted a trust bundle above 64 KiB")
	}
	restart := validationCommand()
	restart.AdvancedTrustBundle = bundle
	if err := ValidateCommand(restart); err == nil {
		t.Fatal("ValidateCommand() accepted a trust bundle on a service operation")
	}
}

func TestValidateCommandAdmitsOnlyAdvancedModeForManagedDeployments(t *testing.T) {
	capability := []byte(`{"schemaVersion":"stackkit.advanced-capability/v1"}`)
	drill := validationCommand()
	drill.Operation = agentpb.StackKitOperation_STACKKIT_OPERATION_ADVANCED_RESTORE_DRILL
	drill.AdvancedCapability = capability
	if err := ValidateCommand(drill); err != nil {
		t.Fatalf("ValidateCommand() Advanced restore drill error = %v", err)
	}
	drill.AdvancedCapability = nil
	if err := ValidateCommand(drill); err == nil {
		t.Fatal("ValidateCommand() accepted an Advanced operation without a capability")
	}
	standard := validationCommand()
	standard.Operation = agentpb.StackKitOperation_STACKKIT_OPERATION_DRIFT_RECONCILE
	standard.DriftMode = agentpb.StackKitDriftMode_STACKKIT_DRIFT_MODE_STANDARD
	if err := ValidateCommand(standard); err == nil {
		t.Fatal("ValidateCommand() accepted a Standard drift reconcile for a managed deployment")
	}
	restart := validationCommand()
	restart.AdvancedCapability = capability
	if err := ValidateCommand(restart); err == nil {
		t.Fatal("ValidateCommand() accepted a capability on a non-Advanced operation")
	}
}
