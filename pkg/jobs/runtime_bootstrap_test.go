package jobs

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kombifyio/techstack/internal/guardbootstrap"
	"golang.org/x/crypto/ssh"
)

func TestRuntimeTargetBootstrapScriptParsesAsPOSIXShell(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("POSIX shell is unavailable")
	}
	script := runtimeTargetBootstrapScript()
	cmd := exec.Command(sh, "-n")
	cmd.Stdin = strings.NewReader(script)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("bootstrap script syntax: %v: %s", err, output)
	}
}

func TestRuntimeTargetBootstrapErrorPolicy(t *testing.T) {
	tests := []struct {
		name, message, output, wantReason string
		wantRetry                         bool
	}{
		{"lost session", "bootstrap managed runtime target: wait: remote command exited without exit status or exit signal", "phase=docker_ready status=wait_begin", RuntimeTargetBootstrapSessionLost, true},
		{"Docker failure", "bootstrap managed runtime target: Process exited with status 1", "phase=docker_status status=failed\nCannot connect to the Docker daemon", RuntimeTargetBootstrapDockerFailed, false},
		{"agent convergence failure", "bootstrap managed runtime target: Process exited with status 1", "phase=agent_convergence status=failed reason=checksum_mismatch", RuntimeTargetBootstrapAgentFailed, false},
		{"SSH authentication failure", "bootstrap managed runtime target: Permission denied", "", RuntimeTargetBootstrapSSHAuth, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reason := classifyRuntimeTargetBootstrapError(errors.New(tt.message), tt.output)
			if reason != tt.wantReason {
				t.Fatalf("reason = %q, want %q", reason, tt.wantReason)
			}
			if got := isRuntimeTargetBootstrapRetryable(reason); got != tt.wantRetry {
				t.Fatalf("retryable = %v, want %v", got, tt.wantRetry)
			}
		})
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

func TestSSHRuntimeTargetBootstrapDialRetriesTransientReadinessErrors(t *testing.T) {
	var attempts atomic.Int32
	readinessErr := errors.New("dial tcp 203.0.113.10:22: connect: connection refused")
	bootstrapper := NewSSHRuntimeTargetBootstrapper(SSHRuntimeTargetBootstrapperConfig{
		Timeout:           12 * time.Millisecond,
		DialRetryInterval: time.Millisecond,
		Dial: func(string, string, *ssh.ClientConfig) (*ssh.Client, error) {
			attempts.Add(1)
			return nil, readinessErr
		},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Millisecond)
	defer cancel()

	_, _, err := bootstrapper.dial(ctx, &RuntimeActionTarget{
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
	if !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, readinessErr) {
		t.Fatalf("error = %v, want timeout and last readiness cause", err)
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
	var tried []string
	authErr := errors.New("ssh: handshake failed: ssh: unable to authenticate")
	bootstrapper := NewSSHRuntimeTargetBootstrapper(SSHRuntimeTargetBootstrapperConfig{
		Timeout:           time.Second,
		DialRetryInterval: time.Millisecond,
		Dial: func(_, _ string, config *ssh.ClientConfig) (*ssh.Client, error) {
			tried = append(tried, config.User)
			return nil, authErr
		},
	})

	_, _, err := bootstrapper.dial(context.Background(), &RuntimeActionTarget{
		Host: "203.0.113.10",
		User: "root",
		Port: 22,
	}, []ssh.AuthMethod{ssh.Password("secret")})
	if err == nil {
		t.Fatal("expected authentication failure")
	}
	seen := map[string]int{}
	for _, login := range tried {
		seen[login]++
	}
	for login, count := range seen {
		if count != 1 {
			t.Fatalf("login %q was dialed %d times, want one attempt per rejected login", login, count)
		}
	}
	if seen["root"] != 1 {
		t.Fatalf("dialed logins = %v, want the configured login attempted", tried)
	}
	if !errors.Is(err, authErr) {
		t.Fatalf("error = %v, want wrapped authentication cause", err)
	}
}

// Cloud host-security disables root SSH as part of what it enforces, so a
// rollout that provisioned as root has to continue on the non-root channel the
// same key authorizes instead of reporting the node unreachable.
func TestSSHRuntimeTargetBootstrapDialContinuesOnANonRootLogin(t *testing.T) {
	var tried []string
	rootAuthErr := errors.New("ssh: handshake failed: ssh: unable to authenticate")
	fallbackErr := errors.New("dial tcp 203.0.113.10:22: connect: connection refused")
	bootstrapper := NewSSHRuntimeTargetBootstrapper(SSHRuntimeTargetBootstrapperConfig{
		Timeout:           time.Second,
		DialRetryInterval: time.Millisecond,
		Dial: func(_, _ string, config *ssh.ClientConfig) (*ssh.Client, error) {
			tried = append(tried, config.User)
			if config.User == "root" {
				return nil, rootAuthErr
			}
			return nil, fallbackErr
		},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	_, _, err := bootstrapper.dial(ctx, &RuntimeActionTarget{Host: "203.0.113.10", User: "root", Port: 22},
		[]ssh.AuthMethod{ssh.Password("secret")})
	if err == nil {
		t.Fatal("expected the fallback login to report its own failure")
	}
	if len(tried) < 2 || tried[0] != "root" || tried[1] == "root" {
		t.Fatalf("dialed logins = %v, want a non-root login after root was rejected", tried)
	}
	if !errors.Is(err, fallbackErr) || errors.Is(err, rootAuthErr) {
		t.Fatalf("error = %v, want only the fallback login failure", err)
	}
}

// A managed host answers on different logins before and after hardening, so
// both directions have to stay reachable from either recorded login.
func TestRuntimeTargetBootstrapLoginsCoverHardenedAndUnhardenedHosts(t *testing.T) {
	for _, configured := range []string{"root", guardbootstrap.ExecutionChannelUser, "ubuntu"} {
		logins := runtimeTargetBootstrapLogins(configured)
		if logins[0] != configured {
			t.Fatalf("logins = %v, want the configured login %q first", logins, configured)
		}
		seen := map[string]bool{}
		for _, login := range logins {
			if seen[login] {
				t.Fatalf("logins = %v, want each login attempted once", logins)
			}
			seen[login] = true
		}
		// Before host-security holds only the provider login exists; after it
		// holds only the channel login does.
		if !seen["root"] || !seen[guardbootstrap.ExecutionChannelUser] {
			t.Fatalf("logins = %v, want both the provider and the channel login reachable", logins)
		}
	}
}
