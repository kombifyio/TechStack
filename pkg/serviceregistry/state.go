package serviceregistry

import (
	"fmt"
	"strings"
)

// DesiredState is the user-owned target for one managed service. It is
// independent of what the agent currently observes and of health evidence.
type DesiredState string

// ObservedState is the measured runtime state of the service process. It maps
// onto the StackKits runtime-observation service status vocabulary.
type ObservedState string

// HealthState is measured health evidence. It is deliberately independent of
// ObservedState: a running service can be unhealthy, and a stopped service has
// no health evidence at all.
type HealthState string

// ManagementState answers who owns the service definition. It is a separate
// axis from every measured dimension and from the inventory Source: Source says
// which pipeline reported the row, ManagementState says whether a declared
// target configuration exists for it at all.
//
//	managed  - rolled out by us through StackKits/PaaS. The declared target
//	           comes from the kit contract, so desired state and drift
//	           comparison are defined.
//	observed - discovered on a server we watch. There is no declared target, so
//	           desired state is not applicable and drift is undefined.
type ManagementState string

const (
	DesiredRunning DesiredState = "running"
	DesiredStopped DesiredState = "stopped"

	ObservedRunning  ObservedState = "running"
	ObservedStopped  ObservedState = "stopped"
	ObservedStarting ObservedState = "starting"
	ObservedError    ObservedState = "error"
	ObservedUnknown  ObservedState = "unknown"

	HealthHealthy     HealthState = "healthy"
	HealthUnhealthy   HealthState = "unhealthy"
	HealthStarting    HealthState = "starting"
	HealthNotRequired HealthState = "not-required"
	HealthUnknown     HealthState = "unknown"

	ManagementManaged  ManagementState = "managed"
	ManagementObserved ManagementState = "observed"
)

// Inventory source vocabulary. `source` is the provenance axis: which pipeline
// last reported this row. It is deliberately NOT the management axis, and it is
// also not the StackKits evidence-provenance vocabulary
// (local-runtime|standard-process|verified-apply-evidence), which describes how
// strong an apply evidence record is and lives on the StackKits side.
const (
	// SourceObserved is a service discovered without a declared contract, and
	// the fail-closed default for a row whose provenance is unknown.
	SourceObserved = "observed"
	// SourceStackKitsInventory is an authenticated Guard runtime observation of
	// a StackKits-deployed service.
	SourceStackKitsInventory = "stackkits-inventory"
	// SourceStackKitOutputs is the StackKits apply/job output projection.
	SourceStackKitOutputs = "stackkit_outputs"
	// SourceLegacyRegistryBackfill is the bounded read-through projection of
	// pre-aggregate registry rows.
	SourceLegacyRegistryBackfill = "legacy-registry-backfill"
)

// LegacyCustomServiceType is the legacy PocketBase marker for a service a user
// imported by hand. It never had a declared contract.
const LegacyCustomServiceType = "custom"

// ReasonDesiredStateNotApplicable is returned when a caller tries to declare a
// target state for a service that has no declared contract.
const ReasonDesiredStateNotApplicable = "desired_state_not_applicable_for_observed_service"

// ServiceSources is the closed `services.source` vocabulary, mirrored by the
// services_source_check database constraint.
var ServiceSources = []string{
	SourceObserved, SourceStackKitsInventory, SourceStackKitOutputs, SourceLegacyRegistryBackfill,
}

var managementStates = map[ManagementState]bool{
	ManagementManaged: true, ManagementObserved: true,
}

var serviceSources = map[string]bool{
	SourceObserved: true, SourceStackKitsInventory: true,
	SourceStackKitOutputs: true, SourceLegacyRegistryBackfill: true,
}

// ControlPlaneWorkflowStates are the legacy services.status values that
// describe a control-plane workflow rather than a measured observation. They
// are not part of the observed vocabulary and always win over the derived
// status projection.
var ControlPlaneWorkflowStates = []string{"migrating", "deploying", "pending_verification", WorkflowArchived}

// WorkflowArchived is the terminal control-plane workflow state.
const WorkflowArchived = "archived"

var desiredStates = map[DesiredState]bool{
	DesiredRunning: true, DesiredStopped: true,
}

var observedStates = map[ObservedState]bool{
	ObservedRunning: true, ObservedStopped: true, ObservedStarting: true,
	ObservedError: true, ObservedUnknown: true,
}

var healthStates = map[HealthState]bool{
	HealthHealthy: true, HealthUnhealthy: true, HealthStarting: true,
	HealthNotRequired: true, HealthUnknown: true,
}

// ValidateDesiredState rejects values outside the user-owned target vocabulary.
func ValidateDesiredState(value string) error {
	state := DesiredState(strings.TrimSpace(value))
	if !desiredStates[state] {
		return fmt.Errorf("unknown service desired state %q", state)
	}
	return nil
}

// ValidateObservedState rejects values outside the measured runtime vocabulary.
func ValidateObservedState(value string) error {
	state := ObservedState(strings.TrimSpace(value))
	if !observedStates[state] {
		return fmt.Errorf("unknown service observed state %q", state)
	}
	return nil
}

// ValidateHealthState rejects values outside the measured health vocabulary.
func ValidateHealthState(value string) error {
	state := HealthState(strings.TrimSpace(value))
	if !healthStates[state] {
		return fmt.Errorf("unknown service health state %q", state)
	}
	return nil
}

// ValidateManagementState rejects values outside the ownership vocabulary.
func ValidateManagementState(value string) error {
	state := ManagementState(strings.TrimSpace(value))
	if !managementStates[state] {
		return fmt.Errorf("unknown service management state %q", state)
	}
	return nil
}

// ValidateSource rejects values outside the closed inventory-source vocabulary.
func ValidateSource(value string) error {
	if !serviceSources[normalize(value)] {
		return fmt.Errorf("unknown service source %q", strings.TrimSpace(value))
	}
	return nil
}

// CanonicalSource projects a reported provenance onto the closed vocabulary. An
// unrecognized or absent provenance is `observed`: it proves nothing about a
// declared contract, so it must not claim one.
func CanonicalSource(value string) string {
	if normalized := normalize(value); serviceSources[normalized] {
		return normalized
	}
	return SourceObserved
}

// ManagementStateForSource is THE rule that decides ownership for a service
// aggregate row. It is evaluated once at the write boundary and persisted;
// no reader recomputes it.
//
// Only the `observed` provenance means "discovered, no declared target". Every
// other source in the vocabulary is produced by a StackKits/PaaS rollout
// pipeline of ours, so the kit contract supplies the target.
func ManagementStateForSource(source string) ManagementState {
	if normalize(source) == string(ManagementObserved) {
		return ManagementObserved
	}
	return ManagementManaged
}

// ManagementStateForLegacyRecord is the same rule extended with the two legacy
// markers that predate the `source` column: a legacy `status` of `observed` and
// the hand-imported `custom` service type. It exists for exactly two callers -
// the PocketBase compatibility bridge, which has no source column at all, and
// the 074 backfill, which must reproduce the historical read-time derivation so
// no stored row changes meaning. Aggregate writes use ManagementStateForSource.
func ManagementStateForLegacyRecord(source, status, serviceType string) ManagementState {
	if ManagementStateForSource(source) == ManagementObserved {
		return ManagementObserved
	}
	if normalize(status) == string(ManagementObserved) || normalize(serviceType) == LegacyCustomServiceType {
		return ManagementObserved
	}
	return ManagementManaged
}

// CanonicalManagementState projects a reported ownership claim onto the
// vocabulary. Anything unreadable falls back to `observed`: claiming a contract
// we cannot prove would let drift comparison run against fiction.
func CanonicalManagementState(value string) ManagementState {
	state := ManagementState(normalize(value))
	if managementStates[state] {
		return state
	}
	return ManagementObserved
}

// DesiredStateApplicable reports whether a declared target state has any
// meaning for a service. Only a managed service has a kit contract to compare
// against, so drift comparison is defined for managed services only.
func DesiredStateApplicable(state ManagementState) bool {
	return CanonicalManagementState(string(state)) == ManagementManaged
}

// CanonicalDesiredState projects agent-reported and legacy intent onto the
// canonical target vocabulary. An unreadable or absent intent stays running,
// which is the column default the registry has always used.
func CanonicalDesiredState(value string) DesiredState {
	switch normalize(value) {
	case "stopped", "stop", "stopping", "exited", "dead", "down", "absent", WorkflowArchived:
		return DesiredStopped
	default:
		return DesiredRunning
	}
}

// CanonicalObservedState projects agent-reported runtime status onto the
// StackKits service status vocabulary. Anything that is not measured evidence
// becomes unknown rather than an optimistic green value.
func CanonicalObservedState(value string) ObservedState {
	switch normalize(value) {
	case "running", "ready", "up", "healthy", "reachable", "active", "observed":
		return ObservedRunning
	case "stopped", "stop", "exited", "dead", "down", WorkflowArchived:
		return ObservedStopped
	case "starting", "created", "restarting", "pending", "deploying", "initializing",
		"migrating", "pending_verification":
		return ObservedStarting
	case "failed", "error", "unhealthy", "crashed", "degraded":
		return ObservedError
	default:
		return ObservedUnknown
	}
}

// CanonicalHealthState projects measured health evidence onto the StackKits
// health vocabulary.
//
// runtimehealth reports `reachable` when a service answers without a health
// probe having passed, and it already returns `starting` for the equivalent
// "running but unproven" case. The canonical projection keeps that meaning
// instead of inventing a value or claiming healthy.
func CanonicalHealthState(value string) HealthState {
	switch normalize(value) {
	case "healthy", "ok", "pass", "passing", "up":
		return HealthHealthy
	case "unhealthy", "error", "failed", "critical", "down":
		return HealthUnhealthy
	case "starting", "reachable", "created", "restarting", "pending", "initializing", "deploying":
		return HealthStarting
	case "not-required", "not_required", "none", "disabled", "n/a":
		return HealthNotRequired
	default:
		return HealthUnknown
	}
}

// DeriveStatus returns the legacy services.status projection for one observed
// state. Status describes what the runtime is doing; it is never the health
// value. Callers must let a retained control-plane workflow state win.
func DeriveStatus(observed ObservedState) string {
	if !observedStates[observed] {
		return string(ObservedUnknown)
	}
	return string(observed)
}

// RetainedWorkflowState returns the control-plane workflow state held by a
// legacy status or migration status value, or an empty string when none is
// held. A retained workflow state always wins over the derived status
// projection and over measured access evidence. `archived` is terminal and
// therefore wins over any other retained value.
func RetainedWorkflowState(values ...string) string {
	held := ""
	for _, value := range values {
		normalized := normalize(value)
		for _, workflow := range ControlPlaneWorkflowStates {
			if normalized != workflow {
				continue
			}
			if normalized == WorkflowArchived {
				return normalized
			}
			if held == "" {
				held = normalized
			}
		}
	}
	return held
}

// IsControlPlaneWorkflowState reports whether any value describes a
// control-plane workflow state.
func IsControlPlaneWorkflowState(values ...string) bool {
	return RetainedWorkflowState(values...) != ""
}

func normalize(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}
