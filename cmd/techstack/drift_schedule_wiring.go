package main

import (
	"github.com/kombifyio/techstack/pkg/config"
	"github.com/kombifyio/techstack/pkg/logger"
	"github.com/kombifyio/techstack/pkg/orchestrator"
)

// bootDriftScheduler composes the scheduled Advanced drift detection over the
// canonical drift schedule tenant directory. It returns nil when drift
// scheduling is disabled or the control-plane database is unavailable.
func bootDriftScheduler(boot *v2Boot, cfg *config.Config, orch *orchestrator.Orchestrator, log *logger.Logger) *orchestrator.DriftScheduler {
	if cfg == nil || !cfg.Drift.Enabled || orch == nil {
		return nil
	}
	store := controlPlanePostgresStore(boot)
	if store == nil {
		return nil
	}
	scheduler, err := orch.NewDriftScheduler(orchestrator.DriftSchedulerConfig{
		Directory: store,
		Interval:  cfg.Drift.DriftIntervalDuration(),
		OnError: func(err error) {
			if log != nil {
				log.Warn("drift_schedule_pass_error", "error", err)
			}
		},
	})
	if err != nil {
		if log != nil {
			log.Warn("drift_scheduler_unavailable", "error", err)
		}
		return nil
	}
	return scheduler
}
