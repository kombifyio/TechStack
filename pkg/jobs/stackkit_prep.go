package jobs

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kombifyio/techstack/internal/stackkitrelease"
	"github.com/kombifyio/techstack/pkg/secrets"
)

const defaultStackKitPrepProgressLimit = 64

var stackKitPrepKnownHostScanner = scanStackKitPrepKnownHost

type StackKitCLIPrepRunner struct {
	Binary       string
	StackKitsDir string
	Timeout      time.Duration
	Progress     func(stackKitCLIProgressEvent)
	release      *stackkitrelease.Release
}

type stackKitCLIProgressEvent struct {
	Time               time.Time         `json:"time"`
	Phase              string            `json:"phase"`
	Status             string            `json:"status"`
	Message            string            `json:"message,omitempty"`
	FailureClass       string            `json:"failure_class,omitempty"`
	FailureClassLegacy string            `json:"failureClass,omitempty"`
	Attributes         map[string]string `json:"attributes,omitempty"`
}

func NewStackKitCLIPrepRunner(stackKitsDir string) *StackKitCLIPrepRunner {
	return &StackKitCLIPrepRunner{
		Binary:       firstNonEmpty(os.Getenv(stackKitCLIEnv), defaultStackKitCLIBinary),
		StackKitsDir: stackKitsDir,
		Timeout:      runtimeActionHTTPTimeout,
	}
}

// NewPinnedStackKitCLIPrepRunner creates the runtime preparation runner from
// the exact published release admitted by stackkitrelease.Cache.
func NewPinnedStackKitCLIPrepRunner(release stackkitrelease.Release) *StackKitCLIPrepRunner {
	return &StackKitCLIPrepRunner{
		Binary:  release.BinaryPath(),
		Timeout: runtimeActionHTTPTimeout,
		release: &release,
	}
}

func (r *StackKitCLIPrepRunner) PrepareStackKitRuntimeTarget(ctx context.Context, req RuntimeActionRequest) (*RuntimeTargetBootstrapResult, error) {
	if r == nil {
		return nil, fmt.Errorf("StackKits CLI prep runner is not configured")
	}
	target := normalizeRuntimeActionTarget(req.RuntimeTarget)
	if !runtimeActionTargetHasSSHCredential(target) {
		return nil, nil
	}
	stackSpecPath := strings.TrimSpace(req.StackSpecPath)
	if stackSpecPath == "" {
		return nil, fmt.Errorf("StackKits CLI prepare requires stack_spec_path")
	}
	stackSpecPath, err := filepath.Abs(filepath.Clean(stackSpecPath))
	if err != nil {
		return nil, fmt.Errorf("resolve stack spec path: %w", err)
	}
	workDir := filepath.Dir(stackSpecPath)
	if _, err := os.Stat(stackSpecPath); err != nil {
		return nil, fmt.Errorf("StackKits CLI prepare stack spec missing: %w", err)
	}
	stackKit := firstNonEmpty(strings.TrimSpace(req.StackKit), DefaultBaseKitRef)
	// prepare executes the same pinned CLI as generate, so it needs the same
	// canonical document. Handing it the persisted v1 handoff failed the
	// rollout one step after generate finally passed:
	//
	//	Managed runtime target bootstrap failed: StackKits CLI prepare failed:
	//	Error: architecturev2 migration_required: StackSpec v1 is readable only
	//	through the migration adapter and cannot enter prepare
	executedSpecPath := stackSpecPath
	if r.release != nil {
		canonical, canonicalErr := canonicalStackSpecFor(stackSpecPath, stackKit, req.StackName)
		if canonicalErr != nil {
			return nil, canonicalErr
		}
		executedSpecPath = canonical.Path
		// The v2 line has no host preparation to run. docs/CLI.md calls prepare
		// "an optional host-conformance step" and the CLI refuses it outright
		// for a canonical document:
		//
		//	Error: prepare: canonical StackSpec v2 has no governed
		//	host-preparation implementation; use external host
		//	admission/conformance and the resolved execution channel
		//
		// Host readiness is Techstack's own concern and is covered by
		// SSHRuntimeTargetBootstrapper; running the CLI here only produced a
		// guaranteed failure, so the step reports that it does not apply
		// instead of inventing a success.
		return &RuntimeTargetBootstrapResult{
			Status:     "ready",
			ReasonCode: RuntimeTargetBootstrapNotApplicable,
			Message:    "StackKits has no governed host preparation for a canonical v2 StackSpec; host conformance is external to the CLI",
		}, nil
	}
	if r.release == nil {
		stackKitsDir, err := cleanRequiredDir(r.StackKitsDir, "StackKits source directory")
		if err != nil {
			return nil, err
		}
		if err := ensureStackKitCLIWorkspace(workDir, stackKitsDir, stackKit); err != nil {
			return nil, err
		}
	}

	timeout := r.Timeout
	if timeout <= 0 || timeout > runtimeActionHTTPTimeout {
		timeout = runtimeActionHTTPTimeout
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	sshFiles, err := stackKitPrepSSHFiles(runCtx, target)
	if err != nil {
		return nil, err
	}
	defer sshFiles.cleanup()

	args := stackKitPrepareArgs(workDir, filepath.Base(executedSpecPath), target, sshFiles.keyPath)

	started := time.Now()
	binary := firstNonEmpty(strings.TrimSpace(r.Binary), defaultStackKitCLIBinary)
	cmd := exec.CommandContext(runCtx, binary, args...) // #nosec G204 -- binary is configured by operator env/image, args are fixed except validated target values.
	cmd.Dir = workDir
	cmd.Env = append(os.Environ(),
		"HOME="+sshFiles.homeDir,
		"STACKKIT_STACKKIT="+stackKit,
		"STACKKIT_TARGET_NODE_ID="+firstNonEmpty(req.StackID, req.StackName),
	)
	cmd.Env = append(cmd.Env, stackKitPrepTechStackEnv(req)...)
	output, events, err := r.runStackKitPrepareCommand(cmd)
	result := stackKitPrepResult(events, started, output)
	if err != nil {
		if runCtx.Err() != nil {
			result.Status = "failed"
			result.ReasonCode = RuntimeTargetBootstrapTimeout
			result.Message = secrets.Redact(runCtx.Err().Error())
			return result, fmt.Errorf("StackKits CLI prepare timed out after %s: %w", timeout, runCtx.Err())
		}
		result.Status = "failed"
		result.ReasonCode = stackKitPrepFailureReason(result.ReasonCode, output)
		result.Message = firstNonEmpty(result.Message, secrets.Redact(tailText(string(output), 1800)))
		return result, fmt.Errorf("StackKits CLI prepare failed: %w: %s", err, tailText(string(output), 4000))
	}
	if result.Status == "" {
		result.Status = "ready"
	}
	if result.ReasonCode == "" {
		result.ReasonCode = RuntimeTargetBootstrapReady
	}
	if result.Message == "" {
		result.Message = "StackKits CLI prepare completed"
	}
	return result, nil
}

func stackKitPrepTechStackEnv(req RuntimeActionRequest) []string {
	enrollment := normalizeTechStackEnrollment(req.TechStackEnrollment)
	if enrollment == nil {
		return nil
	}
	env := []string{
		"TECHSTACK_MANAGED=true",
		"TECHSTACK_SERVER_ID=" + enrollment.ServerID,
		"TECHSTACK_RUNTIME_AGENT_ID=" + enrollment.RuntimeAgentID,
	}
	if enrollment.TenantID != "" {
		env = append(env, "TECHSTACK_TENANT_ID="+enrollment.TenantID)
	}
	if enrollment.OwnerID != "" {
		env = append(env, "TECHSTACK_OWNER_ID="+enrollment.OwnerID)
	}
	if enrollment.StackID != "" {
		env = append(env, "TECHSTACK_STACK_ID="+enrollment.StackID)
	}
	if enrollment.LeaseID != "" {
		env = append(env, "TECHSTACK_LEASE_ID="+enrollment.LeaseID)
	}
	if enrollment.ServerURL != "" {
		env = append(env, "TECHSTACK_SERVER_URL="+enrollment.ServerURL)
	}
	if enrollment.AgentToken != "" {
		env = append(env, "TECHSTACK_AGENT_TOKEN="+enrollment.AgentToken)
	}
	if enrollment.HeartbeatURL != "" {
		env = append(env, "TECHSTACK_HEARTBEAT_URL="+enrollment.HeartbeatURL)
	}
	if enrollment.InventoryURL != "" {
		env = append(env, "TECHSTACK_INVENTORY_URL="+enrollment.InventoryURL)
	}
	if bootstrap := stackKitPrepChannelBootstrapJSON(enrollment); bootstrap != "" {
		env = append(env, "TECHSTACK_CHANNEL_BOOTSTRAP="+bootstrap)
	}
	return env
}

func stackKitPrepChannelBootstrapJSON(enrollment *TechStackEnrollment) string {
	if enrollment == nil || len(enrollment.ChannelBootstrap) == 0 {
		return ""
	}
	data, err := json.Marshal(enrollment.ChannelBootstrap)
	if err != nil {
		return ""
	}
	return string(data)
}

func (r *StackKitCLIPrepRunner) runStackKitPrepareCommand(cmd *exec.Cmd) ([]byte, []stackKitCLIProgressEvent, error) {
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, nil, err
	}

	var outputMu sync.Mutex
	var output bytes.Buffer
	events := []stackKitCLIProgressEvent{}
	var eventsMu sync.Mutex
	appendOutput := func(line string) {
		outputMu.Lock()
		defer outputMu.Unlock()
		output.WriteString(line)
		output.WriteByte('\n')
	}
	appendEvent := func(event stackKitCLIProgressEvent) {
		eventsMu.Lock()
		if len(events) >= defaultStackKitPrepProgressLimit {
			events = events[1:]
		}
		events = append(events, event)
		eventsMu.Unlock()
		if r.Progress != nil {
			r.Progress(event)
		}
	}

	if err := cmd.Start(); err != nil {
		return nil, nil, err
	}

	var wg sync.WaitGroup
	scan := func(reader io.Reader) {
		defer wg.Done()
		scanner := bufio.NewScanner(reader)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for scanner.Scan() {
			line := scanner.Text()
			appendOutput(line)
			if event, ok := parseStackKitCLIProgressLine(line); ok {
				appendEvent(event)
			}
		}
		if err := scanner.Err(); err != nil {
			appendOutput("stackkit prepare output scan error: " + secrets.Redact(err.Error()))
		}
	}
	wg.Add(2)
	go scan(stdout)
	go scan(stderr)

	waitErr := cmd.Wait()
	wg.Wait()

	outputMu.Lock()
	outputBytes := append([]byte(nil), output.Bytes()...)
	outputMu.Unlock()
	eventsMu.Lock()
	eventCopy := append([]stackKitCLIProgressEvent(nil), events...)
	eventsMu.Unlock()
	return outputBytes, eventCopy, waitErr
}

type stackKitPrepSSHFileSet struct {
	homeDir string
	keyPath string
	cleanup func()
}

func stackKitPrepSSHFiles(ctx context.Context, target *RuntimeActionTarget) (*stackKitPrepSSHFileSet, error) {
	dir, err := os.MkdirTemp("", "techstack-stackkit-prepare-*")
	if err != nil {
		return nil, err
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	sshDir := filepath.Join(dir, ".ssh")
	if err := os.MkdirAll(sshDir, 0700); err != nil {
		cleanup()
		return nil, err
	}

	keyPath := strings.TrimSpace(target.KeyPath)
	if keyPath == "" {
		key := firstNonEmpty(target.ClientPrivateKey, target.PrivateKey)
		if strings.TrimSpace(key) == "" {
			cleanup()
			return nil, fmt.Errorf("StackKits CLI prepare requires an SSH private key")
		}
		keyPath = filepath.Join(sshDir, "id_stackkit_prepare")
		if err := os.WriteFile(keyPath, []byte(key), 0600); err != nil {
			cleanup()
			return nil, err
		}
	}

	knownHosts, err := stackKitPrepKnownHostScanner(ctx, target)
	if err != nil {
		cleanup()
		return nil, err
	}
	if strings.TrimSpace(knownHosts) == "" {
		cleanup()
		return nil, fmt.Errorf("ssh-keyscan returned no host keys for %s", target.Host)
	}
	if err := os.WriteFile(filepath.Join(sshDir, "known_hosts"), []byte(knownHosts), 0644); err != nil {
		cleanup()
		return nil, err
	}

	return &stackKitPrepSSHFileSet{homeDir: dir, keyPath: keyPath, cleanup: cleanup}, nil
}

func scanStackKitPrepKnownHost(ctx context.Context, target *RuntimeActionTarget) (string, error) {
	host := strings.TrimSpace(target.Host)
	if host == "" {
		return "", fmt.Errorf("StackKits CLI prepare requires an SSH host")
	}
	port := target.Port
	if port <= 0 {
		port = 22
	}
	scanCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(scanCtx, "ssh-keyscan", "-T", "10", "-p", strconv.Itoa(port), host)
	output, err := cmd.CombinedOutput()
	if scanCtx.Err() != nil {
		return "", fmt.Errorf("ssh-keyscan timed out for %s:%d: %w", host, port, scanCtx.Err())
	}
	if err != nil {
		return "", fmt.Errorf("ssh-keyscan failed for %s:%d: %w: %s", host, port, err, secrets.Redact(tailText(string(output), 1200)))
	}
	return string(output), nil
}

func parseStackKitCLIProgress(output []byte) []stackKitCLIProgressEvent {
	events := []stackKitCLIProgressEvent{}
	scanner := bufio.NewScanner(strings.NewReader(string(output)))
	for scanner.Scan() {
		event, ok := parseStackKitCLIProgressLine(scanner.Text())
		if !ok {
			continue
		}
		if len(events) >= defaultStackKitPrepProgressLimit {
			events = events[1:]
		}
		events = append(events, event)
	}
	return events
}

func parseStackKitCLIProgressLine(line string) (stackKitCLIProgressEvent, bool) {
	line = strings.TrimSpace(line)
	if line == "" || !strings.HasPrefix(line, "{") {
		return stackKitCLIProgressEvent{}, false
	}
	var event stackKitCLIProgressEvent
	if err := json.Unmarshal([]byte(line), &event); err != nil {
		return stackKitCLIProgressEvent{}, false
	}
	if strings.TrimSpace(event.Phase) == "" {
		return stackKitCLIProgressEvent{}, false
	}
	if strings.TrimSpace(event.FailureClass) == "" {
		event.FailureClass = event.FailureClassLegacy
	}
	event.FailureClassLegacy = ""
	return redactStackKitCLIProgressEvent(event), true
}

func redactStackKitCLIProgressEvent(event stackKitCLIProgressEvent) stackKitCLIProgressEvent {
	event.Message = secrets.Redact(event.Message)
	if len(event.Attributes) > 0 {
		redacted := make(map[string]string, len(event.Attributes))
		for key, value := range event.Attributes {
			redacted[key] = secrets.Redact(value)
		}
		event.Attributes = redacted
	}
	return event
}

func stackKitPrepResult(events []stackKitCLIProgressEvent, started time.Time, output []byte) *RuntimeTargetBootstrapResult {
	result := &RuntimeTargetBootstrapResult{
		Status:     "ready",
		ReasonCode: RuntimeTargetBootstrapReady,
		Message:    "StackKits CLI prepare completed",
		Output:     truncateRuntimeDiagnosticsOutput(secrets.Redact(string(output)), defaultRuntimeTargetBootstrapMaxLog),
		Events:     append([]stackKitCLIProgressEvent(nil), events...),
		DurationMS: time.Since(started).Milliseconds(),
		Attempts:   1,
	}
	for _, event := range events {
		if strings.EqualFold(event.Status, "failed") {
			result.Status = "failed"
			result.ReasonCode = stackKitPrepReasonFromEvent(event)
			result.Message = firstNonEmpty(event.Message, "StackKits CLI prepare failed")
		}
	}
	return result
}

func stackKitPrepReasonFromEvent(event stackKitCLIProgressEvent) string {
	switch strings.TrimSpace(event.FailureClass) {
	case "apt_wait_timeout", "apt_lock_timeout", "apt_process_timeout", "unattended_upgrade_timeout", "cloud_init_timeout":
		return RuntimeTargetBootstrapTimeout
	case "docker_install_failed", "docker_daemon_failed":
		return RuntimeTargetBootstrapDockerFailed
	case "target_inspect_failed":
		return RuntimeTargetBootstrapSSHNotReady
	case "opentofu_prepare_failed", "terramate_prepare_failed":
		return RuntimeTargetBootstrapUnknown
	}
	return stackKitPrepReasonFromOutput([]byte(event.Message))
}

func stackKitPrepReasonFromOutput(output []byte) string {
	text := strings.ToLower(string(output))
	switch {
	case strings.Contains(text, "apt_wait"),
		strings.Contains(text, "apt_lock_timeout"),
		strings.Contains(text, "apt_process_timeout"),
		strings.Contains(text, "unattended_upgrade_timeout"),
		strings.Contains(text, "cloud_init_timeout"),
		strings.Contains(text, "timed out waiting for apt"),
		strings.Contains(text, "context deadline exceeded"):
		return RuntimeTargetBootstrapTimeout
	case strings.Contains(text, "docker"):
		return RuntimeTargetBootstrapDockerFailed
	case strings.Contains(text, "ssh"):
		return RuntimeTargetBootstrapSSHNotReady
	default:
		return RuntimeTargetBootstrapUnknown
	}
}

func stackKitPrepFailureReason(current string, output []byte) string {
	if current != "" && current != RuntimeTargetBootstrapReady {
		return current
	}
	return stackKitPrepReasonFromOutput(output)
}

func stackKitPrepareArgs(workDir, stackSpecFile string, target *RuntimeActionTarget, keyPath string) []string {
	port := target.Port
	if port <= 0 {
		port = 22
	}
	args := []string{
		"--quiet",
		"--progress-jsonl", "-",
		"--chdir", workDir,
		"--spec", stackSpecFile,
		"prepare",
		"--host", target.Host,
		"--user", target.User,
		"--non-interactive",
		"--port", strconv.Itoa(port),
	}
	if strings.TrimSpace(keyPath) != "" {
		args = append(args, "--key", keyPath)
	}
	return args
}
