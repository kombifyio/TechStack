package serverregistry

import (
	"fmt"
	"strings"
	"time"

	"github.com/kombifyio/techstack/pkg/runtimehealth"
)

type LifecycleState string

// DesiredState is the user-owned target for a durable runtime-server
// identity. It is independent of lifecycle, lease, provider, and Guard state.
type DesiredState string

type ConnectionState string
type HealthState string

const (
	LifecyclePlanned         LifecycleState = "planned"
	LifecycleProvisioning    LifecycleState = "provisioning"
	LifecycleEnrolling       LifecycleState = "enrolling"
	LifecycleActive          LifecycleState = "active"
	LifecycleFailed          LifecycleState = "failed"
	LifecycleDecommissioning LifecycleState = "decommissioning"
	LifecycleDecommissioned  LifecycleState = "decommissioned"

	DesiredRunning DesiredState = "running"
	DesiredStopped DesiredState = "stopped"
	DesiredAbsent  DesiredState = "absent"

	ConnectionPending    ConnectionState = "pending"
	ConnectionConnecting ConnectionState = "connecting"
	ConnectionConnected  ConnectionState = "connected"
	ConnectionDegraded   ConnectionState = "degraded"
	ConnectionStale      ConnectionState = "stale"
	ConnectionOffline    ConnectionState = "offline"
	ConnectionRevoked    ConnectionState = "revoked"

	HealthUnknown   HealthState = "unknown"
	HealthHealthy   HealthState = "healthy"
	HealthDegraded  HealthState = "degraded"
	HealthUnhealthy HealthState = "unhealthy"
)

var lifecycleTransitions = map[LifecycleState]map[LifecycleState]bool{
	LifecyclePlanned:      {LifecycleProvisioning: true, LifecycleEnrolling: true, LifecycleFailed: true, LifecycleDecommissioning: true},
	LifecycleProvisioning: {LifecycleEnrolling: true, LifecycleActive: true, LifecycleFailed: true, LifecycleDecommissioning: true},
	LifecycleEnrolling:    {LifecycleActive: true, LifecycleFailed: true, LifecycleDecommissioning: true},
	LifecycleActive:       {LifecycleEnrolling: true, LifecycleFailed: true, LifecycleDecommissioning: true},
	LifecycleFailed:       {LifecycleProvisioning: true, LifecycleEnrolling: true, LifecycleDecommissioning: true},
	// Teardown is a tombstone. Cleanup failures remain a separate projection;
	// they cannot re-enter the retryable pre-teardown Failed state.
	LifecycleDecommissioning: {LifecycleDecommissioned: true},
	LifecycleDecommissioned:  {},
}

var lifecycleStates = map[LifecycleState]bool{
	LifecyclePlanned: true, LifecycleProvisioning: true, LifecycleEnrolling: true,
	LifecycleActive: true, LifecycleFailed: true, LifecycleDecommissioning: true,
	LifecycleDecommissioned: true,
}

var desiredStates = map[DesiredState]bool{
	DesiredRunning: true, DesiredStopped: true, DesiredAbsent: true,
}

var connectionStates = map[ConnectionState]bool{
	ConnectionPending: true, ConnectionConnecting: true, ConnectionConnected: true,
	ConnectionDegraded: true, ConnectionStale: true, ConnectionOffline: true,
	ConnectionRevoked: true,
}

var healthStates = map[HealthState]bool{
	HealthUnknown: true, HealthHealthy: true, HealthDegraded: true, HealthUnhealthy: true,
}

func CanTransitionLifecycle(from, to LifecycleState) bool {
	if from == to {
		return true
	}
	return lifecycleTransitions[from][to]
}

// HasDecommissionIntent reports whether teardown has already started or
// reached its terminal tombstone. Reconciliation must treat both states as
// idempotent evidence of the same intent and must not move a tombstone back to
// decommissioning.
func HasDecommissionIntent(state LifecycleState) bool {
	return state == LifecycleDecommissioning || state == LifecycleDecommissioned
}

func ValidateLifecycleTransition(from, to string) error {
	left, right := LifecycleState(strings.TrimSpace(from)), LifecycleState(strings.TrimSpace(to))
	if err := ValidateLifecycleState(string(left)); err != nil {
		return err
	}
	if err := ValidateLifecycleState(string(right)); err != nil {
		return err
	}
	if !CanTransitionLifecycle(left, right) {
		return fmt.Errorf("server lifecycle transition %q -> %q is not allowed", left, right)
	}
	return nil
}

// ValidateLifecycleState rejects values outside the canonical lifecycle vocabulary.
func ValidateLifecycleState(value string) error {
	state := LifecycleState(strings.TrimSpace(value))
	if !lifecycleStates[state] {
		return fmt.Errorf("unknown server lifecycle state %q", state)
	}
	return nil
}

// ValidateDesiredState rejects values outside the user-owned target vocabulary.
func ValidateDesiredState(value string) error {
	state := DesiredState(strings.TrimSpace(value))
	if !desiredStates[state] {
		return fmt.Errorf("unknown server desired state %q", state)
	}
	return nil
}

// ValidateConnectionState rejects values outside the Guard connection vocabulary.
func ValidateConnectionState(value string) error {
	state := ConnectionState(strings.TrimSpace(value))
	if !connectionStates[state] {
		return fmt.Errorf("unknown server connection state %q", state)
	}
	return nil
}

// ValidateHealthState rejects values outside the observed-health vocabulary.
func ValidateHealthState(value string) error {
	state := HealthState(strings.TrimSpace(value))
	if !healthStates[state] {
		return fmt.Errorf("unknown server health state %q", state)
	}
	return nil
}

// DeriveObservedState translates authenticated heartbeat evidence into the
// canonical connection and health dimensions. Provisioning success and
// provider reachability are deliberately not accepted as health evidence.
func DeriveObservedState(now time.Time, heartbeatAt *time.Time, observedHostState string) (ConnectionState, HealthState) {
	state := runtimehealth.DeriveServerState(runtimehealth.ServerInput{
		Now: now, HeartbeatAt: heartbeatAt, ObservedState: observedHostState,
	})
	switch state {
	case runtimehealth.ServerHealthy:
		return ConnectionConnected, HealthHealthy
	case runtimehealth.ServerDegraded:
		return ConnectionDegraded, HealthDegraded
	case runtimehealth.ServerStale:
		return ConnectionStale, HealthUnknown
	case runtimehealth.ServerOffline:
		return ConnectionOffline, HealthUnknown
	default:
		return ConnectionPending, HealthUnknown
	}
}

func MutationsAllowed(connection string) bool {
	switch ConnectionState(strings.TrimSpace(connection)) {
	case ConnectionConnected, ConnectionDegraded:
		return true
	default:
		return false
	}
}
