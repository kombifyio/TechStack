package main

import (
	"context"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/monitoring"
	"github.com/kombifyio/techstack/pkg/ril/signals"
)

type fakeRILAlertOutbox struct {
	owner       string
	observation signals.Observation
	emitCalls   int
}

func (f *fakeRILAlertOutbox) ResolveServerOwner(context.Context, string, string) (string, error) {
	return f.owner, nil
}

func (f *fakeRILAlertOutbox) Emit(_ context.Context, observation signals.Observation) (signals.Record, bool, error) {
	f.emitCalls++
	f.observation = observation
	return signals.Record{}, true, nil
}

func TestRILSignalAlertNotifierEmitsTenantScopedResourceSignal(t *testing.T) {
	firedAt := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)
	outbox := &fakeRILAlertOutbox{owner: "auth0|owner-1"}
	notifier := &rilSignalAlertNotifier{outbox: outbox}
	alert := monitoring.Alert{
		RuleName: "DiskFull", Severity: "critical", Message: "disk pressure", FiredAt: firedAt,
		Labels: map[string]string{"tenant_id": "tenant-1", "server_id": "server-1"},
	}
	if err := notifier.Notify(t.Context(), alert); err != nil {
		t.Fatal(err)
	}
	got := outbox.observation
	if outbox.emitCalls != 1 || got.TenantID != "tenant-1" || got.UserID != "auth0|owner-1" ||
		got.ServerID != "server-1" || got.Source != signals.SourceResource || got.Severity != signals.SeverityCritical {
		t.Fatalf("observation = %#v, calls = %d", got, outbox.emitCalls)
	}
	if got.TraceID == "" || got.AuditID == "" || got.DedupeKey == "" || !got.ReceivedAt.Equal(firedAt) {
		t.Fatalf("correlation was not deterministic: %#v", got)
	}
	firstKey := got.DedupeKey
	if err := notifier.Notify(t.Context(), alert); err != nil {
		t.Fatal(err)
	}
	if outbox.observation.DedupeKey != firstKey {
		t.Fatalf("dedupe key changed: %q != %q", outbox.observation.DedupeKey, firstKey)
	}
}

func TestRILSignalAlertNotifierIgnoresUnscopedAndResolvedAlerts(t *testing.T) {
	outbox := &fakeRILAlertOutbox{owner: "auth0|owner-1"}
	notifier := &rilSignalAlertNotifier{outbox: outbox}
	if err := notifier.Notify(t.Context(), monitoring.Alert{RuleName: "HighCPU", Severity: "critical", FiredAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	resolvedAt := time.Now().UTC()
	if err := notifier.Notify(t.Context(), monitoring.Alert{
		RuleName: "HighCPU", Severity: "resolved", FiredAt: resolvedAt.Add(-time.Minute), ResolvedAt: &resolvedAt,
		Labels: map[string]string{"tenant_id": "tenant-1", "server_id": "server-1"},
	}); err != nil {
		t.Fatal(err)
	}
	if outbox.emitCalls != 0 {
		t.Fatalf("non-actionable alerts emitted %d signals", outbox.emitCalls)
	}
}
