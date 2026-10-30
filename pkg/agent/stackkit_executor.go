package agent

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kombifyio/techstack/internal/stackkitrelease"
	"github.com/kombifyio/techstack/pkg/api/agentpb"
	"github.com/kombifyio/techstack/pkg/secrets"
	"github.com/kombifyio/techstack/pkg/specv2"
	"github.com/kombifyio/techstack/pkg/stackkitcommand"
	"google.golang.org/protobuf/proto"
	"gopkg.in/yaml.v3"
)

const (
	StackKitAgentCapability = "stackkit"
	stackKitReleasePinEnv   = "TECHSTACK_STACKKIT_RELEASE_PIN"
	stackKitReleaseCacheEnv = "TECHSTACK_STACKKIT_RELEASE_CACHE"
	// defaultStackKitPersistentReleaseCache lies outside the runtime tree a
	// replacement deletes; guardlayout writes the same path into executor.env.
	defaultStackKitPersistentReleaseCache = "/var/lib/techstack/.stackkit/releases"
	stackKitCommandResultVersion          = "stackkit.command-result/v1"
	stackKitRolloutEventVersion           = "stackkit.rollout-event/v1"
	defaultStackKitCommandTimeout         = 30 * time.Minute
	maxStackKitCommandOutputBytes         = 16 << 20
	maxStackKitCommandEventBytes          = 16 << 20
	maxStackKitCommandEventLine           = 1 << 20
	maxStackKitCommandEventCount          = 10_000
	maxStackKitInventoryBytes             = 2 << 20
	stackKitOutputTruncationMessage       = "\n<output truncated by Techstack agent>"
	// stackKitAdvancedTrustBundlePath is the workspace-relative file the
	// agent hands to `stackkit advanced trust import --bundle`; the CLI reads
	// the bundle from a file only. It is removed after the command.
	stackKitAdvancedTrustBundlePath = ".stackkit/techstack-advanced-trust-bundle.json"
)

var stackKitTargetReleasePattern = regexp.MustCompile(
	`^(latest|v[0-9]+\.[0-9]+\.[0-9]+(?:-(?:beta|edge)\.[0-9]+)?|channel:(?:stable|beta|edge))$`,
)

var stackKitExpectedSpecHashPattern = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)
var stackKitServiceKeyPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
var stackKitAddressPrefixPattern = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// StackKitExecutor is the agent-side adapter for the typed StackKitCommand
// seam. It resolves the local immutable release pin on every operation and
// never accepts an executable, argv, environment, or shell fragment from Core.
type StackKitExecutor struct {
	pinPath                  string
	cacheRoot                string
	candidateAdmission       func(stackkitrelease.Pin) error
	preinstalledManifestPath string
	// silenceLimit overrides stackKitApplySilenceLimit; zero keeps the default.
	silenceLimit time.Duration
	mu           sync.RWMutex
}

// NewStackKitExecutorFromEnv creates a lazy executor. Missing configuration is
// reported when a command arrives, so agents that do not advertise the
// "stackkit" command class can still start.
func NewStackKitExecutorFromEnv() *StackKitExecutor {
	return NewStackKitExecutorFromConfig(os.Getenv(stackKitReleasePinEnv), os.Getenv(stackKitReleaseCacheEnv))
}

// NewStackKitExecutorFromConfig accepts only host-owned runtime configuration.
// The privileged Guard uses its own EnvironmentFile, never request fields.
func NewStackKitExecutorFromConfig(pinPath, cacheRoot string) *StackKitExecutor {
	pinPath = strings.TrimSpace(pinPath)
	cacheRoot = strings.TrimSpace(cacheRoot)
	if runtime.GOOS == "linux" {
		if pinPath == "" {
			pinPath = "/app/.stackkit/stackkits-release-pin.json"
		}
		if cacheRoot == "" {
			cacheRoot = defaultStackKitPersistentReleaseCache
		}
	}
	return &StackKitExecutor{
		pinPath:                  pinPath,
		cacheRoot:                cacheRoot,
		preinstalledManifestPath: defaultStackKitPreinstalledManifest(),
	}
}

// WithInternalCandidateAdmission returns a separate executor using the root
// host's installed-candidate verifier. Default constructors stay public-only.
func (executor *StackKitExecutor) WithInternalCandidateAdmission(admit func(stackkitrelease.Pin) error) *StackKitExecutor {
	return &StackKitExecutor{pinPath: executor.pinPath, cacheRoot: executor.cacheRoot, preinstalledManifestPath: executor.preinstalledManifestPath, silenceLimit: executor.silenceLimit, candidateAdmission: admit}
}
func (executor *StackKitExecutor) releaseCache() stackkitrelease.Cache {
	return stackkitrelease.Cache{Root: executor.cacheRoot, CandidateAdmission: executor.candidateAdmission}
}
func (executor *StackKitExecutor) Available() bool {
	return executor != nil && executor.pinPath != "" && executor.cacheRoot != ""
}

func (executor *StackKitExecutor) Validate() error {
	executor.mu.RLock()
	defer executor.mu.RUnlock()
	ctx, cancel := context.WithTimeout(context.Background(), hostSecurityHelpTimeout)
	defer cancel()
	unlock, err := executor.lockRuntime(ctx, false)
	if err != nil {
		return err
	}
	defer unlock()
	return executor.validate()
}

func (executor *StackKitExecutor) validate() error {
	if !executor.Available() {
		return fmt.Errorf("StackKits runtime is not configured")
	}
	_, err := executor.releaseCache().ResolvePin(executor.pinPath)
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
	timeout := defaultStackKitCommandTimeout
	if command != nil && command.TimeoutSeconds > 0 {
		timeout = time.Duration(command.TimeoutSeconds) * time.Second
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	unlock, lockErr := executor.lockRuntime(runCtx, false)
	if lockErr != nil {
		return finishStackKitResult(result, command, nil, nil, lockErr, started)
	}
	defer unlock()

	release, args, workDir, eventPath, tempFiles, err := executor.prepare(command)
	if eventPath != "" {
		defer os.Remove(eventPath)
	}
	// The capability and candidate files exist only for this invocation.
	defer removeStackKitTempFiles(tempFiles)
	if workDir != "" && command.Operation == agentpb.StackKitOperation_STACKKIT_OPERATION_ADVANCED_TRUST_IMPORT {
		bundlePath := filepath.Join(workDir, filepath.FromSlash(stackKitAdvancedTrustBundlePath))
		defer func() { _ = os.Remove(bundlePath) }()
	}
	if err != nil {
		return finishStackKitResult(result, command, nil, nil, err, started)
	}

	stdout := newBoundedStackKitBuffer(maxStackKitCommandOutputBytes)
	stderr := newBoundedStackKitBuffer(maxStackKitCommandOutputBytes)
	stderrLogs := newStackKitLineEmitter(command, "error", sink)
	// The wall-clock deadline of runCtx stays the upper cap. endProcess ends the
	// same process earlier, with its own cause, when an apply goes silent.
	processCtx, endProcess := context.WithCancelCause(runCtx)
	defer endProcess(nil)
	process := newStackKitProcess(processCtx, release.BinaryPath(), args...)
	process.Dir = workDir
	if command.Operation == agentpb.StackKitOperation_STACKKIT_OPERATION_INIT {
		process.Stdin = bytes.NewReader(command.CandidateSpecJson)
	}
	if (stackkitcommand.IsBackupTargetDeliveryOperation(command.Operation) || command.Operation == agentpb.StackKitOperation_STACKKIT_OPERATION_WALLET_ITEM_IMPORT ||
		command.Operation == agentpb.StackKitOperation_STACKKIT_OPERATION_HOST_SSH_APPLY) && len(command.SealedDelivery) != 0 {
		// Ciphertext only: the pinned CLI opens it with the node's own key.
		process.Stdin = bytes.NewReader(command.SealedDelivery)
	}
	if stackkitcommand.RelocationPlanOnStdin(command.Operation) {
		// The secret-free plan from the source; argv names only "-".
		process.Stdin = bytes.NewReader(command.RelocationPlanJson)
	}
	process.Env = stackKitChildEnvironment(os.Environ())
	if command.Operation == agentpb.StackKitOperation_STACKKIT_OPERATION_INIT {
		// INIT consumes only this non-secret mode marker. Its native image
		// admission enforces the CUE-owned managed target; prepare's separate
		// orchestration custody environment is never passed to this child.
		process.Env = append(process.Env, "TECHSTACK_MANAGED=1")
	}
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
	silenceStopped := make(chan struct{})
	if command.Operation == agentpb.StackKitOperation_STACKKIT_OPERATION_APPLY {
		go func() {
			defer close(silenceStopped)
			ticker := time.NewTicker(stackKitProgressPollInterval)
			defer ticker.Stop()
			watchStackKitSilence(eventPath, newStackKitSilenceWatch(executor.applySilenceLimit()), ticker.C, eventsDone,
				func(stall stackkitcommand.ApplyStall) { endProcess(&stackKitApplyStalledError{stall: stall}) })
		}()
	} else {
		close(silenceStopped)
	}
	runErr := process.Start()
	if runErr == nil {
		observeStackKitProcess(ctx, process)
		runErr = process.Wait()
	}
	close(eventsDone)
	<-eventsStopped
	<-silenceStopped
	stderrLogs.Close()
	var stalled *stackKitApplyStalledError
	switch cause := context.Cause(processCtx); {
	case errors.As(cause, &stalled) && runErr != nil:
		// The node ended the apply itself: it went silent, it was not slow.
		runErr = stalled
	case runCtx.Err() != nil:
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
	result.ExitCode = clampIntToInt32(exitCode(runErr))
	result.Stderr = secrets.Redact(stderr.String())
	result.EventsJsonl = events
	publicOutput := []byte(secrets.Redact(stdout.String()))
	if identityMutationCarriesTransientURL(command) {
		// Bearer URLs are not covered by generic token regexes. Keep the
		// ordinary result structurally secret-free; the approved direct caller
		// receives the original envelope through SensitiveResultJson only.
		var envelope struct {
			SchemaVersion string `json:"schemaVersion"`
			Command       string `json:"command"`
			Status        string `json:"status"`
		}
		if json.Unmarshal([]byte(stdout.String()), &envelope) != nil {
			runErr = errors.New("invalid identity command response")
		}
		publicOutput, _ = json.Marshal(map[string]any{"schemaVersion": envelope.SchemaVersion, "command": envelope.Command, "status": envelope.Status, "data": map[string]any{}})
	}
	var handoffDocument []byte
	if stackkitcommand.IsHandoffOperation(command.Operation) && runErr == nil {
		// The documents are public by contract and must reach the other node
		// unredacted, so they are read from the CLI's own output, not from the
		// redacted copy.
		document, envelope, handoffErr := collectStackKitHandoff(workDir, command, []byte(stdout.String()))
		if handoffErr != nil {
			runErr = handoffErr
		} else {
			handoffDocument, publicOutput = document, envelope
		}
	}
	finished := finishStackKitResult(
		result,
		command,
		publicOutput,
		&receipt,
		runErr,
		started,
	)
	if finished.Success && command.Operation == agentpb.StackKitOperation_STACKKIT_OPERATION_USE_CASE_REMOVE {
		if err := attachUseCaseRemoveCandidate(command, finished, tempFiles); err != nil {
			return finishStackKitResult(result, command, nil, &receipt, err, started)
		}
	}
	if finished.Success && command.Operation == agentpb.StackKitOperation_STACKKIT_OPERATION_USE_CASE_CONFIGURE {
		if err := attachUseCaseConfigureCandidate(command, finished, tempFiles); err != nil {
			return finishStackKitResult(result, command, nil, &receipt, err, started)
		}
	}
	if finished.Success && len(handoffDocument) != 0 {
		finished.HandoffDocument = handoffDocument
	}
	if finished.Success && command.Operation == agentpb.StackKitOperation_STACKKIT_OPERATION_ADVANCED_CHANGE_SET_CREATE {
		digest, digestErr := stackKitChangeSetRecordDigest(workDir, finished.CommandResultJson)
		if digestErr != nil {
			return finishStackKitResult(result, command, publicOutput, &receipt, digestErr, started)
		}
		finished.AdvancedChangeSetSha256 = digest
	}
	if finished.Success && command.Operation == agentpb.StackKitOperation_STACKKIT_OPERATION_PLAN && command.StackkitInstanceId != "" {
		if command.SourceGenerationInspection {
			if err := attachSourceGenerationInspection(finished); err != nil {
				return finishStackKitResult(result, command, nil, &receipt, err, started)
			}
		}
		if err := attachManagedBackupPlan(workDir, command.StackkitInstanceId, finished); err != nil {
			return finishStackKitResult(result, command, nil, &receipt, err, started)
		}
		if command.SourceGenerationInspection {
			if _, _, err := stackkitcommand.ManagedSourceGenerationBackupPlan(finished, command.StackkitInstanceId); err != nil {
				return finishStackKitResult(result, command, nil, &receipt, err, started)
			}
		}
	}
	if finished.Success && identityMutationCarriesTransientURL(command) {
		finished.SensitiveResultJson = []byte(stdout.String())
	}
	return finished
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
	ticker := time.NewTicker(stackKitProgressPollInterval)
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
	fields := forwardedStackKitEventFields(event)
	fields["phase"], fields["status"] = phase, status
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
		fields["job_id"] = stackkitcommand.JobID(command.CommandId)
		fields["stackkit"] = command.Stackkit
		fields["stack_name"] = command.StackName
	}
	return &agentpb.LogEntry{TimestampUnix: time.Now().UTC().Unix(), Level: level, Message: secrets.Redact(message), Fields: fields}
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
	[]string,
	error,
) {
	if command == nil {
		return stackkitrelease.Release{}, nil, "", "", nil, fmt.Errorf("StackKit command is required")
	}
	if stackkitcommand.IsRestoreActivationOperation(command.Operation) {
		if err := stackkitcommand.ValidateCommand(command); err != nil {
			return stackkitrelease.Release{}, nil, "", "", nil, err
		}
	}
	command.CommandId = strings.TrimSpace(command.CommandId)
	if command.CommandId == "" {
		return stackkitrelease.Release{}, nil, "", "", nil, fmt.Errorf("StackKit command_id is required")
	}
	if executor == nil || executor.pinPath == "" || executor.cacheRoot == "" {
		return stackkitrelease.Release{}, nil, "", "", nil, fmt.Errorf(
			"StackKit execution requires %s and %s",
			stackKitReleasePinEnv,
			stackKitReleaseCacheEnv,
		)
	}
	release, err := executor.releaseCache().ResolvePin(executor.pinPath)
	if err != nil {
		return stackkitrelease.Release{}, nil, "", "", nil, fmt.Errorf("resolve pinned StackKits release: %w", err)
	}
	if err := requireMatchingStackKitRelease(command.Release, release.Receipt()); err != nil {
		return stackkitrelease.Release{}, nil, "", "", nil, err
	}
	hostOperation := stackkitcommand.IsHostOperation(command.Operation)
	var workDir string
	if hostOperation {
		// Host maintenance acts on the node's OS, not a stack: it takes no
		// workspace, StackSpec or workspace binding, only a neutral directory.
		workDir, err = cleanStackKitWorkDir(os.TempDir())
	} else {
		workDir, err = confineStackKitWorkDir(command.WorkingDirectory)
	}
	if err != nil {
		return stackkitrelease.Release{}, nil, "", "", nil, err
	}
	specPath, err := cleanStackKitRelativePath(command.SpecPath, "stack-spec.yaml", "spec_path")
	if err != nil {
		return stackkitrelease.Release{}, nil, "", "", nil, err
	}
	if stackkitcommand.RequiresWorkspaceInstanceBinding(command) {
		if err := requireStackKitWorkspaceInstance(workDir, specPath, command.StackkitInstanceId); err != nil {
			return stackkitrelease.Release{}, nil, "", "", nil, err
		}
	}
	eventFile, err := os.CreateTemp("", "techstack-stackkit-events-*.jsonl")
	if err != nil {
		return stackkitrelease.Release{}, nil, "", "", nil, fmt.Errorf("create StackKits event spool: %w", err)
	}
	eventPath := eventFile.Name()
	if closeErr := eventFile.Close(); closeErr != nil {
		_ = os.Remove(eventPath)
		return stackkitrelease.Release{}, nil, "", "", nil, fmt.Errorf("close StackKits event spool: %w", closeErr)
	}
	if chmodErr := os.Chmod(eventPath, 0600); chmodErr != nil && runtime.GOOS != "windows" {
		_ = os.Remove(eventPath)
		return stackkitrelease.Release{}, nil, "", "", nil, fmt.Errorf("protect StackKits event spool: %w", chmodErr)
	}

	args := []string{
		"--no-log",
		"--chdir", workDir,
		"--spec", specPath,
		"--progress-jsonl", eventPath,
	}
	if hostOperation {
		args = []string{"--no-log", "--progress-jsonl", eventPath}
	}
	var tempFiles []string
	var operationArgs []string
	if command.Operation == agentpb.StackKitOperation_STACKKIT_OPERATION_USE_CASE_REMOVE {
		operationArgs, tempFiles, err = stackKitUseCaseRemoveArgs(workDir, command)
	} else if command.Operation == agentpb.StackKitOperation_STACKKIT_OPERATION_USE_CASE_CONFIGURE {
		operationArgs, tempFiles, err = stackKitUseCaseConfigureArgs(workDir, command)
	} else if hostOperation {
		operationArgs, err = stackKitHostArgs(command)
	} else if stackkitcommand.IsHandoffOperation(command.Operation) {
		operationArgs, tempFiles, err = stackKitHandoffArgs(workDir, command)
	} else if stackkitcommand.IsApplicationConnectorOperation(command.Operation) {
		operationArgs, tempFiles, err = stackKitConnectorArgs(release, workDir, command)
	} else if stackkitcommand.IsApplicationOperation(command.Operation) {
		operationArgs, err = stackKitApplicationArgs(release, command)
	} else if stackkitcommand.IsAdvancedOperation(command.Operation) {
		operationArgs, tempFiles, err = stackKitAdvancedOperationArgs(release, workDir, specPath, command)
	} else {
		operationArgs, err = stackKitOperationArgs(command)
	}
	if err != nil {
		_ = os.Remove(eventPath)
		return stackkitrelease.Release{}, nil, "", "", tempFiles, err
	}
	if command.Operation == agentpb.StackKitOperation_STACKKIT_OPERATION_BACKUP_RESTORE_RECOVER && command.OriginalRecoveryRelease != nil {
		cache, cacheErr := executor.persistentReleaseCache()
		if cacheErr != nil {
			_ = os.Remove(eventPath)
			return stackkitrelease.Release{}, nil, "", "", tempFiles, cacheErr
		}
		operationArgs = append(operationArgs, "--original-release-cache", cache)
	}
	if release.Receipt().Candidate != nil && (command.SourceGenerationInspection || command.Operation == agentpb.StackKitOperation_STACKKIT_OPERATION_PREPARE_SOURCE_GENERATION) {
		cache, cacheErr := executor.persistentReleaseCache()
		if cacheErr != nil {
			_ = os.Remove(eventPath)
			return stackkitrelease.Release{}, nil, "", "", tempFiles, cacheErr
		}
		operationArgs = append(operationArgs, "--original-release-cache", cache)
	}
	if command.Operation == agentpb.StackKitOperation_STACKKIT_OPERATION_INIT {
		manifest, admissionErr := stackKitPreinstalledManifest(executor.preinstalledManifestPath)
		if admissionErr != nil {
			_ = os.Remove(eventPath)
			return stackkitrelease.Release{}, nil, "", "", tempFiles, admissionErr
		}
		if manifest != "" {
			if len(command.InventoryJson) != 0 {
				_ = os.Remove(eventPath)
				return stackkitrelease.Release{}, nil, "", "", tempFiles, fmt.Errorf("preinstalled image INIT requires Inventory to be handed off after native admission")
			}
			// StackKits owns content, profile, release and retry admission. Keep
			// the flag on every INIT, including an already activated workspace.
			operationArgs = append(operationArgs, "--preinstalled-manifest", manifest)
		}
	}
	if hostOperation {
		return release, append(args, operationArgs...), workDir, eventPath, tempFiles, nil
	}
	if err := materializeStackKitInventory(workDir, command.InventoryJson); err != nil {
		_ = os.Remove(eventPath)
		return stackkitrelease.Release{}, nil, "", "", tempFiles, err
	}
	if command.Operation == agentpb.StackKitOperation_STACKKIT_OPERATION_ADVANCED_TRUST_IMPORT {
		if err := materializeStackKitAdvancedTrustBundle(workDir, command.AdvancedTrustBundle); err != nil {
			_ = os.Remove(eventPath)
			return stackkitrelease.Release{}, nil, workDir, "", tempFiles, err
		}
	}
	return release, append(args, operationArgs...), workDir, eventPath, tempFiles, nil
}

func requireStackKitWorkspaceInstance(workDir, specPath, expected string) error {
	expected = strings.TrimSpace(expected)
	if expected == "" {
		return fmt.Errorf("StackKit workspace instance identity is required")
	}
	path := filepath.Join(workDir, filepath.FromSlash(specPath))
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect StackKit workspace identity: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() <= 0 || info.Size() > 2<<20 {
		return fmt.Errorf("StackKit workspace identity requires a bounded regular StackSpec")
	}
	data, err := os.ReadFile(path) // #nosec G304 -- path is confined to the validated StackKit workspace and relative spec path.
	if err != nil {
		return fmt.Errorf("read StackKit workspace identity: %w", err)
	}
	var spec map[string]any
	if err := yaml.Unmarshal(data, &spec); err != nil {
		return fmt.Errorf("decode StackKit workspace identity: %w", err)
	}
	actual := specv2.ResolvedStackKitInstanceID(spec)
	if actual == "" || actual != expected {
		return fmt.Errorf("StackKit workspace instance identity does not match the dispatched authority")
	}
	return nil
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
	if err := preserveNodeIssuedBackupBinding(workDir, inventory); err != nil {
		return err
	}
	canonical, err := json.Marshal(inventory)
	if err != nil {
		return fmt.Errorf("encode StackKits Inventory: %w", err)
	}
	canonical = append(canonical, '\n')
	directory := filepath.Join(workDir, ".stackkit")
	if err := requireStackKitStateDir(workDir); err != nil {
		return err
	}
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
		return stackKitInitArgs(command)
	case agentpb.StackKitOperation_STACKKIT_OPERATION_ADDRESS_BIND:
		return stackKitAddressBindArgs(command)
	case agentpb.StackKitOperation_STACKKIT_OPERATION_VALIDATE:
		return []string{"validate"}, nil
	case agentpb.StackKitOperation_STACKKIT_OPERATION_GENERATE:
		return stackKitGenerateArgs(command)
	case agentpb.StackKitOperation_STACKKIT_OPERATION_PREPARE_SOURCE_GENERATION:
		return []string{"kit", "prepare-source-generation", "--json"}, nil
	case agentpb.StackKitOperation_STACKKIT_OPERATION_PLAN:
		args := []string{"plan", "--json"}
		if command.SourceGenerationInspection {
			args = append(args, "--source-generation")
		}
		return args, nil
	case agentpb.StackKitOperation_STACKKIT_OPERATION_APPLY:
		return stackKitApplyArgs(command)
	case agentpb.StackKitOperation_STACKKIT_OPERATION_VERIFY:
		return stackKitVerifyArgs(command), nil
	case agentpb.StackKitOperation_STACKKIT_OPERATION_UPGRADE:
		return stackKitUpgradeArgs(command)
	case agentpb.StackKitOperation_STACKKIT_OPERATION_DRIFT_DETECT:
		return []string{"drift", "detect", "--json"}, nil
	case agentpb.StackKitOperation_STACKKIT_OPERATION_DRIFT_RECONCILE:
		// A Techstack-managed deployment reconciles only in Advanced Mode.
		return nil, fmt.Errorf("StackKit DRIFT_RECONCILE is not dispatched for a managed deployment; use ADVANCED_DRIFT_RECONCILE")
	case agentpb.StackKitOperation_STACKKIT_OPERATION_SERVICE_START,
		agentpb.StackKitOperation_STACKKIT_OPERATION_SERVICE_STOP,
		agentpb.StackKitOperation_STACKKIT_OPERATION_SERVICE_RESTART:
		return stackKitServiceMutationArgs(command)
	case agentpb.StackKitOperation_STACKKIT_OPERATION_SERVICE_LOGS:
		return stackKitServiceLogsArgs(command)
	case agentpb.StackKitOperation_STACKKIT_OPERATION_REMOVE:
		return stackKitRemoveArgs(command)
	case agentpb.StackKitOperation_STACKKIT_OPERATION_BACKUP_RUN:
		return stackKitBackupRunArgs(command)
	case agentpb.StackKitOperation_STACKKIT_OPERATION_BACKUP_STATUS:
		return []string{"backup", "status", "--json"}, nil
	case agentpb.StackKitOperation_STACKKIT_OPERATION_BACKUP_CONFIGURE:
		return []string{"backup", "configure", "--json"}, nil
	case agentpb.StackKitOperation_STACKKIT_OPERATION_BACKUP_RESTORE:
		return stackKitBackupRestoreArgs(command)
	case agentpb.StackKitOperation_STACKKIT_OPERATION_BACKUP_RESTORE_ACTIVATE,
		agentpb.StackKitOperation_STACKKIT_OPERATION_BACKUP_RESTORE_RECOVER:
		return stackKitRestoreActivationArgs(command)
	case agentpb.StackKitOperation_STACKKIT_OPERATION_BACKUP_TARGET_RECIPIENT:
		return stackKitBackupTargetRecipientArgs(command)
	case agentpb.StackKitOperation_STACKKIT_OPERATION_BACKUP_TARGET_IMPORT,
		agentpb.StackKitOperation_STACKKIT_OPERATION_BACKUP_TARGET_REBIND:
		return stackKitBackupTargetImportArgs(command)
	case agentpb.StackKitOperation_STACKKIT_OPERATION_HOST_DELIVERY_RECIPIENT,
		agentpb.StackKitOperation_STACKKIT_OPERATION_WALLET_ITEM_IMPORT,
		agentpb.StackKitOperation_STACKKIT_OPERATION_AI_PROVIDER_SETUP,
		agentpb.StackKitOperation_STACKKIT_OPERATION_HOST_SSH_APPLY:
		return stackkitcommand.WalletItemDeliveryArgs(command)
	case agentpb.StackKitOperation_STACKKIT_OPERATION_RELOCATE_PLAN,
		agentpb.StackKitOperation_STACKKIT_OPERATION_RELOCATE_CHECK,
		agentpb.StackKitOperation_STACKKIT_OPERATION_RELOCATE_DRAIN,
		agentpb.StackKitOperation_STACKKIT_OPERATION_RELOCATE_UNDRAIN,
		agentpb.StackKitOperation_STACKKIT_OPERATION_RELOCATE_SEND,
		agentpb.StackKitOperation_STACKKIT_OPERATION_RELOCATE_RECEIVE,
		agentpb.StackKitOperation_STACKKIT_OPERATION_RELOCATE_STATUS, agentpb.StackKitOperation_STACKKIT_OPERATION_RELOCATE_ABORT:
		return stackkitcommand.WorkloadRelocationArgs(command)
	case agentpb.StackKitOperation_STACKKIT_OPERATION_NETWORK_REBIND:
		if !command.OwnerApproved {
			return nil, fmt.Errorf("StackKit network rebind requires Owner approval")
		}
		return []string{"network", "rebind", "--yes", "--json"}, nil
	case agentpb.StackKitOperation_STACKKIT_OPERATION_NETWORK_STATUS:
		return []string{"network", "status", "--json"}, nil
	case agentpb.StackKitOperation_STACKKIT_OPERATION_OWNER_ACTIVATION_STATUS:
		return []string{"user", "owner", "status", "--json"}, nil
	case agentpb.StackKitOperation_STACKKIT_OPERATION_OWNER_ACTIVATION_ISSUE:
		return []string{"user", "owner", "activate", "--owner-approve", "--json"}, nil
	case agentpb.StackKitOperation_STACKKIT_OPERATION_HOUSEHOLD_LIST:
		return []string{"user", "list", "--json"}, nil
	case agentpb.StackKitOperation_STACKKIT_OPERATION_HOUSEHOLD_PROJECT:
		if err := stackkitcommand.ValidateCommand(command); err != nil {
			return nil, err
		}
		args := []string{"user", "project", command.HouseholdUsername,
			"--projection-key", command.HouseholdProjectionKey, "--revision", strconv.FormatUint(command.HouseholdProjectionRevision, 10),
			"--action", command.HouseholdProjectionAction, "--owner-approve", "--json"}
		if command.HouseholdProjectionAction == "add" {
			args = append(args, "--email", command.HouseholdEmail, "--display-name", command.HouseholdDisplayName)
			switch {
			case !command.HouseholdAppsSet:
				args = append(args, "--all")
			case len(command.HouseholdApps) == 0:
				args = append(args, "--none")
			default:
				for _, app := range command.HouseholdApps {
					args = append(args, "--app", app)
				}
			}
		}
		return args, nil
	case agentpb.StackKitOperation_STACKKIT_OPERATION_ADVANCED_TRUST_IMPORT:
		return stackKitAdvancedTrustImportArgs(command)
	case agentpb.StackKitOperation_STACKKIT_OPERATION_HOUSEHOLD_INVITE:
		args := []string{"user", "add", command.HouseholdUsername, "--email", command.HouseholdEmail}
		if command.HouseholdDisplayName != "" {
			args = append(args, "--display-name", command.HouseholdDisplayName)
		}
		for _, app := range command.HouseholdApps {
			args = append(args, "--app", app)
		}
		return append(args, "--owner-approve", "--json"), nil
	case agentpb.StackKitOperation_STACKKIT_OPERATION_HOUSEHOLD_UPDATE_APPS:
		if !command.OwnerApproved || strings.TrimSpace(command.HouseholdUsername) == "" {
			return nil, fmt.Errorf("StackKit household app update requires Owner approval and a username")
		}
		args := []string{"user", "apps", command.HouseholdUsername}
		switch {
		case !command.HouseholdAppsSet:
			args = append(args, "--all")
		case len(command.HouseholdApps) == 0:
			args = append(args, "--none")
		default:
			for _, app := range command.HouseholdApps {
				args = append(args, "--app", app)
			}
		}
		return append(args, "--owner-approve", "--json"), nil
	case agentpb.StackKitOperation_STACKKIT_OPERATION_HOUSEHOLD_REMOVE:
		if !command.OwnerApproved || strings.TrimSpace(command.HouseholdUsername) == "" {
			return nil, fmt.Errorf("StackKit household removal requires Owner approval and a username")
		}
		return []string{"user", "remove", command.HouseholdUsername, "--owner-approve", "--json"}, nil
	default:
		return nil, fmt.Errorf("unsupported StackKit operation %s", command.Operation.String())
	}
}

// stackKitAdvancedTrustImportArgs pins the exact bundle bytes Core sent; the
// argv carries no issuer or capability semantics of its own.
func stackKitAdvancedTrustImportArgs(command *agentpb.StackKitCommand) ([]string, error) {
	bundle := command.AdvancedTrustBundle
	if !command.OwnerApproved || len(bundle) == 0 || len(bundle) > stackkitcommand.MaxAdvancedTrustBundleBytes {
		return nil, fmt.Errorf("StackKit advanced trust import requires Owner approval and a bounded trust bundle")
	}
	digest := sha256.Sum256(bundle)
	return []string{
		"advanced", "trust", "import",
		"--bundle", stackKitAdvancedTrustBundlePath,
		"--expect-sha256", "sha256:" + hex.EncodeToString(digest[:]),
		"--owner-approve", "--json",
	}, nil
}

func materializeStackKitAdvancedTrustBundle(workDir string, bundle []byte) error {
	directory := filepath.Join(workDir, ".stackkit")
	if err := requireStackKitStateDir(workDir); err != nil {
		return err
	}
	if err := os.MkdirAll(directory, 0o750); err != nil {
		return fmt.Errorf("create StackKits workspace directory: %w", err)
	}
	tmp, err := os.CreateTemp(directory, ".advanced-trust-*.json")
	if err != nil {
		return fmt.Errorf("create temporary Advanced trust bundle: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if _, err := tmp.Write(bundle); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write Advanced trust bundle: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close Advanced trust bundle: %w", err)
	}
	if err := replaceStackKitInventory(tmpName, filepath.Join(workDir, filepath.FromSlash(stackKitAdvancedTrustBundlePath))); err != nil {
		return fmt.Errorf("publish Advanced trust bundle: %w", err)
	}
	return nil
}

func identityMutationCarriesTransientURL(command *agentpb.StackKitCommand) bool {
	if command == nil || !command.OwnerApproved {
		return false
	}
	return command.Operation == agentpb.StackKitOperation_STACKKIT_OPERATION_OWNER_ACTIVATION_ISSUE ||
		command.Operation == agentpb.StackKitOperation_STACKKIT_OPERATION_HOUSEHOLD_INVITE ||
		(command.Operation == agentpb.StackKitOperation_STACKKIT_OPERATION_HOUSEHOLD_PROJECT && command.HouseholdProjectionAction == "add")
}

func stackKitInitArgs(command *agentpb.StackKitCommand) ([]string, error) {
	kit := strings.TrimSpace(command.Stackkit)
	if kit != "basement-kit" && kit != "cloud-kit" && kit != "modern-homelab" {
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
	if err := stackkitcommand.ValidateInitCandidate(command.CandidateSpecJson, kit, name); err != nil {
		return nil, err
	}
	args := []string{"init", kit, "--candidate-spec=-", "--owner-source=local", "--non-interactive"}
	if expectedHash != "" {
		args = append(args, "--expected-spec-hash", expectedHash)
	}
	// A fresh node has no Owner custody yet, and StackKits refuses a local
	// Owner without a real email. A node that already holds custody keeps its
	// established identity when the email is omitted.
	if email := command.OwnerEmail; email != "" {
		if !stackkitcommand.ValidOwnerEmail(email) {
			return nil, fmt.Errorf("StackKit init owner_email is invalid")
		}
		args = append(args, "--owner-email", email)
	}
	return args, nil
}

func stackKitAddressBindArgs(command *agentpb.StackKitCommand) ([]string, error) {
	prefix := strings.TrimSpace(command.AddressPrefix)
	if !stackKitAddressPrefixPattern.MatchString(prefix) {
		return nil, fmt.Errorf("StackKit address prefix is invalid")
	}
	boundSpecPath, err := cleanStackKitRelativePath(command.BoundSpecPath, "", "bound_spec_path")
	if err != nil || boundSpecPath == "" {
		return nil, fmt.Errorf("StackKit bound_spec_path is invalid")
	}
	return []string{"address", "bind", "--prefix", prefix, "--output", filepath.ToSlash(boundSpecPath)}, nil
}

func stackKitGenerateArgs(command *agentpb.StackKitCommand) ([]string, error) {
	output, err := cleanStackKitRelativePath(command.OutputDirectory, "deploy", "output_directory")
	if err != nil {
		return nil, err
	}
	return []string{"generate", "--output", filepath.ToSlash(output)}, nil
}

func stackKitApplyArgs(command *agentpb.StackKitCommand) ([]string, error) {
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
		"--json",
		"--expected-plan-hash", expectedPlanHash,
		"--local-site", siteRef,
		"--local-node", nodeRef,
		"--local-execution-channel", channelRef,
	}, nil
}

func stackKitVerifyArgs(command *agentpb.StackKitCommand) []string {
	args := []string{"verify", "--json"}
	if command.Offline {
		args = append(args, "--offline")
	}
	return args
}

func stackKitUpgradeArgs(command *agentpb.StackKitCommand) ([]string, error) {
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
}

func stackKitServiceMutationArgs(command *agentpb.StackKitCommand) ([]string, error) {
	if !command.OwnerApproved {
		return nil, fmt.Errorf("StackKit service mutation requires explicit Owner approval")
	}
	serviceKey := strings.TrimSpace(command.ServiceKey)
	if !stackKitServiceKeyPattern.MatchString(serviceKey) {
		return nil, fmt.Errorf("StackKit service_key is invalid")
	}
	action := strings.TrimPrefix(strings.ToLower(command.Operation.String()), "stackkit_operation_service_")
	return []string{"service", action, serviceKey, "--json", "--owner-approve"}, nil
}

func stackKitServiceLogsArgs(command *agentpb.StackKitCommand) ([]string, error) {
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
}

func stackKitRemoveArgs(command *agentpb.StackKitCommand) ([]string, error) {
	if !command.OwnerApproved {
		return nil, fmt.Errorf("StackKit remove requires explicit Owner approval")
	}
	workloadRef := strings.TrimSpace(command.WorkloadRef)
	if workloadRef == "" || workloadRef != strings.ToLower(workloadRef) {
		return nil, fmt.Errorf("StackKit remove requires one canonical workload_ref")
	}
	siteRef := strings.TrimSpace(command.LocalSiteRef)
	nodeRef := strings.TrimSpace(command.LocalNodeRef)
	channelRef := strings.TrimSpace(command.LocalExecutionChannelRef)
	if siteRef == "" || nodeRef == "" || channelRef == "" {
		return nil, fmt.Errorf("StackKit remove requires local Site, node, and execution-channel references")
	}
	return []string{
		"remove", "--auto-approve", "--terminal-evidence-json", "--workload", workloadRef,
		"--local-site", siteRef, "--local-node", nodeRef, "--local-execution-channel", channelRef,
	}, nil
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

// confineStackKitWorkDir admits a command's working_directory only when it is
// the node's StackKits workspace or a real directory below it. The executor
// runs as root, so a control-plane command must not be able to point it at an
// arbitrary directory, and no component below the workspace may be a symlink
// that could redirect the later .stackkit writes elsewhere. The workspace root
// itself is operator configuration (TECHSTACK_STACKKIT_WORKSPACE, default
// /opt/stackkit) and is trusted as given.
func confineStackKitWorkDir(path string) (string, error) {
	absolute, err := cleanStackKitWorkDir(path)
	if err != nil {
		return "", err
	}
	root, err := filepath.Abs(filepath.Clean(stackKitWorkspaceRoot()))
	if err != nil {
		return "", fmt.Errorf("resolve StackKits workspace root: %w", err)
	}
	relative, err := filepath.Rel(root, absolute)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("StackKit working_directory must be inside the StackKits workspace")
	}
	current := root
	for _, part := range strings.Split(relative, string(filepath.Separator)) {
		if part == "." {
			continue
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			return "", fmt.Errorf("inspect StackKit working_directory: %w", err)
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("StackKit working_directory must not pass through a symlink or non-directory")
		}
	}
	return current, nil
}

// requireStackKitStateDir refuses a .stackkit entry that is a symlink or a
// non-directory, so the root-owned inventory and trust-bundle writes stay in
// the confined workspace. A missing entry is created by the caller.
func requireStackKitStateDir(workDir string) error {
	info, err := os.Lstat(filepath.Join(workDir, ".stackkit"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect StackKits state directory: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("StackKits state directory must be a real directory")
	}
	return nil
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
	defer func() { attestPinnedCLILocalEvidence(result, command) }()
	commandName := ""
	if command != nil {
		commandName = stackkitcommand.ResultCommandName(command.Operation)
	}
	typedStatus, typedResult := stackKitCommandResultStatus(stdout, commandName)
	if typedResult && typedStatus != "success" && runErr == nil {
		runErr = errors.New("StackKit command-result reported failure after a successful process exit")
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
	if typedResult && (typedStatus == "success") == result.Success {
		result.CommandResultJson = append([]byte(nil), stdout...)
		return result
	}
	if typedResult && !result.Success {
		// Apply can emit a valid partial ledger before its process exits nonzero.
		// Normalize the transport status without hiding that ledger in output text.
		var typedEnvelope map[string]json.RawMessage
		if json.Unmarshal(stdout, &typedEnvelope) == nil {
			typedEnvelope["status"] = json.RawMessage(`"failed"`)
			if encoded, encodeErr := json.Marshal(typedEnvelope); encodeErr == nil {
				result.CommandResultJson = encoded
				return result
			}
		}
	}
	status := "success"
	if runErr != nil {
		status = "failed"
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

// attestPinnedCLILocalEvidence records what this process can prove: the exact
// pinned CLI completed an operation whose StackKits implementation verifies
// local Owner custody before returning its JSON evidence. The Agent binds that
// observation to the exact result bytes. It does not claim to possess or use
// the Owner public key itself.
func attestPinnedCLILocalEvidence(result *agentpb.StackKitResult, command *agentpb.StackKitCommand) {
	if result == nil {
		return
	}
	result.PinnedCliEvidenceVerified = false
	result.PinnedCliEvidenceSha256 = ""
	if command == nil || !result.Success || len(result.CommandResultJson) == 0 {
		return
	}
	if stackkitcommand.IsApplicationConnectorOperation(command.Operation) {
		digest := sha256.Sum256(result.CommandResultJson)
		proof := proto.Clone(result).(*agentpb.StackKitResult)
		proof.PinnedCliEvidenceVerified = true
		proof.PinnedCliEvidenceSha256 = "sha256:" + hex.EncodeToString(digest[:])
		if _, _, err := stackkitcommand.ParseApplicationConnectorEvidence(command, proof); err != nil {
			return
		}
		result.PinnedCliEvidenceVerified = true
		result.PinnedCliEvidenceSha256 = proof.PinnedCliEvidenceSha256
		return
	}
	var wantAPIVersion string
	if stackkitcommand.IsApplicationOperation(command.Operation) && command.Operation != agentpb.StackKitOperation_STACKKIT_OPERATION_APPLICATION_INSPECT {
		var envelope struct {
			Data struct {
				Result struct {
					APIVersion string `json:"apiVersion"`
				} `json:"result"`
			} `json:"data"`
		}
		if json.Unmarshal(result.CommandResultJson, &envelope) != nil {
			return
		}
		if command.Operation == agentpb.StackKitOperation_STACKKIT_OPERATION_APPLICATION_VERIFY {
			var verified struct {
				Data struct {
					APIVersion        string `json:"apiVersion"`
					SignatureVerified bool   `json:"signatureVerified"`
				} `json:"data"`
			}
			if json.Unmarshal(result.CommandResultJson, &verified) != nil || verified.Data.APIVersion != "stackkit.application-adoption-verification/v1" || !verified.Data.SignatureVerified {
				return
			}
		} else if envelope.Data.Result.APIVersion != "stackkit.application-adoption-result/v1" {
			return
		}
		// The published CLI verifies the signature against existing node-local
		// Owner custody before returning this receipt, as for backup evidence.
		digest := sha256.Sum256(result.CommandResultJson)
		result.PinnedCliEvidenceVerified = true
		result.PinnedCliEvidenceSha256 = "sha256:" + hex.EncodeToString(digest[:])
		return
	}
	switch command.Operation {
	case agentpb.StackKitOperation_STACKKIT_OPERATION_BACKUP_RUN:
		wantAPIVersion = "stackkit.local-backup-snapshot-anchor/v1"
	case agentpb.StackKitOperation_STACKKIT_OPERATION_BACKUP_RESTORE:
		wantAPIVersion = "stackkit.local-backup-restore-result/v1"
	case agentpb.StackKitOperation_STACKKIT_OPERATION_BACKUP_RESTORE_ACTIVATE,
		agentpb.StackKitOperation_STACKKIT_OPERATION_BACKUP_RESTORE_RECOVER:
		wantAPIVersion = "stackkit.restore-activation-result/v1"
	default:
		return
	}
	var envelope struct {
		SchemaVersion string `json:"schemaVersion"`
		Command       string `json:"command"`
		Status        string `json:"status"`
		Data          struct {
			APIVersion string `json:"apiVersion"`
		} `json:"data"`
	}
	if json.Unmarshal(result.CommandResultJson, &envelope) != nil ||
		envelope.SchemaVersion != stackKitCommandResultVersion ||
		envelope.Command != stackkitcommand.ResultCommandName(command.Operation) ||
		envelope.Status != "success" || envelope.Data.APIVersion != wantAPIVersion {
		return
	}
	digest := sha256.Sum256(result.CommandResultJson)
	result.PinnedCliEvidenceVerified = true
	result.PinnedCliEvidenceSha256 = "sha256:" + hex.EncodeToString(digest[:])
}

func stackKitCommandResultStatus(data []byte, commandName string) (string, bool) {
	if len(bytes.TrimSpace(data)) == 0 || !json.Valid(data) {
		return "", false
	}
	var identity struct {
		SchemaVersion string `json:"schemaVersion"`
		Command       string `json:"command"`
		Status        string `json:"status"`
	}
	if err := json.Unmarshal(data, &identity); err != nil {
		return "", false
	}
	valid := identity.SchemaVersion == stackKitCommandResultVersion &&
		identity.Command == commandName &&
		(identity.Status == "success" || identity.Status == "failed" || identity.Status == "denied")
	return identity.Status, valid
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
			!stackkitcommand.ValidRolloutStatus(identity.Status) {
			return nil, fmt.Errorf("StackKits event spool contains a malformed rollout event")
		}
		events = append(events, []byte(secrets.Redact(string(line))))
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan StackKits event spool: %w", err)
	}
	return events, nil
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

// nodeBackupBindingsKey is the Inventory section the node itself writes when
// `backup target import` issues its own backup-target binding. Core's Inventory
// carries observed facts and execution channels only, so replacing the node's
// Inventory must never drop the node-issued binding.
const nodeBackupBindingsKey = "externalBackupTargetBindings"

func preserveNodeIssuedBackupBinding(workDir string, incoming map[string]any) error {
	if _, carries := incoming[nodeBackupBindingsKey]; carries {
		return nil
	}
	path := filepath.Join(workDir, ".stackkit", "inventory.json")
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxStackKitInventoryBytes {
		return fmt.Errorf("inspect the node StackKits Inventory before replacing it")
	}
	raw, err := os.ReadFile(path) // #nosec G304 -- fixed node workspace Inventory path.
	if err != nil {
		return fmt.Errorf("read the node StackKits Inventory before replacing it: %w", err)
	}
	var existing map[string]any
	if err := json.Unmarshal(raw, &existing); err != nil {
		return fmt.Errorf("decode the node StackKits Inventory before replacing it: %w", err)
	}
	if bindings, ok := existing[nodeBackupBindingsKey]; ok {
		incoming[nodeBackupBindingsKey] = bindings
	}
	return nil
}
