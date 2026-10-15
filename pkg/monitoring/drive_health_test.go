package monitoring_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/drivehealth"
	"github.com/kombifyio/techstack/pkg/guardtelemetry"
	"github.com/kombifyio/techstack/pkg/monitoring"
)

func ptr[T any](value T) *T { return &value }
func driveFixture(now time.Time, serial string) drivehealth.Reading {
	return drivehealth.Reading{DriveID: drivehealth.StableID("test", serial, ""), DevicePath: "/dev/sda", Model: "test", Serial: serial, Protocol: "ATA", Source: drivehealth.SourceSMART, ObservedAt: now, Status: drivehealth.Available, Missing: []string{}, TemperatureCelsius: ptr(35.0), SmartPassed: ptr(true), AttributeFailed: ptr(false), SelfTestFailed: ptr(false), ReallocatedSectors: ptr("0"), PendingSectors: ptr("0"), UncorrectableSectors: ptr("0")}
}
func openAlert(alerts []monitoring.DriveAlert, code string) *monitoring.DriveAlert {
	for i := range alerts {
		if alerts[i].Code == code && alerts[i].ResolvedAt == nil {
			return &alerts[i]
		}
	}
	return nil
}
func driveByID(t *testing.T, response monitoring.DriveHealthResponse, id string) monitoring.DriveState {
	t.Helper()
	for _, s := range response.Servers {
		for _, d := range s.Drives {
			if d.DriveID == id {
				return d
			}
		}
	}
	t.Fatalf("drive %s unavailable in response", id)
	return monitoring.DriveState{}
}

// Hardware continuity, monotonic counters, and missing evidence form one
// lifecycle invariant: a location or a collection failure never supplies health.
func TestDriveHealthLifecyclePreservesEvidenceAndRecoversOnlyFromObservations(t *testing.T) {
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Second)
	store := monitoring.NewMemoryDriveHealthStore()
	service := &monitoring.DriveHealthService{Store: store, Now: func() time.Time { return now }, StaleAfter: time.Hour}
	identity := guardtelemetry.Identity{TenantID: "tenant-a", ServerID: "server", WorkerID: "guard"}
	sequence := int64(0)
	ingest := func(status string, complete bool, readings ...drivehealth.Reading) {
		t.Helper()
		sequence++
		for i := range readings {
			readings[i].ObservedAt = now
		}
		if err := service.Ingest(ctx, identity, "epoch", sequence, drivehealth.Collection{ObservedAt: now, Status: status, Complete: complete, Drives: readings}); err != nil {
			t.Fatal(err)
		}
	}
	read := func() monitoring.DriveHealthResponse {
		t.Helper()
		response, err := service.Read(ctx, identity.TenantID, identity.ServerID)
		if err != nil {
			t.Fatal(err)
		}
		return response
	}
	missing := read()
	if missing.Servers[0].CollectionStatus != "missing" {
		t.Fatal("an unobserved server was not missing")
	}
	original := driveFixture(now, "serial-1")
	ingest(drivehealth.Available, true, original)
	if d := driveByID(t, read(), original.DriveID); d.Health != "healthy" || d.LastSuccess == nil {
		t.Fatal("fresh healthy evidence was not projected")
	}
	now = now.Add(time.Minute)
	original.ReallocatedSectors = ptr("5")
	ingest(drivehealth.Available, true, original)
	historical := driveByID(t, read(), original.DriveID)
	history := openAlert(historical.Alerts, "reallocated_sectors")
	if history == nil || history.Evidence["interpretation"] != "historical_total" || openAlert(historical.Alerts, "reallocated_sectors_increase") == nil {
		t.Fatal("an observed counter increase was not distinguished from historical totals")
	}
	episode := history.ID
	if err := service.Ingest(ctx, identity, "epoch", sequence, drivehealth.Collection{ObservedAt: now, Status: drivehealth.Available, Complete: true, Drives: []drivehealth.Reading{original}}); err != nil {
		t.Fatal(err)
	}
	repeated := driveByID(t, read(), original.DriveID)
	if openAlert(repeated.Alerts, "reallocated_sectors").ID != episode {
		t.Fatal("replay changed the active condition episode")
	}
	now = now.Add(time.Minute)
	partial := original
	partial.Status = drivehealth.Incomplete
	partial.ReallocatedSectors = nil
	partial.Missing = []string{"reallocated_sectors"}
	ingest(drivehealth.Available, true, partial)
	d := driveByID(t, read(), original.DriveID)
	if openAlert(d.Alerts, "reallocated_sectors_increase") == nil || d.LastSuccess.ReallocatedSectors == nil {
		t.Fatal("missing evidence erased hardware alert or prior success")
	}
	now = now.Add(time.Minute)
	ingest(drivehealth.Inaccessible, false)
	d = driveByID(t, read(), original.DriveID)
	if d.ObservationStatus == "disconnected" || d.Health != "unknown" || d.ObservationStatus != drivehealth.Inaccessible || openAlert(d.Alerts, "reallocated_sectors") == nil {
		t.Fatal("failed inventory inferred hardware removal or recovery")
	}
	now = now.Add(2 * time.Hour)
	d = driveByID(t, read(), original.DriveID)
	if d.Health != "unknown" || d.ObservationStatus != "stale" || d.LastSuccess == nil {
		t.Fatal("stale evidence retained current healthy assessment or lost history")
	}
	now = now.Add(time.Minute)
	original.ReallocatedSectors = ptr("0")
	original.DevicePath = "/dev/sdb"
	ingest(drivehealth.Available, true, original)
	d = driveByID(t, read(), original.DriveID)
	if d.DevicePath != "/dev/sdb" || d.Health != "healthy" || openAlert(d.Alerts, "reallocated_sectors") != nil || openAlert(d.Alerts, "stale") != nil {
		t.Fatal("fresh same-identity recovery failed")
	}
	now = now.Add(time.Minute)
	replacement := driveFixture(now, "serial-2")
	replacement.DevicePath = "/dev/sdb"
	ingest(drivehealth.Available, true, replacement)
	old := driveByID(t, read(), original.DriveID)
	fresh := driveByID(t, read(), replacement.DriveID)
	if old.ObservationStatus != "disconnected" || fresh.Health != "healthy" || fresh.LastSuccess.Serial != "serial-2" || openAlert(fresh.Alerts, "reallocated_sectors") != nil {
		t.Fatal("replacement inherited old identity evidence")
	}
	// The collector may report a known missing identity explicitly before
	// dropping it from later inventories. That must preserve the same incident.
	disconnectedEpisode := openAlert(old.Alerts, "disconnected").ID
	missingReading := drivehealth.Reading{DriveID: original.DriveID, DevicePath: original.DevicePath, Model: original.Model, Serial: original.Serial, Protocol: original.Protocol, Source: drivehealth.SourceInventory, ObservedAt: now, Status: drivehealth.Missing, Reason: "device_missing", Missing: []string{}}
	now = now.Add(time.Minute)
	ingest(drivehealth.Available, true, missingReading, replacement)
	old = driveByID(t, read(), original.DriveID)
	if old.ObservationStatus != "disconnected" || openAlert(old.Alerts, "disconnected") == nil || openAlert(old.Alerts, "disconnected").ID != disconnectedEpisode {
		t.Fatal("explicit missing identity recovered or reopened its disconnected episode")
	}
	now = now.Add(time.Minute)
	ingest(drivehealth.Available, true, original, replacement)
	if old = driveByID(t, read(), original.DriveID); old.ObservationStatus != "available" || openAlert(old.Alerts, "disconnected") != nil {
		t.Fatal("reconnect did not recover missing-drive episode")
	}
	// Equal-time epoch turnover and an old epoch replay cannot change
	// already-accepted fresh hardware evidence.
	conflicting := original
	conflicting.SmartPassed = ptr(false)
	conflicting.ObservedAt = now
	replacement.ObservedAt = now
	if err := service.Ingest(ctx, identity, "other-epoch", 1, drivehealth.Collection{ObservedAt: now, Status: drivehealth.Available, Complete: true, Drives: []drivehealth.Reading{conflicting, replacement}}); err != nil {
		t.Fatal(err)
	}
	if driveByID(t, read(), original.DriveID).Health != "healthy" {
		t.Fatal("equal-time epoch turnover overwrote accepted evidence")
	}
	now = now.Add(time.Minute)
	original.ObservedAt = now
	replacement.ObservedAt = now
	if err := service.Ingest(ctx, identity, "other-epoch", 2, drivehealth.Collection{ObservedAt: now, Status: drivehealth.Available, Complete: true, Drives: []drivehealth.Reading{original, replacement}}); err != nil {
		t.Fatal(err)
	}
	conflicting.ObservedAt = now
	if err := service.Ingest(ctx, identity, "epoch", sequence+100, drivehealth.Collection{ObservedAt: now, Status: drivehealth.Available, Complete: true, Drives: []drivehealth.Reading{conflicting, replacement}}); err != nil {
		t.Fatal(err)
	}
	if driveByID(t, read(), original.DriveID).Health != "healthy" {
		t.Fatal("retired epoch replay overwrote accepted evidence")
	}
	foreign, err := service.Read(ctx, "tenant-b", identity.ServerID)
	if err != nil {
		t.Fatal(err)
	}
	if foreign.Servers[0].CollectionStatus != "missing" || len(foreign.Servers[0].Drives) != 0 {
		t.Fatal("another tenant reached drive evidence")
	}
}

type retryNotifier struct {
	fail      bool
	delivered []monitoring.Alert
}

func (n *retryNotifier) Name() string { return "probe" }
func (n *retryNotifier) Notify(_ context.Context, a monitoring.Alert) error {
	if n.fail {
		return errors.New("temporarily unavailable")
	}
	n.delivered = append(n.delivered, a)
	return nil
}
func TestDriveNotificationFailureRetriesWithoutReopeningTheCondition(t *testing.T) {
	ctx := t.Context()
	now := time.Now().UTC()
	store := monitoring.NewMemoryDriveHealthStore()
	service := &monitoring.DriveHealthService{Store: store, Now: func() time.Time { return now }}
	identity := guardtelemetry.Identity{TenantID: "tenant", ServerID: "server", WorkerID: "guard"}
	r := driveFixture(now, "bad")
	r.SmartPassed = ptr(false)
	if err := service.Ingest(ctx, identity, "epoch", 1, drivehealth.Collection{ObservedAt: now, Status: drivehealth.Available, Complete: true, Drives: []drivehealth.Reading{r}}); err != nil {
		t.Fatal(err)
	}
	notifier := &retryNotifier{fail: true}
	if err := store.Deliver(ctx, "tenant", notifier); err == nil {
		t.Fatal("failure was not observable")
	}
	notifier.fail = false
	if err := store.Deliver(ctx, "tenant", notifier); err != nil {
		t.Fatal(err)
	}
	if len(notifier.delivered) == 0 || notifier.delivered[0].Labels["drive_id"] != r.DriveID {
		t.Fatal("retry lost the affected hardware identity")
	}
	first := notifier.delivered[0]
	if first.Labels["agent_id"] != identity.WorkerID || first.Labels["condition_id"] == "" || first.Labels["device_path"] != r.DevicePath || first.Labels["source"] != r.Source {
		t.Fatal("notification lacks owner-routing or drive evidence labels")
	}
	if err := service.Ingest(ctx, identity, "epoch", 2, drivehealth.Collection{ObservedAt: now, Status: drivehealth.Available, Complete: true, Drives: []drivehealth.Reading{r}}); err != nil {
		t.Fatal(err)
	}
	if err := store.Deliver(ctx, "tenant", notifier); err != nil {
		t.Fatal(err)
	}
	for _, a := range notifier.delivered[1:] {
		if a.RuleName == first.RuleName && a.ResolvedAt == nil {
			t.Fatal("repeat observation renotified an unchanged condition")
		}
	}
}

// Anonymous inventory preserves uncertainty at a seen path and has no
// historical identity; each new attempt replaces its anonymous observation.
func TestDriveHealthAnonymousPathsNeverProveRemovalOrReplacementContinuity(t *testing.T) {
	now := time.Now().UTC()
	service := &monitoring.DriveHealthService{Store: monitoring.NewMemoryDriveHealthStore(), Now: func() time.Time { return now }}
	id := guardtelemetry.Identity{TenantID: "tenant", WorkerID: "guard", ServerID: "server"}
	known := driveFixture(now, "serial")
	seq := int64(1)
	ingest := func(readings ...drivehealth.Reading) {
		t.Helper()
		for i := range readings {
			readings[i].ObservedAt = now
		}
		if err := service.Ingest(t.Context(), id, "epoch", seq, drivehealth.Collection{ObservedAt: now, Status: drivehealth.Available, Complete: true, Drives: readings}); err != nil {
			t.Fatal(err)
		}
		seq++
	}
	ingest(known)
	unknown := drivehealth.Reading{DevicePath: "/dev/sda", Protocol: "ATA", Source: drivehealth.SourceSMART, ObservedAt: now, Status: drivehealth.Inaccessible, Reason: "permission_denied", Missing: []string{"identity"}}
	now = now.Add(time.Minute)
	ingest(unknown)
	response, err := service.Read(t.Context(), "tenant", "server")
	if err != nil {
		t.Fatal(err)
	}
	d := driveByID(t, response, known.DriveID)
	if d.ObservationStatus != "incomplete" || d.Health != "unknown" || openAlert(d.Alerts, "disconnected") != nil || d.LastSuccess.Serial != "serial" {
		t.Fatal("unreadable identity at a seen path proved absence or erased prior evidence")
	}
	availabilityEpisode := openAlert(response.Servers[0].Alerts, "observation")
	if availabilityEpisode == nil {
		t.Fatal("anonymous collection incident was not persisted")
	}
	availabilityID := availabilityEpisode.ID
	firstAnonymous := ""
	for _, d := range response.Servers[0].Drives {
		if d.LastAttempt != nil && d.LastAttempt.DriveID == "" {
			firstAnonymous = d.DriveID
			if d.LastSuccess != nil {
				t.Fatal("anonymous reading acquired historical success")
			}
		}
	}
	now = now.Add(time.Minute)
	ingest(unknown)
	response, err = service.Read(t.Context(), "tenant", "server")
	if err != nil {
		t.Fatal(err)
	}
	if openAlert(response.Servers[0].Alerts, "observation").ID != availabilityID {
		t.Fatal("repeated anonymous observation reopened its availability episode")
	}
	for _, d := range response.Servers[0].Drives {
		if d.DriveID == firstAnonymous {
			t.Fatal("anonymous identity accumulated across collection attempts")
		}
	}
	now = now.Add(time.Minute)
	ingest(known)
	response, err = service.Read(t.Context(), "tenant", "server")
	if err != nil {
		t.Fatal(err)
	}
	if openAlert(response.Servers[0].Alerts, "observation") != nil {
		t.Fatal("fresh identified reading did not recover path availability episode")
	}
}
func TestDriveHealthDistinguishesPolicyHeatAndAdvisoryEnduranceFromHardwareFailure(t *testing.T) {
	now := time.Now().UTC()
	service := &monitoring.DriveHealthService{Store: monitoring.NewMemoryDriveHealthStore(), Now: func() time.Time { return now }}
	id := guardtelemetry.Identity{TenantID: "tenant", WorkerID: "guard", ServerID: "server"}
	r := drivehealth.Reading{DriveID: drivehealth.StableID("nvme", "serial", ""), Model: "nvme", Serial: "serial", DevicePath: "/dev/nvme0n1", Protocol: "NVMe", Source: drivehealth.SourceNVMe, ObservedAt: now, Status: drivehealth.Available, Missing: []string{}, TemperatureCelsius: ptr(66.0), CriticalWarning: ptr(uint8(0)), PercentageUsed: ptr(uint8(101)), AvailableSpare: ptr(uint8(100)), SpareThreshold: ptr(uint8(10)), MediaErrors: ptr("0"), ErrorLogEntries: ptr("15")}
	ingest := func(seq int64) {
		t.Helper()
		r.ObservedAt = now
		if err := service.Ingest(t.Context(), id, "epoch", seq, drivehealth.Collection{ObservedAt: now, Status: drivehealth.Available, Complete: true, Drives: []drivehealth.Reading{r}}); err != nil {
			t.Fatal(err)
		}
	}
	read := func() monitoring.DriveState {
		t.Helper()
		response, err := service.Read(t.Context(), "tenant", "server")
		if err != nil {
			t.Fatal(err)
		}
		return driveByID(t, response, r.DriveID)
	}
	ingest(1)
	d := read()
	wear := openAlert(d.Alerts, "wear")
	temperature := openAlert(d.Alerts, "temperature")
	if wear == nil || wear.Severity != "warning" || d.Health == "critical" || temperature == nil || temperature.Evidence["threshold_source"] != "guard_policy" || openAlert(d.Alerts, "error_log_entries_increase") != nil {
		t.Fatal("advisory endurance, policy heat or historical command errors claimed hardware failure")
	}
	now = now.Add(time.Minute)
	r.TemperatureWarningCelsius = ptr(75.0)
	r.TemperatureCriticalCelsius = ptr(90.0)
	r.ErrorLogEntries = ptr("16")
	ingest(2)
	d = read()
	if openAlert(d.Alerts, "temperature") != nil || openAlert(d.Alerts, "error_log_entries_increase").Severity != "warning" {
		t.Fatal("device thresholds or command-error advisory semantics were ignored")
	}
	now = now.Add(time.Minute)
	r.TemperatureCelsius = ptr(91.0)
	ingest(3)
	d = read()
	if a := openAlert(d.Alerts, "temperature"); a == nil || a.Severity != "critical" || a.Evidence["threshold_source"] != "device" {
		t.Fatal("reported device critical temperature was not used")
	}
}
