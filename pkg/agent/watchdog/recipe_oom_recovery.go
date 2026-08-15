package watchdog

import (
	"context"
	"fmt"
	"strings"
	"time"
)

const (
	oomRecoveryName        = "oom-recovery"
	oomMemoryBumpFactor    = 1.20 // +20%
	oomRevertAfter         = 1 * time.Hour
	oomRecoveryEstDuration = 15 * time.Second
)

// OOMRecovery detects OOM-killed processes (via dmesg/journald entries
// in SystemState.OOMEvents) and restarts them with an increased memory
// limit.  The bump is reverted after oomRevertAfter.
type OOMRecovery struct {
	enabled bool
}

// NewOOMRecovery creates an enabled OOMRecovery recipe.
func NewOOMRecovery() *OOMRecovery {
	return &OOMRecovery{enabled: true}
}

func (r *OOMRecovery) Name() string      { return oomRecoveryName }
func (r *OOMRecovery) AutoExec() bool    { return true }
func (r *OOMRecovery) Enabled() bool     { return r.enabled }
func (r *OOMRecovery) SetEnabled(v bool) { r.enabled = v }

func (r *OOMRecovery) Check(_ context.Context, state *SystemState) (*Condition, error) {
	if state == nil || len(state.OOMEvents) == 0 {
		return &Condition{Triggered: false, Recipe: r.Name()}, nil
	}

	// Only consider events within the last 5 minutes to avoid re-triggering
	// on stale journal entries.
	cutoff := state.CollectedAt.Add(-5 * time.Minute)
	var recent []OOMEvent
	for _, ev := range state.OOMEvents {
		if ev.Timestamp.After(cutoff) {
			recent = append(recent, ev)
		}
	}

	if len(recent) == 0 {
		return &Condition{Triggered: false, Recipe: r.Name()}, nil
	}

	names := make([]string, 0, len(recent))
	evidence := make(map[string]string)
	for _, ev := range recent {
		names = append(names, ev.ProcessName)
		evidence[fmt.Sprintf("pid_%d", ev.PID)] = fmt.Sprintf("%s killed at %s",
			ev.ProcessName, ev.Timestamp.Format(time.RFC3339))
	}

	return &Condition{
		Triggered: true,
		Recipe:    r.Name(),
		Reason:    fmt.Sprintf("OOM kills detected: %s", strings.Join(names, ", ")),
		Severity:  "critical",
		Evidence:  evidence,
	}, nil
}

func (r *OOMRecovery) DryRun(_ context.Context, cond *Condition) (*Plan, error) {
	var steps []string
	for _, name := range processNamesFromEvidence(cond.Evidence) {
		steps = append(steps,
			fmt.Sprintf("restart %s with +%.0f%% memory limit", name, (oomMemoryBumpFactor-1)*100),
			fmt.Sprintf("schedule revert of %s memory limit after %s", name, oomRevertAfter),
		)
	}
	return &Plan{
		Steps:        steps,
		DryRunOutput: fmt.Sprintf("would restart OOM-killed processes with +%.0f%% memory", (oomMemoryBumpFactor-1)*100),
		RollbackHint: "revert memory limit to original value",
		EstDuration:  oomRecoveryEstDuration,
	}, nil
}

func (r *OOMRecovery) Execute(_ context.Context, _ *Plan) (*Result, error) {
	// Stub: in production this would adjust cgroup limits and restart.
	return &Result{
		Success:  true,
		Output:   fmt.Sprintf("restarted OOM-killed process(es) with +%.0f%% memory (stub)", (oomMemoryBumpFactor-1)*100),
		Duration: oomRecoveryEstDuration,
		RollbackData: map[string]string{
			"bump_factor":  fmt.Sprintf("%.2f", oomMemoryBumpFactor),
			"revert_after": oomRevertAfter.String(),
		},
	}, nil
}

func (r *OOMRecovery) Rollback(_ context.Context, result *Result) (*Result, error) {
	if result == nil {
		return &Result{Success: true, Output: "nothing to rollback"}, nil
	}
	return &Result{
		Success:  true,
		Output:   "reverted memory limits to original values (stub rollback)",
		Duration: 2 * time.Second,
	}, nil
}

// processNamesFromEvidence extracts process names from the evidence map.
func processNamesFromEvidence(evidence map[string]string) []string {
	seen := make(map[string]bool)
	var names []string
	for _, v := range evidence {
		// Value format: "<name> killed at <time>"
		if idx := strings.Index(v, " killed at "); idx > 0 {
			name := v[:idx]
			if !seen[name] {
				seen[name] = true
				names = append(names, name)
			}
		}
	}
	return names
}
