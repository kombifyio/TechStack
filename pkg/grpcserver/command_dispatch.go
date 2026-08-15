// Package grpcserver implements the gRPC server for agent communication.
// This file handles command sending and streaming: SendCommand, CommandStream, and queue operations.
package grpcserver

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/kombifyio/techstack/pkg/api/agentpb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var diagnosticCommandTypes = map[string]struct{}{
	diagnosticCommandHealthCheck: {},
	diagnosticCommandGetLogs:     {},
}

const diagnosticCommandHealthCheck, diagnosticCommandGetLogs, commandClassTofu = "health_check", "get_logs", "tofu"

// CommandStream implements agentpb.AgentServiceServer.CommandStream
func (s *Server) CommandStream(stream grpc.BidiStreamingServer[agentpb.AgentMessage, agentpb.CoreMessage]) error {
	// First message should identify the agent
	msg, err := stream.Recv()
	if err != nil {
		return status.Errorf(codes.InvalidArgument, "failed to receive initial message: %v", err)
	}

	agentID := msg.AgentId
	if agentID == "" {
		return status.Error(codes.InvalidArgument, "agent_id is required in initial message")
	}
	if err := s.authorizeCommandStream(stream.Context(), agentID); err != nil {
		return err
	}

	s.log.Info("command_stream_opened", "agent_id", agentID)

	// Create a done channel for cleanup
	done := make(chan struct{})
	defer close(done)

	// Goroutine to send commands to agent using backpressure-aware queues (S7)
	go func() {
		// Polling ticker for queue checks (backpressure queues don't use channels directly)
		ticker := time.NewTicker(50 * time.Millisecond)
		defer ticker.Stop()

		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				stackKitEntry, found := s.stackKitCommandQueue.DequeueWithFilter(func(entry *stackKitCommandEntry) bool {
					return entry.AgentID == agentID
				})
				if found {
					coreMsg := &agentpb.CoreMessage{
						MessageId: stackKitEntry.Command.CommandId,
						Payload: &agentpb.CoreMessage_StackkitCommand{
							StackkitCommand: stackKitEntry.Command,
						},
					}
					if err := stream.Send(coreMsg); err != nil {
						s.log.Error("send_stackkit_command_error", "error", err, "agent_id", agentID)
						if queueErr := s.stackKitCommandQueue.Enqueue(stackKitEntry); queueErr != nil {
							s.log.Error("stackkit_requeue_failed", "error", queueErr.Error(), "cmd_id", stackKitEntry.Command.CommandId)
						}
						return
					}
					s.log.Info(
						"stackkit_command_sent",
						"cmd_id", stackKitEntry.Command.CommandId,
						"agent_id", agentID,
						"operation", stackKitEntry.Command.Operation.String(),
					)
					continue
				}

				// S7: Check TofuCommand queue (priority over legacy commands)
				tofuEntry, found := s.tofuCommandQueue.DequeueWithFilter(func(e *tofuCommandEntry) bool {
					return e.AgentID == agentID
				})
				if found {
					// Send TofuCommand to agent
					coreMsg := &agentpb.CoreMessage{
						MessageId: tofuEntry.Command.CommandId,
						Payload: &agentpb.CoreMessage_TofuCommand{
							TofuCommand: tofuEntry.Command,
						},
					}

					if err := stream.Send(coreMsg); err != nil {
						s.log.Error("send_tofu_command_error", "error", err, "agent_id", agentID)
						// Re-queue the command if send failed
						if qErr := s.tofuCommandQueue.Enqueue(tofuEntry); qErr != nil {
							s.log.Error("tofu_requeue_failed", "error", qErr.Error(), "cmd_id", tofuEntry.Command.CommandId)
						}
						return
					}
					s.log.Info("tofu_command_sent", "cmd_id", tofuEntry.Command.CommandId, "agent_id", agentID, "operation", tofuEntry.Command.Operation.String())
					continue
				}

				// S7: Check legacy command queue
				cmd, found := s.commandQueue.DequeueWithFilter(func(c *AgentCommand) bool {
					return c.AgentID == agentID
				})
				if found {
					// Send command to agent using oneof payload
					coreMsg := &agentpb.CoreMessage{
						MessageId: cmd.ID,
						Payload: &agentpb.CoreMessage_Execute{
							Execute: &agentpb.ExecuteCommand{
								CommandId:        cmd.ID,
								Command:          cmd.Command,
								Args:             cmd.Args,
								Environment:      cmd.Environment,
								WorkingDirectory: cmd.WorkDir,
								TimeoutSeconds:   int32(cmd.Timeout.Seconds()),
							},
						},
					}

					if err := stream.Send(coreMsg); err != nil {
						s.log.Error("send_command_error", "error", err, "agent_id", agentID)
						// Re-queue the command if send failed
						if qErr := s.commandQueue.Enqueue(cmd); qErr != nil {
							s.log.Error("command_requeue_failed", "error", qErr.Error(), "cmd_id", cmd.ID)
						}
						return
					}
					s.log.Info("command_sent", "cmd_id", cmd.ID, "agent_id", agentID)
				}
			}
		}
	}()

	// Main loop to receive messages from agent
	for {
		msg, err := stream.Recv()
		if err == io.EOF {
			s.log.Info("command_stream_closed", "agent_id", agentID)
			return nil
		}
		if err != nil {
			s.log.Error("stream_recv_error", "error", err, "agent_id", agentID)
			return err
		}

		// Handle different payload types using oneof
		switch payload := msg.Payload.(type) {
		case *agentpb.AgentMessage_CommandResult:
			// Command result from agent
			if payload.CommandResult != nil {
				result := &CommandResult{
					CommandID:  payload.CommandResult.CommandId,
					AgentID:    agentID,
					ExitCode:   int(payload.CommandResult.ExitCode),
					Stdout:     payload.CommandResult.Stdout,
					Stderr:     payload.CommandResult.Stderr,
					StartedAt:  time.Unix(payload.CommandResult.StartedAtUnix, 0),
					FinishedAt: time.Unix(payload.CommandResult.FinishedAtUnix, 0),
				}

				select {
				case s.resultQueue <- result:
					s.log.Info("result_received", "cmd_id", result.CommandID, "agent_id", agentID)
				default:
					s.log.Warn("result_queue_full")
				}
			}

		// IAC-4: Handle TofuResult messages
		case *agentpb.AgentMessage_TofuResult:
			if payload.TofuResult != nil {
				s.log.Info("tofu_result_received",
					"cmd_id", payload.TofuResult.CommandId,
					"agent_id", agentID,
					"success", payload.TofuResult.Success,
					"exit_code", payload.TofuResult.ExitCode,
				)

				// Route to command router if available
				if s.commandRouter != nil {
					if err := s.commandRouter.HandleTofuResult(payload.TofuResult, agentID); err != nil {
						s.log.Warn("failed_to_handle_tofu_result",
							"error", err.Error(),
							"cmd_id", payload.TofuResult.CommandId,
							"agent_id", agentID,
						)
					}
				}
			}

		case *agentpb.AgentMessage_StackkitResult:
			if payload.StackkitResult != nil {
				if err := s.stackKitHandler.HandleResult(payload.StackkitResult, agentID); err != nil {
					s.log.Warn(
						"stackkit_result_rejected",
						"error", err.Error(),
						"cmd_id", payload.StackkitResult.CommandId,
						"agent_id", agentID,
					)
				} else {
					s.appendStackKitResultLogs(agentID, payload.StackkitResult)
					s.log.Info(
						"stackkit_result_received",
						"cmd_id", payload.StackkitResult.CommandId,
						"agent_id", agentID,
						"success", payload.StackkitResult.Success,
					)
				}
			}

		case *agentpb.AgentMessage_LogEntry:
			// Log message from agent
			if payload.LogEntry != nil {
				entry := AgentLogEntry{
					AgentID:   agentID,
					Source:    "agent-grpc",
					Timestamp: time.Unix(payload.LogEntry.TimestampUnix, 0),
					Level:     payload.LogEntry.Level,
					Message:   payload.LogEntry.Message,
					Fields:    payload.LogEntry.Fields,
				}
				s.addAgentLog(agentID, entry)
				entry = normalizeRuntimeLogEntry(entry, time.Now().UTC())
				s.log.Info("agent_log",
					"agent_id", agentID,
					"level", entry.Level,
					"message", entry.Message,
					"stack_id", entry.StackID,
					"job_id", entry.JobID,
					"lease_id", entry.LeaseID,
					"provider", entry.Provider,
				)
			}

		case *agentpb.AgentMessage_ServiceStatus:
			// Service status update
			if payload.ServiceStatus != nil {
				s.log.Info("service_status",
					"agent_id", agentID,
					"service_id", payload.ServiceStatus.ServiceId,
					"state", payload.ServiceStatus.State,
					"healthy", payload.ServiceStatus.Healthy,
				)
			}

		default:
			// Just a heartbeat message (no payload)
			s.UpdateHeartbeat(agentID, nil)
		}
	}
}

// SendCommand queues a command for an agent with backpressure handling (S7).
func (s *Server) SendCommand(cmd *AgentCommand) error {
	if err := s.validateCommandForQueue(cmd); err != nil {
		return err
	}

	if err := s.enqueueCommand(cmd); err != nil {
		return err
	}

	s.log.Info("command_queued",
		"id", cmd.ID,
		"agent", cmd.AgentID,
		"type", cmd.Type,
		"queue_size", s.commandQueue.Size(),
	)
	return nil
}

func (s *Server) validateCommandForQueue(cmd *AgentCommand) error {
	if cmd == nil {
		return fmt.Errorf("command is nil")
	}
	if cmd.ID == "" {
		return fmt.Errorf("command ID is required")
	}
	if cmd.AgentID == "" {
		return fmt.Errorf("agent ID is required")
	}
	if cmd.Type == "" {
		return fmt.Errorf("command type is required")
	}
	cmd.Type = strings.ToLower(strings.TrimSpace(cmd.Type))
	if cmd.Command == "" {
		cmd.Command = cmd.Type
	}
	if _, ok := diagnosticCommandTypes[cmd.Type]; !ok {
		return fmt.Errorf("agent command type %q is not allowed on the diagnostic command queue; use a typed command path", cmd.Type)
	}
	if cmd.Command != cmd.Type {
		return fmt.Errorf("agent diagnostic command %q must match type %q", cmd.Command, cmd.Type)
	}

	s.agentsMu.RLock()
	agent, ok := s.agents[cmd.AgentID]
	s.agentsMu.RUnlock()

	if !ok {
		return fmt.Errorf("agent not connected: %s", cmd.AgentID)
	}
	if agent.Status == agentStatusDisconnected {
		return fmt.Errorf("agent is disconnected: %s", cmd.AgentID)
	}

	if err := s.enforceCommandClass(agent, cmd.Type); err != nil {
		return err
	}

	return nil
}

// enforceCommandClass enforces the per-agent AllowedCommandClasses pinned at
// registration time (Phase 7.1). When no enrollment store is wired this check
// is a no-op so legacy / standalone deployments keep working. Once an
// enrollment store is wired, an empty allowlist means no commands are allowed.
func (s *Server) enforceCommandClass(agent *ConnectedAgent, cmdClass string) error {
	if s.enrollmentStore == nil {
		return nil
	}
	if agent == nil || len(agent.AllowedCommandClasses) == 0 {
		return fmt.Errorf("command class %q is not in agent allowlist", cmdClass)
	}
	for _, c := range agent.AllowedCommandClasses {
		if c == cmdClass {
			return nil
		}
	}
	return fmt.Errorf("command class %q is not in agent %q allowlist (tenant=%s)", cmdClass, agent.ID, agent.Tenant)
}

func (s *Server) enqueueCommand(cmd *AgentCommand) error {
	if err := s.commandQueue.Enqueue(cmd); err != nil {
		if IsQueueFullError(err) {
			s.log.Warn("command_queue_full",
				"agent_id", cmd.AgentID,
				"command_id", cmd.ID,
				"queue_size", s.commandQueue.Size(),
				"max_size", s.queueConfig.MaxSize,
			)
		}
		return fmt.Errorf("command queue full: %w", err)
	}
	return nil
}

// Results returns the channel for receiving command results.
func (s *Server) Results() <-chan *CommandResult {
	return s.resultQueue
}

// =============================================================================
// IAC-4: Tofu/Terramate Command Integration
// =============================================================================

// InitCommandRouter initializes the command router for IaC operations.
// This should be called after server creation to enable TofuCommand handling.
func (s *Server) InitCommandRouter() {
	s.commandRouter = NewCommandRouter(s, s.log)
	s.log.Info("command_router_initialized", "note", "IaC-4 command routing enabled")
}

// GetCommandRouter returns the command router for external access.
func (s *Server) GetCommandRouter() *CommandRouter {
	return s.commandRouter
}

// SendTofuCommand queues a TofuCommand for delivery to the specified agent with backpressure handling (S7).
// This is the low-level method; prefer using CommandRouter.SendTofuCommand for
// full command lifecycle management including result handling.
func (s *Server) SendTofuCommand(agentID string, cmd *agentpb.TofuCommand) error {
	if cmd == nil {
		return fmt.Errorf("command is nil")
	}
	if cmd.CommandId == "" {
		return fmt.Errorf("command_id is required")
	}
	if agentID == "" {
		return fmt.Errorf("agent_id is required")
	}

	// Check if agent is connected
	s.agentsMu.RLock()
	agent, ok := s.agents[agentID]
	s.agentsMu.RUnlock()

	if !ok {
		return fmt.Errorf("agent not connected: %s", agentID)
	}
	if agent.Status == agentStatusDisconnected {
		return fmt.Errorf("agent is disconnected: %s", agentID)
	}

	// Phase 7.1: tofu commands are subject to the same per-agent allowlist.
	if err := s.enforceCommandClass(agent, commandClassTofu); err != nil {
		return err
	}

	// Queue the command using backpressure-aware queue (S7)
	entry := &tofuCommandEntry{
		AgentID: agentID,
		Command: cmd,
	}

	if err := s.tofuCommandQueue.Enqueue(entry); err != nil {
		if IsQueueFullError(err) {
			s.log.Warn("tofu_command_queue_full",
				"agent_id", agentID,
				"command_id", cmd.CommandId,
				"queue_size", s.tofuCommandQueue.Size(),
				"max_size", s.tofuQueueConfig.MaxSize,
			)
		}
		return fmt.Errorf("tofu command queue full: %w", err)
	}

	s.log.Info("tofu_command_queued",
		"command_id", cmd.CommandId,
		"agent_id", agentID,
		"operation", cmd.Operation.String(),
		"queue_size", s.tofuCommandQueue.Size(),
	)
	return nil
}

// SendTerramateCommand queues a TerramateCommand for delivery to the specified agent.
// Note: Terramate is a Day-2 feature; this is a placeholder implementation.
func (s *Server) SendTerramateCommand(agentID string, cmd *agentpb.TerramateCommand) error {
	s.log.Warn("terramate_command_not_implemented",
		"command_id", cmd.CommandId,
		"agent_id", agentID,
		"note", "Terramate integration is a Day-2 feature",
	)
	return fmt.Errorf("terramate commands not yet implemented (Day-2 feature)")
}

// =============================================================================
// S7: Queue Statistics and Metrics
// =============================================================================

// GetQueueStats returns statistics for all command queues.
// This is useful for monitoring and metrics collection.
func (s *Server) GetQueueStats() map[string]QueueStats {
	stats := make(map[string]QueueStats)
	stats["command"] = s.commandQueue.Stats()
	stats[commandClassTofu] = s.tofuCommandQueue.Stats()
	stats[commandClassStackKit] = s.stackKitCommandQueue.Stats()
	return stats
}

// GetCommandQueueStats returns statistics for the command queue.
func (s *Server) GetCommandQueueStats() QueueStats {
	return s.commandQueue.Stats()
}

// GetTofuQueueStats returns statistics for the tofu command queue.
func (s *Server) GetTofuQueueStats() QueueStats {
	return s.tofuCommandQueue.Stats()
}

// IsCommandQueueHealthy returns true if the command queue is below warning threshold.
func (s *Server) IsCommandQueueHealthy() bool {
	return !s.commandQueue.IsAboveThreshold()
}

// IsTofuQueueHealthy returns true if the tofu queue is below warning threshold.
func (s *Server) IsTofuQueueHealthy() bool {
	return !s.tofuCommandQueue.IsAboveThreshold()
}

// RunPreChecks implements agentpb.AgentServiceServer.RunPreChecks (H6a: Pre-Check Execution)
// This allows the Core to request pre-deployment checks from connected agents.
func (s *Server) RunPreChecks(ctx context.Context, req *agentpb.PreCheckRequest) (*agentpb.PreCheckResponse, error) {
	s.log.Info("precheck_request", "request_id", req.RequestId, "checks_count", len(req.Checks))

	// Note: In a real implementation, this would be dispatched to the appropriate agent
	// via the CommandStream. For now, we return a placeholder indicating the feature is available.
	// The actual execution happens on the agent side (pkg/agent/precheck_runner.go).

	response := &agentpb.PreCheckResponse{
		RequestId:         req.RequestId,
		Results:           make([]*agentpb.PreCheckResult, 0, len(req.Checks)),
		AllBlockingPassed: true,
		ExecutedAtUnix:    time.Now().Unix(),
	}

	// Mark as pending - actual execution would be via agent
	for _, check := range req.Checks {
		result := &agentpb.PreCheckResult{
			Type:    check.Type,
			Status:  "pending",
			Message: "Pre-check queued for agent execution",
		}
		response.Results = append(response.Results, result)
	}

	return response, nil
}

// =============================================================================
// Sprint 9: Command Persistence (F9)
// =============================================================================

// recoverPendingCommands recovers commands that were pending before server restart.
func (s *Server) recoverPendingCommands() error {
	if s.commandStore == nil {
		return nil
	}

	commands, err := s.commandStore.RecoverAllPending()
	if err != nil {
		return fmt.Errorf("failed to recover pending commands: %w", err)
	}

	if len(commands) == 0 {
		s.log.Info("command_recovery_complete", "recovered", 0)
		return nil
	}

	// Re-queue recovered commands
	recovered := 0
	for _, cmd := range commands {
		if err := s.validateRecoveredCommand(cmd); err != nil {
			s.log.Warn("command_recovery_rejected",
				"command_id", commandIDForLog(cmd),
				"agent_id", agentIDForLog(cmd),
				"error", err.Error(),
			)
			continue
		}
		if err := s.commandQueue.Enqueue(cmd); err != nil {
			s.log.Warn("command_recovery_enqueue_failed",
				"command_id", cmd.ID,
				"agent_id", cmd.AgentID,
				"error", err.Error(),
			)
			continue
		}
		recovered++
	}

	s.log.Info("command_recovery_complete",
		"recovered", recovered,
		"total", len(commands),
	)

	return nil
}

func (s *Server) validateRecoveredCommand(cmd *AgentCommand) error {
	if err := s.validateBasicCommandFields(cmd); err != nil {
		return err
	}
	if _, ok := diagnosticCommandTypes[cmd.Type]; !ok {
		return fmt.Errorf("agent command type %q is not allowed on the diagnostic command queue", cmd.Type)
	}
	if cmd.Command != cmd.Type {
		return fmt.Errorf("agent diagnostic command %q must match type %q", cmd.Command, cmd.Type)
	}
	if s.enrollmentStore == nil {
		return nil
	}

	s.agentsMu.RLock()
	agent, ok := s.agents[cmd.AgentID]
	s.agentsMu.RUnlock()
	if !ok {
		return fmt.Errorf("agent %q is not connected for identity-bound recovery", cmd.AgentID)
	}
	if agent.Status == agentStatusDisconnected {
		return fmt.Errorf("agent %q is disconnected for identity-bound recovery", cmd.AgentID)
	}
	return s.enforceCommandClass(agent, cmd.Type)
}

func (s *Server) validateBasicCommandFields(cmd *AgentCommand) error {
	if cmd == nil {
		return fmt.Errorf("command is nil")
	}
	cmd.ID = strings.TrimSpace(cmd.ID)
	cmd.AgentID = strings.TrimSpace(cmd.AgentID)
	cmd.Type = strings.ToLower(strings.TrimSpace(cmd.Type))
	if cmd.Command == "" {
		cmd.Command = cmd.Type
	}
	if cmd.ID == "" {
		return fmt.Errorf("command ID is required")
	}
	if cmd.AgentID == "" {
		return fmt.Errorf("agent ID is required")
	}
	if cmd.Type == "" {
		return fmt.Errorf("command type is required")
	}
	return nil
}

func commandIDForLog(cmd *AgentCommand) string {
	if cmd == nil {
		return ""
	}
	return cmd.ID
}

func agentIDForLog(cmd *AgentCommand) string {
	if cmd == nil {
		return ""
	}
	return cmd.AgentID
}

// SendCommandWithPersistence sends a command to an agent with database persistence.
// This enables restart recovery and provides a full audit trail.
// Use this method in production; use SendCommand for testing without persistence.
func (s *Server) SendCommandWithPersistence(cmd *AgentCommand, ownerID string) error {
	if err := s.validateCommandForQueue(cmd); err != nil {
		return err
	}

	// Persist command before queueing (Sprint 9: F9)
	if s.commandStore != nil {
		if _, err := s.commandStore.PersistCommand(cmd, ownerID); err != nil {
			s.log.Error("command_persist_failed",
				"command_id", cmd.ID,
				"agent_id", cmd.AgentID,
				"error", err.Error(),
			)
			// Non-fatal: still try to send the command
		}
	}

	if err := s.enqueueCommand(cmd); err != nil {
		// Update status to failed if persisted
		if s.commandStore != nil {
			_ = s.commandStore.UpdateStatus(cmd.ID, "queue_rejected")
		}
		return err
	}

	// Update status to queued
	if s.commandStore != nil {
		_ = s.commandStore.UpdateStatus(cmd.ID, "queued")
	}

	s.log.Debug("command_queued",
		"command_id", cmd.ID,
		"agent_id", cmd.AgentID,
		"type", cmd.Type,
	)

	return nil
}

// CommandStore returns the command store for external use (e.g., by routes).
// Returns nil if persistence is not enabled.
func (s *Server) CommandStore() *CommandStore {
	return s.commandStore
}
