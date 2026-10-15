package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	productnotifications "github.com/kombifyio/techstack/internal/notifications"
	"github.com/kombifyio/techstack/pkg/drivehealth"
	"github.com/kombifyio/techstack/pkg/guardtelemetry"
	"github.com/kombifyio/techstack/pkg/monitoring"
	"github.com/kombifyio/techstack/pkg/ril/signals"
)

type driveProductOutbox struct {
	events   map[string]productnotifications.ProductEvent
	failOnce bool
}

func (o *driveProductOutbox) ResolveWorkerRecipient(_ context.Context, tenant, worker string) (string, error) {
	if tenant != "drive-tenant" || worker != "drive-worker" {
		return "", errors.New("recipient resolution lost authenticated scope")
	}
	return "auth0|drive-owner", nil
}

func (o *driveProductOutbox) Enqueue(_ context.Context, event productnotifications.ProductEvent) error {
	// Simulate an ambiguous acknowledgement after durable receiver acceptance.
	o.events[event.IdempotencyKey] = event
	if o.failOnce {
		o.failOnce = false
		return errors.New("receiver acknowledgement interrupted")
	}
	return nil
}

type driveSignalOutbox struct {
	signals map[string]signals.Observation
}

func (o *driveSignalOutbox) ResolveServerOwner(context.Context, string, string) (string, error) {
	return "auth0|drive-owner", nil
}

func (o *driveSignalOutbox) Emit(_ context.Context, observation signals.Observation) (signals.Record, bool, error) {
	o.signals[observation.DedupeKey] = observation
	return signals.Record{}, true, nil
}

// Simultaneous conditions on different drives must survive downstream dedupe,
// including a receiver acknowledgement failure and a later recovery transition.
func TestDriveAlertDeliveryKeepsDistinctHardwareIncidentsAcrossRetries(t *testing.T) {
	now := time.Now().UTC()
	store := monitoring.NewMemoryDriveHealthStore()
	service := &monitoring.DriveHealthService{Store: store, Now: func() time.Time { return now }}
	id := guardtelemetry.Identity{TenantID: "drive-tenant", ServerID: "drive-server", WorkerID: "drive-worker"}
	passed, selfTestFailed, temperature := false, false, 35.0
	reading := func(serial, path string) drivehealth.Reading {
		return drivehealth.Reading{DriveID: drivehealth.StableID("test", serial, ""), DevicePath: path,
			Model: "test", Serial: serial, Protocol: "ATA", Source: drivehealth.SourceSMART,
			ObservedAt: now, Status: drivehealth.Available, Missing: []string{},
			SmartPassed: &passed, SelfTestFailed: &selfTestFailed, TemperatureCelsius: &temperature}
	}
	drives := []drivehealth.Reading{reading("serial-a", "/dev/sda"), reading("serial-b", "/dev/sdb")}
	ingest := func(sequence int64) {
		t.Helper()
		for i := range drives {
			drives[i].ObservedAt = now
		}
		if err := service.Ingest(t.Context(), id, "drive-epoch", sequence,
			drivehealth.Collection{ObservedAt: now, Status: drivehealth.Available, Complete: true, Drives: drives}); err != nil {
			t.Fatal(err)
		}
	}
	product := &driveProductOutbox{events: map[string]productnotifications.ProductEvent{}, failOnce: true}
	ril := &driveSignalOutbox{signals: map[string]signals.Observation{}}
	notifier := monitoring.NewMultiNotifier(&productAlertNotifier{outbox: product}, &rilSignalAlertNotifier{outbox: ril})
	ingest(1)
	if err := store.Deliver(t.Context(), id.TenantID, notifier); err == nil {
		t.Fatal("ambiguous receiver acknowledgement was not retried")
	}
	if err := store.Deliver(t.Context(), id.TenantID, notifier); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, event := range product.events {
		labels := event.Payload["labels"].(map[string]string)
		if event.OrganizationID != id.TenantID || event.Auth0UserID != "auth0|drive-owner" || labels["evidence"] == "" || labels["recommended_step"] == "" {
			t.Fatal("notification lost ownership or actionable evidence")
		}
		if seen[labels["drive_id"]] {
			t.Fatal("retry produced another notification for the same hardware incident")
		}
		seen[labels["drive_id"]] = true
	}
	for _, drive := range drives {
		if !seen[drive.DriveID] {
			t.Fatalf("simultaneous condition for %s was suppressed", drive.DevicePath)
		}
		found := false
		for _, signal := range ril.signals {
			if signal.TenantID == id.TenantID && signal.ServerID == id.ServerID && strings.Contains(signal.RecommendedAction, drive.DriveID) {
				found = true
			}
		}
		if !found {
			t.Fatalf("RIL suppressed the simultaneous condition for %s", drive.DevicePath)
		}
	}
	passed = true
	now = now.Add(time.Minute)
	ingest(2)
	if err := store.Deliver(t.Context(), id.TenantID, notifier); err != nil {
		t.Fatal(err)
	}
	for _, drive := range drives {
		resolved := false
		for _, event := range product.events {
			labels := event.Payload["labels"].(map[string]string)
			if labels["drive_id"] == drive.DriveID && event.Payload["transition"] == "resolved" {
				resolved = true
			}
		}
		if !resolved {
			t.Fatalf("recovery for %s was suppressed by the firing notification", drive.DevicePath)
		}
	}
}
