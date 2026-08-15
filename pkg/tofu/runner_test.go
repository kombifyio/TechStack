package tofu

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestDefaultRunner_bin(t *testing.T) {
	tests := []struct {
		name     string
		binary   string
		expected string
	}{
		{
			name:     "default binary",
			binary:   "",
			expected: "tofu",
		},
		{
			name:     "custom binary",
			binary:   "/usr/local/bin/tofu",
			expected: "/usr/local/bin/tofu",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := &DefaultRunner{Binary: tt.binary}
			if got := runner.bin(); got != tt.expected {
				t.Errorf("bin() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestDefaultRunner_Init_InvalidDir(t *testing.T) {
	runner := &DefaultRunner{}
	err := runner.Init("/nonexistent/directory")
	if err == nil {
		t.Error("Init() should fail with nonexistent directory")
	}
}

func TestDefaultRunner_Plan_InvalidDir(t *testing.T) {
	runner := &DefaultRunner{}
	err := runner.Plan("/nonexistent/directory")
	if err == nil {
		t.Error("Plan() should fail with nonexistent directory")
	}
}

func TestDefaultRunner_Apply_InvalidDir(t *testing.T) {
	runner := &DefaultRunner{}
	err := runner.Apply("/nonexistent/directory")
	if err == nil {
		t.Error("Apply() should fail with nonexistent directory")
	}
}

func TestDefaultRunner_Destroy_InvalidDir(t *testing.T) {
	runner := &DefaultRunner{}
	err := runner.Destroy("/nonexistent/directory")
	if err == nil {
		t.Error("Destroy() should fail with nonexistent directory")
	}
}

func TestRunnerInterface(t *testing.T) {
	// Ensure DefaultRunner implements Runner interface
	var _ Runner = &DefaultRunner{}
}

func TestDefaultRunner_Init(t *testing.T) {
	// Skip if OpenTofu is not installed
	if _, err := exec.LookPath("tofu"); err != nil {
		t.Skip("OpenTofu not installed (tofu not in PATH), skipping test")
	}

	tmpDir := t.TempDir()

	// Create a minimal terraform config
	mainTf := filepath.Join(tmpDir, "main.tf")
	if err := os.WriteFile(mainTf, []byte(`terraform {}`), 0644); err != nil {
		t.Fatalf("Failed to create main.tf: %v", err)
	}

	runner := &DefaultRunner{}
	err := runner.Init(tmpDir)
	if err != nil {
		t.Errorf("Init() error = %v", err)
	}
}

func TestAcquireStateLock(t *testing.T) {
	t.Run("acquires lock for directory", func(t *testing.T) {
		dir := "/test/dir1"
		unlock := acquireStateLock(dir)
		if unlock == nil {
			t.Error("acquireStateLock should return unlock function")
		}
		unlock()
	})

	t.Run("same directory returns same lock", func(t *testing.T) {
		dir := "/test/dir2"
		unlock1 := acquireStateLock(dir)
		unlock1() // Release first lock

		unlock2 := acquireStateLock(dir)
		unlock2() // Release second lock

		// If we got here without deadlock, the test passes
	})

	t.Run("different directories get different locks", func(t *testing.T) {
		dir1 := "/test/dir3"
		dir2 := "/test/dir4"

		// Acquire both locks - should not deadlock
		unlock1 := acquireStateLock(dir1)
		unlock2 := acquireStateLock(dir2)

		unlock1()
		unlock2()
	})

	t.Run("concurrent access to same directory is serialized", func(t *testing.T) {
		dir := "/test/concurrent"
		counter := 0
		done := make(chan bool, 10)

		for i := 0; i < 10; i++ {
			go func() {
				unlock := acquireStateLock(dir)
				defer unlock()
				// Simulate work
				counter++
				done <- true
			}()
		}

		// Wait for all goroutines
		for i := 0; i < 10; i++ {
			<-done
		}

		if counter != 10 {
			t.Errorf("expected counter=10, got %d", counter)
		}
	})

	t.Run("different path spellings share the same lock", func(t *testing.T) {
		base := t.TempDir()
		variant := filepath.Join(base, ".")

		unlock1 := acquireStateLock(base)

		acquired2 := make(chan struct{})
		go func() {
			unlock2 := acquireStateLock(variant)
			defer unlock2()
			close(acquired2)
		}()

		select {
		case <-acquired2:
			t.Fatal("expected second lock acquisition to block while first is held")
		case <-time.After(50 * time.Millisecond):
			// ok
		}

		// Once the first lock is released (via defer), the second should acquire quickly.
		unlock1()
		select {
		case <-acquired2:
			// ok
		case <-time.After(2 * time.Second):
			t.Fatal("expected second lock acquisition after releasing first")
		}
	})
}

func TestGetPluginCacheDir(t *testing.T) {
	t.Run("returns env var if set", func(t *testing.T) {
		originalEnv := os.Getenv("TF_PLUGIN_CACHE_DIR")
		defer os.Setenv("TF_PLUGIN_CACHE_DIR", originalEnv)

		testDir := "/custom/cache/dir"
		os.Setenv("TF_PLUGIN_CACHE_DIR", testDir)

		result := getPluginCacheDir()
		if result != testDir {
			t.Errorf("getPluginCacheDir() = %v, want %v", result, testDir)
		}
	})

	t.Run("returns default if env not set", func(t *testing.T) {
		originalEnv := os.Getenv("TF_PLUGIN_CACHE_DIR")
		defer os.Setenv("TF_PLUGIN_CACHE_DIR", originalEnv)

		os.Unsetenv("TF_PLUGIN_CACHE_DIR")

		result := getPluginCacheDir()
		// Should return a path containing .terraform.d/plugin-cache
		if result == "" {
			t.Skip("Could not determine home directory")
		}
		if !filepath.IsAbs(result) {
			t.Errorf("getPluginCacheDir() should return absolute path, got %v", result)
		}
	})
}

func TestDefaultRunner_timeout(t *testing.T) {
	tests := []struct {
		name     string
		timeout  time.Duration
		expected time.Duration
	}{
		{
			name:     "default timeout",
			timeout:  0,
			expected: 30 * time.Minute,
		},
		{
			name:     "custom timeout",
			timeout:  5 * time.Minute,
			expected: 5 * time.Minute,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := &DefaultRunner{Timeout: tt.timeout}
			if got := runner.timeout(); got != tt.expected {
				t.Errorf("timeout() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestExtendedRunnerInterface(t *testing.T) {
	// Ensure DefaultRunner implements ExtendedRunner interface
	var _ ExtendedRunner = &DefaultRunner{}
}

func TestDefaultRunner_State(t *testing.T) {
	runner := &DefaultRunner{}

	t.Run("returns empty state for nonexistent state file", func(t *testing.T) {
		tmpDir := t.TempDir()
		state, err := runner.State(tmpDir)
		if err != nil {
			t.Fatalf("State() error = %v", err)
		}
		if state.Resources != 0 {
			t.Errorf("Resources = %v, want 0", state.Resources)
		}
		if state.Outputs != 0 {
			t.Errorf("Outputs = %v, want 0", state.Outputs)
		}
	})

	t.Run("parses valid state file", func(t *testing.T) {
		tmpDir := t.TempDir()
		stateContent := `{
			"version": 4,
			"terraform_version": "1.5.0",
			"resources": [
				{"type": "null_resource", "name": "test1"},
				{"type": "null_resource", "name": "test2"}
			],
			"outputs": {
				"ip": {"value": "1.2.3.4"}
			}
		}`
		stateFile := filepath.Join(tmpDir, "terraform.tfstate")
		if err := os.WriteFile(stateFile, []byte(stateContent), 0644); err != nil {
			t.Fatalf("failed to write state file: %v", err)
		}

		state, err := runner.State(tmpDir)
		if err != nil {
			t.Fatalf("State() error = %v", err)
		}
		if state.Resources != 2 {
			t.Errorf("Resources = %v, want 2", state.Resources)
		}
		if state.Outputs != 1 {
			t.Errorf("Outputs = %v, want 1", state.Outputs)
		}
		if state.TerraformVersion != "1.5.0" {
			t.Errorf("TerraformVersion = %v, want 1.5.0", state.TerraformVersion)
		}
	})

	t.Run("returns error for invalid state file", func(t *testing.T) {
		tmpDir := t.TempDir()
		stateFile := filepath.Join(tmpDir, "terraform.tfstate")
		if err := os.WriteFile(stateFile, []byte("invalid json"), 0644); err != nil {
			t.Fatalf("failed to write state file: %v", err)
		}

		_, err := runner.State(tmpDir)
		if err == nil {
			t.Error("expected error for invalid state file")
		}
	})
}

func TestDefaultRunner_Output(t *testing.T) {
	runner := &DefaultRunner{}

	t.Run("returns error for nonexistent directory", func(t *testing.T) {
		_, err := runner.Output("/nonexistent/directory")
		if err == nil {
			t.Error("Output() should fail with nonexistent directory")
		}
	})
}

func TestDefaultRunner_Validate(t *testing.T) {
	runner := &DefaultRunner{}

	t.Run("returns error for nonexistent directory", func(t *testing.T) {
		result, err := runner.Validate("/nonexistent/directory")
		// Validate may return a result even on error (JSON parsing)
		if err == nil && (result == nil || result.Valid) {
			t.Error("Validate() should fail with nonexistent directory")
		}
	})

	t.Run("returns error for directory with invalid terraform files", func(t *testing.T) {
		tmpDir := t.TempDir()
		// Create a .tf file with invalid syntax so tofu validate actually fails
		if err := os.WriteFile(filepath.Join(tmpDir, "main.tf"), []byte("this is not valid terraform syntax !!!"), 0600); err != nil {
			t.Fatalf("failed to create test file: %v", err)
		}
		result, err := runner.Validate(tmpDir)
		// Validate should fail for a directory with invalid terraform
		if err == nil && (result == nil || result.Valid) {
			t.Error("Validate() should fail with invalid terraform files")
		}
	})
}

func TestApplyResult(t *testing.T) {
	result := &ApplyResult{
		Success: true,
		Outputs: map[string]OutputValue{
			"ip": {Value: "1.2.3.4", Type: "string", Sensitive: false},
		},
		Resources: ResourceChanges{
			Added:     1,
			Changed:   0,
			Destroyed: 0,
		},
		Duration: 5 * time.Second,
	}

	if !result.Success {
		t.Error("Success should be true")
	}
	if result.Resources.Added != 1 {
		t.Errorf("Resources.Added = %v, want 1", result.Resources.Added)
	}
}

func TestValidateResult(t *testing.T) {
	result := &ValidateResult{
		Valid:        true,
		ErrorCount:   0,
		WarningCount: 2,
		Diagnostics:  []string{"warning1", "warning2"},
	}

	if !result.Valid {
		t.Error("Valid should be true")
	}
	if result.WarningCount != 2 {
		t.Errorf("WarningCount = %v, want 2", result.WarningCount)
	}
	if len(result.Diagnostics) != 2 {
		t.Errorf("Diagnostics length = %v, want 2", len(result.Diagnostics))
	}
}

func TestStateSummary(t *testing.T) {
	now := time.Now()
	summary := &StateSummary{
		Resources:        5,
		Outputs:          3,
		LastModified:     now,
		TerraformVersion: "1.6.0",
	}

	if summary.Resources != 5 {
		t.Errorf("Resources = %v, want 5", summary.Resources)
	}
	if summary.Outputs != 3 {
		t.Errorf("Outputs = %v, want 3", summary.Outputs)
	}
	if summary.TerraformVersion != "1.6.0" {
		t.Errorf("TerraformVersion = %v, want 1.6.0", summary.TerraformVersion)
	}
}

func TestOutputValue(t *testing.T) {
	output := OutputValue{
		Value:     "test-value",
		Type:      "string",
		Sensitive: true,
	}

	if output.Value != "test-value" {
		t.Errorf("Value = %v, want test-value", output.Value)
	}
	if output.Type != "string" {
		t.Errorf("Type = %v, want string", output.Type)
	}
	if !output.Sensitive {
		t.Error("Sensitive should be true")
	}
}

func TestLockKeyForDir(t *testing.T) {
	t.Run("returns empty for empty input", func(t *testing.T) {
		result := lockKeyForDir("")
		if result != "" {
			t.Errorf("lockKeyForDir('') = %v, want empty string", result)
		}
	})

	t.Run("returns cleaned absolute path", func(t *testing.T) {
		tmpDir := t.TempDir()
		result := lockKeyForDir(tmpDir)
		if result == "" {
			t.Error("lockKeyForDir should return non-empty for valid dir")
		}
		if !filepath.IsAbs(result) {
			t.Errorf("lockKeyForDir should return absolute path, got %v", result)
		}
	})

	t.Run("normalizes path with dots", func(t *testing.T) {
		tmpDir := t.TempDir()
		pathWithDots := filepath.Join(tmpDir, ".", "subdir", "..")
		result := lockKeyForDir(pathWithDots)
		expected := lockKeyForDir(tmpDir)
		if result != expected {
			t.Errorf("lockKeyForDir(%v) = %v, want %v", pathWithDots, result, expected)
		}
	})
}
