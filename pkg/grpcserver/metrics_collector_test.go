package grpcserver

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/api/agentpb"
	"github.com/kombifyio/techstack/pkg/logger"
	"github.com/kombifyio/techstack/pkg/monitoring"
	"github.com/prometheus/prometheus/promql"
	collectormetricspb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func assertVectorHasSamples(t *testing.T, value any, label string) promql.Vector {
	t.Helper()

	vec, ok := value.(promql.Vector)
	if !ok {
		t.Fatalf("expected %s query result to be promql.Vector, got %T", label, value)
	}
	if len(vec) == 0 {
		t.Fatalf("expected %s query result to contain samples", label)
	}
	return vec
}

// newTestServer creates a minimal Server for unit tests (no TLS, no persistence).
func newTestServer(t *testing.T) *Server {
	t.Helper()
	log := logger.New("error", "text")
	srv, err := New(Config{ListenAddr: ":0"}, log)
	if err != nil {
		t.Fatalf("failed to create test server: %v", err)
	}
	return srv
}

// newTestTSDB creates a real MonitorTSDB backed by a temp directory.
// The caller does not need to close it; t.Cleanup handles teardown.
func newTestTSDB(t *testing.T) *monitoring.MonitorTSDB {
	t.Helper()
	dir := t.TempDir()
	tsdb, err := monitoring.NewMonitorTSDB(monitoring.TSDBConfig{
		DataDir:   dir,
		Retention: 24 * time.Hour,
		Logger:    slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})),
	})
	if err != nil {
		t.Fatalf("failed to create test TSDB: %v", err)
	}
	t.Cleanup(func() {
		if err := tsdb.Close(); err != nil {
			t.Errorf("failed to close test TSDB: %v", err)
		}
	})
	return tsdb
}

func TestPushMetrics_NoTSDB(t *testing.T) {
	srv := newTestServer(t)
	// Do not set monitorTSDB -- it stays nil.

	ack, err := srv.PushMetrics(context.Background(), &agentpb.MetricsBatch{
		AgentId:       "agent-1",
		TimestampUnix: time.Now().UnixMilli(),
		Samples: []*agentpb.MetricSample{
			{Name: "cpu_usage", Value: 42.5, TimestampUnix: time.Now().UnixMilli()},
		},
	})
	if err != nil {
		t.Fatalf("expected no gRPC error, got: %v", err)
	}
	if ack.Accepted {
		t.Error("expected accepted=false when TSDB is nil")
	}
	if ack.Error != "monitoring not enabled" {
		t.Errorf("expected error message %q, got %q", "monitoring not enabled", ack.Error)
	}
	if ack.SamplesAccepted != 0 {
		t.Errorf("expected samples_accepted=0, got %d", ack.SamplesAccepted)
	}
}

func TestPushMetrics_EmptyAgentID(t *testing.T) {
	srv := newTestServer(t)
	srv.SetMonitorTSDB(newTestTSDB(t))

	ack, err := srv.PushMetrics(context.Background(), &agentpb.MetricsBatch{
		AgentId:       "",
		TimestampUnix: time.Now().UnixMilli(),
		Samples: []*agentpb.MetricSample{
			{Name: "cpu_usage", Value: 10, TimestampUnix: time.Now().UnixMilli()},
		},
	})
	if ack != nil {
		t.Errorf("expected nil ack on InvalidArgument, got %+v", ack)
	}
	if err == nil {
		t.Fatal("expected gRPC error for empty agent_id, got nil")
	}

	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got: %v", err)
	}
	if st.Code() != codes.InvalidArgument {
		t.Errorf("expected code InvalidArgument, got %v", st.Code())
	}
	if st.Message() != "agent_id is required" {
		t.Errorf("expected message %q, got %q", "agent_id is required", st.Message())
	}
}

func TestPushMetrics_EmptySamples(t *testing.T) {
	srv := newTestServer(t)
	srv.SetMonitorTSDB(newTestTSDB(t))

	ack, err := srv.PushMetrics(context.Background(), &agentpb.MetricsBatch{
		AgentId:       "agent-1",
		TimestampUnix: time.Now().UnixMilli(),
		Samples:       nil,
	})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !ack.Accepted {
		t.Error("expected accepted=true for empty samples batch")
	}
	if ack.SamplesAccepted != 0 {
		t.Errorf("expected samples_accepted=0, got %d", ack.SamplesAccepted)
	}
}

func TestPushMetrics_Success(t *testing.T) {
	srv := newTestServer(t)
	tsdb := newTestTSDB(t)
	srv.SetMonitorTSDB(tsdb)

	now := time.Now()
	samples := []*agentpb.MetricSample{
		{
			Name:          "cpu_usage",
			Value:         72.5,
			Labels:        map[string]string{"host": "node-1"},
			TimestampUnix: now.UnixMilli(),
		},
		{
			Name:          "mem_usage",
			Value:         4096,
			Labels:        map[string]string{"host": "node-1"},
			TimestampUnix: now.UnixMilli(),
		},
		{
			Name:          "disk_free",
			Value:         1024000,
			TimestampUnix: now.UnixMilli(),
			// Labels intentionally nil to exercise the nil-labels path
		},
	}

	ack, err := srv.PushMetrics(context.Background(), &agentpb.MetricsBatch{
		AgentId:       "agent-42",
		TimestampUnix: now.UnixMilli(),
		Samples:       samples,
	})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !ack.Accepted {
		t.Errorf("expected accepted=true, got false (error: %s)", ack.Error)
	}
	if ack.SamplesAccepted != int32(len(samples)) {
		t.Errorf("expected samples_accepted=%d, got %d", len(samples), ack.SamplesAccepted)
	}

	// Verify data is actually in TSDB by querying series count.
	count := tsdb.SeriesCount()
	if count < uint64(len(samples)) {
		t.Errorf("expected at least %d series in TSDB, got %d", len(samples), count)
	}
}

func TestPushMetrics_AddsAgentIDLabel(t *testing.T) {
	srv := newTestServer(t)
	tsdb := newTestTSDB(t)
	srv.SetMonitorTSDB(tsdb)

	now := time.Now()
	agentID := "agent-label-test"

	ack, err := srv.PushMetrics(context.Background(), &agentpb.MetricsBatch{
		AgentId:       agentID,
		TimestampUnix: now.UnixMilli(),
		Samples: []*agentpb.MetricSample{
			{
				Name:          "test_metric",
				Value:         99.9,
				Labels:        map[string]string{"env": "staging"},
				TimestampUnix: now.UnixMilli(),
			},
		},
	})
	if err != nil {
		t.Fatalf("PushMetrics failed: %v", err)
	}
	if !ack.Accepted {
		t.Fatalf("expected accepted=true, got false (error: %s)", ack.Error)
	}

	// Query the TSDB for the metric we just pushed and inspect labels.
	ctx := context.Background()
	result, err := tsdb.InstantQuery(ctx, `test_metric{env="staging"}`, now)
	if err != nil {
		t.Fatalf("InstantQuery failed: %v", err)
	}

	vec := assertVectorHasSamples(t, result.Value, "PushMetrics")

	sample := vec[0]
	gotAgent := sample.Metric.Get("agent_id")
	if gotAgent != agentID {
		t.Errorf("expected agent_id label %q, got %q", agentID, gotAgent)
	}
	gotEnv := sample.Metric.Get("env")
	if gotEnv != "staging" {
		t.Errorf("expected env label %q, got %q", "staging", gotEnv)
	}
}

func TestExportOTLPMetrics_NoTSDB(t *testing.T) {
	srv := newTestServer(t)
	req := newTestOTLPRequest(time.Now())

	resp, err := srv.Export(context.Background(), req)
	if err != nil {
		t.Fatalf("expected no OTLP gRPC error, got: %v", err)
	}
	if resp.GetPartialSuccess() == nil {
		t.Fatal("expected partial success when TSDB is nil")
	}
	if resp.GetPartialSuccess().GetRejectedDataPoints() != 1 {
		t.Fatalf("expected rejected_data_points=1, got %d", resp.GetPartialSuccess().GetRejectedDataPoints())
	}
	if resp.GetPartialSuccess().GetErrorMessage() != "monitoring not enabled" {
		t.Fatalf("expected error message %q, got %q", "monitoring not enabled", resp.GetPartialSuccess().GetErrorMessage())
	}
}

func TestExportOTLPMetrics_Success(t *testing.T) {
	srv := newTestServer(t)
	tsdb := newTestTSDB(t)
	srv.SetMonitorTSDB(tsdb)

	now := time.Now().UTC()
	resp, err := srv.Export(context.Background(), newTestOTLPRequest(now))
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if resp.GetPartialSuccess() != nil {
		t.Fatalf("expected full success, got partial success: %+v", resp.GetPartialSuccess())
	}

	result, err := tsdb.InstantQuery(context.Background(), `system_cpu_utilization{host_name="node-1",ingest_protocol="otlp",cpu="0"}`, now)
	if err != nil {
		t.Fatalf("InstantQuery failed: %v", err)
	}

	vec := assertVectorHasSamples(t, result.Value, "OTLP")

	sample := vec[0]
	if got := sample.Metric.Get("host_name"); got != "node-1" {
		t.Fatalf("expected host_name label %q, got %q", "node-1", got)
	}
	if got := sample.Metric.Get("service_name"); got != "otel-agent" {
		t.Fatalf("expected service_name label %q, got %q", "otel-agent", got)
	}
	if got := sample.Metric.Get("ingest_protocol"); got != "otlp" {
		t.Fatalf("expected ingest_protocol label %q, got %q", "otlp", got)
	}
}

func TestExportOTLPMetrics_CoexistsWithPushMetrics(t *testing.T) {
	srv := newTestServer(t)
	tsdb := newTestTSDB(t)
	srv.SetMonitorTSDB(tsdb)

	now := time.Now().UTC()
	if _, err := srv.Export(context.Background(), newTestOTLPRequest(now)); err != nil {
		t.Fatalf("Export failed: %v", err)
	}
	if _, err := srv.PushMetrics(context.Background(), &agentpb.MetricsBatch{
		AgentId:       "legacy-agent",
		TimestampUnix: now.UnixMilli(),
		Samples: []*agentpb.MetricSample{{
			Name:          "legacy_cpu_usage",
			Value:         55.5,
			Labels:        map[string]string{"host": "node-1"},
			TimestampUnix: now.UnixMilli(),
		}},
	}); err != nil {
		t.Fatalf("PushMetrics failed: %v", err)
	}

	otlpResult, err := tsdb.InstantQuery(context.Background(), `system_cpu_utilization{ingest_protocol="otlp"}`, now)
	if err != nil {
		t.Fatalf("OTLP InstantQuery failed: %v", err)
	}
	legacyResult, err := tsdb.InstantQuery(context.Background(), `legacy_cpu_usage{agent_id="legacy-agent"}`, now)
	if err != nil {
		t.Fatalf("legacy InstantQuery failed: %v", err)
	}

	assertVectorHasSamples(t, otlpResult.Value, "OTLP")
	assertVectorHasSamples(t, legacyResult.Value, "legacy")
	if tsdb.SeriesCount() < 2 {
		t.Fatalf("expected at least 2 series after mixed OTLP + PushMetrics ingest, got %d", tsdb.SeriesCount())
	}
}

func TestMonitoringIngestHealth_TracksOTLPAndLegacyFlows(t *testing.T) {
	srv := newTestServer(t)
	now := time.Now().UTC()

	if _, err := srv.Export(context.Background(), newTestOTLPRequest(now)); err != nil {
		t.Fatalf("initial OTLP Export failed: %v", err)
	}

	tsdb := newTestTSDB(t)
	srv.SetMonitorTSDB(tsdb)

	if _, err := srv.Export(context.Background(), newTestOTLPRequest(now)); err != nil {
		t.Fatalf("OTLP Export with TSDB failed: %v", err)
	}
	if _, err := srv.PushMetrics(context.Background(), &agentpb.MetricsBatch{}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument for missing agent_id, got %v", err)
	}
	ack, err := srv.PushMetrics(context.Background(), &agentpb.MetricsBatch{
		AgentId:       "agent-1",
		TimestampUnix: now.UnixMilli(),
		Samples: []*agentpb.MetricSample{{
			Name:          "cpu_usage",
			Value:         42.5,
			TimestampUnix: now.UnixMilli(),
		}},
	})
	if err != nil {
		t.Fatalf("legacy PushMetrics failed: %v", err)
	}
	if !ack.GetAccepted() {
		t.Fatalf("expected accepted legacy PushMetrics ack, got %#v", ack)
	}

	health := srv.MonitoringIngestHealth()
	if health.OTLP.RequestsRejected != 1 {
		t.Fatalf("expected OTLP RequestsRejected=1, got %d", health.OTLP.RequestsRejected)
	}
	if health.OTLP.RequestsAccepted != 1 {
		t.Fatalf("expected OTLP RequestsAccepted=1, got %d", health.OTLP.RequestsAccepted)
	}
	if health.OTLP.Status != "ok" {
		t.Fatalf("expected OTLP status ok after successful retry, got %s", health.OTLP.Status)
	}
	if health.OTLP.QueueMode != "synchronous" || health.OTLP.QueueDepth != 0 {
		t.Fatalf("expected synchronous OTLP ingest with zero queue depth, got mode=%s depth=%d", health.OTLP.QueueMode, health.OTLP.QueueDepth)
	}
	if health.LegacyPush.ParseErrors != 1 {
		t.Fatalf("expected legacy parseErrors=1, got %d", health.LegacyPush.ParseErrors)
	}
	if health.LegacyPush.RequestsAccepted != 1 {
		t.Fatalf("expected legacy RequestsAccepted=1, got %d", health.LegacyPush.RequestsAccepted)
	}
	if health.LegacyPush.Status != "ok" {
		t.Fatalf("expected legacy status ok after successful write, got %s", health.LegacyPush.Status)
	}
	if health.LegacyPush.LastFailureAt == nil {
		t.Fatal("expected legacy last failure timestamp to be populated")
	}
	if health.LegacyPush.LastSuccessAt == nil {
		t.Fatal("expected legacy last success timestamp to be populated")
	}
}

func newTestOTLPRequest(ts time.Time) *collectormetricspb.ExportMetricsServiceRequest {
	return &collectormetricspb.ExportMetricsServiceRequest{
		ResourceMetrics: []*metricspb.ResourceMetrics{{
			Resource: &resourcepb.Resource{Attributes: []*commonpb.KeyValue{
				{Key: "host.name", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: "node-1"}}},
				{Key: "service.name", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: "otel-agent"}}},
			}},
			ScopeMetrics: []*metricspb.ScopeMetrics{{
				Metrics: []*metricspb.Metric{{
					Name: "system.cpu.utilization",
					Data: &metricspb.Metric_Gauge{Gauge: &metricspb.Gauge{DataPoints: []*metricspb.NumberDataPoint{{
						Attributes: []*commonpb.KeyValue{{
							Key:   "cpu",
							Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: "0"}},
						}},
						TimeUnixNano: uint64(ts.UnixNano()),
						Value:        &metricspb.NumberDataPoint_AsDouble{AsDouble: 0.72},
					}}}},
				}},
			}},
		}},
	}
}
