// Package agent provides the agent-side command execution for kombifyTechstack.
// As of IAC-4 + Phase 7.2, this package handles only the IaC-First command
// surface: diagnostic commands (health_check, get_logs) directly, plus
// tofu / terramate / pulumi_operation via dedicated executors. Legacy compose
// deployment paths have been removed.
package agent

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/kombifyio/techstack/pkg/api/agentpb"
	"github.com/kombifyio/techstack/pkg/secrets"
)

// CommandType defines the command-type strings the agent recognises.
type CommandType string

const (
	// CommandTypeHealthCheck checks health of services on the agent host.
	CommandTypeHealthCheck CommandType = "health_check"
	// CommandTypeGetLogs retrieves service logs for debugging.
	CommandTypeGetLogs CommandType = "get_logs"
	// CommandTypeTofu executes OpenTofu operations (init, plan, apply, destroy).
	// Routed through TofuExecutor (see tofu_executor.go), not CommandExecutor.
	CommandTypeTofu CommandType = "tofu"
	// CommandTypeTerramate executes Terramate operations (run, sync-drift, list-changed).
	// Routed through TerramateExecutor (see terramate_executor.go), not CommandExecutor.
	CommandTypeTerramate CommandType = "terramate"
	// CommandTypePulumiOperation executes signed StackKit Pulumi operation bundles.
	CommandTypePulumiOperation CommandType = "pulumi_operation"
)

// AllowedCommandTypes is the whitelist of command types the IaC-First agent
// will accept. Anything else is rejected with an "unknown command type" error.
var AllowedCommandTypes = map[CommandType]bool{
	CommandTypeHealthCheck:     true,
	CommandTypeGetLogs:         true,
	CommandTypeTofu:            true,
	CommandTypeTerramate:       true,
	CommandTypePulumiOperation: true,
}

// Default timeouts for various operations.
const (
	DefaultCommandTimeout   = 5 * time.Minute
	DefaultTofuTimeout      = 30 * time.Minute
	DefaultLogsTimeout      = 30 * time.Second
	DefaultHealthTimeout    = 30 * time.Second
	DefaultTerramateTimeout = 10 * time.Minute
)

// ExecutorConfig holds configuration for the CommandExecutor.
type ExecutorConfig struct {
	// WorkDir is the working directory for IaC configurations and compose
	// projects (used by diagnostic handlers to locate per-project files).
	WorkDir string
	// DockerComposePath is the path to the docker compose binary
	// (defaults to "docker"). Used by health_check and get_logs handlers.
	DockerComposePath string
	// TofuPath is the path to the OpenTofu binary (defaults to "tofu").
	TofuPath string
	// TerramatePath is the path to the Terramate binary (defaults to "terramate").
	TerramatePath string
	// Logger is the structured logger to use.
	Logger *slog.Logger
	// DefaultTimeout is the default command timeout if not specified.
	DefaultTimeout time.Duration
}

// CommandExecutor handles execution of commands received from Core. As of
// IAC-4 this executor handles only the diagnostic command types directly;
// tofu/terramate/pulumi_operation are routed through dedicated executors.
type CommandExecutor struct {
	workDir           string
	dockerComposePath string
	tofuPath          string
	terramatePath     string
	defaultTimeout    time.Duration
	log               *slog.Logger
}

// NewCommandExecutor creates a new CommandExecutor with the given configuration.
func NewCommandExecutor(config ExecutorConfig) (*CommandExecutor, error) {
	workDir := config.WorkDir
	if workDir == "" {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("failed to get home directory: %w", err)
		}
		workDir = filepath.Join(homeDir, ".techstack", "deployments")
	}
	if err := os.MkdirAll(workDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create work directory %s: %w", workDir, err)
	}
	dockerPath := config.DockerComposePath
	if dockerPath == "" {
		dockerPath = "docker"
	}
	tofuPath := config.TofuPath
	if tofuPath == "" {
		tofuPath = "tofu"
	}
	if _, err := exec.LookPath(tofuPath); err != nil {
		if config.Logger != nil {
			config.Logger.Warn("tofu_not_found",
				"path", tofuPath,
				"message", "OpenTofu binary not found in PATH - IaC operations will fail",
			)
		}
	}
	terramatePath := config.TerramatePath
	if terramatePath == "" {
		terramatePath = "terramate"
	}
	defaultTimeout := config.DefaultTimeout
	if defaultTimeout == 0 {
		defaultTimeout = DefaultCommandTimeout
	}
	log := config.Logger
	if log == nil {
		log = slog.Default()
	}
	log = log.With("component", "command-executor")
	return &CommandExecutor{
		workDir:           workDir,
		dockerComposePath: dockerPath,
		tofuPath:          tofuPath,
		terramatePath:     terramatePath,
		defaultTimeout:    defaultTimeout,
		log:               log,
	}, nil
}

// Execute dispatches a command to the appropriate handler based on command
// type. Only diagnostic types (health_check, get_logs) are handled directly;
// tofu, terramate, and pulumi_operation are accepted by the allowlist but routed
// through their dedicated executors elsewhere. Reaching the default branch here
// means a caller bypassed the routing layer.
func (e *CommandExecutor) Execute(ctx context.Context, cmd *agentpb.ExecuteCommand) (*agentpb.CommandResult, error) {
	startTime := time.Now()
	result := &agentpb.CommandResult{
		CommandId:     cmd.CommandId,
		StartedAtUnix: startTime.Unix(),
	}
	cmdType := CommandType(cmd.Command)
	if !AllowedCommandTypes[cmdType] {
		e.log.Error("unknown_command_type", "command_id", cmd.CommandId, "command", cmd.Command)
		result.ExitCode = 1
		result.Stderr = fmt.Sprintf("Unknown command type: %s. Allowed types: health_check, get_logs, tofu, terramate, pulumi_operation", cmd.Command)
		result.FinishedAtUnix = time.Now().Unix()
		return result, fmt.Errorf("unknown command type: %s", cmd.Command)
	}
	timeout := e.defaultTimeout
	if cmd.TimeoutSeconds > 0 {
		timeout = time.Duration(cmd.TimeoutSeconds) * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	e.log.Info("executing_command", "command_id", cmd.CommandId, "command", cmd.Command, "timeout", timeout.String())

	var stdout, stderr string
	var exitCode int
	var execErr error
	switch cmdType {
	case CommandTypeHealthCheck:
		stdout, stderr, exitCode, execErr = e.handleHealthCheck(ctx, cmd)
	case CommandTypeGetLogs:
		stdout, stderr, exitCode, execErr = e.handleGetLogs(ctx, cmd)
	default:
		stdout, stderr, exitCode, execErr = "",
			fmt.Sprintf("Command type %q must be routed via its dedicated executor", cmd.Command),
			1, fmt.Errorf("command type %q not handled by CommandExecutor.Execute", cmd.Command)
	}

	// Phase 7.3 redaction wire (lives in this file post-7.2 because executor_compose.go is gone).
	result.Stdout = secrets.Redact(stdout)
	result.Stderr = secrets.Redact(stderr)
	result.ExitCode = int32(exitCode)
	result.FinishedAtUnix = time.Now().Unix()

	if execErr != nil {
		e.log.Error("command_execution_failed", "command_id", cmd.CommandId, "error", execErr.Error(), "exit_code", exitCode)
		if !strings.Contains(stderr, execErr.Error()) {
			if stderr != "" {
				stderr += "\n"
			}
			result.Stderr = secrets.Redact(stderr + "Error: " + execErr.Error())
		}
	} else {
		e.log.Info("command_execution_completed", "command_id", cmd.CommandId, "exit_code", exitCode, "duration", time.Since(startTime).String())
	}
	return result, execErr
}
