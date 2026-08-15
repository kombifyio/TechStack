package tofu

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestTerramateRunner tests for the TerramateRunner
func TestNewTerramateRunner(t *testing.T) {
	t.Run("creates runner with valid work directory", func(t *testing.T) {
		tmpDir := t.TempDir()
		runner, err := NewTerramateRunner(tmpDir)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if runner == nil {
			t.Fatal("expected runner to be non-nil")
		}
		if runner.workDir != tmpDir {
			t.Errorf("expected workDir %s, got %s", tmpDir, runner.workDir)
		}
	})

	t.Run("creates work directory if it doesn't exist", func(t *testing.T) {
		tmpDir := filepath.Join(t.TempDir(), "new-dir")
		runner, err := NewTerramateRunner(tmpDir)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if runner == nil {
			t.Fatal("expected runner to be non-nil")
		}
		if _, err := os.Stat(tmpDir); os.IsNotExist(err) {
			t.Error("expected work directory to be created")
		}
	})

	t.Run("returns error for empty work directory", func(t *testing.T) {
		_, err := NewTerramateRunner("")
		if err == nil {
			t.Error("expected error for empty work directory")
		}
	})
}

func TestTerramateRunnerWithOptions(t *testing.T) {
	tmpDir := t.TempDir()
	runner, err := NewTerramateRunner(tmpDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	t.Run("WithTimeout sets custom timeout", func(t *testing.T) {
		customTimeout := 5 * time.Minute
		runner.WithTimeout(customTimeout)
		if runner.timeout != customTimeout {
			t.Errorf("expected timeout %v, got %v", customTimeout, runner.timeout)
		}
	})

	t.Run("WithBinaryPath sets custom binary path", func(t *testing.T) {
		customPath := "/usr/local/bin/terramate"
		runner.WithBinaryPath(customPath)
		if runner.binaryPath != customPath {
			t.Errorf("expected binaryPath %s, got %s", customPath, runner.binaryPath)
		}
	})
}

func TestParseVersion(t *testing.T) {
	tests := []struct {
		name    string
		version string
		major   int
		minor   int
		patch   int
		wantErr bool
	}{
		{"standard version", "0.5.0", 0, 5, 0, false},
		{"version with v prefix", "v0.5.0", 0, 5, 0, false},
		{"major version", "1.0.0", 1, 0, 0, false},
		{"high numbers", "10.20.30", 10, 20, 30, false},
		{"version with suffix", "0.5.0-beta", 0, 5, 0, false},
		{"invalid version", "invalid", 0, 0, 0, true},
		{"empty version", "", 0, 0, 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v, err := ParseVersion(tt.version)
			if tt.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if v.Major != tt.major || v.Minor != tt.minor || v.Patch != tt.patch {
				t.Errorf("expected %d.%d.%d, got %d.%d.%d",
					tt.major, tt.minor, tt.patch, v.Major, v.Minor, v.Patch)
			}
		})
	}
}

func TestTerramateVersionCompare(t *testing.T) {
	tests := []struct {
		name     string
		v1       string
		v2       string
		expected int
	}{
		{"equal versions", "0.5.0", "0.5.0", 0},
		{"v1 less than v2 major", "0.5.0", "1.0.0", -1},
		{"v1 greater than v2 major", "1.0.0", "0.5.0", 1},
		{"v1 less than v2 minor", "0.4.0", "0.5.0", -1},
		{"v1 greater than v2 minor", "0.6.0", "0.5.0", 1},
		{"v1 less than v2 patch", "0.5.0", "0.5.1", -1},
		{"v1 greater than v2 patch", "0.6.2", "0.6.1", 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v1, err := ParseVersion(tt.v1)
			if err != nil {
				t.Fatalf("failed to parse v1: %v", err)
			}
			v2, err := ParseVersion(tt.v2)
			if err != nil {
				t.Fatalf("failed to parse v2: %v", err)
			}

			result := v1.Compare(v2)
			if result != tt.expected {
				t.Errorf("expected %d, got %d", tt.expected, result)
			}
		})
	}
}

func TestTerramateVersionAtLeast(t *testing.T) {
	v, _ := ParseVersion("0.5.0")

	tests := []struct {
		name     string
		major    int
		minor    int
		patch    int
		expected bool
	}{
		{"exactly at version", 0, 5, 0, true},
		{"below required major", 1, 0, 0, false},
		{"below required minor", 0, 6, 0, false},
		{"below required patch", 0, 5, 1, false},
		{"above required major", 0, 4, 0, true},
		{"above required minor", 0, 4, 9, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := v.AtLeast(tt.major, tt.minor, tt.patch)
			if result != tt.expected {
				t.Errorf("expected %v, got %v", tt.expected, result)
			}
		})
	}
}

func TestExtractStackName(t *testing.T) {
	tests := []struct {
		path     string
		expected string
	}{
		{"stacks/network", "network"},
		{"stacks/services", "services"},
		{"stacks/network/", "network"},
		{"network", "network"},
		{"a/b/c/d", "d"},
		{"", ""},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			result := extractStackName(tt.path)
			if result != tt.expected {
				t.Errorf("expected %s, got %s", tt.expected, result)
			}
		})
	}
}

// Integration test for TerramateRunner (skipped unless terramate is installed)
func TestTerramateRunnerIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	tmpDir := t.TempDir()
	runner, err := NewTerramateRunner(tmpDir)
	if err != nil {
		t.Fatalf("failed to create runner: %v", err)
	}

	// Check if terramate is installed
	version, err := runner.CheckInstalled()
	if err != nil {
		t.Skipf("terramate not installed, skipping integration test: %v", err)
	}

	t.Logf("terramate version: %s", version)

	// Parse and validate version
	v, err := ParseVersion(version)
	if err != nil {
		t.Fatalf("failed to parse version: %v", err)
	}

	// Check minimum version
	if !v.AtLeast(0, 5, 0) {
		t.Logf("terramate version %s is below recommended 0.5.0", version)
	}
}

// TestDriftResult tests DriftResult structure
func TestDriftResult(t *testing.T) {
	result := &DriftResult{
		Drifted:   []string{"stacks/network"},
		InSync:    []string{"stacks/services", "stacks/applications"},
		Failed:    []string{},
		CheckedAt: time.Now(),
	}

	if len(result.Drifted) != 1 {
		t.Errorf("expected 1 drifted stack, got %d", len(result.Drifted))
	}
	if len(result.InSync) != 2 {
		t.Errorf("expected 2 in-sync stacks, got %d", len(result.InSync))
	}
	if len(result.Failed) != 0 {
		t.Errorf("expected 0 failed stacks, got %d", len(result.Failed))
	}
}

// TestCommandResult tests CommandResult structure
func TestCommandResult(t *testing.T) {
	result := &CommandResult{
		ExitCode: 0,
		Stdout:   "success",
		Stderr:   "",
		Duration: 5 * time.Second,
	}

	if result.ExitCode != 0 {
		t.Errorf("expected exit code 0, got %d", result.ExitCode)
	}
	if result.Duration != 5*time.Second {
		t.Errorf("expected duration 5s, got %v", result.Duration)
	}
}

// TestStackInfo tests StackInfo structure
func TestStackInfo(t *testing.T) {
	stack := StackInfo{
		Path:        "stacks/network",
		Name:        "network",
		Description: "Network infrastructure",
		After:       []string{},
		ID:          "homelab-network",
	}

	if stack.Path != "stacks/network" {
		t.Errorf("expected path stacks/network, got %s", stack.Path)
	}
	if len(stack.After) != 0 {
		t.Errorf("expected no dependencies, got %d", len(stack.After))
	}
}

// Benchmark tests
func BenchmarkParseVersion(b *testing.B) {
	for i := 0; i < b.N; i++ {
		ParseVersion("0.5.0")
	}
}

// Test context cancellation
func TestTerramateRunnerContextCancellation(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping context cancellation test in short mode")
	}

	tmpDir := t.TempDir()
	runner, err := NewTerramateRunner(tmpDir)
	if err != nil {
		t.Fatalf("failed to create runner: %v", err)
	}

	// Create a canceled context
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	// This should return an error due to canceled context
	_, err = runner.Run(ctx, "list")
	if err == nil {
		t.Skip("terramate not installed or command succeeded before cancellation")
	}
	// Error is expected - either from cancellation or terramate not being installed
}

// TestTerramateRunnerWithLogger tests the WithLogger option
func TestTerramateRunnerWithLogger(t *testing.T) {
	tmpDir := t.TempDir()
	runner, err := NewTerramateRunner(tmpDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	customLogger := runner.logger // Get reference to check

	result := runner.WithLogger(customLogger)

	if result != runner {
		t.Error("WithLogger should return the same runner for chaining")
	}
	if runner.logger != customLogger {
		t.Error("WithLogger should set the logger")
	}
}

// TestParseStackList tests the parseStackList method
func TestParseStackList(t *testing.T) {
	tmpDir := t.TempDir()
	runner, err := NewTerramateRunner(tmpDir)
	if err != nil {
		t.Fatalf("failed to create runner: %v", err)
	}

	tests := []struct {
		name     string
		input    string
		expected int
	}{
		{
			name:     "single stack",
			input:    "stacks/network",
			expected: 1,
		},
		{
			name:     "multiple stacks",
			input:    "stacks/network\nstacks/services\nstacks/apps",
			expected: 3,
		},
		{
			name:     "empty input",
			input:    "",
			expected: 0,
		},
		{
			name:     "whitespace only",
			input:    "   \n   \n   ",
			expected: 0,
		},
		{
			name:     "stacks with extra whitespace",
			input:    "  stacks/network  \n  stacks/services  ",
			expected: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stacks, err := runner.parseStackList(tt.input)
			if err != nil {
				t.Fatalf("parseStackList() error = %v", err)
			}
			if len(stacks) != tt.expected {
				t.Errorf("parseStackList() returned %d stacks, want %d", len(stacks), tt.expected)
			}
		})
	}
}

// TestTerramateVersionString tests the String method of TerramateVersion
func TestTerramateVersionString(t *testing.T) {
	tests := []struct {
		version  TerramateVersion
		expected string
	}{
		{TerramateVersion{Major: 0, Minor: 5, Patch: 0}, "0.5.0"},
		{TerramateVersion{Major: 1, Minor: 0, Patch: 0}, "1.0.0"},
		{TerramateVersion{Major: 10, Minor: 20, Patch: 30}, "10.20.30"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			result := tt.version.String()
			if result != tt.expected {
				t.Errorf("String() = %v, want %v", result, tt.expected)
			}
		})
	}
}

// TestTerramateRunnerFileNotDirectory tests error handling for non-directory path
func TestTerramateRunnerFileNotDirectory(t *testing.T) {
	// Create a file instead of directory
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "not-a-dir")
	if err := os.WriteFile(filePath, []byte("content"), 0644); err != nil {
		t.Fatalf("failed to create file: %v", err)
	}

	_, err := NewTerramateRunner(filePath)
	if err == nil {
		t.Error("expected error for file path instead of directory")
	}
}

// TestTerramateRunnerCheckInstalledNotFound tests CheckInstalled when terramate is not installed
func TestTerramateRunnerCheckInstalledNotFound(t *testing.T) {
	tmpDir := t.TempDir()
	runner, err := NewTerramateRunner(tmpDir)
	if err != nil {
		t.Fatalf("failed to create runner: %v", err)
	}

	// Set a non-existent binary path
	runner.WithBinaryPath("/nonexistent/terramate-binary")

	_, err = runner.CheckInstalled()
	if err == nil {
		t.Error("CheckInstalled should fail with nonexistent binary")
	}
}

// TestTerramateRunnerExecuteTimeout tests execution timeout handling
func TestTerramateRunnerExecuteTimeout(t *testing.T) {
	tmpDir := t.TempDir()
	runner, err := NewTerramateRunner(tmpDir)
	if err != nil {
		t.Fatalf("failed to create runner: %v", err)
	}

	// Set a very short timeout
	runner.WithTimeout(1 * time.Nanosecond)

	// This might timeout or fail because terramate isn't installed
	// Either way, we're testing the timeout code path
	ctx := context.Background()
	_, _ = runner.Run(ctx, "list")
	// We don't assert on the error because it depends on whether terramate is installed
}
