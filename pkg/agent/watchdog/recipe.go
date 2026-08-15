// Package watchdog provides a rule-based self-healing engine for the
// kombify-TechStack agent.  It runs as a goroutine and periodically
// evaluates curated recipes against aggregated system state.
// No LLM calls are involved — all decisions are deterministic.
package watchdog

import (
	"context"
	"time"
)

// ---------------------------------------------------------------------------
// Recipe interface
// ---------------------------------------------------------------------------

// Recipe is a single self-heal rule.  Implementations live in
// recipe_*.go files.
type Recipe interface {
	// Name returns a stable, human-readable identifier (e.g. "service-restart").
	Name() string

	// Check inspects the current SystemState and returns a Condition.
	// Condition.Triggered == true means the recipe wants to act.
	Check(ctx context.Context, state *SystemState) (*Condition, error)

	// DryRun produces a Plan without side-effects.
	DryRun(ctx context.Context, cond *Condition) (*Plan, error)

	// Execute carries out the remediation described in plan.
	Execute(ctx context.Context, plan *Plan) (*Result, error)

	// Rollback attempts to undo the last Execute.
	Rollback(ctx context.Context, result *Result) (*Result, error)

	// AutoExec returns true if the recipe may execute without operator
	// confirmation.
	AutoExec() bool

	// Enabled reports whether the recipe is active.
	Enabled() bool

	// SetEnabled toggles the recipe on or off at runtime.
	SetEnabled(bool)
}

// ---------------------------------------------------------------------------
// Value types
// ---------------------------------------------------------------------------

// SystemState is an aggregated snapshot of the host that recipes inspect.
type SystemState struct {
	// Services maps a unit/service name to its current state string
	// (e.g. "running", "failed", "inactive").
	Services map[string]ServiceInfo

	// DiskUsagePercent is the root-filesystem usage in [0, 100].
	DiskUsagePercent float64

	// MemoryUsagePercent is total RAM usage in [0, 100].
	MemoryUsagePercent float64

	// Processes is a snapshot of running process names.
	Processes []string

	// CertExpiresAt is the mTLS leaf certificate's NotAfter.
	CertExpiresAt time.Time

	// LastHeartbeatAt is the timestamp of the most recent successful
	// heartbeat to Core.
	LastHeartbeatAt time.Time

	// OOMEvents lists recently detected OOM-kill entries.
	OOMEvents []OOMEvent

	// CollectedAt records when this state snapshot was taken.
	CollectedAt time.Time
}

// ServiceInfo holds the state of a single systemd unit (or equivalent).
type ServiceInfo struct {
	State     string    // "running", "failed", "inactive", …
	Since     time.Time // when the unit entered this state
	FailCount int       // consecutive check cycles the unit has been non-running
}

// OOMEvent records a single OOM-kill entry from dmesg/journald.
type OOMEvent struct {
	ProcessName string
	PID         int
	Timestamp   time.Time
}

// Condition is the output of Recipe.Check().
type Condition struct {
	Triggered bool
	Recipe    string
	Reason    string
	Severity  string            // "info", "warning", "critical"
	Evidence  map[string]string // free-form key/value diagnostic data
}

// Plan describes the steps a recipe intends to execute.
type Plan struct {
	Steps        []string
	DryRunOutput string
	RollbackHint string
	EstDuration  time.Duration
}

// Result is the outcome of Execute or Rollback.
type Result struct {
	Success      bool
	Output       string
	Duration     time.Duration
	RollbackData map[string]string // opaque data needed by Rollback
}

// HealEvent is emitted after every recipe execution attempt and reported
// back to Core via the HealReporter callback.
type HealEvent struct {
	EventID        string
	RecipeName     string
	TriggerReason  string
	AutoExecuted   bool
	Success        bool
	ActionTaken    string
	RollbackAction string
	Severity       string
	Duration       time.Duration
	Timestamp      time.Time
}
