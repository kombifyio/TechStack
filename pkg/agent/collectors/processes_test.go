package collectors

import (
	"context"
	"os"
	"regexp"
	"strconv"
	"testing"
	"time"
)

func TestProcessCollector_Name(t *testing.T) {
	c := NewProcessCollector(0, nil)
	if c.Name() != "processes" {
		t.Errorf("expected name %q, got %q", "processes", c.Name())
	}
}

func TestProcessCollector_CollectNoPatterns(t *testing.T) {
	c := NewProcessCollector(0, nil)
	samples, err := c.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect returned unexpected error: %v", err)
	}

	byName := make(map[string][]Sample)
	for _, s := range samples {
		byName[s.Name] = append(byName[s.Name], s)
	}

	// node_processes_total must exist with value > 0.
	totalSamples, ok := byName["node_processes_total"]
	if !ok {
		t.Fatal("missing expected sample node_processes_total")
	}
	if totalSamples[0].Value <= 0 {
		t.Errorf("node_processes_total should be > 0, got %v", totalSamples[0].Value)
	}

	// State breakdown samples must be present.
	for _, name := range []string{
		"node_processes_running",
		"node_processes_sleeping",
		"node_processes_stopped",
		"node_processes_zombie",
	} {
		if _, ok := byName[name]; !ok {
			t.Errorf("missing expected state breakdown sample %q", name)
		}
	}
}

func TestProcessCollector_CollectWithPattern(t *testing.T) {
	// Use the current process PID to build a pattern that is guaranteed to match.
	// The test binary runs as a child of `go test`; matching on PID in the cmdline
	// is fragile, so instead we match on a regex that reliably matches at least the
	// current test binary or the parent go process.
	// We use the current executable's PID to verify the pattern matched something.
	pid := os.Getpid()

	// A broad pattern that matches any process whose name or cmdline contains
	// "collectors.test" (the test binary) or falls back to matching the PID string.
	// On CI and local alike, the test binary itself will match.
	pattern := regexp.MustCompile(`collectors\.test|` + strconv.Itoa(pid))

	c := NewProcessCollector(10*time.Second, []ProcessPattern{
		{Name: "test-process", Pattern: pattern},
	})

	samples, err := c.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect returned unexpected error: %v", err)
	}

	byName := make(map[string][]Sample)
	for _, s := range samples {
		byName[s.Name] = append(byName[s.Name], s)
	}

	// process_pattern_up must exist and indicate at least one match.
	upSamples, ok := byName["process_pattern_up"]
	if !ok {
		t.Fatal("missing expected sample process_pattern_up")
	}
	var found bool
	for _, s := range upSamples {
		if s.Labels["pattern"] == "test-process" && s.Value == 1 {
			found = true
			break
		}
	}
	if !found {
		t.Error("process_pattern_up for pattern 'test-process' should be 1")
	}

	// When a pattern matches, per-process metrics must be emitted.
	for _, name := range []string{
		"process_cpu_usage_percent",
		"process_memory_rss_bytes",
		"process_memory_vms_bytes",
		"process_threads",
	} {
		if _, ok := byName[name]; !ok {
			t.Errorf("missing expected per-process sample %q", name)
		}
	}
}
