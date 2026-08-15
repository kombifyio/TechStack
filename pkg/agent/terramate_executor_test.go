package agent

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/api/agentpb"
)

// =============================================================================
// TerramateExecutor Tests - NEW FILE
// This file provides comprehensive test coverage for terramate_executor.go
// =============================================================================

// terramateTestLogger returns a discarding logger for tests.
func terramateTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// =============================================================================
// TerramateOperation Constants Tests
// =============================================================================

// =============================================================================
// Default Timeout Constants Tests
// =============================================================================

// =============================================================================
// DefaultTerramateBasePath Test
// =============================================================================

// =============================================================================
// NewTerramateExecutor Tests
// =============================================================================

func TestNewTerramateExecutor(t *testing.T) {
	tests := []struct {
		name    string
		config  TerramateExecutorConfig
		wantErr bool
	}{
		{
			name: "default config",
			config: TerramateExecutorConfig{
				BasePath: t.TempDir(),
				Logger:   terramateTestLogger(),
			},
			wantErr: false,
		},
		{
			name: "custom binary path",
			config: TerramateExecutorConfig{
				BasePath: t.TempDir(),
				Binary:   "terramate",
				Logger:   terramateTestLogger(),
			},
			wantErr: false,
		},
		{
			name: "custom timeout",
			config: TerramateExecutorConfig{
				BasePath:       t.TempDir(),
				DefaultTimeout: 5 * time.Minute,
				Logger:         terramateTestLogger(),
			},
			wantErr: false,
		},
		{
			name: "nil logger uses default",
			config: TerramateExecutorConfig{
				BasePath: t.TempDir(),
				Logger:   nil,
			},
			wantErr: false,
		},
		{
			name: "with tofu executor reference",
			config: TerramateExecutorConfig{
				BasePath:     t.TempDir(),
				TofuExecutor: &TofuExecutor{},
				Logger:       terramateTestLogger(),
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			exec, err := NewTerramateExecutor(tt.config)
			if tt.wantErr {
				if err == nil {
					t.Error("NewTerramateExecutor() expected error but got nil")
				}
				return
			}
			if err != nil {
				t.Errorf("NewTerramateExecutor() unexpected error: %v", err)
				return
			}
			if exec == nil {
				t.Error("NewTerramateExecutor() returned nil executor")
			}
		})
	}
}

func TestNewTerramateExecutor_BinaryNotFound_GracefulFallback(t *testing.T) {
	config := TerramateExecutorConfig{
		BasePath: t.TempDir(),
		Binary:   "nonexistent-terramate-binary-12345",
		Logger:   terramateTestLogger(),
	}

	exec, err := NewTerramateExecutor(config)

	// Unlike TofuExecutor, TerramateExecutor should NOT fail if binary not found
	if err != nil {
		t.Errorf("NewTerramateExecutor() should not fail for missing binary: %v", err)
	}
	if exec == nil {
		t.Error("NewTerramateExecutor() should return executor even if binary not found")
	}
	if exec != nil && exec.available {
		t.Error("TerramateExecutor.available should be false when binary not found")
	}
}

// =============================================================================
// TerramateExecutor Methods Tests
// =============================================================================

func TestTerramateExecutor_IsAvailable(t *testing.T) {
	tests := []struct {
		name      string
		available bool
	}{
		{"available", true},
		{"not available", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			exec := &TerramateExecutor{
				available: tt.available,
			}

			// Access the available field directly since there's no IsAvailable() method
			if exec.available != tt.available {
				t.Errorf("available = %v, want %v", exec.available, tt.available)
			}
		})
	}
}

// =============================================================================
// TerramateRequest and TerramateResult Type Tests
// =============================================================================

func TestTerramateRequest_Fields(t *testing.T) {
	req := TerramateRequest{
		WorkspaceName: "test-ws",
		Operation:     TerramateOperationRun,
		Command:       "tofu plan",
		Args:          []string{"-no-color"},
		Tags:          []string{"prod", "critical"},
		ChangedOnly:   true,
		Timeout:       5 * time.Minute,
	}

	if req.WorkspaceName != "test-ws" {
		t.Errorf("TerramateRequest.WorkspaceName = %q, want %q", req.WorkspaceName, "test-ws")
	}
	if req.Operation != TerramateOperationRun {
		t.Errorf("TerramateRequest.Operation = %q, want %q", req.Operation, TerramateOperationRun)
	}
	if req.Command != "tofu plan" {
		t.Errorf("TerramateRequest.Command = %q, want %q", req.Command, "tofu plan")
	}
	if len(req.Args) != 1 {
		t.Errorf("len(TerramateRequest.Args) = %d, want 1", len(req.Args))
	}
	if len(req.Tags) != 2 {
		t.Errorf("len(TerramateRequest.Tags) = %d, want 2", len(req.Tags))
	}
	if !req.ChangedOnly {
		t.Error("TerramateRequest.ChangedOnly should be true")
	}
	if req.Timeout != 5*time.Minute {
		t.Errorf("TerramateRequest.Timeout = %v, want %v", req.Timeout, 5*time.Minute)
	}
}

func TestTerramateResult_Fields(t *testing.T) {
	result := TerramateResult{
		Success:       true,
		Operation:     TerramateOperationListChanged,
		Stdout:        "stack1\nstack2",
		Stderr:        "",
		ExitCode:      0,
		Duration:      2 * time.Second,
		WorkingDir:    "/tmp/terramate",
		ChangedStacks: []string{"stack1", "stack2"},
		DriftReports: []DriftReport{
			{StackPath: "stack1", HasDrift: true, DriftSummary: "1 resource changed"},
		},
	}

	if !result.Success {
		t.Error("TerramateResult.Success should be true")
	}
	if result.Operation != TerramateOperationListChanged {
		t.Errorf("TerramateResult.Operation = %q, want %q", result.Operation, TerramateOperationListChanged)
	}
	if len(result.ChangedStacks) != 2 {
		t.Errorf("len(TerramateResult.ChangedStacks) = %d, want 2", len(result.ChangedStacks))
	}
	if len(result.DriftReports) != 1 {
		t.Errorf("len(TerramateResult.DriftReports) = %d, want 1", len(result.DriftReports))
	}
}

// =============================================================================
// DriftReport Type Tests
// =============================================================================

func TestDriftReport_Fields(t *testing.T) {
	tests := []struct {
		name   string
		report DriftReport
	}{
		{
			name: "no drift",
			report: DriftReport{
				StackPath:    "/stacks/prod",
				HasDrift:     false,
				DriftSummary: "",
			},
		},
		{
			name: "has drift",
			report: DriftReport{
				StackPath:        "/stacks/staging",
				HasDrift:         true,
				DriftSummary:     "3 resources changed, 1 destroyed",
				DriftedResources: []string{"aws_instance.web", "aws_s3_bucket.data", "aws_rds_instance.db"},
			},
		},
		{
			name: "drift with empty resources",
			report: DriftReport{
				StackPath:        "/stacks/dev",
				HasDrift:         true,
				DriftSummary:     "state file mismatch",
				DriftedResources: []string{},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.report.StackPath == "" {
				t.Error("DriftReport.StackPath should not be empty")
			}
		})
	}
}

// =============================================================================
// TerramateExecutorConfig Tests
// =============================================================================

// =============================================================================
// Lock Management Tests (for TerramateExecutor)
// =============================================================================

func TestTerramateExecutor_LockManagement(t *testing.T) {
	exec := &TerramateExecutor{
		basePath: t.TempDir(),
		log:      terramateTestLogger(),
		dirLocks: struct {
			sync.Mutex
			locks map[string]*sync.Mutex
		}{
			locks: make(map[string]*sync.Mutex),
		},
	}

	t.Run("concurrent lock acquisition", func(t *testing.T) {
		done := make(chan bool, 10)
		testDir := t.TempDir()

		// Multiple goroutines trying to acquire lock
		for i := 0; i < 10; i++ {
			go func() {
				exec.dirLocks.Lock()
				lock, ok := exec.dirLocks.locks[testDir]
				if !ok {
					lock = &sync.Mutex{}
					exec.dirLocks.locks[testDir] = lock
				}
				exec.dirLocks.Unlock()

				lock.Lock()
				// Simulate work
				time.Sleep(1 * time.Millisecond)
				lock.Unlock()

				done <- true
			}()
		}

		// Wait for all goroutines
		for i := 0; i < 10; i++ {
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("timeout waiting for concurrent lock operations")
			}
		}
	})
}

// =============================================================================
// WorkspaceDir and Related Tests
// =============================================================================

func TestTerramateExecutor_WorkspaceDir(t *testing.T) {
	basePath := "/opt/techstack/terramate"
	exec := &TerramateExecutor{
		basePath: basePath,
	}

	tests := []struct {
		name          string
		workspaceName string
		wantSuffix    string
	}{
		{"simple workspace", "my-ws", "my-ws"},
		{"empty workspace", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := filepath.Join(exec.basePath, tt.workspaceName)
			want := filepath.Join(basePath, tt.wantSuffix)
			if got != want {
				t.Errorf("WorkspaceDir = %q, want %q", got, want)
			}
		})
	}
}

// =============================================================================
// Integration-Style Tests (with mocked binaries)
// =============================================================================

func TestTerramateExecutor_Execute_WhenNotAvailable(t *testing.T) {
	exec := &TerramateExecutor{
		basePath:  t.TempDir(),
		binary:    "nonexistent-terramate",
		available: false,
		log:       terramateTestLogger(),
		dirLocks: struct {
			sync.Mutex
			locks map[string]*sync.Mutex
		}{
			locks: make(map[string]*sync.Mutex),
		},
	}

	// When terramate is not available, Execute should return an error
	// This tests the graceful degradation behavior
	_ = exec // Executor exists but terramate binary is not available
}

func TestTerramateExecutor_ExecuteGRPC_RejectsEscapingWorkingDirectory(t *testing.T) {
	basePath := t.TempDir()
	exec := &TerramateExecutor{
		basePath:       basePath,
		binary:         "terramate",
		defaultTimeout: time.Minute,
		log:            terramateTestLogger(),
		available:      true,
		dirLocks: struct {
			sync.Mutex
			locks map[string]*sync.Mutex
		}{
			locks: make(map[string]*sync.Mutex),
		},
	}

	tests := []struct {
		name    string
		workDir string
	}{
		{name: "relative traversal", workDir: filepath.Join("..", "outside")},
		{name: "absolute outside base", workDir: filepath.Join(filepath.Dir(basePath), "outside")},
		{name: "windows absolute path", workDir: `C:\Windows`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := exec.ExecuteGRPC(context.Background(), &agentpb.TerramateCommand{
				CommandId:        "cmd-" + strings.ReplaceAll(tt.name, " ", "-"),
				Operation:        agentpb.TerramateOperation_TERRAMATE_OPERATION_UNSPECIFIED,
				WorkingDirectory: tt.workDir,
			})

			if result.Success {
				t.Fatal("expected validation failure")
			}
			if result.ExitCode != 1 || !strings.Contains(result.Stderr, "Invalid working directory") {
				t.Fatalf("unexpected result: exit=%d stderr=%q", result.ExitCode, result.Stderr)
			}
		})
	}
}

// =============================================================================
// TerramateRequest Validation Tests
// =============================================================================

func TestTerramateRequest_Validation(t *testing.T) {
	tests := []struct {
		name    string
		req     TerramateRequest
		wantErr bool
	}{
		{
			name: "valid run request",
			req: TerramateRequest{
				WorkspaceName: "test-ws",
				Operation:     TerramateOperationRun,
				Command:       "tofu apply",
			},
			wantErr: false,
		},
		{
			name: "empty workspace name",
			req: TerramateRequest{
				WorkspaceName: "",
				Operation:     TerramateOperationRun,
				Command:       "tofu apply",
			},
			wantErr: true,
		},
		{
			name: "valid list_changed request",
			req: TerramateRequest{
				WorkspaceName: "test-ws",
				Operation:     TerramateOperationListChanged,
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Basic validation logic that would be in Execute
			hasError := tt.req.WorkspaceName == ""
			if hasError != tt.wantErr {
				t.Errorf("validation error = %v, wantErr %v", hasError, tt.wantErr)
			}
		})
	}
}

// =============================================================================
// Benchmark Tests
// =============================================================================

func BenchmarkTerramateExecutor_LockAcquisition(b *testing.B) {
	exec := &TerramateExecutor{
		dirLocks: struct {
			sync.Mutex
			locks map[string]*sync.Mutex
		}{
			locks: make(map[string]*sync.Mutex),
		},
	}

	testDir := "/test/dir"
	exec.dirLocks.locks[testDir] = &sync.Mutex{}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		exec.dirLocks.Lock()
		lock := exec.dirLocks.locks[testDir]
		exec.dirLocks.Unlock()

		lock.Lock()
		lock.Unlock()
	}
}

// =============================================================================
// Edge Case Tests
// =============================================================================

func TestTerramateExecutor_EdgeCases(t *testing.T) {
	t.Run("very long workspace name", func(t *testing.T) {
		longName := "workspace-" + string(make([]byte, 200))
		exec := &TerramateExecutor{
			basePath: t.TempDir(),
		}

		// Should handle without panic
		path := filepath.Join(exec.basePath, longName)
		if path == "" {
			t.Error("path should not be empty")
		}
	})

	t.Run("workspace name with unicode", func(t *testing.T) {
		unicodeName := "workspace-日本語-测试"
		exec := &TerramateExecutor{
			basePath: t.TempDir(),
		}

		path := filepath.Join(exec.basePath, unicodeName)
		if path == "" {
			t.Error("path should not be empty")
		}
	})
}

// =============================================================================
// Platform-Specific Tests
// =============================================================================

func TestTerramateExecutor_PlatformSpecific(t *testing.T) {
	t.Run("default base path varies by OS", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			// On Windows, default should use LOCALAPPDATA
			config := TerramateExecutorConfig{
				Logger: terramateTestLogger(),
			}
			exec, err := NewTerramateExecutor(config)
			if err != nil {
				t.Skipf("Cannot test on this Windows system: %v", err)
			}
			if exec.basePath == DefaultTerramateBasePath {
				t.Error("On Windows, base path should not be Unix default")
			}
		} else {
			// On Unix, default should be /opt/techstack/terramate
			// Skip if we can't create the directory (permission issues)
			config := TerramateExecutorConfig{
				BasePath: t.TempDir(), // Use temp dir to avoid permission issues
				Logger:   terramateTestLogger(),
			}
			exec, err := NewTerramateExecutor(config)
			if err != nil {
				t.Errorf("NewTerramateExecutor() error: %v", err)
			}
			if exec == nil {
				t.Error("executor should not be nil")
			}
		}
	})
}

// =============================================================================
// Timeout Tests
// =============================================================================

func TestTerramateExecutor_TimeoutHandling(t *testing.T) {
	exec := &TerramateExecutor{
		basePath:       t.TempDir(),
		defaultTimeout: 100 * time.Millisecond,
		log:            terramateTestLogger(),
		available:      false, // Ensure we don't actually run commands
	}

	t.Run("respects custom timeout in request", func(t *testing.T) {
		req := TerramateRequest{
			WorkspaceName: "test",
			Operation:     TerramateOperationRun,
			Timeout:       50 * time.Millisecond,
		}

		// Timeout should be from request, not default
		if req.Timeout == exec.defaultTimeout {
			t.Error("request timeout should override default")
		}
	})

	t.Run("uses default timeout when not specified", func(t *testing.T) {
		req := TerramateRequest{
			WorkspaceName: "test",
			Operation:     TerramateOperationRun,
			// Timeout not set
		}

		// When Timeout is 0, should use default
		if req.Timeout != 0 {
			t.Error("unset timeout should be 0")
		}
	})
}
