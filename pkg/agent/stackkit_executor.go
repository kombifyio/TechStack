package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/kombifyio/techstack/internal/stackkitrelease"
	"github.com/kombifyio/techstack/pkg/api/agentpb"
	"github.com/kombifyio/techstack/pkg/secrets"
	"github.com/kombifyio/techstack/pkg/stackkitcommand"
)

const (
	StackKitAgentCapability         = "stackkit"
	stackKitReleasePinEnv           = "TECHSTACK_STACKKIT_RELEASE_PIN"
	stackKitReleaseCacheEnv         = "TECHSTACK_STACKKIT_RELEASE_CACHE"
	stackKitCommandResultVersion    = "stackkit.command-result/v1"
	stackKitRolloutEventVersion     = "stackkit.rollout-event/v1"
	defaultStackKitCommandTimeout   = 30 * time.Minute
	maxStackKitCommandOutputBytes   = 16 << 20
	maxStackKitCommandEventBytes    = 16 << 20
	maxStackKitCommandEventLine     = 1 << 20
	maxStackKitCommandEventCount    = 10_000
	maxStackKitInventoryBytes       = 2 << 20
	stackKitOutputTruncationMessage = "\n<output truncated by Techstack agent>"
)

var stackKitTargetReleasePattern = regexp.MustCompile(
	`^(latest|v[0-9]+\.[0-9]+\.[0-9]+(?:-(?:beta|edge)\.[0-9]+)?|channel:(?:stable|beta|edge))$`,
)

var stackKitExpectedSpecHashPattern = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)
var stackKitServiceKeyPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// StackKitExecutor is the agent-side adapter for the typed StackKitCommand
// seam. It resolves the local immutable release pin on every operation and
// never accepts an executable, argv, environment, or shell fragment from Core.
type StackKitExecutor struct {
	pinPath   string
	cacheRoot string
	mu        sync.RWMutex
}

// NewStackKitExecutorFromEnv creates a lazy executor. Missing configuration is
// reported when a command arrives, so agents that do not advertise the
// "stackkit" command class can still start.
func NewStackKitExecutorFromEnv() *StackKitExecutor {
	pinPath := strings.TrimSpace(os.Getenv(stackKitReleasePinEnv))
	cacheRoot := strings.TrimSpace(os.Getenv(stackKitReleaseCacheEnv))
	if runtime.GOOS == "linux" {
		if pinPath == "" {
			pinPath = "/app/.stackkit/stackkits-release-pin.json"
		}
		if cacheRoot == "" {
			cacheRoot = "/app/.stackkit/releases"
		}
	}
	return &StackKitExecutor{
		pinPath:   pinPath,
		cacheRoot: cacheRoot,
	}
}

func (executor *StackKitExecutor) Available() bool {
	return executor != nil && executor.pinPath != "" && executor.cacheRoot != ""
}

func (executor *StackKitExecutor) Validate() error {
	executor.mu.RLock()
	defer executor.mu.RUnlock()
	return executor.validate()
}

func (executor *StackKitExecutor) validate() error {
	if !executor.Available() {
		return fmt.Errorf("StackKits runtime is not configured")
	}
	_, err := (stackkitrelease.Cache{Root: executor.cacheRoot}).ResolvePin(executor.pinPath)
	return err
}

// Execute validates one typed command, revalidates its exact published
// release, executes the fixed operation, and returns bounded/redacted
// command-result and rollout-event data.
func (executor *StackKitExecutor) Execute(ctx context.Context, command *agentpb.StackKitCommand) *agentpb.StackKitResult {
	return executor.execute(ctx, command, nil)
}

// ExecuteStreaming executes the same closed typed command while forwarding
// redacted progress and stderr records as they are produced.
func (executor *StackKitExecutor) ExecuteStreaming(ctx context.Context, command *agentpb.StackKitCommand, sink func(*agentpb.LogEntry)) *agentpb.StackKitResult {
	return executor.execute(ctx, command, sink)
}

func (executor *StackKitExecutor) execute(ctx context.Context, command *agentpb.StackKitCommand, sink func(*agentpb.LogEntry)) *agentpb.StackKitResult {
	executor.mu.RLock()
	defer executor.mu.RUnlock()
	started := time.Now().UTC()
	result := &agentpb.StackKitResult{
		CommandResultSchemaVersion: stackKitCommandResultVersion,
		EventsSchemaVersion:        stackKitRolloutEventVersion,
		StartedAtUnix:              started.Unix(),
	}
	if command != nil {
		result.CommandId = command.CommandId
		result.Release = cloneStackKitReleasePin(command.Release)
	}

	release, args, workDir, eventPath, err := executor.prepare(command)
	if eventPath != "" {
		defer os.Remove(eventPath)
	}
	if err != nil {
		return finishStackKitResult(result, command, nil, nil, err, started)
	}

	timeout := defaultStackKitCommandTimeout
	if command.TimeoutSeconds > 0 {
		timeout = time.Duration(command.TimeoutSeconds) * time.Second
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	stdout := newBoundedStackKitBuffer(maxStackKitCommandOutputBytes)
	stderr := newBoundedStackKitBuffer(maxStackKitCommandOutputBytes)
	stderrLogs := newStackKitLineEmitter(command, "error", sink)
	process := exec.CommandContext(runCtx, release.BinaryPath(), args...) // #nosec G204 -- executable is revalidated from the immutable release cache and argv is built from typed fields.
	process.Dir = workDir
	process.Env = stackKitChildEnvironment(os.Environ())
	process.Stdout = stdout
	process.Stderr = io.MultiWriter(stderr, stderrLogs)
	eventsDone := make(chan struct{})
	eventsStopped := make(chan struct{})
	if sink != nil {
		go func() {
			defer close(eventsStopped)
			streamStackKitEventFile(runCtx, eventPath, command, sink, eventsDone)
		}()
	} else {
		close(eventsStopped)
	}
	runErr := process.Run()
	close(eventsDone)
	<-eventsStopped
	stderrLogs.Close()
	if runCtx.Err() != nil {
		runErr = fmt.Errorf("StackKits command timed out after %s: %w", timeout, runCtx.Err())
	}
	events, eventsErr := readStackKitEvents(eventPath)
	if eventsErr != nil {
		if runErr == nil {
			runErr = eventsErr
		} else {
			runErr = fmt.Errorf("%v; %w", runErr, eventsErr)
		}
	}

	receipt := release.Receipt()
	result.Release = stackKitReleasePinFromReceipt(receipt)
	result.ExitCode = int32(exitCode(runErr))
	result.Stderr = secrets.Redact(stderr.String())
	result.EventsJsonl = events
	return finishStackKitResult(
		result,
		command,
		[]byte(secrets.Redact(stdout.String())),
		&receipt,
		runErr,
		started,
	)
}

type stackKitLineEmitter struct {
	mu      sync.Mutex
	pending []byte
	command *agentpb.StackKitCommand
	level   string
	sink    func(*agentpb.LogEntry)
}

func newStackKitLineEmitter(command *agentpb.StackKitCommand, level string, sink func(*agentpb.LogEntry)) *stackKitLineEmitter {
	return &stackKitLineEmitter{command: command, level: level, sink: sink}
}

func (writer *stackKitLineEmitter) Write(payload []byte) (int, error) {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	written := len(payload)
	writer.pending = append(writer.pending, payload...)
	for {
		index := bytes.IndexByte(writer.pending, '\n')
		if index < 0 {
			break
		}
		writer.emit(writer.pending[:index])
		writer.pending = writer.pending[index+1:]
	}
	return written, nil
}

func (writer *stackKitLineEmitter) Close() {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	writer.emit(writer.pending)
	writer.pending = nil
}

func (writer *stackKitLineEmitter) emit(line []byte) {
	message := strings.TrimSpace(secrets.Redact(string(line)))
	if writer.sink == nil || message == "" {
		return
	}
	writer.sink(stackKitLiveLogEntry(writer.command, writer.level, message, nil))
}

func streamStackKitEventFile(ctx context.Context, path string, command *agentpb.StackKitCommand, sink func(*agentpb.LogEntry), done <-chan struct{}) {
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	offset := 0
	read := func(final bool) {
		payload, err := os.ReadFile(filepath.Clean(path))
		if err != nil || offset >= len(payload) {
			return
		}
		remaining := payload[offset:]
		end := bytes.LastIndexByte(remaining, '\n')
		if end < 0 {
			if !final {
				return
			}
			end = len(remaining) - 1
		}
		chunk := remaining[:end+1]
		offset += end + 1
		for _, line := range bytes.Split(chunk, []byte{'\n'}) {
			emitStackKitProgressLine(command, line, sink)
		}
	}
	for {
		select {
		case <-ctx.Done():
			read(true)
			return
		case <-done:
			read(true)
			return
		case <-ticker.C:
			read(false)
		}
	}
}

func emitStackKitProgressLine(command *agentpb.StackKitCommand, raw []byte, sink func(*agentpb.LogEntry)) {
	if sink == nil || len(bytes.TrimSpace(raw)) == 0 {
		return
	}
	var event map[string]any
	if json.Unmarshal(raw, &event) != nil {
		return
	}
	phase, _ := event["phase"].(string)
	status, _ := event["status"].(string)
	message, _ := event["message"].(string)
	if strings.TrimSpace(message) == "" {
		message = strings.TrimSpace(phase + " " + status)
	}
	fields := map[string]string{"phase": phase, "status": status}
	entry := stackKitLiveLogEntry(command, stackKitProgressLevel(status), message, fields)
	if rawTime, _ := event["time"].(string); rawTime != "" {
		if parsed, err := time.Parse(time.RFC3339Nano, rawTime); err == nil {
			entry.TimestampUnix = parsed.Unix()
		}
	}
	sink(entry)
}

func stackKitLiveLogEntry(command *agentpb.StackKitCommand, level, message string, fields map[string]string) *agentpb.LogEntry {
	if fields == nil {
		fields = map[string]string{}
	}
	if command != nil {
		fields["command_id"] = command.CommandId
		fields["job_id"] = stackKitLiveJobID(command.CommandId)
		fields["stackkit"] = command.Stackkit
		fields["stack_name"] = command.StackName
	}
	return &agentpb.LogEntry{TimestampUnix: time.Now().UTC().Unix(), Level: level, Message: secrets.Redact(message), Fields: fields}
}

func stackKitLiveJobID(commandID string) string {
	value := strings.TrimSpace(commandID)
	for _, suffix := range []string{"-plan", "-verify"} {
		value = strings.TrimSuffix(value, suffix)
	}
	return value
}

func stackKitProgressLevel(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "failed", "error":
		return "error"
	case "warning", "warn", "skipped":
		return "warn"
	default:
		return "info"
	}
}

// FailureResult returns the same bounded, redacted result envelope used by a
// failed StackKits child process. Runtime admission wrappers use it when an
// exact release cannot be converged before the command is allowed to execute.
func (executor *StackKitExecutor) FailureResult(command *agentpb.StackKitCommand, runErr error) *agentpb.StackKitResult {
	started := time.Now().UTC()
	result := &agentpb.StackKitResult{
		StartedAtUnix: started.Unix(),
		ExitCode:      1,
	}
	if command != nil {
		result.CommandId = command.CommandId
		result.Release = cloneStackKitReleasePin(command.Release)
	}
	if runErr == nil {
		runErr = errors.New("StackKits runtime admission failed")
	}
	return finishStackKitResult(result, command, nil, nil, runErr, started)
}

func (executor *StackKitExecutor) prepare(command *agentpb.StackKitCommand) (
	stackkitrelease.Release,
	[]string,
	string,
	string,
	error,
) {
	if command == nil {
		return stackkitrelease.Release{}, nil, "", "", fmt.Errorf("StackKit command is required")
	}
	command.CommandId = strings.TrimSpace(command.CommandId)
	if command.CommandId == "" {
		return stackkitrelease.Release{}, nil, "", "", fmt.Errorf("StackKit command_id is required")
	}
	if executor == nil || executor.pinPath == "" || executor.cacheRoot == "" {
		return stackkitrelease.Release{}, nil, "", "", fmt.Errorf(
			"StackKit execution requires %s and %s",
			stackKitReleasePinEnv,
			stackKitReleaseCacheEnv,
		)
	}
	release, err := (stackkitrelease.Cache{Root: executor.cacheRoot}).ResolvePin(executor.pinPath)
	if err != nil {
		return stackkitrelease.Release{}, nil, "", "", fmt.Errorf("resolve pinned StackKits release: %w", err)
	}
	if err := requireMatchingStackKitRelease(command.Release, release.Receipt()); err != nil {
		return stackkitrelease.Release{}, nil, "", "", err
	}
	workDir, err := cleanStackKitWorkDir(command.WorkingDirectory)
	if err != nil {
		return stackkitrelease.Release{}, nil, "", "", err
	}
	specPath, err := cleanStackKitRelativePath(command.SpecPath, "stack-spec.yaml", "spec_path")
	if err != nil {
		return stackkitrelease.Release{}, nil, "", "", err
	}
	eventFile, err := os.CreateTemp("", "techstack-stackkit-events-*.jsonl")
	if err != nil {
		return stackkitrelease.Release{}, nil, "", "", fmt.Errorf("create StackKits event spool: %w", err)
	}
	eventPath := eventFile.Name()
	if closeErr := eventFile.Close(); closeErr != nil {
		_ = os.Remove(eventPath)
		return stackkitrelease.Release{}, nil, "", "", fmt.Errorf("close StackKits event spool: %w", closeErr)
	}
	if chmodErr := os.Chmod(eventPath, 0600); chmodErr != nil && runtime.GOOS != "windows" {
		_ = os.Remove(eventPath)
		return stackkitrelease.Release{}, nil, "", "", fmt.Errorf("protect StackKits event spool: %w", chmodErr)
	}

	args := []string{
		"--no-log",
		"--chdir", workDir,
		"--spec", specPath,
		"--progress-jsonl", eventPath,
	}
	operationArgs, err := stackKitOperationArgs(command)
	if err != nil {
		_ = os.Remove(eventPath)
		return stackkitrelease.Release{}, nil, "", "", err
	}
	if err := materializeStackKitInventory(workDir, command.InventoryJson); err != nil {
		_ = os.Remove(eventPath)
		return stackkitrelease.Release{}, nil, "", "", err
	}
	return release, append(args, operationArgs...), workDir, eventPath, nil
}

func materializeStackKitInventory(workDir string, raw []byte) error {
	if len(raw) == 0 {
		return nil
	}
	if len(raw) > maxStackKitInventoryBytes {
		return fmt.Errorf("StackKits Inventory exceeds %d bytes", maxStackKitInventoryBytes)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var inventory map[string]any
	if err := decoder.Decode(&inventory); err != nil {
		return fmt.Errorf("decode StackKits Inventory: %w", err)
	}
	schemaVersion, _ := inventory["schemaVersion"].(string)
	if strings.TrimSpace(schemaVersion) != "stackkit.inventory/v1" {
		return fmt.Errorf("StackKits Inventory requires schemaVersion stackkit.inventory/v1")
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return fmt.Errorf("StackKits Inventory must contain exactly one JSON document")
		}
		return fmt.Errorf("decode trailing StackKits Inventory data: %w", err)
	}
	canonical, err := json.Marshal(inventory)
	if err != nil {
		return fmt.Errorf("encode StackKits Inventory: %w", err)
	}
	canonical = append(canonical, '\n')
	directory := filepath.Join(workDir, ".stackkit")
	if err := os.MkdirAll(directory, 0o750); err != nil {
		return fmt.Errorf("create StackKits Inventory directory: %w", err)
	}
	tmp, err := os.CreateTemp(directory, ".inventory-*.json")
	if err != nil {
		return fmt.Errorf("create temporary StackKits Inventory: %w", err)
	}
	tmpName := tmp.Name()
	keep := false
	defer func() {
		_ = tmp.Close()
		if !keep {
			_ = os.Remove(tmpName)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil && runtime.GOOS != "windows" {
		return fmt.Errorf("protect temporary StackKits Inventory: %w", err)
	}
	if _, err := tmp.Write(canonical); err != nil {
		return fmt.Errorf("write temporary StackKits Inventory: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("sync temporary StackKits Inventory: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temporary StackKits Inventory: %w", err)
	}
	if err := replaceStackKitInventory(tmpName, filepath.Join(directory, "inventory.json")); err != nil {
		return fmt.Errorf("publish StackKits Inventory: %w", err)
	}
	keep = true
	return nil
}

func stackKitOperationArgs(command *agentpb.StackKitCommand) ([]string, error) {
	switch command.Operation {
	case agentpb.StackKitOperation_STACKKIT_OPERATION_INIT:
		kit := strings.TrimSpace(command.Stackkit)
		if kit != "basement-kit" && kit != "cloud-kit" {
			return nil, fmt.Errorf("StackKit init requires a supported StackKit")
		}
		name := strings.TrimSpace(command.StackName)
		if name == "" || strings.HasPrefix(name, "-") || strings.ContainsAny(name, `/\\`) {
			return nil, fmt.Errorf("StackKit init stack_name is invalid")
		}
		expectedHash := strings.TrimSpace(command.ExpectedSpecHash)
		if expectedHash != "" && !stackKitExpectedSpecHashPattern.MatchString(expectedHash) {
			return nil, fmt.Errorf("StackKit init expected_spec_hash is invalid")
		}
		args := []string{"init", kit, "--name", name, "--owner-source=local", "--non-interactive"}
		if expectedHash != "" {
			args = append(args, "--expected-spec-hash", expectedHash)
		}
		if domain := strings.TrimSpace(command.Domain); domain != "" {
			if strings.HasPrefix(domain, "-") || strings.ContainsAny(domain, `/\\`) {
				return nil, fmt.Errorf("StackKit init domain is invalid")
			}
			args = append(args, "--domain", domain)
		}
		return args, nil
	case agentpb.StackKitOperation_STACKKIT_OPERATION_VALIDATE:
		return []string{"validate"}, nil
	case agentpb.StackKitOperation_STACKKIT_OPERATION_GENERATE:
		output, err := cleanStackKitRelativePath(command.OutputDirectory, "deploy", "output_directory")
		if err != nil {
			return nil, err
		}
		return []string{"generate", "--output", filepath.ToSlash(output)}, nil
	case agentpb.StackKitOperation_STACKKIT_OPERATION_PLAN:
		return []string{"plan", "--json"}, nil
	case agentpb.StackKitOperation_STACKKIT_OPERATION_APPLY:
		siteRef := strings.TrimSpace(command.LocalSiteRef)
		nodeRef := strings.TrimSpace(command.LocalNodeRef)
		channelRef := strings.TrimSpace(command.LocalExecutionChannelRef)
		if siteRef == "" || nodeRef == "" || channelRef == "" {
			return nil, fmt.Errorf("StackKit apply requires local Site, node, and execution-channel references")
		}
		expectedPlanHash := strings.TrimSpace(command.ExpectedPlanHash)
		if !stackKitExpectedSpecHashPattern.MatchString(expectedPlanHash) {
			return nil, fmt.Errorf("StackKit apply expected_plan_hash is required and must be a lowercase sha256:<64-hex> digest")
		}
		return []string{
			"apply",
			"--auto-approve",
			"--expected-plan-hash", expectedPlanHash,
			"--local-site", siteRef,
			"--local-node", nodeRef,
			"--local-execution-channel", channelRef,
		}, nil
	case agentpb.StackKitOperation_STACKKIT_OPERATION_VERIFY:
		args := []string{"verify", "--json"}
		if command.Offline {
			args = append(args, "--offline")
		}
		return args, nil
	case agentpb.StackKitOperation_STACKKIT_OPERATION_UPGRADE:
		target := strings.TrimSpace(command.TargetRelease)
		if target == "" {
			target = "latest"
		}
		if !stackKitTargetReleasePattern.MatchString(target) {
			return nil, fmt.Errorf("StackKit upgrade target_release is invalid")
		}
		if !command.DryRun && !command.OwnerApproved {
			return nil, fmt.Errorf("StackKit upgrade requires explicit Owner approval")
		}
		args := []string{"upgrade", "--to", target, "--json"}
		if command.DryRun {
			args = append(args, "--dry-run")
		}
		return args, nil
	case agentpb.StackKitOperation_STACKKIT_OPERATION_DRIFT_DETECT:
		return []string{"drift", "detect", "--json"}, nil
	case agentpb.StackKitOperation_STACKKIT_OPERATION_DRIFT_RECONCILE:
		if !command.OwnerApproved {
			return nil, fmt.Errorf("StackKit drift reconcile requires explicit Owner approval")
		}
		mode := "standard"
		switch command.DriftMode {
		case agentpb.StackKitDriftMode_STACKKIT_DRIFT_MODE_STANDARD:
		case agentpb.StackKitDriftMode_STACKKIT_DRIFT_MODE_ADVANCED:
			mode = "advanced"
		default:
			return nil, fmt.Errorf("StackKit drift reconcile requires an explicit drift_mode")
		}
		return []string{"drift", "reconcile", "--mode", mode, "--owner-approve", "--json"}, nil
	case agentpb.StackKitOperation_STACKKIT_OPERATION_SERVICE_START,
		agentpb.StackKitOperation_STACKKIT_OPERATION_SERVICE_STOP,
		agentpb.StackKitOperation_STACKKIT_OPERATION_SERVICE_RESTART:
		if !command.OwnerApproved {
			return nil, fmt.Errorf("StackKit service mutation requires explicit Owner approval")
		}
		serviceKey := strings.TrimSpace(command.ServiceKey)
		if !stackKitServiceKeyPattern.MatchString(serviceKey) {
			return nil, fmt.Errorf("StackKit service_key is invalid")
		}
		action := strings.TrimPrefix(strings.ToLower(command.Operation.String()), "stackkit_operation_service_")
		return []string{"service", action, serviceKey, "--json", "--owner-approve"}, nil
	case agentpb.StackKitOperation_STACKKIT_OPERATION_SERVICE_LOGS:
		serviceKey := strings.TrimSpace(command.ServiceKey)
		if !stackKitServiceKeyPattern.MatchString(serviceKey) {
			return nil, fmt.Errorf("StackKit service_key is invalid")
		}
		tail := command.LogTail
		if tail == 0 {
			tail = 100
		}
		if tail < 1 || tail > 200 {
			return nil, fmt.Errorf("StackKit service log_tail must be between 1 and 200")
		}
		args := []string{"service", "logs", serviceKey, "--tail", fmt.Sprint(tail), "--json"}
		if cursor := strings.TrimSpace(command.LogCursor); cursor != "" {
			args = append(args, "--cursor", cursor)
		}
		return args, nil
	default:
		return nil, fmt.Errorf("unsupported StackKit operation %s", command.Operation.String())
	}
}

func requireMatchingStackKitRelease(expected *agentpb.StackKitReleasePin, actual stackkitrelease.Receipt) error {
	if expected == nil {
		return fmt.Errorf("StackKit command release pin is required")
	}
	if strings.TrimSpace(expected.Version) != actual.Version ||
		strings.TrimSpace(expected.PlatformOs) != actual.Platform.OS ||
		strings.TrimSpace(expected.PlatformArch) != actual.Platform.Arch ||
		strings.TrimSpace(expected.ArchiveSha256) != actual.ArchiveSHA256 ||
		strings.TrimSpace(expected.ReleaseIndexSha256) != actual.IndexSHA256 {
		return fmt.Errorf("StackKit command release does not match the locally verified release")
	}
	return nil
}

func cleanStackKitWorkDir(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", fmt.Errorf("StackKit working_directory is required")
	}
	absolute, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return "", fmt.Errorf("resolve StackKit working_directory: %w", err)
	}
	info, err := os.Lstat(absolute)
	if err != nil {
		return "", fmt.Errorf("inspect StackKit working_directory: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("StackKit working_directory must be a real directory")
	}
	return absolute, nil
}

func cleanStackKitRelativePath(value, fallback, field string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		value = fallback
	}
	if value == "" {
		return "", nil
	}
	if filepath.IsAbs(value) {
		return "", fmt.Errorf("StackKit %s must be relative to working_directory", field)
	}
	clean := filepath.Clean(value)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("StackKit %s escapes working_directory", field)
	}
	return clean, nil
}

type stackKitCommandResultEnvelope struct {
	SchemaVersion string                    `json:"schemaVersion"`
	Command       string                    `json:"command"`
	Status        string                    `json:"status"`
	Data          stackKitCommandResultData `json:"data"`
}

type stackKitCommandResultData struct {
	Output  string                      `json:"output,omitempty"`
	Error   string                      `json:"error,omitempty"`
	Release *agentpb.StackKitReleasePin `json:"release,omitempty"`
}

func finishStackKitResult(
	result *agentpb.StackKitResult,
	command *agentpb.StackKitCommand,
	stdout []byte,
	receipt *stackkitrelease.Receipt,
	runErr error,
	started time.Time,
) *agentpb.StackKitResult {
	if result == nil {
		result = &agentpb.StackKitResult{}
	}
	result.CommandResultSchemaVersion = stackKitCommandResultVersion
	result.EventsSchemaVersion = stackKitRolloutEventVersion
	result.Success = runErr == nil
	if runErr != nil && result.ExitCode == 0 {
		result.ExitCode = 1
	}
	result.FinishedAtUnix = time.Now().UTC().Unix()
	if result.StartedAtUnix == 0 {
		result.StartedAtUnix = started.Unix()
	}
	if runErr != nil {
		message := secrets.Redact(runErr.Error())
		if result.Stderr == "" {
			result.Stderr = message
		} else if !strings.Contains(result.Stderr, message) {
			result.Stderr += "\n" + message
		}
	}
	if isStackKitCommandResult(stdout) {
		result.CommandResultJson = append([]byte(nil), stdout...)
		return result
	}
	status := "success"
	if runErr != nil {
		status = "failed"
	}
	commandName := ""
	if command != nil {
		commandName = stackkitcommand.ResultCommandName(command.Operation)
	}
	releasePin := result.Release
	if receipt != nil {
		releasePin = stackKitReleasePinFromReceipt(*receipt)
	}
	envelope, err := json.Marshal(stackKitCommandResultEnvelope{
		SchemaVersion: stackKitCommandResultVersion,
		Command:       commandName,
		Status:        status,
		Data: stackKitCommandResultData{
			Output:  strings.TrimSpace(string(stdout)),
			Error:   errorString(runErr),
			Release: releasePin,
		},
	})
	if err != nil {
		result.Success = false
		result.ExitCode = 1
		result.Stderr = secrets.Redact(fmt.Sprintf("encode StackKit command result: %v", err))
		return result
	}
	result.CommandResultJson = envelope
	return result
}

func isStackKitCommandResult(data []byte) bool {
	if len(bytes.TrimSpace(data)) == 0 || !json.Valid(data) {
		return false
	}
	var identity struct {
		SchemaVersion string `json:"schemaVersion"`
		Command       string `json:"command"`
		Status        string `json:"status"`
	}
	if err := json.Unmarshal(data, &identity); err != nil {
		return false
	}
	return identity.SchemaVersion == stackKitCommandResultVersion &&
		strings.TrimSpace(identity.Command) != "" &&
		(identity.Status == "success" || identity.Status == "failed" || identity.Status == "denied")
}

func readStackKitEvents(path string) ([][]byte, error) {
	file, err := os.Open(filepath.Clean(path))
	if err != nil {
		return nil, fmt.Errorf("read StackKits event spool: %w", err)
	}
	defer file.Close()
	limited := io.LimitReader(file, maxStackKitCommandEventBytes+1)
	scanner := bufio.NewScanner(limited)
	scanner.Buffer(make([]byte, 64<<10), maxStackKitCommandEventLine)
	events := make([][]byte, 0, 32)
	var total int
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		total += len(line) + 1
		if total > maxStackKitCommandEventBytes {
			return nil, fmt.Errorf("StackKits event spool exceeds %d bytes", maxStackKitCommandEventBytes)
		}
		if len(events) >= maxStackKitCommandEventCount {
			return nil, fmt.Errorf("StackKits event spool exceeds %d events", maxStackKitCommandEventCount)
		}
		var identity struct {
			Time   time.Time `json:"time"`
			Phase  string    `json:"phase"`
			Status string    `json:"status"`
		}
		if !json.Valid(line) || json.Unmarshal(line, &identity) != nil ||
			identity.Time.IsZero() || strings.TrimSpace(identity.Phase) == "" ||
			!validStackKitEventStatus(identity.Status) {
			return nil, fmt.Errorf("StackKits event spool contains a malformed rollout event")
		}
		events = append(events, []byte(secrets.Redact(string(line))))
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan StackKits event spool: %w", err)
	}
	return events, nil
}

func validStackKitEventStatus(status string) bool {
	switch status {
	case "started", "running", "succeeded", "failed", "skipped":
		return true
	default:
		return false
	}
}

func stackKitReleasePinFromReceipt(receipt stackkitrelease.Receipt) *agentpb.StackKitReleasePin {
	return &agentpb.StackKitReleasePin{
		Version:            receipt.Version,
		PlatformOs:         receipt.Platform.OS,
		PlatformArch:       receipt.Platform.Arch,
		ArchiveSha256:      receipt.ArchiveSHA256,
		ReleaseIndexSha256: receipt.IndexSHA256,
	}
}

func cloneStackKitReleasePin(pin *agentpb.StackKitReleasePin) *agentpb.StackKitReleasePin {
	if pin == nil {
		return nil
	}
	return &agentpb.StackKitReleasePin{
		Version:            pin.Version,
		PlatformOs:         pin.PlatformOs,
		PlatformArch:       pin.PlatformArch,
		ArchiveSha256:      pin.ArchiveSha256,
		ReleaseIndexSha256: pin.ReleaseIndexSha256,
	}
}

func stackKitChildEnvironment(environment []string) []string {
	filtered := make([]string, 0, len(environment))
	for _, entry := range environment {
		key, _, _ := strings.Cut(entry, "=")
		upper := strings.ToUpper(strings.TrimSpace(key))
		if strings.HasPrefix(upper, "KOMBIFY_") ||
			strings.HasPrefix(upper, "TECHSTACK_") ||
			upper == "SERVICE_AUTH_SECRET" ||
			upper == "STACKKIT_ADMIN_TOKEN" {
			continue
		}
		filtered = append(filtered, entry)
	}
	return filtered
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return 1
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return secrets.Redact(err.Error())
}

type boundedStackKitBuffer struct {
	buffer    bytes.Buffer
	remaining int
	truncated bool
}

func newBoundedStackKitBuffer(limit int) *boundedStackKitBuffer {
	return &boundedStackKitBuffer{remaining: limit}
}

func (buffer *boundedStackKitBuffer) Write(data []byte) (int, error) {
	original := len(data)
	if buffer.remaining > 0 {
		write := len(data)
		if write > buffer.remaining {
			write = buffer.remaining
		}
		_, _ = buffer.buffer.Write(data[:write])
		buffer.remaining -= write
		if write < len(data) {
			buffer.truncated = true
		}
	} else if len(data) > 0 {
		buffer.truncated = true
	}
	return original, nil
}

func (buffer *boundedStackKitBuffer) String() string {
	if buffer == nil {
		return ""
	}
	if buffer.truncated {
		return buffer.buffer.String() + stackKitOutputTruncationMessage
	}
	return buffer.buffer.String()
}
