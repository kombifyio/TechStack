package jobs

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

func destroyStackKitWorkspace(ctx context.Context, workDir, workload string) error {
	workload = strings.TrimSpace(workload)
	if workload == "" {
		return fmt.Errorf("StackKits remove requires a workload reference")
	}
	workDir = strings.TrimSpace(workDir)
	if workDir == "" {
		return fmt.Errorf("StackKits remove requires a workspace directory")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	timeout := runtimeActionHTTPTimeout
	if timeout <= 0 {
		timeout = 12 * time.Minute
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	binary := firstNonEmpty(strings.TrimSpace(os.Getenv(stackKitCLIEnv)), defaultStackKitCLIBinary)
	args := []string{
		"--no-log",
		"--chdir", workDir,
		"remove",
		"--auto-approve",
		"--json",
		"--workload", workload,
	}
	cmd := exec.CommandContext(runCtx, binary, args...) // #nosec G702 -- the operator-owned process environment selects the executable; arguments are passed without a shell.
	cmd.Dir = workDir
	cmd.Env = os.Environ()
	output, err := cmd.CombinedOutput()
	if err != nil {
		if runCtx.Err() != nil {
			return fmt.Errorf("StackKits remove timed out after %s: %w", timeout, runCtx.Err())
		}
		return fmt.Errorf("StackKits remove failed: %w: %s", err, tailText(string(output), 4000))
	}
	return nil
}
