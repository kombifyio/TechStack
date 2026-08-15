package main

import (
	"errors"
	"time"

	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/logger"
	"github.com/kombifyio/techstack/pkg/monitoring"
	"github.com/kombifyio/techstack/pkg/serverregistry"
)

const (
	envServerRegistrySweepInterval   = "TECHSTACK_SERVER_REGISTRY_SWEEP_INTERVAL"
	envServerRegistryOutboxRetention = "TECHSTACK_SERVER_REGISTRY_OUTBOX_RETENTION"

	outboxRowsMetric      = "server_registry_outbox_rows_estimate"
	outboxOldestAgeMetric = "server_registry_outbox_oldest_age_seconds"
)

// bootServerRegistrySweeper composes the honest-observed-state sweeper: it
// demotes stale server connection/health through ApplyServerEvent and bounds
// server_registry_outbox (30d retention prune + size gauge into the
// monitoring/alert path).
func bootServerRegistrySweeper(boot *v2Boot, monitor *monitoringBoot, log *logger.Logger) *serverregistry.Sweeper {
	if boot == nil || boot.db == nil || boot.db.DB == nil {
		return nil
	}
	store := controlplane.NewPostgresStore(boot.db.DB)
	sweeper, err := serverregistry.NewSweeper(serverregistry.SweeperConfig{
		Registry:        store,
		Outbox:          store,
		Interval:        durationFromEnv(envServerRegistrySweepInterval, serverregistry.DefaultSweepInterval),
		OutboxRetention: durationFromEnv(envServerRegistryOutboxRetention, serverregistry.DefaultOutboxRetention),
		Logger:          log.Logger,
		IsBenignConflict: func(err error) bool {
			return errors.Is(err, controlplane.ErrConflict)
		},
		RecordOutboxStats: serverRegistryOutboxStatsRecorder(monitor),
	})
	if err != nil {
		log.Error("server_registry_sweeper_config_invalid", "error", err)
		return nil
	}
	return sweeper
}

// serverRegistryOutboxStatsRecorder wires the outbox size signal into the
// embedded monitoring TSDB, where DefaultAlertRules evaluates the backlog and
// prune-stall alerts.
func serverRegistryOutboxStatsRecorder(monitor *monitoringBoot) func(serverregistry.OutboxStats) {
	if monitor == nil || monitor.tsdb == nil {
		return nil
	}
	tsdb := monitor.tsdb
	return func(stats serverregistry.OutboxStats) {
		now := time.Now().UTC()
		samples := []monitoring.MetricSample{{
			Name: outboxRowsMetric, Value: float64(stats.EstimatedRows), Timestamp: now,
		}}
		if stats.OldestCreatedAt != nil {
			age := now.Sub(stats.OldestCreatedAt.UTC()).Seconds()
			if age < 0 {
				age = 0
			}
			samples = append(samples, monitoring.MetricSample{
				Name: outboxOldestAgeMetric, Value: age, Timestamp: now,
			})
		}
		_ = tsdb.Write(samples)
	}
}

func durationFromEnv(key string, fallback time.Duration) time.Duration {
	if raw := v2FirstEnv(key); raw != "" {
		if parsed, err := time.ParseDuration(raw); err == nil && parsed > 0 {
			return parsed
		}
	}
	return fallback
}
