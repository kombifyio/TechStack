package jobs

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kombifyio/techstack/internal/guardbootstrap"
	"golang.org/x/crypto/ssh"
)

// A host-bound enrollment must never fall back to a bearer downloader or
// overwrite the Guard. SSH admits only a protected, running canonical service;
// authenticated artifact/version proof stays with its runtime readiness gate.
func TestRuntimeTargetEnrolledAgentDelegatesOnlyToReadyProtectedGuard(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("POSIX shell is unavailable")
	}
	for _, tc := range []struct {
		name, legacy, state, owner, enabled, active, admission, status string
		newer                                                          bool
		wantFailure                                                    bool
	}{
		{name: "legacy root layout", legacy: "credential", owner: "0:600", status: "delegated"},
		{name: "migrated Guard layout", state: "credential", owner: "321:600", status: "delegated"},
		{name: "newer state credential", legacy: "credential", state: "credential", newer: true, owner: "321:644", status: "failed", wantFailure: true},
		{name: "newer revoked credential", legacy: "credential", state: "empty", newer: true, owner: "321:600", status: "failed", wantFailure: true},
		{name: "disabled Guard", legacy: "credential", owner: "0:600", enabled: "1", status: "failed", wantFailure: true},
		{name: "inactive Guard", legacy: "credential", owner: "0:600", active: "1", status: "failed", wantFailure: true},
		{name: "restarting Guard", state: "credential", owner: "321:600", active: "restart", status: "delegated"},
		{name: "unenrolled direct target", status: "not_applicable"},
		{name: "host admission denied", legacy: "credential", owner: "0:600", admission: "1", wantFailure: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := filepath.ToSlash(t.TempDir())
			for name, content := range map[string]string{"legacy": tc.legacy, "state": tc.state} {
				if content == "" {
					continue
				}
				if content == "empty" {
					content = ""
				}
				if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if tc.newer {
				old := time.Now().Add(-time.Hour)
				if err := os.Chtimes(filepath.Join(dir, "legacy"), old, old); err != nil {
					t.Fatal(err)
				}
			}
			// The host boundary is faked; run the production shell function itself.
			// For the old layout the protected enrollment is root-owned, while the
			// state copy uses the configured owner and may invalidate delegation.
			script := `set -u
log() { printf '%s\n' "$*"; }
admit_ufw_nftables() { :; }
admit_docker_firewall() { return "${ADMISSION:-0}"; }
admit_guard_host_writes() { :; }
id() { printf '321\n'; }
sleep() { :; }
curl() { printf 'unauthorized download\n' >> "$FIXTURE/effects"; return 1; }
install() { printf 'unsigned activation\n' >> "$FIXTURE/effects"; return 1; }
mv() { printf 'binary replacement\n' >> "$FIXTURE/effects"; return 1; }
host() {
  operation="$1"; shift
  case "$operation" in
    test)
      a="${2:-}"; b="${3:-}"
      case "$a" in /etc/techstack/agent-enrollment.json) a="$FIXTURE/legacy";; /var/lib/techstack/guard/agent-enrollment.json) a="$FIXTURE/state";; esac
      case "$b" in /etc/techstack/agent-enrollment.json) b="$FIXTURE/legacy";; /var/lib/techstack/guard/agent-enrollment.json) b="$FIXTURE/state";; esac
      if [ "$a" = "-nt" ]; then
        case "$1" in /var/lib/techstack/guard/agent-enrollment.json) test "$FIXTURE/state" -nt "$b";; *) return 1;; esac
      else test "$1" "$a"; fi
      ;;
    stat)
      if [ "$3" = "/etc/techstack/agent-enrollment.json" ]; then printf '0:600\n'; else printf '%s\n' "$OWNER"; fi
      ;;
    systemctl)
      case "$1" in
        is-enabled) return "${ENABLED:-0}";;
        is-active)
          if [ "${ACTIVE:-0}" = "restart" ]; then
            if test -f "$FIXTURE/restarted"; then return 0; fi
            printf 'restarting\n' > "$FIXTURE/restarted"; return 1
          fi
          return "${ACTIVE:-0}";;
        *) printf 'unexpected service mutation\n' >> "$FIXTURE/effects"; return 1;;
      esac
      ;;
    *) "$operation" "$@";;
  esac
}
SUDO=host
` + runtimeTargetEnrolledAgentConvergenceScript + "\nconverge_enrolled_agent\n"
			cmd := exec.Command(sh)
			cmd.Stdin = strings.NewReader(script)
			cmd.Env = append(os.Environ(), "FIXTURE="+dir, "OWNER="+tc.owner,
				"ENABLED="+firstNonEmpty(tc.enabled, "0"), "ACTIVE="+firstNonEmpty(tc.active, "0"), "ADMISSION="+firstNonEmpty(tc.admission, "0"))
			output, runErr := cmd.CombinedOutput()
			if (runErr != nil) != tc.wantFailure {
				t.Fatalf("bootstrap error=%v, output=%s", runErr, output)
			}
			status, digest := runtimeTargetAgentConvergenceProof(string(output))
			if status != tc.status || digest != "" {
				t.Fatalf("bootstrap proof=(%q, %q), output=%s", status, digest, output)
			}
			if effects, err := os.ReadFile(filepath.Join(dir, "effects")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("bootstrap attempted a competing update: %s (read error %v)", effects, err)
			}
		})
	}
}

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
	status, sha256 = runtimeTargetAgentConvergenceProof("phase=agent_convergence status=updated sha256=" + digest + "\nphase=agent_convergence status=delegated")
	if status != "delegated" || sha256 != "" {
		t.Fatalf("delegated convergence must not retain a previous artifact proof: (%q, %q)", status, sha256)
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

func TestSSHRuntimeTargetBootstrapTimeoutCoversAllRetries(t *testing.T) {
	readinessErr := errors.New("dial tcp 203.0.113.10:22: connect: connection refused")
	bootstrapper := NewSSHRuntimeTargetBootstrapper(SSHRuntimeTargetBootstrapperConfig{
		Timeout:           100 * time.Millisecond,
		MaxAttempts:       3,
		DialRetryInterval: time.Millisecond,
		RetryInterval:     time.Millisecond,
		Dial: func(string, string, *ssh.ClientConfig) (*ssh.Client, error) {
			return nil, readinessErr
		},
	})
	result, err := bootstrapper.BootstrapRuntimeTarget(context.Background(), &RuntimeActionTarget{
		Host: "203.0.113.10", Port: 22, User: "kombify", Password: "test-only",
	})
	if err == nil || result == nil || result.Status != "failed" {
		t.Fatalf("bootstrap result = %#v, error = %v, want failed timeout", result, err)
	}
	if result.DurationMS > 200 || !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, readinessErr) {
		t.Fatalf("bootstrap result = %#v, error = %v, want one bounded execution window and the network cause", result, err)
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
