package grpcserver

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/api/agentpb"
	collectormetricspb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

func TestGRPCMetricsServices_Compatibility(t *testing.T) {
	srv := newTestServer(t)
	tsdb := newTestTSDB(t)
	srv.SetMonitorTSDB(tsdb)

	listener := bufconn.Listen(1024 * 1024)
	grpcSrv := grpc.NewServer()
	agentpb.RegisterAgentServiceServer(grpcSrv, srv)
	collectormetricspb.RegisterMetricsServiceServer(grpcSrv, srv)

	go func() {
		if err := grpcSrv.Serve(listener); err != nil {
			t.Logf("bufconn grpc server stopped: %v", err)
		}
	}()
	t.Cleanup(func() {
		grpcSrv.Stop()
		_ = listener.Close()
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, err := grpc.DialContext(ctx, "bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
			return listener.Dial()
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("failed to dial bufconn grpc server: %v", err)
	}
	t.Cleanup(func() {
		_ = conn.Close()
	})

	otlpClient := collectormetricspb.NewMetricsServiceClient(conn)
	legacyClient := agentpb.NewAgentServiceClient(conn)
	now := time.Now().UTC()

	otlpResp, err := otlpClient.Export(ctx, newTestOTLPRequest(now))
	if err != nil {
		t.Fatalf("OTLP Export failed: %v", err)
	}
	if otlpResp.GetPartialSuccess() != nil {
		t.Fatalf("expected full OTLP success, got partial success: %+v", otlpResp.GetPartialSuccess())
	}

	legacyResp, err := legacyClient.PushMetrics(ctx, &agentpb.MetricsBatch{
		AgentId:       "legacy-agent-bufconn",
		TimestampUnix: now.UnixMilli(),
		Samples: []*agentpb.MetricSample{{
			Name:          "legacy_node_temperature_celsius",
			Value:         48.2,
			Labels:        map[string]string{"host": "node-1"},
			TimestampUnix: now.UnixMilli(),
		}},
	})
	if err != nil {
		t.Fatalf("legacy PushMetrics failed: %v", err)
	}
	if !legacyResp.GetAccepted() {
		t.Fatalf("expected legacy PushMetrics accepted=true, got error: %s", legacyResp.GetError())
	}

	otlpResult, err := tsdb.InstantQuery(ctx, `system_cpu_utilization{ingest_protocol="otlp"}`, now)
	if err != nil {
		t.Fatalf("OTLP InstantQuery failed: %v", err)
	}
	legacyResult, err := tsdb.InstantQuery(ctx, `legacy_node_temperature_celsius{agent_id="legacy-agent-bufconn"}`, now)
	if err != nil {
		t.Fatalf("legacy InstantQuery failed: %v", err)
	}

	assertVectorHasSamples(t, otlpResult.Value, "OTLP")
	assertVectorHasSamples(t, legacyResult.Value, "legacy")
	if tsdb.SeriesCount() < 2 {
		t.Fatalf("expected at least 2 series after OTLP + legacy gRPC ingest, got %d", tsdb.SeriesCount())
	}
}
