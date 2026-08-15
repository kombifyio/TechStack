package agent

import (
	"context"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/core"
)

// TestPreCheckRunner_RunAll tests the RunAll method aggregation.
func TestPreCheckRunner_RunAll(t *testing.T) {
	checks := []core.PreCheckDefinition{
		{Type: "disk_space", Description: "Check disk space", Blocking: true},
		{Type: "ram_available", Description: "Check RAM", Blocking: false},
	}

	runner := NewPreCheckRunner(checks)
	ctx := context.Background()

	results := runner.RunAll(ctx)

	if len(results) != len(checks) {
		t.Errorf("RunAll() returned %d results, expected %d", len(results), len(checks))
	}

	// Verify each result has the correct type
	for i, result := range results {
		if result.Type != checks[i].Type {
			t.Errorf("Result %d type = %s, expected %s", i, result.Type, checks[i].Type)
		}
		// Status should be set (not empty)
		if result.Status == "" {
			t.Errorf("Result %d has empty status", i)
		}
	}
}

// TestPreCheckRunner_RunBlocking tests that only blocking checks are executed.
func TestPreCheckRunner_RunBlocking(t *testing.T) {
	checks := []core.PreCheckDefinition{
		{Type: "disk_space", Description: "Check disk space", Blocking: true},
		{Type: "ram_available", Description: "Check RAM", Blocking: false},
		{Type: "port_available", Description: "Check port", Blocking: true, MinVersion: "59999"},
	}

	runner := NewPreCheckRunner(checks)
	ctx := context.Background()

	results, _ := runner.RunBlocking(ctx)

	// Should only return 2 blocking checks
	if len(results) != 2 {
		t.Errorf("RunBlocking() returned %d results, expected 2 (blocking only)", len(results))
	}

	// Verify types are the blocking ones
	expectedTypes := map[string]bool{"disk_space": true, "port_available": true}
	for _, result := range results {
		if !expectedTypes[result.Type] {
			t.Errorf("RunBlocking() returned non-blocking check type: %s", result.Type)
		}
	}
}

// TestPreCheckRunner_WithTimeout tests timeout configuration.
func TestPreCheckRunner_WithTimeout(t *testing.T) {
	runner := NewPreCheckRunner(nil)

	// Default timeout
	if runner.timeout != 30*time.Second {
		t.Errorf("Default timeout = %v, expected 30s", runner.timeout)
	}

	// Set custom timeout
	runner.WithTimeout(10 * time.Second)
	if runner.timeout != 10*time.Second {
		t.Errorf("Custom timeout = %v, expected 10s", runner.timeout)
	}
}

// TestPreCheckRunner_SetChecks tests replacing checks.
func TestPreCheckRunner_SetChecks(t *testing.T) {
	runner := NewPreCheckRunner(nil)

	if len(runner.checks) != 0 {
		t.Errorf("Initial checks should be empty, got %d", len(runner.checks))
	}

	newChecks := []core.PreCheckDefinition{
		{Type: "disk_space", Description: "Test", Blocking: true},
	}
	runner.SetChecks(newChecks)

	if len(runner.checks) != 1 {
		t.Errorf("After SetChecks, got %d checks, expected 1", len(runner.checks))
	}
}

// TestPreCheckRunner_DiskSpace tests the disk space check.
func TestPreCheckRunner_DiskSpace(t *testing.T) {
	checks := []core.PreCheckDefinition{
		{Type: "disk_space", Description: "Check disk space", Blocking: true},
	}

	runner := NewPreCheckRunner(checks)
	ctx := context.Background()

	results := runner.RunAll(ctx)

	if len(results) != 1 {
		t.Fatalf("Expected 1 result, got %d", len(results))
	}

	result := results[0]
	if result.Type != "disk_space" {
		t.Errorf("Expected type disk_space, got %s", result.Type)
	}

	// Should pass (or fail with informative error if disk info unavailable)
	if result.Status != PreCheckStatusPassed && result.Status != PreCheckStatusFailed {
		t.Errorf("Unexpected status: %s", result.Status)
	}

	// Check details are populated
	if result.Status == PreCheckStatusPassed {
		if _, ok := result.Details["availableGB"]; !ok {
			t.Error("Expected availableGB in details")
		}
		if _, ok := result.Details["totalGB"]; !ok {
			t.Error("Expected totalGB in details")
		}
	}
}

// TestPreCheckRunner_DiskSpaceWithMinimum tests disk space check with minimum requirement.
func TestPreCheckRunner_DiskSpaceWithMinimum(t *testing.T) {
	// Test with very small requirement (should pass)
	checks := []core.PreCheckDefinition{
		{Type: "disk_space", Description: "Check disk space", Blocking: true, MinVersion: "1GB"},
	}

	runner := NewPreCheckRunner(checks)
	ctx := context.Background()

	results := runner.RunAll(ctx)
	result := results[0]

	// With 1GB requirement, should pass on most systems
	if result.Details["requiredGB"] != 1 {
		t.Errorf("Expected requiredGB=1, got %v", result.Details["requiredGB"])
	}
}

// TestPreCheckRunner_RAMAvailable tests the RAM check.
func TestPreCheckRunner_RAMAvailable(t *testing.T) {
	checks := []core.PreCheckDefinition{
		{Type: "ram_available", Description: "Check RAM", Blocking: false},
	}

	runner := NewPreCheckRunner(checks)
	ctx := context.Background()

	results := runner.RunAll(ctx)

	if len(results) != 1 {
		t.Fatalf("Expected 1 result, got %d", len(results))
	}

	result := results[0]
	if result.Type != "ram_available" {
		t.Errorf("Expected type ram_available, got %s", result.Type)
	}

	// Should have totalGB in details if passed
	if result.Status == PreCheckStatusPassed {
		if _, ok := result.Details["totalGB"]; !ok {
			t.Error("Expected totalGB in details")
		}
	}
}

// TestPreCheckRunner_PortAvailable tests port availability check.
func TestPreCheckRunner_PortAvailable(t *testing.T) {
	// Use high port that's likely to be free
	checks := []core.PreCheckDefinition{
		{Type: "port_available", Description: "Check port", Blocking: true, MinVersion: "59998"},
	}

	runner := NewPreCheckRunner(checks)
	ctx := context.Background()

	results := runner.RunAll(ctx)

	if len(results) != 1 {
		t.Fatalf("Expected 1 result, got %d", len(results))
	}

	result := results[0]
	if result.Type != "port_available" {
		t.Errorf("Expected type port_available, got %s", result.Type)
	}

	// Port 59998 should typically be available
	if result.Status != PreCheckStatusPassed {
		t.Logf("Port check status: %s (may be in use)", result.Status)
	}
}

// TestPreCheckRunner_PortAvailableMultiple tests checking multiple ports.
func TestPreCheckRunner_PortAvailableMultiple(t *testing.T) {
	checks := []core.PreCheckDefinition{
		{Type: "port_available", Description: "Check ports", Blocking: true, MinVersion: "59996,59997"},
	}

	runner := NewPreCheckRunner(checks)
	ctx := context.Background()

	results := runner.RunAll(ctx)
	result := results[0]

	// Should have checked ports in details
	if result.Details["checkedPorts"] == nil {
		t.Error("Expected checkedPorts in details")
	}
}

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

// TestPreCheckRunner_ContextCancellation tests timeout handling.
func TestPreCheckRunner_ContextCancellation(t *testing.T) {
	checks := []core.PreCheckDefinition{
		{Type: "disk_space", Description: "Check disk space", Blocking: true},
	}

	runner := NewPreCheckRunner(checks).WithTimeout(1 * time.Millisecond)

	// Create already-canceled context
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	results := runner.RunAll(ctx)

	// Should still return results (checks handle their own timeouts)
	if len(results) != 1 {
		t.Errorf("Expected 1 result even with canceled context, got %d", len(results))
	}
}

// TestPreCheckRunner_ToCoreResult tests conversion to core.PreCheckResult.
func TestPreCheckRunner_ToCoreResult(t *testing.T) {
	result := PreCheckResult{
		Type:    "disk_space",
		Status:  PreCheckStatusPassed,
		Message: "100 GB available",
		Details: map[string]any{"availableGB": "100"},
	}

	coreResult := result.ToCoreResult()

	if coreResult.Type != result.Type {
		t.Errorf("ToCoreResult().Type = %s, expected %s", coreResult.Type, result.Type)
	}
	if coreResult.Status != string(result.Status) {
		t.Errorf("ToCoreResult().Status = %s, expected %s", coreResult.Status, string(result.Status))
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

// TestParseVersionPart tests version part parsing.
func TestParseVersionPart(t *testing.T) {
	tests := []struct {
		input string
		want  int
	}{
		{"20", 20},
		{"10", 10},
		{"0", 0},
		{"5-ce", 5},
		{"", 0},
		{"abc", 0},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := parseVersionPart(tt.input)
			if got != tt.want {
				t.Errorf("parseVersionPart(%q) = %d, want %d", tt.input, got, tt.want)
			}
		})
	}
}

// TestPreCheckRunner_DockerVersion tests Docker version check (may skip if Docker not installed).
func TestPreCheckRunner_DockerVersion(t *testing.T) {
	checks := []core.PreCheckDefinition{
		{Type: "docker_version", Description: "Check Docker", Blocking: false},
	}

	runner := NewPreCheckRunner(checks)
	ctx := context.Background()

	results := runner.RunAll(ctx)

	if len(results) != 1 {
		t.Fatalf("Expected 1 result, got %d", len(results))
	}

	result := results[0]
	if result.Type != "docker_version" {
		t.Errorf("Expected type docker_version, got %s", result.Type)
	}

	// Log the result - may fail if Docker not installed
	t.Logf("Docker check result: status=%s, message=%s, error=%s",
		result.Status, result.Message, result.Error)
}

// TestPreCheckRunner_GPUAvailable tests GPU check (expected to fail on most dev machines).
func TestPreCheckRunner_GPUAvailable(t *testing.T) {
	checks := []core.PreCheckDefinition{
		{Type: "gpu_available", Description: "Check GPU", Blocking: false},
	}

	runner := NewPreCheckRunner(checks)
	ctx := context.Background()

	results := runner.RunAll(ctx)

	if len(results) != 1 {
		t.Fatalf("Expected 1 result, got %d", len(results))
	}

	result := results[0]
	if result.Type != "gpu_available" {
		t.Errorf("Expected type gpu_available, got %s", result.Type)
	}

	// GPU check will likely return warning or failed on dev machines
	t.Logf("GPU check result: status=%s, message=%s", result.Status, result.Message)
}
