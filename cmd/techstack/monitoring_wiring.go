package main

import (
	"fmt"
	"os"

	productnotifications "github.com/kombifyio/techstack/internal/notifications"
	"github.com/kombifyio/techstack/internal/routes"
	"github.com/kombifyio/techstack/pkg/config"
	"github.com/kombifyio/techstack/pkg/grpcserver"
	"github.com/kombifyio/techstack/pkg/logger"
	"github.com/kombifyio/techstack/pkg/monitoring"
	"github.com/kombifyio/techstack/pkg/ril/signals"
)

func bootMonitoring(cfg *config.Config, grpcSrv *grpcserver.Server, log *logger.Logger, notificationOutbox *productnotifications.Outbox, rilSignalOutbox *signals.PostgresOutbox) *monitoringBoot {
	boot := &monitoringBoot{
		dataDir: cfg.Monitoring.TSDBDir,
		status: routes.MonitoringStatusMetadata{
			QueryBackend:       "embedded-tsdb",
			IngestBackend:      "embedded-tsdb",
			CollectorMode:      cfg.Monitoring.CollectorMode,
			CompatibilityMode:  "dual-ingest",
			IngestFreshnessTTL: cfg.Monitoring.IngestFreshnessTTLDuration(),
			OTLPRequirement:    cfg.Monitoring.OTLPLaneRequirement,
			LegacyRequirement:  cfg.Monitoring.LegacyPushLaneRequirement,
		},
	}
	boot.tsdb = newMonitorTSDB(cfg, grpcSrv, log)
	boot.queryBackend = monitoring.MetricsQueryBackend(boot.tsdb)
	configureRemoteMonitoringBackend(cfg, log, boot)
	if boot.tsdb == nil || grpcSrv == nil {
		boot.status.IngestBackend = "unavailable"
		boot.status.CompatibilityMode = "query-only"
	}
	if boot.queryBackend != nil {
		boot.notifyOutbox = notificationOutbox
		boot.alertEngine = monitoring.NewAlertEngine(boot.queryBackend, buildAlertNotifier(notificationOutbox, rilSignalOutbox), monitoring.DefaultAlertRules(), monitoring.AlertEngineConfig{
			Interval: cfg.Monitoring.AlertIntervalDuration(),
			Logger:   log.Logger,
		})
	}
	return boot
}

func newMonitorTSDB(cfg *config.Config, grpcSrv *grpcserver.Server, log *logger.Logger) *monitoring.MonitorTSDB {
	monitorTSDB, err := monitoring.NewMonitorTSDB(monitoring.TSDBConfig{
		DataDir: cfg.Monitoring.TSDBDir,
		Logger:  log.Logger,
	})
	if err != nil {
		fmt.Printf("  Monitoring:    TSDB init failed: %v\n", err)
		return nil
	}
	if grpcSrv != nil {
		grpcSrv.SetMonitorTSDB(monitorTSDB)
	}
	return monitorTSDB
}

func configureRemoteMonitoringBackend(cfg *config.Config, log *logger.Logger, boot *monitoringBoot) {
	if cfg.Monitoring.QueryBackendURL == "" {
		return
	}
	remote, err := monitoring.NewHTTPPromQLBackend(monitoring.HTTPPromQLBackendConfig{
		BaseURL: cfg.Monitoring.QueryBackendURL,
		Logger:  log.Logger,
	})
	if err != nil {
		log.Error("monitor_query_backend_invalid", "url", cfg.Monitoring.QueryBackendURL, "error", err)
		return
	}
	boot.remote = remote
	boot.queryBackend = remote
	boot.status.QueryBackend = "remote-promql"
	boot.status.QueryBackendURL = remote.RedactedBaseURL()
	log.Info("monitor_query_backend_remote", "url", remote.RedactedBaseURL())
}

func buildAlertNotifier(notificationOutbox *productnotifications.Outbox, rilSignalOutbox *signals.PostgresOutbox) monitoring.Notifier {
	var notifiers []monitoring.Notifier
	if notificationOutbox != nil {
		notifiers = append(notifiers, &productAlertNotifier{outbox: notificationOutbox})
	}
	if rilSignalOutbox != nil {
		notifiers = append(notifiers, &rilSignalAlertNotifier{outbox: rilSignalOutbox})
	}
	if tgToken := os.Getenv("TECHSTACK_ALERT_TELEGRAM_TOKEN"); tgToken != "" {
		if chatID := os.Getenv("TECHSTACK_ALERT_TELEGRAM_CHAT_ID"); chatID != "" {
			notifiers = append(notifiers, monitoring.NewTelegramNotifier(tgToken, chatID))
		}
	}
	if webhookURL := os.Getenv("TECHSTACK_ALERT_WEBHOOK_URL"); webhookURL != "" {
		notifiers = append(notifiers, monitoring.NewWebhookNotifier(webhookURL))
	}
	if len(notifiers) == 0 {
		return nil
	}
	return monitoring.NewMultiNotifier(notifiers...)
}
