package monthlyruntime

import (
	"strings"
	"time"
)

// State is the explicit managed-runtime node lifecycle state. It is the single
// source of truth (B1) that both the read side (dashboard projection) and the
// write side (rollout / enrollment resolver) reconcile against, replacing the
// ad-hoc status switches that previously derived a node's status independently
// on each side and could drift apart.
type State string

const (
	// StateRequested is the initial state right after a managed runtime is
	// requested, before provisioning has begun.
	StateRequested State = "requested"
	// StateProvisioning covers the window where infrastructure is being
	// provisioned and the node is not yet enrolled.
	StateProvisioning State = "provisioning"
	// StateEnrolledPendingAgent means enrollment completed but no reachable
	// runtime target / agent has been observed yet.
	StateEnrolledPendingAgent State = "enrolled_pending_agent"
	// StateProvisioned means the node is enrolled and reachable, but live agent
	// telemetry has not yet been observed.
	StateProvisioned State = "provisioned"
	// StateConnected means enrolled, reachable, and reporting live telemetry.
	StateConnected State = "connected"
	// StateDegraded means the node was healthy but is currently recovering /
	// retrying.
	StateDegraded State = "degraded"
	// StateStale means the node has not reported within its heartbeat window.
	StateStale State = "stale"
	// StateStalled means enrollment has been pending longer than
	// EnrollmentStalledThreshold and the owner should be guided to reconnect.
	StateStalled State = "stalled"
	// StateDecommissioning covers an in-flight teardown.
	StateDecommissioning State = "decommissioning"
	// StateDecommissioned is the terminal torn-down state.
	StateDecommissioned State = "decommissioned"
	// StateFailed is a failure state that carries a reason_code (see
	// ManagedRuntimeProvisionFailureDetails / rollout guidance).
	StateFailed State = "failed"
)

// Persisted enrollment-status metadata values (lease metadata key
// runtime_enrollment_status). These are the only lifecycle-ish values written
// by pre-B1 code; DeriveState maps them 1:1 into the richer State vocabulary so
// existing leases continue to project correctly. internal/routes/metrics.go
// aliases these to keep a single declaration.
const (
	EnrollmentStatusPending  = "pending"
	EnrollmentStatusRetrying = "retrying"
	EnrollmentStatusFailed   = "failed"
	EnrollmentStatusEnrolled = "enrolled"
)

// EnrollmentStalledThreshold is how long a managed runtime may sit in a pending
// (pre-enrolled) state before it is treated as stalled and the owner is guided
// to reconnect. It is measured from when enrollment first started
// (enrollment_started_at), not from lease renewal — the latter resets and would
// hide a genuinely stuck enrollment.
const EnrollmentStalledThreshold = 15 * time.Minute

// EnrollmentStartedAtKey is the lease metadata key recording when enrollment
// began. Stalled detection measures age from it, NOT from lease RenewedAt (which
// resets on every patch and would hide a genuinely stuck enrollment).
const EnrollmentStartedAtKey = "enrollment_started_at"

// StampEnrollmentStart records EnrollmentStartedAtKey at lease creation if it is
// not already set, so the projection can measure how long a runtime has been
// pending. It never overwrites an existing stamp — age is measured from first
// creation, across retries.
func StampEnrollmentStart(metadata map[string]string, now time.Time) {
	if metadata == nil {
		return
	}
	if strings.TrimSpace(metadata[EnrollmentStartedAtKey]) != "" {
		return
	}
	metadata[EnrollmentStartedAtKey] = now.UTC().Format(time.RFC3339)
}

// String returns the wire representation of the state.
func (s State) String() string { return string(s) }

// allowedTransitions is the lifecycle transition table. A transition that is
// not listed here is illegal and is rejected by CanTransition / Transition.
var allowedTransitions = map[State][]State{
	StateRequested:            {StateProvisioning, StateFailed, StateDecommissioning},
	StateProvisioning:         {StateEnrolledPendingAgent, StateStalled, StateFailed, StateDecommissioning},
	StateEnrolledPendingAgent: {StateProvisioned, StateStalled, StateDegraded, StateFailed, StateDecommissioning},
	StateProvisioned:          {StateConnected, StateDegraded, StateStale, StateDecommissioning, StateFailed},
	StateConnected:            {StateDegraded, StateStale, StateDecommissioning},
	StateDegraded:             {StateConnected, StateStale, StateFailed, StateDecommissioning},
	StateStale:                {StateConnected, StateDegraded, StateDecommissioning, StateFailed},
	StateStalled:              {StateProvisioning, StateEnrolledPendingAgent, StateProvisioned, StateDecommissioning, StateFailed},
	StateDecommissioning:      {StateDecommissioned, StateFailed},
	StateDecommissioned:       {},
	StateFailed:               {StateDecommissioning, StateProvisioning},
}

// KnownState reports whether s is a recognized lifecycle state.
func KnownState(s State) bool {
	_, ok := allowedTransitions[s]
	return ok
}

// CanTransition reports whether moving from -> to is a legal lifecycle
// transition. A self-transition (from == to) on a known state is always allowed
// (idempotent re-observation). An unknown source state is rejected so the
// write side cannot persist a state the projection cannot map.
func CanTransition(from, to State) bool {
	if !KnownState(from) {
		return false
	}
	if from == to {
		return true
	}
	for _, next := range allowedTransitions[from] {
		if next == to {
			return true
		}
	}
	return false
}

// IsEnrollmentStalled reports whether an enrollment that started age ago has
// exceeded the stalled threshold. The caller is responsible for only applying
// this to pending states (see DeriveState); an age of 0 (unknown start time)
// never counts as stalled.
func IsEnrollmentStalled(age time.Duration) bool {
	return age > 0 && age >= EnrollmentStalledThreshold
}

// DeriveInputs carries the observable facts used to derive a lifecycle State
// from persisted lease / enrollment data. It is deliberately decoupled from the
// PocketBase/vmlease types so both the route projection and the job write side
// can populate it without importing each other.
type DeriveInputs struct {
	// EnrollmentStatus is the persisted runtime_enrollment_status metadata value
	// (pending / retrying / failed / enrolled). Case-insensitive; empty is
	// treated as pending.
	EnrollmentStatus string
	// DesiredStopped is true when the desired state is stopped or archived, i.e.
	// the node is intentionally offline rather than unhealthy.
	DesiredStopped bool
	// HasRuntimeTarget is true when a reachable runtime target (public IP / SSH
	// host) is known for the node.
	HasRuntimeTarget bool
	// HasTelemetry is true when live agent telemetry has been observed.
	HasTelemetry bool
	// EnrollmentAge is the time since enrollment started. Zero means unknown
	// (never counts as stalled).
	EnrollmentAge time.Duration
}

// DeriveState maps observable enrollment facts to the canonical lifecycle
// State. It reproduces the intent of the legacy managedRuntimeHealthState
// switch while adding stalled TTL detection, and is the single function the
// projection layer calls so read and write sides agree.
func DeriveState(in DeriveInputs) State {
	switch strings.ToLower(strings.TrimSpace(in.EnrollmentStatus)) {
	case EnrollmentStatusFailed:
		return StateFailed
	case EnrollmentStatusEnrolled:
		switch {
		case in.DesiredStopped:
			// Enrolled but intentionally stopped: provisioned, not degraded.
			return StateProvisioned
		case in.HasTelemetry:
			return StateConnected
		case in.HasRuntimeTarget:
			return StateProvisioned
		default:
			return StateEnrolledPendingAgent
		}
	case EnrollmentStatusRetrying:
		if IsEnrollmentStalled(in.EnrollmentAge) {
			return StateStalled
		}
		return StateDegraded
	default:
		// pending / unknown / empty: still provisioning until it either
		// enrolls or crosses the stalled threshold.
		if IsEnrollmentStalled(in.EnrollmentAge) {
			return StateStalled
		}
		return StateProvisioning
	}
}

// IsPending reports whether the node is still coming up (pre-provisioned) and
// therefore eligible to become Stalled on age.
func (s State) IsPending() bool {
	switch s {
	case StateRequested, StateProvisioning, StateEnrolledPendingAgent, StateStalled:
		return true
	default:
		return false
	}
}

// IsActionAllowed reports whether runtime actions (start / stop / ssh) are
// allowed in this state — i.e. the node has completed enrollment. Decommission
// is handled separately and is allowed from any non-terminal state.
func (s State) IsActionAllowed() bool {
	switch s {
	case StateProvisioned, StateConnected, StateDegraded, StateStale:
		return true
	default:
		return false
	}
}

// IsHealthy reports whether the node should count toward healthy KPIs. Only a
// fully connected node (enrolled + reachable + live telemetry) is healthy;
// stalled / degraded / provisioned-without-telemetry must not inflate KPIs.
func (s State) IsHealthy() bool {
	return s == StateConnected
}

// IsTerminal reports whether the node has reached an end state from which no
// further lifecycle progress is expected.
func (s State) IsTerminal() bool {
	return s == StateDecommissioned
}
