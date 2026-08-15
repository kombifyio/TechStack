package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"time"

	productnotifications "github.com/kombifyio/techstack/internal/notifications"
	"github.com/kombifyio/techstack/pkg/logger"
	"github.com/kombifyio/techstack/pkg/monitoring"
	"github.com/kombifyio/techstack/pkg/ril/signals"
)

type productAlertNotifier struct {
	outbox *productnotifications.Outbox
}

const notificationSourceApp = "techstack"

type rilAlertOutbox interface {
	ResolveServerOwner(context.Context, string, string) (string, error)
	Emit(context.Context, signals.Observation) (signals.Record, bool, error)
}

type rilSignalAlertNotifier struct{ outbox rilAlertOutbox }

func (n *rilSignalAlertNotifier) Name() string { return "ril-signal-outbox" }

func (n *rilSignalAlertNotifier) Notify(ctx context.Context, alert monitoring.Alert) error {
	if n == nil || n.outbox == nil || alert.ResolvedAt != nil || strings.EqualFold(alert.Severity, "resolved") {
		return nil
	}
	tenantID := firstNotificationLabel(alert.Labels, "organization_id", "tenant_id")
	serverID := firstNotificationLabel(alert.Labels, "server_id", "runtime_server_id")
	if tenantID == "" || serverID == "" {
		return nil
	}
	ownerID := firstNotificationLabel(alert.Labels, "auth0_user_id", "owner_subject_id", "owner_id")
	if ownerID == "" {
		var err error
		ownerID, err = n.outbox.ResolveServerOwner(ctx, tenantID, serverID)
		if err != nil {
			return err
		}
	}
	severity := signals.SeverityHigh
	switch strings.ToLower(strings.TrimSpace(alert.Severity)) {
	case "critical":
		severity = signals.SeverityCritical
	case "info":
		severity = signals.SeverityLow
	}
	source := signals.SourceHealth
	if strings.Contains(strings.ToLower(alert.RuleName), "cpu") ||
		strings.Contains(strings.ToLower(alert.RuleName), "memory") ||
		strings.Contains(strings.ToLower(alert.RuleName), "disk") {
		source = signals.SourceResource
	}
	firedAt := alert.FiredAt.UTC()
	identity := stableAlertIdentity(tenantID, serverID, alert.RuleName, firedAt.Format(time.RFC3339Nano))
	_, _, err := n.outbox.Emit(ctx, signals.Observation{
		DedupeKey: "monitor:" + identity, TenantID: tenantID, UserID: ownerID, ServerID: serverID,
		Source: source, Severity: severity, RecommendedAction: alert.Message,
		TraceID: "ril-monitor-trace:" + identity, AuditID: "ril-monitor-audit:" + identity,
		ReceivedAt: firedAt,
	})
	return err
}

func stableAlertIdentity(parts ...string) string {
	digest := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(digest[:16])
}

func (n *productAlertNotifier) Name() string { return "kombify-notifications-outbox" }

func (n *productAlertNotifier) Notify(ctx context.Context, alert monitoring.Alert) error {
	recipient := firstNotificationLabel(alert.Labels, "auth0_user_id", "owner_subject_id", "owner_id")
	organizationID := firstNotificationLabel(alert.Labels, "organization_id", "tenant_id")
	if recipient == "" {
		var err error
		recipient, err = n.outbox.ResolveWorkerRecipient(ctx, organizationID, firstNotificationLabel(alert.Labels, "agent_id"))
		if err != nil {
			return err
		}
	}
	topic := "system.service-degraded"
	transition := "fired"
	occurredAt := alert.FiredAt
	if alert.ResolvedAt != nil {
		topic = "system.fix-applied"
		transition = "resolved"
		occurredAt = *alert.ResolvedAt
	}
	key := fmt.Sprintf("techstack-monitor:%s:%s:%s:%d", organizationID, recipient, alert.RuleName, occurredAt.UTC().UnixNano())
	return n.outbox.Enqueue(ctx, productnotifications.ProductEvent{
		Topic: topic, Auth0UserID: recipient, OrganizationID: organizationID, IdempotencyKey: key,
		Payload: map[string]any{
			"subject":     alert.RuleName,
			"body":        alert.Message,
			"severity":    alert.Severity,
			"transition":  transition,
			"value":       alert.Value,
			"labels":      alert.Labels,
			"occurred_at": occurredAt.UTC().Format(time.RFC3339Nano),
			"source_app":  notificationSourceApp,
			"link_url":    "/monitoring",
		},
	})
}

func firstNotificationLabel(labels map[string]string, keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(labels[key]); value != "" {
			return value
		}
	}
	return ""
}

func bootProductNotificationOutbox(v2 *v2Boot, log *logger.Logger) *productnotifications.Outbox {
	if v2 == nil || v2.db == nil || v2.db.DB == nil {
		return nil
	}
	secret := strings.TrimSpace(os.Getenv("SERVICE_AUTH_SECRET"))
	if secret == "" {
		log.Warn("notification_outbox_disabled", "reason", "SERVICE_AUTH_SECRET missing")
		return nil
	}
	client := productnotifications.NewEngineFromEnv()
	return productnotifications.NewOutbox(v2.db.DB, client, log.Logger)
}
