package main

import (
	commonobservability "github.com/kombifyio/techstack/internal/gocommon/observability"

	"github.com/kombifyio/techstack/pkg/fulllocal"
	"github.com/kombifyio/techstack/pkg/logger"
)

// initErrorReporting starts Sentry error reporting. Under Full Local it starts
// nothing, even when a DSN in the homelab is set; the Full Local build does not
// contain the Sentry SDK at all.
func initErrorReporting(cfg commonobservability.Config) func() {
	if fulllocal.Active() {
		logger.Default().Info("error_reporting_disabled", "reason", "data profile full-local")
		return func() {}
	}
	return commonobservability.MustInit(cfg)
}
