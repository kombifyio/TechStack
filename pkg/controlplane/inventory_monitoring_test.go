package controlplane

import (
	"context"
	"testing"
	"time"
)

func floatPtr(value float64) *float64 { return &value }

// Averages follow the reports, not the buckets: merging buckets weights each by
// its sample count, and an interval without reports stays a gap.
func TestDownsampleMetricBucketsWeightsByCountAndKeepsGaps(t *testing.T) {
	base := time.Date(2026, 10, 9, 10, 0, 0, 0, time.UTC)
	buckets := []MetricBucket{
		// One 15-minute bucket averaging 10 over 3 reports, one averaging 50 over 1.
		{Start: base, CPUSum: 30, CPUCount: 3, MemorySum: 40, MemoryCount: 1},
		{Start: base.Add(15 * time.Minute), CPUSum: 50, CPUCount: 1},
		// Two hours later: a different hour, so the interval between is a gap.
		{Start: base.Add(2 * time.Hour), DiskSum: 70, DiskCount: 1},
	}
	points := DownsampleMetricBuckets(buckets, time.Hour)
	if len(points) != 2 {
		t.Fatalf("points = %#v, want the 10:00 hour and the 12:00 hour only", points)
	}
	if points[0].CPU == nil || *points[0].CPU != 20 {
		t.Fatalf("10:00 cpu = %v, want the count-weighted 20", points[0].CPU)
	}
	if points[0].Memory == nil || *points[0].Memory != 40 || points[0].Disk != nil {
		t.Fatalf("10:00 memory/disk = %v/%v, want 40 and absent", points[0].Memory, points[0].Disk)
	}
	if !points[1].At.Equal(base.Add(2*time.Hour)) || points[1].Disk == nil || *points[1].Disk != 70 || points[1].CPU != nil {
		t.Fatalf("12:00 point = %#v", points[1])
	}
}

// A Guard that never measured the host sends zero-valued placeholders; they are
// not a reading and must never enter the history as 0 %.
func TestHostMetricSampleRejectsPlaceholderHost(t *testing.T) {
	if _, ok := HostMetricSample(map[string]any{"cpu_percent": 0.0, "memory_total_bytes": int64(0), "disk_total_bytes": int64(0)}); ok {
		t.Fatal("a host without totals was accepted as a reading")
	}
	sample, ok := HostMetricSample(map[string]any{
		"cpu_percent": 12.34, "memory_used_bytes": int64(2 << 30), "memory_total_bytes": int64(8 << 30),
		"disk_used_bytes": int64(0), "disk_total_bytes": int64(0),
	})
	if !ok || sample.CPU == nil || *sample.CPU != 12.3 || sample.Memory == nil || *sample.Memory != 25 || sample.Disk != nil {
		t.Fatalf("sample = %#v, ok=%v", sample, ok)
	}
}

// Retention is enforced where the rows are produced: opening a new fine bucket
// drops the server's fine buckets past 24 h and coarse buckets past 30 d, and
// keeps the coarse history the fine one no longer holds.
func TestRecordedMetricSamplesAreBoundedByRetention(t *testing.T) {
	store := NewMemoryStore()
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	ctx := context.Background()
	sample := MetricSample{CPU: floatPtr(10)}
	for _, at := range []time.Time{
		now.Add(-31 * 24 * time.Hour), // past both retentions
		now.Add(-3 * 24 * time.Hour),  // past fine, inside coarse
		now,
	} {
		if err := store.RecordServerMetricSample(ctx, "tenant-1", "server-1", at, sample); err != nil {
			t.Fatal(err)
		}
	}
	fine := store.metricBuckets[memoryMetricKey{tenantID: "tenant-1", serverID: "server-1", resolution: MetricFineResolution}]
	coarse := store.metricBuckets[memoryMetricKey{tenantID: "tenant-1", serverID: "server-1", resolution: MetricCoarseResolution}]
	if len(fine) != 1 || len(coarse) != 2 {
		t.Fatalf("fine=%d coarse=%d buckets, want 1 fine and 2 coarse after pruning", len(fine), len(coarse))
	}
	if _, ok := fine[MetricBucketStart(now, MetricFineResolution)]; !ok {
		t.Fatalf("the current fine bucket was pruned: %#v", fine)
	}
}

// The monitoring reads never cross the owner: history, events and backup facts
// of another owner's server in the same tenant are invisible to the signed
// owner, and a scope that is not an owner or collection scope is refused.
func TestMonitoringReadsAreOwnerScoped(t *testing.T) {
	store := NewMemoryStore()
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	store.SetNow(func() time.Time { return now })
	applyTestEnrollment(t, store, now, "active") // server-1, owner-1, tenant-1
	applyTestGuard(t, store, 1, 1, "epoch-a", 1, now.Add(time.Minute))
	if err := store.RecordServerMetricSample(t.Context(), "tenant-1", "server-1", now, MetricSample{CPU: floatPtr(42)}); err != nil {
		t.Fatal(err)
	}
	window, _ := ParseMetricRange("1h")

	owner, _ := NewOwnerInventoryReadScope("tenant-1", "owner-1")
	series, err := store.ListInventoryMetricHistory(t.Context(), owner, window, now.Add(time.Minute))
	if err != nil || len(series) != 1 || series[0].ServerID != "server-1" || len(series[0].Points) != 1 || *series[0].Points[0].CPU != 42 {
		t.Fatalf("owner history = %#v, err=%v", series, err)
	}
	events, err := store.ListInventoryTransitions(t.Context(), owner, now.Add(-time.Hour), 50)
	if err != nil || len(events) == 0 {
		t.Fatalf("owner events = %#v, err=%v", events, err)
	}
	for _, event := range events {
		if event.From == "" || event.Kind != "server" || event.ServerID != "server-1" {
			t.Fatalf("unexpected event %#v", event)
		}
	}

	other, _ := NewOwnerInventoryReadScope("tenant-1", "owner-2")
	if series, _ := store.ListInventoryMetricHistory(t.Context(), other, window, now); len(series) != 0 {
		t.Fatalf("another owner read history: %#v", series)
	}
	if events, _ := store.ListInventoryTransitions(t.Context(), other, now.Add(-time.Hour), 50); len(events) != 0 {
		t.Fatalf("another owner read events: %#v", events)
	}
	if facts, _ := store.ListInventoryBackupFacts(t.Context(), other, now); len(facts) != 0 {
		t.Fatalf("another owner read backup facts: %#v", facts)
	}
	objectScope, _ := NewTenantInventoryObjectReadScope("tenant-1", InventoryReadTargetServer, "server-1")
	if _, err := store.ListInventoryTransitions(t.Context(), objectScope, now.Add(-time.Hour), 50); err == nil {
		t.Fatal("an exact-object scope was widened into a collection read")
	}
}

// A day shows the newest run's outcome; a day without a run has no entry.
func TestSummarizeBackupRunsKeepsNewestRunPerDay(t *testing.T) {
	day := time.Date(2026, 10, 8, 2, 0, 0, 0, time.UTC)
	days := SummarizeBackupRuns([]BackupRunFact{
		{At: day, State: "failed"},
		{At: day.Add(time.Hour), State: "completed"},
		{At: day.Add(-48 * time.Hour), State: "failed"},
	}, 7)
	if len(days) != 2 || days[0].Date != "2026-10-08" || days[0].State != "completed" || days[1].Date != "2026-10-06" || days[1].State != "failed" {
		t.Fatalf("days = %#v", days)
	}
}
