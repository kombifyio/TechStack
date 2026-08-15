// Package grpcserver implements the gRPC server for agent communication.
// This file provides command handlers for IAC-4 (IaC-First Architecture).
package grpcserver

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/kombifyio/techstack/pkg/api/agentpb"
	"github.com/kombifyio/techstack/pkg/logger"
)

// =============================================================================
// IAC-4: Command Handler for OpenTofu and Terramate Operations
// This module routes IaC commands from Core to connected Agents
// =============================================================================

// TofuCommandHandler handles routing and execution of TofuCommand messages.
// It manages the command lifecycle from Core to Agent and back.
type TofuCommandHandler struct {
	log *logger.Logger

	// pendingCommands tracks commands waiting for results
	pendingCommands sync.Map // map[commandID]*PendingTofuCommand

	// Timeout for waiting for command results
	defaultTimeout time.Duration
}

// PendingTofuCommand tracks a tofu command that's been dispatched to an agent.
type PendingTofuCommand struct {
	Command      *agentpb.TofuCommand
	AgentID      string
	DispatchedAt time.Time
	ResultChan   chan *agentpb.TofuResult
	Timeout      time.Duration
}

// TerramateCommandHandler handles routing of TerramateCommand messages.
// Note: Terramate integration is a Day-2 feature (placeholder implementation).
type TerramateCommandHandler struct {
	log *logger.Logger
}

// PendingTerramateCommand tracks a terramate command dispatched to an agent.
type PendingTerramateCommand struct {
	Command      *agentpb.TerramateCommand
	AgentID      string
	DispatchedAt time.Time
	ResultChan   chan *agentpb.TerramateResult
	Timeout      time.Duration
}

// CommandPermission defines what operations an agent is allowed to perform.
type CommandPermission struct {
	// AllowedOperations lists the permitted TofuOperations for this agent
	AllowedTofuOps []agentpb.TofuOperation
	// AllowedTerramateOps lists permitted TerramateOperations
	AllowedTerramateOps []agentpb.TerramateOperation
	// MaxTimeout is the maximum timeout allowed for operations
	MaxTimeout time.Duration
	// AllowDestroy controls whether destroy operations are permitted
	AllowDestroy bool
}

// DefaultPermissions returns the default command permissions for agents.
func DefaultPermissions() *CommandPermission {
	return &CommandPermission{
		AllowedTofuOps: []agentpb.TofuOperation{
			agentpb.TofuOperation_TOFU_OPERATION_INIT,
			agentpb.TofuOperation_TOFU_OPERATION_PLAN,
			agentpb.TofuOperation_TOFU_OPERATION_APPLY,
			agentpb.TofuOperation_TOFU_OPERATION_DESTROY,
			agentpb.TofuOperation_TOFU_OPERATION_OUTPUT,
		},
		AllowedTerramateOps: []agentpb.TerramateOperation{
			agentpb.TerramateOperation_TERRAMATE_OPERATION_RUN,
			agentpb.TerramateOperation_TERRAMATE_OPERATION_SYNC_DRIFT,
			agentpb.TerramateOperation_TERRAMATE_OPERATION_LIST_CHANGED,
		},
		MaxTimeout:   30 * time.Minute,
		AllowDestroy: true,
	}
}

// NewTofuCommandHandler creates a new TofuCommandHandler.
func NewTofuCommandHandler(log *logger.Logger) *TofuCommandHandler {
	return &TofuCommandHandler{
		log:            log.WithComponent("tofu-handler"),
		defaultTimeout: 30 * time.Minute,
	}
}

// NewTerramateCommandHandler creates a new TerramateCommandHandler.
func NewTerramateCommandHandler(log *logger.Logger) *TerramateCommandHandler {
	return &TerramateCommandHandler{
		log: log.WithComponent("terramate-handler"),
	}
}

// ValidateCommand validates a TofuCommand against the agent's permissions.
func (h *TofuCommandHandler) ValidateCommand(cmd *agentpb.TofuCommand, perms *CommandPermission) error {
	if cmd == nil {
		return fmt.Errorf("command is nil")
	}
	if cmd.CommandId == "" {
		return fmt.Errorf("command_id is required")
	}

	// Validate operation is permitted
	opAllowed := false
	for _, op := range perms.AllowedTofuOps {
		if op == cmd.Operation {
			opAllowed = true
			break
		}
	}
	if !opAllowed {
		return fmt.Errorf("operation %s not permitted", cmd.Operation.String())
	}

	// Check destroy permission
	if cmd.Operation == agentpb.TofuOperation_TOFU_OPERATION_DESTROY && !perms.AllowDestroy {
		return fmt.Errorf("destroy operations are not permitted")
	}

	// Validate timeout
	if cmd.TimeoutSeconds > 0 {
		requestedTimeout := time.Duration(cmd.TimeoutSeconds) * time.Second
		if requestedTimeout > perms.MaxTimeout {
			return fmt.Errorf("requested timeout %v exceeds maximum allowed %v", requestedTimeout, perms.MaxTimeout)
		}
	}

	return nil
}

// DispatchCommand dispatches a TofuCommand to the specified agent.
// Returns a channel that will receive the result when available.
func (h *TofuCommandHandler) DispatchCommand(ctx context.Context, cmd *agentpb.TofuCommand, agentID string) (<-chan *agentpb.TofuResult, error) {
	// Validate command
	if err := h.ValidateCommand(cmd, DefaultPermissions()); err != nil {
		return nil, fmt.Errorf("invalid command: %w", err)
	}

	// Determine timeout
	timeout := h.defaultTimeout
	if cmd.TimeoutSeconds > 0 {
		timeout = time.Duration(cmd.TimeoutSeconds) * time.Second
	}

	// Create pending command entry
	resultChan := make(chan *agentpb.TofuResult, 1)
	pending := &PendingTofuCommand{
		Command:      cmd,
		AgentID:      agentID,
		DispatchedAt: time.Now(),
		ResultChan:   resultChan,
		Timeout:      timeout,
	}

	h.pendingCommands.Store(cmd.CommandId, pending)

	h.log.Info("tofu_command_dispatched",
		"command_id", cmd.CommandId,
		"operation", cmd.Operation.String(),
		"agent_id", agentID,
		"timeout", timeout.String(),
	)

	// Start timeout watcher
	go h.watchTimeout(ctx, cmd.CommandId, timeout)

	return resultChan, nil
}

// HandleResult processes a TofuResult from the authenticated command stream
// agent. Results are accepted only from the agent that received the command.
func (h *TofuCommandHandler) HandleResult(result *agentpb.TofuResult, agentID string) error {
	if result == nil {
		return fmt.Errorf("result is nil")
	}
	if agentID == "" {
		return fmt.Errorf("agent_id is required for tofu result")
	}

	pending, ok := h.pendingCommands.Load(result.CommandId)
	if !ok {
		h.log.Warn("received_result_for_unknown_command",
			"command_id", result.CommandId,
			"agent_id", agentID,
		)
		return fmt.Errorf("no pending command found for ID: %s", result.CommandId)
	}

	pc := pending.(*PendingTofuCommand)
	if pc.AgentID != agentID {
		h.log.Warn("tofu_result_agent_mismatch",
			"command_id", result.CommandId,
			"expected_agent_id", pc.AgentID,
			"reported_agent_id", agentID,
		)
		return fmt.Errorf("tofu result for command %s came from agent %s, expected %s", result.CommandId, agentID, pc.AgentID)
	}
	h.pendingCommands.Delete(result.CommandId)

	h.log.Info("tofu_result_received",
		"command_id", result.CommandId,
		"agent_id", pc.AgentID,
		"success", result.Success,
		"exit_code", result.ExitCode,
		"duration", time.Since(pc.DispatchedAt).String(),
	)

	// Send result to waiting channel
	select {
	case pc.ResultChan <- result:
		// Result delivered
	default:
		// Channel full or closed
		h.log.Warn("failed_to_deliver_result",
			"command_id", result.CommandId,
		)
	}

	close(pc.ResultChan)
	return nil
}

// watchTimeout monitors for command timeout and sends timeout result if needed.
func (h *TofuCommandHandler) watchTimeout(ctx context.Context, commandID string, timeout time.Duration) {
	select {
	case <-ctx.Done():
		// Context canceled, cleanup handled elsewhere
		return
	case <-time.After(timeout + 30*time.Second): // Add buffer for network latency
		pending, ok := h.pendingCommands.LoadAndDelete(commandID)
		if !ok {
			return // Already handled
		}

		pc := pending.(*PendingTofuCommand)
		h.log.Warn("tofu_command_timeout",
			"command_id", commandID,
			"agent_id", pc.AgentID,
			"timeout", timeout.String(),
		)

		// Send timeout result
		timeoutResult := &agentpb.TofuResult{
			CommandId:      commandID,
			Success:        false,
			ExitCode:       -1,
			Stderr:         fmt.Sprintf("Command timed out after %v", timeout),
			FinishedAtUnix: time.Now().Unix(),
		}

		select {
		case pc.ResultChan <- timeoutResult:
		default:
		}
		close(pc.ResultChan)
	}
}

// CancelCommand cancels a pending command.
func (h *TofuCommandHandler) CancelCommand(commandID string) error {
	pending, ok := h.pendingCommands.LoadAndDelete(commandID)
	if !ok {
		return fmt.Errorf("no pending command found for ID: %s", commandID)
	}

	pc := pending.(*PendingTofuCommand)
	h.log.Info("tofu_command_canceled",
		"command_id", commandID,
		"agent_id", pc.AgentID,
	)

	// Send cancellation result
	cancelResult := &agentpb.TofuResult{
		CommandId:      commandID,
		Success:        false,
		ExitCode:       -1,
		Stderr:         "Command was canceled",
		FinishedAtUnix: time.Now().Unix(),
	}

	select {
	case pc.ResultChan <- cancelResult:
	default:
	}
	close(pc.ResultChan)
	return nil
}

// GetPendingCommandCount returns the number of pending commands.
func (h *TofuCommandHandler) GetPendingCommandCount() int {
	count := 0
	h.pendingCommands.Range(func(_, _ interface{}) bool {
		count++
		return true
	})
	return count
}

// =============================================================================
// TerramateCommandHandler Implementation (Day-2 Feature - Placeholder)
// =============================================================================

// ValidateCommand validates a TerramateCommand against the agent's permissions.
func (h *TerramateCommandHandler) ValidateCommand(cmd *agentpb.TerramateCommand, perms *CommandPermission) error {
	if cmd == nil {
		return fmt.Errorf("command is nil")
	}
	if cmd.CommandId == "" {
		return fmt.Errorf("command_id is required")
	}

	// Validate operation is permitted
	opAllowed := false
	for _, op := range perms.AllowedTerramateOps {
		if op == cmd.Operation {
			opAllowed = true
			break
		}
	}
	if !opAllowed {
		return fmt.Errorf("operation %s not permitted", cmd.Operation.String())
	}

	return nil
}

// DispatchCommand dispatches a TerramateCommand to the specified agent.
// Note: This is a placeholder for Day-2 implementation.
func (h *TerramateCommandHandler) DispatchCommand(ctx context.Context, cmd *agentpb.TerramateCommand, agentID string) (<-chan *agentpb.TerramateResult, error) {
	h.log.Info("terramate_command_dispatched",
		"command_id", cmd.CommandId,
		"operation", cmd.Operation.String(),
		"agent_id", agentID,
		"note", "Day-2 feature - placeholder implementation",
	)

	// Placeholder: Return a result indicating the feature is not yet implemented
	resultChan := make(chan *agentpb.TerramateResult, 1)
	go func() {
		resultChan <- &agentpb.TerramateResult{
			CommandId:      cmd.CommandId,
			Success:        false,
			ExitCode:       1,
			Stderr:         "Terramate integration is a Day-2 feature and not yet implemented",
			FinishedAtUnix: time.Now().Unix(),
		}
		close(resultChan)
	}()

	return resultChan, nil
}

// HandleResult processes a TerramateResult from an agent.
// Note: This is a placeholder for Day-2 implementation.
func (h *TerramateCommandHandler) HandleResult(result *agentpb.TerramateResult) error {
	h.log.Info("terramate_result_received",
		"command_id", result.CommandId,
		"success", result.Success,
		"note", "Day-2 feature - placeholder implementation",
	)
	return nil
}

// =============================================================================
// CommandRouter integrates command handlers with the gRPC Server
// =============================================================================

// CommandRouter routes commands to appropriate handlers based on type.
type CommandRouter struct {
	server           *Server
	tofuHandler      *TofuCommandHandler
	terramateHandler *TerramateCommandHandler
	log              *logger.Logger
}

// NewCommandRouter creates a new CommandRouter for the given server.
func NewCommandRouter(server *Server, log *logger.Logger) *CommandRouter {
	return &CommandRouter{
		server:           server,
		tofuHandler:      NewTofuCommandHandler(log),
		terramateHandler: NewTerramateCommandHandler(log),
		log:              log.WithComponent("command-router"),
	}
}

// SendTofuCommand sends a TofuCommand to an agent and waits for the result.
// This is the main entry point for sending IaC commands from Core to agents.
func (r *CommandRouter) SendTofuCommand(ctx context.Context, agentID string, cmd *agentpb.TofuCommand) (*agentpb.TofuResult, error) {
	// Check if agent is connected
	agent, ok := r.server.GetAgent(agentID)
	if !ok {
		return nil, fmt.Errorf("agent not connected: %s", agentID)
	}
	if agent.Status == agentStatusDisconnected {
		return nil, fmt.Errorf("agent is disconnected: %s", agentID)
	}

	// Check agent capabilities for tofu support
	hasTofuCapability := false
	for _, cap := range agent.Capabilities {
		if cap == commandClassTofu || cap == "iac" || cap == "infrastructure" {
			hasTofuCapability = true
			break
		}
	}
	if !hasTofuCapability {
		r.log.Warn("agent_lacks_tofu_capability",
			"agent_id", agentID,
			"capabilities", agent.Capabilities,
		)
		// Continue anyway - capability check is advisory
	}

	// Dispatch command and get result channel
	resultChan, err := r.tofuHandler.DispatchCommand(ctx, cmd, agentID)
	if err != nil {
		return nil, fmt.Errorf("failed to dispatch command: %w", err)
	}

	// Queue typed TofuCommand for delivery to agent via CommandStream.
	if err := r.server.SendTofuCommand(agentID, cmd); err != nil {
		r.tofuHandler.CancelCommand(cmd.CommandId)
		return nil, fmt.Errorf("failed to queue command: %w", err)
	}

	r.log.Info("tofu_command_sent",
		"command_id", cmd.CommandId,
		"agent_id", agentID,
		"operation", cmd.Operation.String(),
	)

	// Wait for result
	select {
	case result := <-resultChan:
		return result, nil
	case <-ctx.Done():
		r.tofuHandler.CancelCommand(cmd.CommandId)
		return nil, fmt.Errorf("context canceled while waiting for result: %w", ctx.Err())
	}
}

// SendTerramateCommand sends a TerramateCommand to an agent.
// Note: This is a Day-2 feature - currently returns a placeholder response.
func (r *CommandRouter) SendTerramateCommand(ctx context.Context, agentID string, cmd *agentpb.TerramateCommand) (*agentpb.TerramateResult, error) {
	resultChan, err := r.terramateHandler.DispatchCommand(ctx, cmd, agentID)
	if err != nil {
		return nil, fmt.Errorf("failed to dispatch terramate command: %w", err)
	}

	select {
	case result := <-resultChan:
		return result, nil
	case <-ctx.Done():
		return nil, fmt.Errorf("context canceled: %w", ctx.Err())
	}
}

// HandleTofuResult processes a TofuResult received from an authenticated agent.
// Called by the CommandStream handler when receiving results.
func (r *CommandRouter) HandleTofuResult(result *agentpb.TofuResult, agentID string) error {
	return r.tofuHandler.HandleResult(result, agentID)
}

// HandleTerramateResult processes a TerramateResult received from an agent.
// Called by the CommandStream handler when receiving results.
func (r *CommandRouter) HandleTerramateResult(result *agentpb.TerramateResult) error {
	return r.terramateHandler.HandleResult(result)
}

// GetPendingTofuCommandCount returns the number of pending tofu commands.
func (r *CommandRouter) GetPendingTofuCommandCount() int {
	return r.tofuHandler.GetPendingCommandCount()
}
