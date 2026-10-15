package guardtelemetry_test

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/drivehealth"
	"github.com/kombifyio/techstack/pkg/guardtelemetry"
	"github.com/kombifyio/techstack/pkg/monitoring"
)

type driveTransportMetricSink struct {
	mu     sync.Mutex
	refuse bool
	stored []guardtelemetry.SinkSample
}

func (s *driveTransportMetricSink) WriteSamples(samples []guardtelemetry.SinkSample) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.refuse {
		return errors.New("isolated store is unavailable")
	}
	s.stored = append(s.stored, samples...)
	return nil
}

// A durable drive write followed by a metric-store failure must leave the
// complete record spooled, and its replay must preserve the same alert episode.
func TestDriveTelemetrySpoolReplayPreservesEvidenceAndCapacity(t *testing.T) {
	now := time.Now().UTC()
	ptr := func(v uint8) *uint8 { return &v }
	media := "9007199254740993"
	errorLog := "0"
	temperature := 42.0
	reading := drivehealth.Reading{
		DriveID:    drivehealth.StableID("Probe NVMe", "probe-serial", ""),
		DevicePath: "/dev/nvme0n1", Protocol: "NVMe", Model: "Probe NVMe", Serial: "probe-serial",
		Source: drivehealth.SourceSMART, ObservedAt: now, Status: drivehealth.Available, Missing: []string{},
		CriticalWarning: ptr(4), PercentageUsed: ptr(7), AvailableSpare: ptr(100), SpareThreshold: ptr(10),
		MediaErrors: &media, ErrorLogEntries: &errorLog, TemperatureCelsius: &temperature,
	}
	collection := drivehealth.Collection{ObservedAt: now, Status: drivehealth.Available, Complete: true, Drives: []drivehealth.Reading{reading}}
	if err := collection.Validate(now); err != nil {
		t.Fatal(err)
	}
	spoolConfig := guardtelemetry.SpoolConfig{Dir: t.TempDir()}
	spool, err := guardtelemetry.OpenSpool(spoolConfig)
	if err != nil {
		t.Fatal(err)
	}
	_, err = spool.AppendDrives(now, []guardtelemetry.Sample{{Name: "node_disk_free_bytes", Value: 2048, Labels: map[string]string{"origin": "guard", "mount": "/data"}}}, &collection)
	if err != nil {
		t.Fatal(err)
	}
	spool, err = guardtelemetry.OpenSpool(spoolConfig)
	if err != nil {
		t.Fatal(err)
	}
	store := monitoring.NewMemoryDriveHealthStore()
	service := &monitoring.DriveHealthService{Store: store, Now: func() time.Time { return now }}
	metrics := &driveTransportMetricSink{refuse: true}
	identity := guardtelemetry.Identity{TenantID: "transport-tenant", WorkerID: "transport-worker", ServerID: "transport-server"}
	ingest := &guardtelemetry.Ingestor{Sink: metrics, Drives: service, Cursors: guardtelemetry.NewMemoryCursors(), Now: func() time.Time { return now }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		ack, ingestErr := ingest.Ingest(r.Context(), identity, body)
		if ingestErr != nil {
			http.Error(w, "store unavailable", http.StatusServiceUnavailable)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"telemetry": ack}})
	}))
	t.Cleanup(server.Close)
	sender, err := guardtelemetry.NewSender(guardtelemetry.SenderConfig{URL: server.URL, HTTPClient: server.Client(), Spool: spool})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sender.Flush(t.Context()); err == nil {
		t.Fatal("failed capacity write acknowledged drive record")
	}
	before, err := service.Read(t.Context(), identity.TenantID, identity.ServerID)
	if err != nil {
		t.Fatal(err)
	}
	driveBefore := before.Servers[0].Drives[0]
	if driveBefore.LastSuccess == nil || *driveBefore.LastSuccess.MediaErrors != media || driveBefore.Health != "critical" {
		t.Fatalf("drive evidence lost: %+v", driveBefore)
	}
	var episode string
	for _, alert := range driveBefore.Alerts {
		if alert.Code == "nvme_warning" {
			episode = alert.ID
		}
	}
	metrics.mu.Lock()
	metrics.refuse = false
	metrics.mu.Unlock()
	if _, err := sender.Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	after, err := service.Read(t.Context(), identity.TenantID, identity.ServerID)
	if err != nil {
		t.Fatal(err)
	}
	for _, alert := range after.Servers[0].Drives[0].Alerts {
		if alert.Code == "nvme_warning" && alert.ID != episode {
			t.Fatal("replay opened another hardware alert episode")
		}
	}
	if _, pendingBytes := spool.Backlog(); pendingBytes != 0 {
		t.Fatal("acknowledged evidence remains queued")
	}
	var capacitySeen bool
	metrics.mu.Lock()
	defer metrics.mu.Unlock()
	for _, sample := range metrics.stored {
		if sample.Name == "node_disk_free_bytes" && sample.Value == 2048 && sample.Labels["server_id"] == identity.ServerID && sample.Labels["tenant_id"] == identity.TenantID {
			capacitySeen = true
		}
	}
	if !capacitySeen {
		t.Fatal("drive retry lost the independent filesystem capacity sample")
	}
}
