package grpcserver

import (
	"context"
	"time"

	"github.com/kombifyio/techstack/pkg/api/agentpb"
	"github.com/kombifyio/techstack/pkg/monitoring"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// SetMonitorTSDB configures the TSDB backend for metric ingestion.
// Must be called before the server starts accepting PushMetrics calls.
func (s *Server) SetMonitorTSDB(tsdb *monitoring.MonitorTSDB) {
	s.monitorTSDB = tsdb
}

// MonitorTSDB returns the monitoring TSDB instance (for query API access).
func (s *Server) MonitorTSDB() *monitoring.MonitorTSDB {
	return s.monitorTSDB
}

// PushMetrics implements agentpb.AgentServiceServer.PushMetrics
// Receives batched metric samples from agents and writes them to the embedded TSDB.
func (s *Server) PushMetrics(ctx context.Context, req *agentpb.MetricsBatch) (*agentpb.PushAck, error) {
	if s.monitorTSDB == nil {
		if s.monitorIngestHealth != nil {
			s.monitorIngestHealth.recordLegacyRejected(0, "monitoring not enabled")
		}
		return &agentpb.PushAck{
			Accepted:        false,
			SamplesAccepted: 0,
			Error:           "monitoring not enabled",
		}, nil
	}

	if req.AgentId == "" {
		if s.monitorIngestHealth != nil {
			s.monitorIngestHealth.recordLegacyParseError("agent_id is required")
		}
		return nil, status.Error(codes.InvalidArgument, "agent_id is required")
	}
	var connectedAgent *ConnectedAgent
	if s.requiresAgentIdentityBinding() {
		var err error
		connectedAgent, err = s.authorizeRegisteredAgentRPC(ctx, req.AgentId)
		if err != nil {
			if s.monitorIngestHealth != nil {
				s.monitorIngestHealth.recordLegacyRejected(len(req.Samples), err.Error())
			}
			return nil, err
		}
	}

	if len(req.Samples) == 0 {
		if s.monitorIngestHealth != nil {
			s.monitorIngestHealth.recordLegacySuccess(0)
		}
		return &agentpb.PushAck{Accepted: true, SamplesAccepted: 0}, nil
	}

	// Convert protobuf samples to monitoring.MetricSample
	samples := make([]monitoring.MetricSample, len(req.Samples))
	for i, ps := range req.Samples {
		lbls := ps.Labels
		if lbls == nil {
			lbls = map[string]string{}
		}
		// Tag every sample with the agent that produced it
		lbls["agent_id"] = req.AgentId
		if connectedAgent != nil && connectedAgent.Tenant != "" {
			lbls["tenant_id"] = connectedAgent.Tenant
		}

		samples[i] = monitoring.MetricSample{
			Name:      ps.Name,
			Value:     ps.Value,
			Labels:    lbls,
			Timestamp: time.UnixMilli(ps.TimestampUnix),
		}
	}

	if err := s.monitorTSDB.Write(samples); err != nil {
		if s.monitorIngestHealth != nil {
			s.monitorIngestHealth.recordLegacyRejected(len(samples), err.Error())
		}
		s.log.Warn("metrics_push_write_error",
			"agent_id", req.AgentId,
			"samples", len(samples),
			"error", err.Error(),
		)
		return &agentpb.PushAck{
			Accepted:        false,
			SamplesAccepted: 0,
			Error:           err.Error(),
		}, nil
	}

	s.log.Debug("metrics_push_accepted",
		"agent_id", req.AgentId,
		"samples", len(samples),
	)
	if s.monitorIngestHealth != nil {
		s.monitorIngestHealth.recordLegacySuccess(len(samples))
	}

	return &agentpb.PushAck{
		Accepted:        true,
		SamplesAccepted: clampIntToInt32(len(samples)),
	}, nil
}
