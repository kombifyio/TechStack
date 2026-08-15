package monitoring

import (
	"context"
	"slices"
	"testing"
	"time"
)

// helperWriteSamples writes a batch of test_cpu_usage samples with standard labels.
func helperWriteSamples(t *testing.T, m *MonitorTSDB, count int, baseTime time.Time, interval time.Duration) {
	t.Helper()
	samples := make([]MetricSample, count)
	for i := range count {
		samples[i] = MetricSample{
			Name:      "test_cpu_usage",
			Value:     float64(i) * 1.5,
			Labels:    map[string]string{"host": "test-host", "core": "0"},
			Timestamp: baseTime.Add(time.Duration(i) * interval),
		}
	}
	if err := m.Write(samples); err != nil {
		t.Fatalf("Write failed: %v", err)
	}
}

// newTestTSDB creates a MonitorTSDB backed by t.TempDir and registers Close via t.Cleanup.
func newTestTSDB(t *testing.T) *MonitorTSDB {
	t.Helper()
	m, err := NewMonitorTSDB(TSDBConfig{DataDir: t.TempDir()})
	if err != nil {
		t.Fatalf("NewMonitorTSDB: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })
	return m
}

func TestNewMonitorTSDB(t *testing.T) {
	t.Run("opens_successfully", func(t *testing.T) {
		dir := t.TempDir()
		m, err := NewMonitorTSDB(TSDBConfig{DataDir: dir})
		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
		if m == nil {
			t.Fatal("expected non-nil MonitorTSDB")
		}
		if m.db == nil {
			t.Fatal("expected non-nil underlying TSDB db")
		}
		if m.engine == nil {
			t.Fatal("expected non-nil PromQL engine")
		}
		if err := m.Close(); err != nil {
			t.Fatalf("Close failed: %v", err)
		}
	})

	t.Run("default_logger_assigned", func(t *testing.T) {
		dir := t.TempDir()
		m, err := NewMonitorTSDB(TSDBConfig{DataDir: dir})
		if err != nil {
			t.Fatalf("NewMonitorTSDB: %v", err)
		}
		defer m.Close()

		if m.logger == nil {
			t.Fatal("expected default logger to be assigned when none provided")
		}
	})

	t.Run("custom_retention_opens", func(t *testing.T) {
		dir := t.TempDir()
		m, err := NewMonitorTSDB(TSDBConfig{DataDir: dir, Retention: 7 * 24 * time.Hour})
		if err != nil {
			t.Fatalf("NewMonitorTSDB with custom retention: %v", err)
		}
		defer m.Close()

		if m.db == nil {
			t.Fatal("expected non-nil db with custom retention")
		}
	})
}

func TestWrite(t *testing.T) {
	t.Run("single_sample", func(t *testing.T) {
		m := newTestTSDB(t)
		err := m.Write([]MetricSample{
			{
				Name:      "test_cpu_usage",
				Value:     42.0,
				Labels:    map[string]string{"host": "test-host", "core": "0"},
				Timestamp: time.Now(),
			},
		})
		if err != nil {
			t.Fatalf("Write single sample failed: %v", err)
		}
	})

	t.Run("multiple_samples", func(t *testing.T) {
		m := newTestTSDB(t)
		now := time.Now()
		samples := []MetricSample{
			{Name: "test_cpu_usage", Value: 10.0, Labels: map[string]string{"host": "test-host", "core": "0"}, Timestamp: now},
			{Name: "test_cpu_usage", Value: 20.0, Labels: map[string]string{"host": "test-host", "core": "0"}, Timestamp: now.Add(time.Second)},
			{Name: "test_cpu_usage", Value: 30.0, Labels: map[string]string{"host": "test-host", "core": "0"}, Timestamp: now.Add(2 * time.Second)},
		}
		if err := m.Write(samples); err != nil {
			t.Fatalf("Write multiple samples failed: %v", err)
		}
	})

	t.Run("empty_samples", func(t *testing.T) {
		m := newTestTSDB(t)
		if err := m.Write([]MetricSample{}); err != nil {
			t.Fatalf("Write empty slice should not fail: %v", err)
		}
	})

	t.Run("different_metrics", func(t *testing.T) {
		m := newTestTSDB(t)
		now := time.Now()
		samples := []MetricSample{
			{Name: "test_cpu_usage", Value: 55.0, Labels: map[string]string{"host": "test-host", "core": "0"}, Timestamp: now},
			{Name: "test_mem_usage", Value: 1024.0, Labels: map[string]string{"host": "test-host"}, Timestamp: now},
		}
		if err := m.Write(samples); err != nil {
			t.Fatalf("Write different metrics failed: %v", err)
		}
	})
}

func TestInstantQuery(t *testing.T) {
	m := newTestTSDB(t)
	ctx := context.Background()
	now := time.Now()

	helperWriteSamples(t, m, 5, now, time.Second)

	t.Run("basic_query", func(t *testing.T) {
		// Query at a time that covers the written samples.
		result, err := m.InstantQuery(ctx, "test_cpu_usage", now.Add(4*time.Second))
		if err != nil {
			t.Fatalf("InstantQuery failed: %v", err)
		}
		if result == nil {
			t.Fatal("expected non-nil QueryResult")
		}
		if result.Value == nil {
			t.Fatal("expected non-nil Value in QueryResult")
		}
		if result.Value.String() == "" {
			t.Fatal("expected non-empty Value string representation")
		}
	})

	t.Run("query_with_label_matcher", func(t *testing.T) {
		result, err := m.InstantQuery(ctx, `test_cpu_usage{host="test-host"}`, now.Add(4*time.Second))
		if err != nil {
			t.Fatalf("InstantQuery with label matcher failed: %v", err)
		}
		if result == nil || result.Value == nil {
			t.Fatal("expected non-nil result with label matcher")
		}
	})

	t.Run("query_no_match", func(t *testing.T) {
		result, err := m.InstantQuery(ctx, `nonexistent_metric`, now.Add(4*time.Second))
		if err != nil {
			t.Fatalf("InstantQuery for nonexistent metric should not error: %v", err)
		}
		if result == nil {
			t.Fatal("expected non-nil result even for empty match")
		}
	})
}

func TestRangeQuery(t *testing.T) {
	m := newTestTSDB(t)
	ctx := context.Background()
	now := time.Now()

	// Write 10 samples, each 10 seconds apart, giving a 90-second window.
	helperWriteSamples(t, m, 10, now, 10*time.Second)

	t.Run("basic_range", func(t *testing.T) {
		start := now
		end := now.Add(90 * time.Second)
		step := 10 * time.Second

		result, err := m.RangeQuery(ctx, "test_cpu_usage", start, end, step)
		if err != nil {
			t.Fatalf("RangeQuery failed: %v", err)
		}
		if result == nil {
			t.Fatal("expected non-nil QueryResult")
		}
		if result.Value == nil {
			t.Fatal("expected non-nil Value in range QueryResult")
		}
		valStr := result.Value.String()
		if valStr == "" {
			t.Fatal("expected non-empty range query result")
		}
	})

	t.Run("range_with_label_filter", func(t *testing.T) {
		start := now
		end := now.Add(90 * time.Second)
		step := 10 * time.Second

		result, err := m.RangeQuery(ctx, `test_cpu_usage{core="0"}`, start, end, step)
		if err != nil {
			t.Fatalf("RangeQuery with label filter failed: %v", err)
		}
		if result == nil || result.Value == nil {
			t.Fatal("expected non-nil result for label-filtered range query")
		}
	})

	t.Run("range_empty_window", func(t *testing.T) {
		// Query a time range well before any data was written.
		farPast := now.Add(-24 * time.Hour)
		result, err := m.RangeQuery(ctx, "test_cpu_usage", farPast, farPast.Add(time.Minute), 10*time.Second)
		if err != nil {
			t.Fatalf("RangeQuery on empty window should not error: %v", err)
		}
		if result == nil {
			t.Fatal("expected non-nil result even for empty window")
		}
	})
}

func TestLabelNames(t *testing.T) {
	m := newTestTSDB(t)
	ctx := context.Background()

	helperWriteSamples(t, m, 3, time.Now(), time.Second)

	names, err := m.LabelNames(ctx)
	if err != nil {
		t.Fatalf("LabelNames failed: %v", err)
	}

	// We expect at least __name__, host, core.
	required := []string{"__name__", "host", "core"}
	for _, req := range required {
		if !slices.Contains(names, req) {
			t.Errorf("expected label name %q in %v", req, names)
		}
	}
}

func TestLabelValues(t *testing.T) {
	m := newTestTSDB(t)
	ctx := context.Background()

	helperWriteSamples(t, m, 3, time.Now(), time.Second)

	t.Run("host_values", func(t *testing.T) {
		vals, err := m.LabelValues(ctx, "host")
		if err != nil {
			t.Fatalf("LabelValues(host) failed: %v", err)
		}
		if !slices.Contains(vals, "test-host") {
			t.Errorf("expected 'test-host' in host label values, got %v", vals)
		}
	})

	t.Run("core_values", func(t *testing.T) {
		vals, err := m.LabelValues(ctx, "core")
		if err != nil {
			t.Fatalf("LabelValues(core) failed: %v", err)
		}
		if !slices.Contains(vals, "0") {
			t.Errorf("expected '0' in core label values, got %v", vals)
		}
	})

	t.Run("nonexistent_label", func(t *testing.T) {
		vals, err := m.LabelValues(ctx, "nonexistent_label_xyz")
		if err != nil {
			t.Fatalf("LabelValues for nonexistent label should not error: %v", err)
		}
		if len(vals) != 0 {
			t.Errorf("expected empty values for nonexistent label, got %v", vals)
		}
	})
}

func TestMetricNames(t *testing.T) {
	m := newTestTSDB(t)
	ctx := context.Background()
	now := time.Now()

	// Write two different metric names.
	samples := []MetricSample{
		{Name: "test_cpu_usage", Value: 50.0, Labels: map[string]string{"host": "test-host", "core": "0"}, Timestamp: now},
		{Name: "test_mem_usage", Value: 2048.0, Labels: map[string]string{"host": "test-host"}, Timestamp: now},
	}
	if err := m.Write(samples); err != nil {
		t.Fatalf("Write failed: %v", err)
	}

	names, err := m.MetricNames(ctx)
	if err != nil {
		t.Fatalf("MetricNames failed: %v", err)
	}

	if !slices.Contains(names, "test_cpu_usage") {
		t.Errorf("expected 'test_cpu_usage' in metric names, got %v", names)
	}
	if !slices.Contains(names, "test_mem_usage") {
		t.Errorf("expected 'test_mem_usage' in metric names, got %v", names)
	}
}

func TestSeriesCount(t *testing.T) {
	m := newTestTSDB(t)
	now := time.Now()

	// Before any writes, series count should be 0.
	if count := m.SeriesCount(); count != 0 {
		t.Fatalf("expected 0 series before writes, got %d", count)
	}

	// Write one series (same metric+labels = one series).
	helperWriteSamples(t, m, 5, now, time.Second)

	count := m.SeriesCount()
	if count == 0 {
		t.Fatal("expected non-zero series count after writes")
	}

	// Write a second distinct series (different label set).
	err := m.Write([]MetricSample{
		{Name: "test_cpu_usage", Value: 99.0, Labels: map[string]string{"host": "test-host", "core": "1"}, Timestamp: now},
	})
	if err != nil {
		t.Fatalf("Write second series failed: %v", err)
	}

	countAfter := m.SeriesCount()
	if countAfter <= count {
		t.Fatalf("expected series count to increase after writing a new series: before=%d after=%d", count, countAfter)
	}
}

func TestStats(t *testing.T) {
	m := newTestTSDB(t)
	ctx := context.Background()
	now := time.Now()

	// Write samples spanning a 10-second window.
	helperWriteSamples(t, m, 5, now, 2*time.Second)

	stats, err := m.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats failed: %v", err)
	}
	if stats == nil {
		t.Fatal("expected non-nil TSDBStats")
	}

	t.Run("num_series", func(t *testing.T) {
		if stats.NumSeries == 0 {
			t.Error("expected non-zero NumSeries")
		}
	})

	t.Run("time_bounds", func(t *testing.T) {
		if stats.MinTime == 0 {
			t.Error("expected non-zero MinTime")
		}
		if stats.MaxTime == 0 {
			t.Error("expected non-zero MaxTime")
		}
		if stats.MaxTime < stats.MinTime {
			t.Errorf("MaxTime (%d) should be >= MinTime (%d)", stats.MaxTime, stats.MinTime)
		}
	})

	t.Run("retention_days", func(t *testing.T) {
		if stats.RetentionDays < 0 {
			t.Errorf("expected non-negative RetentionDays, got %f", stats.RetentionDays)
		}
	})

	t.Run("top_metrics", func(t *testing.T) {
		if len(stats.TopMetrics) == 0 {
			t.Error("expected non-empty TopMetrics")
		}
		found := false
		for _, mc := range stats.TopMetrics {
			if mc.Name == "test_cpu_usage" {
				found = true
				break
			}
		}
		if !found {
			t.Error("expected 'test_cpu_usage' in TopMetrics")
		}
	})
}

func TestClose(t *testing.T) {
	dir := t.TempDir()
	m, err := NewMonitorTSDB(TSDBConfig{DataDir: dir})
	if err != nil {
		t.Fatalf("NewMonitorTSDB: %v", err)
	}

	// Write some data before closing to ensure a non-trivial shutdown.
	helperWriteSamples(t, m, 3, time.Now(), time.Second)

	if err := m.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	// Re-open the same directory to prove it was shut down cleanly.
	m2, err := NewMonitorTSDB(TSDBConfig{DataDir: dir})
	if err != nil {
		t.Fatalf("re-open after Close failed: %v", err)
	}

	// The previously written data should survive the close/reopen cycle.
	ctx := context.Background()
	names, err := m2.MetricNames(ctx)
	if err != nil {
		t.Fatalf("MetricNames after reopen failed: %v", err)
	}
	if !slices.Contains(names, "test_cpu_usage") {
		t.Errorf("expected 'test_cpu_usage' to survive close/reopen, got %v", names)
	}

	if err := m2.Close(); err != nil {
		t.Fatalf("second Close failed: %v", err)
	}
}
