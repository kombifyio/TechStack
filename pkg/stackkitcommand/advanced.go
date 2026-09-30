package stackkitcommand

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/kombifyio/techstack/pkg/api/agentpb"
)

const (
	// AdvancedOperationsCapability marks an agent that renders the Advanced
	// operations from the pinned release's stackkit.advanced-operations/v1
	// catalog and hands the capability over as a 0600 file.
	AdvancedOperationsCapability = "stackkit.advanced-operations.v1"
	// MaxAdvancedCapabilityBytes bounds the capability transport; it equals
	// the StackKits capability file limit.
	MaxAdvancedCapabilityBytes = 64 << 10

	OperationDenialVersion      = "stackkit.operation-denial/v1"
	advancedMutationVersion     = "stackkit.advanced-mutation/v1"
	advancedChangeSetVersion    = "stackkit.advanced-change-set/v2"
	changeSetResultVersion      = "stackkit.change-set-result/v1"
	driftReportVersion          = "stackkit.drift-report/v1"
	restoreDrillReportVersion   = "stackkit.restore-drill-report/v1"
	rollbackResultVersion       = "stackkit.rollback-result/v1"
	commandResultStatusDenied   = "denied"
	restoreDrillStatusSucceeded = "succeeded"
	rollbackStatusConverged     = "converged"
	driftStatusClean            = "clean"
)

// AdvancedCatalogOperation maps a typed Advanced operation to the
// stackkit.advanced-operations/v1 catalog operation (and capability
// operation) it dispatches. It is empty for every other operation.
func AdvancedCatalogOperation(operation agentpb.StackKitOperation) string {
	switch operation {
	case agentpb.StackKitOperation_STACKKIT_OPERATION_ADVANCED_CHANGE_SET_CREATE:
		return "terramate.change-set.create"
	case agentpb.StackKitOperation_STACKKIT_OPERATION_ADVANCED_CHANGE_SET_APPLY:
		return "terramate.change-set.apply"
	case agentpb.StackKitOperation_STACKKIT_OPERATION_ADVANCED_DRIFT_RECONCILE:
		return "drift.reconcile.advanced"
	case agentpb.StackKitOperation_STACKKIT_OPERATION_ADVANCED_ROLLBACK:
		return "rollback.coordinated"
	case agentpb.StackKitOperation_STACKKIT_OPERATION_ADVANCED_RESTORE_DRILL:
		return "restore.drill"
	default:
		return ""
	}
}

// IsAdvancedOperation reports the capability-gated Advanced operations.
func IsAdvancedOperation(operation agentpb.StackKitOperation) bool {
	return AdvancedCatalogOperation(operation) != ""
}

func acceptsCandidateSpec(operation agentpb.StackKitOperation) bool {
	switch operation {
	case agentpb.StackKitOperation_STACKKIT_OPERATION_INIT,
		agentpb.StackKitOperation_STACKKIT_OPERATION_ADVANCED_CHANGE_SET_CREATE,
		agentpb.StackKitOperation_STACKKIT_OPERATION_ADVANCED_CHANGE_SET_APPLY,
		agentpb.StackKitOperation_STACKKIT_OPERATION_ADVANCED_DRIFT_RECONCILE:
		return true
	default:
		return false
	}
}

func appliesChangeSet(operation agentpb.StackKitOperation) bool {
	return operation == agentpb.StackKitOperation_STACKKIT_OPERATION_ADVANCED_CHANGE_SET_APPLY ||
		operation == agentpb.StackKitOperation_STACKKIT_OPERATION_ADVANCED_DRIFT_RECONCILE
}

// validateAdvancedFields bounds the Advanced-only fields on every command:
// a capability exactly on the Advanced operations, change-set and rollback
// references exactly where the catalog argv takes them.
func validateAdvancedFields(command *agentpb.StackKitCommand) error {
	for _, check := range []func(*agentpb.StackKitCommand) error{
		validateAdvancedCapability, validateAdvancedCandidate, validateChangeSetReferences,
		validateRollbackReference, validateAdvancedApproval,
	} {
		if err := check(command); err != nil {
			return err
		}
	}
	if command.DriftMode == agentpb.StackKitDriftMode_STACKKIT_DRIFT_MODE_STANDARD {
		return fmt.Errorf("StackKit Standard drift mode is never dispatched for a Techstack-managed deployment")
	}
	return nil
}

func validateAdvancedCapability(command *agentpb.StackKitCommand) error {
	advanced := IsAdvancedOperation(command.Operation)
	if !advanced && len(command.AdvancedCapability) != 0 {
		return fmt.Errorf("StackKit advanced_capability is only valid for the Advanced operations")
	}
	if advanced && (len(command.AdvancedCapability) == 0 || len(command.AdvancedCapability) > MaxAdvancedCapabilityBytes) {
		return fmt.Errorf("StackKit %s requires one capability within %d bytes", command.Operation, MaxAdvancedCapabilityBytes)
	}
	return nil
}

func validateAdvancedCandidate(command *agentpb.StackKitCommand) error {
	if len(command.CandidateSpecJson) == 0 {
		return nil
	}
	if !acceptsCandidateSpec(command.Operation) {
		return fmt.Errorf("StackKit candidate intent is only valid for init and the Advanced change-set operations")
	}
	if IsAdvancedOperation(command.Operation) && (len(command.CandidateSpecJson) > MaxInitCandidateBytes || !json.Valid(command.CandidateSpecJson)) {
		return fmt.Errorf("StackKit Advanced candidate must be one JSON StackSpec within %d bytes", MaxInitCandidateBytes)
	}
	return nil
}

func validateChangeSetReferences(command *agentpb.StackKitCommand) error {
	changeSetID, changeSetSHA := strings.TrimSpace(command.ChangeSetId), strings.TrimSpace(command.ChangeSetSha256)
	if !appliesChangeSet(command.Operation) {
		if changeSetID != "" || changeSetSHA != "" {
			return fmt.Errorf("StackKit change-set references are only valid for change-set apply and Advanced drift reconcile")
		}
		return nil
	}
	if !specHashPattern.MatchString(changeSetID) || !specHashPattern.MatchString(changeSetSHA) {
		return fmt.Errorf("StackKit %s requires change_set_id and change_set_sha256 as sha256 digests", command.Operation)
	}
	return nil
}

func validateRollbackReference(command *agentpb.StackKitCommand) error {
	targetRef := strings.TrimSpace(command.RollbackTargetRef)
	if command.Operation != agentpb.StackKitOperation_STACKKIT_OPERATION_ADVANCED_ROLLBACK {
		if targetRef != "" {
			return fmt.Errorf("StackKit rollback_target_ref is only valid for Advanced rollback")
		}
		return nil
	}
	if !specHashPattern.MatchString(targetRef) {
		return fmt.Errorf("StackKit Advanced rollback requires a sha256 rollback_target_ref")
	}
	return nil
}

func validateAdvancedApproval(command *agentpb.StackKitCommand) error {
	switch command.Operation {
	case agentpb.StackKitOperation_STACKKIT_OPERATION_ADVANCED_ROLLBACK:
		if !command.OwnerApproved {
			return fmt.Errorf("StackKit Advanced rollback requires Owner approval")
		}
	case agentpb.StackKitOperation_STACKKIT_OPERATION_ADVANCED_RESTORE_DRILL:
		if !command.OwnerApproved {
			return fmt.Errorf("StackKit Advanced restore drill requires Owner approval")
		}
		if anchor := strings.TrimSpace(command.SnapshotAnchorId); anchor != "" && !specHashPattern.MatchString(anchor) {
			return fmt.Errorf("StackKit Advanced restore drill snapshot_anchor_id is invalid")
		}
	}
	return nil
}

// OperationDenial is the stackkit.operation-denial/v1 admission denial a
// capability-gated command reports before any side effect.
type OperationDenial struct {
	Operation  string
	Mode       string
	ReasonCode string
	Message    string
}

// ParseOperationDenial returns the denial of a denied Advanced command. A
// denied change-set apply reports its mutation record instead of the
// denial document; it maps to the reason advanced_operation_denied.
func ParseOperationDenial(result *agentpb.StackKitResult) (OperationDenial, bool) {
	if result == nil || len(result.CommandResultJson) == 0 {
		return OperationDenial{}, false
	}
	var envelope struct {
		SchemaVersion string `json:"schemaVersion"`
		Status        string `json:"status"`
		Data          struct {
			SchemaVersion string `json:"schemaVersion"`
			Operation     string `json:"operation"`
			Mode          string `json:"mode"`
			ReasonCode    string `json:"reasonCode"`
			Message       string `json:"message"`
		} `json:"data"`
	}
	if json.Unmarshal(result.CommandResultJson, &envelope) != nil || envelope.SchemaVersion != CommandResultVersion ||
		envelope.Status != commandResultStatusDenied {
		return OperationDenial{}, false
	}
	denial := OperationDenial{
		Operation: strings.TrimSpace(envelope.Data.Operation), Mode: strings.TrimSpace(envelope.Data.Mode),
		ReasonCode: strings.TrimSpace(envelope.Data.ReasonCode), Message: strings.TrimSpace(envelope.Data.Message),
	}
	if envelope.Data.SchemaVersion != OperationDenialVersion || denial.ReasonCode == "" {
		denial.ReasonCode = "advanced_operation_denied"
	}
	return denial, true
}

// AdvancedOperationResult is the admitted evidence of one successful
// Advanced operation.
type AdvancedOperationResult struct {
	// Operation is the catalog operation.
	Operation string
	// ChangeSetID and ChangeSetSHA256 name the change set a create stored.
	ChangeSetID     string
	ChangeSetSHA256 string
	// Status is the operation's own status: the change-set result, the
	// post-reconcile drift report, the drill report or the rollback result.
	Status string
	// Report is the operation's report payload: data.changeSetResult,
	// data.driftReport, the drill report or the rollback result.
	Report json.RawMessage
}

// advancedData is the part of an Advanced command's data Core admits.
type advancedData struct {
	SchemaVersion   string          `json:"schemaVersion"`
	ChangeSetID     string          `json:"changeSetId"`
	Status          string          `json:"status"`
	ChangeSetResult json.RawMessage `json:"changeSetResult"`
	DriftReport     json.RawMessage `json:"driftReport"`
}

// ParseAdvancedOperationResult admits a successful Advanced command only when
// its data is the payload the catalog binds to that operation and it names
// the dispatched change set.
func ParseAdvancedOperationResult(command *agentpb.StackKitCommand, result *agentpb.StackKitResult) (AdvancedOperationResult, error) {
	operation := AdvancedCatalogOperation(command.GetOperation())
	if operation == "" || result == nil || !result.Success {
		return AdvancedOperationResult{}, fmt.Errorf("successful StackKit Advanced operation evidence is required")
	}
	var envelope struct {
		SchemaVersion string          `json:"schemaVersion"`
		Command       string          `json:"command"`
		Status        string          `json:"status"`
		Data          json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(result.CommandResultJson, &envelope); err != nil {
		return AdvancedOperationResult{}, fmt.Errorf("decode StackKit %s result: %w", operation, err)
	}
	if envelope.SchemaVersion != CommandResultVersion || envelope.Command != ResultCommandName(command.Operation) || envelope.Status != commandResultStatusSuccess {
		return AdvancedOperationResult{}, fmt.Errorf("StackKit %s command-result identity is invalid", operation)
	}
	var data advancedData
	if err := json.Unmarshal(envelope.Data, &data); err != nil {
		return AdvancedOperationResult{}, fmt.Errorf("decode StackKit %s data: %w", operation, err)
	}
	switch command.Operation {
	case agentpb.StackKitOperation_STACKKIT_OPERATION_ADVANCED_CHANGE_SET_CREATE:
		return admitChangeSetCreate(operation, data, envelope.Data, result)
	case agentpb.StackKitOperation_STACKKIT_OPERATION_ADVANCED_CHANGE_SET_APPLY,
		agentpb.StackKitOperation_STACKKIT_OPERATION_ADVANCED_DRIFT_RECONCILE:
		return admitAdvancedMutation(operation, command, data)
	case agentpb.StackKitOperation_STACKKIT_OPERATION_ADVANCED_RESTORE_DRILL:
		return admitAdvancedReport(operation, data, envelope.Data, restoreDrillReportVersion, restoreDrillStatusSucceeded)
	default:
		return admitAdvancedReport(operation, data, envelope.Data, rollbackResultVersion, rollbackStatusConverged)
	}
}

func admitChangeSetCreate(operation string, data advancedData, raw json.RawMessage, result *agentpb.StackKitResult) (AdvancedOperationResult, error) {
	admitted := AdvancedOperationResult{
		Operation: operation, ChangeSetID: strings.TrimSpace(data.ChangeSetID),
		ChangeSetSHA256: strings.TrimSpace(result.AdvancedChangeSetSha256), Status: "created", Report: raw,
	}
	if data.SchemaVersion != advancedChangeSetVersion || !specHashPattern.MatchString(admitted.ChangeSetID) ||
		!specHashPattern.MatchString(admitted.ChangeSetSHA256) {
		return AdvancedOperationResult{}, fmt.Errorf("StackKit change-set create evidence lacks the change set id or its stored digest")
	}
	return admitted, nil
}

func admitAdvancedMutation(operation string, command *agentpb.StackKitCommand, data advancedData) (AdvancedOperationResult, error) {
	if data.SchemaVersion != advancedMutationVersion || strings.TrimSpace(data.ChangeSetID) != strings.TrimSpace(command.ChangeSetId) {
		return AdvancedOperationResult{}, fmt.Errorf("StackKit %s evidence does not name the dispatched change set", operation)
	}
	reconcile := command.Operation == agentpb.StackKitOperation_STACKKIT_OPERATION_ADVANCED_DRIFT_RECONCILE
	report, want := data.ChangeSetResult, changeSetResultVersion
	if reconcile {
		report, want = data.DriftReport, driftReportVersion
	}
	status, err := reportStatus(report, want)
	if err != nil {
		return AdvancedOperationResult{}, fmt.Errorf("StackKit %s: %w", operation, err)
	}
	if reconcile && status != driftStatusClean {
		return AdvancedOperationResult{}, fmt.Errorf("StackKit Advanced reconcile left drift status %q", status)
	}
	return AdvancedOperationResult{
		Operation: operation, ChangeSetID: strings.TrimSpace(data.ChangeSetID),
		ChangeSetSHA256: strings.TrimSpace(command.ChangeSetSha256), Status: status, Report: report,
	}, nil
}

func admitAdvancedReport(operation string, data advancedData, raw json.RawMessage, schemaVersion, status string) (AdvancedOperationResult, error) {
	if data.SchemaVersion != schemaVersion || data.Status != status {
		return AdvancedOperationResult{}, fmt.Errorf("StackKit %s result is not a %s %s", operation, status, schemaVersion)
	}
	return AdvancedOperationResult{Operation: operation, Status: data.Status, Report: raw}, nil
}

func reportStatus(raw json.RawMessage, schemaVersion string) (string, error) {
	var report struct {
		SchemaVersion string `json:"schemaVersion"`
		Status        string `json:"status"`
	}
	if len(raw) == 0 || json.Unmarshal(raw, &report) != nil || report.SchemaVersion != schemaVersion {
		return "", fmt.Errorf("result lacks its %s report", schemaVersion)
	}
	return strings.TrimSpace(report.Status), nil
}

// DriftStack is one per-stack entry of a stackkit.drift-report/v1.
type DriftStack struct {
	StackID     string `json:"stackId"`
	Role        string `json:"role"`
	ModuleRef   string `json:"moduleRef"`
	Status      string `json:"status"`
	PlanExit    *int   `json:"planExitCode,omitempty"`
	RuntimeRoot string `json:"runtimeRoot"`
}

// DriftReport is the admitted part of a stackkit.drift-report/v1: the native
// verdict plus, under the terramate target, the combined status and stacks.
type DriftReport struct {
	Mode             string
	GenerationTarget string
	HasDrift         bool
	Status           string
	Stacks           []DriftStack
	Subjects         []DriftSubject
}

// DriftSubject is one native drift subject.
type DriftSubject struct {
	Subject string `json:"subject"`
	Status  string `json:"status"`
	Code    string `json:"code,omitempty"`
}

// ParseDriftReport admits the drift report of a successful DRIFT_DETECT.
func ParseDriftReport(command *agentpb.StackKitCommand, result *agentpb.StackKitResult) (DriftReport, error) {
	if command.GetOperation() != agentpb.StackKitOperation_STACKKIT_OPERATION_DRIFT_DETECT || result == nil || !result.Success {
		return DriftReport{}, fmt.Errorf("successful StackKit drift detect evidence is required")
	}
	var envelope struct {
		SchemaVersion string `json:"schemaVersion"`
		Command       string `json:"command"`
		Status        string `json:"status"`
		Data          struct {
			SchemaVersion    string         `json:"schemaVersion"`
			Mode             string         `json:"mode"`
			GenerationTarget string         `json:"generationTarget"`
			HasDrift         bool           `json:"hasDrift"`
			Status           string         `json:"status"`
			Stacks           []DriftStack   `json:"stacks"`
			Subjects         []DriftSubject `json:"subjects"`
		} `json:"data"`
	}
	if err := json.Unmarshal(result.CommandResultJson, &envelope); err != nil {
		return DriftReport{}, fmt.Errorf("decode StackKit drift report: %w", err)
	}
	if envelope.SchemaVersion != CommandResultVersion || envelope.Command != ResultCommandName(command.Operation) ||
		envelope.Status != commandResultStatusSuccess || envelope.Data.SchemaVersion != driftReportVersion {
		return DriftReport{}, fmt.Errorf("StackKit drift detect did not return a stackkit.drift-report/v1")
	}
	report := DriftReport{
		Mode: envelope.Data.Mode, GenerationTarget: envelope.Data.GenerationTarget, HasDrift: envelope.Data.HasDrift,
		Status: envelope.Data.Status, Stacks: envelope.Data.Stacks, Subjects: envelope.Data.Subjects,
	}
	if report.Status == "" {
		// Without the per-stack section the native verdict is the status.
		report.Status = driftStatusClean
		if report.HasDrift {
			report.Status = "drifted"
		}
	}
	return report, nil
}
