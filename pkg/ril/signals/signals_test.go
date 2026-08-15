package signals

import (
	"errors"
	"testing"
	"time"
)

func TestPriorityClassificationIsCentralAndBounded(t *testing.T) {
	tests := []struct {
		severity Severity
		priority Priority
	}{
		{SeverityInfo, PriorityLow}, {SeverityLow, PriorityLow},
		{SeverityMedium, PriorityNormal}, {SeverityHigh, PriorityHigh},
		{SeverityCritical, PriorityUrgent},
	}
	for _, test := range tests {
		got, err := ClassifyPriority(test.severity)
		if err != nil || got != test.priority {
			t.Fatalf("ClassifyPriority(%q) = %q, %v; want %q", test.severity, got, err, test.priority)
		}
	}
	if _, err := ClassifyPriority("operator-selected"); !errors.Is(err, ErrInvalidSignal) {
		t.Fatalf("invalid severity error = %v", err)
	}
}

func TestNormalizeObservationBuildsReplayStableAgentsWire(t *testing.T) {
	now := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)
	input := Observation{
		DedupeKey: "health:server-1:disk", TenantID: "tenant-1", UserID: "auth0|user-1",
		ServerID: "server-1", Source: SourceResource, Severity: SeverityCritical,
		RecommendedAction: "Inspect disk pressure", TraceID: "trace-1", AuditID: "audit-1", ReceivedAt: now,
		Connector: &ConnectorRequirement{ConnectorID: "kombify-tools", ConnectorGrantID: "grant-1", RequiredScopes: []string{"mcp:tools:read"}, BindingScope: "signal"},
	}
	first, envelope, err := normalizeObservation(input)
	if err != nil {
		t.Fatal(err)
	}
	second, replay, err := normalizeObservation(input)
	if err != nil {
		t.Fatal(err)
	}
	if first.SignalID != second.SignalID || envelope.SignalID != replay.SignalID {
		t.Fatalf("signal id is not deterministic: %q vs %q", first.SignalID, second.SignalID)
	}
	if envelope.TenantID != "tenant-1" || envelope.ServerID != "server-1" || envelope.Priority != PriorityUrgent ||
		envelope.TraceID != "trace-1" || envelope.AuditID != "audit-1" || envelope.ConnectorGrantID != "grant-1" ||
		envelope.ActionCardID != "ril-action-card:"+envelope.SignalID {
		t.Fatalf("envelope lost identity or authority fields: %+v", envelope)
	}
}

func TestNormalizeObservationAcceptsCanonicalAuth0SubjectTenant(t *testing.T) {
	input := Observation{
		DedupeKey: "health:server-1:offline", TenantID: "usr:auth0|tenant-subject",
		UserID: "auth0|tenant-subject", ServerID: "server-1", Source: SourceHealth,
		Severity: SeverityHigh, TraceID: "trace-1", AuditID: "audit-1",
		ReceivedAt: time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC),
	}
	_, envelope, err := normalizeObservation(input)
	if err != nil {
		t.Fatal(err)
	}
	if envelope.TenantID != input.TenantID || envelope.UserID != input.UserID {
		t.Fatalf("identity drift: tenant=%q user=%q", envelope.TenantID, envelope.UserID)
	}
	if stableID(input.TenantID) {
		t.Fatal("tenant-only Auth0 subject syntax must not widen generic stable IDs")
	}
}

func TestRetryDelayIsExponentialAndCapped(t *testing.T) {
	if got := RetryDelay(1); got != 2*time.Second {
		t.Fatalf("first retry = %s", got)
	}
	if got := RetryDelay(4); got != 16*time.Second {
		t.Fatalf("fourth retry = %s", got)
	}
	if got := RetryDelay(99); got != 5*time.Minute {
		t.Fatalf("capped retry = %s", got)
	}
}

func TestConnectorSignalRequiresBoundedDelegatedGrantContext(t *testing.T) {
	input := Observation{
		DedupeKey: "connector:server-1", TenantID: "tenant-1", ServerID: "server-1",
		Source: SourceConnector, Severity: SeverityMedium, TraceID: "trace-1", AuditID: "audit-1",
		ReceivedAt: time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC),
	}
	if _, _, err := normalizeObservation(input); !errors.Is(err, ErrInvalidSignal) {
		t.Fatalf("connector signal without grant context error = %v", err)
	}
}
