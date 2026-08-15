package grpcserver

import (
	"context"
	"encoding/hex"
	"strings"
	"time"

	collectorlogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type otlpLogsService struct {
	collectorlogspb.UnimplementedLogsServiceServer
	server *Server
}

func newOTLPLogsService(server *Server) *otlpLogsService {
	return &otlpLogsService{server: server}
}

func (s *otlpLogsService) Export(ctx context.Context, req *collectorlogspb.ExportLogsServiceRequest) (*collectorlogspb.ExportLogsServiceResponse, error) {
	_ = ctx
	if s == nil || s.server == nil {
		return nil, status.Error(codes.Unavailable, "runtime log store not available")
	}
	for _, entry := range otlpLogsToRuntimeLogs(req) {
		s.server.appendRuntimeLog(entry)
	}
	return &collectorlogspb.ExportLogsServiceResponse{}, nil
}

func otlpLogsToRuntimeLogs(req *collectorlogspb.ExportLogsServiceRequest) []AgentLogEntry {
	if req == nil {
		return nil
	}
	entries := make([]AgentLogEntry, 0)
	now := time.Now().UTC()
	for _, resourceLogs := range req.GetResourceLogs() {
		resourceFields := otlpKeyValuesToFields(resourceLogs.GetResource().GetAttributes())
		for _, scopeLogs := range resourceLogs.GetScopeLogs() {
			scopeFields := cloneRuntimeLogFields(resourceFields)
			if scope := scopeLogs.GetScope(); scope != nil {
				if scope.GetName() != "" {
					scopeFields["otel.scope.name"] = scope.GetName()
				}
				if scope.GetVersion() != "" {
					scopeFields["otel.scope.version"] = scope.GetVersion()
				}
				mergeRuntimeLogFields(scopeFields, otlpKeyValuesToFields(scope.GetAttributes()))
			}
			for _, record := range scopeLogs.GetLogRecords() {
				fields := cloneRuntimeLogFields(scopeFields)
				mergeRuntimeLogFields(fields, otlpKeyValuesToFields(record.GetAttributes()))
				entries = append(entries, AgentLogEntry{
					Timestamp:   otlpLogTimestamp(record, now),
					IngestedAt:  now,
					Source:      "otlp-logs",
					Level:       otlpLogSeverity(record),
					Message:     strings.TrimSpace(otlpAnyValueString(record.GetBody())),
					TraceID:     hex.EncodeToString(record.GetTraceId()),
					SpanID:      hex.EncodeToString(record.GetSpanId()),
					Fields:      fields,
					ServiceName: firstRuntimeLogValue(fields["service.name"], fields["service_name"]),
					AgentID:     firstRuntimeLogValue(fields["agent_id"], fields["agent.id"]),
				})
			}
		}
	}
	return entries
}

func otlpKeyValuesToFields(attrs []*commonpb.KeyValue) map[string]string {
	if len(attrs) == 0 {
		return nil
	}
	fields := make(map[string]string, len(attrs))
	for _, attr := range attrs {
		if attr == nil {
			continue
		}
		key := strings.TrimSpace(attr.GetKey())
		value := strings.TrimSpace(otlpAnyValueString(attr.GetValue()))
		if key == "" || value == "" {
			continue
		}
		fields[key] = value
	}
	if len(fields) == 0 {
		return nil
	}
	return fields
}

func cloneRuntimeLogFields(fields map[string]string) map[string]string {
	if len(fields) == 0 {
		return map[string]string{}
	}
	out := make(map[string]string, len(fields))
	for key, value := range fields {
		out[key] = value
	}
	return out
}

func mergeRuntimeLogFields(dst, src map[string]string) {
	if dst == nil || len(src) == 0 {
		return
	}
	for key, value := range src {
		dst[key] = value
	}
}

func otlpLogTimestamp(record *logspb.LogRecord, fallback time.Time) time.Time {
	if record == nil {
		return fallback
	}
	if ts := record.GetTimeUnixNano(); ts > 0 {
		return otlpTimestamp(ts)
	}
	if observed := record.GetObservedTimeUnixNano(); observed > 0 {
		return otlpTimestamp(observed)
	}
	return fallback
}

func otlpLogSeverity(record *logspb.LogRecord) string {
	if record == nil {
		return "info"
	}
	if severity := strings.TrimSpace(record.GetSeverityText()); severity != "" {
		return severity
	}
	switch n := record.GetSeverityNumber(); {
	case n >= logspb.SeverityNumber_SEVERITY_NUMBER_FATAL:
		return "fatal"
	case n >= logspb.SeverityNumber_SEVERITY_NUMBER_ERROR:
		return "error"
	case n >= logspb.SeverityNumber_SEVERITY_NUMBER_WARN:
		return "warn"
	case n >= logspb.SeverityNumber_SEVERITY_NUMBER_INFO:
		return "info"
	case n >= logspb.SeverityNumber_SEVERITY_NUMBER_DEBUG:
		return "debug"
	default:
		return "info"
	}
}
