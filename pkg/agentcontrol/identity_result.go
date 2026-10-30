package agentcontrol

import (
	"context"
	"errors"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/kombifyio/techstack/pkg/api/agentpb"
)

// A restarted caller or a result received by another replica has no bearer
// handoff. It must request a fresh node enrollment, never replay one from SQL.
var ErrTransientIdentityResultUnavailable = errors.New("transient identity result unavailable; request a fresh identity projection")

const transientIdentityLifetime = 2 * time.Minute

type transientIdentityKey struct {
	tenantID, agentID, commandID string
}

type transientIdentityWaiter struct {
	ctx       context.Context
	command   *agentpb.StackKitCommand
	result    chan *agentpb.StackKitResult
	delivered bool // guarded by Hub.mu, including after the channel is consumed
}

func needsTransientIdentityResult(command *agentpb.StackKitCommand) bool {
	if command == nil || !command.OwnerApproved {
		return false
	}
	return command.Operation == agentpb.StackKitOperation_STACKKIT_OPERATION_OWNER_ACTIVATION_ISSUE ||
		command.Operation == agentpb.StackKitOperation_STACKKIT_OPERATION_HOUSEHOLD_INVITE ||
		(command.Operation == agentpb.StackKitOperation_STACKKIT_OPERATION_HOUSEHOLD_PROJECT && command.HouseholdProjectionAction == "add")
}

// Keep the public result and its binding digest unchanged. The Guard already
// excludes this field from its durable journal and result-revision digest.
func durableStackKitResult(result *agentpb.StackKitResult) *agentpb.StackKitResult {
	public := proto.Clone(result).(*agentpb.StackKitResult)
	public.SensitiveResultJson = nil
	return public
}

func (h *Hub) registerTransientIdentity(ctx context.Context, tenantID, agentID string, command *agentpb.StackKitCommand) (*transientIdentityWaiter, error) {
	key := transientIdentityKey{tenantID, agentID, command.CommandId}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.transientIdentity == nil {
		h.transientIdentity = make(map[transientIdentityKey]*transientIdentityWaiter)
	}
	if h.transientIdentity[key] != nil {
		return nil, ErrResultRejected
	}
	waiter := &transientIdentityWaiter{ctx: ctx, command: proto.Clone(command).(*agentpb.StackKitCommand), result: make(chan *agentpb.StackKitResult, 1)}
	h.transientIdentity[key] = waiter
	return waiter, nil
}

func (h *Hub) removeTransientIdentity(tenantID, agentID, commandID string, waiter *transientIdentityWaiter) {
	key := transientIdentityKey{tenantID, agentID, commandID}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.transientIdentity[key] != waiter {
		return
	}
	delete(h.transientIdentity, key)
	select {
	case result := <-waiter.result:
		clear(result.SensitiveResultJson)
		result.SensitiveResultJson = nil
	default:
	}
}

// Called only after result validation and a successful durable commit. The
// tuple and full command must match the registered sender; this is no cache
// and cannot serve another caller or a later history/replay request.
func (h *Hub) deliverTransientIdentity(tenantID, agentID string, command *agentpb.StackKitCommand, result *agentpb.StackKitResult) {
	if result == nil || !result.Success || !needsTransientIdentityResult(command) || len(result.SensitiveResultJson) == 0 {
		return
	}
	key := transientIdentityKey{tenantID, agentID, command.CommandId}
	h.mu.Lock()
	defer h.mu.Unlock()
	waiter := h.transientIdentity[key]
	if waiter == nil || waiter.delivered || waiter.ctx.Err() != nil || !proto.Equal(waiter.command, command) {
		return
	}
	waiter.result <- proto.Clone(result).(*agentpb.StackKitResult)
	waiter.delivered = true
}

func (w *transientIdentityWaiter) attach(ctx context.Context, public *agentpb.StackKitResult) (*agentpb.StackKitResult, error) {
	// The SQL result can become visible before this process publishes the
	// handoff after commit. Wait for that handoff within the original budget.
	select {
	case result := <-w.result:
		if ctx.Err() != nil || !proto.Equal(durableStackKitResult(result), public) {
			clear(result.SensitiveResultJson)
			result.SensitiveResultJson = nil
			return nil, ErrTransientIdentityResultUnavailable
		}
		return result, nil
	case <-ctx.Done():
		return nil, ErrTransientIdentityResultUnavailable
	}
}
