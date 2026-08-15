// Package agent provides the agent-side command execution for kombifyTechstack.
// This file implements the TerramateExecutor for IAC-5 Day-2 operations.
package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/kombifyio/techstack/pkg/api/agentpb"
)

// TerramateOperation defines the types of Terramate operations.
type TerramateOperation string

const (
	// TerramateOperationRun executes commands across stacks.
	TerramateOperationRun TerramateOperation = "run"
	// TerramateOperationSyncDrift synchronizes drift detection status.
	TerramateOperationSyncDrift TerramateOperation = "sync_drift"
	// TerramateOperationListChanged lists stacks with changes.
	TerramateOperationListChanged TerramateOperation = "list_changed"
)

// Default timeouts for Terramate operations.
const (
	DefaultTerramateRunTimeout         = 30 * time.Minute
	DefaultTerramateSyncDriftTimeout   = 15 * time.Minute
	DefaultTerramateListChangedTimeout = 2 * time.Minute
)

// Default base path for Terramate projects.
const DefaultTerramateBasePath = "/opt/techstack/terramate"

// TerramateExecutorConfig holds configuration for the TerramateExecutor.
type TerramateExecutorConfig struct {
	// BasePath is the base directory for Terramate projects.
	// Defaults to /opt/techstack/terramate on Linux or %LOCALAPPDATA%\techstack\terramate on Windows.
	BasePath string
	// Binary is the path to the terramate binary. Defaults to "terramate".
	Binary string
	// Logger is the structured logger to use.
	Logger *slog.Logger
	// DefaultTimeout is the default operation timeout if not specified.
	DefaultTimeout time.Duration
	// TofuExecutor is an optional reference to share lock mechanisms.
	TofuExecutor *TofuExecutor
}

// TerramateExecutor handles execution of Terramate commands on the agent side.
type TerramateExecutor struct {
	basePath       string
	binary         string
	defaultTimeout time.Duration
	log            *slog.Logger
	tofuExecutor   *TofuExecutor
	available      bool

	// dirLocks provides per-directory locking to prevent concurrent operations.
	dirLocks struct {
		sync.Mutex
		locks map[string]*sync.Mutex
	}
}

// TerramateRequest contains parameters for a Terramate operation.
type TerramateRequest struct {
	// WorkspaceName is the name of the workspace (used as directory name).
	WorkspaceName string `json:"workspace_name"`
	// Operation is the Terramate operation to perform.
	Operation TerramateOperation `json:"operation"`
	// Command is the command to run (for run operations).
	Command string `json:"command,omitempty"`
	// Args are additional arguments for the command.
	Args []string `json:"args,omitempty"`
	// Tags filter stacks by tags.
	Tags []string `json:"tags,omitempty"`
	// ChangedOnly limits operations to changed stacks only.
	ChangedOnly bool `json:"changed_only,omitempty"`
	// Timeout overrides the default timeout for this operation.
	Timeout time.Duration `json:"timeout,omitempty"`
}

// TerramateResult contains the result of a Terramate operation.
type TerramateResult struct {
	// Success indicates whether the operation succeeded.
	Success bool `json:"success"`
	// Operation is the operation that was performed.
	Operation TerramateOperation `json:"operation"`
	// Stdout contains the standard output from the command.
	Stdout string `json:"stdout"`
	// Stderr contains the standard error from the command.
	Stderr string `json:"stderr"`
	// ExitCode is the exit code from the command.
	ExitCode int `json:"exit_code"`
	// Duration is how long the operation took.
	Duration time.Duration `json:"duration"`
	// WorkingDir is the working directory used.
	WorkingDir string `json:"working_dir"`
	// ChangedStacks contains list of changed stacks (for list_changed operation).
	ChangedStacks []string `json:"changed_stacks,omitempty"`
	// DriftReports contains drift information (for sync_drift operation).
	DriftReports []DriftReport `json:"drift_reports,omitempty"`
	// Error contains the error message if the operation failed.
	Error string `json:"error,omitempty"`
}

// DriftReport contains drift detection information for a single stack.
type DriftReport struct {
	// StackPath is the path to the stack.
	StackPath string `json:"stack_path"`
	// HasDrift indicates if drift was detected.
	HasDrift bool `json:"has_drift"`
	// DriftSummary is a human-readable drift summary.
	DriftSummary string `json:"drift_summary,omitempty"`
	// DriftedResources lists resources with drift.
	DriftedResources []string `json:"drifted_resources,omitempty"`
}

// NewTerramateExecutor creates a new TerramateExecutor with the given configuration.
// Unlike TofuExecutor, this does not fail if terramate is not installed.
func NewTerramateExecutor(config TerramateExecutorConfig) (*TerramateExecutor, error) {
	// Set base path
	basePath := config.BasePath
	if basePath == "" {
		if runtime.GOOS == "windows" {
			localAppData := os.Getenv("LOCALAPPDATA")
			if localAppData == "" {
				homeDir, err := os.UserHomeDir()
				if err != nil {
					return nil, fmt.Errorf("failed to get home directory: %w", err)
				}
				localAppData = filepath.Join(homeDir, "AppData", "Local")
			}
			basePath = filepath.Join(localAppData, "techstack", "terramate")
		} else {
			basePath = DefaultTerramateBasePath
		}
	}

	// Ensure base path exists
	if err := os.MkdirAll(basePath, 0755); err != nil {
		return nil, fmt.Errorf("failed to create base directory %s: %w", basePath, err)
	}

	// Set binary path
	binary := config.Binary
	if binary == "" {
		binary = "terramate"
	}

	// Check if terramate binary is available (graceful fallback if not)
	available := true
	if _, err := exec.LookPath(binary); err != nil {
		available = false
		if config.Logger != nil {
			config.Logger.Warn("terramate binary not found - Terramate features will be disabled",
				"binary", binary,
				"error", err.Error(),
			)
		}
	}

	// Set default timeout
	defaultTimeout := config.DefaultTimeout
	if defaultTimeout == 0 {
		defaultTimeout = DefaultTerramateRunTimeout
	}

	// Set logger
	log := config.Logger
	if log == nil {
		log = slog.Default()
	}
	log = log.With("component", "terramate-executor")

	return &TerramateExecutor{
		basePath:       basePath,
		binary:         binary,
		defaultTimeout: defaultTimeout,
		log:            log,
		tofuExecutor:   config.TofuExecutor,
		available:      available,
		dirLocks: struct {
			sync.Mutex
			locks map[string]*sync.Mutex
		}{
			locks: make(map[string]*sync.Mutex),
		},
	}, nil
}

// IsAvailable returns true if the terramate binary is installed and available.
func (e *TerramateExecutor) IsAvailable() bool {
	return e.available
}

// Execute runs a Terramate operation based on the request.
func (e *TerramateExecutor) Execute(ctx context.Context, req *TerramateRequest) (*TerramateResult, error) {
	if !e.available {
		return &TerramateResult{
			Success:   false,
			Operation: req.Operation,
			Error:     "terramate binary not available - please install terramate",
		}, fmt.Errorf("terramate binary not available")
	}

	if req.WorkspaceName == "" {
		return nil, fmt.Errorf("workspace_name is required")
	}

	// Sanitize workspace name to prevent path traversal
	if strings.Contains(req.WorkspaceName, "..") || strings.ContainsAny(req.WorkspaceName, `/\`) {
		return nil, fmt.Errorf("invalid workspace_name: must not contain path separators or '..'")
	}

	workDir := filepath.Join(e.basePath, req.WorkspaceName)

	// Ensure working directory exists
	if err := os.MkdirAll(workDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create working directory %s: %w", workDir, err)
	}

	// Execute the operation
	switch req.Operation {
	case TerramateOperationRun:
		return e.Run(ctx, workDir, req.Command, req.Args, req.Tags, req.ChangedOnly, req.Timeout)
	case TerramateOperationSyncDrift:
		return e.SyncDrift(ctx, workDir, req.Tags, req.Timeout)
	case TerramateOperationListChanged:
		return e.ListChanged(ctx, workDir, req.Tags, req.Timeout)
	default:
		return nil, fmt.Errorf("unknown operation: %s", req.Operation)
	}
}

// Run executes `terramate run <command>` across stacks.
func (e *TerramateExecutor) Run(ctx context.Context, workDir, command string, args, tags []string, changedOnly bool, timeout time.Duration) (*TerramateResult, error) {
	if !e.available {
		return &TerramateResult{
			Success:   false,
			Operation: TerramateOperationRun,
			Error:     "terramate binary not available",
		}, fmt.Errorf("terramate binary not available")
	}

	if timeout == 0 {
		timeout = DefaultTerramateRunTimeout
	}

	unlock := e.acquireLock(workDir)
	defer unlock()

	e.log.Info("terramate_run_starting",
		"work_dir", workDir,
		"command", command,
		"args", args,
		"tags", tags,
		"changed_only", changedOnly,
		"timeout", timeout.String(),
	)

	// Build terramate run command
	cmdArgs := []string{"run"}

	// Add tags filter
	for _, tag := range tags {
		cmdArgs = append(cmdArgs, "--tag", tag)
	}

	// Add changed filter
	if changedOnly {
		cmdArgs = append(cmdArgs, "--changed")
	}

	// Add the command separator and actual command
	cmdArgs = append(cmdArgs, "--")

	// Add the command to run
	if command != "" {
		cmdArgs = append(cmdArgs, command)
	}
	cmdArgs = append(cmdArgs, args...)

	result, err := e.runCommand(ctx, workDir, timeout, cmdArgs...)
	result.Operation = TerramateOperationRun

	if err != nil {
		e.log.Error("terramate_run_failed",
			"work_dir", workDir,
			"error", err.Error(),
			"duration", result.Duration.String(),
		)
	} else {
		e.log.Info("terramate_run_completed",
			"work_dir", workDir,
			"success", result.Success,
			"duration", result.Duration.String(),
		)
	}

	return result, err
}

// SyncDrift executes `terramate run --sync-drift-status` for drift detection.
func (e *TerramateExecutor) SyncDrift(ctx context.Context, workDir string, tags []string, timeout time.Duration) (*TerramateResult, error) {
	if !e.available {
		return &TerramateResult{
			Success:   false,
			Operation: TerramateOperationSyncDrift,
			Error:     "terramate binary not available",
		}, fmt.Errorf("terramate binary not available")
	}

	if timeout == 0 {
		timeout = DefaultTerramateSyncDriftTimeout
	}

	unlock := e.acquireLock(workDir)
	defer unlock()

	e.log.Info("terramate_sync_drift_starting",
		"work_dir", workDir,
		"tags", tags,
		"timeout", timeout.String(),
	)

	// Build terramate command for drift sync
	cmdArgs := []string{"run", "--sync-drift-status"}

	// Add tags filter
	for _, tag := range tags {
		cmdArgs = append(cmdArgs, "--tag", tag)
	}

	// The drift sync typically runs terraform plan in each stack
	cmdArgs = append(cmdArgs, "--", "tofu", "plan", "-detailed-exitcode", "-no-color")

	result, err := e.runCommand(ctx, workDir, timeout, cmdArgs...)
	result.Operation = TerramateOperationSyncDrift

	// Parse drift reports from output
	if result.Success || result.ExitCode == 2 { // Exit code 2 indicates drift
		result.DriftReports = e.parseDriftOutput(result.Stdout, result.Stderr)
	}

	if err != nil {
		e.log.Error("terramate_sync_drift_failed",
			"work_dir", workDir,
			"error", err.Error(),
			"duration", result.Duration.String(),
		)
	} else {
		e.log.Info("terramate_sync_drift_completed",
			"work_dir", workDir,
			"success", result.Success,
			"duration", result.Duration.String(),
			"drift_reports_count", len(result.DriftReports),
		)
	}

	return result, err
}

// ListChanged executes `terramate list --changed` to find stacks with changes.
func (e *TerramateExecutor) ListChanged(ctx context.Context, workDir string, tags []string, timeout time.Duration) (*TerramateResult, error) {
	if !e.available {
		return &TerramateResult{
			Success:   false,
			Operation: TerramateOperationListChanged,
			Error:     "terramate binary not available",
		}, fmt.Errorf("terramate binary not available")
	}

	if timeout == 0 {
		timeout = DefaultTerramateListChangedTimeout
	}

	unlock := e.acquireLock(workDir)
	defer unlock()

	e.log.Info("terramate_list_changed_starting",
		"work_dir", workDir,
		"tags", tags,
		"timeout", timeout.String(),
	)

	// Build terramate list command
	cmdArgs := []string{"list", "--changed"}

	// Add tags filter
	for _, tag := range tags {
		cmdArgs = append(cmdArgs, "--tag", tag)
	}

	result, err := e.runCommand(ctx, workDir, timeout, cmdArgs...)
	result.Operation = TerramateOperationListChanged

	// Parse changed stacks from output
	if result.Success && result.Stdout != "" {
		result.ChangedStacks = e.parseChangedStacks(result.Stdout)
	}

	if err != nil {
		e.log.Error("terramate_list_changed_failed",
			"work_dir", workDir,
			"error", err.Error(),
			"duration", result.Duration.String(),
		)
	} else {
		e.log.Info("terramate_list_changed_completed",
			"work_dir", workDir,
			"success", result.Success,
			"duration", result.Duration.String(),
			"changed_stacks_count", len(result.ChangedStacks),
		)
	}

	return result, err
}

// runCommand executes a terramate command and returns the result.
func (e *TerramateExecutor) runCommand(ctx context.Context, workDir string, timeout time.Duration, args ...string) (*TerramateResult, error) {
	start := time.Now()

	result := &TerramateResult{
		WorkingDir: workDir,
	}

	// Create timeout context
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, e.binary, args...)
	cmd.Dir = workDir

	// Build environment
	env := os.Environ()
	cmd.Env = env

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	e.log.Debug("running_terramate_command",
		"binary", e.binary,
		"args", args,
		"work_dir", workDir,
	)

	err := cmd.Run()
	result.Duration = time.Since(start)
	result.Stdout = strings.TrimSpace(stdout.String())
	result.Stderr = strings.TrimSpace(stderr.String())

	if err != nil {
		// Check for context errors
		if ctx.Err() == context.DeadlineExceeded {
			result.Success = false
			result.ExitCode = -1
			result.Error = fmt.Sprintf("command timed out after %v", timeout)
			return result, fmt.Errorf("terramate %s timed out after %v", strings.Join(args, " "), timeout)
		}
		if ctx.Err() == context.Canceled {
			result.Success = false
			result.ExitCode = -1
			result.Error = "command was canceled"
			return result, fmt.Errorf("terramate %s was canceled", strings.Join(args, " "))
		}

		// Get exit code
		if exitErr, ok := err.(*exec.ExitError); ok {
			result.ExitCode = exitErr.ExitCode()
		} else {
			result.ExitCode = 1
		}

		result.Success = false
		errMsg := result.Stderr
		if errMsg == "" {
			errMsg = result.Stdout
		}
		if errMsg == "" {
			errMsg = err.Error()
		}
		result.Error = errMsg

		return result, fmt.Errorf("terramate %s failed: %w: %s", strings.Join(args, " "), err, errMsg)
	}

	result.Success = true
	result.ExitCode = 0
	return result, nil
}

// parseChangedStacks parses the output of `terramate list --changed`.
func (e *TerramateExecutor) parseChangedStacks(output string) []string {
	lines := strings.Split(output, "\n")
	var stacks []string

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "#") {
			stacks = append(stacks, line)
		}
	}

	return stacks
}

// parseDriftOutput parses drift detection output to extract drift reports.
func (e *TerramateExecutor) parseDriftOutput(stdout, stderr string) []DriftReport {
	var reports []DriftReport

	// Combine stdout and stderr for parsing
	combined := stdout + "\n" + stderr

	// Pattern to identify stack execution blocks
	// Terramate outputs stack path when running commands
	stackPattern := regexp.MustCompile(`(?m)^(stacks?/[^\s]+)`)
	driftPattern := regexp.MustCompile(`Plan:\s*(\d+)\s*to add,\s*(\d+)\s*to change,\s*(\d+)\s*to destroy`)

	// Simple heuristic: extract stack paths and check for drift indicators
	stackMatches := stackPattern.FindAllStringSubmatch(combined, -1)
	for _, match := range stackMatches {
		if len(match) >= 2 {
			stackPath := match[1]
			report := DriftReport{
				StackPath: stackPath,
				HasDrift:  false,
			}

			// Check if there's drift indication for this stack
			if driftPattern.MatchString(combined) {
				report.HasDrift = true
				report.DriftSummary = "Changes detected - drift present"
			}

			reports = append(reports, report)
		}
	}

	// If no stacks found but output indicates drift, create a general report
	if len(reports) == 0 && strings.Contains(combined, "to add") || strings.Contains(combined, "to change") || strings.Contains(combined, "to destroy") {
		reports = append(reports, DriftReport{
			StackPath:    ".",
			HasDrift:     true,
			DriftSummary: "Drift detected in workspace",
		})
	}

	return reports
}

// acquireLock acquires a lock for the given directory.
func (e *TerramateExecutor) acquireLock(dir string) func() {
	key := e.lockKeyForDir(dir)
	if key == "" {
		return func() {} // No-op for empty directory
	}

	e.dirLocks.Lock()
	lock, ok := e.dirLocks.locks[key]
	if !ok {
		lock = &sync.Mutex{}
		e.dirLocks.locks[key] = lock
	}
	e.dirLocks.Unlock()

	lock.Lock()
	return lock.Unlock
}

// lockKeyForDir normalizes a directory path for use as a lock key.
func (e *TerramateExecutor) lockKeyForDir(dir string) string {
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

	// Windows filesystems are typically case-insensitive
	if runtime.GOOS == "windows" {
		key = strings.ToLower(key)
	}

	return key
}

// BasePath returns the executor's base path for working directories.
func (e *TerramateExecutor) BasePath() string {
	return e.basePath
}

// WorkspaceDir returns the directory for a specific workspace.
func (e *TerramateExecutor) WorkspaceDir(workspaceName string) string {
	return filepath.Join(e.basePath, workspaceName)
}

// WorkspaceExists checks if a workspace directory exists.
func (e *TerramateExecutor) WorkspaceExists(workspaceName string) bool {
	workDir := e.WorkspaceDir(workspaceName)
	info, err := os.Stat(workDir)
	return err == nil && info.IsDir()
}

// InitializeProject initializes a Terramate project structure in the workspace.
func (e *TerramateExecutor) InitializeProject(ctx context.Context, workspaceName string) error {
	if !e.available {
		return fmt.Errorf("terramate binary not available")
	}

	workDir := e.WorkspaceDir(workspaceName)
	if err := os.MkdirAll(workDir, 0755); err != nil {
		return fmt.Errorf("failed to create workspace directory: %w", err)
	}

	// Check if terramate.tm.hcl already exists
	tmConfig := filepath.Join(workDir, "terramate.tm.hcl")
	if _, err := os.Stat(tmConfig); err == nil {
		e.log.Debug("terramate project already initialized",
			"workspace", workspaceName,
		)
		return nil
	}

	// Create basic terramate.tm.hcl configuration
	configContent := `# Terramate Configuration
# Generated by kombifyTechstack

terramate {
  config {
    # Enable experimental features if needed
    # experiments = ["scripts"]
  }
}

# Global settings for all stacks
globals {
  # Add global variables here
  project_name = "techstack"
}
`

	if err := os.WriteFile(tmConfig, []byte(configContent), 0644); err != nil {
		return fmt.Errorf("failed to write terramate.tm.hcl: %w", err)
	}

	e.log.Info("terramate project initialized",
		"workspace", workspaceName,
		"config_path", tmConfig,
	)

	return nil
}

// CreateStack creates a new Terramate stack in the workspace.
func (e *TerramateExecutor) CreateStack(ctx context.Context, workspaceName, stackName, description string, tags []string) error {
	if !e.available {
		return fmt.Errorf("terramate binary not available")
	}

	workDir := e.WorkspaceDir(workspaceName)
	stackDir := filepath.Join(workDir, "stacks", stackName)

	if err := os.MkdirAll(stackDir, 0755); err != nil {
		return fmt.Errorf("failed to create stack directory: %w", err)
	}

	// Create stack.tm.hcl
	tagsStr := ""
	if len(tags) > 0 {
		quotedTags := make([]string, len(tags))
		for i, tag := range tags {
			quotedTags[i] = fmt.Sprintf("%q", tag)
		}
		tagsStr = fmt.Sprintf("  tags = [%s]\n", strings.Join(quotedTags, ", "))
	}

	stackConfig := fmt.Sprintf(`# Stack Configuration
# Generated by kombifyTechstack

stack {
  name        = %q
  description = %q
%s}
`, stackName, description, tagsStr)

	stackTmFile := filepath.Join(stackDir, "stack.tm.hcl")
	if err := os.WriteFile(stackTmFile, []byte(stackConfig), 0644); err != nil {
		return fmt.Errorf("failed to write stack.tm.hcl: %w", err)
	}

	e.log.Info("terramate stack created",
		"workspace", workspaceName,
		"stack", stackName,
		"path", stackDir,
	)

	return nil
}

// VerifyBinary checks if the terramate binary is available and returns its version.
func (e *TerramateExecutor) VerifyBinary(ctx context.Context) (string, error) {
	if !e.available {
		return "", fmt.Errorf("terramate binary not available in PATH")
	}

	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, e.binary, "version")
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to get terramate version: %w", err)
	}

	return strings.TrimSpace(string(output)), nil
}

// DiscoverStacks discovers all stacks in a workspace.
func (e *TerramateExecutor) DiscoverStacks(ctx context.Context, workspaceName string, tags []string) ([]string, error) {
	if !e.available {
		return nil, fmt.Errorf("terramate binary not available")
	}

	workDir := e.WorkspaceDir(workspaceName)
	if !e.WorkspaceExists(workspaceName) {
		return nil, fmt.Errorf("workspace %q does not exist", workspaceName)
	}

	// Build terramate list command
	args := []string{"list"}
	for _, tag := range tags {
		args = append(args, "--tag", tag)
	}

	result, err := e.runCommand(ctx, workDir, DefaultTerramateListChangedTimeout, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to discover stacks: %w", err)
	}

	return e.parseChangedStacks(result.Stdout), nil
}

// =============================================================================
// IAC-5: gRPC TerramateCommand Integration
// These methods bridge the gRPC layer to the TerramateExecutor
// =============================================================================

// ExecuteGRPC processes a gRPC TerramateCommand and returns a TerramateResult.
// This method bridges the agentpb.TerramateCommand to the internal TerramateExecutor.
func (e *TerramateExecutor) ExecuteGRPC(ctx context.Context, cmd *agentpb.TerramateCommand) *agentpb.TerramateResult {
	startTime := time.Now()

	result := &agentpb.TerramateResult{
		CommandId:     cmd.CommandId,
		StartedAtUnix: startTime.Unix(),
		Success:       false,
	}

	// Check if terramate is available
	if !e.available {
		result.Stderr = "Terramate binary not available - please install terramate"
		result.ExitCode = 1
		result.FinishedAtUnix = time.Now().Unix()
		return result
	}

	// Determine timeout
	timeout := e.defaultTimeout
	if cmd.TimeoutSeconds > 0 {
		timeout = time.Duration(cmd.TimeoutSeconds) * time.Second
	}

	// Create timeout context
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	workDir, err := resolveAgentWorkDir(e.basePath, cmd.CommandId, cmd.WorkingDirectory)
	if err != nil {
		result.Stderr = fmt.Sprintf("Invalid working directory: %v", err)
		result.ExitCode = 1
		result.FinishedAtUnix = time.Now().Unix()
		return result
	}

	e.log.Info("executing_grpc_terramate_command",
		"command_id", cmd.CommandId,
		"operation", cmd.Operation.String(),
		"working_dir", workDir,
		"timeout", timeout.String(),
	)

	// Ensure working directory exists
	if err := os.MkdirAll(workDir, 0755); err != nil {
		result.Stderr = fmt.Sprintf("Failed to create working directory: %v", err)
		result.ExitCode = 1
		result.FinishedAtUnix = time.Now().Unix()
		return result
	}

	// Convert gRPC operation to internal operation and execute
	var internalResult *TerramateResult
	var execErr error

	switch cmd.Operation {
	case agentpb.TerramateOperation_TERRAMATE_OPERATION_RUN:
		// Extract command from args
		var runCmd string
		var runArgs []string
		if len(cmd.Args) > 0 {
			runCmd = cmd.Args[0]
			if len(cmd.Args) > 1 {
				runArgs = cmd.Args[1:]
			}
		}
		internalResult, execErr = e.Run(ctx, workDir, runCmd, runArgs, cmd.Tags, cmd.ChangedOnly, timeout)

	case agentpb.TerramateOperation_TERRAMATE_OPERATION_SYNC_DRIFT:
		internalResult, execErr = e.SyncDrift(ctx, workDir, cmd.Tags, timeout)

	case agentpb.TerramateOperation_TERRAMATE_OPERATION_LIST_CHANGED:
		internalResult, execErr = e.ListChanged(ctx, workDir, cmd.Tags, timeout)

	default:
		result.Stderr = fmt.Sprintf("Unknown terramate operation: %v", cmd.Operation)
		result.ExitCode = 1
		result.FinishedAtUnix = time.Now().Unix()
		return result
	}

	// Convert internal result to gRPC result
	if internalResult != nil {
		result.Success = internalResult.Success
		result.Stdout = internalResult.Stdout
		result.Stderr = internalResult.Stderr
		result.ExitCode = clampIntToInt32(internalResult.ExitCode)
		result.ChangedStacks = internalResult.ChangedStacks

		// Convert drift reports
		if len(internalResult.DriftReports) > 0 {
			result.DriftReports = make([]*agentpb.DriftReport, len(internalResult.DriftReports))
			for i, dr := range internalResult.DriftReports {
				result.DriftReports[i] = &agentpb.DriftReport{
					StackPath:        dr.StackPath,
					HasDrift:         dr.HasDrift,
					DriftSummary:     dr.DriftSummary,
					DriftedResources: dr.DriftedResources,
				}
			}
		}
	}

	if execErr != nil {
		result.Stderr = execErr.Error()
		if result.ExitCode == 0 {
			result.ExitCode = 1
		}
	}

	result.FinishedAtUnix = time.Now().Unix()

	e.log.Info("grpc_terramate_command_completed",
		"command_id", cmd.CommandId,
		"operation", cmd.Operation.String(),
		"success", result.Success,
		"exit_code", result.ExitCode,
		"duration", time.Since(startTime).String(),
	)

	return result
}

// CleanupWorkspace removes a workspace directory and all its contents.
func (e *TerramateExecutor) CleanupWorkspace(workspaceName string) error {
	unlock := e.acquireLock(e.WorkspaceDir(workspaceName))
	defer unlock()

	workDir := e.WorkspaceDir(workspaceName)
	e.log.Info("cleaning_up_workspace",
		"workspace", workspaceName,
		"work_dir", workDir,
	)

	if err := os.RemoveAll(workDir); err != nil {
		return fmt.Errorf("failed to cleanup workspace %s: %w", workspaceName, err)
	}

	return nil
}

// ListWorkspaces returns a list of all workspace names.
func (e *TerramateExecutor) ListWorkspaces() ([]string, error) {
	entries, err := os.ReadDir(e.basePath)
	if err != nil {
		if os.IsNotExist(err) {
			return []string{}, nil
		}
		return nil, fmt.Errorf("failed to list workspaces: %w", err)
	}

	var workspaces []string
	for _, entry := range entries {
		if entry.IsDir() {
			workspaces = append(workspaces, entry.Name())
		}
	}

	return workspaces, nil
}

// ValidateTerramateProject checks if a workspace has a valid Terramate configuration.
func (e *TerramateExecutor) ValidateTerramateProject(workspaceName string) error {
	workDir := e.WorkspaceDir(workspaceName)

	// Check for terramate.tm.hcl or terramate.tm files
	tmHCL := filepath.Join(workDir, "terramate.tm.hcl")
	tmFile := filepath.Join(workDir, "terramate.tm")

	if _, err := os.Stat(tmHCL); err == nil {
		return nil // Valid - has terramate.tm.hcl
	}
	if _, err := os.Stat(tmFile); err == nil {
		return nil // Valid - has terramate.tm
	}

	return fmt.Errorf("workspace %q is not a valid Terramate project: missing terramate.tm.hcl or terramate.tm", workspaceName)
}

// GetStackInfo returns information about a specific stack.
func (e *TerramateExecutor) GetStackInfo(ctx context.Context, workspaceName, stackPath string) (map[string]interface{}, error) {
	if !e.available {
		return nil, fmt.Errorf("terramate binary not available")
	}

	workDir := e.WorkspaceDir(workspaceName)

	// Run terramate list with JSON output for the specific stack
	args := []string{"list", "--json", stackPath}
	result, err := e.runCommand(ctx, workDir, DefaultTerramateListChangedTimeout, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to get stack info: %w", err)
	}

	var info map[string]interface{}
	if err := json.Unmarshal([]byte(result.Stdout), &info); err != nil {
		// If JSON parsing fails, return basic info
		return map[string]interface{}{
			"path":   stackPath,
			"output": result.Stdout,
		}, nil
	}

	return info, nil
}
