package grpcserver

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/kombifyio/techstack/pkg/api/agentpb"
	"github.com/kombifyio/techstack/pkg/usernotice"
)

// SetUserNotices wires the opt-in `user_notification` class before the server
// starts. Without it every report is refused as unavailable; the class is
// never silently dropped.
func (s *Server) SetUserNotices(sink usernotice.Sink, nodes usernotice.NodeResolver) {
	s.userNotices = sink
	s.userNoticeNodes = nodes
}

// ReportUserNotification accepts one user-authored smart-home notice from an
// authenticated agent. The node identity comes from the agent's
// authentication, never from the message. A refusal (text over the bound, rate
// limit, unknown audience) is a structured ack, not a transport error.
func (s *Server) ReportUserNotification(ctx context.Context, req *agentpb.UserNotification) (*agentpb.UserNotificationAck, error) {
	if req.GetAgentId() == "" {
		return nil, status.Error(codes.InvalidArgument, "agent_id is required")
	}
	agent, err := s.authorizeRegisteredAgentRPC(ctx, req.GetAgentId())
	if err != nil {
		return nil, err
	}
	if s.userNotices == nil || s.userNoticeNodes == nil {
		return refusedUserNotification(usernotice.Result{RefusalCode: usernotice.RefusalNotConfigured}), nil
	}
	node, err := s.userNoticeNodes.ResolveNode(ctx, agent.Tenant, agent.ID)
	if err != nil {
		return refusedUserNotification(usernotice.Result{RefusalCode: usernotice.RefusalNodeUnresolved}), nil
	}
	result, err := s.userNotices.Report(ctx, node, usernotice.Notice{
		AutomationID: req.GetAutomationId(), Text: req.GetText(), Audience: req.GetAudience(),
		MemberUsername: req.GetMemberUsername(), Source: req.GetSource(),
	})
	if err != nil {
		return nil, status.Error(codes.Unavailable, "user notification could not be dispatched")
	}
	if !result.Accepted {
		return refusedUserNotification(result), nil
	}
	return &agentpb.UserNotificationAck{Accepted: true, Status: "accepted", Recipients: int32(result.Recipients)}, nil //nolint:gosec // bounded by household size
}

func refusedUserNotification(result usernotice.Result) *agentpb.UserNotificationAck {
	return &agentpb.UserNotificationAck{
		Accepted: false, Status: "refused", RefusalCode: result.RefusalCode,
		RetryAfterSeconds: int32(result.RetryAfter.Seconds()), //nolint:gosec // bounded by the one-hour window
	}
}
