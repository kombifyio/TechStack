// Package tofu provides integration with Terramate for advanced stack orchestration.
package tofu

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// TerramateRunner wraps the terramate CLI for stack orchestration.
type TerramateRunner struct {
	binaryPath string
	workDir    string
	logger     *slog.Logger
	timeout    time.Duration
}

// CommandResult contains the output from a terramate command execution.
type CommandResult struct {
	ExitCode int           `json:"exit_code"`
	Stdout   string        `json:"stdout"`
	Stderr   string        `json:"stderr"`
	Duration time.Duration `json:"duration"`
}

// StackInfo contains information about a Terramate stack.
type StackInfo struct {
	Path        string   `json:"path"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	After       []string `json:"after,omitempty"`
	ID          string   `json:"id,omitempty"`
}

// DriftResult contains the results of drift detection across stacks.
type DriftResult struct {
	Drifted   []string  `json:"drifted"`
	InSync    []string  `json:"in_sync"`
	Failed    []string  `json:"failed"`
	CheckedAt time.Time `json:"checked_at"`
}

// NewTerramateRunner creates a new Terramate runner for the given work directory.
func NewTerramateRunner(workDir string) (*TerramateRunner, error) {
	if workDir == "" {
		return nil, fmt.Errorf("work directory cannot be empty")
	}

	// Check if work directory exists
	info, err := os.Stat(workDir)
	if err != nil {
		if os.IsNotExist(err) {
			// Create the directory
			if err := os.MkdirAll(workDir, 0755); err != nil {
				return nil, fmt.Errorf("failed to create work directory: %w", err)
			}
		} else {
			return nil, fmt.Errorf("failed to access work directory: %w", err)
		}
	} else if !info.IsDir() {
		return nil, fmt.Errorf("work directory path is not a directory: %s", workDir)
	}

	return &TerramateRunner{
		binaryPath: "terramate",
		workDir:    workDir,
		logger:     slog.Default(),
		timeout:    30 * time.Minute,
	}, nil
}

// WithLogger sets a custom logger for the runner.
func (t *TerramateRunner) WithLogger(logger *slog.Logger) *TerramateRunner {
	t.logger = logger
	return t
}

// WithTimeout sets the default timeout for terramate commands.
func (t *TerramateRunner) WithTimeout(timeout time.Duration) *TerramateRunner {
	t.timeout = timeout
	return t
}

// WithBinaryPath sets a custom path to the terramate binary.
func (t *TerramateRunner) WithBinaryPath(path string) *TerramateRunner {
	t.binaryPath = path
	return t
}

// CheckInstalled verifies terramate is installed and returns the version.
func (t *TerramateRunner) CheckInstalled() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, t.binaryPath, "version")

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return "", fmt.Errorf("terramate version check timed out")
		}
		// Try to detect if terramate is not installed
		if strings.Contains(err.Error(), "executable file not found") ||
			strings.Contains(err.Error(), "not found") {
			return "", fmt.Errorf("terramate is not installed: %w", err)
		}
		return "", fmt.Errorf("failed to check terramate version: %w: %s", err, stderr.String())
	}

	version := strings.TrimSpace(stdout.String())
	// Extract version number from output (e.g., "terramate version 0.5.0")
	if parts := strings.Fields(version); len(parts) >= 3 {
		return parts[len(parts)-1], nil
	}

	return version, nil
}

// Run executes terramate run with the given arguments.
// Example: Run(ctx, "--parallel", "5", "--changed", "--", "tofu", "plan")
func (t *TerramateRunner) Run(ctx context.Context, args ...string) (*CommandResult, error) {
	return t.execute(ctx, append([]string{"run"}, args...)...)
}

// RunInStack executes a command in a specific stack.
func (t *TerramateRunner) RunInStack(ctx context.Context, stackPath string, cmd string, cmdArgs ...string) (*CommandResult, error) {
	// Build the command: terramate run --filter=stackPath -- cmd args...
	args := []string{"run", fmt.Sprintf("--filter=%s", stackPath), "--"}
	args = append(args, cmd)
	args = append(args, cmdArgs...)

	return t.execute(ctx, args...)
}

// ListStacks returns all stacks in the project.
func (t *TerramateRunner) ListStacks() ([]StackInfo, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	result, err := t.execute(ctx, "list", "--run-order")
	if err != nil {
		return nil, fmt.Errorf("failed to list stacks: %w", err)
	}

	return t.parseStackList(result.Stdout)
}

// ListChanged returns stacks that have changes.
func (t *TerramateRunner) ListChanged() ([]StackInfo, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	result, err := t.execute(ctx, "list", "--changed")
	if err != nil {
		// If exit code is non-zero but no error message, might mean no changes
		if result != nil && result.ExitCode == 0 {
			return []StackInfo{}, nil
		}
		return nil, fmt.Errorf("failed to list changed stacks: %w", err)
	}

	return t.parseStackList(result.Stdout)
}

// SyncDriftStatus triggers drift detection across all stacks.
func (t *TerramateRunner) SyncDriftStatus(ctx context.Context) (*DriftResult, error) {
	t.logger.Info("starting drift detection")
	start := time.Now()

	result := &DriftResult{
		Drifted:   make([]string, 0),
		InSync:    make([]string, 0),
		Failed:    make([]string, 0),
		CheckedAt: time.Now(),
	}

	// List all stacks first
	stacks, err := t.ListStacks()
	if err != nil {
		return nil, fmt.Errorf("failed to list stacks for drift detection: %w", err)
	}

	// Run plan in each stack and check for drift
	for _, stack := range stacks {
		stackResult, err := t.RunInStack(ctx, stack.Path, "tofu", "plan", "-detailed-exitcode", "-input=false", "-no-color")
		if err != nil {
			// Check if this is a "changes detected" exit code (2 for terraform/tofu)
			if stackResult != nil && stackResult.ExitCode == 2 {
				result.Drifted = append(result.Drifted, stack.Path)
				t.logger.Info("drift detected", "stack", stack.Path)
				continue
			}
			result.Failed = append(result.Failed, stack.Path)
			t.logger.Error("drift detection failed", "stack", stack.Path, "error", err)
			continue
		}

		// Exit code 0 means no changes
		result.InSync = append(result.InSync, stack.Path)
	}

	t.logger.Info("drift detection complete",
		"drifted", len(result.Drifted),
		"in_sync", len(result.InSync),
		"failed", len(result.Failed),
		"duration", time.Since(start),
	)

	return result, nil
}

// execute runs a terramate command with the given arguments.
func (t *TerramateRunner) execute(ctx context.Context, args ...string) (*CommandResult, error) {
	if ctx == nil {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(context.Background(), t.timeout)
		defer cancel()
	}

	t.logger.Debug("executing terramate command", "args", args, "workDir", t.workDir)

	start := time.Now()
	cmd := exec.CommandContext(ctx, t.binaryPath, args...)
	cmd.Dir = t.workDir
	cmd.Env = append(os.Environ(),
		"TF_IN_AUTOMATION=1",
		"TF_INPUT=0",
	)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	duration := time.Since(start)

	result := &CommandResult{
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		Duration: duration,
	}

	if cmd.ProcessState != nil {
		result.ExitCode = cmd.ProcessState.ExitCode()
	}

	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return result, fmt.Errorf("terramate command timed out after %v: %s", t.timeout, strings.Join(args, " "))
		}
		if ctx.Err() == context.Canceled {
			return result, fmt.Errorf("terramate command was canceled: %s", strings.Join(args, " "))
		}

		t.logger.Error("terramate command failed",
			"args", args,
			"exit_code", result.ExitCode,
			"stderr", result.Stderr,
			"duration", duration,
		)

		return result, fmt.Errorf("terramate %s failed (exit code %d): %s",
			strings.Join(args, " "), result.ExitCode, strings.TrimSpace(result.Stderr))
	}

	t.logger.Debug("terramate command completed",
		"args", args,
		"exit_code", result.ExitCode,
		"duration", duration,
	)

	return result, nil
}

// parseStackList parses the output of `terramate list` into StackInfo structs.
func (t *TerramateRunner) parseStackList(output string) ([]StackInfo, error) {
	lines := strings.Split(strings.TrimSpace(output), "\n")
	stacks := make([]StackInfo, 0, len(lines))

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		// Basic parsing: the line is typically just the stack path
		stack := StackInfo{
			Path: line,
			Name: extractStackName(line),
		}
		stacks = append(stacks, stack)
	}

	return stacks, nil
}

// extractStackName extracts the stack name from its path.
func extractStackName(path string) string {
	// Get the last component of the path
	parts := strings.Split(strings.TrimSuffix(path, "/"), "/")
	if len(parts) > 0 {
		return parts[len(parts)-1]
	}
	return path
}

// GetStackDetails retrieves detailed information about a specific stack.
func (t *TerramateRunner) GetStackDetails(stackPath string) (*StackInfo, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Use terramate debug show to get stack details
	result, err := t.execute(ctx, "debug", "show", "metadata", stackPath)
	if err != nil {
		return nil, fmt.Errorf("failed to get stack details: %w", err)
	}

	stack := &StackInfo{
		Path: stackPath,
		Name: extractStackName(stackPath),
	}

	// Parse the metadata output for additional details
	// Format varies, but typically includes name, description, etc.
	lines := strings.Split(result.Stdout, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "name:") {
			stack.Name = strings.TrimSpace(strings.TrimPrefix(line, "name:"))
		} else if strings.HasPrefix(line, "description:") {
			stack.Description = strings.TrimSpace(strings.TrimPrefix(line, "description:"))
		} else if strings.HasPrefix(line, "id:") {
			stack.ID = strings.TrimSpace(strings.TrimPrefix(line, "id:"))
		}
	}

	return stack, nil
}

// ValidateStacks validates all stack configurations.
func (t *TerramateRunner) ValidateStacks() error {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	_, err := t.execute(ctx, "validate")
	return err
}

// GenerateFiles triggers Terramate code generation.
func (t *TerramateRunner) GenerateFiles() error {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	_, err := t.execute(ctx, "generate")
	return err
}

// CreateStack creates a new stack at the given path with the specified configuration.
func (t *TerramateRunner) CreateStack(path string, name string, description string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	args := []string{"create", path}
	if name != "" {
		args = append(args, "--name", name)
	}
	if description != "" {
		args = append(args, "--description", description)
	}

	_, err := t.execute(ctx, args...)
	return err
}

// TerramateVersion represents a parsed terramate version.
type TerramateVersion struct {
	Major int
	Minor int
	Patch int
	Raw   string
}

// ParseVersion parses a version string into a TerramateVersion.
func ParseVersion(version string) (*TerramateVersion, error) {
	// Remove 'v' prefix if present
	version = strings.TrimPrefix(version, "v")

	// Use regex to extract version numbers
	re := regexp.MustCompile(`^(\d+)\.(\d+)\.(\d+)`)
	matches := re.FindStringSubmatch(version)
	if len(matches) < 4 {
		return nil, fmt.Errorf("invalid version format: %s", version)
	}

	major, _ := parseInt(matches[1])
	minor, _ := parseInt(matches[2])
	patch, _ := parseInt(matches[3])

	return &TerramateVersion{
		Major: major,
		Minor: minor,
		Patch: patch,
		Raw:   version,
	}, nil
}

// parseInt safely parses an integer.
func parseInt(s string) (int, error) {
	var n int
	_, err := fmt.Sscanf(s, "%d", &n)
	return n, err
}

// Compare compares two versions. Returns:
//
//	-1 if v < other
//	 0 if v == other
//	 1 if v > other
func (v *TerramateVersion) Compare(other *TerramateVersion) int {
	if v.Major != other.Major {
		if v.Major < other.Major {
			return -1
		}
		return 1
	}
	if v.Minor != other.Minor {
		if v.Minor < other.Minor {
			return -1
		}
		return 1
	}
	if v.Patch != other.Patch {
		if v.Patch < other.Patch {
			return -1
		}
		return 1
	}
	return 0
}

// AtLeast checks if the version is at least the given version.
func (v *TerramateVersion) AtLeast(major, minor, patch int) bool {
	other := &TerramateVersion{Major: major, Minor: minor, Patch: patch}
	return v.Compare(other) >= 0
}

// String returns the string representation of the version.
func (v *TerramateVersion) String() string {
	return fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch)
}

// StackRunResult contains the result of running a command across multiple stacks.
type StackRunResult struct {
	StackPath string         `json:"stack_path"`
	Result    *CommandResult `json:"result"`
	Error     error          `json:"error,omitempty"`
}

// RunParallel executes a command across all stacks in parallel.
func (t *TerramateRunner) RunParallel(ctx context.Context, parallelism int, cmd string, cmdArgs ...string) ([]*StackRunResult, error) {
	args := []string{
		"run",
		fmt.Sprintf("--parallel=%d", parallelism),
		"--",
		cmd,
	}
	args = append(args, cmdArgs...)

	result, err := t.execute(ctx, args...)

	// For parallel runs, we return a single result representing the overall execution
	stackResult := &StackRunResult{
		StackPath: "all",
		Result:    result,
		Error:     err,
	}

	return []*StackRunResult{stackResult}, err
}

// RunChanged executes a command only on stacks with changes.
func (t *TerramateRunner) RunChanged(ctx context.Context, cmd string, cmdArgs ...string) (*CommandResult, error) {
	args := []string{
		"run",
		"--changed",
		"--",
		cmd,
	}
	args = append(args, cmdArgs...)

	return t.execute(ctx, args...)
}

// StackOrderInfo contains ordering information for stacks.
type StackOrderInfo struct {
	Stacks []StackInfo `json:"stacks"`
}

// GetStackOrder returns stacks in their execution order (respecting dependencies).
func (t *TerramateRunner) GetStackOrder() (*StackOrderInfo, error) {
	stacks, err := t.ListStacks()
	if err != nil {
		return nil, err
	}

	return &StackOrderInfo{
		Stacks: stacks,
	}, nil
}

// DriftDetails provides detailed drift information for a single stack.
type DriftDetails struct {
	StackPath      string                 `json:"stack_path"`
	HasChanges     bool                   `json:"has_changes"`
	PlannedChanges map[string]interface{} `json:"planned_changes,omitempty"`
	Error          string                 `json:"error,omitempty"`
}

// GetDriftDetails returns detailed drift information for a specific stack.
func (t *TerramateRunner) GetDriftDetails(ctx context.Context, stackPath string) (*DriftDetails, error) {
	result, err := t.RunInStack(ctx, stackPath, "tofu", "plan", "-json", "-detailed-exitcode", "-input=false")

	details := &DriftDetails{
		StackPath: stackPath,
	}

	if err != nil {
		if result != nil && result.ExitCode == 2 {
			details.HasChanges = true
			// Try to parse the JSON output for change details
			if result.Stdout != "" {
				var planOutput map[string]interface{}
				if jsonErr := json.Unmarshal([]byte(result.Stdout), &planOutput); jsonErr == nil {
					details.PlannedChanges = planOutput
				}
			}
			return details, nil
		}
		details.Error = err.Error()
		return details, err
	}

	details.HasChanges = false
	return details, nil
}
