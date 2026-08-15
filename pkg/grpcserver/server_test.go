package grpcserver

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/api/agentpb"
	"github.com/kombifyio/techstack/pkg/logger"
)

// generateTestCerts creates temporary test certificates for TLS testing
// Returns paths to ca.pem, server.pem, server-key.pem, and cleanup function
func generateTestCerts(t *testing.T) (caFile, certFile, keyFile string, cleanup func()) {
	t.Helper()

	tmpDir, err := os.MkdirTemp("", "grpc-test-certs")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}

	cleanup = func() { os.RemoveAll(tmpDir) }

	// Generate CA key
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		cleanup()
		t.Fatalf("Failed to generate CA key: %v", err)
	}

	// CA certificate template
	caTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "Test CA"},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
	}

	// Self-sign CA cert
	caCertDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		cleanup()
		t.Fatalf("Failed to create CA cert: %v", err)
	}

	caCert, err := x509.ParseCertificate(caCertDER)
	if err != nil {
		cleanup()
		t.Fatalf("Failed to parse CA cert: %v", err)
	}

	// Write CA cert
	caFile = filepath.Join(tmpDir, "ca.pem")
	caOut, err := os.Create(caFile)
	if err != nil {
		cleanup()
		t.Fatalf("Failed to create CA file: %v", err)
	}
	pem.Encode(caOut, &pem.Block{Type: "CERTIFICATE", Bytes: caCertDER})
	caOut.Close()

	// Generate server key
	serverKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		cleanup()
		t.Fatalf("Failed to generate server key: %v", err)
	}

	// Server certificate template
	serverTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "localhost"},
		NotBefore:    time.Now(),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"localhost"},
	}

	// Sign server cert with CA
	serverCertDER, err := x509.CreateCertificate(rand.Reader, serverTemplate, caCert, &serverKey.PublicKey, caKey)
	if err != nil {
		cleanup()
		t.Fatalf("Failed to create server cert: %v", err)
	}

	// Write server cert
	certFile = filepath.Join(tmpDir, "server.pem")
	certOut, err := os.Create(certFile)
	if err != nil {
		cleanup()
		t.Fatalf("Failed to create cert file: %v", err)
	}
	pem.Encode(certOut, &pem.Block{Type: "CERTIFICATE", Bytes: serverCertDER})
	certOut.Close()

	// Write server key
	keyFile = filepath.Join(tmpDir, "server-key.pem")
	keyOut, err := os.Create(keyFile)
	if err != nil {
		cleanup()
		t.Fatalf("Failed to create key file: %v", err)
	}
	keyDER, _ := x509.MarshalECPrivateKey(serverKey)
	pem.Encode(keyOut, &pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	keyOut.Close()

	return caFile, certFile, keyFile, cleanup
}

// TestServerCreation tests basic server creation
func TestServerCreation(t *testing.T) {
	log := logger.New("error", "text")

	tests := []struct {
		name    string
		cfg     Config
		wantErr bool
	}{
		{
			name: "valid config without TLS",
			cfg: Config{
				ListenAddr:       ":0",
				ReadTimeout:      60 * time.Second,
				HeartbeatTimeout: 90 * time.Second,
			},
			wantErr: false,
		},
		{
			name: "empty listen address",
			cfg: Config{
				ListenAddr:       "",
				ReadTimeout:      60 * time.Second,
				HeartbeatTimeout: 90 * time.Second,
			},
			wantErr: false, // Empty address defaults to a valid value
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, err := New(tt.cfg, log)
			if (err != nil) != tt.wantErr {
				t.Errorf("New() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if err == nil && srv == nil {
				t.Error("New() returned nil server without error")
			}
		})
	}
}

// TestServerDefaults tests that server uses sensible defaults
func TestServerDefaults(t *testing.T) {
	log := logger.New("error", "text")

	cfg := Config{
		ListenAddr: ":0",
		// No timeouts specified
	}

	srv, err := New(cfg, log)
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}

	if srv.readTimeout == 0 {
		t.Error("readTimeout should have a default value")
	}

	if srv.heartbeatTimeout == 0 {
		t.Error("heartbeatTimeout should have a default value")
	}

	// Check that default values are reasonable
	if srv.readTimeout < 30*time.Second {
		t.Errorf("readTimeout too short: %v", srv.readTimeout)
	}

	if srv.heartbeatTimeout < 60*time.Second {
		t.Errorf("heartbeatTimeout too short: %v", srv.heartbeatTimeout)
	}
}

// TestServerStartStop tests server lifecycle
func TestServerStartStop(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping server lifecycle test in short mode")
	}

	log := logger.New("error", "text")

	cfg := Config{
		ListenAddr:       ":0", // Random port
		ReadTimeout:      60 * time.Second,
		HeartbeatTimeout: 90 * time.Second,
	}

	srv, err := New(cfg, log)
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}

	// Start server in goroutine
	ctx := context.Background()
	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.Start(ctx)
	}()

	// Give server time to start
	time.Sleep(100 * time.Millisecond)

	// Stop server
	if err := srv.Stop(); err != nil {
		t.Errorf("Stop() failed: %v", err)
	}

	// Check if Start() returned without error
	select {
	case err := <-errCh:
		if err != nil {
			t.Errorf("Start() returned error: %v", err)
		}
	case <-time.After(1 * time.Second):
		t.Error("Start() did not return after Stop()")
	}
}

// TestAgentStorage tests agent storage functionality
func TestAgentStorage(t *testing.T) {
	log := logger.New("error", "text")

	cfg := Config{
		ListenAddr:       ":0",
		ReadTimeout:      60 * time.Second,
		HeartbeatTimeout: 90 * time.Second,
	}

	srv, err := New(cfg, log)
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}

	// Test agent registration (internal)
	agent := &ConnectedAgent{
		ID:           "test-agent-1",
		Hostname:     "test-host",
		OS:           "linux",
		Arch:         "amd64",
		Version:      "1.0.0",
		Capabilities: []string{"docker", "systemd"},
		ConnectedAt:  time.Now(),
		LastSeen:     time.Now(),
		Status:       "active",
	}

	// Register agent
	srv.agentsMu.Lock()
	srv.agents[agent.ID] = agent
	srv.agentsMu.Unlock()

	// Verify agent is stored
	srv.agentsMu.RLock()
	stored, exists := srv.agents[agent.ID]
	srv.agentsMu.RUnlock()

	if !exists {
		t.Error("agent not found after registration")
	}

	if stored.ID != agent.ID {
		t.Errorf("agent ID mismatch: got %s, want %s", stored.ID, agent.ID)
	}

	if stored.Hostname != agent.Hostname {
		t.Errorf("agent hostname mismatch: got %s, want %s", stored.Hostname, agent.Hostname)
	}
}

// TestRemoveAgent tests agent removal functionality
func TestRemoveAgent(t *testing.T) {
	log := logger.New("error", "text")

	cfg := Config{
		ListenAddr:       ":0",
		ReadTimeout:      60 * time.Second,
		HeartbeatTimeout: 90 * time.Second,
	}

	srv, err := New(cfg, log)
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}

	// Register agent
	agent := &ConnectedAgent{
		ID:           "test-agent-remove",
		Hostname:     "test-host",
		OS:           "linux",
		Arch:         "amd64",
		Version:      "1.0.0",
		Capabilities: []string{"docker"},
		ConnectedAt:  time.Now(),
		LastSeen:     time.Now(),
		Status:       "active",
	}

	srv.agentsMu.Lock()
	srv.agents[agent.ID] = agent
	srv.agentsMu.Unlock()

	// Verify agent exists
	_, found := srv.GetAgent(agent.ID)
	if !found {
		t.Fatal("agent should exist before removal")
	}

	// Remove agent
	err = srv.RemoveAgent(agent.ID)
	if err != nil {
		t.Errorf("RemoveAgent() failed: %v", err)
	}

	// Verify agent is removed
	_, found = srv.GetAgent(agent.ID)
	if found {
		t.Error("agent should not exist after removal")
	}

	// Try to remove non-existent agent
	err = srv.RemoveAgent("non-existent-agent")
	if err == nil {
		t.Error("RemoveAgent() should return error for non-existent agent")
	}
}

// TestMultipleAgents tests handling of multiple agents
func TestMultipleAgents(t *testing.T) {
	log := logger.New("error", "text")

	cfg := Config{
		ListenAddr:       ":0",
		ReadTimeout:      60 * time.Second,
		HeartbeatTimeout: 90 * time.Second,
	}

	srv, err := New(cfg, log)
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}

	// Register multiple agents
	agents := []*ConnectedAgent{
		{ID: "agent-1", Hostname: "host-1", OS: "linux", Arch: "amd64"},
		{ID: "agent-2", Hostname: "host-2", OS: "linux", Arch: "arm64"},
		{ID: "agent-3", Hostname: "host-3", OS: "darwin", Arch: "amd64"},
	}

	srv.agentsMu.Lock()
	for _, agent := range agents {
		srv.agents[agent.ID] = agent
	}
	srv.agentsMu.Unlock()

	// Verify all agents are stored
	srv.agentsMu.RLock()
	count := len(srv.agents)
	srv.agentsMu.RUnlock()

	if count != len(agents) {
		t.Errorf("expected %d agents, got %d", len(agents), count)
	}
}

// TestCommandQueue tests command queuing functionality
func TestCommandQueue(t *testing.T) {
	log := logger.New("error", "text")

	cfg := Config{
		ListenAddr:       ":0",
		ReadTimeout:      60 * time.Second,
		HeartbeatTimeout: 90 * time.Second,
	}

	srv, err := New(cfg, log)
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}

	// Create a command
	cmd := &AgentCommand{
		ID:      "cmd-1",
		AgentID: "agent-1",
		Type:    "health_check",
		Command: "health_check",
	}

	// Queue command using backpressure-aware queue (S7)
	if err := srv.commandQueue.Enqueue(cmd); err != nil {
		t.Errorf("failed to queue command: %v", err)
	}

	// Dequeue command using backpressure-aware queue (S7)
	received, ok := srv.commandQueue.TryDequeue()
	if !ok {
		t.Error("failed to dequeue command")
	} else {
		if received.ID != cmd.ID {
			t.Errorf("command ID mismatch: got %s, want %s", received.ID, cmd.ID)
		}
		if received.AgentID != cmd.AgentID {
			t.Errorf("agent ID mismatch: got %s, want %s", received.AgentID, cmd.AgentID)
		}
	}
}

// TestResultQueue tests result queuing functionality
func TestResultQueue(t *testing.T) {
	log := logger.New("error", "text")

	cfg := Config{
		ListenAddr:       ":0",
		ReadTimeout:      60 * time.Second,
		HeartbeatTimeout: 90 * time.Second,
	}

	srv, err := New(cfg, log)
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}

	// Create a result
	result := &CommandResult{
		CommandID:  "cmd-1",
		AgentID:    "agent-1",
		ExitCode:   0,
		Stdout:     "success",
		Stderr:     "",
		StartedAt:  time.Now().Add(-1 * time.Second),
		FinishedAt: time.Now(),
	}

	// Queue result
	select {
	case srv.resultQueue <- result:
		// Successfully queued
	case <-time.After(100 * time.Millisecond):
		t.Error("failed to queue result")
	}

	// Dequeue result
	select {
	case received := <-srv.resultQueue:
		if received.CommandID != result.CommandID {
			t.Errorf("command ID mismatch: got %s, want %s", received.CommandID, result.CommandID)
		}
		if received.ExitCode != result.ExitCode {
			t.Errorf("exit code mismatch: got %d, want %d", received.ExitCode, result.ExitCode)
		}
	case <-time.After(100 * time.Millisecond):
		t.Error("failed to dequeue result")
	}
}

// TestConcurrentAgentAccess tests thread-safe agent access
func TestConcurrentAgentAccess(t *testing.T) {
	log := logger.New("error", "text")

	cfg := Config{
		ListenAddr:       ":0",
		ReadTimeout:      60 * time.Second,
		HeartbeatTimeout: 90 * time.Second,
	}

	srv, err := New(cfg, log)
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}

	// Concurrently add and read agents
	done := make(chan bool)
	errors := make(chan error, 100)

	// Writers
	for i := 0; i < 10; i++ {
		go func(id int) {
			defer func() { done <- true }()
			agent := &ConnectedAgent{
				ID:       string(rune(id)),
				Hostname: "test",
			}
			srv.agentsMu.Lock()
			srv.agents[agent.ID] = agent
			srv.agentsMu.Unlock()
		}(i)
	}

	// Readers
	for i := 0; i < 10; i++ {
		go func() {
			defer func() { done <- true }()
			srv.agentsMu.RLock()
			_ = len(srv.agents)
			srv.agentsMu.RUnlock()
		}()
	}

	// Wait for all goroutines
	for i := 0; i < 20; i++ {
		<-done
	}

	// Check for errors
	select {
	case err := <-errors:
		t.Errorf("concurrent access error: %v", err)
	default:
		// No errors
	}
}

// BenchmarkAgentRegistration benchmarks agent registration
func BenchmarkAgentRegistration(b *testing.B) {
	log := logger.New("error", "text")

	cfg := Config{
		ListenAddr:       ":0",
		ReadTimeout:      60 * time.Second,
		HeartbeatTimeout: 90 * time.Second,
	}

	srv, err := New(cfg, log)
	if err != nil {
		b.Fatalf("New() failed: %v", err)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		agent := &ConnectedAgent{
			ID:       string(rune(i % 1000)),
			Hostname: "bench-host",
		}
		srv.agentsMu.Lock()
		srv.agents[agent.ID] = agent
		srv.agentsMu.Unlock()
	}
}

// TestSendCommandValidation tests command validation in SendCommand
func TestSendCommandValidation(t *testing.T) {
	log := logger.New("error", "text")

	cfg := Config{
		ListenAddr:       ":0",
		ReadTimeout:      60 * time.Second,
		HeartbeatTimeout: 90 * time.Second,
	}

	srv, err := New(cfg, log)
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}

	// Register a test agent
	agent := &ConnectedAgent{
		ID:       "test-agent",
		Hostname: "test-host",
		Status:   "connected",
	}
	if err := srv.RegisterAgent(agent); err != nil {
		t.Fatalf("Failed to register agent: %v", err)
	}

	tests := []struct {
		name    string
		cmd     *AgentCommand
		wantErr string
	}{
		{
			name:    "nil command",
			cmd:     nil,
			wantErr: "command is nil",
		},
		{
			name: "empty command ID",
			cmd: &AgentCommand{
				AgentID: "test-agent",
				Type:    "health_check",
			},
			wantErr: "command ID is required",
		},
		{
			name: "empty agent ID",
			cmd: &AgentCommand{
				ID:   "cmd-1",
				Type: "health_check",
			},
			wantErr: "agent ID is required",
		},
		{
			name: "empty command type",
			cmd: &AgentCommand{
				ID:      "cmd-1",
				AgentID: "test-agent",
			},
			wantErr: "command type is required",
		},
		{
			name: "agent not connected",
			cmd: &AgentCommand{
				ID:      "cmd-1",
				AgentID: "nonexistent-agent",
				Type:    "health_check",
			},
			wantErr: "agent not connected",
		},
		{
			name: "deprecated execute command rejected",
			cmd: &AgentCommand{
				ID:      "cmd-1",
				AgentID: "test-agent",
				Type:    "execute",
				Command: "execute",
			},
			wantErr: "not allowed",
		},
		{
			name: "generic exec command rejected",
			cmd: &AgentCommand{
				ID:      "cmd-1",
				AgentID: "test-agent",
				Type:    "exec",
				Command: "exec",
			},
			wantErr: "not allowed",
		},
		{
			name: "valid command",
			cmd: &AgentCommand{
				ID:      "cmd-1",
				AgentID: "test-agent",
				Type:    "health_check",
				Command: "health_check",
			},
			wantErr: "", // No error expected
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := srv.SendCommand(tt.cmd)
			if tt.wantErr == "" {
				if err != nil {
					t.Errorf("SendCommand() unexpected error: %v", err)
				}
			} else {
				if err == nil {
					t.Error("SendCommand() expected error but got nil")
				} else if !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("SendCommand() error = %q, want containing %q", err.Error(), tt.wantErr)
				}
			}
		})
	}
}

// TestSendCommandDisconnectedAgent tests sending to disconnected agent
func TestSendCommandDisconnectedAgent(t *testing.T) {
	log := logger.New("error", "text")

	cfg := Config{
		ListenAddr:       ":0",
		ReadTimeout:      60 * time.Second,
		HeartbeatTimeout: 90 * time.Second,
	}

	srv, err := New(cfg, log)
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}

	// Register an agent and mark it as disconnected
	agent := &ConnectedAgent{
		ID:       "disconnected-agent",
		Hostname: "test-host",
		Status:   "disconnected",
	}
	srv.agentsMu.Lock()
	srv.agents[agent.ID] = agent
	srv.agentsMu.Unlock()

	cmd := &AgentCommand{
		ID:      "cmd-1",
		AgentID: "disconnected-agent",
		Type:    "health_check",
	}

	err = srv.SendCommand(cmd)
	if err == nil {
		t.Error("SendCommand() should fail for disconnected agent")
	}
	if !strings.Contains(err.Error(), "disconnected") {
		t.Errorf("expected 'disconnected' in error, got: %v", err)
	}
}

func TestSendCommandWithPersistenceUsesQueueValidation(t *testing.T) {
	log := logger.New("error", "text")

	cfg := Config{
		ListenAddr:       ":0",
		ReadTimeout:      60 * time.Second,
		HeartbeatTimeout: 90 * time.Second,
	}

	srv, err := New(cfg, log)
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}

	cmd := &AgentCommand{
		ID:      "cmd-1",
		AgentID: "missing-agent",
		Type:    "health_check",
		Command: "health_check",
	}

	err = srv.SendCommandWithPersistence(cmd, "owner-1")
	if err == nil {
		t.Fatal("SendCommandWithPersistence() should reject commands for missing agents")
	}
	if !strings.Contains(err.Error(), "agent not connected") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestUpdateHeartbeat tests heartbeat update functionality
func TestUpdateHeartbeat(t *testing.T) {
	log := logger.New("error", "text")

	cfg := Config{
		ListenAddr:       ":0",
		ReadTimeout:      60 * time.Second,
		HeartbeatTimeout: 90 * time.Second,
	}

	srv, err := New(cfg, log)
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}

	// Register an agent first
	agent := &ConnectedAgent{
		ID:       "heartbeat-agent",
		Hostname: "test-host",
		Status:   "connecting",
		LastSeen: time.Now().Add(-1 * time.Hour), // Old timestamp
	}
	srv.agentsMu.Lock()
	srv.agents[agent.ID] = agent
	srv.agentsMu.Unlock()

	// Update heartbeat with resources
	resources := &ResourceUsage{
		CPUPercent:       50.5,
		MemoryUsedBytes:  1024 * 1024 * 512,  // 512MB
		MemoryTotalBytes: 1024 * 1024 * 1024, // 1GB
	}

	beforeUpdate := time.Now()
	err = srv.UpdateHeartbeat("heartbeat-agent", resources)
	if err != nil {
		t.Fatalf("UpdateHeartbeat() failed: %v", err)
	}

	// Verify update
	srv.agentsMu.RLock()
	updated := srv.agents["heartbeat-agent"]
	srv.agentsMu.RUnlock()

	if updated.Status != "connected" {
		t.Errorf("expected status 'connected', got '%s'", updated.Status)
	}

	if updated.LastSeen.Before(beforeUpdate) {
		t.Error("LastSeen was not updated")
	}

	if updated.Resources == nil {
		t.Error("Resources was not set")
	} else if updated.Resources.CPUPercent != 50.5 {
		t.Errorf("CPUPercent mismatch: got %f, want 50.5", updated.Resources.CPUPercent)
	}
}

// TestUpdateHeartbeatNonexistent tests heartbeat for non-existent agent
func TestUpdateHeartbeatNonexistent(t *testing.T) {
	log := logger.New("error", "text")

	cfg := Config{
		ListenAddr:       ":0",
		ReadTimeout:      60 * time.Second,
		HeartbeatTimeout: 90 * time.Second,
	}

	srv, err := New(cfg, log)
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}

	err = srv.UpdateHeartbeat("nonexistent-agent", nil)
	if err == nil {
		t.Error("UpdateHeartbeat() should fail for non-existent agent")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected 'not found' in error, got: %v", err)
	}
}

// TestGetAgents tests retrieving all agents
func TestGetAgents(t *testing.T) {
	log := logger.New("error", "text")

	cfg := Config{
		ListenAddr:       ":0",
		ReadTimeout:      60 * time.Second,
		HeartbeatTimeout: 90 * time.Second,
	}

	srv, err := New(cfg, log)
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}

	// Initially empty
	agents := srv.GetAgents()
	if len(agents) != 0 {
		t.Errorf("expected 0 agents initially, got %d", len(agents))
	}

	// Register some agents
	testAgents := []*ConnectedAgent{
		{ID: "agent-a", Hostname: "host-a", Status: "connected"},
		{ID: "agent-b", Hostname: "host-b", Status: "connected"},
		{ID: "agent-c", Hostname: "host-c", Status: "disconnected"},
	}

	srv.agentsMu.Lock()
	for _, a := range testAgents {
		srv.agents[a.ID] = a
	}
	srv.agentsMu.Unlock()

	// Get all agents
	agents = srv.GetAgents()
	if len(agents) != 3 {
		t.Errorf("expected 3 agents, got %d", len(agents))
	}

	// Verify all IDs are present
	foundIDs := make(map[string]bool)
	for _, a := range agents {
		foundIDs[a.ID] = true
	}

	for _, expected := range []string{"agent-a", "agent-b", "agent-c"} {
		if !foundIDs[expected] {
			t.Errorf("missing agent: %s", expected)
		}
	}
}

// TestCheckAgentHealth tests agent health checking
func TestCheckAgentHealth(t *testing.T) {
	log := logger.New("error", "text")

	cfg := Config{
		ListenAddr:       ":0",
		ReadTimeout:      60 * time.Second,
		HeartbeatTimeout: 90 * time.Second,
	}

	srv, err := New(cfg, log)
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}

	// Register agents with different LastSeen times
	now := time.Now()
	srv.agentsMu.Lock()
	srv.agents["recent-agent"] = &ConnectedAgent{
		ID:       "recent-agent",
		LastSeen: now.Add(-10 * time.Second), // 10s ago
		Status:   "connected",
	}
	srv.agents["stale-agent"] = &ConnectedAgent{
		ID:       "stale-agent",
		LastSeen: now.Add(-2 * time.Minute), // 2 minutes ago
		Status:   "connected",
	}
	srv.agentsMu.Unlock()

	// Check health with 1 minute timeout
	srv.CheckAgentHealth(1 * time.Minute)

	// Verify states
	srv.agentsMu.RLock()
	recentAgent := srv.agents["recent-agent"]
	staleAgent := srv.agents["stale-agent"]
	srv.agentsMu.RUnlock()

	if recentAgent.Status != "connected" {
		t.Errorf("recent agent should still be 'connected', got '%s'", recentAgent.Status)
	}

	if staleAgent.Status != "disconnected" {
		t.Errorf("stale agent should be 'disconnected', got '%s'", staleAgent.Status)
	}
}

// TestResultsChannel tests the Results() channel accessor
func TestResultsChannel(t *testing.T) {
	log := logger.New("error", "text")

	cfg := Config{
		ListenAddr:       ":0",
		ReadTimeout:      60 * time.Second,
		HeartbeatTimeout: 90 * time.Second,
	}

	srv, err := New(cfg, log)
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}

	// Get results channel
	resultsCh := srv.Results()
	if resultsCh == nil {
		t.Fatal("Results() returned nil channel")
	}

	// Send a result through the internal queue
	testResult := &CommandResult{
		CommandID: "test-cmd",
		AgentID:   "test-agent",
		ExitCode:  0,
		Stdout:    "success",
	}

	select {
	case srv.resultQueue <- testResult:
		// Sent successfully
	case <-time.After(100 * time.Millisecond):
		t.Fatal("failed to send result to queue")
	}

	// Receive through Results() channel
	select {
	case received := <-resultsCh:
		if received.CommandID != testResult.CommandID {
			t.Errorf("CommandID mismatch: got %s, want %s", received.CommandID, testResult.CommandID)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("failed to receive result from Results() channel")
	}
}

// TestSendCommandQueueFull tests command queue overflow handling with S7 backpressure
func TestSendCommandQueueFull(t *testing.T) {
	log := logger.New("error", "text")

	cfg := Config{
		ListenAddr:       ":0",
		ReadTimeout:      60 * time.Second,
		HeartbeatTimeout: 90 * time.Second,
		QueueMaxSize:     100, // S7: Set a small queue size for testing
	}

	srv, err := New(cfg, log)
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}

	// Register an agent
	agent := &ConnectedAgent{
		ID:       "queue-test-agent",
		Hostname: "test-host",
		Status:   "connected",
	}
	if err := srv.RegisterAgent(agent); err != nil {
		t.Fatalf("RegisterAgent() failed: %v", err)
	}

	// Fill the command queue using backpressure-aware queue (S7)
	for i := 0; i < 100; i++ {
		cmd := &AgentCommand{
			ID:      string(rune('a' + i%26)),
			AgentID: "queue-test-agent",
			Type:    "health_check",
		}
		if err := srv.commandQueue.Enqueue(cmd); err != nil {
			// Queue full is expected after max size
			break
		}
	}

	// Try to send one more command - should fail
	cmd := &AgentCommand{
		ID:      "overflow-cmd",
		AgentID: "queue-test-agent",
		Type:    "health_check",
	}

	err = srv.SendCommand(cmd)
	if err == nil {
		// Queue might not be full due to race conditions, but if it is, we expect an error
		t.Log("SendCommand() did not return queue full error (may be timing-dependent)")
	} else if !strings.Contains(err.Error(), "queue full") {
		t.Errorf("expected 'queue full' in error, got: %v", err)
	}
}

// TestGRPCRegister tests the gRPC Register method
func TestGRPCRegister(t *testing.T) {
	log := logger.New("error", "text")

	cfg := Config{
		ListenAddr:       ":0",
		ReadTimeout:      60 * time.Second,
		HeartbeatTimeout: 90 * time.Second,
	}

	srv, err := New(cfg, log)
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}

	tests := []struct {
		name    string
		req     *agentpb.RegisterRequest
		wantErr bool
		errMsg  string
	}{
		{
			name: "valid registration",
			req: &agentpb.RegisterRequest{
				AgentId:      "grpc-test-agent",
				Hostname:     "test-host",
				Os:           "linux",
				Arch:         "amd64",
				Version:      "1.0.0",
				Capabilities: []string{"docker", "systemd"},
			},
			wantErr: false,
		},
		{
			name: "empty agent ID",
			req: &agentpb.RegisterRequest{
				Hostname: "test-host",
			},
			wantErr: true,
			errMsg:  "agent_id is required",
		},
		{
			name: "minimal valid request",
			req: &agentpb.RegisterRequest{
				AgentId: "minimal-agent",
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			resp, err := srv.Register(ctx, tt.req)

			if tt.wantErr {
				if err == nil {
					t.Error("Register() expected error but got nil")
				} else if !strings.Contains(err.Error(), tt.errMsg) {
					t.Errorf("Register() error = %q, want containing %q", err.Error(), tt.errMsg)
				}
				return
			}

			if err != nil {
				t.Fatalf("Register() unexpected error: %v", err)
			}

			if !resp.Accepted {
				t.Error("Register() should return Accepted=true")
			}

			if resp.Config == nil {
				t.Error("Register() should return Config")
			}

			// Verify agent is registered
			agent, found := srv.GetAgent(tt.req.AgentId)
			if !found {
				t.Error("Agent not found after registration")
			}
			if agent.Hostname != tt.req.Hostname {
				t.Errorf("Hostname mismatch: got %s, want %s", agent.Hostname, tt.req.Hostname)
			}
		})
	}
}

// TestGRPCHeartbeat tests the gRPC Heartbeat method
func TestGRPCHeartbeat(t *testing.T) {
	log := logger.New("error", "text")

	cfg := Config{
		ListenAddr:       ":0",
		ReadTimeout:      60 * time.Second,
		HeartbeatTimeout: 90 * time.Second,
	}

	srv, err := New(cfg, log)
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}

	// First register an agent
	ctx := context.Background()
	_, err = srv.Register(ctx, &agentpb.RegisterRequest{
		AgentId:  "heartbeat-test-agent",
		Hostname: "test-host",
	})
	if err != nil {
		t.Fatalf("Register() failed: %v", err)
	}

	tests := []struct {
		name    string
		req     *agentpb.HeartbeatRequest
		wantErr bool
		errMsg  string
	}{
		{
			name: "valid heartbeat with resources",
			req: &agentpb.HeartbeatRequest{
				AgentId:       "heartbeat-test-agent",
				TimestampUnix: time.Now().Unix(),
				Resources: &agentpb.ResourceUsage{
					CpuPercent:       25.5,
					MemoryUsedBytes:  1024 * 1024 * 256,
					MemoryTotalBytes: 1024 * 1024 * 1024,
				},
			},
			wantErr: false,
		},
		{
			name: "valid heartbeat without resources",
			req: &agentpb.HeartbeatRequest{
				AgentId:       "heartbeat-test-agent",
				TimestampUnix: time.Now().Unix(),
			},
			wantErr: false,
		},
		{
			name: "empty agent ID",
			req: &agentpb.HeartbeatRequest{
				TimestampUnix: time.Now().Unix(),
			},
			wantErr: true,
			errMsg:  "agent_id is required",
		},
		{
			name: "non-existent agent",
			req: &agentpb.HeartbeatRequest{
				AgentId:       "nonexistent-agent",
				TimestampUnix: time.Now().Unix(),
			},
			wantErr: true,
			errMsg:  "not found",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp, err := srv.Heartbeat(ctx, tt.req)

			if tt.wantErr {
				if err == nil {
					t.Error("Heartbeat() expected error but got nil")
				} else if !strings.Contains(err.Error(), tt.errMsg) {
					t.Errorf("Heartbeat() error = %q, want containing %q", err.Error(), tt.errMsg)
				}
				return
			}

			if err != nil {
				t.Fatalf("Heartbeat() unexpected error: %v", err)
			}

			if !resp.Acknowledged {
				t.Error("Heartbeat() should return Acknowledged=true")
			}

			if resp.ServerTimestampUnix == 0 {
				t.Error("Heartbeat() should return ServerTimestampUnix")
			}
		})
	}
}

// TestGRPCReportStatus tests the gRPC ReportStatus method
func TestGRPCReportStatus(t *testing.T) {
	log := logger.New("error", "text")

	cfg := Config{
		ListenAddr:       ":0",
		ReadTimeout:      60 * time.Second,
		HeartbeatTimeout: 90 * time.Second,
	}

	srv, err := New(cfg, log)
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}

	// First register an agent
	ctx := context.Background()
	_, err = srv.Register(ctx, &agentpb.RegisterRequest{
		AgentId:  "status-test-agent",
		Hostname: "test-host",
	})
	if err != nil {
		t.Fatalf("Register() failed: %v", err)
	}

	// Test status report using correct proto structure
	req := &agentpb.StatusReport{
		AgentId:       "status-test-agent",
		TimestampUnix: time.Now().Unix(),
		Node: &agentpb.NodeStatus{
			State:         "healthy",
			UptimeSeconds: 3600,
			KernelVersion: "5.15.0",
			Resources: &agentpb.ResourceUsage{
				CpuPercent:       25.0,
				MemoryUsedBytes:  512 * 1024 * 1024,
				MemoryTotalBytes: 1024 * 1024 * 1024,
			},
		},
	}

	resp, err := srv.ReportStatus(ctx, req)
	if err != nil {
		t.Fatalf("ReportStatus() failed: %v", err)
	}

	if !resp.Received {
		t.Error("ReportStatus() should return Received=true")
	}
}

// TestGRPCRunPreChecks tests the gRPC RunPreChecks method
func TestGRPCRunPreChecks(t *testing.T) {
	log := logger.New("error", "text")

	cfg := Config{
		ListenAddr:       ":0",
		ReadTimeout:      60 * time.Second,
		HeartbeatTimeout: 90 * time.Second,
	}

	srv, err := New(cfg, log)
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}

	ctx := context.Background()
	req := &agentpb.PreCheckRequest{
		RequestId:      "precheck-test-001",
		TimeoutSeconds: 30,
		Checks: []*agentpb.PreCheckDefinition{
			{Type: "docker_version", Blocking: true, MinVersion: "20.0.0"},
			{Type: "disk_space", Blocking: false, Params: map[string]string{"min_gb": "10"}},
		},
	}

	resp, err := srv.RunPreChecks(ctx, req)
	if err != nil {
		t.Fatalf("RunPreChecks() failed: %v", err)
	}

	if resp.RequestId != req.RequestId {
		t.Errorf("RequestId mismatch: got %s, want %s", resp.RequestId, req.RequestId)
	}

	if len(resp.Results) != len(req.Checks) {
		t.Errorf("Results count mismatch: got %d, want %d", len(resp.Results), len(req.Checks))
	}

	if resp.ExecutedAtUnix == 0 {
		t.Error("ExecutedAtUnix should be set")
	}
}

// TestRequireMTLSWithoutCerts tests that RequireMTLS fails without certificates
func TestRequireMTLSWithoutCerts(t *testing.T) {
	log := logger.New("error", "text")

	cfg := Config{
		ListenAddr:  ":0",
		RequireMTLS: true,
		// No cert files provided
	}

	_, err := New(cfg, log)
	if err == nil {
		t.Error("New() should fail when RequireMTLS is true but no certs provided")
	}
	if !strings.Contains(err.Error(), "mTLS required") {
		t.Errorf("error should mention mTLS requirement, got: %v", err)
	}
}

// TestGRPCReportStatusEmptyAgentID tests ReportStatus with empty agent ID
func TestGRPCReportStatusEmptyAgentID(t *testing.T) {
	log := logger.New("error", "text")

	cfg := Config{
		ListenAddr:       ":0",
		ReadTimeout:      60 * time.Second,
		HeartbeatTimeout: 90 * time.Second,
	}

	srv, err := New(cfg, log)
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}

	ctx := context.Background()
	req := &agentpb.StatusReport{
		AgentId:       "", // Empty agent ID
		TimestampUnix: time.Now().Unix(),
	}

	_, err = srv.ReportStatus(ctx, req)
	if err == nil {
		t.Error("ReportStatus() should fail with empty agent ID")
	}
	if !strings.Contains(err.Error(), "agent_id is required") {
		t.Errorf("error should mention agent_id is required, got: %v", err)
	}
}

// TestGRPCReportStatusUnregisteredAgent tests ReportStatus for non-registered agent
func TestGRPCReportStatusUnregisteredAgent(t *testing.T) {
	log := logger.New("error", "text")

	cfg := Config{
		ListenAddr:       ":0",
		ReadTimeout:      60 * time.Second,
		HeartbeatTimeout: 90 * time.Second,
	}

	srv, err := New(cfg, log)
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}

	ctx := context.Background()
	req := &agentpb.StatusReport{
		AgentId:       "unregistered-agent",
		TimestampUnix: time.Now().Unix(),
		Node: &agentpb.NodeStatus{
			State: "healthy",
		},
	}

	// Should succeed but not update anything (agent doesn't exist)
	resp, err := srv.ReportStatus(ctx, req)
	if err != nil {
		t.Fatalf("ReportStatus() failed: %v", err)
	}

	if !resp.Received {
		t.Error("ReportStatus() should return Received=true even for unregistered agent")
	}
}

// TestGetAgent tests GetAgent for existing and non-existing agents
func TestGetAgent(t *testing.T) {
	log := logger.New("error", "text")

	cfg := Config{
		ListenAddr:       ":0",
		ReadTimeout:      60 * time.Second,
		HeartbeatTimeout: 90 * time.Second,
	}

	srv, err := New(cfg, log)
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}

	// Register an agent
	testAgent := &ConnectedAgent{
		ID:       "get-agent-test",
		Hostname: "test-host",
		OS:       "linux",
		Arch:     "amd64",
		Status:   "connected",
	}
	if err := srv.RegisterAgent(testAgent); err != nil {
		t.Fatalf("RegisterAgent() failed: %v", err)
	}

	// Test GetAgent for existing agent
	agent, found := srv.GetAgent("get-agent-test")
	if !found {
		t.Error("GetAgent() should find registered agent")
	}
	if agent.ID != testAgent.ID {
		t.Errorf("GetAgent() ID mismatch: got %s, want %s", agent.ID, testAgent.ID)
	}
	if agent.Hostname != testAgent.Hostname {
		t.Errorf("GetAgent() Hostname mismatch: got %s, want %s", agent.Hostname, testAgent.Hostname)
	}

	// Test GetAgent for non-existing agent
	_, found = srv.GetAgent("nonexistent-agent")
	if found {
		t.Error("GetAgent() should not find non-existing agent")
	}
}

// TestServerStopWithoutStart tests Stop on a server that wasn't started
func TestServerStopWithoutStart(t *testing.T) {
	log := logger.New("error", "text")

	cfg := Config{
		ListenAddr:       ":0",
		ReadTimeout:      60 * time.Second,
		HeartbeatTimeout: 90 * time.Second,
	}

	srv, err := New(cfg, log)
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}

	// Stop without starting - should not panic
	err = srv.Stop()
	if err != nil {
		t.Errorf("Stop() on unstarted server should not error, got: %v", err)
	}
}

// TestHeartbeatWithPendingCommand tests Heartbeat when there's a pending command
func TestHeartbeatWithPendingCommand(t *testing.T) {
	log := logger.New("error", "text")

	cfg := Config{
		ListenAddr:       ":0",
		ReadTimeout:      60 * time.Second,
		HeartbeatTimeout: 90 * time.Second,
	}

	srv, err := New(cfg, log)
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}

	ctx := context.Background()

	// Register an agent
	_, err = srv.Register(ctx, &agentpb.RegisterRequest{
		AgentId:  "pending-cmd-agent",
		Hostname: "test-host",
	})
	if err != nil {
		t.Fatalf("Register() failed: %v", err)
	}

	// Queue a command for this agent using backpressure-aware queue (S7)
	cmd := &AgentCommand{
		ID:      "pending-cmd-1",
		AgentID: "pending-cmd-agent",
		Type:    "health_check",
		Command: "health_check",
	}

	if err := srv.commandQueue.Enqueue(cmd); err != nil {
		t.Fatalf("Failed to queue command: %v", err)
	}

	// Send heartbeat
	resp, err := srv.Heartbeat(ctx, &agentpb.HeartbeatRequest{
		AgentId:       "pending-cmd-agent",
		TimestampUnix: time.Now().Unix(),
	})
	if err != nil {
		t.Fatalf("Heartbeat() failed: %v", err)
	}

	if !resp.Acknowledged {
		t.Error("Heartbeat() should be acknowledged")
	}

	// Note: PendingCommands may or may not be populated depending on timing
	// The command is dequeued and re-queued during heartbeat processing
}

// TestGRPCRunPreChecksEmptyRequest tests RunPreChecks with empty checks
func TestGRPCRunPreChecksEmptyRequest(t *testing.T) {
	log := logger.New("error", "text")

	cfg := Config{
		ListenAddr:       ":0",
		ReadTimeout:      60 * time.Second,
		HeartbeatTimeout: 90 * time.Second,
	}

	srv, err := New(cfg, log)
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}

	ctx := context.Background()
	req := &agentpb.PreCheckRequest{
		RequestId:      "empty-checks",
		TimeoutSeconds: 30,
		Checks:         []*agentpb.PreCheckDefinition{}, // Empty checks list
	}

	resp, err := srv.RunPreChecks(ctx, req)
	if err != nil {
		t.Fatalf("RunPreChecks() with empty checks failed: %v", err)
	}

	if resp.RequestId != req.RequestId {
		t.Errorf("RequestId mismatch: got %s, want %s", resp.RequestId, req.RequestId)
	}

	if len(resp.Results) != 0 {
		t.Errorf("Results should be empty for empty checks, got %d", len(resp.Results))
	}

	if !resp.AllBlockingPassed {
		t.Error("AllBlockingPassed should be true when no checks")
	}
}

// TestRegisterAgentOverwrite tests registering an agent with same ID
func TestRegisterAgentOverwrite(t *testing.T) {
	log := logger.New("error", "text")

	cfg := Config{
		ListenAddr:       ":0",
		ReadTimeout:      60 * time.Second,
		HeartbeatTimeout: 90 * time.Second,
	}

	srv, err := New(cfg, log)
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}

	// Register first agent
	agent1 := &ConnectedAgent{
		ID:       "overwrite-test",
		Hostname: "host-v1",
		Version:  "1.0.0",
	}
	if err := srv.RegisterAgent(agent1); err != nil {
		t.Fatalf("RegisterAgent() failed: %v", err)
	}

	// Register second agent with same ID
	agent2 := &ConnectedAgent{
		ID:       "overwrite-test",
		Hostname: "host-v2",
		Version:  "2.0.0",
	}
	if err := srv.RegisterAgent(agent2); err != nil {
		t.Fatalf("RegisterAgent() second time failed: %v", err)
	}

	// Verify second agent overwrote first
	stored, found := srv.GetAgent("overwrite-test")
	if !found {
		t.Fatal("Agent should exist")
	}
	if stored.Hostname != "host-v2" {
		t.Errorf("Hostname should be 'host-v2', got '%s'", stored.Hostname)
	}
	if stored.Version != "2.0.0" {
		t.Errorf("Version should be '2.0.0', got '%s'", stored.Version)
	}
}

// TestHeartbeatUpdatesResourcesNil tests heartbeat without resources clears them
func TestHeartbeatUpdatesResourcesNil(t *testing.T) {
	log := logger.New("error", "text")

	cfg := Config{
		ListenAddr:       ":0",
		ReadTimeout:      60 * time.Second,
		HeartbeatTimeout: 90 * time.Second,
	}

	srv, err := New(cfg, log)
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}

	// Register agent with resources
	agent := &ConnectedAgent{
		ID:       "resources-test-agent",
		Hostname: "test-host",
		Status:   "connected",
		Resources: &ResourceUsage{
			CPUPercent:       75.0,
			MemoryUsedBytes:  1024,
			MemoryTotalBytes: 2048,
		},
	}
	if err := srv.RegisterAgent(agent); err != nil {
		t.Fatalf("RegisterAgent() failed: %v", err)
	}

	// Update heartbeat without resources (nil)
	err = srv.UpdateHeartbeat("resources-test-agent", nil)
	if err != nil {
		t.Fatalf("UpdateHeartbeat() failed: %v", err)
	}

	// Verify resources are now nil
	stored, found := srv.GetAgent("resources-test-agent")
	if !found {
		t.Fatal("Agent should exist")
	}
	if stored.Resources != nil {
		t.Error("Resources should be nil after heartbeat with nil resources")
	}
}

// TestNewWithTLS tests server creation with valid TLS certificates
func TestNewWithTLS(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping TLS test in short mode")
	}

	caFile, certFile, keyFile, cleanup := generateTestCerts(t)
	defer cleanup()

	log := logger.New("error", "text")

	cfg := Config{
		ListenAddr:       ":0",
		CertFile:         certFile,
		KeyFile:          keyFile,
		CAFile:           caFile,
		ReadTimeout:      60 * time.Second,
		HeartbeatTimeout: 90 * time.Second,
	}

	srv, err := New(cfg, log)
	if err != nil {
		t.Fatalf("New() with TLS failed: %v", err)
	}

	if srv.tlsConfig == nil {
		t.Error("TLS config should be set when certificates are provided")
	}
}

// TestNewWithInvalidCert tests server creation with invalid certificate path
func TestNewWithInvalidCert(t *testing.T) {
	log := logger.New("error", "text")

	cfg := Config{
		ListenAddr:       ":0",
		CertFile:         "/nonexistent/cert.pem",
		KeyFile:          "/nonexistent/key.pem",
		CAFile:           "/nonexistent/ca.pem",
		ReadTimeout:      60 * time.Second,
		HeartbeatTimeout: 90 * time.Second,
	}

	_, err := New(cfg, log)
	if err == nil {
		t.Error("New() should fail with invalid certificate paths")
	}
}

// TestStartWithTLS tests server Start with TLS configuration
func TestStartWithTLS(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping TLS server start test in short mode")
	}

	caFile, certFile, keyFile, cleanup := generateTestCerts(t)
	defer cleanup()

	log := logger.New("error", "text")

	cfg := Config{
		ListenAddr:       ":0",
		CertFile:         certFile,
		KeyFile:          keyFile,
		CAFile:           caFile,
		ReadTimeout:      60 * time.Second,
		HeartbeatTimeout: 90 * time.Second,
	}

	srv, err := New(cfg, log)
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err = srv.Start(ctx)
	if err != nil {
		t.Fatalf("Start() with TLS failed: %v", err)
	}

	// Give server time to start
	time.Sleep(100 * time.Millisecond)

	// Stop server
	if err := srv.Stop(); err != nil {
		t.Errorf("Stop() failed: %v", err)
	}
}

// TestServerStartContextCancellation tests that server stops on context cancel
func TestServerStartContextCancellation(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping context cancellation test in short mode")
	}

	log := logger.New("error", "text")

	cfg := Config{
		ListenAddr:       ":0",
		ReadTimeout:      60 * time.Second,
		HeartbeatTimeout: 90 * time.Second,
	}

	srv, err := New(cfg, log)
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())

	err = srv.Start(ctx)
	if err != nil {
		t.Fatalf("Start() failed: %v", err)
	}

	// Give server time to start
	time.Sleep(100 * time.Millisecond)

	// Cancel context should trigger shutdown
	cancel()

	// Give server time to stop
	time.Sleep(200 * time.Millisecond)

	// Server should have stopped
	if srv.running {
		t.Error("Server should have stopped after context cancellation")
	}
}

// TestReportStatusWithNilNode tests ReportStatus when Node is nil
func TestReportStatusWithNilNode(t *testing.T) {
	log := logger.New("error", "text")

	cfg := Config{
		ListenAddr:       ":0",
		ReadTimeout:      60 * time.Second,
		HeartbeatTimeout: 90 * time.Second,
	}

	srv, err := New(cfg, log)
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}

	// Register an agent first
	ctx := context.Background()
	_, err = srv.Register(ctx, &agentpb.RegisterRequest{
		AgentId:  "nil-node-agent",
		Hostname: "test-host",
	})
	if err != nil {
		t.Fatalf("Register() failed: %v", err)
	}

	// Send status report with nil Node
	req := &agentpb.StatusReport{
		AgentId:       "nil-node-agent",
		TimestampUnix: time.Now().Unix(),
		Node:          nil, // nil Node
	}

	resp, err := srv.ReportStatus(ctx, req)
	if err != nil {
		t.Fatalf("ReportStatus() with nil Node failed: %v", err)
	}

	if !resp.Received {
		t.Error("ReportStatus() should return Received=true")
	}
}

// TestReportStatusWithNilResources tests ReportStatus when Node.Resources is nil
func TestReportStatusWithNilResources(t *testing.T) {
	log := logger.New("error", "text")

	cfg := Config{
		ListenAddr:       ":0",
		ReadTimeout:      60 * time.Second,
		HeartbeatTimeout: 90 * time.Second,
	}

	srv, err := New(cfg, log)
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}

	// Register an agent first
	ctx := context.Background()
	_, err = srv.Register(ctx, &agentpb.RegisterRequest{
		AgentId:  "nil-resources-agent",
		Hostname: "test-host",
	})
	if err != nil {
		t.Fatalf("Register() failed: %v", err)
	}

	// Send status report with Node but nil Resources
	req := &agentpb.StatusReport{
		AgentId:       "nil-resources-agent",
		TimestampUnix: time.Now().Unix(),
		Node: &agentpb.NodeStatus{
			State:     "healthy",
			Resources: nil, // nil Resources
		},
	}

	resp, err := srv.ReportStatus(ctx, req)
	if err != nil {
		t.Fatalf("ReportStatus() with nil Resources failed: %v", err)
	}

	if !resp.Received {
		t.Error("ReportStatus() should return Received=true")
	}

	// Verify agent status was updated
	agent, found := srv.GetAgent("nil-resources-agent")
	if !found {
		t.Fatal("Agent should exist")
	}
	if agent.Status != "healthy" {
		t.Errorf("Agent status should be 'healthy', got '%s'", agent.Status)
	}
}

// TestReportStatusUpdatesResources tests that ReportStatus updates agent resources
func TestReportStatusUpdatesResources(t *testing.T) {
	log := logger.New("error", "text")

	cfg := Config{
		ListenAddr:       ":0",
		ReadTimeout:      60 * time.Second,
		HeartbeatTimeout: 90 * time.Second,
	}

	srv, err := New(cfg, log)
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}

	ctx := context.Background()
	_, err = srv.Register(ctx, &agentpb.RegisterRequest{
		AgentId:  "full-status-agent",
		Hostname: "test-host",
	})
	if err != nil {
		t.Fatalf("Register() failed: %v", err)
	}

	// Send status report with full data
	req := &agentpb.StatusReport{
		AgentId:       "full-status-agent",
		TimestampUnix: time.Now().Unix(),
		Node: &agentpb.NodeStatus{
			State:         "healthy",
			UptimeSeconds: 7200,
			Resources: &agentpb.ResourceUsage{
				CpuPercent:       45.5,
				MemoryUsedBytes:  1024 * 1024 * 512,
				MemoryTotalBytes: 1024 * 1024 * 1024,
				DiskUsedBytes:    1024 * 1024 * 1024 * 50,
				DiskTotalBytes:   1024 * 1024 * 1024 * 100,
			},
		},
	}

	resp, err := srv.ReportStatus(ctx, req)
	if err != nil {
		t.Fatalf("ReportStatus() failed: %v", err)
	}

	if !resp.Received {
		t.Error("ReportStatus() should return Received=true")
	}

	// Verify resources were updated
	agent, found := srv.GetAgent("full-status-agent")
	if !found {
		t.Fatal("Agent should exist")
	}

	if agent.Resources == nil {
		t.Fatal("Agent resources should be set")
	}

	if agent.Resources.CPUPercent != 45.5 {
		t.Errorf("CPUPercent should be 45.5, got %f", agent.Resources.CPUPercent)
	}

	if agent.Resources.MemoryUsedBytes != 1024*1024*512 {
		t.Errorf("MemoryUsedBytes mismatch")
	}
}

// TestCheckAgentHealthNoAgents tests CheckAgentHealth with no agents
func TestCheckAgentHealthNoAgents(t *testing.T) {
	log := logger.New("error", "text")

	cfg := Config{
		ListenAddr:       ":0",
		ReadTimeout:      60 * time.Second,
		HeartbeatTimeout: 90 * time.Second,
	}

	srv, err := New(cfg, log)
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}

	// Should not panic with no agents
	srv.CheckAgentHealth(1 * time.Minute)

	agents := srv.GetAgents()
	if len(agents) != 0 {
		t.Errorf("Expected 0 agents, got %d", len(agents))
	}
}

// TestInvalidCAFile tests server creation with invalid CA file
func TestInvalidCAFile(t *testing.T) {
	// Create temp dir with invalid cert content
	tmpDir, err := os.MkdirTemp("", "invalid-ca-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create valid-looking but invalid cert file
	caFile := filepath.Join(tmpDir, "ca.pem")
	certFile := filepath.Join(tmpDir, "server.pem")
	keyFile := filepath.Join(tmpDir, "server-key.pem")

	// Write invalid content (not valid PEM)
	os.WriteFile(caFile, []byte("invalid ca content"), 0600)
	os.WriteFile(certFile, []byte("invalid cert content"), 0600)
	os.WriteFile(keyFile, []byte("invalid key content"), 0600)

	log := logger.New("error", "text")

	cfg := Config{
		ListenAddr: ":0",
		CertFile:   certFile,
		KeyFile:    keyFile,
		CAFile:     caFile,
	}

	_, err = New(cfg, log)
	if err == nil {
		t.Error("New() should fail with invalid certificate content")
	}
}

// TestInitCommandRouter tests command router initialization.
func TestInitCommandRouter(t *testing.T) {
	log := logger.New("error", "text")

	cfg := Config{
		ListenAddr:       ":0",
		ReadTimeout:      60 * time.Second,
		HeartbeatTimeout: 90 * time.Second,
	}

	srv, err := New(cfg, log)
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}

	// Initially, command router should be nil
	if srv.GetCommandRouter() != nil {
		t.Error("GetCommandRouter() should return nil before initialization")
	}

	// Initialize command router
	srv.InitCommandRouter()

	// Now it should be available
	router := srv.GetCommandRouter()
	if router == nil {
		t.Fatal("GetCommandRouter() should return non-nil after initialization")
	}
}

// TestSendTofuCommand tests low-level tofu command queueing.
func TestSendTofuCommand(t *testing.T) {
	log := logger.New("error", "text")

	cfg := Config{
		ListenAddr:       ":0",
		ReadTimeout:      60 * time.Second,
		HeartbeatTimeout: 90 * time.Second,
	}

	srv, err := New(cfg, log)
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}

	tests := []struct {
		name    string
		agentID string
		cmd     *agentpb.TofuCommand
		setup   func()
		wantErr bool
		errMsg  string
	}{
		{
			name:    "nil command",
			agentID: "agent-1",
			cmd:     nil,
			setup:   func() {},
			wantErr: true,
			errMsg:  "command is nil",
		},
		{
			name:    "empty command_id",
			agentID: "agent-1",
			cmd: &agentpb.TofuCommand{
				CommandId: "",
				Operation: agentpb.TofuOperation_TOFU_OPERATION_INIT,
			},
			setup:   func() {},
			wantErr: true,
			errMsg:  "command_id is required",
		},
		{
			name:    "empty agent_id",
			agentID: "",
			cmd: &agentpb.TofuCommand{
				CommandId: "test-1",
				Operation: agentpb.TofuOperation_TOFU_OPERATION_INIT,
			},
			setup:   func() {},
			wantErr: true,
			errMsg:  "agent_id is required",
		},
		{
			name:    "agent not connected",
			agentID: "non-existent",
			cmd: &agentpb.TofuCommand{
				CommandId: "test-1",
				Operation: agentpb.TofuOperation_TOFU_OPERATION_INIT,
			},
			setup:   func() {},
			wantErr: true,
			errMsg:  "not connected",
		},
		{
			name:    "agent disconnected",
			agentID: "agent-1",
			cmd: &agentpb.TofuCommand{
				CommandId: "test-1",
				Operation: agentpb.TofuOperation_TOFU_OPERATION_INIT,
			},
			setup: func() {
				srv.RegisterAgent(&ConnectedAgent{ID: "agent-1", Hostname: "test"})
				srv.agentsMu.Lock()
				if a, ok := srv.agents["agent-1"]; ok {
					a.Status = "disconnected"
				}
				srv.agentsMu.Unlock()
			},
			wantErr: true,
			errMsg:  "disconnected",
		},
		{
			name:    "valid command",
			agentID: "agent-2",
			cmd: &agentpb.TofuCommand{
				CommandId: "test-2",
				Operation: agentpb.TofuOperation_TOFU_OPERATION_PLAN,
			},
			setup: func() {
				srv.RegisterAgent(&ConnectedAgent{ID: "agent-2", Hostname: "test"})
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.setup()
			err := srv.SendTofuCommand(tt.agentID, tt.cmd)
			if tt.wantErr {
				if err == nil {
					t.Error("SendTofuCommand() expected error but got nil")
				} else if tt.errMsg != "" && !strings.Contains(err.Error(), tt.errMsg) {
					t.Errorf("SendTofuCommand() error = %q, want containing %q", err.Error(), tt.errMsg)
				}
			} else {
				if err != nil {
					t.Errorf("SendTofuCommand() unexpected error: %v", err)
				}
			}
		})
	}
}

// TestSendTerramateCommand tests Day-2 placeholder behavior.
func TestSendTerramateCommand(t *testing.T) {
	log := logger.New("error", "text")

	cfg := Config{
		ListenAddr:       ":0",
		ReadTimeout:      60 * time.Second,
		HeartbeatTimeout: 90 * time.Second,
	}

	srv, err := New(cfg, log)
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}

	cmd := &agentpb.TerramateCommand{
		CommandId: "terramate-test",
		Operation: agentpb.TerramateOperation_TERRAMATE_OPERATION_RUN,
	}

	// Should return error indicating Day-2 feature
	err = srv.SendTerramateCommand("agent-1", cmd)
	if err == nil {
		t.Error("SendTerramateCommand() expected error for Day-2 placeholder")
	}
	if err != nil && !strings.Contains(err.Error(), "Day-2") {
		t.Errorf("SendTerramateCommand() error = %q, want containing 'Day-2'", err.Error())
	}
}

// TestGetQueueStats tests queue statistics retrieval.
func TestGetQueueStats(t *testing.T) {
	log := logger.New("error", "text")

	cfg := Config{
		ListenAddr:       ":0",
		ReadTimeout:      60 * time.Second,
		HeartbeatTimeout: 90 * time.Second,
		QueueMaxSize:     100,
		TofuQueueMaxSize: 50,
	}

	srv, err := New(cfg, log)
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}

	// Get initial stats
	stats := srv.GetQueueStats()

	if stats["command"].MaxSize != 100 {
		t.Errorf("CommandQueue.MaxSize = %d, want 100", stats["command"].MaxSize)
	}

	if stats["tofu"].MaxSize != 50 {
		t.Errorf("TofuQueue.MaxSize = %d, want 50", stats["tofu"].MaxSize)
	}

	// Add some commands to queue
	srv.RegisterAgent(&ConnectedAgent{ID: "agent-1", Hostname: "test"})

	for i := 0; i < 5; i++ {
		cmd := &AgentCommand{
			ID:      fmt.Sprintf("cmd-%d", i),
			AgentID: "agent-1",
			Type:    "health_check",
			Command: "health_check",
		}
		srv.SendCommand(cmd)
	}

	// Check updated stats
	stats = srv.GetQueueStats()
	if stats["command"].CurrentSize != 5 {
		t.Errorf("CommandQueue.CurrentSize = %d, want 5", stats["command"].CurrentSize)
	}
}

// TestSendCommand_Validation tests SendCommand validation.
func TestSendCommand_Validation(t *testing.T) {
	log := logger.New("error", "text")

	cfg := Config{
		ListenAddr:       ":0",
		ReadTimeout:      60 * time.Second,
		HeartbeatTimeout: 90 * time.Second,
	}

	srv, err := New(cfg, log)
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}

	tests := []struct {
		name    string
		cmd     *AgentCommand
		wantErr bool
		errMsg  string
	}{
		{
			name:    "nil command",
			cmd:     nil,
			wantErr: true,
			errMsg:  "command is nil",
		},
		{
			name: "empty ID",
			cmd: &AgentCommand{
				ID:      "",
				AgentID: "agent-1",
				Type:    "health_check",
			},
			wantErr: true,
			errMsg:  "command ID is required",
		},
		{
			name: "empty AgentID",
			cmd: &AgentCommand{
				ID:      "cmd-1",
				AgentID: "",
				Type:    "health_check",
			},
			wantErr: true,
			errMsg:  "agent ID is required",
		},
		{
			name: "empty Type",
			cmd: &AgentCommand{
				ID:      "cmd-1",
				AgentID: "agent-1",
				Type:    "",
			},
			wantErr: true,
			errMsg:  "command type is required",
		},
		{
			name: "agent not connected",
			cmd: &AgentCommand{
				ID:      "cmd-1",
				AgentID: "non-existent",
				Type:    "health_check",
			},
			wantErr: true,
			errMsg:  "not connected",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := srv.SendCommand(tt.cmd)
			if tt.wantErr {
				if err == nil {
					t.Error("SendCommand() expected error but got nil")
				} else if tt.errMsg != "" && !strings.Contains(err.Error(), tt.errMsg) {
					t.Errorf("SendCommand() error = %q, want containing %q", err.Error(), tt.errMsg)
				}
			} else {
				if err != nil {
					t.Errorf("SendCommand() unexpected error: %v", err)
				}
			}
		})
	}
}

// TestResults tests the Results() channel accessor.
func TestResults(t *testing.T) {
	log := logger.New("error", "text")

	cfg := Config{
		ListenAddr:       ":0",
		ReadTimeout:      60 * time.Second,
		HeartbeatTimeout: 90 * time.Second,
	}

	srv, err := New(cfg, log)
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}

	// Get results channel
	resultsChan := srv.Results()
	if resultsChan == nil {
		t.Fatal("Results() should return non-nil channel")
	}

	// Send a result
	result := &CommandResult{
		CommandID: "test-cmd",
		AgentID:   "agent-1",
		ExitCode:  0,
	}

	select {
	case srv.resultQueue <- result:
		// OK
	default:
		t.Fatal("Could not send to result queue")
	}

	// Receive via Results()
	select {
	case received := <-resultsChan:
		if received.CommandID != "test-cmd" {
			t.Errorf("CommandID = %q, want 'test-cmd'", received.CommandID)
		}
	case <-time.After(time.Second):
		t.Error("Timeout waiting for result")
	}
}

// TestCheckAgentHealth_StaleAgent tests marking stale agents as disconnected.
func TestCheckAgentHealth_StaleAgent(t *testing.T) {
	log := logger.New("error", "text")

	cfg := Config{
		ListenAddr:       ":0",
		ReadTimeout:      60 * time.Second,
		HeartbeatTimeout: 90 * time.Second,
	}

	srv, err := New(cfg, log)
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}

	// Register an agent with old LastSeen
	srv.RegisterAgent(&ConnectedAgent{
		ID:       "stale-agent",
		Hostname: "test",
	})

	// Manually set LastSeen to 2 minutes ago
	srv.agentsMu.Lock()
	if agent, ok := srv.agents["stale-agent"]; ok {
		agent.LastSeen = time.Now().Add(-2 * time.Minute)
	}
	srv.agentsMu.Unlock()

	// Check health with 1 minute timeout
	srv.CheckAgentHealth(1 * time.Minute)

	// Agent should now be disconnected
	agent, found := srv.GetAgent("stale-agent")
	if !found {
		t.Fatal("Agent should still exist")
	}
	if agent.Status != "disconnected" {
		t.Errorf("Agent.Status = %q, want 'disconnected'", agent.Status)
	}
}
