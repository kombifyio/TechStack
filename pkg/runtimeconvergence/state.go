// Package runtimeconvergence defines the provider-neutral agent convergence
// status carried by Guard observations.  It deliberately contains only
// bounded, reader-safe state: callers must never put an executor error string
// on the wire.
package runtimeconvergence

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync/atomic"
	"time"
)

const (
	StatePending  = "pending"
	StateReady    = "ready"
	StateDegraded = "degraded"

	ComponentPending = "pending"
	ComponentReady   = "ready"
	ComponentFailed  = "failed"

	TechstackRuntimeComponent = "techstack_runtime"
	StackKitsRuntimeComponent = "stackkits_runtime"

	TechstackRuntimeUnavailableError = "techstack_runtime_unavailable"
	StackKitsRuntimeUnavailableError = "stackkits_runtime_unavailable"
	AgentRestartRequiredError        = "agent_restart_required"
	ConvergenceIncompleteError       = "runtime_convergence_incomplete"
)

var ErrInvalid = errors.New("invalid runtime convergence")

// Component is one bounded runtime package convergence observation.
type Component struct {
	Name       string    `json:"name"`
	State      string    `json:"state"`
	ObservedAt time.Time `json:"observed_at"`
	ErrorCode  string    `json:"error_code,omitempty"`
}

// Snapshot is the wire and persistence contract for agent runtime
// convergence.  It has no provider-specific fields so every provider follows
// the same lifecycle/read-model path.
type Snapshot struct {
	State      string      `json:"state"`
	ObservedAt time.Time   `json:"observed_at"`
	ErrorCode  string      `json:"error_code,omitempty"`
	Components []Component `json:"components"`
}

// Tracker stores the most recent status atomically so heartbeat reads never
// race with the convergence worker and never need to hold the executor lock.
type Tracker struct {
	current atomic.Pointer[Snapshot]
}

func NewTracker(observedAt time.Time) *Tracker {
	tracker := &Tracker{}
	initial := Pending(observedAt)
	tracker.current.Store(&initial)
	return tracker
}

func (t *Tracker) Snapshot() Snapshot {
	if t == nil {
		return Pending(time.Now().UTC())
	}
	current := t.current.Load()
	if current == nil {
		return Pending(time.Now().UTC())
	}
	return clone(*current)
}

// Set publishes a validated copy.  A malformed internal update is ignored;
// the heartbeat therefore keeps the last truthful state instead of emitting
// an unbounded or secret-bearing payload.
func (t *Tracker) Set(snapshot Snapshot) {
	if t == nil {
		return
	}
	normalized, err := Normalize(snapshot)
	if err != nil {
		return
	}
	t.current.Store(&normalized)
}

func Pending(observedAt time.Time) Snapshot {
	observedAt = normalizeTime(observedAt)
	return Snapshot{
		State:      StatePending,
		ObservedAt: observedAt,
		Components: []Component{{Name: TechstackRuntimeComponent, State: ComponentPending, ObservedAt: observedAt}, {Name: StackKitsRuntimeComponent, State: ComponentPending, ObservedAt: observedAt}},
	}
}

// Aggregate derives the top-level state from component observations.  It is
// intentionally small and deterministic: pending means work has not finished,
// degraded means at least one component failed, and ready means every
// component is ready.
func Aggregate(observedAt time.Time, components ...Component) Snapshot {
	observedAt = normalizeTime(observedAt)
	snapshot := Snapshot{State: StatePending, ObservedAt: observedAt, Components: append([]Component(nil), components...)}
	ready, failed := len(components) > 0, false
	for index := range snapshot.Components {
		snapshot.Components[index].ObservedAt = normalizeTime(snapshot.Components[index].ObservedAt)
		switch snapshot.Components[index].State {
		case ComponentReady:
		case ComponentFailed:
			ready, failed = false, true
		default:
			ready = false
		}
	}
	switch {
	case failed:
		snapshot.State, snapshot.ErrorCode = StateDegraded, ConvergenceIncompleteError
	case ready:
		snapshot.State = StateReady
	}
	return snapshot
}

// Normalize validates and canonicalizes a wire/persistence snapshot.  The
// error-code allowlist is the boundary that prevents raw executor errors from
// reaching resources_json or the server read model.
func Normalize(snapshot Snapshot) (Snapshot, error) {
	snapshot.Components = append([]Component(nil), snapshot.Components...)
	snapshot.State = strings.ToLower(strings.TrimSpace(snapshot.State))
	snapshot.ErrorCode = strings.ToLower(strings.TrimSpace(snapshot.ErrorCode))
	snapshot.ObservedAt = normalizeTime(snapshot.ObservedAt)
	if snapshot.ObservedAt.IsZero() || !validState(snapshot.State) || !validErrorCode(snapshot.ErrorCode) {
		return Snapshot{}, ErrInvalid
	}
	if len(snapshot.Components) == 0 || len(snapshot.Components) > 8 {
		return Snapshot{}, ErrInvalid
	}
	seen := make(map[string]struct{}, len(snapshot.Components))
	for index := range snapshot.Components {
		component := &snapshot.Components[index]
		component.Name = strings.ToLower(strings.TrimSpace(component.Name))
		component.State = strings.ToLower(strings.TrimSpace(component.State))
		component.ErrorCode = strings.ToLower(strings.TrimSpace(component.ErrorCode))
		component.ObservedAt = normalizeTime(component.ObservedAt)
		if !validName(component.Name) || !validComponentState(component.State) || component.ObservedAt.IsZero() || !validErrorCode(component.ErrorCode) {
			return Snapshot{}, ErrInvalid
		}
		if _, duplicate := seen[component.Name]; duplicate {
			return Snapshot{}, ErrInvalid
		}
		seen[component.Name] = struct{}{}
		if component.State == ComponentFailed && component.ErrorCode == "" {
			return Snapshot{}, ErrInvalid
		}
		if component.State != ComponentFailed && component.ErrorCode != "" && component.ErrorCode != AgentRestartRequiredError {
			return Snapshot{}, ErrInvalid
		}
	}
	if snapshot.State == StateReady && snapshot.ErrorCode != "" {
		return Snapshot{}, ErrInvalid
	}
	if snapshot.State == StateDegraded && snapshot.ErrorCode == "" {
		return Snapshot{}, ErrInvalid
	}
	sort.Slice(snapshot.Components, func(i, j int) bool { return snapshot.Components[i].Name < snapshot.Components[j].Name })
	return clone(snapshot), nil
}

// Map returns the canonical JSON-compatible representation used by the
// worker resources/capabilities and server metadata projections.
func Map(snapshot Snapshot) map[string]any {
	normalized, err := Normalize(snapshot)
	if err != nil {
		return nil
	}
	encoded, err := json.Marshal(normalized)
	if err != nil {
		return nil
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	var value map[string]any
	if decoder.Decode(&value) != nil {
		return nil
	}
	return value
}

func validState(value string) bool {
	return value == StatePending || value == StateReady || value == StateDegraded
}

func validComponentState(value string) bool {
	return value == ComponentPending || value == ComponentReady || value == ComponentFailed
}

func validName(value string) bool {
	if value == "" || len(value) > 64 {
		return false
	}
	for index, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= '0' && character <= '9' && index > 0) || character == '_' || character == '-' {
			continue
		}
		return false
	}
	return value[0] >= 'a' && value[0] <= 'z'
}

func validErrorCode(value string) bool {
	if value == "" {
		return true
	}
	switch value {
	case TechstackRuntimeUnavailableError, StackKitsRuntimeUnavailableError,
		AgentRestartRequiredError, ConvergenceIncompleteError:
		return true
	default:
		return false
	}
}

func normalizeTime(value time.Time) time.Time {
	if value.IsZero() {
		return time.Time{}
	}
	return value.UTC()
}

func clone(snapshot Snapshot) Snapshot {
	snapshot.Components = append([]Component(nil), snapshot.Components...)
	return snapshot
}

// ValidateComponent keeps callers honest when they construct component states
// outside this package.
func ValidateComponent(component Component) error {
	if _, err := Normalize(Snapshot{State: StatePending, ObservedAt: component.ObservedAt, Components: []Component{component}}); err != nil {
		return fmt.Errorf("%w: component", ErrInvalid)
	}
	return nil
}
