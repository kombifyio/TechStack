// Package tofu provides a wrapper around the OpenTofu CLI.
package tofu

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// stateLocks provides per-directory locking to prevent concurrent state operations.
// This prevents state corruption when multiple operations target the same directory.
var stateLocks = struct {
	sync.Mutex
	locks map[string]*sync.Mutex
}{
	locks: make(map[string]*sync.Mutex),
}

// acquireStateLock acquires a lock for the given directory.
// Returns a function to release the lock.
func acquireStateLock(dir string) func() {
	dir = lockKeyForDir(dir)
	if dir == "" {
		// Fallback to a no-op unlock to keep call-sites simple.
		return func() {}
	}

	stateLocks.Lock()
	lock, ok := stateLocks.locks[dir]
	if !ok {
		lock = &sync.Mutex{}
		stateLocks.locks[dir] = lock
	}
	stateLocks.Unlock()

	lock.Lock()
	return lock.Unlock
}

func lockKeyForDir(dir string) string {
	if dir == "" {
		return ""
	}

	key := filepath.Clean(dir)
	if abs, err := filepath.Abs(key); err == nil {
		key = abs
	}
	if real, err := filepath.EvalSymlinks(key); err == nil {
		key = real
	}

	// Windows filesystems are typically case-insensitive.
	if runtime.GOOS == "windows" {
		key = strings.ToLower(key)
	}

	return key
}

// Runner executes OpenTofu commands.
type Runner interface {
	Init(dir string) error
	Plan(dir string) error
	Apply(dir string) error
	Destroy(dir string) error
}

// ExtendedRunner provides additional OpenTofu operations with state management.
type ExtendedRunner interface {
	Runner
	// InitWithContext runs init with context for cancellation.
	InitWithContext(ctx context.Context, dir string) error
	// ApplyWithContext runs apply with context for cancellation.
	ApplyWithContext(ctx context.Context, dir string) (*ApplyResult, error)
	// DestroyWithContext runs destroy with context for cancellation.
	DestroyWithContext(ctx context.Context, dir string) error
	// Output retrieves output values from state.
	Output(dir string) (map[string]OutputValue, error)
	// State returns the current state summary.
	State(dir string) (*StateSummary, error)
	// Validate runs terraform validate.
	Validate(dir string) (*ValidateResult, error)
}

// ApplyResult contains the result of an apply operation.
type ApplyResult struct {
	Success   bool                   `json:"success"`
	Outputs   map[string]OutputValue `json:"outputs,omitempty"`
	Resources ResourceChanges        `json:"resources"`
	Duration  time.Duration          `json:"duration"`
}

// OutputValue represents a terraform output value.
type OutputValue struct {
	Value     interface{} `json:"value"`
	Type      string      `json:"type"`
	Sensitive bool        `json:"sensitive"`
}

// ResourceChanges summarizes resource changes.
type ResourceChanges struct {
	Added     int `json:"added"`
	Changed   int `json:"changed"`
	Destroyed int `json:"destroyed"`
}

// StateSummary provides an overview of the current state.
type StateSummary struct {
	Resources        int       `json:"resources"`
	Outputs          int       `json:"outputs"`
	LastModified     time.Time `json:"last_modified"`
	TerraformVersion string    `json:"terraform_version"`
}

// ValidateResult contains validation output.
type ValidateResult struct {
	Valid        bool     `json:"valid"`
	ErrorCount   int      `json:"error_count"`
	WarningCount int      `json:"warning_count"`
	Diagnostics  []string `json:"diagnostics,omitempty"`
}

// DefaultRunner implements the Runner and ExtendedRunner interfaces.
type DefaultRunner struct {
	Binary  string
	Timeout time.Duration
}

func (r *DefaultRunner) bin() string {
	if r.Binary != "" {
		return r.Binary
	}
	return "tofu"
}

func (r *DefaultRunner) timeout() time.Duration {
	if r.Timeout > 0 {
		return r.Timeout
	}
	return 30 * time.Minute
}

func (r *DefaultRunner) run(dir string, args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), r.timeout())
	defer cancel()
	return r.runWithContext(ctx, dir, args...)
}

func (r *DefaultRunner) runWithContext(ctx context.Context, dir string, args ...string) error {
	_, err := r.runWithOutput(ctx, dir, args...)
	return err
}

// getPluginCacheDir returns the plugin cache directory path.
// Uses TF_PLUGIN_CACHE_DIR env var if set, otherwise defaults to ~/.terraform.d/plugin-cache
func getPluginCacheDir() string {
	if dir := os.Getenv("TF_PLUGIN_CACHE_DIR"); dir != "" {
		return dir
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".terraform.d", "plugin-cache")
}

func (r *DefaultRunner) runWithOutput(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, r.bin(), args...)
	cmd.Dir = dir

	// Build environment with plugin cache for better performance
	env := append(os.Environ(),
		"TF_IN_AUTOMATION=1",
		"TF_INPUT=0",
	)
	if cacheDir := getPluginCacheDir(); cacheDir != "" {
		// Ensure cache directory exists
		_ = os.MkdirAll(cacheDir, 0755)
		env = append(env, "TF_PLUGIN_CACHE_DIR="+cacheDir)
	}
	cmd.Env = env

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		// Check for context cancellation
		if ctx.Err() == context.DeadlineExceeded {
			return "", fmt.Errorf("tofu %s timed out after %v", strings.Join(args, " "), r.timeout())
		}
		if ctx.Err() == context.Canceled {
			return "", fmt.Errorf("tofu %s was canceled", strings.Join(args, " "))
		}

		output := strings.TrimSpace(stderr.String())
		if output == "" {
			output = strings.TrimSpace(stdout.String())
		}
		if output == "" {
			return "", fmt.Errorf("tofu %s failed: %w", strings.Join(args, " "), err)
		}
		return "", fmt.Errorf("tofu %s failed: %w: %s", strings.Join(args, " "), err, output)
	}

	return strings.TrimSpace(stdout.String()), nil
}

// Init runs `tofu init` in the specified directory.
func (r *DefaultRunner) Init(dir string) error {
	unlock := acquireStateLock(dir)
	defer unlock()
	return r.run(dir, "init", "-input=false", "-no-color")
}

// InitWithContext runs `tofu init` with context for cancellation.
func (r *DefaultRunner) InitWithContext(ctx context.Context, dir string) error {
	unlock := acquireStateLock(dir)
	defer unlock()
	return r.runWithContext(ctx, dir, "init", "-input=false", "-no-color")
}

// Plan runs `tofu plan` in the specified directory.
func (r *DefaultRunner) Plan(dir string) error {
	unlock := acquireStateLock(dir)
	defer unlock()
	return r.run(dir, "plan", "-input=false", "-no-color")
}

// Apply runs `tofu apply` in the specified directory.
// Acquires a state lock for the directory to prevent concurrent operations.
func (r *DefaultRunner) Apply(dir string) error {
	unlock := acquireStateLock(dir)
	defer unlock()
	return r.run(dir, "apply", "-auto-approve", "-input=false", "-no-color")
}

// ApplyWithContext runs apply and returns detailed results.
// Acquires a state lock for the directory to prevent concurrent operations.
func (r *DefaultRunner) ApplyWithContext(ctx context.Context, dir string) (*ApplyResult, error) {
	unlock := acquireStateLock(dir)
	defer unlock()

	start := time.Now()

	err := r.runWithContext(ctx, dir, "apply", "-auto-approve", "-input=false", "-no-color")
	if err != nil {
		return &ApplyResult{
			Success:  false,
			Duration: time.Since(start),
		}, err
	}

	// Get outputs after successful apply.
	// We are already holding the state lock, so call the unlocked variant.
	outputs, _ := r.outputUnlocked(dir) // Ignore error, outputs are optional

	return &ApplyResult{
		Success:  true,
		Outputs:  outputs,
		Duration: time.Since(start),
	}, nil
}

// Destroy runs `tofu destroy` in the specified directory.
// Acquires a state lock for the directory to prevent concurrent operations.
func (r *DefaultRunner) Destroy(dir string) error {
	unlock := acquireStateLock(dir)
	defer unlock()
	return r.run(dir, "destroy", "-auto-approve", "-input=false", "-no-color")
}

// DestroyWithContext runs destroy with context for cancellation.
// Acquires a state lock for the directory to prevent concurrent operations.
func (r *DefaultRunner) DestroyWithContext(ctx context.Context, dir string) error {
	unlock := acquireStateLock(dir)
	defer unlock()
	return r.runWithContext(ctx, dir, "destroy", "-auto-approve", "-input=false", "-no-color")
}

// Output retrieves output values from the state.
func (r *DefaultRunner) Output(dir string) (map[string]OutputValue, error) {
	unlock := acquireStateLock(dir)
	defer unlock()
	return r.outputUnlocked(dir)
}

func (r *DefaultRunner) outputUnlocked(dir string) (map[string]OutputValue, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	output, err := r.runWithOutput(ctx, dir, "output", "-json", "-no-color")
	if err != nil {
		return nil, err
	}

	if output == "" || output == "{}" {
		return make(map[string]OutputValue), nil
	}

	var outputs map[string]OutputValue
	if err := json.Unmarshal([]byte(output), &outputs); err != nil {
		return nil, fmt.Errorf("failed to parse output: %w", err)
	}

	return outputs, nil
}

// State returns a summary of the current state.
func (r *DefaultRunner) State(dir string) (*StateSummary, error) {
	unlock := acquireStateLock(dir)
	defer unlock()
	return r.stateUnlocked(dir)
}

func (r *DefaultRunner) stateUnlocked(dir string) (*StateSummary, error) {
	stateFile := filepath.Join(dir, "terraform.tfstate")

	info, err := os.Stat(stateFile)
	if os.IsNotExist(err) {
		return &StateSummary{
			Resources: 0,
			Outputs:   0,
		}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to stat state file: %w", err)
	}

	// Read and parse state file
	data, err := os.ReadFile(stateFile)
	if err != nil {
		return nil, fmt.Errorf("failed to read state file: %w", err)
	}

	var state struct {
		Version          int                    `json:"version"`
		TerraformVersion string                 `json:"terraform_version"`
		Resources        []interface{}          `json:"resources"`
		Outputs          map[string]interface{} `json:"outputs"`
	}

	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("failed to parse state file: %w", err)
	}

	return &StateSummary{
		Resources:        len(state.Resources),
		Outputs:          len(state.Outputs),
		LastModified:     info.ModTime(),
		TerraformVersion: state.TerraformVersion,
	}, nil
}

// Validate runs terraform validate on the configuration.
func (r *DefaultRunner) Validate(dir string) (*ValidateResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	output, err := r.runWithOutput(ctx, dir, "validate", "-json", "-no-color")

	// Parse JSON output even on error
	var result struct {
		Valid        bool `json:"valid"`
		ErrorCount   int  `json:"error_count"`
		WarningCount int  `json:"warning_count"`
		Diagnostics  []struct {
			Summary string `json:"summary"`
		} `json:"diagnostics"`
	}

	if output != "" {
		if parseErr := json.Unmarshal([]byte(output), &result); parseErr == nil {
			diagnostics := make([]string, 0, len(result.Diagnostics))
			for _, d := range result.Diagnostics {
				diagnostics = append(diagnostics, d.Summary)
			}
			return &ValidateResult{
				Valid:        result.Valid,
				ErrorCount:   result.ErrorCount,
				WarningCount: result.WarningCount,
				Diagnostics:  diagnostics,
			}, err
		}
	}

	// Fallback if JSON parsing fails
	if err != nil {
		return &ValidateResult{
			Valid:       false,
			ErrorCount:  1,
			Diagnostics: []string{err.Error()},
		}, err
	}

	return &ValidateResult{Valid: true}, nil
}
