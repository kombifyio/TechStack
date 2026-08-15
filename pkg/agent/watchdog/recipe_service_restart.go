package watchdog

import (
	"context"
	"fmt"
	"strings"
	"time"
)

const (
	serviceRestartName        = "service-restart"
	serviceFailThreshold      = 3 // consecutive failed checks before action
	serviceRestartEstDuration = 10 * time.Second
)

// ServiceRestart detects systemd units that have been failed/inactive
// for multiple consecutive check cycles and restarts them.
type ServiceRestart struct {
	enabled bool
}

// NewServiceRestart creates an enabled ServiceRestart recipe.
func NewServiceRestart() *ServiceRestart {
	return &ServiceRestart{enabled: true}
}

func (r *ServiceRestart) Name() string      { return serviceRestartName }
func (r *ServiceRestart) AutoExec() bool    { return true }
func (r *ServiceRestart) Enabled() bool     { return r.enabled }
func (r *ServiceRestart) SetEnabled(v bool) { r.enabled = v }

func (r *ServiceRestart) Check(_ context.Context, state *SystemState) (*Condition, error) {
	if state == nil || len(state.Services) == 0 {
		return &Condition{Triggered: false, Recipe: r.Name()}, nil
	}

	var failed []string
	evidence := make(map[string]string)

	for name, info := range state.Services {
		if info.State == "running" || info.State == "" {
			continue
		}
		if info.FailCount >= serviceFailThreshold {
			failed = append(failed, name)
			evidence[name] = fmt.Sprintf("state=%s fail_count=%d since=%s",
				info.State, info.FailCount, info.Since.Format(time.RFC3339))
		}
	}

	if len(failed) == 0 {
		return &Condition{Triggered: false, Recipe: r.Name()}, nil
	}

	return &Condition{
		Triggered: true,
		Recipe:    r.Name(),
		Reason:    fmt.Sprintf("units persistently failed: %s", strings.Join(failed, ", ")),
		Severity:  "critical",
		Evidence:  evidence,
	}, nil
}

func (r *ServiceRestart) DryRun(_ context.Context, cond *Condition) (*Plan, error) {
	var steps []string
	for unit := range cond.Evidence {
		steps = append(steps, fmt.Sprintf("systemctl restart %s", unit))
	}
	return &Plan{
		Steps:        steps,
		DryRunOutput: fmt.Sprintf("would restart: %s", strings.Join(steps, "; ")),
		RollbackHint: "systemctl stop <unit> + restore previous state",
		EstDuration:  serviceRestartEstDuration,
	}, nil
}

func (r *ServiceRestart) Execute(_ context.Context, plan *Plan) (*Result, error) {
	// Stub: in production this would shell out to systemctl.
	rbData := make(map[string]string)
	for _, step := range plan.Steps {
		// Record what we restarted for rollback.
		rbData[step] = "restarted"
	}
	return &Result{
		Success:      true,
		Output:       fmt.Sprintf("executed %d restart(s)", len(plan.Steps)),
		Duration:     serviceRestartEstDuration,
		RollbackData: rbData,
	}, nil
}

func (r *ServiceRestart) Rollback(_ context.Context, result *Result) (*Result, error) {
	// Stub: would stop the units and restore previous state.
	if result == nil {
		return &Result{Success: true, Output: "nothing to rollback"}, nil
	}
	return &Result{
		Success:  true,
		Output:   fmt.Sprintf("rolled back %d unit(s)", len(result.RollbackData)),
		Duration: 2 * time.Second,
	}, nil
}
