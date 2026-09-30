package grpcserver

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/api/agentpb"
	"github.com/kombifyio/techstack/pkg/auth"
	"github.com/kombifyio/techstack/pkg/logger"
	collectormetricspb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

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
	if srv.agentConnected(agent.ID) {
		t.Error("removed agent remains eligible for command-stream delivery")
	}

	// Try to remove non-existent agent
	err = srv.RemoveAgent("non-existent-agent")
	if err == nil {
		t.Error("RemoveAgent() should return error for non-existent agent")
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

// TestMTLSListenerAdmitsOnlyCAIssuedTLS13Clients pins the agent listener's
// security boundary: only clients presenting a certificate issued by the
// configured CA, over TLS 1.3, reach an RPC handler.
func TestMTLSListenerAdmitsOnlyCAIssuedTLS13Clients(t *testing.T) {
	dir := t.TempDir()
	cm, err := auth.NewCertManager(filepath.Join(dir, "ca"))
	if err != nil {
		t.Fatalf("NewCertManager: %v", err)
	}
	serverCert, err := cm.GenerateServerCert("localhost", []string{"127.0.0.1"}, 1)
	if err != nil {
		t.Fatalf("GenerateServerCert: %v", err)
	}
	agentCert, err := cm.GenerateAgentCert("agent-1", 1)
	if err != nil {
		t.Fatalf("GenerateAgentCert: %v", err)
	}
	write := func(name string, data []byte) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
		return path
	}
	caFile := write("ca.pem", cm.CACertPEM())
	serverCertFile := write("server.pem", serverCert.CertPEM)
	serverKeyFile := write("server-key.pem", serverCert.KeyPEM)

	srv, err := New(Config{
		ListenAddr:  "127.0.0.1:0",
		CertFile:    serverCertFile,
		KeyFile:     serverKeyFile,
		CAFile:      caFile,
		RequireMTLS: true,
	}, logger.New("error", "text"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := srv.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer srv.Stop()
	addr := srv.listener.Addr().String()

	caPool := x509.NewCertPool()
	caPool.AppendCertsFromPEM(cm.CACertPEM())
	agentKeyPair, err := tls.X509KeyPair(agentCert.CertPEM, agentCert.KeyPEM)
	if err != nil {
		t.Fatalf("agent key pair: %v", err)
	}

	cases := []struct {
		name  string
		tls   *tls.Config
		admit bool
	}{
		{"ca-issued client cert", &tls.Config{RootCAs: caPool, Certificates: []tls.Certificate{agentKeyPair}}, true},
		{"no client cert", &tls.Config{RootCAs: caPool}, false},
		{"tls 1.2 client", &tls.Config{RootCAs: caPool, Certificates: []tls.Certificate{agentKeyPair}, MaxVersion: tls.VersionTLS12}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.tls.ServerName = "localhost"
			conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(credentials.NewTLS(tc.tls)))
			if err != nil {
				t.Fatalf("grpc.NewClient: %v", err)
			}
			defer conn.Close()
			callCtx, callCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer callCancel()
			_, err = collectormetricspb.NewMetricsServiceClient(conn).Export(callCtx, &collectormetricspb.ExportMetricsServiceRequest{})
			if tc.admit && err != nil {
				t.Fatalf("CA-issued TLS 1.3 client was rejected: %v", err)
			}
			if !tc.admit && err == nil {
				t.Fatal("client reached an RPC handler without passing mTLS")
			}
		})
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

func TestSendTofuCommand(t *testing.T) {
	log := logger.New("error", "text")
	srv, err := New(Config{ListenAddr: ":0", ReadTimeout: 60 * time.Second, HeartbeatTimeout: 90 * time.Second}, log)
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	srv.RegisterAgent(&ConnectedAgent{ID: "agent-2", Hostname: "test"})
	err = srv.SendTofuCommand("agent-2", &agentpb.TofuCommand{
		CommandId: "test-2",
		Operation: agentpb.TofuOperation_TOFU_OPERATION_PLAN,
	})
	if err == nil || !strings.Contains(err.Error(), "retired") {
		t.Fatalf("SendTofuCommand() error = %v, want retired", err)
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

	err = srv.SendTerramateCommand("agent-1", cmd)
	if err == nil || !strings.Contains(err.Error(), "retired") {
		t.Fatalf("SendTerramateCommand() error = %v, want retired", err)
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
