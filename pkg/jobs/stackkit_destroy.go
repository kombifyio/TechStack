package jobs

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

func destroyStackKitWorkspace(ctx context.Context, workDir, workload string, scope ...StackKitLifecycleRequest) error {
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

	resolve := pinnedStackKitRemoveBinary
	if StageCandidateReleaseConfigured() {
		resolve = func() (string, error) {
			if len(scope) != 1 {
				return "", fmt.Errorf("candidate stack removal needs actual tenant and Stack scope")
			}
			release, err := PinnedStackKitReleaseForStack(ctx, scope[0].TenantID, scope[0].StackID)
			if err != nil {
				return "", err
			}
			if release == nil {
				return "", fmt.Errorf("candidate removal controller is unavailable")
			}
			return release.BinaryPath(), nil
		}
	}
	binary, err := resolve()
	if err != nil {
		return err
	}
	args := []string{
		"--no-log",
		"--chdir", workDir,
		"remove",
		"--auto-approve",
		"--json",
		"--workload", workload,
	}
	cmd := exec.CommandContext(runCtx, binary, args...) // #nosec G702 -- the executable is the admitted pinned release or the Windows bundle's pinned CLI; arguments are passed without a shell.
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

// pinnedStackKitRemoveBinary resolves the executable for StackKits remove:
// the Windows client's bundled pinned CLI (TECHSTACK_STACKKIT_CLI) or the
// admitted pinned release. A PATH lookup or source-built CLI is never used.
func pinnedStackKitRemoveBinary() (string, error) {
	if bundled := strings.TrimSpace(os.Getenv(stackKitCLIEnv)); bundled != "" {
		return bundled, nil
	}
	release, err := configuredPinnedStackKitRelease()
	if err != nil {
		return "", err
	}
	if release == nil {
		return "", fmt.Errorf("StackKits remove requires the pinned published release (%s and %s)", stackKitReleasePinEnv, stackKitReleaseCacheEnv)
	}
	return release.BinaryPath(), nil
}
