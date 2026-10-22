package main

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	productnotifications "github.com/kombifyio/techstack/internal/notifications"
	"github.com/kombifyio/techstack/pkg/grpcserver"
	"github.com/kombifyio/techstack/pkg/guardbinding"
	"github.com/kombifyio/techstack/pkg/guardposture"
	"github.com/kombifyio/techstack/pkg/jobs"
	"github.com/kombifyio/techstack/pkg/logger"
	"github.com/kombifyio/techstack/pkg/orchestrator"
)

const (
	// guardPostureEnabledEnv is the kill switch of kombify Guard posture. It is
	// on by default: observing the host baseline changes nothing on a host.
	guardPostureEnabledEnv = "TECHSTACK_GUARD_POSTURE_ENABLED"

	guardPostureBackupMaxAgeEnv     = "TECHSTACK_GUARD_POSTURE_BACKUP_MAX_AGE"
	guardPostureDrillMaxAgeEnv      = "TECHSTACK_GUARD_POSTURE_RESTORE_DRILL_MAX_AGE"
	guardPostureVerifyIntervalEnv   = "TECHSTACK_GUARD_POSTURE_VERIFY_INTERVAL"
	guardPosturePendingAlertEnv     = "TECHSTACK_GUARD_POSTURE_PENDING_SECURITY_ALERT_AFTER"
	guardPostureRebootAlertEnv      = "TECHSTACK_GUARD_POSTURE_REBOOT_ALERT_AFTER"
	guardPostureRepairCooldownEnv   = "TECHSTACK_GUARD_REPAIR_CONTROL_COOLDOWN"
	guardPostureRepairMaxAttemptEnv = "TECHSTACK_GUARD_REPAIR_MAX_ATTEMPTS_PER_DAY"
)

// bootGuardPosture composes the kombify Guard posture service: the persisted
// host security, recovery and update observations, their findings and alerts,
// and the bounded repair. Every bound has a default; an environment value only
// tightens or relaxes it within what the service itself accepts.
func bootGuardPosture(ctx context.Context, v2 *v2Boot, orch *orchestrator.Orchestrator, outbox *productnotifications.Outbox, log *logger.Logger) *guardposture.Service {
	if !envBoolDefault(guardPostureEnabledEnv, true) || v2 == nil || v2.db == nil || v2.db.DB == nil {
		return nil
	}
	var sink guardposture.AlertSink
	if outbox != nil {
		sink = guardPostureAlertSink{outbox: outbox}
	}
	store, err := guardposture.NewPostgresStore(v2.db.DB, sink)
	if err != nil {
		log.Warn("guard_posture_disabled", "reason", err.Error())
		return nil
	}
	policy := guardposture.DefaultPolicy()
	policy.BackupMaxAge = durationEnv(guardPostureBackupMaxAgeEnv, policy.BackupMaxAge)
	policy.RestoreDrillMaxAge = durationEnv(guardPostureDrillMaxAgeEnv, policy.RestoreDrillMaxAge)
	policy.VerifyInterval = durationEnv(guardPostureVerifyIntervalEnv, policy.VerifyInterval)
	policy.PendingSecurityAlertAfter = durationEnv(guardPosturePendingAlertEnv, policy.PendingSecurityAlertAfter)
	policy.RebootAlertAfter = durationEnv(guardPostureRebootAlertEnv, policy.RebootAlertAfter)
	policy.Repair.ControlCooldown = durationEnv(guardPostureRepairCooldownEnv, policy.Repair.ControlCooldown)
	if attempts, parseErr := strconv.Atoi(strings.TrimSpace(os.Getenv(guardPostureRepairMaxAttemptEnv))); parseErr == nil && attempts > 0 {
		policy.Repair.MaxAttempts = attempts
	}
	cfg := guardposture.ServiceConfig{Store: store, Policy: policy, Logger: log.Logger}
	// The StackKit commander is installed by route wiring after this boot step,
	// so the dispatcher resolves it per request. Until then repair reports
	// unavailable; observation and alerts run either way.
	if dispatcher := guardPostureRepairDispatcher(orch); dispatcher != nil {
		cfg.Dispatcher = dispatcher
	}
	// A Guard's local repair runs on the release a managed Guard is admitted
	// to run; without one no local repair grant is issued.
	cfg.LocalRelease = guardLocalRepairRelease
	service, err := guardposture.NewService(cfg)
	if err != nil {
		log.Warn("guard_posture_disabled", "reason", err.Error())
		return nil
	}
	go func() {
		<-ctx.Done()
		service.Close()
	}()
	log.Info("guard_posture_ready", "notifications", outbox != nil, "repair_dispatch", cfg.Dispatcher != nil, "repair_available_at_boot", service.RepairAvailable())
	return service
}

// guardPostureRepairDispatcher binds repair to the orchestrator's current
// StackKit commander, resolved on every use. It returns nil without an
// orchestrator.
func guardPostureRepairDispatcher(orch *orchestrator.Orchestrator) guardposture.RepairDispatcher {
	if orch == nil {
		return nil
	}
	return jobs.NewLazyHostSecurityRepairDispatcher(orch.StackKitCommander)
}

// guardLocalRepairRelease is the admitted managed-Guard StackKits release as a
// local repair grant names it.
func guardLocalRepairRelease() (*guardbinding.LocalReleasePin, error) {
	release, err := jobs.CachedTargetStackKitRelease()
	if err != nil || release == nil {
		return nil, err
	}
	pin := grpcserver.StackKitReleasePinFor(*release)
	return &guardbinding.LocalReleasePin{
		Version: pin.GetVersion(), PlatformOS: pin.GetPlatformOs(), PlatformArch: pin.GetPlatformArch(),
		ArchiveSHA256: pin.GetArchiveSha256(), ReleaseIndexSHA256: pin.GetReleaseIndexSha256(),
	}, nil
}

func durationEnv(name string, fallback time.Duration) time.Duration {
	if value, err := time.ParseDuration(strings.TrimSpace(os.Getenv(name))); err == nil && value > 0 {
		return value
	}
	return fallback
}

// guardPostureAlertSink turns a finding's opening or resolution into a product
// notification inside the transaction that recorded it, so a finding and its
// alert commit together or not at all. One episode has one opening and at most
// one resolution, and the idempotency key names both.
type guardPostureAlertSink struct {
	outbox productNotificationTxEnqueuer
}

func (s guardPostureAlertSink) EnqueueAlert(ctx context.Context, tx *sql.Tx, alert guardposture.AlertEvent) error {
	if s.outbox == nil || tx == nil {
		return nil
	}
	if strings.TrimSpace(alert.OwnerSubjectID) == "" {
		return nil
	}
	transition, topic, priority := "opened", "system.service-degraded", "normal"
	if alert.Resolved {
		transition, topic = "resolved", "system.fix-applied"
	} else if alert.Severity == "critical" || alert.Severity == "high" {
		priority = "high"
	}
	payloadSeverity := "info"
	switch alert.Severity {
	case "critical":
		payloadSeverity = "critical"
	case "high", "medium":
		payloadSeverity = "warning"
	}
	if alert.Resolved {
		payloadSeverity = "info"
	}
	deepLink := "/monitoring/" + url.PathEscape(strings.TrimSpace(alert.ServerID))
	base := productnotifications.ProductEvent{
		Topic: topic, Auth0UserID: strings.TrimSpace(alert.OwnerSubjectID), OrganizationID: strings.TrimSpace(alert.TenantID),
		SourceApp: notificationSourceApp, EventKey: alert.EventKey, SubjectRef: "server:" + strings.TrimSpace(alert.ServerID),
		DeepLink: deepLink, GroupKey: "techstack:guard:" + strings.TrimSpace(alert.WorkerID) + ":" + alert.Key, Priority: priority,
		Payload: map[string]any{
			"subject": alert.Subject, "body": alert.Body, "severity": payloadSeverity, "transition": transition,
			"reason_code": alert.ReasonCode, "finding_key": alert.Key, "server_id": strings.TrimSpace(alert.ServerID),
			"occurred_at": alert.OccurredAt.UTC().Format(time.RFC3339Nano), "source_app": notificationSourceApp,
			"event_key": alert.EventKey, "link_url": deepLink,
		},
	}
	for _, channel := range []string{"in_app", "push"} {
		event := base
		event.Channel = channel
		event.IdempotencyKey = fmt.Sprintf("techstack:guard-posture:%s:%s:%s", alert.FindingID, transition, channel)
		if err := s.outbox.EnqueueTx(ctx, tx, event); err != nil {
			return fmt.Errorf("enqueue %s notification: %w", alert.EventKey, err)
		}
	}
	return nil
}
