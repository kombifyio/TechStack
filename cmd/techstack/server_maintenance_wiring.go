package main

import (
	"github.com/kombifyio/techstack/pkg/jobs"
	"github.com/kombifyio/techstack/pkg/logger"
	"github.com/kombifyio/techstack/pkg/orchestrator"
	"github.com/kombifyio/techstack/pkg/servermaintenance"
)

const envServerMaintenanceInterval = "TECHSTACK_SERVER_MAINTENANCE_INTERVAL"

// bootServerMaintenanceLoop advances owner reboots and OS updates through the
// same typed StackKit command channel the lifecycle jobs use. Without a
// database or commander it returns nil and maintenance stays inert.
func bootServerMaintenanceLoop(boot *v2Boot, orch *orchestrator.Orchestrator, log *logger.Logger) *servermaintenance.Loop {
	if boot == nil || boot.db == nil || boot.db.DB == nil || orch == nil {
		return nil
	}
	dispatcher := jobs.HostMaintenanceDispatcherFor(orch.StackKitCommander())
	if dispatcher == nil {
		return nil
	}
	store := controlPlanePostgresStore(boot)
	return servermaintenance.NewLoop(store, servermaintenance.Runner{
		Jobs: store, Servers: store, Events: store, Dispatcher: dispatcher,
	}, durationFromEnv(envServerMaintenanceInterval, 0), log.Logger)
}
