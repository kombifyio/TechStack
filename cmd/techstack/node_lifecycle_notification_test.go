package main

import (
	"context"
	"database/sql"
	"testing"
	"time"

	productnotifications "github.com/kombifyio/techstack/internal/notifications"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/serverregistry"
)

type recordingNodeNotificationOutbox struct {
	events map[string]productnotifications.ProductEvent
}

func (o *recordingNodeNotificationOutbox) EnqueueTx(_ context.Context, _ *sql.Tx, event productnotifications.ProductEvent) error {
	if o.events == nil {
		o.events = map[string]productnotifications.ProductEvent{}
	}
	o.events[event.IdempotencyKey] = event
	return nil
}

func TestNodeLifecycleNotificationsFollowDurableConnectionTransitions(t *testing.T) {
	transitionAt := time.Date(2026, 8, 31, 10, 0, 0, 0, time.UTC)
	outbox := &recordingNodeNotificationOutbox{}
	projector := nodeLifecycleNotificationProjector{outbox: outbox}
	server := &controlplane.ServerRuntime{
		ID: "server-1", TenantID: "tenant-1", StackID: "techstack-1",
		OwnerSubjectID: "auth0|owner-1", NodeID: "node-1", Name: "Storage Node",
		LifecycleState: string(serverregistry.LifecycleActive),
		DesiredState:   string(serverregistry.DesiredRunning),
	}
	stale := &controlplane.ServerEventResult{
		Server: server,
		Transitions: []controlplane.ServerStateTransition{{
			ID: 39, Dimension: "connection", FromState: "connected", ToState: "stale", ReasonCode: serverregistry.ReasonHeartbeatStale, ObservedAt: transitionAt.Add(-4 * time.Minute),
		}},
		Applied: true,
	}
	if err := projector.ProjectServerEvent(context.Background(), nil, stale); err != nil {
		t.Fatalf("project stale transition: %v", err)
	}
	if len(outbox.events) != 0 {
		t.Fatalf("stale transition was reported as an outage: %#v", outbox.events)
	}
	outage := &controlplane.ServerEventResult{
		Server: server,
		Transitions: []controlplane.ServerStateTransition{
			{ID: 40, Dimension: "health", FromState: "healthy", ToState: "unknown", ReasonCode: serverregistry.ReasonHeartbeatExpired, ObservedAt: transitionAt},
			{ID: 41, Dimension: "connection", FromState: "stale", ToState: "offline", ReasonCode: serverregistry.ReasonHeartbeatExpired, ObservedAt: transitionAt},
		},
		Applied: true,
	}
	if err := projector.ProjectServerEvent(context.Background(), nil, outage); err != nil {
		t.Fatalf("project outage: %v", err)
	}
	// A retry of the same committed transition must reuse the same keys.
	if err := projector.ProjectServerEvent(context.Background(), nil, outage); err != nil {
		t.Fatalf("reproject outage: %v", err)
	}
	assertNodeNotification(t, outbox.events, "node.unreachable", "system.server-down", "high", "node-1", "/monitoring/server-1", transitionAt)
	if len(outbox.events) != 2 {
		t.Fatalf("outage dispatches = %d, want one idempotent in_app and push request", len(outbox.events))
	}

	recoveredAt := transitionAt.Add(2 * time.Minute)
	recovery := &controlplane.ServerEventResult{
		Server: server,
		Transitions: []controlplane.ServerStateTransition{{
			ID: 42, Dimension: "connection", FromState: "offline", ToState: "connected", ObservedAt: recoveredAt,
		}},
		Applied: true,
	}
	if err := projector.ProjectServerEvent(context.Background(), nil, recovery); err != nil {
		t.Fatalf("project recovery: %v", err)
	}
	assertNodeNotification(t, outbox.events, "node.recovered", "system.fix-applied", "normal", "node-1", "/monitoring/server-1", recoveredAt)
	if len(outbox.events) != 4 {
		t.Fatalf("outage and recovery dispatches = %d, want two channels per durable transition", len(outbox.events))
	}

	// Service and health degradation have their own transition/alert path and
	// must never be promoted to a node outage while connectivity remains up.
	healthOnly := &controlplane.ServerEventResult{
		Server: server,
		Transitions: []controlplane.ServerStateTransition{{
			ID: 43, Dimension: "health", FromState: "healthy", ToState: "degraded", ObservedAt: recoveredAt,
		}},
		Applied: true,
	}
	if err := projector.ProjectServerEvent(context.Background(), nil, healthOnly); err != nil {
		t.Fatalf("project health-only degradation: %v", err)
	}
	if len(outbox.events) != 4 {
		t.Fatalf("health-only degradation emitted a node outage: %#v", outbox.events)
	}
}

func assertNodeNotification(t *testing.T, events map[string]productnotifications.ProductEvent, eventKey, topic, priority, nodeID, deepLink string, occurredAt time.Time) {
	t.Helper()
	channels := map[string]bool{}
	for _, event := range events {
		if event.EventKey != eventKey {
			continue
		}
		channels[event.Channel] = true
		if event.Topic != topic || event.SourceApp != "techstack" || event.Priority != priority {
			t.Fatalf("%s envelope = %#v", eventKey, event)
		}
		if event.SubjectRef != "node:"+nodeID || event.DeepLink != deepLink || event.Payload["node_id"] != nodeID || event.Payload["techstack_id"] != "techstack-1" {
			t.Fatalf("%s context = %#v", eventKey, event)
		}
		if event.Payload["occurred_at"] != occurredAt.UTC().Format(time.RFC3339Nano) {
			t.Fatalf("%s transition time = %#v", eventKey, event.Payload["occurred_at"])
		}
	}
	if !channels["in_app"] || !channels["push"] || len(channels) != 2 {
		t.Fatalf("%s channels = %#v, want in_app and push", eventKey, channels)
	}
}
