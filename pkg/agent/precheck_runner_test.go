package agent

import (
	"context"
	"testing"

	"github.com/kombifyio/techstack/pkg/core"
)

// TestPreCheckRunner_PortAvailableNoPort tests port check with no port specified.
func TestPreCheckRunner_PortAvailableNoPort(t *testing.T) {
	checks := []core.PreCheckDefinition{
		{Type: "port_available", Description: "Check port", Blocking: true, MinVersion: ""},
	}

	runner := NewPreCheckRunner(checks)
	ctx := context.Background()

	results := runner.RunAll(ctx)
	result := results[0]

	// Should be skipped when no port specified
	if result.Status != PreCheckStatusSkipped {
		t.Errorf("Expected status skipped, got %s", result.Status)
	}
}

// TestPreCheckRunner_UnknownCheckType tests handling of unknown check types.
func TestPreCheckRunner_UnknownCheckType(t *testing.T) {
	checks := []core.PreCheckDefinition{
		{Type: "unknown_check_type", Description: "Unknown", Blocking: false},
	}

	runner := NewPreCheckRunner(checks)
	ctx := context.Background()

	results := runner.RunAll(ctx)

	if len(results) != 1 {
		t.Fatalf("Expected 1 result, got %d", len(results))
	}

	result := results[0]
	if result.Status != PreCheckStatusSkipped {
		t.Errorf("Unknown check type should be skipped, got %s", result.Status)
	}
}

// TestIsVersionAtLeast tests version comparison.
func TestIsVersionAtLeast(t *testing.T) {
	tests := []struct {
		actual   string
		required string
		want     bool
	}{
		{"20.10.0", "20.10.0", true},
		{"20.10.1", "20.10.0", true},
		{"20.11.0", "20.10.0", true},
		{"21.0.0", "20.10.0", true},
		{"20.9.0", "20.10.0", false},
		{"19.10.0", "20.10.0", false},
		{"v20.10.0", "20.10.0", true},
		{"20.10.0", "v20.10.0", true},
		{"20.10.0-ce", "20.10.0", true},
		{"24.0.5", "20.10.0", true},
		{"", "20.10.0", false},
		{"20.10.0", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.actual+"_vs_"+tt.required, func(t *testing.T) {
			got := isVersionAtLeast(tt.actual, tt.required)
			if got != tt.want {
				t.Errorf("isVersionAtLeast(%q, %q) = %v, want %v", tt.actual, tt.required, got, tt.want)
			}
		})
	}
}

// TestParseStorageSize tests storage size parsing.
func TestParseStorageSize(t *testing.T) {
	tests := []struct {
		input string
		want  int
	}{
		{"50GB", 50},
		{"50gb", 50},
		{"50 GB", 50},
		{"8192MB", 8},
		{"1TB", 1024},
		{"100", 100},
		{"", 0},
		{"10GB", 10},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := parseStorageSize(tt.input)
			if got != tt.want {
				t.Errorf("parseStorageSize(%q) = %d, want %d", tt.input, got, tt.want)
			}
		})
	}
}
