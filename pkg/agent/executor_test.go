package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/api/agentpb"
)

// testLogger returns a discarding logger for tests.
func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// dockerAvailable checks if docker is available in PATH.
func dockerAvailable() bool {
	_, err := exec.LookPath("docker")
	return err == nil
}

// TestNewCommandExecutor tests the CommandExecutor constructor.
func TestNewCommandExecutor(t *testing.T) {
	tests := []struct {
		name      string
		config    ExecutorConfig
		wantErr   bool
		errSubstr string
	}{
		{name: "default config - uses home directory", config: ExecutorConfig{}, wantErr: false},
		{name: "custom work directory", config: ExecutorConfig{WorkDir: t.TempDir()}, wantErr: false},
		{name: "custom docker path - not found",
			config:  ExecutorConfig{WorkDir: t.TempDir(), DockerComposePath: "/nonexistent/docker"},
			wantErr: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ex, err := NewCommandExecutor(tt.config)
			if tt.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				} else if tt.errSubstr != "" && !strings.Contains(err.Error(), tt.errSubstr) {
					t.Errorf("err = %q, want containing %q", err.Error(), tt.errSubstr)
				}
				return
			}
			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}
			if ex == nil {
				t.Error("returned nil executor")
			}
		})
	}
}

// TestCommandType tests active CommandType constants.
func TestCommandType(t *testing.T) {
	tests := []struct {
		name     string
		cmdType  CommandType
		expected string
	}{
		{"get logs", CommandTypeGetLogs, "get_logs"},
		{"health check", CommandTypeHealthCheck, "health_check"},
		{"tofu", CommandTypeTofu, "tofu"},
		{"terramate", CommandTypeTerramate, "terramate"},
		{"pulumi operation", CommandTypePulumiOperation, "pulumi_operation"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if string(tt.cmdType) != tt.expected {
				t.Errorf("CommandType %s = %q, want %q", tt.name, tt.cmdType, tt.expected)
			}
		})
	}
}

// TestComposeCommand tests ComposeCommand JSON marshaling.
func TestComposeCommand(t *testing.T) {
	tests := []struct {
		name string
		cmd  ComposeCommand
	}{
		{"full", ComposeCommand{ProjectName: "p", ComposeContent: "v: '3'", ServiceName: "web", Lines: 50}},
		{"minimal", ComposeCommand{ProjectName: "minimal"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := json.Marshal(tt.cmd)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			var decoded ComposeCommand
			if err := json.Unmarshal(data, &decoded); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if decoded != tt.cmd {
				t.Errorf("round-trip mismatch: %+v vs %+v", decoded, tt.cmd)
			}
		})
	}
}

// TestExecutorConfig tests ExecutorConfig zero-value.
func TestExecutorConfig(t *testing.T) {
	cfg := ExecutorConfig{}
	if cfg.WorkDir != "" || cfg.DockerComposePath != "" || cfg.Logger != nil || cfg.DefaultTimeout != 0 {
		t.Errorf("zero-value ExecutorConfig has unexpected fields: %+v", cfg)
	}
}

// TestParseComposeCommand tests parseComposeCommand.
func TestParseComposeCommand(t *testing.T) {
	executor := &CommandExecutor{workDir: t.TempDir(), log: testLogger()}
	tests := []struct {
		name        string
		cmd         *agentpb.ExecuteCommand
		wantProject string
		wantService string
		wantContent string
		wantLines   int
	}{
		{"JSON args", &agentpb.ExecuteCommand{CommandId: "1", Args: []string{`{"project_name":"p","service_name":"web","lines":50}`}}, "p", "web", "", 50},
		{"positional", &agentpb.ExecuteCommand{CommandId: "2", Args: []string{"p", "s"}}, "p", "s", "", 0},
		{"env vars", &agentpb.ExecuteCommand{CommandId: "3", Environment: map[string]string{"PROJECT_NAME": "p", "SERVICE_NAME": "s", "COMPOSE_CONTENT": "v: '3'"}}, "p", "s", "v: '3'", 0},
		{"args override env", &agentpb.ExecuteCommand{CommandId: "4", Args: []string{"args-p"}, Environment: map[string]string{"PROJECT_NAME": "env-p"}}, "args-p", "", "", 0},
		{"empty", &agentpb.ExecuteCommand{CommandId: "5"}, "", "", "", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cc, err := executor.parseComposeCommand(tt.cmd)
			if err != nil {
				t.Fatalf("err: %v", err)
			}
			if cc.ProjectName != tt.wantProject {
				t.Errorf("ProjectName = %q, want %q", cc.ProjectName, tt.wantProject)
			}
			if cc.ServiceName != tt.wantService {
				t.Errorf("ServiceName = %q, want %q", cc.ServiceName, tt.wantService)
			}
			if cc.ComposeContent != tt.wantContent {
				t.Errorf("ComposeContent = %q, want %q", cc.ComposeContent, tt.wantContent)
			}
			if cc.Lines != tt.wantLines {
				t.Errorf("Lines = %d, want %d", cc.Lines, tt.wantLines)
			}
		})
	}
}

// TestIsWindows is a smoke test.
func TestIsWindows(t *testing.T) {
	got := isWindows()
	expected := os.PathSeparator == '\\' && os.PathListSeparator == ';'
	if got != expected {
		t.Errorf("isWindows() = %v, expected %v", got, expected)
	}
}

func TestAllowedCommandTypesIsTightPulumiOperationBoundary(t *testing.T) {
	want := map[CommandType]bool{
		CommandTypeHealthCheck:     true,
		CommandTypeGetLogs:         true,
		CommandTypeTofu:            true,
		CommandTypeTerramate:       true,
		CommandTypePulumiOperation: true,
	}

	if len(AllowedCommandTypes) != len(want) {
		t.Fatalf("AllowedCommandTypes has %d entries, want %d: %#v", len(AllowedCommandTypes), len(want), AllowedCommandTypes)
	}
	for cmd := range want {
		if !AllowedCommandTypes[cmd] {
			t.Fatalf("AllowedCommandTypes[%q] = false, want true", cmd)
		}
	}
	for _, raw := range []CommandType{"pulumi", "pulumi_command", "command", "execute"} {
		if AllowedCommandTypes[raw] {
			t.Fatalf("raw command type %q must not be allowlisted", raw)
		}
	}
}

// TestExecuteUnknownCommand tests rejection of unknown command types.
func TestExecuteUnknownCommand(t *testing.T) {
	executor := &CommandExecutor{workDir: t.TempDir(), defaultTimeout: DefaultCommandTimeout, log: testLogger()}
	cmd := &agentpb.ExecuteCommand{CommandId: "test-unknown", Command: "some_unknown", Args: []string{"a"}}
	result, err := executor.Execute(context.Background(), cmd)
	if err == nil {
		t.Error("expected error for unknown command type")
	}
	if result.ExitCode != 1 {
		t.Errorf("ExitCode = %d, want 1", result.ExitCode)
	}
	if !strings.Contains(result.Stderr, "Unknown command type") {
		t.Errorf("stderr = %q, want containing 'Unknown command type'", result.Stderr)
	}
}

// TestExecute_RoutesDiagnosticsAndRejectsBareIaC validates the Phase 7.2 allowlist.
func TestExecute_RoutesDiagnosticsAndRejectsBareIaC(t *testing.T) {
	executor := &CommandExecutor{workDir: t.TempDir(), defaultTimeout: 5 * time.Second, log: testLogger()}
	tests := []struct {
		name       string
		cmd        *agentpb.ExecuteCommand
		wantStderr string
	}{
		{"tofu routed via dedicated executor",
			&agentpb.ExecuteCommand{CommandId: "t1", Command: string(CommandTypeTofu)}, "dedicated executor"},
		{"terramate routed via dedicated executor",
			&agentpb.ExecuteCommand{CommandId: "t2", Command: string(CommandTypeTerramate)}, "dedicated executor"},
		{"pulumi operation routed via dedicated executor",
			&agentpb.ExecuteCommand{CommandId: "t5", Command: string(CommandTypePulumiOperation)}, "dedicated executor"},
		{"deploy_compose now unknown",
			&agentpb.ExecuteCommand{CommandId: "t3", Command: "deploy_compose"}, "Unknown command type"},
		{"execute now unknown",
			&agentpb.ExecuteCommand{CommandId: "t4", Command: "execute"}, "Unknown command type"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := executor.Execute(context.Background(), tt.cmd)
			if err == nil {
				t.Error("expected error, got nil")
			}
			if !strings.Contains(result.Stderr, tt.wantStderr) {
				t.Errorf("stderr = %q, want containing %q", result.Stderr, tt.wantStderr)
			}
		})
	}
}

// TestHandleGetLogsValidation tests handleGetLogs validation.
func TestHandleGetLogsValidation(t *testing.T) {
	executor := &CommandExecutor{workDir: t.TempDir(), log: testLogger()}
	_, _, exitCode, err := executor.handleGetLogs(context.Background(), &agentpb.ExecuteCommand{CommandId: "t1"})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "project_name is required") {
		t.Errorf("err = %q", err.Error())
	}
	if exitCode != 1 {
		t.Errorf("exitCode = %d, want 1", exitCode)
	}
}

// TestHandleHealthCheckValidation tests handleHealthCheck validation.
func TestHandleHealthCheckValidation(t *testing.T) {
	executor := &CommandExecutor{workDir: t.TempDir(), log: testLogger()}
	_, _, exitCode, err := executor.handleHealthCheck(context.Background(), &agentpb.ExecuteCommand{CommandId: "t1"})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "project_name is required") {
		t.Errorf("err = %q", err.Error())
	}
	if exitCode != 1 {
		t.Errorf("exitCode = %d, want 1", exitCode)
	}
}

// TestRunCommandEnvironment tests env var propagation.
func TestRunCommandEnvironment(t *testing.T) {
	workDir := t.TempDir()
	executor := &CommandExecutor{workDir: workDir, log: testLogger()}
	env := map[string]string{"TEST_VAR": "test_value"}
	var stdout string
	var exitCode int
	var err error
	if isWindows() {
		stdout, _, exitCode, err = executor.runCommand(context.Background(), "cmd", []string{"/C", "echo %TEST_VAR%"}, workDir, env)
	} else {
		stdout, _, exitCode, err = executor.runCommand(context.Background(), "sh", []string{"-c", "echo $TEST_VAR"}, workDir, env)
	}
	if err != nil {
		t.Errorf("err: %v", err)
	}
	if exitCode != 0 {
		t.Errorf("exitCode = %d", exitCode)
	}
	if !strings.Contains(stdout, "test_value") {
		t.Errorf("stdout = %q", stdout)
	}
}

// TestRunCommandNonZeroExit tests non-zero exit code handling.
func TestRunCommandNonZeroExit(t *testing.T) {
	workDir := t.TempDir()
	executor := &CommandExecutor{workDir: workDir, log: testLogger()}
	var cmd string
	var args []string
	if isWindows() {
		cmd = "cmd"
		args = []string{"/C", "exit 42"}
	} else {
		cmd = "sh"
		args = []string{"-c", "exit 42"}
	}
	_, _, exitCode, err := executor.runCommand(context.Background(), cmd, args, workDir, nil)
	if err != nil {
		t.Errorf("should not error on non-zero exit: %v", err)
	}
	if exitCode != 42 {
		t.Errorf("exitCode = %d, want 42", exitCode)
	}
}

// TestWorkDirAndProjectDir smoke-tests the path helpers retained in executor_state.go.
func TestWorkDirAndProjectDir(t *testing.T) {
	workDir := t.TempDir()
	executor := &CommandExecutor{workDir: workDir}
	if executor.WorkDir() != workDir {
		t.Errorf("WorkDir() = %q, want %q", executor.WorkDir(), workDir)
	}
	got := executor.ProjectDir("my-project")
	want := filepath.Join(workDir, "my-project")
	if got != want {
		t.Errorf("ProjectDir() = %q, want %q", got, want)
	}
}

// TestCheckHealthJSONParsing tests JSON parsing logic.
func TestCheckHealthJSONParsing(t *testing.T) {
	tests := []struct {
		name     string
		jsonLine string
		wantKey  string
		wantVal  string
	}{
		{"basic", `{"Name":"web-1","Service":"web","State":"running","Health":""}`, "web", "running"},
		{"healthy", `{"Name":"db-1","Service":"db","State":"running","Health":"healthy"}`, "db", "running (healthy)"},
		{"unhealthy", `{"Name":"app-1","Service":"app","State":"running","Health":"unhealthy"}`, "app", "running (unhealthy)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var c struct {
				Name    string `json:"Name"`
				Service string `json:"Service"`
				State   string `json:"State"`
				Health  string `json:"Health"`
			}
			if err := json.Unmarshal([]byte(tt.jsonLine), &c); err != nil {
				t.Fatalf("Unmarshal: %v", err)
			}
			status := c.State
			if c.Health != "" {
				status = fmt.Sprintf("%s (%s)", status, c.Health)
			}
			serviceName := c.Service
			if serviceName == "" {
				serviceName = c.Name
			}
			if serviceName != tt.wantKey {
				t.Errorf("name = %q, want %q", serviceName, tt.wantKey)
			}
			if status != tt.wantVal {
				t.Errorf("status = %q, want %q", status, tt.wantVal)
			}
		})
	}
}
