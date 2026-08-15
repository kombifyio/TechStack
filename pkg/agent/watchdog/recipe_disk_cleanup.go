package watchdog

import (
	"context"
	"fmt"
	"time"
)

const (
	diskCleanupName        = "disk-cleanup"
	diskUsageThreshold     = 90.0 // percent
	diskCleanupEstDuration = 60 * time.Second
)

// DiskCleanup detects when root-filesystem usage exceeds the threshold
// and cleans up old logs, docker images, and temp files.
type DiskCleanup struct {
	enabled bool
}

// NewDiskCleanup creates an enabled DiskCleanup recipe.
func NewDiskCleanup() *DiskCleanup {
	return &DiskCleanup{enabled: true}
}

func (r *DiskCleanup) Name() string      { return diskCleanupName }
func (r *DiskCleanup) AutoExec() bool    { return true }
func (r *DiskCleanup) Enabled() bool     { return r.enabled }
func (r *DiskCleanup) SetEnabled(v bool) { r.enabled = v }

func (r *DiskCleanup) Check(_ context.Context, state *SystemState) (*Condition, error) {
	if state == nil {
		return &Condition{Triggered: false, Recipe: r.Name()}, nil
	}

	if state.DiskUsagePercent < diskUsageThreshold {
		return &Condition{Triggered: false, Recipe: r.Name()}, nil
	}

	severity := "warning"
	if state.DiskUsagePercent >= 95.0 {
		severity = "critical"
	}

	return &Condition{
		Triggered: true,
		Recipe:    r.Name(),
		Reason:    fmt.Sprintf("disk usage at %.1f%% (threshold %.0f%%)", state.DiskUsagePercent, diskUsageThreshold),
		Severity:  severity,
		Evidence: map[string]string{
			"disk_usage_pct": fmt.Sprintf("%.1f", state.DiskUsagePercent),
		},
	}, nil
}

func (r *DiskCleanup) DryRun(_ context.Context, cond *Condition) (*Plan, error) {
	return &Plan{
		Steps: []string{
			"find and delete log files older than 30 days",
			"docker system prune --force",
			"clean /tmp files older than 7 days",
		},
		DryRunOutput: fmt.Sprintf("would clean disk at %s%% usage: logs >30d, docker prune, /tmp >7d",
			cond.Evidence["disk_usage_pct"]),
		RollbackHint: "deleted files cannot be restored — rollback is best-effort",
		EstDuration:  diskCleanupEstDuration,
	}, nil
}

func (r *DiskCleanup) Execute(_ context.Context, _ *Plan) (*Result, error) {
	// Stub: in production this would shell out to find/rm/docker.
	return &Result{
		Success:  true,
		Output:   "disk cleanup completed (stub)",
		Duration: diskCleanupEstDuration,
		RollbackData: map[string]string{
			"note": "deleted files are not recoverable",
		},
	}, nil
}

func (r *DiskCleanup) Rollback(_ context.Context, _ *Result) (*Result, error) {
	// Disk cleanup is inherently non-reversible; rollback is a no-op.
	return &Result{
		Success:  true,
		Output:   "disk-cleanup rollback is a no-op (deleted files cannot be restored)",
		Duration: 0,
	}, nil
}
