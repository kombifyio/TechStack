package main

import (
	"time"

	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/jobs"
	"github.com/kombifyio/techstack/pkg/logger"
	"github.com/kombifyio/techstack/pkg/monitoring"
	"github.com/kombifyio/techstack/pkg/orchestrator"
)

const (
	envJobExecutionReclaimInterval = "TECHSTACK_JOB_EXECUTION_RECLAIM_INTERVAL"

	jobExecutionReclaimedMetric  = "job_execution_reclaimed_total"
	jobExecutionDeferStallMetric = "job_execution_defer_stalled_seconds"
)

// bootJobExecutionReclaimer composes the durable job execution reclaimer. Its
// first pass is the startup reconciliation: rows this boot finds 'running'
// behind an expired lease belong to a process that is gone, and until they are
// terminalized they hold their stack's execution claim and every later
// operation on that stack defers forever.
func bootJobExecutionReclaimer(
	boot *v2Boot,
	orch *orchestrator.Orchestrator,
	monitor *monitoringBoot,
	log *logger.Logger,
) *orchestrator.JobExecutionReclaimer {
	if boot == nil || boot.db == nil || boot.db.DB == nil || orch == nil {
		return nil
	}
	reclaimer, err := orchestrator.NewJobExecutionReclaimer(orchestrator.JobExecutionReclaimConfig{
		Orchestrator: orch,
		Store:        controlplane.NewPostgresStore(boot.db.DB),
		Interval: durationFromEnv(envJobExecutionReclaimInterval,
			orchestrator.DefaultJobExecutionReclaimInterval),
		RecordStats: jobExecutionReclaimStatsRecorder(monitor),
	})
	if err != nil {
		log.Error("job_execution_reclaimer_config_invalid", "error", err)
		return nil
	}
	return reclaimer
}

// jobExecutionReclaimStatsRecorder publishes how many orphaned executions each
// pass had to retire. A non-zero value means a process died mid-execution.
func jobExecutionReclaimStatsRecorder(monitor *monitoringBoot) func(orchestrator.JobExecutionReclaimStats) {
	if monitor == nil || monitor.tsdb == nil {
		return nil
	}
	tsdb := monitor.tsdb
	return func(stats orchestrator.JobExecutionReclaimStats) {
		if stats.Reclaimed == 0 {
			return
		}
		_ = tsdb.Write([]monitoring.MetricSample{{
			Name:      jobExecutionReclaimedMetric,
			Value:     float64(stats.Reclaimed),
			Timestamp: time.Now().UTC(),
		}})
	}
}

// jobExecutionDeferObserver turns a job that has been blocked on a durable
// execution claim for longer than the alert threshold into a metric, so the
// next time a stack is stuck the wait is visible instead of being buried in a
// once-per-second info log.
func jobExecutionDeferObserver(monitor *monitoringBoot) jobs.ExecutionDeferObserver {
	if monitor == nil || monitor.tsdb == nil {
		return nil
	}
	tsdb := monitor.tsdb
	return func(report jobs.ExecutionDeferReport) {
		_ = tsdb.Write([]monitoring.MetricSample{{
			Name:  jobExecutionDeferStallMetric,
			Value: report.WaitingFor.Seconds(),
			Labels: map[string]string{
				"reason":   report.WaitReason,
				"job_type": string(report.JobType),
			},
			Timestamp: time.Now().UTC(),
		}})
	}
}
