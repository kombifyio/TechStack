package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	agentpkg "github.com/kombifyio/techstack/pkg/agent"
	"github.com/kombifyio/techstack/pkg/api/agentpb"
	"github.com/kombifyio/techstack/pkg/runtimeconvergence"
)

type fakeCurrentStackKitRuntime struct {
	ensureErr    error
	ensureCalls  int
	executeCalls int
}

type fakeRuntimePackageConverger struct {
	techstackResult agentpkg.TechstackRuntimeConvergenceResult
	techstackErr    error
	stackKitErr     error
	techstackCalls  int
	stackKitCalls   int
}

func (runtime *fakeRuntimePackageConverger) EnsureTechstackRuntime(context.Context, agentpkg.TechstackRuntimeConvergenceConfig) (agentpkg.TechstackRuntimeConvergenceResult, error) {
	runtime.techstackCalls++
	return runtime.techstackResult, runtime.techstackErr
}

func (runtime *fakeRuntimePackageConverger) EnsureRuntime(context.Context, agentpkg.StackKitRuntimeBootstrapConfig) error {
	runtime.stackKitCalls++
	return runtime.stackKitErr
}

func (runtime *fakeCurrentStackKitRuntime) EnsureRuntime(context.Context, agentpkg.StackKitRuntimeBootstrapConfig) error {
	runtime.ensureCalls++
	return runtime.ensureErr
}

func (runtime *fakeCurrentStackKitRuntime) Execute(_ context.Context, command *agentpb.StackKitCommand) *agentpb.StackKitResult {
	runtime.executeCalls++
	return &agentpb.StackKitResult{CommandId: command.GetCommandId(), Success: true}
}

func (runtime *fakeCurrentStackKitRuntime) FailureResult(command *agentpb.StackKitCommand, err error) *agentpb.StackKitResult {
	return &agentpb.StackKitResult{CommandId: command.GetCommandId(), ExitCode: 1, Stderr: err.Error()}
}

func TestCurrentStackKitCommandExecutorConvergesBeforeEveryCommand(t *testing.T) {
	runtime := &fakeCurrentStackKitRuntime{}
	executor := currentStackKitCommandExecutor{runtime: runtime}
	command := &agentpb.StackKitCommand{CommandId: "command-1"}

	result := executor.Execute(t.Context(), command)
	if !result.Success || runtime.ensureCalls != 1 || runtime.executeCalls != 1 {
		t.Fatalf("result=%+v ensure=%d execute=%d", result, runtime.ensureCalls, runtime.executeCalls)
	}
}

func TestCurrentStackKitCommandExecutorFailsClosedBeforeExecution(t *testing.T) {
	runtime := &fakeCurrentStackKitRuntime{ensureErr: errors.New("release unavailable")}
	executor := currentStackKitCommandExecutor{runtime: runtime}
	command := &agentpb.StackKitCommand{CommandId: "command-2"}

	result := executor.Execute(t.Context(), command)
	if result.Success || result.ExitCode != 1 || runtime.ensureCalls != 1 || runtime.executeCalls != 0 {
		t.Fatalf("result=%+v ensure=%d execute=%d", result, runtime.ensureCalls, runtime.executeCalls)
	}
	if !strings.Contains(result.Stderr, "converge current StackKits release before command") {
		t.Fatalf("stderr = %q", result.Stderr)
	}
}

func TestCurrentStackKitCommandExecutorWaitsForRuntimeConvergence(t *testing.T) {
	runtime := &fakeCurrentStackKitRuntime{}
	executor := currentStackKitCommandExecutor{runtime: runtime, ready: func() bool { return false }}
	result := executor.Execute(t.Context(), &agentpb.StackKitCommand{CommandId: "command-pending"})
	if result.Success || runtime.ensureCalls != 0 || runtime.executeCalls != 0 || !strings.Contains(result.Stderr, "runtime convergence is pending") {
		t.Fatalf("result=%+v ensure=%d execute=%d", result, runtime.ensureCalls, runtime.executeCalls)
	}
}

func TestRuntimeConvergenceKeepsGuardAliveWhenPackagesAreUnavailable(t *testing.T) {
	runtime := &fakeRuntimePackageConverger{techstackErr: errors.New("binary unavailable"), stackKitErr: errors.New("release unavailable")}
	stopCalls := 0
	keepRunning, ready := convergeRuntimePackagesOnce(
		t.Context(), func() { stopCalls++ }, runtime,
		agentpkg.TechstackRuntimeConvergenceConfig{}, agentpkg.StackKitRuntimeBootstrapConfig{},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
	if !keepRunning || ready || stopCalls != 0 || runtime.techstackCalls != 1 || runtime.stackKitCalls != 1 {
		t.Fatalf("keepRunning=%t ready=%t stop=%d techstack=%d stackkit=%d", keepRunning, ready, stopCalls, runtime.techstackCalls, runtime.stackKitCalls)
	}
}

func TestRuntimeConvergencePublishesStableComponentErrors(t *testing.T) {
	runtime := &fakeRuntimePackageConverger{techstackErr: errors.New("dial tcp 10.0.0.4:443: connection refused"), stackKitErr: errors.New("provider response contained a secret")}
	status := convergeRuntimePackagesOnceStatus(
		t.Context(), func() {}, runtime,
		agentpkg.TechstackRuntimeConvergenceConfig{}, agentpkg.StackKitRuntimeBootstrapConfig{},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
	if status.snapshot.State != runtimeconvergence.StateDegraded {
		t.Fatalf("convergence state = %#v", status.snapshot)
	}
	if status.snapshot.ErrorCode != runtimeconvergence.ConvergenceIncompleteError {
		t.Fatalf("aggregate error code = %q", status.snapshot.ErrorCode)
	}
	for _, component := range status.snapshot.Components {
		if component.State != runtimeconvergence.ComponentFailed {
			t.Fatalf("component = %#v", component)
		}
		if component.ErrorCode == "" || strings.Contains(component.ErrorCode, "dial tcp") || strings.Contains(component.ErrorCode, "secret") {
			t.Fatalf("unstable/raw component error code = %#v", component)
		}
	}
}

func TestRuntimeConvergenceEnablesCommandsAfterBothPackagesAreReady(t *testing.T) {
	runtime := &fakeRuntimePackageConverger{}
	keepRunning, ready := convergeRuntimePackagesOnce(
		t.Context(), func() {}, runtime,
		agentpkg.TechstackRuntimeConvergenceConfig{}, agentpkg.StackKitRuntimeBootstrapConfig{},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
	if !keepRunning || !ready || runtime.techstackCalls != 1 || runtime.stackKitCalls != 1 {
		t.Fatalf("keepRunning=%t ready=%t techstack=%d stackkit=%d", keepRunning, ready, runtime.techstackCalls, runtime.stackKitCalls)
	}
}

func TestRuntimeConvergenceStopsOnlyAfterAgentUpdate(t *testing.T) {
	runtime := &fakeRuntimePackageConverger{techstackResult: agentpkg.TechstackRuntimeConvergenceResult{AgentUpdated: true, SHA256: "abc"}}
	stopCalls := 0
	keepRunning, ready := convergeRuntimePackagesOnce(
		t.Context(), func() { stopCalls++ }, runtime,
		agentpkg.TechstackRuntimeConvergenceConfig{}, agentpkg.StackKitRuntimeBootstrapConfig{},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
	if keepRunning || ready || stopCalls != 1 || runtime.techstackCalls != 1 || runtime.stackKitCalls != 0 {
		t.Fatalf("keepRunning=%t ready=%t stop=%d techstack=%d stackkit=%d", keepRunning, ready, stopCalls, runtime.techstackCalls, runtime.stackKitCalls)
	}
}

func TestParseHTTPSAgentModeDoesNotRequireGRPCCertificates(t *testing.T) {
	path := writeEnrollmentFixture(t, 0o600)
	t.Setenv("TECHSTACK_AGENT_TRANSPORT", "https")
	t.Setenv("TECHSTACK_AGENT_ENROLLMENT_FILE", path)
	t.Setenv("TECHSTACK_AGENT_CORE_ADDR", "")
	t.Setenv("TECHSTACK_AGENT_CERT_FILE", "")
	t.Setenv("TECHSTACK_AGENT_KEY_FILE", "")
	t.Setenv("TECHSTACK_AGENT_CA_FILE", "")

	cfg, err := parseAgentModeConfig(nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.transport != "https" || cfg.enrollmentFile != path {
		t.Fatalf("config = %#v", cfg)
	}
	enrollment, err := resolveHTTPAgentEnrollment(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if enrollment.RuntimeAgentID != "runtime-1" || enrollment.AgentToken != "secret-token" {
		t.Fatalf("enrollment identity/token was not loaded")
	}
	if enrollment.HeartbeatURL != "https://techstack.example/api/v1/workers/runtime-1/heartbeat" {
		t.Fatalf("heartbeat URL = %q", enrollment.HeartbeatURL)
	}
	if enrollment.CommandURL != "https://techstack.example/api/v1/workers/runtime-1/commands/next" ||
		enrollment.CommandResultURL != "https://techstack.example/api/v1/workers/runtime-1/commands/result" {
		t.Fatalf("derived typed control URLs = %q, %q", enrollment.CommandURL, enrollment.CommandResultURL)
	}
}

func TestHTTPSAgentEnrollmentRejectsGroupReadableSecretFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows ACLs are not represented by POSIX permission bits")
	}
	path := writeEnrollmentFixture(t, 0o644)
	_, err := loadAgentEnrollment(path)
	if err == nil || !strings.Contains(err.Error(), "group/world") {
		t.Fatalf("loadAgentEnrollment error = %v", err)
	}
}

func TestHTTPSAgentModeAcceptsExistingManagedConnectHandoffEnvironment(t *testing.T) {
	t.Setenv("TECHSTACK_AGENT_TRANSPORT", "https")
	t.Setenv("TECHSTACK_RUNTIME_AGENT_ID", "runtime-managed")
	t.Setenv("TECHSTACK_SERVER_ID", "server-managed")
	t.Setenv("TECHSTACK_TENANT_ID", "tenant-managed")
	t.Setenv("TECHSTACK_OWNER_ID", "owner-managed")
	t.Setenv("TECHSTACK_STACK_ID", "stack-managed")
	t.Setenv("TECHSTACK_LEASE_ID", "lease-managed")
	t.Setenv("TECHSTACK_AGENT_TOKEN", "managed-token")
	t.Setenv("TECHSTACK_HEARTBEAT_URL", "https://techstack.example/api/v1/workers/runtime-managed/heartbeat")
	t.Setenv("TECHSTACK_INVENTORY_URL", "https://techstack.example/api/v1/workers/runtime-managed/inventory")

	cfg, err := parseAgentModeConfig(nil)
	if err != nil {
		t.Fatal(err)
	}
	enrollment, err := resolveHTTPAgentEnrollment(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if enrollment.RuntimeAgentID != "runtime-managed" || enrollment.TenantID != "tenant-managed" || enrollment.StackID != "stack-managed" {
		t.Fatalf("managed handoff = %#v", enrollment)
	}
	if enrollment.AgentToken != "managed-token" || enrollment.LeaseID != "lease-managed" {
		t.Fatal("managed handoff credential/lease was not consumed")
	}
}

func writeEnrollmentFixture(t *testing.T, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "agent-enrollment.json")
	body := `{"success":true,"data":{"worker_id":"runtime-1","server_id":"server-1","runtime_agent_id":"runtime-1","agent_token":"secret-token","heartbeat_url":"https://techstack.example/api/v1/workers/runtime-1/heartbeat","inventory_url":"https://techstack.example/api/v1/workers/runtime-1/inventory","channel_bootstrap":{"tenant_id":"tenant-1","owner_id":"owner-1","stack_id":"stack-1"}}}`
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
	return path
}
