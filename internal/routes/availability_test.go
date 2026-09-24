package routes

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/availability"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/serverregistry"
)

func availabilityFixture(t *testing.T) (*controlplane.MemoryStore, time.Time) {
	t.Helper()
	store := controlplane.NewMemoryStore()
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	seen := now.Add(-30 * time.Second)
	// The store stamps CreatedAt from its own clock, and enrolment trims the
	// observed window. Without pinning it the fixture would enrol both servers
	// after the window it is asked about and report no history at all.
	enrolled := now.Add(-90 * 24 * time.Hour)
	store.SetNow(func() time.Time { return enrolled })
	defer store.SetNow(nil)

	for _, server := range []struct {
		id, name, connection string
	}{
		{"server-up", "node-01.home", string(serverregistry.ConnectionConnected)},
		{"server-down", "pi-attic", string(serverregistry.ConnectionOffline)},
	} {
		if _, err := store.UpsertServerRuntime(t.Context(), controlplane.ServerRuntime{
			ID: server.id, TenantID: "tenant-1", StackID: "stack-1", OwnerSubjectID: "owner-1",
			Name: server.name, LifecycleState: string(serverregistry.LifecycleActive),
			ConnectionState: server.connection, HealthState: "healthy", LastHeartbeatAt: &seen,
		}); err != nil {
			t.Fatal(err)
		}
	}

	// One closed outage and one that is still open, so the response has to
	// distinguish them.
	for _, transition := range []controlplane.ServerStateTransition{
		{
			TenantID: "tenant-1", ServerID: "server-up", Dimension: "connection",
			FromState: "connected", ToState: "offline", ReasonCode: "power_loss",
			Source: "sweeper", ObservedAt: now.Add(-48 * time.Hour),
		},
		{
			TenantID: "tenant-1", ServerID: "server-up", Dimension: "connection",
			FromState: "offline", ToState: "connected", ReasonCode: "agent_reconnected",
			Source: "guard", ObservedAt: now.Add(-46 * time.Hour),
		},
		{
			TenantID: "tenant-1", ServerID: "server-down", Dimension: "connection",
			FromState: "connected", ToState: "offline", ReasonCode: "agent_channel_closed",
			Source: "sweeper", ObservedAt: now.Add(-10 * time.Hour),
		},
	} {
		if _, err := store.AppendServerTransition(t.Context(), transition); err != nil {
			t.Fatal(err)
		}
	}
	return store, now
}

func readAvailability(t *testing.T, store *controlplane.MemoryStore, now time.Time, query string) (int, availabilityResponse) {
	t.Helper()
	h := availabilityHandlers{store: store, transitions: store, now: func() time.Time { return now }}
	event, recorder := registryRouteStoreTestEvent(
		http.MethodGet, "/api/v1/monitor/availability"+query, "owner-1", "tenant-1", nil)
	if err := h.report(event); err != nil {
		t.Fatal(err)
	}
	var envelope struct {
		Data availabilityResponse `json:"data"`
	}
	if recorder.Code == http.StatusOK {
		if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
			t.Fatalf("decode %s: %v", recorder.Body.String(), err)
		}
	}
	return recorder.Code, envelope.Data
}

// TestAvailabilityReportSeparatesClosedAndOngoingOutages is the contract the
// start page depends on: an outage that has not recovered must be reported as
// ongoing rather than closed at the window edge.
func TestAvailabilityReportSeparatesClosedAndOngoingOutages(t *testing.T) {
	store, now := availabilityFixture(t)
	code, report := readAvailability(t, store, now, "?window=30d")
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}

	byServer := map[string]availability.ServerReport{}
	for _, server := range report.Servers {
		byServer[server.ServerID] = server
	}

	up := byServer["server-up"]
	if len(up.Episodes) != 1 || up.Episodes[0].Ongoing {
		t.Fatalf("recovered node = %#v, want one closed episode", up.Episodes)
	}
	if up.Episodes[0].RecoveryReason != "agent_reconnected" {
		t.Fatalf("recovery reason = %q", up.Episodes[0].RecoveryReason)
	}

	down := byServer["server-down"]
	if len(down.Episodes) != 1 || !down.Episodes[0].Ongoing {
		t.Fatalf("offline node = %#v, want one ongoing episode", down.Episodes)
	}
	if down.Episodes[0].EndedAt != nil {
		t.Fatal("an ongoing outage was given an end time")
	}
	if down.Episodes[0].TriggerReason != "agent_channel_closed" {
		t.Fatalf("trigger = %q", down.Episodes[0].TriggerReason)
	}

	// MTTR must come from the closed episode alone.
	if report.ClosedEpisodes != 1 || report.MTTRSeconds == nil {
		t.Fatalf("mttr = %v over %d closed episodes", report.MTTRSeconds, report.ClosedEpisodes)
	}
	if *report.MTTRSeconds != int64((2 * time.Hour).Seconds()) {
		t.Fatalf("mttr = %d s, want 7200", *report.MTTRSeconds)
	}
}

// TestAvailabilityPublishesItsAccountingVocabulary keeps the number
// self-explaining: a reader can see which states were counted as down without
// reading this repository.
func TestAvailabilityPublishesItsAccountingVocabulary(t *testing.T) {
	store, now := availabilityFixture(t)
	_, report := readAvailability(t, store, now, "")

	if len(report.Accounting.Down) == 0 || len(report.Accounting.Up) == 0 {
		t.Fatalf("accounting = %#v", report.Accounting)
	}
	var staleIsDown bool
	for _, state := range report.Accounting.Down {
		if state == string(serverregistry.ConnectionStale) {
			staleIsDown = true
		}
	}
	if !staleIsDown {
		t.Fatal("stale must be published as downtime — it is the conservative reading the projection uses")
	}
	if report.Window != "30d" {
		t.Fatalf("default window = %q, want 30d", report.Window)
	}
}

// TestAvailabilityRejectsAnUnboundedWindow keeps the scan from becoming a
// denial-of-service knob.
func TestAvailabilityRejectsAnUnboundedWindow(t *testing.T) {
	store, now := availabilityFixture(t)
	if code, _ := readAvailability(t, store, now, "?window=10y"); code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for an unsupported window", code)
	}
}

// TestAvailabilityScopesToOneServer is what the server detail page calls.
func TestAvailabilityScopesToOneServer(t *testing.T) {
	store, now := availabilityFixture(t)
	code, report := readAvailability(t, store, now, "?window=7d&server_id=server-down")
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if len(report.Servers) != 1 || report.Servers[0].ServerID != "server-down" {
		t.Fatalf("servers = %#v, want only the requested one", report.Servers)
	}

	if code, _ := readAvailability(t, store, now, "?server_id=server-missing"); code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 for an unknown server", code)
	}
}

// TestAvailabilityOmitsTerminalTombstones keeps Daily state aligned with the
// current fleet: a decommissioned leftover must not keep a ribbon after the
// owner detached it, while a direct server_id read still serves history.
func TestAvailabilityOmitsTerminalTombstones(t *testing.T) {
	store, now := availabilityFixture(t)
	if _, err := store.UpsertServerRuntime(t.Context(), controlplane.ServerRuntime{
		ID: "homelab-8", TenantID: "tenant-1", OwnerSubjectID: "owner-1", Name: "homelab-8",
		LifecycleState: string(serverregistry.LifecycleDecommissioned), DesiredState: "absent",
	}); err != nil {
		t.Fatal(err)
	}

	code, report := readAvailability(t, store, now, "?window=30d")
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	for _, server := range report.Servers {
		if server.ServerID == "homelab-8" {
			t.Fatalf("tombstone remained in the fleet report: %#v", server)
		}
	}

	code, report = readAvailability(t, store, now, "?window=30d&server_id=homelab-8")
	if code != http.StatusOK {
		t.Fatalf("direct tombstone status = %d", code)
	}
	if len(report.Servers) != 1 || report.Servers[0].ServerID != "homelab-8" {
		t.Fatalf("direct tombstone read = %#v, want the requested history row", report.Servers)
	}
}

// TestAvailabilityDaysCoverTheWholeWindow keeps the ribbon from silently
// shortening: every day in the window gets a bucket, observed or not.
func TestAvailabilityDaysCoverTheWholeWindow(t *testing.T) {
	store, now := availabilityFixture(t)
	_, report := readAvailability(t, store, now, "?window=7d")

	for _, server := range report.Servers {
		if len(server.Days) < 7 {
			t.Fatalf("%s reported %d days for a 7-day window", server.ServerID, len(server.Days))
		}
	}
}
