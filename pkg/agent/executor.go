// Package agent provides Guard-side command execution for kombify Techstack.
// The live diagnostic surface is health_check and get_logs. StackKits
// lifecycle runs through the typed StackKit executor. Direct tofu/terramate
// executors are retired.
package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kombifyio/techstack/pkg/api/agentpb"
	"github.com/kombifyio/techstack/pkg/secrets"
)

const (
	commandTypeHealthCheck = "health_check"
	commandTypeGetLogs     = "get_logs"
	defaultCommandTimeout  = 5 * time.Minute
)

var errUnknownCommandType = errors.New("unknown command type")

func isAllowedCommandType(command string) bool {
	switch command {
	case commandTypeHealthCheck, commandTypeGetLogs:
		return true
	default:
		return false
	}
}

// ExecutorConfig holds configuration for the CommandExecutor.
type ExecutorConfig struct {
	// WorkDir is the working directory for IaC configurations and compose
	// projects (used by diagnostic handlers to locate per-project files).
	WorkDir string
	// DockerComposePath is the path to the docker compose binary
	// (defaults to "docker"). Used by health_check and get_logs handlers.
	DockerComposePath string
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
	defaultTimeout := config.DefaultTimeout
	if defaultTimeout == 0 {
		defaultTimeout = defaultCommandTimeout
	}
	log := config.Logger
	if log == nil {
		log = slog.Default()
	}
	log = log.With("component", "command-executor")
	return &CommandExecutor{
		workDir:           workDir,
		dockerComposePath: dockerPath,
		defaultTimeout:    defaultTimeout,
		log:               log,
	}, nil
}

// Execute dispatches a diagnostic command. Unknown types, including retired
// direct tofu/terramate/pulumi execute strings, fail closed.
func (e *CommandExecutor) Execute(ctx context.Context, cmd *agentpb.ExecuteCommand) (*agentpb.CommandResult, error) {
	startTime := time.Now()
	result := &agentpb.CommandResult{
		CommandId:     cmd.CommandId,
		StartedAtUnix: startTime.Unix(),
	}
	cmdType := cmd.Command
	if !isAllowedCommandType(cmdType) {
		e.log.Error("unknown_command_type", "command_id", cmd.CommandId, "command", cmd.Command)
		result.ExitCode = 1
		result.Stderr = fmt.Sprintf("Unknown command type: %s. Allowed types: health_check, get_logs", cmd.Command)
		result.FinishedAtUnix = time.Now().Unix()
		return result, fmt.Errorf("%w: %s", errUnknownCommandType, cmd.Command)
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
	case commandTypeHealthCheck:
		stdout, stderr, exitCode, execErr = e.handleHealthCheck(ctx, cmd)
	case commandTypeGetLogs:
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
