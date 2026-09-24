package jobs

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/kombifyio/techstack/pkg/secrets"
	"golang.org/x/crypto/ssh"
)

const (
	defaultRuntimeDiagnosticsTimeout        = 25 * time.Second
	defaultRuntimeDiagnosticsCommandTimeout = 5 * time.Second
	defaultRuntimeDiagnosticsMaxOutputBytes = 12 * 1024
)

type SSHRuntimeDiagnosticsCollectorConfig struct {
	Timeout        time.Duration
	CommandTimeout time.Duration
	MaxOutputBytes int
	Now            func() time.Time
	// HostKeys verifies the target against an existing pin without writing
	// trust state. Diagnostics never pin. Without a store every handshake is
	// rejected: an unverifiable host never receives target credentials.
	HostKeys *RuntimeHostKeyStore
}

type SSHRuntimeDiagnosticsCollector struct {
	timeout        time.Duration
	commandTimeout time.Duration
	maxOutputBytes int
	now            func() time.Time
	hostKeys       *RuntimeHostKeyStore
}

func NewSSHRuntimeDiagnosticsCollector(cfg SSHRuntimeDiagnosticsCollectorConfig) *SSHRuntimeDiagnosticsCollector {
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = defaultRuntimeDiagnosticsTimeout
	}
	commandTimeout := cfg.CommandTimeout
	if commandTimeout <= 0 {
		commandTimeout = defaultRuntimeDiagnosticsCommandTimeout
	}
	maxOutputBytes := cfg.MaxOutputBytes
	if maxOutputBytes <= 0 {
		maxOutputBytes = defaultRuntimeDiagnosticsMaxOutputBytes
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	return &SSHRuntimeDiagnosticsCollector{
		timeout:        timeout,
		commandTimeout: commandTimeout,
		maxOutputBytes: maxOutputBytes,
		now:            now,
		hostKeys:       cfg.HostKeys,
	}
}

func (c *SSHRuntimeDiagnosticsCollector) CollectRuntimeDiagnostics(ctx context.Context, req RuntimeDiagnosticsRequest) (*RuntimeDiagnosticsBundle, error) {
	if c == nil {
		return nil, nil
	}
	started := c.now().UTC()
	bundle := &RuntimeDiagnosticsBundle{
		Status:    "skipped",
		Reason:    strings.TrimSpace(req.Reason),
		Action:    strings.TrimSpace(req.Action),
		Binding:   runtimeDiagnosticsBinding(req),
		Target:    runtimeDiagnosticsTargetMap(req.RuntimeTarget),
		Endpoint:  runtimeDiagnosticsEndpointMap(req.ActionEndpoint),
		StartedAt: started,
	}
	defer func() {
		bundle.CompletedAt = c.now().UTC()
		bundle.DurationMS = maxInt64(0, bundle.CompletedAt.Sub(started).Milliseconds())
	}()

	target := normalizeRuntimeActionTarget(req.RuntimeTarget)
	if target == nil {
		bundle.Error = "runtime target is missing"
		return bundle, nil
	}
	authMethods, authErr := runtimeDiagnosticsSSHAuthMethods(target)
	if authErr != nil {
		bundle.Error = authErr.Error()
		return bundle, nil
	}
	if len(authMethods) == 0 {
		bundle.Error = "runtime target has no SSH credential"
		return bundle, nil
	}

	collectorCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	client, err := c.dial(collectorCtx, target, authMethods)
	if err != nil {
		bundle.Status = "failed"
		bundle.Error = secrets.Redact(err.Error())
		return bundle, nil
	}
	defer func() { _ = client.Close() }()

	bundle.Status = "collected"
	commands := runtimeDiagnosticsCommands(req.Action)
	remainingOutputBytes := c.maxOutputBytes
	for index, command := range commands {
		if collectorCtx.Err() != nil {
			bundle.Commands = append(bundle.Commands, RuntimeDiagnosticsCommand{
				Name:    command.name,
				Command: command.command,
				Error:   collectorCtx.Err().Error(),
			})
			break
		}
		outputLimit := remainingOutputBytes / max(1, len(commands)-index)
		entry := c.runCommand(collectorCtx, client, command.name, command.command, outputLimit)
		remainingOutputBytes = max(0, remainingOutputBytes-len(entry.Output))
		bundle.Commands = append(bundle.Commands, entry)
	}
	return bundle, nil
}

func (c *SSHRuntimeDiagnosticsCollector) dial(ctx context.Context, target *RuntimeActionTarget, auth []ssh.AuthMethod) (*ssh.Client, error) {
	config := &ssh.ClientConfig{
		User:            target.User,
		Auth:            auth,
		HostKeyCallback: c.diagnosticsHostKeyCallback(),
		Timeout:         minDuration(c.timeout, 10*time.Second),
	}
	addr := net.JoinHostPort(target.Host, strconv.Itoa(target.Port))
	type dialResult struct {
		client *ssh.Client
		err    error
	}
	done := make(chan dialResult, 1)
	go func() {
		client, err := ssh.Dial("tcp", addr, config)
		done <- dialResult{client: client, err: err}
	}()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case result := <-done:
		if result.err != nil {
			return nil, fmt.Errorf("ssh diagnostics dial %s@%s failed: %w", target.User, addr, result.err)
		}
		return result.client, nil
	}
}

func (c *SSHRuntimeDiagnosticsCollector) runCommand(ctx context.Context, client *ssh.Client, name, command string, outputLimit int) RuntimeDiagnosticsCommand {
	started := c.now()
	entry := RuntimeDiagnosticsCommand{Name: name, Command: command, ExitStatus: 0}
	session, err := client.NewSession()
	if err != nil {
		entry.ExitStatus = -1
		entry.Error = secrets.Redact(err.Error())
		entry.DurationMS = maxInt64(0, c.now().Sub(started).Milliseconds())
		return entry
	}
	defer func() { _ = session.Close() }()

	commandCtx, cancel := context.WithTimeout(ctx, c.commandTimeout)
	defer cancel()

	type commandResult struct {
		output []byte
		err    error
	}
	done := make(chan commandResult, 1)
	go func() {
		output, runErr := session.CombinedOutput(command)
		done <- commandResult{output: output, err: runErr}
	}()

	select {
	case <-commandCtx.Done():
		_ = session.Close()
		entry.ExitStatus = -1
		entry.Error = commandCtx.Err().Error()
	case result := <-done:
		entry.Output = truncateRuntimeDiagnosticsOutput(secrets.Redact(string(result.output)), outputLimit)
		if result.err != nil {
			entry.ExitStatus = runtimeDiagnosticsExitStatus(result.err)
			entry.Error = secrets.Redact(result.err.Error())
		}
	}
	entry.DurationMS = maxInt64(0, c.now().Sub(started).Milliseconds())
	return entry
}

type runtimeDiagnosticsCommandSpec struct {
	name    string
	command string
}

func runtimeDiagnosticsCommands(action string) []runtimeDiagnosticsCommandSpec {
	switch strings.TrimSpace(action) {
	case "target_bootstrap", "managed_runtime_enrollment":
		return []runtimeDiagnosticsCommandSpec{
			{name: "cloud_init_status", command: "cloud-init status --long 2>&1 || true"},
			{name: "cloud_init_digest", command: "if [ -f /var/lib/cloud/instance/user-data.txt ]; then printf 'sha256:'; sha256sum /var/lib/cloud/instance/user-data.txt | awk '{print $1}'; else printf 'unavailable\\n'; fi"},
			{name: "cloud_final_journal", command: "journalctl -u cloud-final.service --no-pager -n 120 -o short-iso 2>&1 || true"},
			{name: "bootstrap_log", command: "tail -n 160 /var/log/kombify-techstack-bootstrap.log 2>&1 || true"},
			{name: "host_security", command: "printf 'ufw='; ufw status 2>&1 | head -n 20 || true; printf 'sshd_config='; if sshd -t >/dev/null 2>&1; then echo valid; else echo invalid; fi; systemctl is-active ssh sshd 2>&1 || true"},
			{name: "techstack_agent", command: "systemctl show techstack-agent.service --no-pager --property=LoadState,ActiveState,SubState,Result,ExecMainCode,ExecMainStatus 2>&1 || true; journalctl -u techstack-agent.service --no-pager -n 120 -o short-iso 2>&1 || true"},
		}
	}
	return []runtimeDiagnosticsCommandSpec{
		{name: "system", command: "set -o pipefail 2>/dev/null; uname -a; uptime; cat /etc/os-release 2>/dev/null | head -n 8"},
		{name: "docker_status", command: "systemctl is-active docker 2>&1 || service docker status 2>&1 || true"},
		{name: "docker_ps", command: "docker ps -a --format '{{.ID}}\t{{.Names}}\t{{.State}}\t{{.Status}}\t{{.Ports}}' 2>&1 | head -n 120"},
		{name: "docker_compose_ls", command: "docker compose ls --format json 2>&1 || docker-compose ls 2>&1 || true"},
		{name: "listening_ports", command: "ss -ltnp 2>/dev/null | grep -E '(:80|:443|:8000|:8082)' || true"},
		{name: "coolify_health", command: "curl -fsS --max-time 5 http://127.0.0.1:8000/api/v1/health 2>&1 || true"},
		{name: "stackkits_runtime_health", command: "curl -fsS --max-time 5 http://127.0.0.1:8082/health 2>&1 || curl -fsS --max-time 5 http://127.0.0.1:8082/api/v1/health 2>&1 || true"},
		{name: "coolify_logs", command: "docker logs --tail 120 coolify 2>&1 || true"},
		{name: "stackkits_router_logs", command: runtimeCoreLogsCommand("router", "coolify-proxy")},
		{name: "stackkit_hub_logs", command: runtimeCoreLogsCommand("hub", "stackkit-hub")},
		{name: "uptime_kuma_logs", command: "docker logs --tail 80 uptime-kuma 2>&1 || true"},
		{name: "docker_journal", command: "journalctl -u docker --no-pager -n 160 2>&1 || true"},
	}
}

// Select only the core projects on the bound SSH target. Compose generates
// container names; service labels survive naming changes and stopped services.
// Both arguments are fixed internal constants, never request values.
func runtimeCoreLogsCommand(service, legacyContainer string) string {
	return fmt.Sprintf(`ids=$(for project in stackkit-basement-core stackkit-cloud-core stackkit-cloud-core-standalone; do
docker ps -a --filter "label=com.docker.compose.project=$project" --filter 'label=com.docker.compose.service=%[1]s' --format '{{.ID}}' || exit 1
done) || exit 1
if [ -n "$ids" ]; then
printf '%%s\n' "$ids" | while IFS= read -r id; do
printf 'container=%%s\n' "$id"
docker logs --tail 120 "$id" 2>&1 || true
done
else
docker logs --tail 120 %[2]s 2>&1 || true
fi`, service, legacyContainer)
}

func runtimeDiagnosticsBinding(req RuntimeDiagnosticsRequest) RuntimeDiagnosticsBinding {
	return RuntimeDiagnosticsBinding{
		JobID:          strings.TrimSpace(req.JobID),
		StackID:        strings.TrimSpace(req.StackID),
		TenantID:       strings.TrimSpace(req.TenantID),
		LeaseID:        strings.TrimSpace(req.LeaseID),
		OperationID:    strings.TrimSpace(req.OperationID),
		ServerID:       strings.TrimSpace(req.ServerID),
		RuntimeAgentID: strings.TrimSpace(req.RuntimeAgentID),
		Provider:       strings.TrimSpace(req.Provider),
	}
}

func runtimeDiagnosticsSSHAuthMethods(target *RuntimeActionTarget) ([]ssh.AuthMethod, error) {
	methods := []ssh.AuthMethod{}
	for _, raw := range []string{target.ClientPrivateKey, target.PrivateKey, target.ProviderPrivateKey} {
		if strings.TrimSpace(raw) == "" {
			continue
		}
		signer, err := ssh.ParsePrivateKey([]byte(raw))
		if err != nil {
			return nil, fmt.Errorf("parse SSH private key for diagnostics: %w", err)
		}
		methods = append(methods, ssh.PublicKeys(signer))
	}
	if len(methods) == 0 && strings.TrimSpace(target.Password) == "" && strings.TrimSpace(target.KeyPath) != "" {
		keyBytes, err := os.ReadFile(target.KeyPath)
		if err != nil {
			return nil, fmt.Errorf("read SSH key for diagnostics: %w", err)
		}
		signer, err := ssh.ParsePrivateKey(keyBytes)
		if err != nil {
			return nil, fmt.Errorf("parse SSH key file for diagnostics: %w", err)
		}
		methods = append(methods, ssh.PublicKeys(signer))
	}
	if strings.TrimSpace(target.Password) != "" {
		methods = append(methods, ssh.Password(target.Password))
	}
	return methods, nil
}

// diagnosticsHostKeyCallback verifies against an existing pin without writing
// one: a failure investigation must never change trust state. Without a store
// the handshake is rejected rather than trusting an arbitrary host key.
func (c *SSHRuntimeDiagnosticsCollector) diagnosticsHostKeyCallback() ssh.HostKeyCallback {
	if c == nil || c.hostKeys == nil {
		return rejectUnverifiableRuntimeHostKey()
	}
	store := c.hostKeys
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		if remote == nil {
			return runtimeHostKeyUnverifiable(hostname, "no remote address")
		}
		return store.VerifyReadOnly(remote.String(), key)
	}
}

// rejectUnverifiableRuntimeHostKey is the host-key policy when no pin store is
// wired. Production wiring always supplies a store; any other caller must not
// hand credentials to an unverified host.
func rejectUnverifiableRuntimeHostKey() ssh.HostKeyCallback {
	return func(hostname string, _ net.Addr, _ ssh.PublicKey) error {
		return runtimeHostKeyUnverifiable(hostname, "no host key store is configured")
	}
}

func runtimeDiagnosticsTargetMap(target *RuntimeActionTarget) map[string]interface{} {
	target = normalizeRuntimeActionTarget(target)
	if target == nil {
		return map[string]interface{}{}
	}
	return map[string]interface{}{
		"host":         target.Host,
		"public_ip":    target.PublicIP,
		"private_ip":   target.PrivateIP,
		"user":         target.User,
		"port":         target.Port,
		"docker_host":  target.DockerHost,
		"has_key":      firstNonEmpty(target.ClientPrivateKey, target.PrivateKey, target.KeyPath) != "",
		"has_password": target.Password != "",
	}
}

func runtimeDiagnosticsEndpointMap(endpoint RuntimeActionDescriptor) map[string]interface{} {
	out := map[string]interface{}{}
	if strings.TrimSpace(endpoint.Action) != "" {
		out["action"] = strings.TrimSpace(endpoint.Action)
	}
	if strings.TrimSpace(endpoint.Target) != "" {
		out["target"] = strings.TrimSpace(endpoint.Target)
	}
	if strings.TrimSpace(endpoint.BaseURL) != "" {
		out["base_url"] = strings.TrimSpace(endpoint.BaseURL)
	}
	if strings.TrimSpace(endpoint.Path) != "" {
		out["path"] = strings.TrimSpace(endpoint.Path)
	}
	return out
}

func runtimeDiagnosticsExitStatus(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *ssh.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitStatus()
	}
	return -1
}

func truncateRuntimeDiagnosticsOutput(output string, maxBytes int) string {
	if maxBytes == 0 {
		return ""
	}
	if maxBytes < 0 || len(output) <= maxBytes {
		return output
	}
	const marker = "\n[truncated]"
	if maxBytes <= len(marker) {
		return output[:maxBytes]
	}
	return output[:maxBytes-len(marker)] + marker
}

func runtimeDiagnosticsBundleMap(bundle *RuntimeDiagnosticsBundle) map[string]interface{} {
	if bundle == nil {
		return nil
	}
	commands := make([]interface{}, 0, len(bundle.Commands))
	for _, command := range bundle.Commands {
		commands = append(commands, map[string]interface{}{
			"name":        command.Name,
			"command":     command.Command,
			"exit_status": command.ExitStatus,
			// Collectors normally redact before returning, but the durable job
			// receipt is the final trust boundary. Keep a malformed or future
			// collector from persisting credentials when a bootstrap times out.
			"output":      secrets.Redact(command.Output),
			"error":       secrets.Redact(command.Error),
			"duration_ms": command.DurationMS,
		})
	}
	return map[string]interface{}{
		"status":       bundle.Status,
		"reason":       bundle.Reason,
		"action":       bundle.Action,
		"binding":      runtimeDiagnosticsBindingMap(bundle.Binding),
		"target":       bundle.Target,
		"endpoint":     bundle.Endpoint,
		"commands":     commands,
		"error":        secrets.Redact(bundle.Error),
		"started_at":   bundle.StartedAt.Format(time.RFC3339Nano),
		"completed_at": bundle.CompletedAt.Format(time.RFC3339Nano),
		"duration_ms":  bundle.DurationMS,
	}
}

func runtimeDiagnosticsBindingMap(binding RuntimeDiagnosticsBinding) map[string]interface{} {
	values := map[string]string{
		"job_id": binding.JobID, "stack_id": binding.StackID, "tenant_id": binding.TenantID,
		"lease_id": binding.LeaseID, "operation_id": binding.OperationID, "server_id": binding.ServerID,
		"runtime_agent_id": binding.RuntimeAgentID, "provider": binding.Provider,
	}
	out := map[string]interface{}{}
	for key, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			out[key] = value
		}
	}
	return out
}

func minDuration(a, b time.Duration) time.Duration {
	if a <= 0 {
		return b
	}
	if b <= 0 {
		return a
	}
	if a < b {
		return a
	}
	return b
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
