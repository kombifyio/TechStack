package unifier

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/core"
)

func TestPreCheckRunner_NewRunner(t *testing.T) {
	runner := NewPreCheckRunner()

	// Verify default checks are registered
	expectedChecks := []string{
		"docker_version",
		"gpu_detection",
		"wireguard_kernel",
		"disk_space",
		"memory_available",
		"network_connectivity",
	}

	for _, checkType := range expectedChecks {
		runner.mu.RLock()
		_, exists := runner.checks[checkType]
		runner.mu.RUnlock()
		if !exists {
			t.Errorf("Expected default check '%s' to be registered", checkType)
		}
	}
}

func TestPreCheckRunner_Register(t *testing.T) {
	runner := NewPreCheckRunner()

	// Register a custom check
	called := false
	runner.Register("custom_check", func(ctx context.Context) core.PreCheckResult {
		called = true
		return core.PreCheckResult{
			Status: "passed",
			Details: map[string]any{
				"custom": true,
			},
		}
	})

	// Verify it's registered
	runner.mu.RLock()
	_, exists := runner.checks["custom_check"]
	runner.mu.RUnlock()
	if !exists {
		t.Fatal("Custom check should be registered")
	}

	// Run it
	results := runner.Run(context.Background(), []core.PreCheckDefinition{
		{Type: "custom_check", Description: "Custom test check"},
	})

	if len(results) != 1 {
		t.Fatalf("Expected 1 result, got %d", len(results))
	}

	if !called {
		t.Error("Custom check function was not called")
	}

	if results[0].Status != "passed" {
		t.Errorf("Expected status 'passed', got '%s'", results[0].Status)
	}
}

func TestPreCheckRunner_UnknownCheckType(t *testing.T) {
	runner := NewPreCheckRunner()

	results := runner.Run(context.Background(), []core.PreCheckDefinition{
		{Type: "unknown_check_type", Description: "This check doesn't exist"},
	})

	if len(results) != 1 {
		t.Fatalf("Expected 1 result, got %d", len(results))
	}

	if results[0].Status != "skipped" {
		t.Errorf("Expected status 'skipped' for unknown check, got '%s'", results[0].Status)
	}

	if results[0].Error == "" {
		t.Error("Expected error message for unknown check type")
	}
}

func TestPreCheckRunner_Run_Parallel(t *testing.T) {
	runner := NewPreCheckRunner()

	// Register slow checks to verify parallel execution
	startTimes := make(map[string]time.Time)
	var mu sync.Mutex

	runner.Register("slow_check_1", func(ctx context.Context) core.PreCheckResult {
		mu.Lock()
		startTimes["slow_check_1"] = time.Now()
		mu.Unlock()
		time.Sleep(100 * time.Millisecond)
		return core.PreCheckResult{Status: "passed"}
	})

	runner.Register("slow_check_2", func(ctx context.Context) core.PreCheckResult {
		mu.Lock()
		startTimes["slow_check_2"] = time.Now()
		mu.Unlock()
		time.Sleep(100 * time.Millisecond)
		return core.PreCheckResult{Status: "passed"}
	})

	start := time.Now()
	results := runner.Run(context.Background(), []core.PreCheckDefinition{
		{Type: "slow_check_1"},
		{Type: "slow_check_2"},
	})
	elapsed := time.Since(start)

	// If parallel, should take ~100ms, not ~200ms
	if elapsed > 150*time.Millisecond {
		t.Errorf("Checks should run in parallel; took %v (expected ~100ms)", elapsed)
	}

	if len(results) != 2 {
		t.Errorf("Expected 2 results, got %d", len(results))
	}
}

func TestPreCheckRunner_RunBlocking(t *testing.T) {
	runner := NewPreCheckRunner()

	// Register a failing check
	runner.Register("failing_check", func(ctx context.Context) core.PreCheckResult {
		return core.PreCheckResult{
			Status: "failed",
			Error:  "intentional failure",
		}
	})

	// Non-blocking check that fails - should not cause error
	err := runner.RunBlocking(context.Background(), []core.PreCheckDefinition{
		{Type: "failing_check", Blocking: false},
	})
	if err != nil {
		t.Errorf("Non-blocking failure should not return error, got: %v", err)
	}

	// Blocking check that fails - should cause error
	err = runner.RunBlocking(context.Background(), []core.PreCheckDefinition{
		{Type: "failing_check", Blocking: true},
	})
	if err == nil {
		t.Error("Blocking failure should return error")
	}
}

func TestPreCheckRunner_ContextTimeout(t *testing.T) {
	runner := NewPreCheckRunner()

	// Register a check that takes too long
	runner.Register("slow_check", func(ctx context.Context) core.PreCheckResult {
		select {
		case <-ctx.Done():
			return core.PreCheckResult{
				Status: "failed",
				Error:  ctx.Err().Error(),
			}
		case <-time.After(5 * time.Second):
			return core.PreCheckResult{Status: "passed"}
		}
	})

	// Run with a short timeout
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	results := runner.Run(ctx, []core.PreCheckDefinition{
		{Type: "slow_check"},
	})

	if len(results) != 1 {
		t.Fatalf("Expected 1 result, got %d", len(results))
	}

	// Should have failed due to timeout
	if results[0].Status != "failed" {
		t.Errorf("Expected 'failed' status due to timeout, got '%s'", results[0].Status)
	}
}

func TestPreCheckRunner_EmptyDefs(t *testing.T) {
	runner := NewPreCheckRunner()

	results := runner.Run(context.Background(), nil)
	if results != nil {
		t.Errorf("Expected nil for empty defs, got %v", results)
	}

	results = runner.Run(context.Background(), []core.PreCheckDefinition{})
	if results != nil {
		t.Errorf("Expected nil for empty slice, got %v", results)
	}
}
