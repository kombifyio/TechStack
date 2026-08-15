package agent

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"

	"github.com/kombifyio/techstack/pkg/validator"
)

// runCommand executes a command with given arguments.
func (e *CommandExecutor) runCommand(ctx context.Context, command string, args []string, workDir string, env map[string]string) (string, string, int, error) {
	// Security: Validate command name to prevent path injection and shell metacharacters
	if err := validator.ValidateCommandName(command); err != nil {
		return "", "", -1, fmt.Errorf("invalid command name: %w", err)
	}

	cmd := exec.CommandContext(ctx, command, args...)
	cmd.Dir = workDir

	// Set up environment
	cmd.Env = os.Environ()
	for k, v := range env {
		cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%s", k, v))
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	e.log.Debug("running_command",
		"command", command,
		"args", args,
		"work_dir", workDir,
	)

	err := cmd.Run()

	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
			// Don't return error for non-zero exit codes, just capture it
			err = nil
		} else if ctx.Err() == context.DeadlineExceeded {
			return stdout.String(), stderr.String(), -1, fmt.Errorf("command timed out: %w", ctx.Err())
		} else if ctx.Err() == context.Canceled {
			return stdout.String(), stderr.String(), -1, fmt.Errorf("command canceled: %w", ctx.Err())
		}
	}

	return stdout.String(), stderr.String(), exitCode, err
}

// isWindows returns true if running on Windows.
func isWindows() bool {
	return os.PathSeparator == '\\' && os.PathListSeparator == ';'
}
