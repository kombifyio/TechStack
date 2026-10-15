package main

import "github.com/kombifyio/techstack/pkg/monitoring"

// bootDriveHealth uses the control-plane database for identity-bound snapshots
// and durable condition episodes, independently of filesystem capacity gauges.
func bootDriveHealth(deps routeDeps) *monitoring.DriveHealthService {
	if deps.v2 == nil || deps.v2.db == nil || deps.v2.db.DB == nil || deps.monitor == nil {
		return nil
	}
	store, err := monitoring.NewPostgresDriveHealthStore(deps.v2.db.DB)
	if err != nil {
		deps.log.Warn("guard_drive_health_unavailable", "error", err)
		return nil
	}
	return &monitoring.DriveHealthService{
		Store: store, Notifier: deps.monitor.alertNotifier,
		OnError: func(err error) { deps.log.Warn("guard_drive_health_pass_failed", "error", err) },
	}
}
