package jobs

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kombifyio/techstack/internal/guardbootstrap"
	"github.com/kombifyio/techstack/pkg/secrets"
	"golang.org/x/crypto/ssh"
)

const (
	// Keep the SSH host-convergence phase inside the five-minute managed
	// rollout observation window. The four-minute bootstrap budget itself
	// reserves one minute for bounded diagnostics and durable terminal-job
	// projection, so a slow target cannot leave the operator at docker_ready
	// until the caller gives up observing the job.
	defaultRuntimeTargetBootstrapTimeout             = 4 * time.Minute
	runtimeTargetBootstrapDiagnosticsReserve         = defaultRuntimeDiagnosticsTimeout + 15*time.Second
	runtimeTargetBootstrapTerminalPersistenceReserve = 20 * time.Second
	defaultRuntimeTargetBootstrapMaxLog              = 12 * 1024
	defaultRuntimeTargetBootstrapDialRetryInterval   = 5 * time.Second
	defaultRuntimeTargetBootstrapRetryInterval       = 10 * time.Second
	defaultRuntimeTargetBootstrapMaxAttempts         = 3

	RuntimeTargetBootstrapReady = "target_bootstrap_ready"
	// RuntimeTargetBootstrapNotApplicable marks a preparation step that has
	// nothing to run rather than one that succeeded. The StackKits v2 line has
	// no governed host preparation, so claiming readiness it never checked
	// would be a false receipt.
	RuntimeTargetBootstrapNotApplicable = "target_bootstrap_not_applicable"
	RuntimeTargetBootstrapUnknown       = "target_bootstrap_failed"
	RuntimeTargetBootstrapSSHNotReady   = "target_bootstrap_ssh_not_ready"
	RuntimeTargetBootstrapSSHAuth       = "target_bootstrap_ssh_auth_failed"
	RuntimeTargetBootstrapSessionLost   = "target_bootstrap_session_lost"
	RuntimeTargetBootstrapAgentFailed   = "target_bootstrap_agent_convergence_failed"
	RuntimeTargetBootstrapDockerFailed  = "target_bootstrap_docker_failed"
	RuntimeTargetBootstrapTimeout       = "target_bootstrap_timeout"
	RuntimeTargetBootstrapCanceled      = "target_bootstrap_canceled"
)

type sshBootstrapDialFunc func(network, addr string, config *ssh.ClientConfig) (*ssh.Client, error)

type SSHRuntimeTargetBootstrapperConfig struct {
	Timeout           time.Duration
	MaxOutputSize     int
	DialRetryInterval time.Duration
	RetryInterval     time.Duration
	MaxAttempts       int
	Now               func() time.Time
	Dial              sshBootstrapDialFunc
	// HostKeys pins the first observed host key per target address and
	// verifies later connections against it. A nil store keeps the legacy
	// permissive dial for tests and disabled wiring only.
	HostKeys *RuntimeHostKeyStore
}

type SSHRuntimeTargetBootstrapper struct {
	timeout           time.Duration
	timeoutDefaulted  bool
	maxOutputSize     int
	dialRetryInterval time.Duration
	retryInterval     time.Duration
	maxAttempts       int
	now               func() time.Time
	dialSSH           sshBootstrapDialFunc
	hostKeys          *RuntimeHostKeyStore
}

type RuntimeTargetBootstrapResult struct {
	Status      string                     `json:"status"`
	ReasonCode  string                     `json:"reason_code,omitempty"`
	Message     string                     `json:"message,omitempty"`
	Output      string                     `json:"output,omitempty"`
	Events      []stackKitCLIProgressEvent `json:"events,omitempty"`
	DurationMS  int64                      `json:"duration_ms"`
	Attempts    int                        `json:"attempts,omitempty"`
	AgentStatus string                     `json:"agent_status,omitempty"`
	AgentSHA256 string                     `json:"agent_sha256,omitempty"`
}

// preferExecutedBootstrapProof keeps the receipt of the bootstrap that actually
// ran on the target. The pinned StackKits CLI prep answers a canonical v2
// StackSpec with a bare not-applicable receipt; persisting that over the real
// SSH/Docker bootstrap proof would erase the only evidence of what executed.
func preferExecutedBootstrapProof(executed, prep *RuntimeTargetBootstrapResult) *RuntimeTargetBootstrapResult {
	if prep == nil {
		return executed
	}
	if executed == nil || prep.ReasonCode != RuntimeTargetBootstrapNotApplicable {
		return prep
	}
	merged := *executed
	if note := strings.TrimSpace(prep.Message); note != "" {
		if strings.TrimSpace(merged.Message) != "" {
			merged.Message += "; " + note
		} else {
			merged.Message = note
		}
	}
	return &merged
}

func NewSSHRuntimeTargetBootstrapper(cfg SSHRuntimeTargetBootstrapperConfig) *SSHRuntimeTargetBootstrapper {
	timeout := cfg.Timeout
	timeoutDefaulted := timeout <= 0
	if timeout <= 0 {
		timeout = defaultRuntimeTargetBootstrapTimeout
	}
	maxOutputSize := cfg.MaxOutputSize
	if maxOutputSize <= 0 {
		maxOutputSize = defaultRuntimeTargetBootstrapMaxLog
	}
	dialRetryInterval := cfg.DialRetryInterval
	if dialRetryInterval <= 0 {
		dialRetryInterval = defaultRuntimeTargetBootstrapDialRetryInterval
	}
	retryInterval := cfg.RetryInterval
	if retryInterval <= 0 {
		retryInterval = defaultRuntimeTargetBootstrapRetryInterval
	}
	maxAttempts := cfg.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = defaultRuntimeTargetBootstrapMaxAttempts
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	dialSSH := cfg.Dial
	if dialSSH == nil {
		dialSSH = ssh.Dial
	}
	return &SSHRuntimeTargetBootstrapper{
		timeout:           timeout,
		timeoutDefaulted:  timeoutDefaulted,
		maxOutputSize:     maxOutputSize,
		dialRetryInterval: dialRetryInterval,
		retryInterval:     retryInterval,
		maxAttempts:       maxAttempts,
		now:               now,
		dialSSH:           dialSSH,
		hostKeys:          cfg.HostKeys,
	}
}

func (b *SSHRuntimeTargetBootstrapper) BootstrapRuntimeTarget(ctx context.Context, target *RuntimeActionTarget) (*RuntimeTargetBootstrapResult, error) {
	return b.bootstrapRuntimeTarget(ctx, target, nil)
}

func (b *SSHRuntimeTargetBootstrapper) BootstrapRuntimeTargetWithProgress(ctx context.Context, target *RuntimeActionTarget, progress func(stackKitCLIProgressEvent)) (*RuntimeTargetBootstrapResult, error) {
	return b.bootstrapRuntimeTarget(ctx, target, progress)
}

func (b *SSHRuntimeTargetBootstrapper) bootstrapRuntimeTarget(ctx context.Context, requested *RuntimeActionTarget, progress func(stackKitCLIProgressEvent)) (*RuntimeTargetBootstrapResult, error) {
	if b == nil {
		return nil, nil
	}
	started := b.now()
	target := normalizeRuntimeActionTarget(requested)
	if target == nil {
		return nil, nil
	}
	authMethods, authErr := runtimeDiagnosticsSSHAuthMethods(target)
	if authErr != nil {
		return nil, authErr
	}
	if len(authMethods) == 0 {
		return nil, nil
	}
	var lastErr error
	var lastOutput string
	attempts := 0
	for attempts < b.maxAttempts {
		attempts++
		// Each attempt is a bounded, idempotent convergence phase. A package
		// install or first-boot preparation that consumes one phase resumes from
		// the observed host state instead of exhausting the whole rollout budget.
		attemptCtx, cancelAttempt := context.WithTimeout(ctx, b.executionTimeout(ctx))
		client, login, err := b.dial(attemptCtx, target, authMethods)
		if err == nil {
			// The remaining rollout addresses the node through the login that
			// actually answered, not the one the record was written with.
			target.User = login
			if requested != nil {
				requested.User = login
			}
			output, runErr := b.runCommand(attemptCtx, client, runtimeTargetBootstrapScript(), progress)
			_ = client.Close()
			lastOutput = appendBootstrapAttemptOutput(lastOutput, attempts, output)
			if runErr == nil {
				cancelAttempt()
				durationMS := maxInt64(0, b.now().Sub(started).Milliseconds())
				output = truncateRuntimeDiagnosticsOutput(secrets.Redact(lastOutput), b.maxOutputSize)
				agentStatus, agentSHA256 := runtimeTargetAgentConvergenceProof(output)
				return &RuntimeTargetBootstrapResult{
					Status:      "ready",
					ReasonCode:  RuntimeTargetBootstrapReady,
					Message:     runtimeTargetBootstrapReadyMessage(agentStatus),
					Output:      output,
					DurationMS:  durationMS,
					Attempts:    attempts,
					AgentStatus: agentStatus,
					AgentSHA256: agentSHA256,
				}, nil
			}
			err = runErr
		}
		cancelAttempt()
		lastErr = err
		reason := classifyRuntimeTargetBootstrapError(err, lastOutput)
		if !isRuntimeTargetBootstrapRetryable(reason) || attempts >= b.maxAttempts {
			break
		}
		if waitErr := waitForRuntimeTargetSSHRetry(ctx, b.retryInterval); waitErr != nil {
			lastErr = waitErr
			break
		}
	}

	durationMS := maxInt64(0, b.now().Sub(started).Milliseconds())
	output := truncateRuntimeDiagnosticsOutput(secrets.Redact(lastOutput), b.maxOutputSize)
	reason := classifyRuntimeTargetBootstrapError(lastErr, output)
	agentStatus, agentSHA256 := runtimeTargetAgentConvergenceProof(output)
	return &RuntimeTargetBootstrapResult{
		Status:      "failed",
		ReasonCode:  reason,
		Message:     secrets.Redact(errorString(lastErr)),
		Output:      output,
		DurationMS:  durationMS,
		Attempts:    attempts,
		AgentStatus: agentStatus,
		AgentSHA256: agentSHA256,
	}, lastErr
}

func (b *SSHRuntimeTargetBootstrapper) executionTimeout(ctx context.Context) time.Duration {
	configuredTimeout := b.timeout
	if b.timeoutDefaulted {
		// Preserve the distinction between the product default and an explicit
		// four-minute operator budget. The execution helper reserves the queue
		// return window only for the default.
		configuredTimeout = 0
	}
	return runtimeTargetBootstrapExecutionTimeout(ctx, configuredTimeout)
}

// runtimeTargetBootstrapExecutionTimeout prevents the default child bootstrap
// deadline from consuming the entire queue observation budget. Explicit
// non-zero timeouts are execution budgets and remain unchanged when the parent
// has no deadline, including intentionally short fast-fail configurations.
// When a parent deadline exists, the caller retains an interval to return from
// bootstrap, collect bounded diagnostics, and hand the terminal result back to
// the queue for persistence.
func runtimeTargetBootstrapExecutionTimeout(ctx context.Context, configured time.Duration) time.Duration {
	postExecutionReserve := runtimeTargetBootstrapDiagnosticsReserve + runtimeTargetBootstrapTerminalPersistenceReserve
	if configured <= 0 {
		configured = defaultRuntimeTargetBootstrapTimeout - postExecutionReserve
	}
	deadline, hasDeadline := ctx.Deadline()
	if !hasDeadline {
		return configured
	}
	remaining := time.Until(deadline)
	if remaining <= postExecutionReserve {
		// The caller is already too close to its own deadline to run a
		// bootstrap. Return promptly so it can record a truthful timeout.
		return time.Nanosecond
	}
	return minDuration(configured, remaining-postExecutionReserve)
}

func runtimeTargetAgentConvergenceProof(output string) (string, string) {
	status := ""
	digest := ""
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) == 0 || fields[0] != "phase=agent_convergence" {
			continue
		}
		for _, field := range fields[1:] {
			key, value, ok := strings.Cut(field, "=")
			if !ok {
				continue
			}
			switch key {
			case "status":
				status = value
			case "sha256":
				if len(value) == sha256.Size*2 {
					digest = value
				}
			}
		}
	}
	return status, digest
}

func runtimeTargetBootstrapReadyMessage(agentStatus string) string {
	switch agentStatus {
	case "updated":
		return "Docker is reachable and the enrolled Agent was updated to the exact control-plane artifact"
	case "current":
		return "Docker is reachable and the enrolled Agent already matches the exact control-plane artifact"
	default:
		return "Docker is installed and reachable"
	}
}

// runtimeTargetBootstrapLogins is the ordered set of logins one bootstrap may
// use on a managed node, all authenticated by the same key.
//
// A managed host changes which login answers over its own lifetime: a fresh
// node only has the provider login, and Cloud host-security disables root as
// part of the hardening it exists to enforce. Both ends of that transition
// have to be reachable, so the channel login and the provider logins are
// candidates in every direction rather than a one-way migration.
func runtimeTargetBootstrapLogins(configured string) []string {
	logins := []string{configured}
	for _, candidate := range []string{guardbootstrap.ExecutionChannelUser, "ubuntu", "root"} {
		if candidate != configured {
			logins = append(logins, candidate)
		}
	}
	return logins
}

// dial returns a session on the first login that authenticates and reports the
// login it used, so the rest of the rollout addresses the node the same way.
func (b *SSHRuntimeTargetBootstrapper) dial(ctx context.Context, target *RuntimeActionTarget, auth []ssh.AuthMethod) (*ssh.Client, string, error) {
	var lastErr error
	for _, login := range runtimeTargetBootstrapLogins(target.User) {
		client, err := b.dialAs(ctx, target, login, auth)
		if err == nil {
			return client, login, nil
		}
		lastErr = err
		// Only a rejected login is worth another identity. Anything else is a
		// property of the node or the network and would repeat unchanged.
		if classifyRuntimeTargetBootstrapError(err, "") != RuntimeTargetBootstrapSSHAuth {
			return nil, "", err
		}
	}
	return nil, "", lastErr
}

// bootstrapHostKeyCallback returns the host-key policy for managed target
// bootstrap: the first contact pins and every later contact must match.
// Without a pin store the handshake is rejected.
func (b *SSHRuntimeTargetBootstrapper) bootstrapHostKeyCallback() ssh.HostKeyCallback {
	if b == nil || b.hostKeys == nil {
		return rejectUnverifiableRuntimeHostKey()
	}
	store := b.hostKeys
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		if remote == nil {
			return runtimeHostKeyUnverifiable(hostname, "no remote address")
		}
		return store.VerifyAndPin(remote.String(), key)
	}
}

func (b *SSHRuntimeTargetBootstrapper) dialAs(ctx context.Context, target *RuntimeActionTarget, login string, auth []ssh.AuthMethod) (*ssh.Client, error) {
	config := &ssh.ClientConfig{
		User:            login,
		Auth:            auth,
		HostKeyCallback: b.bootstrapHostKeyCallback(),
		Timeout:         minDuration(b.timeout, 10*time.Second),
	}
	addr := net.JoinHostPort(target.Host, strconv.Itoa(target.Port))
	var lastErr error
	attempts := 0
	for {
		attempts++
		client, err := b.dialOnce(ctx, addr, config)
		if err == nil {
			return client, nil
		}
		// A context expiration from dialOnce is the observation boundary, not
		// a replacement for the last concrete readiness failure.
		if ctx.Err() == nil || lastErr == nil {
			lastErr = err
		}
		if !isRuntimeTargetSSHReadinessError(err) {
			return nil, fmt.Errorf("ssh bootstrap dial %s@%s failed: %w", login, addr, err)
		}
		if waitErr := waitForRuntimeTargetSSHRetry(ctx, b.dialRetryInterval); waitErr != nil {
			reason := fmt.Sprintf("timeout %s", b.timeout)
			if ctx.Err() != context.DeadlineExceeded {
				reason = "context cancellation"
			}
			if errors.Is(lastErr, waitErr) {
				return nil, fmt.Errorf("ssh bootstrap dial %s@%s was not ready before %s after %d attempts: %w", login, addr, reason, attempts, waitErr)
			}
			return nil, fmt.Errorf("ssh bootstrap dial %s@%s was not ready before %s after %d attempts: %w; last dial: %w", login, addr, reason, attempts, waitErr, lastErr)
		}
	}
}

func (b *SSHRuntimeTargetBootstrapper) dialOnce(ctx context.Context, addr string, config *ssh.ClientConfig) (*ssh.Client, error) {
	type dialResult struct {
		client *ssh.Client
		err    error
	}
	done := make(chan dialResult, 1)
	go func() {
		dialSSH := b.dialSSH
		if dialSSH == nil {
			dialSSH = ssh.Dial
		}
		client, err := dialSSH("tcp", addr, config)
		done <- dialResult{client: client, err: err}
	}()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case result := <-done:
		return result.client, result.err
	}
}

func waitForRuntimeTargetSSHRetry(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		delay = time.Millisecond
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func isRuntimeTargetSSHReadinessError(err error) bool {
	if err == nil {
		return false
	}
	if err == context.DeadlineExceeded || err == context.Canceled {
		return true
	}
	if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
		return true
	}
	msg := strings.ToLower(err.Error())
	for _, needle := range []string{
		"connection refused",
		"connect: cannot assign requested address",
		"connection reset by peer",
		"connection timed out",
		"i/o timeout",
		"no route to host",
		"network is unreachable",
	} {
		if strings.Contains(msg, needle) {
			return true
		}
	}
	return false
}

func classifyRuntimeTargetBootstrapError(err error, output string) string {
	if err == nil {
		return RuntimeTargetBootstrapReady
	}
	if errors.Is(err, context.Canceled) {
		return RuntimeTargetBootstrapCanceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return RuntimeTargetBootstrapTimeout
	}
	var missingExit *ssh.ExitMissingError
	if errors.As(err, &missingExit) {
		return RuntimeTargetBootstrapSessionLost
	}
	text := strings.ToLower(err.Error() + "\n" + output)
	switch {
	case strings.Contains(text, "without exit status or exit signal"):
		return RuntimeTargetBootstrapSessionLost
	case strings.Contains(text, "unable to authenticate") ||
		strings.Contains(text, "permission denied") ||
		strings.Contains(text, "parse ssh"):
		return RuntimeTargetBootstrapSSHAuth
	case isRuntimeTargetSSHReadinessError(err) ||
		strings.Contains(text, "ssh bootstrap dial") ||
		strings.Contains(text, "connection refused") ||
		strings.Contains(text, "no route to host"):
		return RuntimeTargetBootstrapSSHNotReady
	case strings.Contains(text, "agent_convergence"):
		return RuntimeTargetBootstrapAgentFailed
	case strings.Contains(text, "docker") ||
		strings.Contains(text, "apt-get") ||
		strings.Contains(text, "cloud-init") ||
		strings.Contains(text, "get.docker.com"):
		return RuntimeTargetBootstrapDockerFailed
	default:
		return RuntimeTargetBootstrapUnknown
	}
}

func isRuntimeTargetBootstrapRetryable(reason string) bool {
	switch reason {
	case RuntimeTargetBootstrapSessionLost, RuntimeTargetBootstrapSSHNotReady, RuntimeTargetBootstrapTimeout:
		return true
	default:
		return false
	}
}

func appendBootstrapAttemptOutput(existing string, attempt int, output string) string {
	output = strings.TrimSpace(output)
	if output == "" {
		return existing
	}
	prefix := fmt.Sprintf("attempt=%d\n", attempt)
	if existing == "" {
		return prefix + output
	}
	return existing + "\n\n" + prefix + output
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

type runtimeTargetBootstrapOutput struct {
	mu       sync.Mutex
	buffer   bytes.Buffer
	pending  string
	progress func(stackKitCLIProgressEvent)
}

func (w *runtimeTargetBootstrapOutput) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n, err := w.buffer.Write(p)
	w.pending += string(p)
	for {
		line, rest, ok := strings.Cut(w.pending, "\n")
		if !ok {
			break
		}
		w.pending = rest
		if event, parsed := parseRuntimeTargetBootstrapProgressLine(line); parsed && w.progress != nil {
			w.progress(event)
		}
	}
	return n, err
}

func (w *runtimeTargetBootstrapOutput) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buffer.String()
}

func parseRuntimeTargetBootstrapProgressLine(line string) (stackKitCLIProgressEvent, bool) {
	fields := strings.Fields(strings.TrimSpace(line))
	if len(fields) == 0 {
		return stackKitCLIProgressEvent{}, false
	}
	event := stackKitCLIProgressEvent{Attributes: map[string]string{}}
	for _, field := range fields {
		key, value, ok := strings.Cut(field, "=")
		if !ok {
			continue
		}
		switch key {
		case "phase":
			event.Phase = value
		case "status":
			event.Status = value
		default:
			event.Attributes[key] = value
		}
	}
	if event.Phase == "" {
		return stackKitCLIProgressEvent{}, false
	}
	if len(event.Attributes) == 0 {
		event.Attributes = nil
	}
	return event, true
}

func (b *SSHRuntimeTargetBootstrapper) runCommand(ctx context.Context, client *ssh.Client, command string, progress func(stackKitCLIProgressEvent)) (string, error) {
	session, err := client.NewSession()
	if err != nil {
		return "", err
	}
	defer func() { _ = session.Close() }()

	output := &runtimeTargetBootstrapOutput{progress: progress}
	session.Stdout = output
	session.Stderr = output
	type commandResult struct{ err error }
	done := make(chan commandResult, 1)
	go func() {
		done <- commandResult{err: session.Run(command)}
	}()

	select {
	case <-ctx.Done():
		_ = session.Close()
		return output.String(), ctx.Err()
	case result := <-done:
		if result.err != nil {
			return output.String(), fmt.Errorf("bootstrap managed runtime target: %w", result.err)
		}
		return output.String(), nil
	}
}

func runtimeTargetBootstrapScript() string {
	return `set -u
log() { printf '%s\n' "$*"; }
if command -v sudo >/dev/null 2>&1 && [ "$(id -u)" != "0" ]; then SUDO="sudo -n"; else SUDO=""; fi
run_bounded() {
  seconds="$1"
  shift
  if command -v timeout >/dev/null 2>&1; then
    timeout "$seconds" "$@"
  else
    "$@"
  fi
}
docker_ready() {
  command -v docker >/dev/null 2>&1 && $SUDO docker info >/dev/null 2>&1
}
docker_firewall_ready() {
  ! command -v iptables >/dev/null 2>&1 ||
    $SUDO iptables --wait -t filter -S DOCKER-USER >/dev/null 2>&1
}
admit_docker_firewall() {
  docker_firewall_ready && return 0
  log "phase=docker_firewall status=repair"
  $SUDO systemctl restart docker >/dev/null 2>&1 || return 1
  for i in $(seq 1 20); do
    if docker_ready && docker_firewall_ready; then
      log "phase=docker_firewall status=repaired"
      return 0
    fi
    sleep 1
  done
  log "phase=docker_firewall status=failed"
  return 1
}
ufw_active() {
  command -v ufw >/dev/null 2>&1 && $SUDO ufw status 2>/dev/null | grep -q '^Status: active'
}
nftables_quiet() {
  ! $SUDO systemctl is-active --quiet nftables && ! $SUDO systemctl is-enabled --quiet nftables
}
ufw_nftables_ok() {
  if ufw_active; then nftables_quiet; else return 0; fi
}
# Apt install nftables enables the unit, which flushes the live nft ruleset
# including Docker's. Disable it on every bootstrap, not only first-boot, and
# restart Docker so compose Apply can recreate bridge NAT.
admit_ufw_nftables() {
  if ! ufw_active; then
    return 0
  fi
  $SUDO ufw allow 22/tcp >/dev/null 2>&1 || true
  if nftables_quiet; then
    return 0
  fi
  log "phase=nftables status=disable"
  $SUDO systemctl disable --now nftables >/dev/null 2>&1 || true
  if command -v docker >/dev/null 2>&1 && $SUDO systemctl is-active --quiet docker; then
    log "phase=nftables status=docker_restore"
    $SUDO systemctl restart docker >/dev/null 2>&1 || true
    for i in $(seq 1 20); do
      if docker_ready; then
        break
      fi
      sleep 1
    done
  fi
}
# ProtectSystem=strict makes the whole hierarchy read-only. Cloud host-security
# Apply writes sshd/apt drop-ins under /etc and StackKits evidence under
# /opt/stackkit. The pinned compose executor runs docker compose with a
# replaced environment, so the CLI homes under /root/.docker from passwd.
# ProtectHome=read-only turns that into EROFS; ProtectKernelTunables=yes
# does the same for sysctl. Host-security also preserves the execution channel
# under /home/kombify/.ssh. Probe the required writes in the live Guard namespace.
guard_namespace_can_write() {
  pid="$1"
  path="$2"
  [ -n "$pid" ] && [ "$pid" != "0" ] || return 1
  command -v nsenter >/dev/null 2>&1 || return 1
  $SUDO nsenter -t "$pid" -m -- /bin/sh -c "mkdir -p \"$(dirname "$path")\" && : > \"$path\" && rm -f \"$path\""
}
guard_sandbox_ready() {
  pid="$1"
  guard_namespace_can_write "$pid" /etc/ssh/sshd_config.d/.kombify-guard-write-probe &&
    guard_namespace_can_write "$pid" /opt/stackkit/.stackkit/.kombify-guard-write-probe &&
    guard_namespace_can_write "$pid" /root/.docker/.kombify-guard-write-probe &&
    guard_namespace_can_write "$pid" /home/kombify/.ssh/.kombify-guard-write-probe
}
admit_guard_host_writes() {
  unit=/etc/systemd/system/techstack-agent.service
  if ! $SUDO test -f "$unit"; then
    log "phase=guard_sandbox status=not_applicable"
    return 0
  fi
  $SUDO mkdir -p /root/.docker /opt/stackkit/.stackkit /etc/ssh/sshd_config.d
  $SUDO mkdir -p -m 0700 /home/kombify/.ssh
  pid="$($SUDO systemctl show -p MainPID --value techstack-agent.service 2>/dev/null || true)"
  if guard_sandbox_ready "$pid" && ! $SUDO grep -q '^NoNewPrivileges=yes' "$unit"; then
    log "phase=guard_sandbox status=ready"
    return 0
  fi
  log "phase=guard_sandbox status=repair pid=$pid"
  $SUDO sed -i 's/^ProtectSystem=strict$/ProtectSystem=true/' "$unit"
  $SUDO sed -i 's/^NoNewPrivileges=yes$/NoNewPrivileges=no/' "$unit"
  $SUDO sed -i 's/^ProtectKernelTunables=yes$/ProtectKernelTunables=no/' "$unit"
  if ! $SUDO grep -q '^ProtectKernelTunables=' "$unit"; then
    $SUDO sed -i '/^ProtectSystem=/a ProtectKernelTunables=no' "$unit"
  fi
  desired='ReadWritePaths=/app /opt /usr/local/bin /usr/local/libexec /etc /usr /var /run /root/.docker /home/kombify/.ssh'
  if $SUDO grep -q '^ReadWritePaths=' "$unit"; then
    $SUDO sed -i "s|^ReadWritePaths=.*|$desired|" "$unit"
  else
    $SUDO sed -i "/^ProtectSystem=/a $desired" "$unit"
  fi
  desired_ro='ReadOnlyPaths=/etc/techstack -/root/my-homelab'
  if $SUDO grep -q '^ReadOnlyPaths=' "$unit"; then
    $SUDO sed -i "s|^ReadOnlyPaths=.*|$desired_ro|" "$unit"
  fi
  $SUDO systemctl daemon-reload
  $SUDO systemctl restart techstack-agent.service
  for i in $(seq 1 20); do
    if $SUDO systemctl is-active --quiet techstack-agent.service; then
      pid="$($SUDO systemctl show -p MainPID --value techstack-agent.service 2>/dev/null || true)"
      if guard_sandbox_ready "$pid"; then
        log "phase=guard_sandbox status=repaired"
        return 0
      fi
      log "phase=guard_sandbox status=failed reason=write_probe"
      return 1
    fi
    sleep 1
  done
  log "phase=guard_sandbox status=failed"
  return 1
}
converge_enrolled_agent() {
  admit_ufw_nftables
  admit_docker_firewall || return 1
  admit_guard_host_writes || return 1
  enrollment_file="/etc/techstack/agent-enrollment.json"
  if ! $SUDO test -f "$enrollment_file"; then
    log "phase=agent_convergence status=not_applicable reason=enrollment_missing"
    return 0
  fi
  if [ "$($SUDO stat -c '%u:%a' "$enrollment_file" 2>/dev/null || true)" != "0:600" ]; then
    log "phase=agent_convergence status=failed reason=enrollment_permissions_invalid"
    return 1
  fi
  if ! command -v jq >/dev/null 2>&1 || ! command -v curl >/dev/null 2>&1 || ! command -v sha256sum >/dev/null 2>&1; then
    log "phase=agent_convergence status=failed reason=prerequisite_missing"
    return 1
  fi
  agent_token="$($SUDO jq -er '.data.agent_token // .agent_token // empty' "$enrollment_file")" || {
    log "phase=agent_convergence status=failed reason=agent_token_missing"
    return 1
  }
  runtime_agent_id="$($SUDO jq -er '.data.runtime_agent_id // .runtime_agent_id // .data.worker_id // .worker_id // empty' "$enrollment_file")" || {
    log "phase=agent_convergence status=failed reason=runtime_agent_id_missing"
    return 1
  }
  tenant_id="$($SUDO jq -er '.data.tenant_id // .tenant_id // .data.channel_bootstrap.tenant_id // .channel_bootstrap.tenant_id // empty' "$enrollment_file")" || {
    log "phase=agent_convergence status=failed reason=tenant_id_missing"
    return 1
  }
  heartbeat_url="$($SUDO jq -er '.data.heartbeat_url // .heartbeat_url // .data.channel_bootstrap.heartbeat_url // .channel_bootstrap.heartbeat_url // empty' "$enrollment_file")" || {
    log "phase=agent_convergence status=failed reason=heartbeat_url_missing"
    return 1
  }
  case "$heartbeat_url" in
    https://*/api/v1/workers/*/heartbeat) ;;
    *)
      log "phase=agent_convergence status=failed reason=heartbeat_url_invalid"
      return 1
      ;;
  esac
  api_base="${heartbeat_url%%/api/v1/workers/*}"
  case "$(uname -m)" in
    x86_64|amd64) agent_arch="amd64" ;;
    aarch64|arm64) agent_arch="arm64" ;;
    armv7|armv7l) agent_arch="arm" ;;
    *)
      log "phase=agent_convergence status=failed reason=architecture_unsupported"
      return 1
      ;;
  esac
  agent_url="${api_base}/api/v1/agent/binary/linux/${agent_arch}"
  auth_file="$(mktemp "${TMPDIR:-/tmp}/techstack-agent-auth.XXXXXX")"
  header_file="$(mktemp "${TMPDIR:-/tmp}/techstack-agent-headers.XXXXXX")"
  binary_file="$(mktemp "${TMPDIR:-/tmp}/techstack-agent-binary.XXXXXX")"
  chmod 0600 "$auth_file" "$header_file" "$binary_file"
  cleanup_agent_convergence() {
    trap - EXIT HUP INT TERM
    rm -f "$auth_file" "$header_file" "$binary_file"
  }
  trap 'cleanup_agent_convergence' EXIT
  trap 'cleanup_agent_convergence; exit 1' HUP INT TERM
  printf 'header = "Authorization: Bearer %s"\nheader = "X-Kombify-Runtime-Agent-ID: %s"\nheader = "X-Kombify-Tenant-ID: %s"\n' \
    "$agent_token" "$runtime_agent_id" "$tenant_id" > "$auth_file"
  unset agent_token
  log "phase=agent_convergence status=download_begin"
  if ! run_bounded 50 curl --config "$auth_file" --fail-with-body --silent --show-error \
    --proto '=https' --tlsv1.2 --retry 1 --connect-timeout 5 --max-time 45 \
    --dump-header "$header_file" --output "$binary_file" --request POST "$agent_url"; then
    cleanup_agent_convergence
    log "phase=agent_convergence status=failed reason=download_failed"
    return 1
  fi
  expected_sha="$(awk 'tolower($1) == "x-kombify-artifact-sha256:" {gsub("\\r", "", $2); print tolower($2)}' "$header_file" | tail -n 1)"
  case "$expected_sha" in
    *[!0-9a-f]*|'')
      cleanup_agent_convergence
      log "phase=agent_convergence status=failed reason=checksum_missing"
      return 1
      ;;
  esac
  if [ "${#expected_sha}" -ne 64 ] || ! printf '%s  %s\n' "$expected_sha" "$binary_file" | sha256sum -c - >/dev/null 2>&1; then
    cleanup_agent_convergence
    log "phase=agent_convergence status=failed reason=checksum_mismatch"
    return 1
  fi
  current_sha=""
  if [ -f /usr/local/bin/techstack ]; then
    current_sha="$(sha256sum /usr/local/bin/techstack | awk '{print $1}')"
  fi
  if [ "$current_sha" = "$expected_sha" ]; then
    cleanup_agent_convergence
    log "phase=agent_convergence status=current sha256=$expected_sha"
    return 0
  fi
  stage_path="/usr/local/bin/.techstack-agent-${expected_sha}.next"
  if ! $SUDO install -m 0755 "$binary_file" "$stage_path" || ! $SUDO mv -f "$stage_path" /usr/local/bin/techstack; then
    $SUDO rm -f "$stage_path" || true
    cleanup_agent_convergence
    log "phase=agent_convergence status=failed reason=activation_failed"
    return 1
  fi
  cleanup_agent_convergence
  if ! $SUDO systemctl daemon-reload || ! $SUDO systemctl restart techstack-agent.service; then
    log "phase=agent_convergence status=failed reason=restart_failed"
    return 1
  fi
  for i in $(seq 1 30); do
    if $SUDO systemctl is-active --quiet techstack-agent.service; then
      log "phase=agent_convergence status=updated sha256=$expected_sha"
      return 0
    fi
    sleep 1
  done
  log "phase=agent_convergence status=failed reason=service_not_active"
  return 1
}
host_prep_status=/var/lib/kombify/host-prep/v1.status
if [ -f "$host_prep_status" ]; then
  # The bootstrap context is the authority for this wait. Do not introduce a
  # shorter local timeout than the declared first-boot preparation contract;
  # cancellation of the SSH command remains bounded by that context.
  while :; do
    status=$(sed -n 's/^status=//p' "$host_prep_status" | tail -n 1)
    case "$status" in
      ready)
        log "phase=host_prep status=ready"
        break
        ;;
      failed)
        log "phase=host_prep status=failed"
        $SUDO systemctl status kombify-host-prep-v1.service --no-pager -l 2>&1 || true
        exit 1
        ;;
      *)
        log "phase=host_prep status=pending"
        sleep 2
        ;;
    esac
  done
fi
stackkit_ready() {
  docker_ready &&
    docker_firewall_ready &&
    command -v nft >/dev/null 2>&1 &&
    $SUDO systemctl is-enabled --quiet fail2ban &&
    $SUDO systemctl is-enabled --quiet unattended-upgrades &&
    $SUDO sshd -t >/dev/null 2>&1 &&
    ufw_nftables_ok &&
    ` + guardbootstrap.RenderExecutionChannelUserReadyTest("$SUDO") + ` &&
    [ -f /var/lib/kombify/host-prep/v2.status ] &&
    grep -qx 'status=ready' /var/lib/kombify/host-prep/v2.status
}
if [ -f /var/lib/kombify/host-prep/v2.status ]; then
  while :; do
    status=$(sed -n 's/^status=//p' /var/lib/kombify/host-prep/v2.status | tail -n 1)
    case "$status" in
      ready)
        if stackkit_ready; then
          log "phase=host_prep status=ready profile=v2"
          break
        fi
        log "phase=host_prep status=stale profile=v2"
        break
        ;;
      failed)
        # First-boot host-prep-v2 can fail on a missing /run/sshd privilege
        # directory. Treat that as stale so the repair path below can rerun.
        log "phase=host_prep status=repair profile=v2"
        $SUDO systemctl status kombify-host-prep-v2.service --no-pager -l 2>&1 || true
        break
        ;;
      *)
        log "phase=host_prep status=pending profile=v2"
        sleep 2
        ;;
    esac
  done
fi
if ! stackkit_ready; then
  log "phase=host_prep status=install profile=stackkit-ready-v2"
  $SUDO mkdir -p /var/lib/kombify/host-prep /etc/ssh/sshd_config.d /run/sshd
  $SUDO chmod 0755 /run/sshd >/dev/null 2>&1 || true
  printf 'status=pending\n' | $SUDO tee /var/lib/kombify/host-prep/v2.status >/dev/null
  if command -v apt-get >/dev/null 2>&1; then
    log "phase=apt_wait status=begin"
    for i in $(seq 1 30); do
      if ! pgrep -x apt >/dev/null 2>&1 && ! pgrep -x apt-get >/dev/null 2>&1 && ! pgrep -x dpkg >/dev/null 2>&1 && ! pgrep -x unattended-upgr >/dev/null 2>&1; then
        break
      fi
      sleep 2
    done
    log "phase=apt_wait status=done"
    run_bounded 180 $SUDO env DEBIAN_FRONTEND=noninteractive apt-get \
      -o DPkg::Lock::Timeout=120 -o Acquire::Retries=2 \
      -o Acquire::http::Timeout=25 -o Acquire::https::Timeout=25 update || true
    run_bounded 240 $SUDO env DEBIAN_FRONTEND=noninteractive apt-get \
      -o DPkg::Lock::Timeout=120 -o Acquire::Retries=2 \
      -o Acquire::http::Timeout=25 -o Acquire::https::Timeout=25 \
      install -y ca-certificates curl docker.io docker-compose-v2 nftables fail2ban unattended-upgrades openssh-server ||
      run_bounded 240 $SUDO env DEBIAN_FRONTEND=noninteractive apt-get \
        -o DPkg::Lock::Timeout=120 -o Acquire::Retries=2 \
        install -y nftables fail2ban unattended-upgrades openssh-server || true
  fi
  $SUDO systemctl enable --now docker >/dev/null 2>&1 || true
  # Install nftables for the nft binary. Do not start that service:
  # IONOS/Ubuntu cloud-init already owns incoming with ufw (allow 22).
  # Starting the service flushes that ruleset and can silence 22/443/ICMP.
  if command -v ufw >/dev/null 2>&1 && $SUDO ufw status 2>/dev/null | grep -q '^Status: active'; then
    $SUDO ufw allow 22/tcp >/dev/null 2>&1 || true
    $SUDO systemctl disable --now nftables >/dev/null 2>&1 || true
  fi
  $SUDO systemctl enable --now fail2ban >/dev/null 2>&1 || true
  $SUDO systemctl enable --now unattended-upgrades >/dev/null 2>&1 || true
  printf 'PasswordAuthentication no\nKbdInteractiveAuthentication no\n' | $SUDO tee /etc/ssh/sshd_config.d/zz-kombify-host-prep.conf >/dev/null
  $SUDO sshd -t >/dev/null 2>&1 || true
  $SUDO systemctl reload ssh >/dev/null 2>&1 || $SUDO systemctl reload sshd >/dev/null 2>&1 || true
  log "phase=execution_channel status=ensure user=` + guardbootstrap.ExecutionChannelUser + `"
` + guardbootstrap.RenderExecutionChannelUserProvisioning("$SUDO") + `
  if stackkit_ready || { docker_ready && command -v nft >/dev/null 2>&1 && $SUDO systemctl is-enabled --quiet fail2ban && $SUDO systemctl is-enabled --quiet unattended-upgrades; }; then
    printf 'status=ready\n' | $SUDO tee /var/lib/kombify/host-prep/v2.status >/dev/null
    log "phase=host_prep status=ready profile=stackkit-ready-v2"
  else
    printf 'status=failed\n' | $SUDO tee /var/lib/kombify/host-prep/v2.status >/dev/null
    log "phase=host_prep status=failed profile=stackkit-ready-v2"
    exit 1
  fi
fi
if docker_ready; then
  converge_enrolled_agent || exit 1
  log "phase=docker_ready status=already"
  log "docker_ready=already"
  docker --version || true
  exit 0
fi
if command -v cloud-init >/dev/null 2>&1; then
  log "phase=cloud_init status=wait_begin"
  run_bounded 20 cloud-init status --wait >/dev/null 2>&1 || true
  log "phase=cloud_init status=wait_done"
fi
apt_busy() {
  pgrep -x apt >/dev/null 2>&1 ||
  pgrep -x apt-get >/dev/null 2>&1 ||
  pgrep -x dpkg >/dev/null 2>&1 ||
  pgrep -x unattended-upgr >/dev/null 2>&1 ||
  systemctl is-active --quiet apt-daily.service 2>/dev/null ||
  systemctl is-active --quiet apt-daily-upgrade.service 2>/dev/null
}
wait_for_apt() {
  for i in $(seq 1 30); do
    if ! apt_busy; then return 0; fi
    sleep 2
  done
  return 1
}
apt_get() {
  seconds="$1"
  shift
  log "phase=apt_wait status=begin"
  wait_for_apt || true
  log "phase=apt_wait status=done"
  log "phase=apt_get command=$*"
  if run_bounded "$seconds" $SUDO env DEBIAN_FRONTEND=noninteractive apt-get \
    -o DPkg::Lock::Timeout=120 \
    -o Acquire::Retries=2 \
    -o Acquire::http::Timeout=25 \
    -o Acquire::https::Timeout=25 \
    -o Acquire::ForceIPv4=true \
    "$@"; then
    log "phase=apt_get status=done"
    return 0
  fi
  code="$?"
  log "phase=apt_get status=failed code=$code"
  return "$code"
}
if ! command -v apt-get >/dev/null 2>&1; then
  log "apt_get=missing"
else
  log "phase=docker_install method=apt"
  apt_get 180 update || true
  compose_pkg=""
  if apt-cache show docker-compose-v2 >/dev/null 2>&1; then
    compose_pkg="docker-compose-v2"
  elif apt-cache show docker-compose-plugin >/dev/null 2>&1; then
    compose_pkg="docker-compose-plugin"
  fi
  if [ -n "$compose_pkg" ]; then
    log "phase=docker_install compose_package=$compose_pkg"
    apt_get 240 install -y ca-certificates curl docker.io "$compose_pkg" ||
      apt_get 240 install -y ca-certificates curl docker.io || true
  else
    log "phase=docker_install compose_package=none"
    apt_get 240 install -y ca-certificates curl docker.io || true
  fi
fi
if ! command -v docker >/dev/null 2>&1; then
  if ! command -v curl >/dev/null 2>&1; then
    log "curl=missing"
    exit 1
  fi
  log "phase=docker_install method=get_docker"
  run_bounded 180 curl --connect-timeout 10 --max-time 120 -fsSL https://get.docker.com -o /tmp/get-docker.sh
  run_bounded 240 $SUDO sh /tmp/get-docker.sh
fi
if [ "$(id -u)" != "0" ]; then
  $SUDO usermod -aG docker "$(id -un)" || true
fi
$SUDO systemctl enable --now docker >/dev/null 2>&1 || true
log "phase=docker_ready status=wait_begin"
for i in $(seq 1 60); do
  if docker_ready; then
    converge_enrolled_agent || exit 1
    log "phase=docker_ready status=ready"
    log "docker_ready=installed"
    $SUDO docker --version || true
    $SUDO docker compose version || true
    exit 0
  fi
  sleep 2
done
log "phase=docker_status status=failed"
$SUDO systemctl status docker --no-pager -l 2>&1 || true
if ! $SUDO docker info; then
  log "docker_ready=failed"
  exit 1
fi
converge_enrolled_agent || exit 1`
}
