package grpcserver

import (
	"fmt"
	"testing"
	"time"

	"github.com/pocketbase/pocketbase/core"
)

// TestCommandStore_RecordToCommand tests the recordToCommand helper.
func TestCommandStore_RecordToCommand(t *testing.T) {
	// Test nil record
	store := &CommandStore{}
	cmd := store.recordToCommand(nil)
	if cmd != nil {
		t.Error("Expected nil command for nil record")
	}
}

// TestAgentCommand_Fields tests that AgentCommand struct has expected fields.
func TestAgentCommand_Fields(t *testing.T) {
	cmd := &AgentCommand{
		ID:      "cmd-123",
		AgentID: "agent-456",
		Type:    "shell",
		Command: "echo hello",
		Args:    []string{"-n"},
		Environment: map[string]string{
			"KEY": "value",
		},
		WorkDir: "/tmp",
		Timeout: 30 * time.Second,
	}

	if cmd.ID != "cmd-123" {
		t.Errorf("Expected ID 'cmd-123', got %s", cmd.ID)
	}
	if cmd.AgentID != "agent-456" {
		t.Errorf("Expected AgentID 'agent-456', got %s", cmd.AgentID)
	}
	if cmd.Type != "shell" {
		t.Errorf("Expected Type 'shell', got %s", cmd.Type)
	}
	if cmd.Command != "echo hello" {
		t.Errorf("Expected Command 'echo hello', got %s", cmd.Command)
	}
	if len(cmd.Args) != 1 || cmd.Args[0] != "-n" {
		t.Errorf("Expected Args ['-n'], got %v", cmd.Args)
	}
	if cmd.Environment["KEY"] != "value" {
		t.Errorf("Expected Environment['KEY'] = 'value', got %s", cmd.Environment["KEY"])
	}
	if cmd.WorkDir != "/tmp" {
		t.Errorf("Expected WorkDir '/tmp', got %s", cmd.WorkDir)
	}
	if cmd.Timeout != 30*time.Second {
		t.Errorf("Expected Timeout 30s, got %v", cmd.Timeout)
	}
}

// TestCommandResult_Fields tests that CommandResult struct has expected fields.
func TestCommandResult_Fields(t *testing.T) {
	startTime := time.Now()
	finishTime := startTime.Add(5 * time.Second)

	result := &CommandResult{
		CommandID:  "cmd-123",
		AgentID:    "agent-456",
		ExitCode:   0,
		Stdout:     "output",
		Stderr:     "",
		StartedAt:  startTime,
		FinishedAt: finishTime,
		Error:      nil,
	}

	if result.CommandID != "cmd-123" {
		t.Errorf("Expected CommandID 'cmd-123', got %s", result.CommandID)
	}
	if result.ExitCode != 0 {
		t.Errorf("Expected ExitCode 0, got %d", result.ExitCode)
	}
	if result.Stdout != "output" {
		t.Errorf("Expected Stdout 'output', got %s", result.Stdout)
	}
	if result.FinishedAt.Sub(result.StartedAt) != 5*time.Second {
		t.Error("Duration calculation mismatch")
	}
}

// TestTruncateString tests the truncateString helper.
func TestTruncateString(t *testing.T) {
	tests := []struct {
		input    string
		maxLen   int
		expected string
	}{
		{"hello", 10, "hello"},
		{"hello", 5, "hello"},
		{"hello world", 8, "hello..."},
		{"abc", 3, "abc"},
		{"abcd", 3, "..."},
		{"", 5, ""},
	}

	for _, tt := range tests {
		result := truncateString(tt.input, tt.maxLen)
		if result != tt.expected {
			t.Errorf("truncateString(%q, %d) = %q, expected %q",
				tt.input, tt.maxLen, result, tt.expected)
		}
	}
}

// TestNewCommandStore_NilApp tests that NewCommandStore handles nil gracefully.
// Note: In production, app should never be nil, but we test defensive coding.
func TestNewCommandStore_Creation(t *testing.T) {
	// We can't test with nil app as it would panic
	// This test documents the expected behavior
	t.Log("CommandStore requires a valid core.App - see integration tests")
}

// TestCommandStore_PersistenceIntegration documents integration test requirements.
func TestCommandStore_PersistenceIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	// Integration tests require PocketBase instance
	// See pkg/grpcserver/integration_test.go for full integration tests
	t.Log("Full persistence tests require PocketBase - see integration_test.go")
}

// =============================================================================
// Sprint 9.6: Extended CommandStore Tests (TC-2)
// =============================================================================

// TestTruncateString_EdgeCases tests truncateString with edge cases
func TestTruncateString_EdgeCases(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		maxLen   int
		expected string
	}{
		{"exact length", "hello", 5, "hello"},
		{"one over", "hello!", 5, "he..."},
		{"way over", "hello world!", 5, "he..."},
		{"empty string", "", 10, ""},
		{"single char", "a", 1, "a"},
		{"unicode string", "héllo", 6, "héllo"},
		{"long string", "the quick brown fox jumps over the lazy dog", 20, "the quick brown f..."},
		{"newlines", "hello\nworld", 8, "hello..."},
		{"tabs", "hello\tworld", 8, "hello..."},
		{"max 4", "hello", 4, "h..."},
		{"max 5 truncate", "hello world", 5, "he..."},
		{"exactly 3", "abc", 3, "abc"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := truncateString(tt.input, tt.maxLen)
			if result != tt.expected {
				t.Errorf("truncateString(%q, %d) = %q, expected %q",
					tt.input, tt.maxLen, result, tt.expected)
			}
		})
	}
}

// TestCommandResult_WithError tests CommandResult with error
func TestCommandResult_WithError(t *testing.T) {
	testErr := fmt.Errorf("command failed: exit status 1")
	result := &CommandResult{
		CommandID:  "cmd-error-1",
		AgentID:    "agent-1",
		ExitCode:   1,
		Stdout:     "",
		Stderr:     "error output",
		StartedAt:  time.Now().Add(-5 * time.Second),
		FinishedAt: time.Now(),
		Error:      testErr,
	}

	if result.ExitCode != 1 {
		t.Errorf("Expected ExitCode 1, got %d", result.ExitCode)
	}
	if result.Error == nil {
		t.Error("Expected Error to be set")
	}
	if result.Stderr != "error output" {
		t.Errorf("Expected Stderr 'error output', got %s", result.Stderr)
	}
}

// TestCommandResult_ZeroExitCode tests successful command result
func TestCommandResult_ZeroExitCode(t *testing.T) {
	result := &CommandResult{
		CommandID:  "cmd-success-1",
		AgentID:    "agent-1",
		ExitCode:   0,
		Stdout:     "success output",
		Stderr:     "",
		StartedAt:  time.Now().Add(-1 * time.Second),
		FinishedAt: time.Now(),
		Error:      nil,
	}

	if result.ExitCode != 0 {
		t.Errorf("Expected ExitCode 0, got %d", result.ExitCode)
	}
	if result.Error != nil {
		t.Errorf("Expected nil Error, got %v", result.Error)
	}
}

func TestValidateCommandResultAgentRejectsMismatchedAgent(t *testing.T) {
	record := core.NewRecord(core.NewBaseCollection("agent_commands"))
	record.Set("agent_id", "agent-1")

	err := validateCommandResultAgent(record, &CommandResult{
		CommandID: "cmd-1",
		AgentID:   "agent-2",
	})
	if err == nil {
		t.Fatal("validateCommandResultAgent expected mismatch error")
	}
	if !contains(err.Error(), "expected agent-1") {
		t.Fatalf("validateCommandResultAgent error = %q, want expected agent text", err.Error())
	}
}

func TestValidateCommandResultAgentAcceptsMatchingAgent(t *testing.T) {
	record := core.NewRecord(core.NewBaseCollection("agent_commands"))
	record.Set("agent_id", "agent-1")

	if err := validateCommandResultAgent(record, &CommandResult{
		CommandID: "cmd-1",
		AgentID:   "agent-1",
	}); err != nil {
		t.Fatalf("validateCommandResultAgent returned error: %v", err)
	}
}

// TestAgentCommand_EmptyFields tests AgentCommand with empty/nil fields
func TestAgentCommand_EmptyFields(t *testing.T) {
	cmd := &AgentCommand{
		ID:          "",
		AgentID:     "",
		Type:        "",
		Command:     "",
		Args:        nil,
		Environment: nil,
		WorkDir:     "",
		Timeout:     0,
	}

	// All fields should be their zero values
	if cmd.ID != "" {
		t.Error("Expected empty ID")
	}
	if cmd.Args != nil {
		t.Error("Expected nil Args")
	}
	if cmd.Environment != nil {
		t.Error("Expected nil Environment")
	}
	if cmd.Timeout != 0 {
		t.Error("Expected zero Timeout")
	}
}

// TestAgentCommand_WithAllFields tests AgentCommand with all fields populated
func TestAgentCommand_WithAllFields(t *testing.T) {
	env := map[string]string{
		"PATH":   "/usr/bin",
		"HOME":   "/home/user",
		"CUSTOM": "value",
	}
	args := []string{"--flag", "value", "-v"}

	cmd := &AgentCommand{
		ID:          "cmd-full-123",
		AgentID:     "agent-full-456",
		Type:        "tofu",
		Command:     "tofu apply",
		Args:        args,
		Environment: env,
		WorkDir:     "/workspace",
		Timeout:     5 * time.Minute,
	}

	if len(cmd.Args) != 3 {
		t.Errorf("Expected 3 Args, got %d", len(cmd.Args))
	}
	if len(cmd.Environment) != 3 {
		t.Errorf("Expected 3 Environment vars, got %d", len(cmd.Environment))
	}
	if cmd.Timeout != 5*time.Minute {
		t.Errorf("Expected 5 minute timeout, got %v", cmd.Timeout)
	}
}

// TestAgentCommand_CommandTypes tests different command types
func TestAgentCommand_CommandTypes(t *testing.T) {
	types := []string{
		"shell",
		"tofu",
		"terramate",
		"docker",
		"service",
		"custom",
	}

	for _, cmdType := range types {
		t.Run(cmdType, func(t *testing.T) {
			cmd := &AgentCommand{
				ID:      "cmd-" + cmdType,
				AgentID: "agent-1",
				Type:    cmdType,
				Command: "test command",
				Timeout: 30 * time.Second,
			}

			if cmd.Type != cmdType {
				t.Errorf("Expected Type '%s', got '%s'", cmdType, cmd.Type)
			}
		})
	}
}

// TestCommandStore_RecordToCommand_NilHandling tests nil record handling
func TestCommandStore_RecordToCommand_NilHandling(t *testing.T) {
	store := &CommandStore{}

	// Test nil record returns nil
	result := store.recordToCommand(nil)
	if result != nil {
		t.Error("Expected nil result for nil record")
	}
}

// TestCommandResult_Duration tests duration calculation
func TestCommandResult_Duration(t *testing.T) {
	start := time.Date(2026, 1, 9, 10, 0, 0, 0, time.UTC)
	finish := time.Date(2026, 1, 9, 10, 0, 30, 0, time.UTC)

	result := &CommandResult{
		CommandID:  "cmd-duration",
		AgentID:    "agent-1",
		StartedAt:  start,
		FinishedAt: finish,
	}

	duration := result.FinishedAt.Sub(result.StartedAt)
	if duration != 30*time.Second {
		t.Errorf("Expected 30s duration, got %v", duration)
	}
}

// TestCommandResult_LargeOutput tests CommandResult with large output
func TestCommandResult_LargeOutput(t *testing.T) {
	// Create large output (100KB)
	largeOutput := make([]byte, 100*1024)
	for i := range largeOutput {
		largeOutput[i] = byte('x')
	}

	result := &CommandResult{
		CommandID: "cmd-large",
		AgentID:   "agent-1",
		Stdout:    string(largeOutput),
	}

	if len(result.Stdout) != 100*1024 {
		t.Errorf("Expected 100KB output, got %d bytes", len(result.Stdout))
	}

	// Test truncation
	truncated := truncateString(result.Stdout, 1000)
	if len(truncated) != 1000 {
		t.Errorf("Expected truncated to 1000 bytes, got %d", len(truncated))
	}
	if truncated[len(truncated)-3:] != "..." {
		t.Error("Expected truncated string to end with '...'")
	}
}
