package main

import (
	"github.com/kombifyio/techstack/internal/notifications"
	"github.com/kombifyio/techstack/pkg/fulllocal"
	"github.com/kombifyio/techstack/pkg/logger"
	"github.com/kombifyio/techstack/pkg/stackrouting"
)

// bootDomainMonitor returns nil under Full Local: the monitor observes public
// DNS, registration and TLS endpoints, which a homelab that sends nothing out
// has no use for.
func bootDomainMonitor(boot *v2Boot, outbox *notifications.Outbox, log *logger.Logger) *stackrouting.DomainMonitor {
	if fulllocal.Active() {
		return nil
	}
	store := controlPlanePostgresStore(boot)
	if store == nil {
		return nil
	}
	var notifier notifications.ProductEventTxEnqueuer
	if outbox != nil {
		notifier = outbox
	}
	return &stackrouting.DomainMonitor{
		Directory: store, Stacks: store, Servers: store, Services: store,
		Routing:   stackrouting.NewPostgresStore(boot.db.DB),
		Evidence:  stackrouting.NewDomainMonitoringPostgresStore(boot.db.DB, notifier),
		Collector: stackrouting.NewDomainCollector(),
		OnError: func(err error) {
			if log != nil {
				log.Warn("domain_monitoring_pass_failed", "error", err)
			}
		},
	}
}
