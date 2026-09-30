package grpcserver

import (
	"context"
	"encoding/hex"
	"math/big"
	"testing"
	"time"

	collectorlogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestOTLPLogsExportAddsCorrelatedRuntimeLog(t *testing.T) {
	srv := &Server{}
	service := newOTLPLogsService(srv)
	now := time.Now().UTC().Truncate(time.Second)
	traceID := []byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f, 0x10}
	spanID := []byte{0x11, 0x12, 0x13, 0x14, 0x15, 0x16, 0x17, 0x18}

	resp, err := service.Export(context.Background(), &collectorlogspb.ExportLogsServiceRequest{
		ResourceLogs: []*logspb.ResourceLogs{{
			Resource: &resourcepb.Resource{Attributes: []*commonpb.KeyValue{
				stringKV("service.name", "stackkit-hub"),
				stringKV("host.name", "vm-1"),
				stringKV("stack_id", "stack-otlp"),
				stringKV("lease_id", "lease-otlp"),
				stringKV("provider", "centron-managed"),
			}},
			ScopeLogs: []*logspb.ScopeLogs{{
				LogRecords: []*logspb.LogRecord{{
					TimeUnixNano: uint64(now.UnixNano()),
					SeverityText: "ERROR",
					Body:         stringValue("compose rollout failed token=abcdefghijklmnopqrstuvwxyz"),
					TraceId:      traceID,
					SpanId:       spanID,
					Attributes: []*commonpb.KeyValue{
						stringKV("job_id", "job-otlp"),
						stringKV("runtime_action_id", "verify-1"),
						stringKV("container.id", "container-1"),
					},
				}},
			}},
		}},
	})
	if err != nil {
		t.Fatalf("Export() failed: %v", err)
	}
	if resp.GetPartialSuccess() != nil {
		t.Fatalf("Export() partial success = %+v, want nil", resp.GetPartialSuccess())
	}

	logs := srv.GetRuntimeLogs(RuntimeLogQuery{StackID: "stack-otlp", Limit: 10})
	if len(logs) != 1 {
		t.Fatalf("GetRuntimeLogs() len = %d, want 1", len(logs))
	}
	entry := logs[0]
	if entry.Source != "otlp-logs" || entry.ServiceName != "stackkit-hub" || entry.Provider != "centron-managed" {
		t.Fatalf("OTLP runtime metadata missing: %+v", entry)
	}
	if entry.JobID != "job-otlp" || entry.LeaseID != "lease-otlp" || entry.RuntimeActionID != "verify-1" {
		t.Fatalf("OTLP correlation metadata missing: %+v", entry)
	}
	if entry.TraceID != hex.EncodeToString(traceID) || entry.SpanID != hex.EncodeToString(spanID) {
		t.Fatalf("trace/span not preserved: %+v", entry)
	}
	if entry.Fields["container.id"] != "container-1" {
		t.Fatalf("container metadata missing: %+v", entry.Fields)
	}
	if entry.Message == "compose rollout failed token=abcdefghijklmnopqrstuvwxyz" {
		t.Fatalf("OTLP log body was not redacted: %q", entry.Message)
	}
}

func TestOTLPLogsExportBindsIdentityToEnrolledPeer(t *testing.T) {
	cert := makeClientCert(t, "agent-1", "tenant-A", big.NewInt(61))
	revokedAt := time.Now()
	for _, tc := range []struct {
		name       string
		ctx        context.Context
		enrollment *AgentEnrollment
		standalone bool
		wantCode   codes.Code
	}{
		{name: "missing peer", ctx: context.Background(), wantCode: codes.Unauthenticated},
		{name: "unenrolled peer", ctx: ctxWithPeerCert(cert), wantCode: codes.PermissionDenied},
		{name: "revoked peer", ctx: ctxWithPeerCert(cert), enrollment: &AgentEnrollment{AgentID: "agent-1", TenantID: "tenant-A", CertSerial: cert.SerialNumber.String(), RevokedAt: &revokedAt}, wantCode: codes.PermissionDenied},
		{name: "enrolled peer", ctx: ctxWithPeerCert(cert), enrollment: &AgentEnrollment{AgentID: "agent-1", TenantID: "tenant-A", CertSerial: cert.SerialNumber.String()}},
		{name: "standalone", ctx: context.Background(), standalone: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := &Server{}
			if !tc.standalone {
				store := NewMemoryAgentEnrollmentStore()
				if tc.enrollment != nil {
					store.Set(*tc.enrollment)
				}
				srv = newTestServerWithEnrollment(t, store)
			}
			const message = "exporter identity regression"
			_, err := newOTLPLogsService(srv).Export(tc.ctx, &collectorlogspb.ExportLogsServiceRequest{
				ResourceLogs: []*logspb.ResourceLogs{{
					Resource: &resourcepb.Resource{Attributes: []*commonpb.KeyValue{stringKV("tenant_id", "tenant-B"), stringKV("agent_id", "agent-9")}},
					ScopeLogs: []*logspb.ScopeLogs{{
						Scope:      &commonpb.InstrumentationScope{Attributes: []*commonpb.KeyValue{stringKV("tenant.id", "tenant-B"), stringKV("agent.id", "agent-9")}},
						LogRecords: []*logspb.LogRecord{{Body: stringValue(message), Attributes: []*commonpb.KeyValue{stringKV("tenant_id", "tenant-B"), stringKV("agent_id", "agent-9")}}},
					}},
				}},
			})
			if status.Code(err) != tc.wantCode {
				t.Errorf("Export code = %v, want %v", status.Code(err), tc.wantCode)
			}
			if logs := srv.GetRuntimeLogs(RuntimeLogQuery{TenantID: "tenant-B"}); len(logs) != 0 {
				t.Errorf("exporter injected logs into another tenant: %+v", logs)
			}
			if logs := srv.GetRuntimeLogs(RuntimeLogQuery{AgentID: "agent-9"}); len(logs) != 0 {
				t.Errorf("exporter impersonated another agent: %+v", logs)
			}
			if tc.wantCode != codes.OK {
				if logs := srv.GetRuntimeLogs(RuntimeLogQuery{}); len(logs) != 0 {
					t.Errorf("rejected export persisted logs: %+v", logs)
				}
				return
			}
			query := RuntimeLogQuery{}
			if !tc.standalone {
				query.TenantID, query.AgentID = tc.enrollment.TenantID, tc.enrollment.AgentID
			}
			logs := srv.GetRuntimeLogs(query)
			if len(logs) != 1 || logs[0].Message != message {
				t.Fatalf("accepted export not readable by its owner: %+v", logs)
			}
			entry := logs[0]
			if entry.TenantID != query.TenantID || entry.AgentID != query.AgentID {
				t.Errorf("stored identity = %q/%q, want %q/%q", entry.TenantID, entry.AgentID, query.TenantID, query.AgentID)
			}
			for _, key := range []string{"tenant_id", "tenant.id", "agent_id", "agent.id"} {
				if value := entry.Fields[key]; value != "" && value != query.TenantID && value != query.AgentID {
					t.Errorf("untrusted identity attribute %s=%q survived ingestion", key, value)
				}
			}
		})
	}
}

func stringKV(key, value string) *commonpb.KeyValue {
	return &commonpb.KeyValue{Key: key, Value: stringValue(value)}
}

func stringValue(value string) *commonpb.AnyValue {
	return &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: value}}
}
