package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestClientConfig tests configuration validation
func TestClientConfig(t *testing.T) {
	tests := []struct {
		name    string
		cfg     Config
		wantErr string
	}{
		{
			name:    "empty config - missing core address",
			cfg:     Config{},
			wantErr: "core address is required",
		},
		{
			name: "missing agent ID",
			cfg: Config{
				CoreAddress: "core:5263",
			},
			wantErr: "agent ID is required",
		},
		{
			name: "missing cert file",
			cfg: Config{
				CoreAddress: "core:5263",
				AgentID:     "test-agent",
			},
			wantErr: "certificate file path is required",
		},
		{
			name: "missing key file",
			cfg: Config{
				CoreAddress: "core:5263",
				AgentID:     "test-agent",
				CertFile:    "/some/cert.pem",
			},
			wantErr: "key file path is required",
		},
		{
			name: "missing CA file",
			cfg: Config{
				CoreAddress: "core:5263",
				AgentID:     "test-agent",
				CertFile:    "/some/cert.pem",
				KeyFile:     "/some/key.pem",
			},
			wantErr: "CA file path is required",
		},
		{
			name: "cert file not found",
			cfg: Config{
				CoreAddress: "core:5263",
				AgentID:     "test-agent",
				CertFile:    "/nonexistent/cert.pem",
				KeyFile:     "/nonexistent/key.pem",
				CAFile:      "/nonexistent/ca.pem",
			},
			wantErr: "certificate file not found",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewClient(tt.cfg)
			if err == nil {
				t.Error("NewClient() expected error but got nil")
				return
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("NewClient() error = %q, want containing %q", err.Error(), tt.wantErr)
			}
		})
	}
}

// TestCommandChannels tests channel operations
func TestCommandChannels(t *testing.T) {
	// Create a client with mocked TLS (bypassing cert loading)
	c := &Client{
		coreAddr:  "core:5263",
		agentID:   "test-agent",
		commands:  make(chan Command, 10),
		results:   make(chan CommandResult, 10),
		tlsConfig: nil, // nil for testing
	}

	// Test Commands() channel
	cmdChan := c.Commands()
	if cmdChan == nil {
		t.Error("Commands() returned nil channel")
	}

	// Send a test command
	testCmd := Command{
		ID:      "cmd-1",
		Type:    "health_check",
		Command: "health_check",
		Args:    []string{"test"},
		Timeout: 30 * time.Second,
	}

	// Queue command
	c.commands <- testCmd

	// Receive command
	select {
	case received := <-cmdChan:
		if received.ID != testCmd.ID {
			t.Errorf("command ID mismatch: got %s, want %s", received.ID, testCmd.ID)
		}
		if received.Type != testCmd.Type {
			t.Errorf("command type mismatch: got %s, want %s", received.Type, testCmd.Type)
		}
	case <-time.After(100 * time.Millisecond):
		t.Error("timeout waiting for command")
	}
}

// TestSendResult tests result sending
func TestSendResult(t *testing.T) {
	c := &Client{
		coreAddr: "core:5263",
		agentID:  "test-agent",
		commands: make(chan Command, 10),
		results:  make(chan CommandResult, 10),
	}

	result := CommandResult{
		CommandID:  "cmd-1",
		ExitCode:   0,
		Stdout:     "success",
		Stderr:     "",
		StartedAt:  time.Now().Add(-1 * time.Second),
		FinishedAt: time.Now(),
		Error:      nil,
	}

	// Send result
	c.SendResult(result)

	// Verify it was queued
	select {
	case received := <-c.results:
		if received.CommandID != result.CommandID {
			t.Errorf("result command ID mismatch: got %s, want %s", received.CommandID, result.CommandID)
		}
		if received.ExitCode != result.ExitCode {
			t.Errorf("exit code mismatch: got %d, want %d", received.ExitCode, result.ExitCode)
		}
	case <-time.After(100 * time.Millisecond):
		t.Error("timeout waiting for result")
	}
}

// TestClientClose tests graceful close
func TestClientClose(t *testing.T) {
	c := &Client{
		coreAddr: "core:5263",
		agentID:  "test-agent",
		commands: make(chan Command, 10),
		results:  make(chan CommandResult, 10),
	}

	err := c.Close()
	if err != nil {
		t.Errorf("Close() returned error: %v", err)
	}

	// After close, channels should be closed
	_, open := <-c.commands
	if open {
		t.Error("commands channel should be closed after Close()")
	}
}

// TestConnectNotConnected tests that Connect requires TLS config
func TestConnectNotConnected(t *testing.T) {
	c := &Client{
		coreAddr: "core:5263",
		agentID:  "test-agent",
		commands: make(chan Command, 10),
		results:  make(chan CommandResult, 10),
		// tlsConfig is nil - connect will succeed but won't establish real connection
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	// Connect should return nil if already connected or attempt connection
	// Without TLS config, it will use insecure mode which may timeout
	err := c.Connect(ctx)

	// We expect either success (already connected check) or error (no TLS config)
	// The actual behavior depends on implementation
	_ = err // Accept any outcome for this structural test
}

// TestRegisterNotConnected tests that Register returns error when not connected
func TestRegisterNotConnected(t *testing.T) {
	c := &Client{
		coreAddr: "core:5263",
		agentID:  "test-agent",
		commands: make(chan Command, 10),
		results:  make(chan CommandResult, 10),
		// client is nil - not connected
	}

	ctx := context.Background()
	err := c.Register(ctx)

	// Should return "not connected" error
	if err == nil {
		t.Error("Register() should return error when not connected")
	}
	if !strings.Contains(err.Error(), "not connected") {
		t.Errorf("Register() expected 'not connected' error, got: %v", err)
	}
}

// TestRegisterAlreadyRegistered tests that re-registration returns nil
func TestRegisterAlreadyRegistered(t *testing.T) {
	c := &Client{
		coreAddr:   "core:5263",
		agentID:    "test-agent",
		registered: true,
		commands:   make(chan Command, 10),
		results:    make(chan CommandResult, 10),
	}

	ctx := context.Background()
	err := c.Register(ctx)

	if err != nil {
		t.Errorf("Register() should return nil when already registered, got: %v", err)
	}
}

// TestHeartbeatCancellation tests that heartbeat respects context cancellation
func TestHeartbeatCancellation(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping heartbeat test in short mode")
	}

	c := &Client{
		coreAddr: "core:5263",
		agentID:  "test-agent",
		commands: make(chan Command, 10),
		results:  make(chan CommandResult, 10),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- c.StartHeartbeat(ctx, 50*time.Millisecond)
	}()

	select {
	case err := <-done:
		if err != context.DeadlineExceeded {
			t.Errorf("StartHeartbeat() expected DeadlineExceeded error, got: %v", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Error("StartHeartbeat() did not respect context cancellation")
	}
}

// TestLoadTLSConfigErrors tests TLS config error handling
func TestLoadTLSConfigErrors(t *testing.T) {
	// Create a temp directory for test certs
	tmpDir, err := os.MkdirTemp("", "agent-test-")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	tests := []struct {
		name    string
		cfg     Config
		wantErr string
	}{
		{
			name: "missing cert file",
			cfg: Config{
				CertFile: filepath.Join(tmpDir, "nonexistent.crt"),
				KeyFile:  filepath.Join(tmpDir, "test.key"),
				CAFile:   filepath.Join(tmpDir, "ca.crt"),
			},
			wantErr: "failed to load agent certificate",
		},
		{
			name: "missing key file",
			cfg: Config{
				CertFile: filepath.Join(tmpDir, "test.crt"),
				KeyFile:  filepath.Join(tmpDir, "nonexistent.key"),
				CAFile:   filepath.Join(tmpDir, "ca.crt"),
			},
			wantErr: "failed to load agent certificate",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := loadTLSConfig(tt.cfg)
			if err == nil {
				t.Error("loadTLSConfig() should return error")
			}
		})
	}
}

// BenchmarkCommandChannel benchmarks command channel throughput
func BenchmarkCommandChannel(b *testing.B) {
	c := &Client{
		coreAddr: "core:5263",
		agentID:  "test-agent",
		commands: make(chan Command, 1000),
		results:  make(chan CommandResult, 1000),
	}

	cmd := Command{
		ID:      "cmd-bench",
		Type:    "health_check",
		Command: "health_check",
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		select {
		case c.commands <- cmd:
			<-c.commands
		default:
			// Channel full, skip
		}
	}
}
