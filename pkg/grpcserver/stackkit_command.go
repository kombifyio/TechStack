package grpcserver

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/kombifyio/techstack/internal/stackkitrelease"
	"github.com/kombifyio/techstack/pkg/api/agentpb"
	"github.com/kombifyio/techstack/pkg/stackkitcommand"
	"google.golang.org/protobuf/proto"
)

const commandClassStackKit = "stackkit"

type stackKitCommandEntry struct {
	AgentID string
	Command *agentpb.StackKitCommand
}

type pendingStackKitCommand struct {
	agentID string
	command *agentpb.StackKitCommand
	result  chan *agentpb.StackKitResult
}

// StackKitCommandHandler correlates typed lifecycle results with the exact
// agent, command, and published release that Core dispatched.
type StackKitCommandHandler struct {
	pending sync.Map // map[commandID]*pendingStackKitCommand
}

func NewStackKitCommandHandler() *StackKitCommandHandler {
	return &StackKitCommandHandler{}
}

// SendStackKitCommand dispatches a closed typed lifecycle operation and waits
// for its typed result. It never creates an AgentCommand or shell queue entry.
func (s *Server) SendStackKitCommand(
	ctx context.Context,
	agentID string,
	command *agentpb.StackKitCommand,
) (*agentpb.StackKitResult, error) {
	if s == nil || s.stackKitCommandQueue == nil || s.stackKitHandler == nil {
		return nil, fmt.Errorf("typed StackKits command path is not initialized")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	command = cloneGRPCStackKitCommand(command)
	agentID = strings.TrimSpace(agentID)
	if agentID == "" {
		return nil, fmt.Errorf("agent_id is required")
	}
	if err := stackkitcommand.ValidateCommand(command); err != nil {
		return nil, err
	}

	s.agentsMu.RLock()
	connected, ok := s.agents[agentID]
	s.agentsMu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("agent not connected: %s", agentID)
	}
	if connected.Status == agentStatusDisconnected {
		return nil, fmt.Errorf("agent is disconnected: %s", agentID)
	}
	if !containsStackKitCapability(connected.Capabilities) {
		return nil, fmt.Errorf("agent %q does not advertise the typed StackKits capability", agentID)
	}
	if command.GetOperation() == agentpb.StackKitOperation_STACKKIT_OPERATION_APPLY &&
		!containsCapability(connected.Capabilities, stackkitcommand.ExpectedPlanHashCapability) {
		return nil, fmt.Errorf("agent %q does not advertise %s", agentID, stackkitcommand.ExpectedPlanHashCapability)
	}
	if err := s.enforceCommandClass(connected, commandClassStackKit); err != nil {
		return nil, err
	}

	pending := &pendingStackKitCommand{
		agentID: agentID,
		command: command,
		result:  make(chan *agentpb.StackKitResult, 1),
	}
	if _, exists := s.stackKitHandler.pending.LoadOrStore(command.CommandId, pending); exists {
		return nil, fmt.Errorf("StackKit command_id %q is already pending", command.CommandId)
	}
	defer s.stackKitHandler.pending.CompareAndDelete(command.CommandId, pending)

	entry := &stackKitCommandEntry{AgentID: agentID, Command: command}
	if err := s.stackKitCommandQueue.Enqueue(entry); err != nil {
		return nil, fmt.Errorf("StackKit command queue: %w", err)
	}
	s.log.Info(
		"stackkit_command_queued",
		"command_id", command.CommandId,
		"agent_id", agentID,
		"operation", command.Operation.String(),
		"release", command.Release.Version,
		"queue_size", s.stackKitCommandQueue.Size(),
	)

	waitTimeout := 31 * time.Minute
	if command.TimeoutSeconds > 0 {
		waitTimeout = time.Duration(command.TimeoutSeconds)*time.Second + time.Minute
	}
	waitCtx, cancel := context.WithTimeout(ctx, waitTimeout)
	defer cancel()
	select {
	case result := <-pending.result:
		return result, nil
	case <-waitCtx.Done():
		s.stackKitCommandQueue.DequeueWithFilter(func(candidate *stackKitCommandEntry) bool {
			return candidate == entry
		})
		return nil, fmt.Errorf("wait for StackKit command %s: %w", command.CommandId, waitCtx.Err())
	}
}

func containsStackKitCapability(capabilities []string) bool {
	return containsCapability(capabilities, commandClassStackKit)
}

func containsCapability(capabilities []string, expected string) bool {
	for _, capability := range capabilities {
		if strings.EqualFold(strings.TrimSpace(capability), expected) {
			return true
		}
	}
	return false
}

// HandleResult admits a result only when its command, agent identity, release
// pin, command-result envelope, and JSONL event records all match.
func (handler *StackKitCommandHandler) HandleResult(result *agentpb.StackKitResult, agentID string) error {
	if handler == nil {
		return fmt.Errorf("typed StackKits result handler is not initialized")
	}
	if result == nil {
		return fmt.Errorf("StackKit result is required")
	}
	value, ok := handler.pending.Load(strings.TrimSpace(result.CommandId))
	if !ok {
		return fmt.Errorf("StackKit result has no pending command")
	}
	pending := value.(*pendingStackKitCommand)
	if strings.TrimSpace(agentID) != pending.agentID {
		return fmt.Errorf("StackKit result came from agent %q, expected %q", agentID, pending.agentID)
	}
	if err := stackkitcommand.ValidateResult(result, pending.command); err != nil {
		return err
	}
	if !handler.pending.CompareAndDelete(result.CommandId, pending) {
		return fmt.Errorf("StackKit result command is no longer pending")
	}
	pending.result <- result
	return nil
}

// StackKitReleasePinFor exposes only the public immutable release identity
// needed by the gRPC command. Filesystem paths and trust blobs stay local.
func StackKitReleasePinFor(release stackkitrelease.Release) *agentpb.StackKitReleasePin {
	receipt := release.Receipt()
	return &agentpb.StackKitReleasePin{
		Version:            receipt.Version,
		PlatformOs:         receipt.Platform.OS,
		PlatformArch:       receipt.Platform.Arch,
		ArchiveSha256:      receipt.ArchiveSHA256,
		ReleaseIndexSha256: receipt.IndexSHA256,
	}
}

func cloneGRPCStackKitCommand(command *agentpb.StackKitCommand) *agentpb.StackKitCommand {
	if command == nil {
		return nil
	}
	return proto.Clone(command).(*agentpb.StackKitCommand)
}
