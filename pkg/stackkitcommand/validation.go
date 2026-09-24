// Package stackkitcommand owns the transport-neutral validation contract for
// typed StackKits commands and results. Every agent transport must use this
// exact validator before dispatching a command or accepting its evidence.
package stackkitcommand

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/kombifyio/techstack/pkg/api/agentpb"
)

const (
	CommandResultVersion        = "stackkit.command-result/v1"
	RolloutEventVersion         = "stackkit.rollout-event/v1"
	ExpectedPlanHashCapability  = "stackkit.apply.expected-plan-hash.v1"
	WorkspaceInstanceCapability = "stackkit.workspace-instance-binding.v1"
	maxResultBytes              = 16 << 20
	maxEventBytes               = 16 << 20
	maxEvents                   = 10_000
	MaxInitCandidateBytes       = 2 << 20
)

var (
	commandIDPattern     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
	versionPattern       = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+(?:-(?:beta|edge)\.[0-9]+)?$`)
	sha256Pattern        = regexp.MustCompile(`^[0-9a-f]{64}$`)
	specHashPattern      = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	targetPattern        = regexp.MustCompile(`^(latest|v[0-9]+\.[0-9]+\.[0-9]+(?:-(?:beta|edge)\.[0-9]+)?|channel:(?:stable|beta|edge))$`)
	serviceKeyPattern    = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
	addressPrefixPattern = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
	householdUserPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
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
	if command.Operation != agentpb.StackKitOperation_STACKKIT_OPERATION_INIT && len(command.CandidateSpecJson) != 0 {
		return fmt.Errorf("StackKit candidate intent is only valid for init")
	}
	if command.Operation != agentpb.StackKitOperation_STACKKIT_OPERATION_HOUSEHOLD_INVITE &&
		(strings.TrimSpace(command.HouseholdUsername) != "" || strings.TrimSpace(command.HouseholdEmail) != "" || strings.TrimSpace(command.HouseholdDisplayName) != "") {
		return fmt.Errorf("StackKit household identity fields are only valid for household invite")
	}
	if command.Operation != agentpb.StackKitOperation_STACKKIT_OPERATION_INIT && strings.TrimSpace(command.OwnerEmail) != "" {
		return fmt.Errorf("StackKit owner_email is only valid for init")
	}
	switch command.Operation {
	case agentpb.StackKitOperation_STACKKIT_OPERATION_INIT:
		return validateInitCommand(command)
	case agentpb.StackKitOperation_STACKKIT_OPERATION_ADDRESS_BIND:
		return validateAddressBindCommand(command)
	case agentpb.StackKitOperation_STACKKIT_OPERATION_VALIDATE,
		agentpb.StackKitOperation_STACKKIT_OPERATION_GENERATE,
		agentpb.StackKitOperation_STACKKIT_OPERATION_PLAN,
		agentpb.StackKitOperation_STACKKIT_OPERATION_VERIFY,
		agentpb.StackKitOperation_STACKKIT_OPERATION_DRIFT_DETECT:
		return nil
	case agentpb.StackKitOperation_STACKKIT_OPERATION_APPLY:
		return validateApplyCommand(command)
	case agentpb.StackKitOperation_STACKKIT_OPERATION_UPGRADE:
		return validateUpgradeCommand(command)
	case agentpb.StackKitOperation_STACKKIT_OPERATION_DRIFT_RECONCILE:
		return validateDriftReconcileCommand(command)
	case agentpb.StackKitOperation_STACKKIT_OPERATION_SERVICE_START,
		agentpb.StackKitOperation_STACKKIT_OPERATION_SERVICE_STOP,
		agentpb.StackKitOperation_STACKKIT_OPERATION_SERVICE_RESTART:
		return validateServiceMutationCommand(command)
	case agentpb.StackKitOperation_STACKKIT_OPERATION_SERVICE_LOGS:
		return validateServiceLogsCommand(command)
	case agentpb.StackKitOperation_STACKKIT_OPERATION_REMOVE:
		return validateRemoveCommand(command)
	case agentpb.StackKitOperation_STACKKIT_OPERATION_BACKUP_RUN,
		agentpb.StackKitOperation_STACKKIT_OPERATION_BACKUP_STATUS,
		agentpb.StackKitOperation_STACKKIT_OPERATION_BACKUP_CONFIGURE:
		return validateBackupReadOrRunCommand(command)
	case agentpb.StackKitOperation_STACKKIT_OPERATION_BACKUP_RESTORE:
		return validateBackupRestoreCommand(command)
	case agentpb.StackKitOperation_STACKKIT_OPERATION_OWNER_ACTIVATION_STATUS,
		agentpb.StackKitOperation_STACKKIT_OPERATION_HOUSEHOLD_LIST:
		if command.OwnerApproved {
			return fmt.Errorf("StackKit identity read must not carry Owner approval")
		}
		return nil
	case agentpb.StackKitOperation_STACKKIT_OPERATION_OWNER_ACTIVATION_ISSUE:
		if !command.OwnerApproved {
			return fmt.Errorf("StackKit owner activation requires Owner approval")
		}
		return nil
	case agentpb.StackKitOperation_STACKKIT_OPERATION_HOUSEHOLD_INVITE:
		return validateHouseholdInviteCommand(command)
	default:
		return fmt.Errorf("unsupported StackKit operation %s", command.Operation.String())
	}
}

func validateHouseholdInviteCommand(command *agentpb.StackKitCommand) error {
	if !command.OwnerApproved {
		return fmt.Errorf("StackKit household invite requires Owner approval")
	}
	command.HouseholdUsername = strings.TrimSpace(command.HouseholdUsername)
	command.HouseholdEmail = strings.TrimSpace(command.HouseholdEmail)
	command.HouseholdDisplayName = strings.TrimSpace(command.HouseholdDisplayName)
	if !householdUserPattern.MatchString(command.HouseholdUsername) {
		return fmt.Errorf("StackKit household username is invalid")
	}
	if len(command.HouseholdEmail) > 254 || !strings.Contains(command.HouseholdEmail, "@") || strings.ContainsAny(command.HouseholdEmail, "\r\n") {
		return fmt.Errorf("StackKit household email is invalid")
	}
	if len(command.HouseholdDisplayName) > 128 || strings.ContainsAny(command.HouseholdDisplayName, "\r\n") {
		return fmt.Errorf("StackKit household display name is invalid")
	}
	return nil
}

func validateBackupReadOrRunCommand(command *agentpb.StackKitCommand) error {
	instanceID := strings.TrimSpace(command.StackkitInstanceId)
	planHash := strings.TrimSpace(command.ExpectedPlanHash)
	if (instanceID == "") != (planHash == "") {
		return fmt.Errorf("StackKit backup authority must bind both stackkit_instance_id and expected_plan_hash")
	}
	if planHash != "" && !specHashPattern.MatchString(planHash) {
		return fmt.Errorf("StackKit backup expected_plan_hash is invalid")
	}
	return nil
}

func validateBackupRestoreCommand(command *agentpb.StackKitCommand) error {
	if !command.OwnerApproved {
		return fmt.Errorf("StackKit backup restore requires Owner approval")
	}
	if strings.TrimSpace(command.StackkitInstanceId) == "" {
		return fmt.Errorf("StackKit backup restore requires stackkit_instance_id")
	}
	if !specHashPattern.MatchString(strings.TrimSpace(command.ExpectedPlanHash)) {
		return fmt.Errorf("StackKit backup restore requires expected_plan_hash")
	}
	if !specHashPattern.MatchString(strings.TrimSpace(command.SnapshotAnchorId)) {
		return fmt.Errorf("StackKit backup restore requires a snapshot_anchor_id")
	}
	return nil
}

func validateInitCommand(command *agentpb.StackKitCommand) error {
	if strings.TrimSpace(command.Stackkit) == "" || strings.TrimSpace(command.StackName) == "" {
		return fmt.Errorf("StackKit init requires StackKit and stack name")
	}
	// A fresh owner-local workspace has no current StackSpec to compare and
	// swap, so create intentionally carries no expected hash. Replacing an
	// existing spec remains bound to its exact normalized hash.
	if expected := strings.TrimSpace(command.ExpectedSpecHash); expected != "" && !specHashPattern.MatchString(expected) {
		return fmt.Errorf("StackKit init expected spec hash is invalid")
	}
	if email := command.OwnerEmail; email != "" && !ValidOwnerEmail(email) {
		return fmt.Errorf("StackKit init owner_email is invalid")
	}
	return ValidateInitCandidate(command.CandidateSpecJson, command.Stackkit, command.StackName)
}

// ownerEmailPattern admits one plain address: no whitespace, no leading dash
// (it becomes a CLI flag value), exactly one @ and a dotted domain.
var ownerEmailPattern = regexp.MustCompile(`^[A-Za-z0-9._%+'-]+@[A-Za-z0-9-]+(\.[A-Za-z0-9-]+)+$`)

// ValidOwnerEmail reports whether email can be handed to the pinned CLI as the
// local Owner identity for init.
func ValidOwnerEmail(email string) bool {
	return len(email) <= 254 && !strings.HasPrefix(email, "-") && ownerEmailPattern.MatchString(email)
}

// ValidateInitCandidate bounds the transport and binds its identity. The pinned
// StackKits CLI alone validates complete intent against its CUE authority.
func ValidateInitCandidate(raw []byte, kit, name string) error {
	kit, name = strings.TrimSpace(kit), strings.TrimSpace(name)
	if len(raw) == 0 || len(raw) > MaxInitCandidateBytes {
		return fmt.Errorf("StackKit init requires complete approved intent within %d bytes", MaxInitCandidateBytes)
	}
	var identity struct {
		Metadata struct {
			Name string `json:"name"`
		} `json:"metadata"`
		Kit struct {
			Slug string `json:"slug"`
		} `json:"kit"`
	}
	if err := json.Unmarshal(raw, &identity); err != nil {
		return fmt.Errorf("decode StackKit init candidate: %w", err)
	}
	if identity.Metadata.Name != strings.TrimSpace(name) || identity.Kit.Slug != strings.TrimSpace(kit) || name == "" || kit == "" {
		return fmt.Errorf("StackKit init candidate does not match the approved stack name and kit")
	}
	return nil
}

func validateAddressBindCommand(command *agentpb.StackKitCommand) error {
	if !addressPrefixPattern.MatchString(strings.TrimSpace(command.AddressPrefix)) || strings.TrimSpace(command.BoundSpecPath) == "" {
		return fmt.Errorf("StackKit address bind requires a DNS-safe prefix and bound StackSpec path")
	}
	return nil
}

func validateApplyCommand(command *agentpb.StackKitCommand) error {
	if err := validateLocalExecutionPlacement(command, "apply"); err != nil {
		return err
	}
	if !specHashPattern.MatchString(strings.TrimSpace(command.ExpectedPlanHash)) {
		return fmt.Errorf("StackKit apply expected_plan_hash is required and must be a lowercase sha256:<64-hex> digest")
	}
	return nil
}

func validateUpgradeCommand(command *agentpb.StackKitCommand) error {
	if !command.DryRun && !command.OwnerApproved {
		return fmt.Errorf("StackKit upgrade requires Owner approval")
	}
	target := strings.TrimSpace(command.TargetRelease)
	if target != "" && !targetPattern.MatchString(target) {
		return fmt.Errorf("StackKit upgrade target_release is invalid")
	}
	return nil
}

func validateDriftReconcileCommand(command *agentpb.StackKitCommand) error {
	if !command.OwnerApproved {
		return fmt.Errorf("StackKit drift reconcile requires Owner approval")
	}
	if command.DriftMode != agentpb.StackKitDriftMode_STACKKIT_DRIFT_MODE_STANDARD &&
		command.DriftMode != agentpb.StackKitDriftMode_STACKKIT_DRIFT_MODE_ADVANCED {
		return fmt.Errorf("StackKit drift reconcile requires an explicit drift_mode")
	}
	return nil
}

func validateServiceMutationCommand(command *agentpb.StackKitCommand) error {
	if !command.OwnerApproved {
		return fmt.Errorf("StackKit service mutation requires Owner approval")
	}
	if err := validateServiceKey(command); err != nil {
		return err
	}
	return validateServiceInstance(command)
}

func validateServiceLogsCommand(command *agentpb.StackKitCommand) error {
	if err := validateServiceKey(command); err != nil {
		return err
	}
	if command.LogTail < 1 || command.LogTail > 200 {
		return fmt.Errorf("StackKit service log_tail must be between 1 and 200")
	}
	return validateServiceInstance(command)
}

func validateServiceKey(command *agentpb.StackKitCommand) error {
	if !serviceKeyPattern.MatchString(strings.TrimSpace(command.ServiceKey)) {
		return fmt.Errorf("StackKit service_key is invalid")
	}
	return nil
}

func validateServiceInstance(command *agentpb.StackKitCommand) error {
	if strings.TrimSpace(command.StackkitInstanceId) == "" {
		return fmt.Errorf("StackKit service operation requires stackkit_instance_id")
	}
	return nil
}

func validateRemoveCommand(command *agentpb.StackKitCommand) error {
	workloadRef := strings.TrimSpace(command.WorkloadRef)
	if !command.OwnerApproved || workloadRef == "" || workloadRef != strings.ToLower(workloadRef) ||
		strings.TrimSpace(command.StackkitInstanceId) == "" {
		return fmt.Errorf("StackKit remove requires Owner approval, stackkit_instance_id, and one canonical workload_ref")
	}
	return validateLocalExecutionPlacement(command, "remove")
}

func validateLocalExecutionPlacement(command *agentpb.StackKitCommand, operation string) error {
	if strings.TrimSpace(command.LocalSiteRef) == "" || strings.TrimSpace(command.LocalNodeRef) == "" ||
		strings.TrimSpace(command.LocalExecutionChannelRef) == "" {
		return fmt.Errorf("StackKit %s requires local Site, node, and execution-channel references", operation)
	}
	return nil
}

func RequiresWorkspaceInstanceBinding(command *agentpb.StackKitCommand) bool {
	if command == nil {
		return false
	}
	switch command.Operation {
	case agentpb.StackKitOperation_STACKKIT_OPERATION_SERVICE_START,
		agentpb.StackKitOperation_STACKKIT_OPERATION_SERVICE_STOP,
		agentpb.StackKitOperation_STACKKIT_OPERATION_SERVICE_RESTART,
		agentpb.StackKitOperation_STACKKIT_OPERATION_SERVICE_LOGS,
		agentpb.StackKitOperation_STACKKIT_OPERATION_REMOVE,
		agentpb.StackKitOperation_STACKKIT_OPERATION_BACKUP_RESTORE:
		return true
	case agentpb.StackKitOperation_STACKKIT_OPERATION_BACKUP_RUN,
		agentpb.StackKitOperation_STACKKIT_OPERATION_BACKUP_STATUS,
		agentpb.StackKitOperation_STACKKIT_OPERATION_BACKUP_CONFIGURE:
		return strings.TrimSpace(command.StackkitInstanceId) != ""
	default:
		return false
	}
}

func RequiredAgentCapabilities(command *agentpb.StackKitCommand) []string {
	required := make([]string, 0, 2)
	if command != nil && command.Operation == agentpb.StackKitOperation_STACKKIT_OPERATION_APPLY {
		required = append(required, ExpectedPlanHashCapability)
	}
	if RequiresWorkspaceInstanceBinding(command) {
		required = append(required, WorkspaceInstanceCapability)
	}
	return required
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
	if err := validateCommandResultEnvelope(result, command); err != nil {
		return err
	}
	if err := validateSensitiveIdentityResult(result, command); err != nil {
		return err
	}
	if result.Success {
		switch command.Operation {
		case agentpb.StackKitOperation_STACKKIT_OPERATION_BACKUP_RUN:
			if _, err := ParseBackupRunEvidence(command, result); err != nil {
				return err
			}
		case agentpb.StackKitOperation_STACKKIT_OPERATION_BACKUP_RESTORE:
			if _, err := ParseBackupRestoreEvidence(command, result); err != nil {
				return err
			}
		case agentpb.StackKitOperation_STACKKIT_OPERATION_BACKUP_CONFIGURE:
			if err := ParseBackupConfigurationEvidence(command, result); err != nil {
				return err
			}
		}
	}
	if result.EventsSchemaVersion != RolloutEventVersion {
		return fmt.Errorf("StackKit result has unsupported rollout-event schema")
	}
	return validateRolloutEvents(result.EventsJsonl)
}

func validateSensitiveIdentityResult(result *agentpb.StackKitResult, command *agentpb.StackKitCommand) error {
	wantsSensitive := command.OwnerApproved && result.Success && (command.Operation == agentpb.StackKitOperation_STACKKIT_OPERATION_OWNER_ACTIVATION_ISSUE ||
		command.Operation == agentpb.StackKitOperation_STACKKIT_OPERATION_HOUSEHOLD_INVITE)
	if !wantsSensitive {
		if len(result.SensitiveResultJson) != 0 {
			return fmt.Errorf("StackKit sensitive result is not allowed for this operation")
		}
		return nil
	}
	if len(result.SensitiveResultJson) == 0 || len(result.SensitiveResultJson) > 64<<10 || !json.Valid(result.SensitiveResultJson) {
		return fmt.Errorf("StackKit sensitive identity result is invalid")
	}
	var envelope struct {
		SchemaVersion string `json:"schemaVersion"`
		Command       string `json:"command"`
		Status        string `json:"status"`
	}
	if json.Unmarshal(result.SensitiveResultJson, &envelope) != nil || envelope.SchemaVersion != CommandResultVersion ||
		envelope.Command != ResultCommandName(command.Operation) || envelope.Status != "success" {
		return fmt.Errorf("StackKit sensitive identity result is not bound to the pending command")
	}
	return nil
}

func validateCommandResultEnvelope(result *agentpb.StackKitResult, command *agentpb.StackKitCommand) error {
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
	return nil
}

func validateRolloutEvents(events [][]byte) error {
	if len(events) > maxEvents {
		return fmt.Errorf("StackKit result has too many rollout events")
	}
	total := 0
	for _, event := range events {
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
	case agentpb.StackKitOperation_STACKKIT_OPERATION_SERVICE_START,
		agentpb.StackKitOperation_STACKKIT_OPERATION_SERVICE_STOP,
		agentpb.StackKitOperation_STACKKIT_OPERATION_SERVICE_RESTART,
		agentpb.StackKitOperation_STACKKIT_OPERATION_SERVICE_LOGS:
		return serviceResultCommandName(operation)
	case agentpb.StackKitOperation_STACKKIT_OPERATION_REMOVE:
		return "stackkit remove"
	case agentpb.StackKitOperation_STACKKIT_OPERATION_ADDRESS_BIND:
		return "stackkit address bind"
	case agentpb.StackKitOperation_STACKKIT_OPERATION_BACKUP_RUN:
		return "stackkit backup run"
	case agentpb.StackKitOperation_STACKKIT_OPERATION_BACKUP_STATUS:
		return "stackkit backup status"
	case agentpb.StackKitOperation_STACKKIT_OPERATION_BACKUP_CONFIGURE:
		return "stackkit backup configure"
	case agentpb.StackKitOperation_STACKKIT_OPERATION_BACKUP_RESTORE:
		return "stackkit backup restore"
	case agentpb.StackKitOperation_STACKKIT_OPERATION_OWNER_ACTIVATION_STATUS:
		return "stackkit user owner status"
	case agentpb.StackKitOperation_STACKKIT_OPERATION_OWNER_ACTIVATION_ISSUE:
		return "stackkit user owner activate"
	case agentpb.StackKitOperation_STACKKIT_OPERATION_HOUSEHOLD_LIST:
		return "stackkit user list"
	case agentpb.StackKitOperation_STACKKIT_OPERATION_HOUSEHOLD_INVITE:
		return "stackkit user add"
	default:
		return ""
	}
}

// BackupRunEvidence is the minimum native-v2 snapshot identity the control
// plane admits after the Agent attests the pinned CLI's locally verified bytes.
type BackupRunEvidence struct {
	SnapshotAnchorID string
	OperationID      string
	OwnerRef         string
	PlanHash         string
}

// BackupRestoreEvidence is the staged-restore identity admitted for managed
// rollout promotion after the authenticated Agent binds the locally verified
// pinned-CLI evidence. Activation is intentionally absent: the drill proves
// recoverability in isolated staging without replacing live volumes.
type BackupRestoreEvidence struct {
	RestoreResultID  string
	SnapshotAnchorID string
	OperationID      string
	OwnerRef         string
	PlanHash         string
	VerifiedAt       time.Time
}

type backupAuthorityLineage struct {
	Binding struct {
		PlanHash string `json:"planHash"`
	} `json:"binding"`
}

// requirePinnedCLIEvidenceAttestation verifies only the authority Techstack
// actually owns: an authenticated Agent attestation bound to the exact result
// bytes. StackKits verifies the local Owner signature before its pinned CLI
// returns; the Owner public key never leaves that local custody boundary.
func requirePinnedCLIEvidenceAttestation(result *agentpb.StackKitResult) error {
	if result == nil || !result.PinnedCliEvidenceVerified {
		return fmt.Errorf("StackKits result lacks pinned-CLI local evidence attestation")
	}
	digest := sha256.Sum256(result.CommandResultJson)
	want := "sha256:" + hex.EncodeToString(digest[:])
	if strings.TrimSpace(result.PinnedCliEvidenceSha256) != want {
		return fmt.Errorf("StackKits pinned-CLI evidence attestation does not bind the exact result")
	}
	return nil
}

// ParseBackupConfigurationEvidence accepts only a successful owner-bound
// repository configuration for the dispatched configure command.
func ParseBackupConfigurationEvidence(command *agentpb.StackKitCommand, result *agentpb.StackKitResult) error {
	if command == nil || result == nil || !result.Success || command.Operation != agentpb.StackKitOperation_STACKKIT_OPERATION_BACKUP_CONFIGURE {
		return fmt.Errorf("successful StackKit backup configuration evidence is required")
	}
	var envelope struct {
		SchemaVersion string `json:"schemaVersion"`
		Command       string `json:"command"`
		Status        string `json:"status"`
		Data          struct {
			APIVersion string `json:"apiVersion"`
			OwnerRef   string `json:"ownerRef"`
		} `json:"data"`
	}
	if err := json.Unmarshal(result.CommandResultJson, &envelope); err != nil {
		return fmt.Errorf("decode StackKit backup configuration evidence: %w", err)
	}
	if envelope.SchemaVersion != CommandResultVersion || envelope.Command != ResultCommandName(command.Operation) || envelope.Status != "success" ||
		envelope.Data.APIVersion != "stackkit.local-backup-configuration/v1" || strings.TrimSpace(envelope.Data.OwnerRef) == "" {
		return fmt.Errorf("StackKit backup configuration evidence identity is invalid")
	}
	return nil
}

// ParseBackupRunEvidence rejects a successful-looking snapshot result unless
// its operation and current plan match the dispatched typed command.
func ParseBackupRunEvidence(command *agentpb.StackKitCommand, result *agentpb.StackKitResult) (BackupRunEvidence, error) {
	if command == nil || result == nil || !result.Success || command.Operation != agentpb.StackKitOperation_STACKKIT_OPERATION_BACKUP_RUN {
		return BackupRunEvidence{}, fmt.Errorf("successful StackKit backup run evidence is required")
	}
	if err := requirePinnedCLIEvidenceAttestation(result); err != nil {
		return BackupRunEvidence{}, err
	}
	var envelope struct {
		SchemaVersion string `json:"schemaVersion"`
		Command       string `json:"command"`
		Status        string `json:"status"`
		Data          struct {
			APIVersion  string                 `json:"apiVersion"`
			ID          string                 `json:"id"`
			OwnerRef    string                 `json:"ownerRef"`
			OperationID string                 `json:"operationId"`
			Lineage     backupAuthorityLineage `json:"lineage"`
		} `json:"data"`
	}
	if err := json.Unmarshal(result.CommandResultJson, &envelope); err != nil {
		return BackupRunEvidence{}, fmt.Errorf("decode StackKit backup run evidence: %w", err)
	}
	evidence := BackupRunEvidence{
		SnapshotAnchorID: strings.TrimSpace(envelope.Data.ID),
		OperationID:      strings.TrimSpace(envelope.Data.OperationID),
		OwnerRef:         strings.TrimSpace(envelope.Data.OwnerRef),
		PlanHash:         strings.TrimSpace(envelope.Data.Lineage.Binding.PlanHash),
	}
	if envelope.SchemaVersion != CommandResultVersion || envelope.Command != ResultCommandName(command.Operation) || envelope.Status != "success" ||
		envelope.Data.APIVersion != "stackkit.local-backup-snapshot-anchor/v1" ||
		!specHashPattern.MatchString(evidence.SnapshotAnchorID) || evidence.OwnerRef == "" ||
		evidence.OperationID != strings.TrimSpace(command.CommandId) {
		return BackupRunEvidence{}, fmt.Errorf("StackKit backup run evidence identity is invalid")
	}
	if expected := strings.TrimSpace(command.ExpectedPlanHash); expected != "" && evidence.PlanHash != expected {
		return BackupRunEvidence{}, fmt.Errorf("StackKit backup run evidence does not match expected plan")
	}
	return evidence, nil
}

// ParseBackupRestoreEvidence rejects cross-snapshot, cross-operation, or stale
// plan evidence before it can promote a managed rollout to verified.
func ParseBackupRestoreEvidence(command *agentpb.StackKitCommand, result *agentpb.StackKitResult) (BackupRestoreEvidence, error) {
	if command == nil || result == nil || !result.Success || command.Operation != agentpb.StackKitOperation_STACKKIT_OPERATION_BACKUP_RESTORE {
		return BackupRestoreEvidence{}, fmt.Errorf("successful StackKit backup restore evidence is required")
	}
	if err := requirePinnedCLIEvidenceAttestation(result); err != nil {
		return BackupRestoreEvidence{}, err
	}
	var envelope struct {
		SchemaVersion string `json:"schemaVersion"`
		Command       string `json:"command"`
		Status        string `json:"status"`
		Data          struct {
			APIVersion           string                 `json:"apiVersion"`
			ID                   string                 `json:"id"`
			OwnerRef             string                 `json:"ownerRef"`
			SnapshotAnchorID     string                 `json:"snapshotAnchorId"`
			OperationID          string                 `json:"operationId"`
			AuthorizationLineage backupAuthorityLineage `json:"authorizationLineage"`
			SnapshotLineage      backupAuthorityLineage `json:"snapshotLineage"`
			RecoveryAnchor       struct {
				SnapshotAnchorID string `json:"snapshotAnchorId"`
				OperationID      string `json:"operationId"`
			} `json:"recoveryAnchor"`
			Request struct {
				SnapshotAnchorID string `json:"snapshotAnchorId"`
				OperationID      string `json:"operationId"`
			} `json:"request"`
			Receipt struct {
				OperationID               string `json:"operationId"`
				RepositoryContentVerified bool   `json:"repositoryContentVerified"`
			} `json:"receipt"`
			Verification struct {
				PlanHash         string    `json:"planHash"`
				ServicesVerified bool      `json:"servicesVerified"`
				VerifiedAt       time.Time `json:"verifiedAt"`
			} `json:"verification"`
		} `json:"data"`
	}
	if err := json.Unmarshal(result.CommandResultJson, &envelope); err != nil {
		return BackupRestoreEvidence{}, fmt.Errorf("decode StackKit backup restore evidence: %w", err)
	}
	evidence := BackupRestoreEvidence{
		RestoreResultID:  strings.TrimSpace(envelope.Data.ID),
		SnapshotAnchorID: strings.TrimSpace(envelope.Data.SnapshotAnchorID),
		OperationID:      strings.TrimSpace(envelope.Data.OperationID),
		OwnerRef:         strings.TrimSpace(envelope.Data.OwnerRef),
		PlanHash:         strings.TrimSpace(envelope.Data.AuthorizationLineage.Binding.PlanHash),
		VerifiedAt:       envelope.Data.Verification.VerifiedAt,
	}
	expectedSnapshot := strings.TrimSpace(command.SnapshotAnchorId)
	expectedOperation := strings.TrimSpace(command.CommandId)
	expectedPlan := strings.TrimSpace(command.ExpectedPlanHash)
	if envelope.SchemaVersion != CommandResultVersion || envelope.Command != ResultCommandName(command.Operation) || envelope.Status != "success" ||
		envelope.Data.APIVersion != "stackkit.local-backup-restore-result/v1" ||
		!specHashPattern.MatchString(evidence.RestoreResultID) || evidence.OwnerRef == "" ||
		evidence.SnapshotAnchorID != expectedSnapshot || evidence.OperationID != expectedOperation ||
		evidence.PlanHash != expectedPlan || envelope.Data.Verification.PlanHash != expectedPlan ||
		envelope.Data.SnapshotLineage.Binding.PlanHash != expectedPlan ||
		envelope.Data.RecoveryAnchor.SnapshotAnchorID != expectedSnapshot || envelope.Data.RecoveryAnchor.OperationID != expectedOperation ||
		envelope.Data.Request.SnapshotAnchorID != expectedSnapshot || envelope.Data.Request.OperationID != expectedOperation ||
		envelope.Data.Receipt.OperationID != expectedOperation || !envelope.Data.Receipt.RepositoryContentVerified ||
		!envelope.Data.Verification.ServicesVerified || evidence.VerifiedAt.IsZero() {
		return BackupRestoreEvidence{}, fmt.Errorf("StackKit backup restore evidence does not match dispatched plan, snapshot, and operation authority")
	}
	return evidence, nil
}

func serviceResultCommandName(operation agentpb.StackKitOperation) string {
	switch operation {
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
