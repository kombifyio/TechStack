package agent

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/api/agentpb"
)

// tofuTestLogger returns a discarding logger for tests.
func tofuTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// =============================================================================
// NewTofuExecutor Tests
// =============================================================================

func TestNewTofuExecutor(t *testing.T) {
	// Create a fake tofu binary for tests that need binary verification
	tempDir := t.TempDir()
	fakeTofuBinary := filepath.Join(tempDir, "tofu")
	if runtime.GOOS == "windows" {
		fakeTofuBinary += ".exe"
	}
	// Create an empty executable file (won't actually run, but will be found by LookPath)
	f, err := os.OpenFile(fakeTofuBinary, os.O_CREATE|os.O_WRONLY, 0755)
	if err != nil {
		t.Fatalf("Failed to create fake tofu binary: %v", err)
	}
	f.Close()

	// Add tempDir to PATH for the tests
	oldPath := os.Getenv("PATH")
	os.Setenv("PATH", tempDir+string(os.PathListSeparator)+oldPath)
	defer os.Setenv("PATH", oldPath)

	tests := []struct {
		name      string
		config    TofuExecutorConfig
		wantErr   bool
		errSubstr string
	}{
		{
			name: "default config with valid binary",
			config: TofuExecutorConfig{
				BasePath: t.TempDir(),
				Logger:   tofuTestLogger(),
			},
			wantErr: false,
		},
		{
			name: "custom base path",
			config: TofuExecutorConfig{
				BasePath: t.TempDir(),
				Binary:   "tofu",
				Logger:   tofuTestLogger(),
			},
			wantErr: false,
		},
		{
			name: "custom plugin cache directory",
			config: TofuExecutorConfig{
				BasePath:       t.TempDir(),
				PluginCacheDir: filepath.Join(t.TempDir(), "plugin-cache"),
				Logger:         tofuTestLogger(),
			},
			wantErr: false,
		},
		{
			name: "custom default timeout",
			config: TofuExecutorConfig{
				BasePath:       t.TempDir(),
				DefaultTimeout: 5 * time.Minute,
				Logger:         tofuTestLogger(),
			},
			wantErr: false,
		},
		{
			name: "nil logger uses default",
			config: TofuExecutorConfig{
				BasePath: t.TempDir(),
				Logger:   nil,
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			exec, err := NewTofuExecutor(tt.config)
			if tt.wantErr {
				if err == nil {
					t.Error("NewTofuExecutor() expected error but got nil")
				} else if tt.errSubstr != "" && !strings.Contains(err.Error(), tt.errSubstr) {
					t.Errorf("NewTofuExecutor() error = %q, want containing %q", err.Error(), tt.errSubstr)
				}
				return
			}
			if err != nil {
				t.Errorf("NewTofuExecutor() unexpected error: %v", err)
				return
			}
			if exec == nil {
				t.Error("NewTofuExecutor() returned nil executor")
			}
		})
	}
}

func TestNewTofuExecutor_BinaryNotFound(t *testing.T) {
	// Use a non-existent binary
	config := TofuExecutorConfig{
		BasePath: t.TempDir(),
		Binary:   "nonexistent-tofu-binary-12345",
		Logger:   tofuTestLogger(),
	}

	_, err := NewTofuExecutor(config)
	if err == nil {
		t.Error("NewTofuExecutor() expected error for non-existent binary")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("NewTofuExecutor() error should mention 'not found', got: %v", err)
	}
}

func TestNewTofuExecutor_InvalidBasePath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Skipping on Windows: permission tests behave differently")
	}

	// Create a file (not directory) to use as base path
	tempFile := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(tempFile, []byte("test"), 0644); err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}

	// Try to create executor with file as base path (should fail)
	config := TofuExecutorConfig{
		BasePath: filepath.Join(tempFile, "subdir"),
		Binary:   "tofu",
		Logger:   tofuTestLogger(),
	}

	_, err := NewTofuExecutor(config)
	if err == nil {
		t.Error("NewTofuExecutor() expected error for invalid base path")
	}
}

// =============================================================================
// TofuOperation Constants Tests
// =============================================================================

// =============================================================================
// Default Timeout Constants Tests
// =============================================================================

// =============================================================================
// ValidateRequest / Execute Tests
// =============================================================================

func TestTofuExecutor_Execute_ValidationErrors(t *testing.T) {
	// Create fake binary for executor initialization
	tempDir := t.TempDir()
	fakeTofuBinary := filepath.Join(tempDir, "tofu")
	if runtime.GOOS == "windows" {
		fakeTofuBinary += ".exe"
	}
	f, _ := os.OpenFile(fakeTofuBinary, os.O_CREATE|os.O_WRONLY, 0755)
	f.Close()

	oldPath := os.Getenv("PATH")
	os.Setenv("PATH", tempDir+string(os.PathListSeparator)+oldPath)
	defer os.Setenv("PATH", oldPath)

	executor, err := NewTofuExecutor(TofuExecutorConfig{
		BasePath: t.TempDir(),
		Logger:   tofuTestLogger(),
	})
	if err != nil {
		t.Fatalf("Failed to create executor: %v", err)
	}

	ctx := context.Background()

	tests := []struct {
		name      string
		request   *TofuRequest
		wantErr   bool
		errSubstr string
	}{
		{
			name: "empty workspace name",
			request: &TofuRequest{
				WorkspaceName: "",
				Operation:     TofuOperationInit,
			},
			wantErr:   true,
			errSubstr: "workspace_name is required",
		},
		{
			name: "path traversal with ..",
			request: &TofuRequest{
				WorkspaceName: "../../../etc",
				Operation:     TofuOperationInit,
			},
			wantErr:   true,
			errSubstr: "must not contain",
		},
		{
			name: "path traversal with forward slash",
			request: &TofuRequest{
				WorkspaceName: "foo/bar",
				Operation:     TofuOperationInit,
			},
			wantErr:   true,
			errSubstr: "must not contain",
		},
		{
			name: "path traversal with backslash",
			request: &TofuRequest{
				WorkspaceName: "foo\\bar",
				Operation:     TofuOperationInit,
			},
			wantErr:   true,
			errSubstr: "must not contain",
		},
		{
			name: "unknown operation",
			request: &TofuRequest{
				WorkspaceName: "test-workspace",
				Operation:     TofuOperation("unknown"),
			},
			wantErr:   true,
			errSubstr: "unknown operation",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := executor.Execute(ctx, tt.request)
			if tt.wantErr {
				if err == nil {
					t.Error("Execute() expected error but got nil")
				} else if tt.errSubstr != "" && !strings.Contains(err.Error(), tt.errSubstr) {
					t.Errorf("Execute() error = %q, want containing %q", err.Error(), tt.errSubstr)
				}
				return
			}
			if err != nil {
				t.Errorf("Execute() unexpected error: %v", err)
			}
		})
	}
}

func TestTofuExecutor_Execute_AdditionalFilesValidation(t *testing.T) {
	// Create fake binary
	tempDir := t.TempDir()
	fakeTofuBinary := filepath.Join(tempDir, "tofu")
	if runtime.GOOS == "windows" {
		fakeTofuBinary += ".exe"
	}
	f, _ := os.OpenFile(fakeTofuBinary, os.O_CREATE|os.O_WRONLY, 0755)
	f.Close()

	oldPath := os.Getenv("PATH")
	os.Setenv("PATH", tempDir+string(os.PathListSeparator)+oldPath)
	defer os.Setenv("PATH", oldPath)

	executor, err := NewTofuExecutor(TofuExecutorConfig{
		BasePath: t.TempDir(),
		Logger:   tofuTestLogger(),
	})
	if err != nil {
		t.Fatalf("Failed to create executor: %v", err)
	}

	ctx := context.Background()

	// Test path traversal in additional files
	_, err = executor.Execute(ctx, &TofuRequest{
		WorkspaceName: "test-workspace",
		Operation:     TofuOperationInit,
		AdditionalFiles: map[string]string{
			"../../../etc/passwd": "malicious content",
		},
	})
	if err == nil {
		t.Error("Execute() expected error for path traversal in additional files")
	}
	if !strings.Contains(err.Error(), "must not contain") {
		t.Errorf("Execute() error should mention path traversal, got: %v", err)
	}
}

// =============================================================================
// ParsePlanSummary Tests
// =============================================================================

func TestTofuExecutor_ParsePlanOutput(t *testing.T) {
	// Create a minimal executor for testing the parse function
	tempDir := t.TempDir()
	fakeTofuBinary := filepath.Join(tempDir, "tofu")
	if runtime.GOOS == "windows" {
		fakeTofuBinary += ".exe"
	}
	f, _ := os.OpenFile(fakeTofuBinary, os.O_CREATE|os.O_WRONLY, 0755)
	f.Close()

	oldPath := os.Getenv("PATH")
	os.Setenv("PATH", tempDir+string(os.PathListSeparator)+oldPath)
	defer os.Setenv("PATH", oldPath)

	executor, err := NewTofuExecutor(TofuExecutorConfig{
		BasePath: t.TempDir(),
		Logger:   tofuTestLogger(),
	})
	if err != nil {
		t.Fatalf("Failed to create executor: %v", err)
	}

	tests := []struct {
		name       string
		output     string
		wantAdd    int
		wantChange int
		wantDest   int
		hasChanges bool
	}{
		{
			name:       "standard plan output with changes",
			output:     "Plan: 3 to add, 2 to change, 1 to destroy.",
			wantAdd:    3,
			wantChange: 2,
			wantDest:   1,
			hasChanges: true,
		},
		{
			name:       "no changes",
			output:     "No changes. Your infrastructure matches the configuration.",
			wantAdd:    0,
			wantChange: 0,
			wantDest:   0,
			hasChanges: false,
		},
		{
			name:       "no changes lowercase",
			output:     "no changes detected in configuration",
			wantAdd:    0,
			wantChange: 0,
			wantDest:   0,
			hasChanges: false,
		},
		{
			name:       "only additions",
			output:     "Plan: 5 to add, 0 to change, 0 to destroy.",
			wantAdd:    5,
			wantChange: 0,
			wantDest:   0,
			hasChanges: true,
		},
		{
			name:       "only deletions",
			output:     "Plan: 0 to add, 0 to change, 3 to destroy.",
			wantAdd:    0,
			wantChange: 0,
			wantDest:   3,
			hasChanges: true,
		},
		{
			name:       "only changes",
			output:     "Plan: 0 to add, 7 to change, 0 to destroy.",
			wantAdd:    0,
			wantChange: 7,
			wantDest:   0,
			hasChanges: true,
		},
		{
			name:       "large numbers",
			output:     "Plan: 100 to add, 50 to change, 25 to destroy.",
			wantAdd:    100,
			wantChange: 50,
			wantDest:   25,
			hasChanges: true,
		},
		{
			name:       "embedded in other output",
			output:     "Some terraform output\nPlan: 1 to add, 2 to change, 3 to destroy.\nMore output",
			wantAdd:    1,
			wantChange: 2,
			wantDest:   3,
			hasChanges: true,
		},
		{
			name:       "empty output",
			output:     "",
			wantAdd:    0,
			wantChange: 0,
			wantDest:   0,
			hasChanges: false,
		},
		{
			name:       "malformed output",
			output:     "This is not a valid plan output",
			wantAdd:    0,
			wantChange: 0,
			wantDest:   0,
			hasChanges: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			summary := executor.parsePlanOutput(tt.output)
			if summary == nil {
				t.Fatal("parsePlanOutput() returned nil")
			}
			if summary.ToAdd != tt.wantAdd {
				t.Errorf("ToAdd = %d, want %d", summary.ToAdd, tt.wantAdd)
			}
			if summary.ToChange != tt.wantChange {
				t.Errorf("ToChange = %d, want %d", summary.ToChange, tt.wantChange)
			}
			if summary.ToDestroy != tt.wantDest {
				t.Errorf("ToDestroy = %d, want %d", summary.ToDestroy, tt.wantDest)
			}
			if summary.HasChanges != tt.hasChanges {
				t.Errorf("HasChanges = %v, want %v", summary.HasChanges, tt.hasChanges)
			}
		})
	}
}

// =============================================================================
// GetWorkingDir / BasePath / WorkspaceDir Tests
// =============================================================================

func TestTofuExecutor_WorkspaceHelpers(t *testing.T) {
	tempDir := t.TempDir()
	fakeTofuBinary := filepath.Join(tempDir, "tofu")
	if runtime.GOOS == "windows" {
		fakeTofuBinary += ".exe"
	}
	f, _ := os.OpenFile(fakeTofuBinary, os.O_CREATE|os.O_WRONLY, 0755)
	f.Close()

	oldPath := os.Getenv("PATH")
	os.Setenv("PATH", tempDir+string(os.PathListSeparator)+oldPath)
	defer os.Setenv("PATH", oldPath)

	basePath := t.TempDir()
	executor, err := NewTofuExecutor(TofuExecutorConfig{
		BasePath: basePath,
		Logger:   tofuTestLogger(),
	})
	if err != nil {
		t.Fatalf("Failed to create executor: %v", err)
	}

	t.Run("BasePath", func(t *testing.T) {
		if executor.BasePath() != basePath {
			t.Errorf("BasePath() = %q, want %q", executor.BasePath(), basePath)
		}
	})

	t.Run("WorkspaceDir", func(t *testing.T) {
		workspaceName := "test-workspace"
		expected := filepath.Join(basePath, workspaceName)
		if executor.WorkspaceDir(workspaceName) != expected {
			t.Errorf("WorkspaceDir(%q) = %q, want %q", workspaceName, executor.WorkspaceDir(workspaceName), expected)
		}
	})

	t.Run("WorkspaceExists - not exists", func(t *testing.T) {
		if executor.WorkspaceExists("nonexistent-workspace") {
			t.Error("WorkspaceExists() = true for non-existent workspace")
		}
	})

	t.Run("WorkspaceExists - exists", func(t *testing.T) {
		workspaceName := "existing-workspace"
		workspaceDir := filepath.Join(basePath, workspaceName)
		if err := os.MkdirAll(workspaceDir, 0755); err != nil {
			t.Fatalf("Failed to create workspace directory: %v", err)
		}
		if !executor.WorkspaceExists(workspaceName) {
			t.Error("WorkspaceExists() = false for existing workspace")
		}
	})
}

func TestTofuExecutor_ListWorkspaces(t *testing.T) {
	tempDir := t.TempDir()
	fakeTofuBinary := filepath.Join(tempDir, "tofu")
	if runtime.GOOS == "windows" {
		fakeTofuBinary += ".exe"
	}
	f, _ := os.OpenFile(fakeTofuBinary, os.O_CREATE|os.O_WRONLY, 0755)
	f.Close()

	oldPath := os.Getenv("PATH")
	os.Setenv("PATH", tempDir+string(os.PathListSeparator)+oldPath)
	defer os.Setenv("PATH", oldPath)

	basePath := t.TempDir()
	executor, err := NewTofuExecutor(TofuExecutorConfig{
		BasePath: basePath,
		Logger:   tofuTestLogger(),
	})
	if err != nil {
		t.Fatalf("Failed to create executor: %v", err)
	}

	t.Run("empty workspaces", func(t *testing.T) {
		workspaces, err := executor.ListWorkspaces()
		if err != nil {
			t.Errorf("ListWorkspaces() error: %v", err)
		}
		if len(workspaces) != 0 {
			t.Errorf("ListWorkspaces() = %v, want empty", workspaces)
		}
	})

	t.Run("multiple workspaces", func(t *testing.T) {
		// Create some workspaces
		expected := []string{"ws1", "ws2", "ws3"}
		for _, ws := range expected {
			if err := os.MkdirAll(filepath.Join(basePath, ws), 0755); err != nil {
				t.Fatalf("Failed to create workspace %s: %v", ws, err)
			}
		}

		// Create a file (should not be listed)
		if err := os.WriteFile(filepath.Join(basePath, "not-a-workspace.txt"), []byte("test"), 0644); err != nil {
			t.Fatalf("Failed to create test file: %v", err)
		}

		workspaces, err := executor.ListWorkspaces()
		if err != nil {
			t.Errorf("ListWorkspaces() error: %v", err)
		}
		if len(workspaces) != len(expected) {
			t.Errorf("ListWorkspaces() returned %d workspaces, want %d", len(workspaces), len(expected))
		}
	})
}

func TestTofuExecutor_CleanupWorkspace(t *testing.T) {
	tempDir := t.TempDir()
	fakeTofuBinary := filepath.Join(tempDir, "tofu")
	if runtime.GOOS == "windows" {
		fakeTofuBinary += ".exe"
	}
	f, _ := os.OpenFile(fakeTofuBinary, os.O_CREATE|os.O_WRONLY, 0755)
	f.Close()

	oldPath := os.Getenv("PATH")
	os.Setenv("PATH", tempDir+string(os.PathListSeparator)+oldPath)
	defer os.Setenv("PATH", oldPath)

	basePath := t.TempDir()
	executor, err := NewTofuExecutor(TofuExecutorConfig{
		BasePath: basePath,
		Logger:   tofuTestLogger(),
	})
	if err != nil {
		t.Fatalf("Failed to create executor: %v", err)
	}

	t.Run("cleanup existing workspace", func(t *testing.T) {
		workspaceName := "cleanup-test"
		workspaceDir := filepath.Join(basePath, workspaceName)

		// Create workspace with files
		if err := os.MkdirAll(workspaceDir, 0755); err != nil {
			t.Fatalf("Failed to create workspace: %v", err)
		}
		if err := os.WriteFile(filepath.Join(workspaceDir, "test.tf"), []byte("test"), 0644); err != nil {
			t.Fatalf("Failed to create test file: %v", err)
		}

		// Verify it exists
		if !executor.WorkspaceExists(workspaceName) {
			t.Fatal("Workspace should exist before cleanup")
		}

		// Cleanup
		if err := executor.CleanupWorkspace(workspaceName); err != nil {
			t.Errorf("CleanupWorkspace() error: %v", err)
		}

		// Verify it's gone
		if executor.WorkspaceExists(workspaceName) {
			t.Error("Workspace should not exist after cleanup")
		}
	})

	t.Run("cleanup non-existent workspace (no error)", func(t *testing.T) {
		err := executor.CleanupWorkspace("nonexistent-workspace")
		if err != nil {
			t.Errorf("CleanupWorkspace() should not error for non-existent workspace: %v", err)
		}
	})
}

// =============================================================================
// Security Validation Tests
// =============================================================================

func TestTofuExecutor_SecurityValidation_PathTraversal(t *testing.T) {
	tempDir := t.TempDir()
	fakeTofuBinary := filepath.Join(tempDir, "tofu")
	if runtime.GOOS == "windows" {
		fakeTofuBinary += ".exe"
	}
	f, _ := os.OpenFile(fakeTofuBinary, os.O_CREATE|os.O_WRONLY, 0755)
	f.Close()

	oldPath := os.Getenv("PATH")
	os.Setenv("PATH", tempDir+string(os.PathListSeparator)+oldPath)
	defer os.Setenv("PATH", oldPath)

	executor, err := NewTofuExecutor(TofuExecutorConfig{
		BasePath: t.TempDir(),
		Logger:   tofuTestLogger(),
	})
	if err != nil {
		t.Fatalf("Failed to create executor: %v", err)
	}

	ctx := context.Background()

	pathTraversalTests := []struct {
		name      string
		workspace string
	}{
		{"double dot", ".."},
		{"double dot prefix", "../secret"},
		{"double dot middle", "foo/../bar"},
		{"double dot suffix", "foo/.."},
		{"multiple double dots", "../../.."},
		{"forward slash", "foo/bar"},
		{"backslash", "foo\\bar"},
		{"mixed separators", "foo/bar\\baz"},
		{"absolute path unix", "/etc/passwd"},
		{"absolute path windows", "C:\\Windows"},
	}

	for _, tt := range pathTraversalTests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := executor.Execute(ctx, &TofuRequest{
				WorkspaceName: tt.workspace,
				Operation:     TofuOperationInit,
			})
			if err == nil {
				t.Errorf("Execute() should reject workspace name %q", tt.workspace)
			}
		})
	}
}

func TestTofuExecutor_SecurityValidation_AdditionalFiles(t *testing.T) {
	tempDir := t.TempDir()
	fakeTofuBinary := filepath.Join(tempDir, "tofu")
	if runtime.GOOS == "windows" {
		fakeTofuBinary += ".exe"
	}
	f, _ := os.OpenFile(fakeTofuBinary, os.O_CREATE|os.O_WRONLY, 0755)
	f.Close()

	oldPath := os.Getenv("PATH")
	os.Setenv("PATH", tempDir+string(os.PathListSeparator)+oldPath)
	defer os.Setenv("PATH", oldPath)

	executor, err := NewTofuExecutor(TofuExecutorConfig{
		BasePath: t.TempDir(),
		Logger:   tofuTestLogger(),
	})
	if err != nil {
		t.Fatalf("Failed to create executor: %v", err)
	}

	ctx := context.Background()

	maliciousFiles := []struct {
		name     string
		filename string
	}{
		{"path traversal", "../../../etc/passwd"},
		{"double dot in path", "subdir/../../../etc/passwd"},
		{"embedded traversal", "foo/..bar/baz"},
	}

	for _, tt := range maliciousFiles {
		t.Run(tt.name, func(t *testing.T) {
			_, err := executor.Execute(ctx, &TofuRequest{
				WorkspaceName: "valid-workspace",
				Operation:     TofuOperationInit,
				AdditionalFiles: map[string]string{
					tt.filename: "malicious content",
				},
			})
			if err == nil {
				t.Errorf("Execute() should reject malicious filename %q", tt.filename)
			}
		})
	}
}

// =============================================================================
// TofuRequest/TofuResult Structure Tests
// =============================================================================

func TestTofuRequest_Structure(t *testing.T) {
	req := &TofuRequest{
		WorkspaceName:   "my-workspace",
		Operation:       TofuOperationPlan,
		HCLContent:      `resource "local_file" "test" { content = "hello" }`,
		TFVarsJSON:      `{"var1": "value1"}`,
		AdditionalFiles: map[string]string{"extra.tf": "output {}"},
		Timeout:         5 * time.Minute,
	}

	if req.WorkspaceName != "my-workspace" {
		t.Error("WorkspaceName not set correctly")
	}
	if req.Operation != TofuOperationPlan {
		t.Error("Operation not set correctly")
	}
	if req.HCLContent == "" {
		t.Error("HCLContent should not be empty")
	}
	if req.TFVarsJSON == "" {
		t.Error("TFVarsJSON should not be empty")
	}
	if len(req.AdditionalFiles) != 1 {
		t.Error("AdditionalFiles should have 1 entry")
	}
	if req.Timeout != 5*time.Minute {
		t.Error("Timeout not set correctly")
	}
}

func TestTofuResult_Structure(t *testing.T) {
	result := &TofuResult{
		Success:    true,
		Operation:  TofuOperationApply,
		Stdout:     "Apply complete!",
		Stderr:     "",
		ExitCode:   0,
		Duration:   10 * time.Second,
		WorkingDir: "/tmp/test",
		PlanSummary: &TofuPlanSummary{
			ToAdd:      1,
			ToChange:   2,
			ToDestroy:  0,
			HasChanges: true,
		},
		Outputs: map[string]TofuOutputValue{
			"test_output": {Value: "hello", Type: "string", Sensitive: false},
		},
	}

	if !result.Success {
		t.Error("Success should be true")
	}
	if result.Operation != TofuOperationApply {
		t.Error("Operation should be apply")
	}
	if result.PlanSummary == nil {
		t.Error("PlanSummary should not be nil")
	}
	if result.PlanSummary.ToAdd != 1 {
		t.Error("PlanSummary.ToAdd should be 1")
	}
	if len(result.Outputs) != 1 {
		t.Error("Outputs should have 1 entry")
	}
}

func TestTofuPlanSummary_Structure(t *testing.T) {
	tests := []struct {
		name       string
		summary    TofuPlanSummary
		hasChanges bool
	}{
		{
			name: "has additions",
			summary: TofuPlanSummary{
				ToAdd:      5,
				ToChange:   0,
				ToDestroy:  0,
				HasChanges: true,
			},
			hasChanges: true,
		},
		{
			name: "has changes",
			summary: TofuPlanSummary{
				ToAdd:      0,
				ToChange:   3,
				ToDestroy:  0,
				HasChanges: true,
			},
			hasChanges: true,
		},
		{
			name: "has deletions",
			summary: TofuPlanSummary{
				ToAdd:      0,
				ToChange:   0,
				ToDestroy:  2,
				HasChanges: true,
			},
			hasChanges: true,
		},
		{
			name: "no changes",
			summary: TofuPlanSummary{
				ToAdd:      0,
				ToChange:   0,
				ToDestroy:  0,
				HasChanges: false,
			},
			hasChanges: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.summary.HasChanges != tt.hasChanges {
				t.Errorf("HasChanges = %v, want %v", tt.summary.HasChanges, tt.hasChanges)
			}
		})
	}
}

func TestTofuOutputValue_Structure(t *testing.T) {
	tests := []struct {
		name   string
		output TofuOutputValue
	}{
		{
			name: "string value",
			output: TofuOutputValue{
				Value:     "hello",
				Type:      "string",
				Sensitive: false,
			},
		},
		{
			name: "sensitive value",
			output: TofuOutputValue{
				Value:     "secret123",
				Type:      "string",
				Sensitive: true,
			},
		},
		{
			name: "list value",
			output: TofuOutputValue{
				Value:     []string{"a", "b", "c"},
				Type:      "list",
				Sensitive: false,
			},
		},
		{
			name: "map value",
			output: TofuOutputValue{
				Value:     map[string]interface{}{"key": "value"},
				Type:      "map",
				Sensitive: false,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.output.Value == nil {
				t.Error("Value should not be nil")
			}
		})
	}
}

// =============================================================================
// gRPC Integration Tests
// =============================================================================

func TestTofuExecutor_ExecuteGRPC_ValidationErrors(t *testing.T) {
	tempDir := t.TempDir()
	fakeTofuBinary := filepath.Join(tempDir, "tofu")
	if runtime.GOOS == "windows" {
		fakeTofuBinary += ".exe"
	}
	f, _ := os.OpenFile(fakeTofuBinary, os.O_CREATE|os.O_WRONLY, 0755)
	f.Close()

	oldPath := os.Getenv("PATH")
	os.Setenv("PATH", tempDir+string(os.PathListSeparator)+oldPath)
	defer os.Setenv("PATH", oldPath)

	executor, err := NewTofuExecutor(TofuExecutorConfig{
		BasePath: t.TempDir(),
		Logger:   tofuTestLogger(),
	})
	if err != nil {
		t.Fatalf("Failed to create executor: %v", err)
	}

	ctx := context.Background()

	t.Run("unknown operation", func(t *testing.T) {
		cmd := &agentpb.TofuCommand{
			CommandId:        "test-cmd-1",
			Operation:        agentpb.TofuOperation_TOFU_OPERATION_UNSPECIFIED,
			WorkingDirectory: t.TempDir(),
		}

		result := executor.ExecuteGRPC(ctx, cmd)
		if result.Success {
			t.Error("ExecuteGRPC() should fail for unspecified operation")
		}
		if result.CommandId != "test-cmd-1" {
			t.Errorf("CommandId = %q, want %q", result.CommandId, "test-cmd-1")
		}
	})
}

func TestTofuExecutor_ExecuteGRPC_RejectsEscapingWorkingDirectory(t *testing.T) {
	basePath := t.TempDir()
	executor, err := NewTofuExecutor(TofuExecutorConfig{
		BasePath: basePath,
		Binary:   os.Args[0],
		Logger:   tofuTestLogger(),
	})
	if err != nil {
		t.Fatalf("Failed to create executor: %v", err)
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
			result := executor.ExecuteGRPC(context.Background(), &agentpb.TofuCommand{
				CommandId:        "cmd-" + strings.ReplaceAll(tt.name, " ", "-"),
				Operation:        agentpb.TofuOperation_TOFU_OPERATION_UNSPECIFIED,
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

func TestTofuExecutor_ExecuteGRPC_CommandIdPreserved(t *testing.T) {
	tempDir := t.TempDir()
	fakeTofuBinary := filepath.Join(tempDir, "tofu")
	if runtime.GOOS == "windows" {
		fakeTofuBinary += ".exe"
	}
	f, _ := os.OpenFile(fakeTofuBinary, os.O_CREATE|os.O_WRONLY, 0755)
	f.Close()

	oldPath := os.Getenv("PATH")
	os.Setenv("PATH", tempDir+string(os.PathListSeparator)+oldPath)
	defer os.Setenv("PATH", oldPath)

	executor, err := NewTofuExecutor(TofuExecutorConfig{
		BasePath: t.TempDir(),
		Logger:   tofuTestLogger(),
	})
	if err != nil {
		t.Fatalf("Failed to create executor: %v", err)
	}

	ctx := context.Background()

	cmd := &agentpb.TofuCommand{
		CommandId:        "unique-command-id-12345",
		Operation:        agentpb.TofuOperation_TOFU_OPERATION_UNSPECIFIED,
		WorkingDirectory: t.TempDir(),
	}

	result := executor.ExecuteGRPC(ctx, cmd)
	if result.CommandId != cmd.CommandId {
		t.Errorf("CommandId not preserved: got %q, want %q", result.CommandId, cmd.CommandId)
	}
	if result.StartedAtUnix == 0 {
		t.Error("StartedAtUnix should be set")
	}
	if result.FinishedAtUnix == 0 {
		t.Error("FinishedAtUnix should be set")
	}
}

// =============================================================================
// Directory Locking Tests
// =============================================================================

func TestTofuExecutor_LockKeyForDir(t *testing.T) {
	tempDir := t.TempDir()
	fakeTofuBinary := filepath.Join(tempDir, "tofu")
	if runtime.GOOS == "windows" {
		fakeTofuBinary += ".exe"
	}
	f, _ := os.OpenFile(fakeTofuBinary, os.O_CREATE|os.O_WRONLY, 0755)
	f.Close()

	oldPath := os.Getenv("PATH")
	os.Setenv("PATH", tempDir+string(os.PathListSeparator)+oldPath)
	defer os.Setenv("PATH", oldPath)

	executor, err := NewTofuExecutor(TofuExecutorConfig{
		BasePath: t.TempDir(),
		Logger:   tofuTestLogger(),
	})
	if err != nil {
		t.Fatalf("Failed to create executor: %v", err)
	}

	t.Run("empty directory", func(t *testing.T) {
		key := executor.lockKeyForDir("")
		if key != "" {
			t.Errorf("lockKeyForDir(\"\") = %q, want empty", key)
		}
	})

	t.Run("relative path normalized", func(t *testing.T) {
		key := executor.lockKeyForDir("./test/path")
		if key == "" {
			t.Error("lockKeyForDir should return non-empty for valid path")
		}
	})

	t.Run("same path returns same key", func(t *testing.T) {
		dir := t.TempDir()
		key1 := executor.lockKeyForDir(dir)
		key2 := executor.lockKeyForDir(dir)
		if key1 != key2 {
			t.Errorf("Same directory should return same key: %q vs %q", key1, key2)
		}
	})
}

// =============================================================================
// calculateStateHash Tests
// =============================================================================

func TestCalculateStateHash(t *testing.T) {
	t.Run("valid state file", func(t *testing.T) {
		tempFile := filepath.Join(t.TempDir(), "terraform.tfstate")
		content := `{"version": 4, "resources": []}`
		if err := os.WriteFile(tempFile, []byte(content), 0644); err != nil {
			t.Fatalf("Failed to write state file: %v", err)
		}

		hash, err := calculateStateHash(tempFile)
		if err != nil {
			t.Errorf("calculateStateHash() error: %v", err)
		}
		if hash == "" {
			t.Error("calculateStateHash() returned empty hash")
		}
		// SHA256 hash should be 64 characters
		if len(hash) != 64 {
			t.Errorf("calculateStateHash() hash length = %d, want 64", len(hash))
		}
	})

	t.Run("non-existent file", func(t *testing.T) {
		_, err := calculateStateHash("/nonexistent/path/terraform.tfstate")
		if err == nil {
			t.Error("calculateStateHash() should error for non-existent file")
		}
	})

	t.Run("same content same hash", func(t *testing.T) {
		content := `{"version": 4, "resources": []}`

		tempFile1 := filepath.Join(t.TempDir(), "state1.tfstate")
		tempFile2 := filepath.Join(t.TempDir(), "state2.tfstate")

		os.WriteFile(tempFile1, []byte(content), 0644)
		os.WriteFile(tempFile2, []byte(content), 0644)

		hash1, _ := calculateStateHash(tempFile1)
		hash2, _ := calculateStateHash(tempFile2)

		if hash1 != hash2 {
			t.Errorf("Same content should produce same hash: %q vs %q", hash1, hash2)
		}
	})

	t.Run("different content different hash", func(t *testing.T) {
		tempFile1 := filepath.Join(t.TempDir(), "state1.tfstate")
		tempFile2 := filepath.Join(t.TempDir(), "state2.tfstate")

		os.WriteFile(tempFile1, []byte(`{"version": 4}`), 0644)
		os.WriteFile(tempFile2, []byte(`{"version": 5}`), 0644)

		hash1, _ := calculateStateHash(tempFile1)
		hash2, _ := calculateStateHash(tempFile2)

		if hash1 == hash2 {
			t.Error("Different content should produce different hash")
		}
	})
}
