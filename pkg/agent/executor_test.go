package agent

import (
	"context"
	"errors"
	"io"
	"log/slog"
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

// Regression (security finding 5, 2026-10-19): caller-supplied compose names
// reached filepath.Join(workDir, project), `-p` and the docker argv unchecked,
// so a project could leave workDir and a name could inject a docker flag.
func TestComposeDiagnosticsRejectUnsafeNames(t *testing.T) {
	executor := &CommandExecutor{workDir: t.TempDir(), log: testLogger()}
	unsafe := []*agentpb.ExecuteCommand{
		{Args: []string{`{"project_name":"../../etc"}`}},
		{Args: []string{`{"project_name":"-H=tcp://evil:2375"}`}},
		{Args: []string{`{"project_name":"app","service_name":"--privileged"}`}},
		{Args: []string{"../outside"}},
		{Args: []string{"app", "-f/etc/passwd"}},
		{Args: []string{"/abs/path"}},
		{Environment: map[string]string{"PROJECT_NAME": ".."}},
		{Args: []string{`{"project_name":"app","lines":-1}`}},
	}
	for _, cmd := range unsafe {
		if cc, err := executor.parseComposeCommand(cmd); err == nil {
			t.Errorf("parseComposeCommand(%v %v) = %+v, want rejection", cmd.Args, cmd.Environment, cc)
		}
	}
	for _, name := range []string{"..", "../x", "a/b", "-p", ""} {
		if dir, err := executor.composeProjectDir(name); err == nil {
			t.Errorf("composeProjectDir(%q) = %q, want rejection", name, dir)
		}
	}
	dir, err := executor.composeProjectDir("my-stack_1")
	if err != nil {
		t.Fatalf("composeProjectDir(valid) error: %v", err)
	}
	if rel, _ := filepath.Rel(executor.workDir, dir); rel != "my-stack_1" {
		t.Fatalf("composeProjectDir(valid) = %q, want a child of %q", dir, executor.workDir)
	}
}

func TestExecuteEnforcesDiagnosticCommandBoundary(t *testing.T) {
	executor := &CommandExecutor{workDir: t.TempDir(), defaultTimeout: 5 * time.Second, log: testLogger()}
	tests := []struct {
		name     string
		command  string
		admitted bool
	}{
		{"health check admitted", "health_check", true},
		{"logs admitted", "get_logs", true},
		{"tofu retired", "tofu", false},
		{"terramate retired", "terramate", false},
		{"pulumi operation retired", "pulumi_operation", false},
		{"deploy compose retired", "deploy_compose", false},
		{"bare execute retired", "execute", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := executor.Execute(context.Background(), &agentpb.ExecuteCommand{
				CommandId: tt.name,
				Command:   tt.command,
			})
			if err == nil {
				t.Error("expected error, got nil")
			}
			if rejected := errors.Is(err, errUnknownCommandType); rejected == tt.admitted {
				t.Fatalf("admitted = %t, rejection cause = %v", tt.admitted, err)
			}
			if result.ExitCode != 1 {
				t.Fatalf("exit code = %d, want 1", result.ExitCode)
			}
		})
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
