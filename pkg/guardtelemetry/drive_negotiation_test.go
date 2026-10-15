package guardtelemetry_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/drivehealth"
	"github.com/kombifyio/techstack/pkg/guardtelemetry"
)

type negotiationSampler func(context.Context) ([]guardtelemetry.Sample, error)

func (f negotiationSampler) Sample(ctx context.Context) ([]guardtelemetry.Sample, error) {
	return f(ctx)
}

type negotiationCollector func(context.Context) (drivehealth.Collection, error)

func (f negotiationCollector) Collect(ctx context.Context) (drivehealth.Collection, error) {
	return f(ctx)
}

// A new Guard must preserve capacity on a strict v1 receiver, then collect fresh
// hardware evidence immediately after a successful capability advertisement.
// A subsequent rollback retains the queued record instead of discarding drives.
func TestDriveCollectionNegotiatesWithoutBreakingLegacyResourceDelivery(t *testing.T) {
	now := time.Now().UTC()
	var peerMu sync.Mutex
	advertise, upgraded, reject, unauthorized, disabled := false, false, false, false, false
	capacityDelivered := false
	delivered := []drivehealth.Collection{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		peerMu.Lock()
		defer peerMu.Unlock()
		if r.Header.Get("Authorization") != "Bearer isolated-peer" {
			http.Error(w, "credential required", http.StatusUnauthorized)
			return
		}
		if advertise {
			w.Header().Set(guardtelemetry.DriveHealthCapabilityHeader, "1")
		}
		if unauthorized {
			http.Error(w, "credential expired", http.StatusUnauthorized)
			return
		}
		if reject {
			http.Error(w, "legacy peer rejects structured field", http.StatusBadRequest)
			return
		}
		if disabled {
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"telemetry": guardtelemetry.Ack{State: guardtelemetry.StateDisabled}}})
			return
		}
		var batch guardtelemetry.Batch
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		if upgraded {
			if err := decoder.Decode(&batch); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
		} else {
			// This is the legacy receiver's actual closed record contract: the
			// drive_health field is absent and would be refused by the decoder.
			var legacy struct {
				SchemaVersion string `json:"schema_version"`
				SourceEpoch   string `json:"source_epoch"`
				Records       []struct {
					Sequence int64                   `json:"sequence"`
					At       time.Time               `json:"at"`
					Samples  []guardtelemetry.Sample `json:"samples,omitempty"`
					Gap      *guardtelemetry.Gap     `json:"gap,omitempty"`
				} `json:"records"`
			}
			if err := decoder.Decode(&legacy); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			batch.SchemaVersion, batch.SourceEpoch = legacy.SchemaVersion, legacy.SourceEpoch
			for _, record := range legacy.Records {
				batch.Records = append(batch.Records, guardtelemetry.Record{Sequence: record.Sequence, At: record.At, Samples: record.Samples, Gap: record.Gap})
			}
		}
		if err := batch.Validate(now); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		var ack int64
		for _, record := range batch.Records {
			ack = record.Sequence
			for _, sample := range record.Samples {
				if sample.Name == "node_disk_free_bytes" && sample.Value == 2048 && guardtelemetry.Admit(sample) {
					capacityDelivered = true
				}
			}
			if record.DriveHealth != nil {
				delivered = append(delivered, *record.DriveHealth)
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"telemetry": guardtelemetry.Ack{AckedSequence: ack}}})
	}))
	t.Cleanup(server.Close)
	spool, err := guardtelemetry.OpenSpool(guardtelemetry.SpoolConfig{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	sender, err := guardtelemetry.NewSender(guardtelemetry.SenderConfig{URL: server.URL, HTTPClient: server.Client(), AgentToken: "isolated-peer", Spool: spool})
	if err != nil {
		t.Fatal(err)
	}
	collectionAttempted := false
	passed, selfTestFailed, temperature := true, false, 35.0
	agent, err := guardtelemetry.NewAgent(guardtelemetry.AgentConfig{
		Host: negotiationSampler(func(context.Context) ([]guardtelemetry.Sample, error) {
			return []guardtelemetry.Sample{{Name: "node_disk_free_bytes", Value: 2048, Labels: map[string]string{"origin": "guard", "mount": "/data"}}}, nil
		}),
		Drives: negotiationCollector(func(context.Context) (drivehealth.Collection, error) {
			collectionAttempted = true
			r := drivehealth.Reading{DriveID: drivehealth.StableID("Negotiation Disk", "CURRENT", ""), DevicePath: "/dev/sda", Model: "Negotiation Disk", Serial: "CURRENT", Protocol: "ATA", Source: drivehealth.SourceSMART, ObservedAt: now, Status: drivehealth.Available, Missing: []string{}, SmartPassed: &passed, SelfTestFailed: &selfTestFailed, TemperatureCelsius: &temperature}
			return drivehealth.Collection{ObservedAt: now, Status: drivehealth.Available, Complete: true, Drives: []drivehealth.Reading{r}}, nil
		}), DriveHealthEnabled: sender.DriveHealthSupported, Spool: spool, Sender: sender, Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	cycle := func(n int) guardtelemetry.Record {
		t.Helper()
		record, kept, err := agent.Collect(t.Context(), n)
		if err != nil || !kept {
			t.Fatalf("resource collection failed: %v", err)
		}
		return record
	}
	legacyRecord := cycle(0)
	if collectionAttempted || legacyRecord.DriveHealth != nil || sender.DriveHealthSupported() {
		t.Fatal("hardware collected before positive negotiation")
	}
	if _, err := sender.Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	peerMu.Lock()
	legacyCapacity := capacityDelivered
	peerMu.Unlock()
	if !legacyCapacity || sender.DriveHealthSupported() {
		t.Fatal("legacy capacity failed or absent capability enabled drive collection")
	}
	peerMu.Lock()
	advertise, unauthorized = true, true
	peerMu.Unlock()
	cycle(1)
	if _, err := sender.Flush(t.Context()); err == nil || sender.DriveHealthSupported() {
		t.Fatal("unsuccessful response enabled structured telemetry")
	}
	peerMu.Lock()
	unauthorized = false
	disabled = true
	peerMu.Unlock()
	if result, err := sender.Flush(t.Context()); err != nil || !result.Disabled || sender.DriveHealthSupported() {
		t.Fatal("disabled response with a capability header enabled hardware collection")
	}
	peerMu.Lock()
	disabled = false
	peerMu.Unlock()
	if _, err := sender.Flush(t.Context()); err != nil || !sender.DriveHealthSupported() {
		t.Fatalf("successful capability was not learned: %v", err)
	}
	peerMu.Lock()
	upgraded = true
	peerMu.Unlock()
	now = now.Add(time.Minute)
	fresh := cycle(2) // outside the tenth-cycle cadence, negotiation starts now.
	if !collectionAttempted || fresh.DriveHealth == nil || !fresh.DriveHealth.ObservedAt.Equal(now) {
		t.Fatal("negotiation delayed collection or reused pre-negotiation evidence")
	}
	if _, err := sender.Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	peerMu.Lock()
	healthy := delivered[len(delivered)-1]
	peerMu.Unlock()
	if healthy.Drives[0].SmartPassed == nil || !*healthy.Drives[0].SmartPassed || !healthy.ObservedAt.Equal(now) {
		t.Fatal("fresh healthy hardware evidence was lost")
	}
	passed = false
	now = now.Add(time.Minute)
	cycle(10)
	if _, err := sender.Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	peerMu.Lock()
	failing := delivered[len(delivered)-1]
	peerMu.Unlock()
	if failing.Drives[0].SmartPassed == nil || *failing.Drives[0].SmartPassed || !failing.ObservedAt.Equal(now) {
		t.Fatal("fresh failing hardware evidence was lost")
	}
	now = now.Add(time.Minute)
	cycle(20)
	peerMu.Lock()
	advertise, reject = false, true
	peerMu.Unlock()
	if _, err := sender.Flush(t.Context()); err == nil || sender.DriveHealthSupported() {
		t.Fatal("unsupported rollback erased evidence or kept collection enabled")
	}
	queued, err := spool.Peek(guardtelemetry.MaxRecordsPerBatch, guardtelemetry.MaxBatchBytes)
	if err != nil {
		t.Fatal(err)
	}
	retained := false
	for _, record := range queued {
		if record.DriveHealth != nil && record.Gap == nil && !*record.DriveHealth.Drives[0].SmartPassed {
			retained = true
		}
	}
	if !retained {
		t.Fatal("queued hardware evidence was discarded on rollback")
	}
	peerMu.Lock()
	reject, disabled = false, true
	peerMu.Unlock()
	if result, err := sender.Flush(t.Context()); err == nil || result.Disabled {
		t.Fatal("disabled receiver allowed queued drive evidence to be cleared")
	}
	queued, err = spool.Peek(guardtelemetry.MaxRecordsPerBatch, guardtelemetry.MaxBatchBytes)
	if err != nil {
		t.Fatal(err)
	}
	retained = false
	for _, record := range queued {
		if record.DriveHealth != nil && record.Gap == nil {
			retained = true
		}
	}
	if !retained {
		t.Fatal("disabled receiver discarded queued hardware evidence")
	}
	collectionAttempted = false
	if record := cycle(30); collectionAttempted || record.DriveHealth != nil {
		t.Fatal("rollback continued new hardware collection")
	}
	peerMu.Lock()
	advertise, reject, disabled = true, false, false
	peerMu.Unlock()
	if _, err := sender.Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	peerMu.Lock()
	advertise = false
	peerMu.Unlock()
	cycle(31)
	if _, err := sender.Flush(t.Context()); err != nil || sender.DriveHealthSupported() {
		t.Fatal("successful response without capability did not disable new hardware collection")
	}
}
