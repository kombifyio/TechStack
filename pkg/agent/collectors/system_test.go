package collectors

import (
	"context"
	"testing"
	"time"
)

func TestNewSystemCollector_CustomInterval(t *testing.T) {
	c := NewSystemCollector(30 * time.Second)
	if c.Interval() != 30*time.Second {
		t.Errorf("expected custom interval 30s, got %v", c.Interval())
	}
}

func TestSystemCollector_Name(t *testing.T) {
	c := NewSystemCollector(0)
	if c.Name() != "system" {
		t.Errorf("expected name %q, got %q", "system", c.Name())
	}
}

func TestSystemCollector_Collect(t *testing.T) {
	c := NewSystemCollector(0)
	samples, err := c.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect returned unexpected error: %v", err)
	}

	if len(samples) < 5 {
		t.Fatalf("expected at least 5 samples, got %d", len(samples))
	}

	// Verify all samples have non-empty Name and non-zero Timestamp.
	for i, s := range samples {
		if s.Name == "" {
			t.Errorf("sample[%d] has empty Name", i)
		}
		if s.Timestamp.IsZero() {
			t.Errorf("sample[%d] %q has zero Timestamp", i, s.Name)
		}
	}

	// Build a lookup by sample name for targeted assertions.
	byName := make(map[string][]Sample)
	for _, s := range samples {
		byName[s.Name] = append(byName[s.Name], s)
	}

	// node_cpu_count must exist with Value > 0.
	cpuSamples, ok := byName["node_cpu_count"]
	if !ok {
		t.Fatal("missing expected sample node_cpu_count")
	}
	if cpuSamples[0].Value <= 0 {
		t.Errorf("node_cpu_count value should be > 0, got %v", cpuSamples[0].Value)
	}

	// node_memory_total_bytes must exist with Value > 0.
	memSamples, ok := byName["node_memory_total_bytes"]
	if !ok {
		t.Fatal("missing expected sample node_memory_total_bytes")
	}
	if memSamples[0].Value <= 0 {
		t.Errorf("node_memory_total_bytes value should be > 0, got %v", memSamples[0].Value)
	}
}
