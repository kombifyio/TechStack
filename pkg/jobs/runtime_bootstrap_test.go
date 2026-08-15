package jobs

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func TestRuntimeTargetBootstrapScriptParsesAsPOSIXShell(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("POSIX shell is unavailable")
	}
	cmd := exec.Command(sh, "-n")
	cmd.Stdin = strings.NewReader(runtimeTargetBootstrapScript())
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("bootstrap script syntax: %v: %s", err, output)
	}
}

func TestClassifyRuntimeTargetBootstrapSessionLost(t *testing.T) {
	err := errors.New("bootstrap managed runtime target: wait: remote command exited without exit status or exit signal")
	if got := classifyRuntimeTargetBootstrapError(err, "phase=docker_ready status=wait_begin"); got != RuntimeTargetBootstrapSessionLost {
		t.Fatalf("reason = %q, want %q", got, RuntimeTargetBootstrapSessionLost)
	}
}

func TestClassifyRuntimeTargetBootstrapDockerFailure(t *testing.T) {
	err := errors.New("bootstrap managed runtime target: Process exited with status 1")
	output := "phase=docker_status status=failed\nCannot connect to the Docker daemon"
	if got := classifyRuntimeTargetBootstrapError(err, output); got != RuntimeTargetBootstrapDockerFailed {
		t.Fatalf("reason = %q, want %q", got, RuntimeTargetBootstrapDockerFailed)
	}
}

func TestClassifyRuntimeTargetBootstrapAgentConvergenceFailure(t *testing.T) {
	err := errors.New("bootstrap managed runtime target: Process exited with status 1")
	output := "phase=agent_convergence status=failed reason=checksum_mismatch"
	if got := classifyRuntimeTargetBootstrapError(err, output); got != RuntimeTargetBootstrapAgentFailed {
		t.Fatalf("reason = %q, want %q", got, RuntimeTargetBootstrapAgentFailed)
	}
}

func TestRuntimeTargetAgentConvergenceProofIsStructured(t *testing.T) {
	const digest = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	status, sha256 := runtimeTargetAgentConvergenceProof("phase=agent_convergence status=download_begin\nphase=agent_convergence status=updated sha256=" + digest)
	if status != "updated" || sha256 != digest {
		t.Fatalf("agent proof = (%q, %q)", status, sha256)
	}
	if message := runtimeTargetBootstrapReadyMessage(status); !strings.Contains(message, "updated") {
		t.Fatalf("ready message = %q", message)
	}

	status, sha256 = runtimeTargetAgentConvergenceProof("phase=agent_convergence status=failed reason=checksum_mismatch")
	if status != "failed" || sha256 != "" {
		t.Fatalf("failed agent proof = (%q, %q)", status, sha256)
	}
}

func TestRuntimeTargetBootstrapRetryPolicyIsBoundedToTransientReasons(t *testing.T) {
	if !isRuntimeTargetBootstrapRetryable(RuntimeTargetBootstrapSessionLost) {
		t.Fatal("session-lost bootstrap failures should be retryable")
	}
	if isRuntimeTargetBootstrapRetryable(RuntimeTargetBootstrapDockerFailed) {
		t.Fatal("docker bootstrap failures should not be blindly retried")
	}
	if isRuntimeTargetBootstrapRetryable(RuntimeTargetBootstrapSSHAuth) {
		t.Fatal("auth failures should not be retried")
	}
}

func TestSSHRuntimeTargetBootstrapDialRetriesTransientReadinessErrors(t *testing.T) {
	var attempts atomic.Int32
	bootstrapper := NewSSHRuntimeTargetBootstrapper(SSHRuntimeTargetBootstrapperConfig{
		Timeout:           12 * time.Millisecond,
		DialRetryInterval: time.Millisecond,
		Dial: func(string, string, *ssh.ClientConfig) (*ssh.Client, error) {
			attempts.Add(1)
			return nil, errors.New("dial tcp 203.0.113.10:22: connect: connection refused")
		},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Millisecond)
	defer cancel()

	_, err := bootstrapper.dial(ctx, &RuntimeActionTarget{
		Host: "203.0.113.10",
		User: "root",
		Port: 22,
	}, []ssh.AuthMethod{ssh.Password("secret")})
	if err == nil {
		t.Fatal("expected dial readiness timeout")
	}
	if got := attempts.Load(); got < 2 {
		t.Fatalf("dial attempts = %d, want retry after transient readiness error", got)
	}
	if !strings.Contains(err.Error(), "was not ready before timeout") {
		t.Fatalf("error = %q, want readiness timeout", err.Error())
	}
}

func TestRuntimeTargetBootstrapExecutionTimeoutLeavesDiagnosticAndTerminalReserve(t *testing.T) {
	postExecutionReserve := runtimeTargetBootstrapDiagnosticsReserve + runtimeTargetBootstrapTerminalPersistenceReserve
	wantUnbounded := defaultRuntimeTargetBootstrapTimeout - postExecutionReserve
	if got := runtimeTargetBootstrapExecutionTimeout(context.Background(), 0); got != wantUnbounded {
		t.Fatalf("unbounded bootstrap timeout = %s, want %s", got, wantUnbounded)
	}

	parent, cancel := context.WithTimeout(context.Background(), postExecutionReserve+time.Second)
	defer cancel()
	got := runtimeTargetBootstrapExecutionTimeout(parent, 10*time.Minute)
	if got <= 0 || got > time.Second {
		t.Fatalf("bootstrap timeout = %s, want the parent remainder before the post-execution reserve", got)
	}

	nearDeadline, cancelNearDeadline := context.WithTimeout(context.Background(), postExecutionReserve/2)
	defer cancelNearDeadline()
	if got := runtimeTargetBootstrapExecutionTimeout(nearDeadline, time.Minute); got != time.Nanosecond {
		t.Fatalf("near-deadline bootstrap timeout = %s, want immediate terminal timeout", got)
	}
}

func TestRuntimeTargetBootstrapUnboundedDefaultLeavesPostExecutionWindow(t *testing.T) {
	bootstrapBudget := runtimeTargetBootstrapExecutionTimeout(context.Background(), 0)
	postExecutionReserve := runtimeTargetBootstrapDiagnosticsReserve + runtimeTargetBootstrapTerminalPersistenceReserve

	if got, want := bootstrapBudget+postExecutionReserve, defaultRuntimeTargetBootstrapTimeout; got != want {
		t.Fatalf("bootstrap plus post-execution window = %s, want outer poll budget %s", got, want)
	}
	if got, want := postExecutionReserve, time.Minute; got != want {
		t.Fatalf("post-execution reserve = %s, want one minute for diagnostics and queue result handoff", got)
	}
	if got := runtimeTargetBootstrapExecutionTimeout(context.Background(), 30*time.Second); got != 30*time.Second {
		t.Fatalf("explicit short bootstrap timeout = %s, want 30s fast-fail budget", got)
	}
}

func TestSSHRuntimeTargetBootstrapperDefaultWiringLeavesPostExecutionWindow(t *testing.T) {
	bootstrapper := NewSSHRuntimeTargetBootstrapper(SSHRuntimeTargetBootstrapperConfig{})
	if !bootstrapper.timeoutDefaulted {
		t.Fatal("zero-value production config must retain its default provenance")
	}
	if got, want := bootstrapper.executionTimeout(context.Background()), defaultRuntimeTargetBootstrapTimeout-runtimeTargetBootstrapDiagnosticsReserve-runtimeTargetBootstrapTerminalPersistenceReserve; got != want {
		t.Fatalf("production default execution timeout = %s, want %s", got, want)
	}

	explicit := NewSSHRuntimeTargetBootstrapper(SSHRuntimeTargetBootstrapperConfig{Timeout: defaultRuntimeTargetBootstrapTimeout})
	if explicit.timeoutDefaulted {
		t.Fatal("explicit four-minute operator budget must not be treated as the product default")
	}
}

func TestPreferExecutedBootstrapProofKeepsRealReceipt(t *testing.T) {
	executed := &RuntimeTargetBootstrapResult{
		Status: "ready", ReasonCode: RuntimeTargetBootstrapReady,
		Message: "docker runtime ready", Output: "docker install log", DurationMS: 4200, Attempts: 1,
	}
	prep := &RuntimeTargetBootstrapResult{
		Status: "ready", ReasonCode: RuntimeTargetBootstrapNotApplicable,
		Message: "StackKits has no governed host preparation for a canonical v2 StackSpec",
	}
	merged := preferExecutedBootstrapProof(executed, prep)
	if merged.ReasonCode != RuntimeTargetBootstrapReady {
		t.Fatalf("reason = %q, want the executed bootstrap's reason", merged.ReasonCode)
	}
	if merged.Output != "docker install log" || merged.Attempts != 1 || merged.DurationMS != 4200 {
		t.Fatalf("executed proof fields were dropped: %#v", merged)
	}
	if !strings.Contains(merged.Message, "docker runtime ready") || !strings.Contains(merged.Message, "no governed host preparation") {
		t.Fatalf("message should carry both receipts, got %q", merged.Message)
	}

	failedPrep := &RuntimeTargetBootstrapResult{Status: "failed", ReasonCode: RuntimeTargetBootstrapDockerFailed}
	if got := preferExecutedBootstrapProof(executed, failedPrep); got != failedPrep {
		t.Fatalf("a failed prep receipt must win, got %#v", got)
	}
	if got := preferExecutedBootstrapProof(nil, prep); got != prep {
		t.Fatalf("without an executed bootstrap the prep receipt stands, got %#v", got)
	}
	if got := preferExecutedBootstrapProof(executed, nil); got != executed {
		t.Fatalf("nil prep must fall back to the executed proof, got %#v", got)
	}
}

func TestSSHRuntimeTargetBootstrapDialDoesNotRetryAuthenticationFailure(t *testing.T) {
	var attempts atomic.Int32
	bootstrapper := NewSSHRuntimeTargetBootstrapper(SSHRuntimeTargetBootstrapperConfig{
		Timeout:           time.Second,
		DialRetryInterval: time.Millisecond,
		Dial: func(string, string, *ssh.ClientConfig) (*ssh.Client, error) {
			attempts.Add(1)
			return nil, errors.New("ssh: handshake failed: ssh: unable to authenticate")
		},
	})

	_, err := bootstrapper.dial(context.Background(), &RuntimeActionTarget{
		Host: "203.0.113.10",
		User: "root",
		Port: 22,
	}, []ssh.AuthMethod{ssh.Password("secret")})
	if err == nil {
		t.Fatal("expected authentication failure")
	}
	if got := attempts.Load(); got != 1 {
		t.Fatalf("dial attempts = %d, want one non-readiness attempt", got)
	}
	if !strings.Contains(err.Error(), "unable to authenticate") {
		t.Fatalf("error = %q, want auth failure", err.Error())
	}
}
