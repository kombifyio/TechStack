package main

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"strings"
	"time"

	productnotifications "github.com/kombifyio/techstack/internal/notifications"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/serverregistry"
)

const (
	nodeUnreachableEventKey = "node.unreachable"
	nodeRecoveredEventKey   = "node.recovered"
)

type productNotificationTxEnqueuer interface {
	EnqueueTx(context.Context, *sql.Tx, productnotifications.ProductEvent) error
}

// nodeLifecycleNotificationProjector translates only committed connection
// transitions. Liveness remains owned by the Guard heartbeat and registry
// sweeper; this projector neither polls nor derives a second health state.
type nodeLifecycleNotificationProjector struct {
	outbox productNotificationTxEnqueuer
}

func (p nodeLifecycleNotificationProjector) ProjectServerEvent(ctx context.Context, tx *sql.Tx, result *controlplane.ServerEventResult) error {
	if p.outbox == nil || result == nil || !result.Applied || result.Server == nil {
		return nil
	}
	server := result.Server
	if strings.TrimSpace(server.OwnerSubjectID) == "" || strings.TrimSpace(server.NodeID) == "" ||
		strings.TrimSpace(server.StackID) == "" || strings.TrimSpace(server.ID) == "" ||
		server.DesiredState == string(serverregistry.DesiredAbsent) ||
		server.LifecycleState == string(serverregistry.LifecycleDecommissioning) ||
		server.LifecycleState == string(serverregistry.LifecycleDecommissioned) {
		return nil
	}
	for _, transition := range result.Transitions {
		event, ok := nodeLifecycleProductEvent(*server, transition)
		if !ok {
			continue
		}
		for _, channel := range []string{"in_app", "push"} {
			channelEvent := event
			channelEvent.Channel = channel
			channelEvent.IdempotencyKey += ":" + channel
			if err := p.outbox.EnqueueTx(ctx, tx, channelEvent); err != nil {
				return fmt.Errorf("enqueue %s notification: %w", channelEvent.EventKey, err)
			}
		}
	}
	return nil
}

func nodeLifecycleProductEvent(server controlplane.ServerRuntime, transition controlplane.ServerStateTransition) (productnotifications.ProductEvent, bool) {
	if transition.ID <= 0 || transition.Dimension != "connection" {
		return productnotifications.ProductEvent{}, false
	}
	eventKey, topic, priority, severity, subject, body := "", "", "", "", "", ""
	name := strings.TrimSpace(server.Name)
	if name == "" {
		name = "Node"
	}
	switch {
	case transition.ToState == string(serverregistry.ConnectionOffline) && transition.ReasonCode == serverregistry.ReasonHeartbeatExpired:
		eventKey, topic, priority, severity = nodeUnreachableEventKey, "system.server-down", "high", "critical"
		subject = name + " is unreachable"
		body = "Techstack has not received a heartbeat from " + name + " for more than five minutes."
	case transition.FromState == string(serverregistry.ConnectionOffline) && transition.ToState == string(serverregistry.ConnectionConnected):
		eventKey, topic, priority, severity = nodeRecoveredEventKey, "system.fix-applied", "normal", "info"
		subject = name + " is back online"
		body = name + " is sending authenticated heartbeats to Techstack again."
	default:
		return productnotifications.ProductEvent{}, false
	}
	if transition.ObservedAt.IsZero() {
		return productnotifications.ProductEvent{}, false
	}
	occurredAt := transition.ObservedAt.UTC()
	deepLink := "/monitoring/" + url.PathEscape(strings.TrimSpace(server.ID))
	return productnotifications.ProductEvent{
		Topic:          topic,
		Auth0UserID:    strings.TrimSpace(server.OwnerSubjectID),
		OrganizationID: strings.TrimSpace(server.TenantID),
		IdempotencyKey: fmt.Sprintf("techstack:node:%s:transition:%d:%s", strings.TrimSpace(server.NodeID), transition.ID, eventKey),
		SourceApp:      notificationSourceApp,
		EventKey:       eventKey,
		SubjectRef:     "node:" + strings.TrimSpace(server.NodeID),
		DeepLink:       deepLink,
		GroupKey:       "techstack:node:" + strings.TrimSpace(server.NodeID) + ":connectivity",
		Priority:       priority,
		Payload: map[string]any{
			"subject":      subject,
			"body":         body,
			"severity":     severity,
			"transition":   transition.FromState + ":" + transition.ToState,
			"reason_code":  transition.ReasonCode,
			"node_id":      strings.TrimSpace(server.NodeID),
			"techstack_id": strings.TrimSpace(server.StackID),
			"server_id":    strings.TrimSpace(server.ID),
			"occurred_at":  occurredAt.Format(time.RFC3339Nano),
			"source_app":   notificationSourceApp,
			"event_key":    eventKey,
			"link_url":     deepLink,
		},
	}, true
}
