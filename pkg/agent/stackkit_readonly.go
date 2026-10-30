package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/kombifyio/techstack/pkg/secrets"
)

const (
	// MaxReadOnlyOutputBytes bounds what one read-only observation may return.
	MaxReadOnlyOutputBytes = 4 << 20
	// hostSecurityHelpTimeout bounds the capability probe.
	hostSecurityHelpTimeout = 15 * time.Second
)

// ErrStackKitRuntimeUnavailable means no pinned StackKits runtime is installed
// or its pin no longer resolves; the Guard reports it as an observation gap,
// never as a finding about the host.
var ErrStackKitRuntimeUnavailable = errors.New("the pinned StackKits runtime is not available")

// ReadOnlyRun is one fixed, read-only StackKits CLI invocation. The caller
// builds Args from constants; nothing in it comes from a request.
type ReadOnlyRun struct {
	// Dir is the working directory; empty selects the system temp directory.
	Dir     string
	Args    []string
	Timeout time.Duration
}

// ReadOnlyResult is the bounded, redacted outcome of a ReadOnlyRun.
type ReadOnlyResult struct {
	ExitCode int
	Stdout   []byte
	Stderr   string
	// ReleaseVersion is the pinned StackKits release that answered.
	ReleaseVersion string
}

// RunReadOnly executes the admitted, pinned StackKits CLI with a fixed argument
// vector and the same child environment as a typed command. It exists for the
// Guard's own observations (host security verify, backup status), which are not
// typed commands: they carry no stack input and never mutate.
func (executor *StackKitExecutor) RunReadOnly(ctx context.Context, run ReadOnlyRun) (ReadOnlyResult, error) {
	if executor == nil || !executor.Available() {
		return ReadOnlyResult{}, ErrStackKitRuntimeUnavailable
	}
	executor.mu.RLock()
	defer executor.mu.RUnlock()
	timeout := run.Timeout
	if timeout <= 0 {
		timeout = time.Minute
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	unlock, err := executor.lockRuntime(runCtx, false)
	if err != nil {
		return ReadOnlyResult{}, err
	}
	defer unlock()
	release, err := executor.releaseCache().ResolvePin(executor.pinPath)
	if err != nil {
		return ReadOnlyResult{}, fmt.Errorf("%w: %v", ErrStackKitRuntimeUnavailable, err)
	}
	dir := strings.TrimSpace(run.Dir)
	if dir == "" {
		dir = os.TempDir()
	}
	stdout := newBoundedStackKitBuffer(MaxReadOnlyOutputBytes)
	stderr := newBoundedStackKitBuffer(64 << 10)
	process := newStackKitProcess(runCtx, release.BinaryPath(), run.Args...)
	process.Dir = dir
	process.Env = stackKitChildEnvironment(os.Environ())
	process.Stdout = stdout
	process.Stderr = io.Writer(stderr)
	result := ReadOnlyResult{ReleaseVersion: release.Receipt().Version}
	runErr := process.Run()
	if runCtx.Err() != nil {
		return result, fmt.Errorf("StackKits observation timed out after %s: %w", timeout, runCtx.Err())
	}
	result.ExitCode = exitCode(runErr)
	if runErr != nil && result.ExitCode == 1 && process.ProcessState == nil {
		return result, fmt.Errorf("StackKits observation did not start: %w", runErr)
	}
	result.Stdout = []byte(secrets.Redact(stdout.String()))
	result.Stderr = secrets.Redact(stderr.String())
	return result, nil
}

var hostSecurityHelpLine = regexp.MustCompile(`(?m)^\s+security\s`)

// HostSecuritySupported reports whether the pinned StackKits release ships
// `stackkit host security`. Older releases do not, which the Guard reports as
// an unsupported observation rather than a failure: it is decided by what the
// CLI's own command list says, never by a version guess.
func (executor *StackKitExecutor) HostSecuritySupported(ctx context.Context) bool {
	result, err := executor.RunReadOnly(ctx, ReadOnlyRun{Args: []string{"host", "--help"}, Timeout: hostSecurityHelpTimeout})
	return err == nil && result.ExitCode == 0 && hostSecurityHelpLine.Match(result.Stdout)
}
