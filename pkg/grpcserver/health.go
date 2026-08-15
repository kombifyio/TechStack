// Package grpcserver implements the gRPC server for agent communication.
// This file handles health-related operations: Heartbeat, health checks, and timeout monitoring.
package grpcserver

import (
	"context"
	"fmt"
	"time"

	"github.com/kombifyio/techstack/pkg/api/agentpb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Heartbeat implements agentpb.AgentServiceServer.Heartbeat
func (s *Server) Heartbeat(ctx context.Context, req *agentpb.HeartbeatRequest) (*agentpb.HeartbeatResponse, error) {
	if req.AgentId == "" {
		return nil, status.Error(codes.InvalidArgument, "agent_id is required")
	}
	if s.requiresAgentIdentityBinding() {
		if _, err := s.authorizeRegisteredAgentRPC(ctx, req.AgentId); err != nil {
			return nil, err
		}
	}

	// Convert resources if provided
	var resources *ResourceUsage
	if req.Resources != nil {
		resources = &ResourceUsage{
			CPUPercent:       req.Resources.CpuPercent,
			MemoryUsedBytes:  req.Resources.MemoryUsedBytes,
			MemoryTotalBytes: req.Resources.MemoryTotalBytes,
			DiskUsedBytes:    req.Resources.DiskUsedBytes,
			DiskTotalBytes:   req.Resources.DiskTotalBytes,
		}
	}

	if err := s.UpdateHeartbeat(req.AgentId, resources); err != nil {
		return nil, status.Errorf(codes.NotFound, "agent not found: %v", err)
	}

	// Build pending commands list using backpressure-aware queue (S7)
	var pendingCommands []*agentpb.PendingCommand

	// Check for commands matching this agent using filter dequeue
	cmd, found := s.commandQueue.DequeueWithFilter(func(c *AgentCommand) bool {
		return c.AgentID == req.AgentId
	})
	if found {
		pendingCommands = append(pendingCommands, &agentpb.PendingCommand{
			CommandId: cmd.ID,
			Type:      cmd.Type,
		})
		// Re-queue the command for actual delivery via CommandStream
		if err := s.commandQueue.Enqueue(cmd); err != nil {
			s.log.Warn("command_queue_full_on_requeue",
				"agent_id", req.AgentId,
				"error", err.Error(),
			)
		}
	}

	return &agentpb.HeartbeatResponse{
		Acknowledged:        true,
		ServerTimestampUnix: time.Now().Unix(),
		PendingCommands:     pendingCommands,
	}, nil
}

// UpdateHeartbeat updates an agent's last seen time.
func (s *Server) UpdateHeartbeat(agentID string, resources *ResourceUsage) error {
	s.agentsMu.Lock()
	defer s.agentsMu.Unlock()

	agent, ok := s.agents[agentID]
	if !ok {
		return fmt.Errorf("agent not found: %s", agentID)
	}

	agent.LastSeen = time.Now()
	agent.Resources = resources
	agent.Status = agentStatusConnected

	return nil
}

// CheckAgentHealth marks agents as disconnected if not seen recently.
func (s *Server) CheckAgentHealth(timeout time.Duration) {
	s.agentsMu.Lock()
	defer s.agentsMu.Unlock()

	now := time.Now()
	for id, agent := range s.agents {
		if now.Sub(agent.LastSeen) > timeout {
			agent.Status = agentStatusDisconnected
			s.log.Warn("agent_timeout", "id", id, "last_seen", agent.LastSeen)
		}
	}
}
