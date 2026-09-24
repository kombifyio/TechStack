package main

import (
	"database/sql"

	productnotifications "github.com/kombifyio/techstack/internal/notifications"
	"github.com/kombifyio/techstack/internal/providercontrol"
	"github.com/kombifyio/techstack/pkg/config"
	"github.com/kombifyio/techstack/pkg/jobs"
	"github.com/kombifyio/techstack/pkg/logger"
	"github.com/kombifyio/techstack/pkg/ril/workflow"
)

// The public build retains the same native custody, ledger, admission and
// cleanup implementation. No commercial adapter or credential resolver is linked.
func composeProviderControl(database *sql.DB, edition config.Edition, log *logger.Logger,
	_ *productnotifications.Outbox, onParkedOperation func(providercontrol.OperationRef),
	_ ...*workflow.Engine,
) (*providercontrol.Runtime, jobs.RuntimeActions, error) {
	return composeNativeProviderControl(database, edition, log, nil, onParkedOperation, nil)
}
