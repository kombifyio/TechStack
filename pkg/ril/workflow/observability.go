package workflow

import "time"

// Observer receives bounded, low-cardinality workflow lifecycle signals.
// Implementations must not use run IDs, owner IDs, or signal keys as metric
// labels. Durable, per-run detail belongs to the audit store instead.
type Observer interface {
	ObserveRunTransition(RunType, RunStatus, RunStatus, time.Duration)
	ObserveStepTransition(RunType, string, StepStatus, StepStatus, int, time.Duration)
	ObserveTimer(TimerKind, string)
}

type noopObserver struct{}

func (noopObserver) ObserveRunTransition(RunType, RunStatus, RunStatus, time.Duration) {}
func (noopObserver) ObserveStepTransition(RunType, string, StepStatus, StepStatus, int, time.Duration) {
}
func (noopObserver) ObserveTimer(TimerKind, string) {}

// AuditEvent is one append-only workflow lifecycle record backed by the
// central audit_events table. Details contain state metadata only; workflow
// inputs, outputs, error strings, credentials, and connector material are
// deliberately excluded.
type AuditEvent struct {
	ID           int64          `json:"id"`
	Action       string         `json:"action"`
	ResourceType string         `json:"resource_type"`
	ResourceID   string         `json:"resource_id"`
	Details      map[string]any `json:"details,omitempty"`
	CreatedAt    time.Time      `json:"created_at"`
}

const (
	TimerOutcomeDelivered       = "delivered"
	TimerOutcomeAlreadyResolved = "already_resolved"
	TimerOutcomeDeliveryFailed  = "delivery_failed"
	TimerOutcomeMarkFailed      = "mark_failed"
)
