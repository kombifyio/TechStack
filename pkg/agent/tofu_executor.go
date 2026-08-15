// Package agent provides the agent-side command execution for kombifyTechstack.
// This file implements the TofuExecutor for IAC-4 OpenTofu operations.
package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
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

// TofuOperation defines the types of tofu operations.
type TofuOperation string

const (
	// TofuOperationInit initializes a working directory.
	TofuOperationInit TofuOperation = "init"
	// TofuOperationPlan creates an execution plan.
	TofuOperationPlan TofuOperation = "plan"
	// TofuOperationApply applies the planned changes.
	TofuOperationApply TofuOperation = "apply"
	// TofuOperationDestroy destroys managed infrastructure.
	TofuOperationDestroy TofuOperation = "destroy"
	// TofuOperationOutput retrieves output values.
	TofuOperationOutput TofuOperation = "output"
)

// Default timeouts for tofu operations.
const (
	DefaultTofuInitTimeout    = 5 * time.Minute
	DefaultTofuPlanTimeout    = 10 * time.Minute
	DefaultTofuApplyTimeout   = 30 * time.Minute
	DefaultTofuDestroyTimeout = 30 * time.Minute
	DefaultTofuOutputTimeout  = 30 * time.Second
)

// Default base path for tofu working directories.
const DefaultTofuBasePath = "/opt/techstack/tofu"

// TofuExecutorConfig holds configuration for the TofuExecutor.
type TofuExecutorConfig struct {
	// BasePath is the base directory for tofu working directories.
	// Defaults to /opt/techstack/tofu on Linux or %LOCALAPPDATA%\techstack\tofu on Windows.
	BasePath string
	// Binary is the path to the tofu binary. Defaults to "tofu".
	Binary string
	// Logger is the structured logger to use.
	Logger *slog.Logger
	// DefaultTimeout is the default operation timeout if not specified.
	DefaultTimeout time.Duration
	// PluginCacheDir is the directory for caching provider plugins.
	PluginCacheDir string
}

// TofuExecutor handles execution of OpenTofu commands on the agent side.
type TofuExecutor struct {
	basePath       string
	binary         string
	pluginCacheDir string
	defaultTimeout time.Duration
	log            *slog.Logger

	// dirLocks provides per-directory locking to prevent concurrent state operations.
	dirLocks struct {
		sync.Mutex
		locks map[string]*sync.Mutex
	}
}

// TofuRequest contains parameters for a tofu operation.
type TofuRequest struct {
	// WorkspaceName is the name of the workspace (used as directory name).
	WorkspaceName string `json:"workspace_name"`
	// Operation is the tofu operation to perform.
	Operation TofuOperation `json:"operation"`
	// HCLContent is the HCL configuration content to write (optional).
	HCLContent string `json:"hcl_content,omitempty"`
	// TFVarsJSON is the tfvars content as JSON (optional).
	TFVarsJSON string `json:"tfvars_json,omitempty"`
	// AdditionalFiles is a map of filename to content for additional files (optional).
	AdditionalFiles map[string]string `json:"additional_files,omitempty"`
	// Timeout overrides the default timeout for this operation.
	Timeout time.Duration `json:"timeout,omitempty"`
}

// TofuResult contains the result of a tofu operation.
type TofuResult struct {
	// Success indicates whether the operation succeeded.
	Success bool `json:"success"`
	// Operation is the operation that was performed.
	Operation TofuOperation `json:"operation"`
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
	// PlanSummary contains parsed plan summary (for plan operations).
	PlanSummary *TofuPlanSummary `json:"plan_summary,omitempty"`
	// Outputs contains parsed outputs (for output operations).
	Outputs map[string]TofuOutputValue `json:"outputs,omitempty"`
	// Error contains the error message if the operation failed.
	Error string `json:"error,omitempty"`
}

// TofuPlanSummary contains parsed information from a plan operation.
type TofuPlanSummary struct {
	// ToAdd is the number of resources to add.
	ToAdd int `json:"to_add"`
	// ToChange is the number of resources to change.
	ToChange int `json:"to_change"`
	// ToDestroy is the number of resources to destroy.
	ToDestroy int `json:"to_destroy"`
	// HasChanges indicates whether there are any changes planned.
	HasChanges bool `json:"has_changes"`
}

// TofuOutputValue represents a terraform output value.
type TofuOutputValue struct {
	// Value is the output value.
	Value interface{} `json:"value"`
	// Type describes the output type.
	Type string `json:"type,omitempty"`
	// Sensitive indicates if the value is sensitive.
	Sensitive bool `json:"sensitive"`
}

// NewTofuExecutor creates a new TofuExecutor with the given configuration.
func NewTofuExecutor(config TofuExecutorConfig) (*TofuExecutor, error) {
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
			basePath = filepath.Join(localAppData, "techstack", "tofu")
		} else {
			basePath = DefaultTofuBasePath
		}
	}

	// Ensure base path exists
	if err := os.MkdirAll(basePath, 0755); err != nil {
		return nil, fmt.Errorf("failed to create base directory %s: %w", basePath, err)
	}

	// Set binary path
	binary := config.Binary
	if binary == "" {
		binary = "tofu"
	}

	// Verify tofu binary is available
	if _, err := exec.LookPath(binary); err != nil {
		return nil, fmt.Errorf("tofu binary not found in PATH: %w", err)
	}

	// Set plugin cache directory
	pluginCacheDir := config.PluginCacheDir
	if pluginCacheDir == "" {
		pluginCacheDir = os.Getenv("TF_PLUGIN_CACHE_DIR")
		if pluginCacheDir == "" {
			homeDir, _ := os.UserHomeDir()
			if homeDir != "" {
				pluginCacheDir = filepath.Join(homeDir, ".terraform.d", "plugin-cache")
			}
		}
	}
	if pluginCacheDir != "" {
		if err := os.MkdirAll(pluginCacheDir, 0755); err != nil {
			// Non-fatal: just log and continue without cache
			if config.Logger != nil {
				config.Logger.Warn("failed to create plugin cache directory",
					"path", pluginCacheDir,
					"error", err.Error(),
				)
			}
			pluginCacheDir = ""
		}
	}

	// Set default timeout
	defaultTimeout := config.DefaultTimeout
	if defaultTimeout == 0 {
		defaultTimeout = DefaultTofuApplyTimeout
	}

	// Set logger
	log := config.Logger
	if log == nil {
		log = slog.Default()
	}
	log = log.With("component", "tofu-executor")

	return &TofuExecutor{
		basePath:       basePath,
		binary:         binary,
		pluginCacheDir: pluginCacheDir,
		defaultTimeout: defaultTimeout,
		log:            log,
		dirLocks: struct {
			sync.Mutex
			locks map[string]*sync.Mutex
		}{
			locks: make(map[string]*sync.Mutex),
		},
	}, nil
}

// Execute runs a tofu operation based on the request.
func (e *TofuExecutor) Execute(ctx context.Context, req *TofuRequest) (*TofuResult, error) {
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

	// Write HCL content if provided
	if req.HCLContent != "" {
		mainFile := filepath.Join(workDir, "main.tf")
		if err := os.WriteFile(mainFile, []byte(req.HCLContent), 0644); err != nil {
			return nil, fmt.Errorf("failed to write HCL content: %w", err)
		}
		e.log.Debug("wrote_hcl_content",
			"workspace", req.WorkspaceName,
			"file", mainFile,
			"size", len(req.HCLContent),
		)
	}

	// Write tfvars.json if provided
	if req.TFVarsJSON != "" {
		varsFile := filepath.Join(workDir, "terraform.tfvars.json")
		if err := os.WriteFile(varsFile, []byte(req.TFVarsJSON), 0644); err != nil {
			return nil, fmt.Errorf("failed to write tfvars: %w", err)
		}
		e.log.Debug("wrote_tfvars",
			"workspace", req.WorkspaceName,
			"file", varsFile,
			"size", len(req.TFVarsJSON),
		)
	}

	// Write additional files if provided
	for filename, content := range req.AdditionalFiles {
		// Sanitize filename
		if strings.Contains(filename, "..") {
			return nil, fmt.Errorf("invalid filename %q: must not contain '..'", filename)
		}
		filePath := filepath.Join(workDir, filename)
		// Ensure parent directory exists
		if err := os.MkdirAll(filepath.Dir(filePath), 0755); err != nil {
			return nil, fmt.Errorf("failed to create directory for %s: %w", filename, err)
		}
		if err := os.WriteFile(filePath, []byte(content), 0644); err != nil {
			return nil, fmt.Errorf("failed to write file %s: %w", filename, err)
		}
		e.log.Debug("wrote_additional_file",
			"workspace", req.WorkspaceName,
			"file", filePath,
			"size", len(content),
		)
	}

	// Execute the operation
	switch req.Operation {
	case TofuOperationInit:
		return e.Init(ctx, workDir, req.Timeout)
	case TofuOperationPlan:
		return e.Plan(ctx, workDir, req.Timeout)
	case TofuOperationApply:
		return e.Apply(ctx, workDir, req.Timeout)
	case TofuOperationDestroy:
		return e.Destroy(ctx, workDir, req.Timeout)
	case TofuOperationOutput:
		return e.Output(ctx, workDir, req.Timeout)
	default:
		return nil, fmt.Errorf("unknown operation: %s", req.Operation)
	}
}

// Init runs `tofu init` in the specified directory.
func (e *TofuExecutor) Init(ctx context.Context, workDir string, timeout time.Duration) (*TofuResult, error) {
	if timeout == 0 {
		timeout = DefaultTofuInitTimeout
	}

	unlock := e.acquireLock(workDir)
	defer unlock()

	e.log.Info("tofu_init_starting",
		"work_dir", workDir,
		"timeout", timeout.String(),
	)

	result, err := e.runCommand(ctx, workDir, timeout, "init", "-input=false", "-no-color")
	result.Operation = TofuOperationInit

	if err != nil {
		e.log.Error("tofu_init_failed",
			"work_dir", workDir,
			"error", err.Error(),
			"duration", result.Duration.String(),
		)
	} else {
		e.log.Info("tofu_init_completed",
			"work_dir", workDir,
			"success", result.Success,
			"duration", result.Duration.String(),
		)
	}

	return result, err
}

// Plan runs `tofu plan` in the specified directory.
func (e *TofuExecutor) Plan(ctx context.Context, workDir string, timeout time.Duration) (*TofuResult, error) {
	if timeout == 0 {
		timeout = DefaultTofuPlanTimeout
	}

	unlock := e.acquireLock(workDir)
	defer unlock()

	e.log.Info("tofu_plan_starting",
		"work_dir", workDir,
		"timeout", timeout.String(),
	)

	result, err := e.runCommand(ctx, workDir, timeout, "plan", "-input=false", "-no-color", "-out=tfplan")
	result.Operation = TofuOperationPlan

	if result.Success {
		// Parse plan output for resource counts
		result.PlanSummary = e.parsePlanOutput(result.Stdout)
	}

	if err != nil {
		e.log.Error("tofu_plan_failed",
			"work_dir", workDir,
			"error", err.Error(),
			"duration", result.Duration.String(),
		)
	} else {
		e.log.Info("tofu_plan_completed",
			"work_dir", workDir,
			"success", result.Success,
			"duration", result.Duration.String(),
			"to_add", result.PlanSummary.ToAdd,
			"to_change", result.PlanSummary.ToChange,
			"to_destroy", result.PlanSummary.ToDestroy,
		)
	}

	return result, err
}

// Apply runs `tofu apply` in the specified directory.
func (e *TofuExecutor) Apply(ctx context.Context, workDir string, timeout time.Duration) (*TofuResult, error) {
	if timeout == 0 {
		timeout = DefaultTofuApplyTimeout
	}

	unlock := e.acquireLock(workDir)
	defer unlock()

	e.log.Info("tofu_apply_starting",
		"work_dir", workDir,
		"timeout", timeout.String(),
	)

	// Check if plan file exists and use it
	planFile := filepath.Join(workDir, "tfplan")
	args := []string{"apply", "-auto-approve", "-input=false", "-no-color"}
	if _, err := os.Stat(planFile); err == nil {
		args = append(args, planFile)
	}

	result, err := e.runCommand(ctx, workDir, timeout, args...)
	result.Operation = TofuOperationApply

	// Clean up plan file after apply (whether successful or not)
	if _, statErr := os.Stat(planFile); statErr == nil {
		if rmErr := os.Remove(planFile); rmErr != nil {
			e.log.Warn("failed_to_cleanup_plan_file",
				"file", planFile,
				"error", rmErr.Error(),
			)
		}
	}

	// If apply was successful, try to get outputs
	if result.Success {
		outputResult, outputErr := e.outputUnlocked(ctx, workDir)
		if outputErr == nil && outputResult.Outputs != nil {
			result.Outputs = outputResult.Outputs
		}
	}

	if err != nil {
		e.log.Error("tofu_apply_failed",
			"work_dir", workDir,
			"error", err.Error(),
			"duration", result.Duration.String(),
		)
	} else {
		e.log.Info("tofu_apply_completed",
			"work_dir", workDir,
			"success", result.Success,
			"duration", result.Duration.String(),
			"output_count", len(result.Outputs),
		)
	}

	return result, err
}

// Destroy runs `tofu destroy` in the specified directory.
func (e *TofuExecutor) Destroy(ctx context.Context, workDir string, timeout time.Duration) (*TofuResult, error) {
	if timeout == 0 {
		timeout = DefaultTofuDestroyTimeout
	}

	unlock := e.acquireLock(workDir)
	defer unlock()

	e.log.Info("tofu_destroy_starting",
		"work_dir", workDir,
		"timeout", timeout.String(),
	)

	result, err := e.runCommand(ctx, workDir, timeout, "destroy", "-auto-approve", "-input=false", "-no-color")
	result.Operation = TofuOperationDestroy

	if err != nil {
		e.log.Error("tofu_destroy_failed",
			"work_dir", workDir,
			"error", err.Error(),
			"duration", result.Duration.String(),
		)
	} else {
		e.log.Info("tofu_destroy_completed",
			"work_dir", workDir,
			"success", result.Success,
			"duration", result.Duration.String(),
		)
	}

	return result, err
}

// Output runs `tofu output -json` in the specified directory.
func (e *TofuExecutor) Output(ctx context.Context, workDir string, timeout time.Duration) (*TofuResult, error) {
	unlock := e.acquireLock(workDir)
	defer unlock()
	return e.outputUnlocked(ctx, workDir)
}

// outputUnlocked runs output without acquiring the lock (for internal use when lock is already held).
func (e *TofuExecutor) outputUnlocked(ctx context.Context, workDir string) (*TofuResult, error) {
	timeout := DefaultTofuOutputTimeout

	e.log.Debug("tofu_output_starting",
		"work_dir", workDir,
	)

	result, err := e.runCommand(ctx, workDir, timeout, "output", "-json")
	result.Operation = TofuOperationOutput

	if result.Success && result.Stdout != "" {
		// Parse JSON output
		outputs := make(map[string]TofuOutputValue)
		if parseErr := json.Unmarshal([]byte(result.Stdout), &outputs); parseErr != nil {
			e.log.Warn("failed_to_parse_tofu_output",
				"error", parseErr.Error(),
				"stdout", result.Stdout,
			)
		} else {
			result.Outputs = outputs
		}
	}

	return result, err
}

// runCommand executes a tofu command and returns the result.
func (e *TofuExecutor) runCommand(ctx context.Context, workDir string, timeout time.Duration, args ...string) (*TofuResult, error) {
	start := time.Now()

	result := &TofuResult{
		WorkingDir: workDir,
	}

	// Create timeout context
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, e.binary, args...)
	cmd.Dir = workDir

	// Build environment
	env := append(os.Environ(),
		"TF_IN_AUTOMATION=1",
		"TF_INPUT=0",
	)
	if e.pluginCacheDir != "" {
		env = append(env, "TF_PLUGIN_CACHE_DIR="+e.pluginCacheDir)
	}
	cmd.Env = env

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	e.log.Debug("running_tofu_command",
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
			return result, fmt.Errorf("tofu %s timed out after %v", strings.Join(args, " "), timeout)
		}
		if ctx.Err() == context.Canceled {
			result.Success = false
			result.ExitCode = -1
			result.Error = "command was canceled"
			return result, fmt.Errorf("tofu %s was canceled", strings.Join(args, " "))
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

		return result, fmt.Errorf("tofu %s failed: %w: %s", strings.Join(args, " "), err, errMsg)
	}

	result.Success = true
	result.ExitCode = 0
	return result, nil
}

// parsePlanOutput extracts resource counts from plan output.
func (e *TofuExecutor) parsePlanOutput(output string) *TofuPlanSummary {
	summary := &TofuPlanSummary{}

	// Pattern: "Plan: X to add, Y to change, Z to destroy."
	// Also matches: "No changes. Your infrastructure matches the configuration."
	planPattern := regexp.MustCompile(`Plan:\s*(\d+)\s*to add,\s*(\d+)\s*to change,\s*(\d+)\s*to destroy`)
	matches := planPattern.FindStringSubmatch(output)

	if len(matches) == 4 {
		fmt.Sscanf(matches[1], "%d", &summary.ToAdd)
		fmt.Sscanf(matches[2], "%d", &summary.ToChange)
		fmt.Sscanf(matches[3], "%d", &summary.ToDestroy)
		summary.HasChanges = summary.ToAdd > 0 || summary.ToChange > 0 || summary.ToDestroy > 0
	} else if strings.Contains(output, "No changes.") || strings.Contains(output, "no changes") {
		summary.HasChanges = false
	}

	return summary
}

// acquireLock acquires a lock for the given directory.
func (e *TofuExecutor) acquireLock(dir string) func() {
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
func (e *TofuExecutor) lockKeyForDir(dir string) string {
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
func (e *TofuExecutor) BasePath() string {
	return e.basePath
}

// WorkspaceDir returns the directory for a specific workspace.
func (e *TofuExecutor) WorkspaceDir(workspaceName string) string {
	return filepath.Join(e.basePath, workspaceName)
}

// WorkspaceExists checks if a workspace directory exists.
func (e *TofuExecutor) WorkspaceExists(workspaceName string) bool {
	workDir := e.WorkspaceDir(workspaceName)
	info, err := os.Stat(workDir)
	return err == nil && info.IsDir()
}

// CleanupWorkspace removes a workspace directory and all its contents.
func (e *TofuExecutor) CleanupWorkspace(workspaceName string) error {
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
func (e *TofuExecutor) ListWorkspaces() ([]string, error) {
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

// VerifyBinary checks if the tofu binary is available and returns its version.
func (e *TofuExecutor) VerifyBinary(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, e.binary, "version", "-json")
	output, err := cmd.Output()
	if err != nil {
		// Try without -json flag for older versions
		cmd = exec.CommandContext(ctx, e.binary, "version")
		output, err = cmd.Output()
		if err != nil {
			return "", fmt.Errorf("failed to get tofu version: %w", err)
		}
		return strings.TrimSpace(string(output)), nil
	}

	// Parse JSON version output
	var versionInfo struct {
		TerraformVersion string `json:"terraform_version"`
	}
	if err := json.Unmarshal(output, &versionInfo); err != nil {
		return strings.TrimSpace(string(output)), nil
	}

	return versionInfo.TerraformVersion, nil
}

// =============================================================================
// IAC-4: gRPC TofuCommand Integration
// These methods bridge the gRPC layer to the TofuExecutor
// =============================================================================

// ExecuteGRPC processes a gRPC TofuCommand and returns a TofuResult.
// This method bridges the agentpb.TofuCommand to the internal TofuExecutor.
func (e *TofuExecutor) ExecuteGRPC(ctx context.Context, cmd *agentpb.TofuCommand) *agentpb.TofuResult {
	startTime := time.Now()

	result := &agentpb.TofuResult{
		CommandId:     cmd.CommandId,
		StartedAtUnix: startTime.Unix(),
		Success:       false,
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

	e.log.Info("executing_grpc_tofu_command",
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

	// Write HCL content if provided
	if len(cmd.HclContent) > 0 {
		mainTF := filepath.Join(workDir, "main.tf")
		if err := os.WriteFile(mainTF, cmd.HclContent, 0644); err != nil {
			result.Stderr = fmt.Sprintf("Failed to write main.tf: %v", err)
			result.ExitCode = 1
			result.FinishedAtUnix = time.Now().Unix()
			return result
		}
		e.log.Debug("wrote_hcl_content", "path", mainTF, "size", len(cmd.HclContent))
	}

	// Write tfvars if provided
	if len(cmd.TfvarsJson) > 0 {
		tfvars := filepath.Join(workDir, "terraform.tfvars.json")
		if err := os.WriteFile(tfvars, cmd.TfvarsJson, 0644); err != nil {
			result.Stderr = fmt.Sprintf("Failed to write terraform.tfvars.json: %v", err)
			result.ExitCode = 1
			result.FinishedAtUnix = time.Now().Unix()
			return result
		}
		e.log.Debug("wrote_tfvars", "path", tfvars, "size", len(cmd.TfvarsJson))
	}

	// Convert gRPC operation to internal operation and execute
	var internalResult *TofuResult
	var execErr error

	switch cmd.Operation {
	case agentpb.TofuOperation_TOFU_OPERATION_INIT:
		internalResult, execErr = e.Init(ctx, workDir, timeout)
	case agentpb.TofuOperation_TOFU_OPERATION_PLAN:
		internalResult, execErr = e.planWithTargets(ctx, workDir, timeout, cmd.Targets)
	case agentpb.TofuOperation_TOFU_OPERATION_APPLY:
		internalResult, execErr = e.applyWithTargets(ctx, workDir, timeout, cmd.AutoApprove, cmd.Targets)
	case agentpb.TofuOperation_TOFU_OPERATION_DESTROY:
		internalResult, execErr = e.destroyWithTargets(ctx, workDir, timeout, cmd.AutoApprove, cmd.Targets)
	case agentpb.TofuOperation_TOFU_OPERATION_OUTPUT:
		internalResult, execErr = e.Output(ctx, workDir, timeout)
	default:
		result.Stderr = fmt.Sprintf("Unknown tofu operation: %v", cmd.Operation)
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

		// Convert outputs
		if len(internalResult.Outputs) > 0 {
			result.Outputs = make(map[string]string)
			for k, v := range internalResult.Outputs {
				if v.Sensitive {
					result.Outputs[k] = "[sensitive]"
				} else {
					switch val := v.Value.(type) {
					case string:
						result.Outputs[k] = val
					default:
						b, _ := json.Marshal(val)
						result.Outputs[k] = string(b)
					}
				}
			}
		}

		// Set plan summary
		if internalResult.PlanSummary != nil {
			result.PlanSummary = fmt.Sprintf("Plan: %d to add, %d to change, %d to destroy",
				internalResult.PlanSummary.ToAdd,
				internalResult.PlanSummary.ToChange,
				internalResult.PlanSummary.ToDestroy)
			result.ResourcesChanged = clampIntToInt32(internalResult.PlanSummary.ToAdd + internalResult.PlanSummary.ToChange + internalResult.PlanSummary.ToDestroy)
		}
	}

	if execErr != nil {
		result.Stderr = execErr.Error()
		if result.ExitCode == 0 {
			result.ExitCode = 1
		}
	}

	result.FinishedAtUnix = time.Now().Unix()

	// Calculate state hash
	stateFile := filepath.Join(workDir, "terraform.tfstate")
	if hash, err := calculateStateHash(stateFile); err == nil {
		result.StateHash = hash
	}

	e.log.Info("grpc_tofu_command_completed",
		"command_id", cmd.CommandId,
		"operation", cmd.Operation.String(),
		"success", result.Success,
		"exit_code", result.ExitCode,
		"duration", time.Since(startTime).String(),
	)

	return result
}

// planWithTargets runs `tofu plan` with optional targets.
func (e *TofuExecutor) planWithTargets(ctx context.Context, workDir string, timeout time.Duration, targets []string) (*TofuResult, error) {
	if timeout == 0 {
		timeout = DefaultTofuPlanTimeout
	}

	unlock := e.acquireLock(workDir)
	defer unlock()

	e.log.Info("tofu_plan_starting",
		"work_dir", workDir,
		"timeout", timeout.String(),
		"targets", targets,
	)

	args := []string{"plan", "-input=false", "-no-color", "-out=tfplan"}
	for _, target := range targets {
		args = append(args, "-target="+target)
	}

	result, err := e.runCommand(ctx, workDir, timeout, args...)
	result.Operation = TofuOperationPlan

	if result.Success {
		result.PlanSummary = e.parsePlanOutput(result.Stdout)
	}

	return result, err
}

// applyWithTargets runs `tofu apply` with optional targets and auto-approve.
func (e *TofuExecutor) applyWithTargets(ctx context.Context, workDir string, timeout time.Duration, autoApprove bool, targets []string) (*TofuResult, error) {
	if timeout == 0 {
		timeout = DefaultTofuApplyTimeout
	}

	unlock := e.acquireLock(workDir)
	defer unlock()

	e.log.Info("tofu_apply_starting",
		"work_dir", workDir,
		"timeout", timeout.String(),
		"auto_approve", autoApprove,
		"targets", targets,
	)

	args := []string{"apply", "-input=false", "-no-color"}
	if autoApprove {
		args = append(args, "-auto-approve")
	}
	for _, target := range targets {
		args = append(args, "-target="+target)
	}

	// Check if plan file exists
	planFile := filepath.Join(workDir, "tfplan")
	if _, err := os.Stat(planFile); err == nil && autoApprove {
		args = append(args, planFile)
	}

	result, err := e.runCommand(ctx, workDir, timeout, args...)
	result.Operation = TofuOperationApply

	// Clean up plan file
	if _, statErr := os.Stat(planFile); statErr == nil {
		_ = os.Remove(planFile)
	}

	// Get outputs after successful apply
	if result.Success {
		outputResult, outputErr := e.outputUnlocked(ctx, workDir)
		if outputErr == nil && outputResult.Outputs != nil {
			result.Outputs = outputResult.Outputs
		}
	}

	return result, err
}

// destroyWithTargets runs `tofu destroy` with optional targets and auto-approve.
func (e *TofuExecutor) destroyWithTargets(ctx context.Context, workDir string, timeout time.Duration, autoApprove bool, targets []string) (*TofuResult, error) {
	if timeout == 0 {
		timeout = DefaultTofuDestroyTimeout
	}

	unlock := e.acquireLock(workDir)
	defer unlock()

	e.log.Info("tofu_destroy_starting",
		"work_dir", workDir,
		"timeout", timeout.String(),
		"auto_approve", autoApprove,
		"targets", targets,
	)

	args := []string{"destroy", "-input=false", "-no-color"}
	if autoApprove {
		args = append(args, "-auto-approve")
	}
	for _, target := range targets {
		args = append(args, "-target="+target)
	}

	result, err := e.runCommand(ctx, workDir, timeout, args...)
	result.Operation = TofuOperationDestroy

	return result, err
}

// calculateStateHash calculates SHA256 hash of the state file.
func calculateStateHash(stateFile string) (string, error) {
	data, err := os.ReadFile(stateFile)
	if err != nil {
		return "", err
	}

	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:]), nil
}
