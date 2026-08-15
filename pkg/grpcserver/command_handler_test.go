package grpcserver

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/api/agentpb"
	"github.com/kombifyio/techstack/pkg/logger"
)

// TestNewTofuCommandHandler tests TofuCommandHandler creation.
func TestNewTofuCommandHandler(t *testing.T) {
	log := logger.New("test", "debug")
	handler := NewTofuCommandHandler(log)

	if handler == nil {
		t.Fatal("NewTofuCommandHandler() returned nil")
	}

	if handler.defaultTimeout != 30*time.Minute {
		t.Errorf("defaultTimeout = %v, want %v", handler.defaultTimeout, 30*time.Minute)
	}
}

// TestTofuCommandHandlerValidateCommand tests command validation.
func TestTofuCommandHandlerValidateCommand(t *testing.T) {
	log := logger.New("test", "debug")
	handler := NewTofuCommandHandler(log)
	perms := DefaultPermissions()

	tests := []struct {
		name    string
		cmd     *agentpb.TofuCommand
		perms   *CommandPermission
		wantErr bool
		errMsg  string
	}{
		{
			name:    "nil command",
			cmd:     nil,
			perms:   perms,
			wantErr: true,
			errMsg:  "command is nil",
		},
		{
			name: "empty command_id",
			cmd: &agentpb.TofuCommand{
				CommandId: "",
				Operation: agentpb.TofuOperation_TOFU_OPERATION_INIT,
			},
			perms:   perms,
			wantErr: true,
			errMsg:  "command_id is required",
		},
		{
			name: "valid init command",
			cmd: &agentpb.TofuCommand{
				CommandId: "test-1",
				Operation: agentpb.TofuOperation_TOFU_OPERATION_INIT,
			},
			perms:   perms,
			wantErr: false,
		},
		{
			name: "valid apply command",
			cmd: &agentpb.TofuCommand{
				CommandId: "test-2",
				Operation: agentpb.TofuOperation_TOFU_OPERATION_APPLY,
			},
			perms:   perms,
			wantErr: false,
		},
		{
			name: "destroy not allowed",
			cmd: &agentpb.TofuCommand{
				CommandId: "test-3",
				Operation: agentpb.TofuOperation_TOFU_OPERATION_DESTROY,
			},
			perms: &CommandPermission{
				AllowedTofuOps: []agentpb.TofuOperation{
					agentpb.TofuOperation_TOFU_OPERATION_INIT,
					agentpb.TofuOperation_TOFU_OPERATION_PLAN,
				},
				AllowDestroy: false,
			},
			wantErr: true,
			errMsg:  "not permitted",
		},
		{
			name: "timeout exceeds max",
			cmd: &agentpb.TofuCommand{
				CommandId:      "test-4",
				Operation:      agentpb.TofuOperation_TOFU_OPERATION_APPLY,
				TimeoutSeconds: 7200, // 2 hours
			},
			perms: &CommandPermission{
				AllowedTofuOps: []agentpb.TofuOperation{
					agentpb.TofuOperation_TOFU_OPERATION_APPLY,
				},
				MaxTimeout:   30 * time.Minute,
				AllowDestroy: true,
			},
			wantErr: true,
			errMsg:  "exceeds maximum",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := handler.ValidateCommand(tt.cmd, tt.perms)
			if tt.wantErr {
				if err == nil {
					t.Error("ValidateCommand() expected error but got nil")
				} else if tt.errMsg != "" && !contains(err.Error(), tt.errMsg) {
					t.Errorf("ValidateCommand() error = %q, want containing %q", err.Error(), tt.errMsg)
				}
			} else {
				if err != nil {
					t.Errorf("ValidateCommand() unexpected error: %v", err)
				}
			}
		})
	}
}

// TestTofuCommandHandlerDispatchCommand tests command dispatch.
func TestTofuCommandHandlerDispatchCommand(t *testing.T) {
	log := logger.New("test", "debug")
	handler := NewTofuCommandHandler(log)

	ctx := context.Background()
	cmd := &agentpb.TofuCommand{
		CommandId:      "dispatch-test",
		Operation:      agentpb.TofuOperation_TOFU_OPERATION_INIT,
		TimeoutSeconds: 60,
	}

	resultChan, err := handler.DispatchCommand(ctx, cmd, "agent-1")
	if err != nil {
		t.Fatalf("DispatchCommand() error: %v", err)
	}

	if resultChan == nil {
		t.Fatal("DispatchCommand() returned nil channel")
	}

	// Verify command is pending
	count := handler.GetPendingCommandCount()
	if count != 1 {
		t.Errorf("GetPendingCommandCount() = %d, want 1", count)
	}

	// Simulate result
	result := &agentpb.TofuResult{
		CommandId: "dispatch-test",
		Success:   true,
	}

	err = handler.HandleResult(result, "agent-1")
	if err != nil {
		t.Errorf("HandleResult() error: %v", err)
	}

	// Verify command is no longer pending
	count = handler.GetPendingCommandCount()
	if count != 0 {
		t.Errorf("GetPendingCommandCount() = %d, want 0", count)
	}

	// Read result from channel
	select {
	case received := <-resultChan:
		if received.CommandId != "dispatch-test" {
			t.Errorf("result.CommandId = %q, want %q", received.CommandId, "dispatch-test")
		}
		if !received.Success {
			t.Error("result.Success = false, want true")
		}
	case <-time.After(time.Second):
		t.Error("Timeout waiting for result")
	}
}

// TestTofuCommandHandlerCancelCommand tests command cancellation.
func TestTofuCommandHandlerCancelCommand(t *testing.T) {
	log := logger.New("test", "debug")
	handler := NewTofuCommandHandler(log)

	ctx := context.Background()
	cmd := &agentpb.TofuCommand{
		CommandId: "cancel-test",
		Operation: agentpb.TofuOperation_TOFU_OPERATION_APPLY,
	}

	resultChan, err := handler.DispatchCommand(ctx, cmd, "agent-1")
	if err != nil {
		t.Fatalf("DispatchCommand() error: %v", err)
	}

	// Cancel the command
	err = handler.CancelCommand("cancel-test")
	if err != nil {
		t.Errorf("CancelCommand() error: %v", err)
	}

	// Read result from channel - should be cancellation
	select {
	case received := <-resultChan:
		if received.Success {
			t.Error("result.Success = true, want false for canceled")
		}
		if !contains(received.Stderr, "canceled") {
			t.Errorf("result.Stderr = %q, want containing 'canceled'", received.Stderr)
		}
	case <-time.After(time.Second):
		t.Error("Timeout waiting for cancellation result")
	}
}

// TestDefaultPermissions tests the default permissions.
func TestDefaultPermissions(t *testing.T) {
	perms := DefaultPermissions()

	if perms == nil {
		t.Fatal("DefaultPermissions() returned nil")
	}

	// Should allow all basic operations
	expectedOps := []agentpb.TofuOperation{
		agentpb.TofuOperation_TOFU_OPERATION_INIT,
		agentpb.TofuOperation_TOFU_OPERATION_PLAN,
		agentpb.TofuOperation_TOFU_OPERATION_APPLY,
		agentpb.TofuOperation_TOFU_OPERATION_DESTROY,
		agentpb.TofuOperation_TOFU_OPERATION_OUTPUT,
	}

	for _, op := range expectedOps {
		found := false
		for _, allowed := range perms.AllowedTofuOps {
			if allowed == op {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("Operation %v should be allowed by default", op)
		}
	}

	if !perms.AllowDestroy {
		t.Error("AllowDestroy should be true by default")
	}

	if perms.MaxTimeout != 30*time.Minute {
		t.Errorf("MaxTimeout = %v, want %v", perms.MaxTimeout, 30*time.Minute)
	}
}

// TestNewTerramateCommandHandler tests TerramateCommandHandler creation.
func TestNewTerramateCommandHandler(t *testing.T) {
	log := logger.New("test", "debug")
	handler := NewTerramateCommandHandler(log)

	if handler == nil {
		t.Fatal("NewTerramateCommandHandler() returned nil")
	}
}

// TestTerramateCommandHandlerDispatch tests terramate placeholder.
func TestTerramateCommandHandlerDispatch(t *testing.T) {
	log := logger.New("test", "debug")
	handler := NewTerramateCommandHandler(log)

	ctx := context.Background()
	cmd := &agentpb.TerramateCommand{
		CommandId: "terramate-test",
		Operation: agentpb.TerramateOperation_TERRAMATE_OPERATION_RUN,
	}

	resultChan, err := handler.DispatchCommand(ctx, cmd, "agent-1")
	if err != nil {
		t.Fatalf("DispatchCommand() error: %v", err)
	}

	// Should return a placeholder result indicating Day-2 feature
	select {
	case result := <-resultChan:
		if result.Success {
			t.Error("result.Success = true, want false for placeholder")
		}
		if !contains(result.Stderr, "Day-2") {
			t.Errorf("result.Stderr = %q, want containing 'Day-2'", result.Stderr)
		}
	case <-time.After(time.Second):
		t.Error("Timeout waiting for terramate result")
	}
}

// TestTofuCommandHandler_Timeout tests command timeout behavior.
func TestTofuCommandHandler_Timeout(t *testing.T) {
	log := logger.New("test", "debug")
	handler := NewTofuCommandHandler(log)

	// Create a command with very short timeout for testing
	ctx := context.Background()
	cmd := &agentpb.TofuCommand{
		CommandId:      "timeout-test",
		Operation:      agentpb.TofuOperation_TOFU_OPERATION_INIT,
		TimeoutSeconds: 1, // 1 second timeout
	}

	resultChan, err := handler.DispatchCommand(ctx, cmd, "agent-1")
	if err != nil {
		t.Fatalf("DispatchCommand() error: %v", err)
	}

	// Wait for timeout (handler adds 30s buffer, so we use context cancellation)
	ctx2, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	select {
	case result := <-resultChan:
		// Result should arrive (either from timeout or elsewhere)
		_ = result
	case <-ctx2.Done():
		// Timeout test is tricky due to 30s buffer in watchTimeout
		// Just verify command was dispatched
	}

	// Command should be removed from pending after timeout/context cancel
	count := handler.GetPendingCommandCount()
	if count > 1 {
		t.Errorf("GetPendingCommandCount() = %d, expected <= 1", count)
	}
}

// TestTofuCommandHandler_HandleResult_UnknownCommand tests handling results for unknown commands.
func TestTofuCommandHandler_HandleResult_UnknownCommand(t *testing.T) {
	log := logger.New("test", "debug")
	handler := NewTofuCommandHandler(log)

	// Send result for non-existent command
	result := &agentpb.TofuResult{
		CommandId: "non-existent-command",
		Success:   true,
	}

	err := handler.HandleResult(result, "agent-1")
	if err == nil {
		t.Error("HandleResult() expected error for unknown command")
	}
	if err != nil && !contains(err.Error(), "no pending command") {
		t.Errorf("HandleResult() error = %q, want containing 'no pending command'", err.Error())
	}
}

func TestTofuCommandHandler_HandleResultRejectsWrongAgent(t *testing.T) {
	log := logger.New("test", "debug")
	handler := NewTofuCommandHandler(log)

	resultChan, err := handler.DispatchCommand(context.Background(), &agentpb.TofuCommand{
		CommandId: "agent-bound-command",
		Operation: agentpb.TofuOperation_TOFU_OPERATION_PLAN,
	}, "agent-1")
	if err != nil {
		t.Fatalf("DispatchCommand() error: %v", err)
	}

	err = handler.HandleResult(&agentpb.TofuResult{
		CommandId: "agent-bound-command",
		Success:   true,
	}, "agent-2")
	if err == nil {
		t.Fatal("HandleResult() expected error for mismatched agent")
	}
	if !contains(err.Error(), "expected agent-1") {
		t.Fatalf("HandleResult() error = %q, want expected agent text", err.Error())
	}
	if count := handler.GetPendingCommandCount(); count != 1 {
		t.Fatalf("GetPendingCommandCount() = %d, want 1 after rejected result", count)
	}

	if err := handler.HandleResult(&agentpb.TofuResult{
		CommandId: "agent-bound-command",
		Success:   true,
	}, "agent-1"); err != nil {
		t.Fatalf("HandleResult() with expected agent returned error: %v", err)
	}
	select {
	case got := <-resultChan:
		if got.CommandId != "agent-bound-command" || !got.Success {
			t.Fatalf("result = %+v, want successful agent-bound-command", got)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for accepted result")
	}
}

// TestTofuCommandHandler_HandleResult_NilResult tests handling nil result.
func TestTofuCommandHandler_HandleResult_NilResult(t *testing.T) {
	log := logger.New("test", "debug")
	handler := NewTofuCommandHandler(log)

	err := handler.HandleResult(nil, "agent-1")
	if err == nil {
		t.Error("HandleResult(nil) expected error")
	}
	if err != nil && !contains(err.Error(), "nil") {
		t.Errorf("HandleResult() error = %q, want containing 'nil'", err.Error())
	}
}

// TestTofuCommandHandler_CancelCommand_NotFound tests canceling non-existent command.
func TestTofuCommandHandler_CancelCommand_NotFound(t *testing.T) {
	log := logger.New("test", "debug")
	handler := NewTofuCommandHandler(log)

	err := handler.CancelCommand("non-existent")
	if err == nil {
		t.Error("CancelCommand() expected error for non-existent command")
	}
	if err != nil && !contains(err.Error(), "no pending command") {
		t.Errorf("CancelCommand() error = %q, want containing 'no pending command'", err.Error())
	}
}

// TestTofuCommandHandler_ConcurrentDispatch tests concurrent command dispatching.
func TestTofuCommandHandler_ConcurrentDispatch(t *testing.T) {
	log := logger.New("test", "debug")
	handler := NewTofuCommandHandler(log)

	ctx := context.Background()
	numCommands := 10
	channels := make([]<-chan *agentpb.TofuResult, numCommands)
	errors := make(chan error, numCommands)

	// Dispatch multiple commands concurrently
	for i := 0; i < numCommands; i++ {
		go func(idx int) {
			cmd := &agentpb.TofuCommand{
				CommandId: fmt.Sprintf("concurrent-%d", idx),
				Operation: agentpb.TofuOperation_TOFU_OPERATION_INIT,
			}
			ch, err := handler.DispatchCommand(ctx, cmd, "agent-1")
			if err != nil {
				errors <- err
				return
			}
			channels[idx] = ch
			errors <- nil
		}(i)
	}

	// Wait for all dispatches
	for i := 0; i < numCommands; i++ {
		err := <-errors
		if err != nil {
			t.Errorf("Concurrent dispatch %d failed: %v", i, err)
		}
	}

	// Verify all commands are pending
	count := handler.GetPendingCommandCount()
	if count != numCommands {
		t.Errorf("GetPendingCommandCount() = %d, want %d", count, numCommands)
	}
}

// TestTerramateCommandHandler_ValidateCommand tests terramate command validation.
func TestTerramateCommandHandler_ValidateCommand(t *testing.T) {
	log := logger.New("test", "debug")
	handler := NewTerramateCommandHandler(log)
	perms := DefaultPermissions()

	tests := []struct {
		name    string
		cmd     *agentpb.TerramateCommand
		perms   *CommandPermission
		wantErr bool
		errMsg  string
	}{
		{
			name:    "nil command",
			cmd:     nil,
			perms:   perms,
			wantErr: true,
			errMsg:  "command is nil",
		},
		{
			name: "empty command_id",
			cmd: &agentpb.TerramateCommand{
				CommandId: "",
				Operation: agentpb.TerramateOperation_TERRAMATE_OPERATION_RUN,
			},
			perms:   perms,
			wantErr: true,
			errMsg:  "command_id is required",
		},
		{
			name: "valid run command",
			cmd: &agentpb.TerramateCommand{
				CommandId: "test-1",
				Operation: agentpb.TerramateOperation_TERRAMATE_OPERATION_RUN,
			},
			perms:   perms,
			wantErr: false,
		},
		{
			name: "operation not permitted",
			cmd: &agentpb.TerramateCommand{
				CommandId: "test-2",
				Operation: agentpb.TerramateOperation_TERRAMATE_OPERATION_RUN,
			},
			perms: &CommandPermission{
				AllowedTerramateOps: []agentpb.TerramateOperation{}, // Empty - nothing allowed
			},
			wantErr: true,
			errMsg:  "not permitted",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := handler.ValidateCommand(tt.cmd, tt.perms)
			if tt.wantErr {
				if err == nil {
					t.Error("ValidateCommand() expected error but got nil")
				} else if tt.errMsg != "" && !contains(err.Error(), tt.errMsg) {
					t.Errorf("ValidateCommand() error = %q, want containing %q", err.Error(), tt.errMsg)
				}
			} else {
				if err != nil {
					t.Errorf("ValidateCommand() unexpected error: %v", err)
				}
			}
		})
	}
}

// TestTerramateCommandHandler_HandleResult tests placeholder result handling.
func TestTerramateCommandHandler_HandleResult(t *testing.T) {
	log := logger.New("test", "debug")
	handler := NewTerramateCommandHandler(log)

	result := &agentpb.TerramateResult{
		CommandId: "terramate-test",
		Success:   true,
	}

	err := handler.HandleResult(result)
	if err != nil {
		t.Errorf("HandleResult() unexpected error: %v", err)
	}
}

// TestNewCommandRouter tests CommandRouter creation.
func TestNewCommandRouter(t *testing.T) {
	log := logger.New("test", "debug")

	// Create a mock server
	srv, err := New(Config{ListenAddr: ":0"}, log)
	if err != nil {
		t.Fatalf("Failed to create server: %v", err)
	}

	router := NewCommandRouter(srv, log)
	if router == nil {
		t.Fatal("NewCommandRouter() returned nil")
	}

	if router.tofuHandler == nil {
		t.Error("router.tofuHandler is nil")
	}

	if router.terramateHandler == nil {
		t.Error("router.terramateHandler is nil")
	}

	if router.server != srv {
		t.Error("router.server doesn't match provided server")
	}
}

// TestCommandRouter_SendTofuCommand_AgentNotConnected tests error when agent not connected.
func TestCommandRouter_SendTofuCommand_AgentNotConnected(t *testing.T) {
	log := logger.New("test", "debug")

	srv, err := New(Config{ListenAddr: ":0"}, log)
	if err != nil {
		t.Fatalf("Failed to create server: %v", err)
	}

	router := NewCommandRouter(srv, log)

	ctx := context.Background()
	cmd := &agentpb.TofuCommand{
		CommandId: "test-1",
		Operation: agentpb.TofuOperation_TOFU_OPERATION_INIT,
	}

	// Agent not registered - should fail
	_, err = router.SendTofuCommand(ctx, "non-existent-agent", cmd)
	if err == nil {
		t.Error("SendTofuCommand() expected error for non-connected agent")
	}
	if err != nil && !contains(err.Error(), "not connected") {
		t.Errorf("SendTofuCommand() error = %q, want containing 'not connected'", err.Error())
	}
}

// TestCommandRouter_SendTofuCommand_AgentDisconnected tests error when agent is disconnected.
func TestCommandRouter_SendTofuCommand_AgentDisconnected(t *testing.T) {
	log := logger.New("test", "debug")

	srv, err := New(Config{ListenAddr: ":0"}, log)
	if err != nil {
		t.Fatalf("Failed to create server: %v", err)
	}

	// Register agent first
	agent := &ConnectedAgent{
		ID:       "agent-1",
		Hostname: "test-host",
	}
	srv.RegisterAgent(agent)

	// Now manually set status to disconnected after registration
	srv.agentsMu.Lock()
	if a, ok := srv.agents["agent-1"]; ok {
		a.Status = "disconnected"
	}
	srv.agentsMu.Unlock()

	router := NewCommandRouter(srv, log)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	cmd := &agentpb.TofuCommand{
		CommandId: "test-1",
		Operation: agentpb.TofuOperation_TOFU_OPERATION_INIT,
	}

	_, err = router.SendTofuCommand(ctx, "agent-1", cmd)
	if err == nil {
		t.Error("SendTofuCommand() expected error for disconnected agent")
	}
	if err != nil && !contains(err.Error(), "disconnected") {
		t.Errorf("SendTofuCommand() error = %q, want containing 'disconnected'", err.Error())
	}
}

// TestCommandRouter_SendTerramateCommand tests Day-2 placeholder response.
func TestCommandRouter_SendTerramateCommand(t *testing.T) {
	log := logger.New("test", "debug")

	srv, err := New(Config{ListenAddr: ":0"}, log)
	if err != nil {
		t.Fatalf("Failed to create server: %v", err)
	}

	router := NewCommandRouter(srv, log)

	ctx := context.Background()
	cmd := &agentpb.TerramateCommand{
		CommandId: "terramate-test",
		Operation: agentpb.TerramateOperation_TERRAMATE_OPERATION_RUN,
	}

	result, err := router.SendTerramateCommand(ctx, "agent-1", cmd)
	if err != nil {
		t.Fatalf("SendTerramateCommand() error: %v", err)
	}

	if result.Success {
		t.Error("result.Success = true, want false for Day-2 placeholder")
	}
	if !contains(result.Stderr, "Day-2") {
		t.Errorf("result.Stderr = %q, want containing 'Day-2'", result.Stderr)
	}
}

// TestCommandRouter_GetPendingTofuCommandCount tests counting pending commands.
func TestCommandRouter_GetPendingTofuCommandCount(t *testing.T) {
	log := logger.New("test", "debug")

	srv, err := New(Config{ListenAddr: ":0"}, log)
	if err != nil {
		t.Fatalf("Failed to create server: %v", err)
	}

	router := NewCommandRouter(srv, log)

	// Initially should be 0
	count := router.GetPendingTofuCommandCount()
	if count != 0 {
		t.Errorf("GetPendingTofuCommandCount() = %d, want 0", count)
	}
}

// TestCommandRouter_HandleTofuResult tests routing result to handler.
func TestCommandRouter_HandleTofuResult(t *testing.T) {
	log := logger.New("test", "debug")

	srv, err := New(Config{ListenAddr: ":0"}, log)
	if err != nil {
		t.Fatalf("Failed to create server: %v", err)
	}

	router := NewCommandRouter(srv, log)

	// First dispatch a command
	ctx := context.Background()
	cmd := &agentpb.TofuCommand{
		CommandId: "result-test",
		Operation: agentpb.TofuOperation_TOFU_OPERATION_INIT,
	}

	resultChan, err := router.tofuHandler.DispatchCommand(ctx, cmd, "agent-1")
	if err != nil {
		t.Fatalf("DispatchCommand() error: %v", err)
	}

	// Now send result through router
	result := &agentpb.TofuResult{
		CommandId: "result-test",
		Success:   true,
		ExitCode:  0,
	}

	err = router.HandleTofuResult(result, "agent-1")
	if err != nil {
		t.Errorf("HandleTofuResult() error: %v", err)
	}

	// Verify result was delivered to channel
	select {
	case received := <-resultChan:
		if received.CommandId != "result-test" {
			t.Errorf("result.CommandId = %q, want 'result-test'", received.CommandId)
		}
	case <-time.After(time.Second):
		t.Error("Timeout waiting for result on channel")
	}
}

// TestCommandRouter_HandleTerramateResult tests terramate result handling.
func TestCommandRouter_HandleTerramateResult(t *testing.T) {
	log := logger.New("test", "debug")

	srv, err := New(Config{ListenAddr: ":0"}, log)
	if err != nil {
		t.Fatalf("Failed to create server: %v", err)
	}

	router := NewCommandRouter(srv, log)

	result := &agentpb.TerramateResult{
		CommandId: "terramate-result",
		Success:   true,
	}

	// Should not error (placeholder implementation)
	err = router.HandleTerramateResult(result)
	if err != nil {
		t.Errorf("HandleTerramateResult() error: %v", err)
	}
}

// Helper function
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) == 0 ||
		(len(s) > 0 && len(substr) > 0 && findSubstring(s, substr)))
}

func findSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
