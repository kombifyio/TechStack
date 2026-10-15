package main

import (
	"log/slog"
	"time"

	"github.com/kombifyio/techstack/pkg/guardexec"
)

// confirmGuardUpdateTrial is called after the control plane accepted the first
// authenticated heartbeat of this process. During an update trial it tells the
// supervising executor that the new Guard works; without a trial it does nothing.
func confirmGuardUpdateTrial(execution guardExecution, log *slog.Logger) {
	if execution.mode != guardModeSplit {
		return
	}
	if err := guardexec.ConfirmTrial(execution.handoffDir, version, time.Now()); err != nil {
		log.Warn("guard_update_trial_confirmation_failed", "error", err.Error())
	}
}
