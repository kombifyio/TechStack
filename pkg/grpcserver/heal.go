package grpcserver

import (
	"context"
	"strings"
	"time"

	"github.com/kombifyio/techstack/pkg/api/agentpb"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ReportHealEvent accepts one authenticated agent observation and records it
// through the same tenant-scoped RIL authority used by the operator API.
func (s *Server) ReportHealEvent(ctx context.Context, req *agentpb.HealEventReport) (*agentpb.HealEventAck, error) {
	if req.GetAgentId() == "" || req.GetRecipeName() == "" {
		return nil, status.Error(codes.InvalidArgument, "agent_id and recipe_name are required")
	}
	agent, err := s.authorizeRegisteredAgentRPC(ctx, req.GetAgentId())
	if err != nil {
		return nil, err
	}
	if s.healStore == nil {
		return nil, status.Error(codes.FailedPrecondition, "heal event store is not configured")
	}
	server, err := s.healStore.GetRILServer(ctx, agent.Tenant, req.GetAgentId())
	if err != nil {
		return nil, status.Errorf(codes.FailedPrecondition, "canonical RIL server is unavailable: %v", err)
	}

	occurredAt := time.Now().UTC()
	if req.GetTimestampUnix() > 0 {
		occurredAt = time.Unix(req.GetTimestampUnix(), 0).UTC()
	}
	eventID := uuid.NewString()
	event, err := s.healStore.RecordHealEvent(ctx, controlplane.RILHealEvent{
		ID: eventID, TenantID: agent.Tenant, ServerID: server.ID,
		Status: healEventStatus(req.GetSuccess()), Cause: req.GetTriggerReason(),
		Details: map[string]any{
			"recipe_name": req.GetRecipeName(), "auto_executed": req.GetAutoExecuted(),
			"action_taken": req.GetActionTaken(), "rollback_info": req.GetRollbackAvailable(),
			"error_message": req.GetErrorMessage(), "severity": healEventSeverity(req.GetSeverity()),
			"occurred_at": occurredAt.Format(time.RFC3339Nano), "metadata": req.GetMetadata(),
		},
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "record heal event: %v", err)
	}
	return &agentpb.HealEventAck{Received: true, EventId: event.ID}, nil
}

func healEventStatus(success bool) string {
	if success {
		return "succeeded"
	}
	return "failed"
}

func healEventSeverity(value agentpb.HealEventSeverity) string {
	return strings.ToLower(strings.TrimPrefix(value.String(), "HEAL_SEVERITY_"))
}
