// Package stackkitcommand owns the transport-neutral validation contract for
// typed StackKits commands and results. Every agent transport must use this
// exact validator before dispatching a command or accepting its evidence.
package stackkitcommand

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/kombifyio/techstack/pkg/api/agentpb"
)

const (
	CommandResultVersion       = "stackkit.command-result/v1"
	RolloutEventVersion        = "stackkit.rollout-event/v1"
	ExpectedPlanHashCapability = "stackkit.apply.expected-plan-hash.v1"
	maxResultBytes             = 16 << 20
	maxEventBytes              = 16 << 20
	maxEvents                  = 10_000
)

var (
	commandIDPattern  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
	versionPattern    = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+(?:-(?:beta|edge)\.[0-9]+)?$`)
	sha256Pattern     = regexp.MustCompile(`^[0-9a-f]{64}$`)
	specHashPattern   = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	targetPattern     = regexp.MustCompile(`^(latest|v[0-9]+\.[0-9]+\.[0-9]+(?:-(?:beta|edge)\.[0-9]+)?|channel:(?:stable|beta|edge))$`)
	serviceKeyPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
)

func ValidateCommand(command *agentpb.StackKitCommand) error {
	if command == nil {
		return fmt.Errorf("StackKit command is required")
	}
	command.CommandId = strings.TrimSpace(command.CommandId)
	if !commandIDPattern.MatchString(command.CommandId) {
		return fmt.Errorf("StackKit command_id is invalid")
	}
	if strings.TrimSpace(command.WorkingDirectory) == "" {
		return fmt.Errorf("StackKit working_directory is required")
	}
	if command.TimeoutSeconds < 0 || command.TimeoutSeconds > int32((2*time.Hour)/time.Second) {
		return fmt.Errorf("StackKit timeout_seconds must be between 0 and 7200")
	}
	if err := ValidateReleasePin(command.Release); err != nil {
		return err
	}
	switch command.Operation {
	case agentpb.StackKitOperation_STACKKIT_OPERATION_INIT:
		if strings.TrimSpace(command.Stackkit) == "" || strings.TrimSpace(command.StackName) == "" {
			return fmt.Errorf("StackKit init requires StackKit and stack name")
		}
		// A fresh owner-local workspace has no current StackSpec to compare and
		// swap, so create intentionally carries no expected hash. Replacing an
		// existing spec remains bound to its exact normalized hash.
		if expected := strings.TrimSpace(command.ExpectedSpecHash); expected != "" && !specHashPattern.MatchString(expected) {
			return fmt.Errorf("StackKit init expected spec hash is invalid")
		}
	case agentpb.StackKitOperation_STACKKIT_OPERATION_VALIDATE,
		agentpb.StackKitOperation_STACKKIT_OPERATION_GENERATE,
		agentpb.StackKitOperation_STACKKIT_OPERATION_PLAN,
		agentpb.StackKitOperation_STACKKIT_OPERATION_VERIFY,
		agentpb.StackKitOperation_STACKKIT_OPERATION_DRIFT_DETECT:
	case agentpb.StackKitOperation_STACKKIT_OPERATION_APPLY:
		if strings.TrimSpace(command.LocalSiteRef) == "" || strings.TrimSpace(command.LocalNodeRef) == "" || strings.TrimSpace(command.LocalExecutionChannelRef) == "" {
			return fmt.Errorf("StackKit apply requires local Site, node, and execution-channel references")
		}
		if !specHashPattern.MatchString(strings.TrimSpace(command.ExpectedPlanHash)) {
			return fmt.Errorf("StackKit apply expected_plan_hash is required and must be a lowercase sha256:<64-hex> digest")
		}
	case agentpb.StackKitOperation_STACKKIT_OPERATION_UPGRADE:
		if !command.DryRun && !command.OwnerApproved {
			return fmt.Errorf("StackKit upgrade requires Owner approval")
		}
		target := strings.TrimSpace(command.TargetRelease)
		if target != "" && !targetPattern.MatchString(target) {
			return fmt.Errorf("StackKit upgrade target_release is invalid")
		}
	case agentpb.StackKitOperation_STACKKIT_OPERATION_DRIFT_RECONCILE:
		if !command.OwnerApproved {
			return fmt.Errorf("StackKit drift reconcile requires Owner approval")
		}
		if command.DriftMode != agentpb.StackKitDriftMode_STACKKIT_DRIFT_MODE_STANDARD && command.DriftMode != agentpb.StackKitDriftMode_STACKKIT_DRIFT_MODE_ADVANCED {
			return fmt.Errorf("StackKit drift reconcile requires an explicit drift_mode")
		}
	case agentpb.StackKitOperation_STACKKIT_OPERATION_SERVICE_START,
		agentpb.StackKitOperation_STACKKIT_OPERATION_SERVICE_STOP,
		agentpb.StackKitOperation_STACKKIT_OPERATION_SERVICE_RESTART:
		if !command.OwnerApproved {
			return fmt.Errorf("StackKit service mutation requires Owner approval")
		}
		if !serviceKeyPattern.MatchString(strings.TrimSpace(command.ServiceKey)) {
			return fmt.Errorf("StackKit service_key is invalid")
		}
	case agentpb.StackKitOperation_STACKKIT_OPERATION_SERVICE_LOGS:
		if !serviceKeyPattern.MatchString(strings.TrimSpace(command.ServiceKey)) {
			return fmt.Errorf("StackKit service_key is invalid")
		}
		if command.LogTail < 1 || command.LogTail > 200 {
			return fmt.Errorf("StackKit service log_tail must be between 1 and 200")
		}
	default:
		return fmt.Errorf("unsupported StackKit operation %s", command.Operation.String())
	}
	return nil
}

func ValidateReleasePin(pin *agentpb.StackKitReleasePin) error {
	if pin == nil {
		return fmt.Errorf("StackKit release pin is required")
	}
	if !versionPattern.MatchString(strings.TrimSpace(pin.Version)) {
		return fmt.Errorf("StackKit release version is invalid")
	}
	if strings.TrimSpace(pin.PlatformOs) == "" || strings.TrimSpace(pin.PlatformArch) == "" {
		return fmt.Errorf("StackKit release platform is required")
	}
	if !sha256Pattern.MatchString(strings.TrimSpace(pin.ArchiveSha256)) || !sha256Pattern.MatchString(strings.TrimSpace(pin.ReleaseIndexSha256)) {
		return fmt.Errorf("StackKit release digests must be lowercase SHA-256")
	}
	return nil
}

func ValidateResult(result *agentpb.StackKitResult, command *agentpb.StackKitCommand) error {
	if result == nil || command == nil {
		return fmt.Errorf("StackKit result and pending command are required")
	}
	if result.CommandId != command.CommandId {
		return fmt.Errorf("StackKit result command_id does not match pending command")
	}
	if result.CommandResultSchemaVersion != CommandResultVersion {
		return fmt.Errorf("StackKit result has unsupported command-result schema")
	}
	if len(result.CommandResultJson) == 0 || len(result.CommandResultJson) > maxResultBytes {
		return fmt.Errorf("StackKit result command-result payload size is invalid")
	}
	if err := requireMatchingRelease(result.Release, command.Release); err != nil {
		return err
	}
	var envelope struct {
		SchemaVersion string `json:"schemaVersion"`
		Command       string `json:"command"`
		Status        string `json:"status"`
	}
	if err := json.Unmarshal(result.CommandResultJson, &envelope); err != nil {
		return fmt.Errorf("decode StackKit command-result: %w", err)
	}
	if envelope.SchemaVersion != CommandResultVersion || envelope.Command != ResultCommandName(command.Operation) {
		return fmt.Errorf("StackKit command-result identity is invalid")
	}
	switch envelope.Status {
	case "success":
		if !result.Success || result.ExitCode != 0 {
			return fmt.Errorf("StackKit successful command-result conflicts with transport status")
		}
	case "failed", "denied":
		if result.Success || result.ExitCode == 0 {
			return fmt.Errorf("StackKit failed command-result conflicts with transport status")
		}
	default:
		return fmt.Errorf("StackKit command-result status is invalid")
	}
	if result.EventsSchemaVersion != RolloutEventVersion {
		return fmt.Errorf("StackKit result has unsupported rollout-event schema")
	}
	if len(result.EventsJsonl) > maxEvents {
		return fmt.Errorf("StackKit result has too many rollout events")
	}
	total := 0
	for _, event := range result.EventsJsonl {
		total += len(event) + 1
		if total > maxEventBytes {
			return fmt.Errorf("StackKit rollout events exceed the transport limit")
		}
		var identity struct {
			Time   time.Time `json:"time"`
			Phase  string    `json:"phase"`
			Status string    `json:"status"`
		}
		if !json.Valid(event) || json.Unmarshal(event, &identity) != nil || identity.Time.IsZero() || strings.TrimSpace(identity.Phase) == "" || !validRolloutStatus(identity.Status) {
			return fmt.Errorf("StackKit result contains a malformed rollout event")
		}
	}
	return nil
}

// ResultCommandName binds a typed operation to the command identity carried by
// stackkit.command-result/v1. The Agent and every accepting transport share
// this mapping so evidence for one operation cannot satisfy another.
func ResultCommandName(operation agentpb.StackKitOperation) string {
	switch operation {
	case agentpb.StackKitOperation_STACKKIT_OPERATION_INIT:
		return "stackkit init"
	case agentpb.StackKitOperation_STACKKIT_OPERATION_VALIDATE:
		return "stackkit validate"
	case agentpb.StackKitOperation_STACKKIT_OPERATION_GENERATE:
		return "stackkit generate"
	case agentpb.StackKitOperation_STACKKIT_OPERATION_PLAN:
		return "stackkit plan"
	case agentpb.StackKitOperation_STACKKIT_OPERATION_APPLY:
		return "stackkit apply"
	case agentpb.StackKitOperation_STACKKIT_OPERATION_VERIFY:
		return "stackkit verify"
	case agentpb.StackKitOperation_STACKKIT_OPERATION_UPGRADE:
		return "stackkit upgrade"
	case agentpb.StackKitOperation_STACKKIT_OPERATION_DRIFT_DETECT:
		return "stackkit drift detect"
	case agentpb.StackKitOperation_STACKKIT_OPERATION_DRIFT_RECONCILE:
		return "stackkit drift reconcile"
	case agentpb.StackKitOperation_STACKKIT_OPERATION_SERVICE_START:
		return "service_start"
	case agentpb.StackKitOperation_STACKKIT_OPERATION_SERVICE_STOP:
		return "service_stop"
	case agentpb.StackKitOperation_STACKKIT_OPERATION_SERVICE_RESTART:
		return "service_restart"
	case agentpb.StackKitOperation_STACKKIT_OPERATION_SERVICE_LOGS:
		return "service_logs"
	default:
		return ""
	}
}

func requireMatchingRelease(actual, expected *agentpb.StackKitReleasePin) error {
	if actual == nil || expected == nil || actual.Version != expected.Version || actual.PlatformOs != expected.PlatformOs || actual.PlatformArch != expected.PlatformArch || actual.ArchiveSha256 != expected.ArchiveSha256 || actual.ReleaseIndexSha256 != expected.ReleaseIndexSha256 {
		return fmt.Errorf("StackKit result release does not match dispatched release")
	}
	return nil
}

func validRolloutStatus(status string) bool {
	switch status {
	case "started", "running", "succeeded", "failed", "skipped":
		return true
	default:
		return false
	}
}
