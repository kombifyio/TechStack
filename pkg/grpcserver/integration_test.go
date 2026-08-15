// Package grpcserver integration tests.
// These tests require a Docker-compatible engine, locally or through DOCKER_HOST.
//
//go:build integration
// +build integration

package grpcserver_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/api/agentpb"
	"github.com/kombifyio/techstack/pkg/testutil"
	"github.com/golang/snappy"
	"github.com/prometheus/prometheus/prompb"
	"github.com/testcontainers/testcontainers-go"
	tcnetwork "github.com/testcontainers/testcontainers-go/network"
	"github.com/testcontainers/testcontainers-go/wait"
	collectormetricspb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

// TestIntegration_AgentRegistration tests agent registration with a real kombifyTechstack container.
func TestIntegration_AgentRegistration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	// Start real kombifyTechstack container
	ks := testutil.StartkombifyTechstack(t, ctx)
	ks.HealthCheck(t)

	// Connect to gRPC server
	conn, err := grpc.DialContext(ctx, ks.GRPCAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithBlock(),
	)
	if err != nil {
		t.Fatalf("failed to connect to gRPC: %v", err)
	}
	defer conn.Close()

	client := agentpb.NewAgentServiceClient(conn)

	// Test registration (matching proto schema)
	regReq := &agentpb.RegisterRequest{
		AgentId:      "test-agent-integration-001",
		Hostname:     "test-host",
		Os:           "linux",
		Arch:         "amd64",
		Version:      "1.0.0-test",
		Capabilities: []string{"exec", "deploy"},
	}

	regResp, err := client.Register(ctx, regReq)
	if err != nil {
		// Log container logs for debugging
		logs, _ := ks.Logs(ctx)
		t.Fatalf("Register failed: %v\nContainer logs:\n%s", err, logs)
	}

	if !regResp.Accepted {
		t.Errorf("Register returned accepted=false")
	}

	t.Logf("Agent registered successfully: assigned_id=%s", regResp.AssignedId)
}

// TestIntegration_AgentHeartbeat tests heartbeat with a real kombifyTechstack container.
func TestIntegration_AgentHeartbeat(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	// Start real kombifyTechstack container
	ks := testutil.StartkombifyTechstack(t, ctx)
	ks.HealthCheck(t)

	// Connect to gRPC server
	conn, err := grpc.DialContext(ctx, ks.GRPCAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithBlock(),
	)
	if err != nil {
		t.Fatalf("failed to connect to gRPC: %v", err)
	}
	defer conn.Close()

	client := agentpb.NewAgentServiceClient(conn)

	// First register
	regReq := &agentpb.RegisterRequest{
		AgentId:  "test-agent-heartbeat-001",
		Hostname: "test-host",
		Os:       "linux",
		Arch:     "amd64",
		Version:  "1.0.0-test",
	}

	_, err = client.Register(ctx, regReq)
	if err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	// Then heartbeat
	hbReq := &agentpb.HeartbeatRequest{
		AgentId:       "test-agent-heartbeat-001",
		TimestampUnix: time.Now().Unix(),
		Resources: &agentpb.ResourceUsage{
			CpuPercent:       25.0,
			MemoryUsedBytes:  1024 * 1024 * 512,  // 512MB
			MemoryTotalBytes: 1024 * 1024 * 1024, // 1GB
		},
	}

	hbResp, err := client.Heartbeat(ctx, hbReq)
	if err != nil {
		logs, _ := ks.Logs(ctx)
		t.Fatalf("Heartbeat failed: %v\nContainer logs:\n%s", err, logs)
	}

	if !hbResp.Acknowledged {
		t.Errorf("Heartbeat not acknowledged")
	}

	t.Logf("Heartbeat successful, server_timestamp: %d", hbResp.ServerTimestampUnix)
}

// TestIntegration_CommandStream tests bidirectional command streaming.
func TestIntegration_CommandStream(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	// Start real kombifyTechstack container
	ks := testutil.StartkombifyTechstack(t, ctx)
	ks.HealthCheck(t)

	// Connect to gRPC server
	conn, err := grpc.DialContext(ctx, ks.GRPCAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithBlock(),
	)
	if err != nil {
		t.Fatalf("failed to connect to gRPC: %v", err)
	}
	defer conn.Close()

	client := agentpb.NewAgentServiceClient(conn)

	// First register
	regReq := &agentpb.RegisterRequest{
		AgentId:  "test-agent-stream-001",
		Hostname: "test-host",
		Os:       "linux",
		Arch:     "amd64",
		Version:  "1.0.0-test",
	}

	_, err = client.Register(ctx, regReq)
	if err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	// Open command stream
	stream, err := client.CommandStream(ctx)
	if err != nil {
		t.Fatalf("CommandStream failed: %v", err)
	}

	// Send initial message with command result
	err = stream.Send(&agentpb.AgentMessage{
		AgentId: "test-agent-stream-001",
		Payload: &agentpb.AgentMessage_CommandResult{
			CommandResult: &agentpb.CommandResult{
				CommandId:      "init",
				ExitCode:       0,
				Stdout:         "stream connected",
				StartedAtUnix:  time.Now().Unix(),
				FinishedAtUnix: time.Now().Unix(),
			},
		},
	})
	if err != nil {
		t.Fatalf("Send failed: %v", err)
	}

	t.Log("Command stream established successfully")

	// Close stream gracefully
	if err := stream.CloseSend(); err != nil {
		t.Logf("warning: CloseSend failed: %v", err)
	}
}

func TestIntegration_OTLPMetricsCompatibility(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	otlpNetwork, err := tcnetwork.New(ctx)
	if err != nil {
		t.Fatalf("failed to create OTLP collector test network: %v", err)
	}
	testcontainers.CleanupNetwork(t, otlpNetwork)

	ks := testutil.StartkombifyTechstackWithEnvAndRequestMutator(t, ctx, map[string]string{
		"TECHSTACK_ALERT_INTERVAL": "1s",
	}, func(req *testcontainers.ContainerRequest) {
		req.Networks = append(req.Networks, otlpNetwork.Name)
		if req.NetworkAliases == nil {
			req.NetworkAliases = map[string][]string{}
		}
		req.NetworkAliases[otlpNetwork.Name] = append(req.NetworkAliases[otlpNetwork.Name], "techstack")
	})
	ks.HealthCheck(t)

	collector := startOTLPCollector(t, ctx, otlpNetwork.Name, "techstack:5263")
	collectorConn, err := grpc.DialContext(ctx, collector.GRPCAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithBlock(),
	)
	if err != nil {
		t.Fatalf("failed to connect to OTLP collector gRPC: %v", err)
	}
	defer collectorConn.Close()

	conn, err := grpc.DialContext(ctx, ks.GRPCAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithBlock(),
	)
	if err != nil {
		t.Fatalf("failed to connect to TechStack gRPC: %v", err)
	}
	defer conn.Close()

	otlpClient := collectormetricspb.NewMetricsServiceClient(collectorConn)
	legacyClient := agentpb.NewAgentServiceClient(conn)
	now := time.Now().UTC()

	otlpResp, err := otlpClient.Export(ctx, newIntegrationOTLPRequest(now))
	if err != nil {
		logs, _ := ks.Logs(ctx)
		t.Fatalf("OTLP Export failed: %v\nContainer logs:\n%s", err, logs)
	}
	if ps := otlpResp.GetPartialSuccess(); ps != nil && (ps.GetRejectedDataPoints() > 0 || ps.GetErrorMessage() != "") {
		logs, _ := ks.Logs(ctx)
		t.Fatalf("expected full OTLP success, got partial success: %+v\nContainer logs:\n%s", ps, logs)
	}

	legacyResp, err := legacyClient.PushMetrics(ctx, &agentpb.MetricsBatch{
		AgentId:       "integration-legacy-agent-001",
		TimestampUnix: now.UnixMilli(),
		Samples: []*agentpb.MetricSample{{
			Name:          "legacy_node_temperature_celsius",
			Value:         48.2,
			Labels:        map[string]string{"host": "node-1"},
			TimestampUnix: now.UnixMilli(),
		}},
	})
	if err != nil {
		logs, _ := ks.Logs(ctx)
		t.Fatalf("PushMetrics failed: %v\nContainer logs:\n%s", err, logs)
	}
	if !legacyResp.GetAccepted() {
		logs, _ := ks.Logs(ctx)
		t.Fatalf("expected legacy PushMetrics accepted=true, got error: %s\nContainer logs:\n%s", legacyResp.GetError(), logs)
	}

	token := ks.Authenticate(t, ctx)

	type listResponse struct {
		Data []string `json:"data"`
	}
	type statusResponse struct {
		Data struct {
			SeriesCount uint64 `json:"seriesCount"`
		} `json:"data"`
	}
	type queryResponse struct {
		Data struct {
			ResultType string `json:"resultType"`
			Result     string `json:"result"`
		} `json:"data"`
	}
	type healthResponse struct {
		Data struct {
			QueryBackend       string `json:"queryBackend"`
			QueryBackendStatus string `json:"queryBackendStatus"`
			IngestBackend      string `json:"ingestBackend"`
			IngestStatus       string `json:"ingestStatus"`
			OTLP               struct {
				Status           string `json:"status"`
				QueueMode        string `json:"queueMode"`
				RequestsAccepted uint64 `json:"requestsAccepted"`
			} `json:"otlp"`
			LegacyPush struct {
				Status           string `json:"status"`
				RequestsAccepted uint64 `json:"requestsAccepted"`
			} `json:"legacyPush"`
		} `json:"data"`
	}
	type alertState struct {
		Rule struct {
			Name string `json:"name"`
			Expr string `json:"expr"`
		} `json:"rule"`
		Active bool    `json:"active"`
		Value  float64 `json:"value"`
	}
	type alertsResponse struct {
		Data []alertState `json:"data"`
	}

	fetchList := func(path string) ([]string, error) {
		resp, err := ks.AuthenticatedAPIRequest(ctx, http.MethodGet, path, nil, token)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()

		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("unexpected status %d for %s: %s", resp.StatusCode, path, string(body))
		}

		var payload listResponse
		if err := json.Unmarshal(body, &payload); err != nil {
			return nil, fmt.Errorf("decode %s: %w", path, err)
		}
		return payload.Data, nil
	}

	fetchStatus := func() (uint64, error) {
		resp, err := ks.AuthenticatedAPIRequest(ctx, http.MethodGet, "/api/v1/monitor/status", nil, token)
		if err != nil {
			return 0, err
		}
		defer resp.Body.Close()

		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK {
			return 0, fmt.Errorf("unexpected status %d for /api/v1/monitor/status: %s", resp.StatusCode, string(body))
		}

		var payload statusResponse
		if err := json.Unmarshal(body, &payload); err != nil {
			return 0, fmt.Errorf("decode /api/v1/monitor/status: %w", err)
		}
		return payload.Data.SeriesCount, nil
	}

	fetchHealth := func() (*healthResponse, error) {
		resp, err := ks.AuthenticatedAPIRequest(ctx, http.MethodGet, "/api/v1/monitor/health", nil, token)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()

		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("unexpected status %d for /api/v1/monitor/health: %s", resp.StatusCode, string(body))
		}

		var payload healthResponse
		if err := json.Unmarshal(body, &payload); err != nil {
			return nil, fmt.Errorf("decode /api/v1/monitor/health: %w", err)
		}
		return &payload, nil
	}

	fetchQuery := func(path string) (*queryResponse, error) {
		resp, err := ks.AuthenticatedAPIRequest(ctx, http.MethodGet, path, nil, token)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()

		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("unexpected status %d for %s: %s", resp.StatusCode, path, string(body))
		}

		var payload queryResponse
		if err := json.Unmarshal(body, &payload); err != nil {
			return nil, fmt.Errorf("decode %s: %w", path, err)
		}
		return &payload, nil
	}

	fetchAlerts := func(path string) ([]alertState, error) {
		resp, err := ks.AuthenticatedAPIRequest(ctx, http.MethodGet, path, nil, token)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()

		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("unexpected status %d for %s: %s", resp.StatusCode, path, string(body))
		}

		var payload alertsResponse
		if err := json.Unmarshal(body, &payload); err != nil {
			return nil, fmt.Errorf("decode %s: %w", path, err)
		}
		return payload.Data, nil
	}

	waitForListValue := func(path, expected string) {
		deadline := time.Now().Add(15 * time.Second)
		var last []string
		var lastErr error
		for time.Now().Before(deadline) {
			last, lastErr = fetchList(path)
			if lastErr == nil && slices.Contains(last, expected) {
				return
			}
			time.Sleep(500 * time.Millisecond)
		}
		logs, _ := ks.Logs(ctx)
		t.Fatalf("timed out waiting for %q in %s (last=%v err=%v)\nContainer logs:\n%s", expected, path, last, lastErr, logs)
	}

	waitForSeriesCount := func(min uint64) {
		deadline := time.Now().Add(15 * time.Second)
		var last uint64
		var lastErr error
		for time.Now().Before(deadline) {
			last, lastErr = fetchStatus()
			if lastErr == nil && last >= min {
				return
			}
			time.Sleep(500 * time.Millisecond)
		}
		logs, _ := ks.Logs(ctx)
		t.Fatalf("timed out waiting for seriesCount >= %d (last=%d err=%v)\nContainer logs:\n%s", min, last, lastErr, logs)
	}

	waitForHealth := func() *healthResponse {
		deadline := time.Now().Add(15 * time.Second)
		var last *healthResponse
		var lastErr error
		for time.Now().Before(deadline) {
			last, lastErr = fetchHealth()
			if lastErr == nil &&
				last.Data.QueryBackendStatus == "ok" &&
				last.Data.IngestStatus == "ok" &&
				last.Data.OTLP.RequestsAccepted >= 1 &&
				last.Data.LegacyPush.RequestsAccepted >= 1 {
				return last
			}
			time.Sleep(500 * time.Millisecond)
		}
		logs, _ := ks.Logs(ctx)
		t.Fatalf("timed out waiting for /api/v1/monitor/health to report healthy ingest (last=%+v err=%v)\nContainer logs:\n%s", last, lastErr, logs)
		return nil
	}

	waitForQueryResult := func(path, expectedType, expectedContains string) {
		deadline := time.Now().Add(15 * time.Second)
		var last *queryResponse
		var lastErr error
		for time.Now().Before(deadline) {
			last, lastErr = fetchQuery(path)
			if lastErr == nil &&
				last.Data.ResultType == expectedType &&
				strings.Contains(last.Data.Result, expectedContains) {
				return
			}
			time.Sleep(500 * time.Millisecond)
		}
		logs, _ := ks.Logs(ctx)
		t.Fatalf("timed out waiting for query %s to return %s containing %q (last=%+v err=%v)\nContainer logs:\n%s", path, expectedType, expectedContains, last, lastErr, logs)
	}

	waitForAlertRule := func(ruleName string) {
		deadline := time.Now().Add(15 * time.Second)
		var last []alertState
		var lastErr error
		for time.Now().Before(deadline) {
			last, lastErr = fetchAlerts("/api/v1/monitor/alerts/rules")
			if lastErr == nil {
				for _, state := range last {
					if state.Rule.Name == ruleName {
						return
					}
				}
			}
			time.Sleep(500 * time.Millisecond)
		}
		logs, _ := ks.Logs(ctx)
		t.Fatalf("timed out waiting for alert rule %q (last=%+v err=%v)\nContainer logs:\n%s", ruleName, last, lastErr, logs)
	}

	waitForActiveAlert := func(ruleName string) {
		deadline := time.Now().Add(40 * time.Second)
		var last []alertState
		var lastErr error
		for time.Now().Before(deadline) {
			last, lastErr = fetchAlerts("/api/v1/monitor/alerts")
			if lastErr == nil {
				for _, state := range last {
					if state.Rule.Name == ruleName && state.Active {
						return
					}
				}
			}
			time.Sleep(500 * time.Millisecond)
		}
		logs, _ := ks.Logs(ctx)
		t.Fatalf("timed out waiting for active alert %q (last=%+v err=%v)\nContainer logs:\n%s", ruleName, last, lastErr, logs)
	}

	waitForSeriesCount(2)
	waitForListValue("/api/v1/monitor/metrics/names", "system_cpu_utilization")
	waitForListValue("/api/v1/monitor/metrics/names", "node_disk_usage_percent")
	waitForListValue("/api/v1/monitor/metrics/names", "legacy_node_temperature_celsius")
	waitForListValue("/api/v1/monitor/metrics/values?name=ingest_protocol", "otlp")
	waitForListValue("/api/v1/monitor/metrics/values?name=agent_id", "integration-legacy-agent-001")
	waitForQueryResult("/api/v1/monitor/metrics/instant?q="+url.QueryEscape(`system_cpu_utilization{ingest_protocol="otlp"}`), "vector", "system_cpu_utilization")
	waitForQueryResult(
		"/api/v1/monitor/metrics/query?"+url.Values{
			"q":     []string{`system_cpu_utilization{ingest_protocol="otlp"}`},
			"start": []string{now.Add(-1 * time.Minute).Format(time.RFC3339)},
			"end":   []string{now.Add(1 * time.Minute).Format(time.RFC3339)},
			"step":  []string{"15s"},
		}.Encode(),
		"matrix",
		"system_cpu_utilization",
	)
	waitForAlertRule("DiskFull")
	waitForActiveAlert("DiskFull")
	health := waitForHealth()
	if health.Data.QueryBackend != "embedded-tsdb" {
		t.Fatalf("expected embedded-tsdb query backend in integration container, got %q", health.Data.QueryBackend)
	}
	if health.Data.IngestBackend != "embedded-tsdb" {
		t.Fatalf("expected embedded-tsdb ingest backend in integration container, got %q", health.Data.IngestBackend)
	}
	if health.Data.OTLP.QueueMode != "synchronous" {
		t.Fatalf("expected synchronous OTLP queue mode, got %q", health.Data.OTLP.QueueMode)
	}
	if health.Data.OTLP.Status != "ok" {
		t.Fatalf("expected OTLP status ok, got %q", health.Data.OTLP.Status)
	}
	if health.Data.LegacyPush.Status != "ok" {
		t.Fatalf("expected legacy push status ok, got %q", health.Data.LegacyPush.Status)
	}

	t.Log("OTLP collector forwarding and legacy PushMetrics both ingested successfully and were visible through monitoring health, query, and alert APIs")
}

type otlpCollectorContainer struct {
	Container testcontainers.Container
	GRPCAddr  string
}

func startOTLPCollector(t *testing.T, ctx context.Context, networkName, techStackEndpoint string) *otlpCollectorContainer {
	t.Helper()

	config := fmt.Sprintf(`
receivers:
  otlp:
    protocols:
      grpc:
        endpoint: 0.0.0.0:4317
exporters:
  otlp/techstack:
    endpoint: %s
    tls:
      insecure: true
service:
  pipelines:
    metrics:
      receivers: [otlp]
      exporters: [otlp/techstack]
`, techStackEndpoint)

	req := testcontainers.ContainerRequest{
		Image:        "otel/opentelemetry-collector:0.121.0",
		ExposedPorts: []string{"4317/tcp"},
		Cmd:          []string{"--config=/etc/otelcol/config.yaml"},
		Networks:     []string{networkName},
		NetworkAliases: map[string][]string{
			networkName: {"otel-collector"},
		},
		Files: []testcontainers.ContainerFile{{
			Reader:            strings.NewReader(config),
			ContainerFilePath: "/etc/otelcol/config.yaml",
			FileMode:          0644,
		}},
		WaitingFor: wait.ForListeningPort("4317/tcp").WithStartupTimeout(60 * time.Second),
	}
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	if err != nil {
		t.Fatalf("failed to start OTLP collector container: %v", err)
	}
	t.Cleanup(func() {
		if err := container.Terminate(context.Background()); err != nil {
			t.Logf("warning: failed to terminate OTLP collector container: %v", err)
		}
	})

	host, err := container.Host(ctx)
	if err != nil {
		t.Fatalf("failed to resolve OTLP collector host: %v", err)
	}
	port, err := container.MappedPort(ctx, "4317")
	if err != nil {
		t.Fatalf("failed to resolve OTLP collector port: %v", err)
	}
	return &otlpCollectorContainer{
		Container: container,
		GRPCAddr:  fmt.Sprintf("%s:%s", host, port.Port()),
	}
}

func TestIntegration_MonitorHealthReportsLegacyParseErrors(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	ks := testutil.StartkombifyTechstack(t, ctx)
	ks.HealthCheck(t)

	conn, err := grpc.DialContext(ctx, ks.GRPCAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithBlock(),
	)
	if err != nil {
		t.Fatalf("failed to connect to gRPC: %v", err)
	}
	defer conn.Close()

	legacyClient := agentpb.NewAgentServiceClient(conn)
	if _, err := legacyClient.PushMetrics(ctx, &agentpb.MetricsBatch{}); status.Code(err) != codes.InvalidArgument {
		logs, _ := ks.Logs(ctx)
		t.Fatalf("expected InvalidArgument from legacy PushMetrics, got %v\nContainer logs:\n%s", err, logs)
	}

	token := ks.Authenticate(t, ctx)

	type healthResponse struct {
		Data struct {
			QueryBackendStatus string `json:"queryBackendStatus"`
			IngestStatus       string `json:"ingestStatus"`
			LegacyPush         struct {
				Status      string `json:"status"`
				ParseErrors uint64 `json:"parseErrors"`
			} `json:"legacyPush"`
		} `json:"data"`
	}

	fetchHealth := func() (*healthResponse, error) {
		resp, err := ks.AuthenticatedAPIRequest(ctx, http.MethodGet, "/api/v1/monitor/health", nil, token)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()

		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("unexpected status %d for /api/v1/monitor/health: %s", resp.StatusCode, string(body))
		}

		var payload healthResponse
		if err := json.Unmarshal(body, &payload); err != nil {
			return nil, fmt.Errorf("decode /api/v1/monitor/health: %w", err)
		}
		return &payload, nil
	}

	deadline := time.Now().Add(15 * time.Second)
	var last *healthResponse
	var lastErr error
	for time.Now().Before(deadline) {
		last, lastErr = fetchHealth()
		if lastErr == nil && last.Data.LegacyPush.ParseErrors >= 1 && last.Data.LegacyPush.Status == "error" {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if lastErr != nil {
		logs, _ := ks.Logs(ctx)
		t.Fatalf("failed to fetch monitor health after parse error: %v\nContainer logs:\n%s", lastErr, logs)
	}
	if last == nil || last.Data.LegacyPush.ParseErrors < 1 || last.Data.LegacyPush.Status != "error" {
		logs, _ := ks.Logs(ctx)
		t.Fatalf("timed out waiting for legacy parse error health state (last=%+v)\nContainer logs:\n%s", last, logs)
	}
	if last.Data.QueryBackendStatus != "ok" {
		t.Fatalf("expected query backend to remain healthy, got %q", last.Data.QueryBackendStatus)
	}
	if last.Data.IngestStatus != "degraded" {
		t.Fatalf("expected overall ingest status degraded after legacy parse error, got %q", last.Data.IngestStatus)
	}
}

func TestIntegration_MonitorHealthReportsRemoteBackendOutage(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	ks := testutil.StartkombifyTechstackWithEnv(t, ctx, map[string]string{
		"TECHSTACK_MONITOR_QUERY_BACKEND_URL": "http://127.0.0.1:65534",
	})
	ks.HealthCheck(t)

	token := ks.Authenticate(t, ctx)

	type healthResponse struct {
		Data struct {
			QueryBackend       string `json:"queryBackend"`
			QueryBackendStatus string `json:"queryBackendStatus"`
			QueryBackendError  string `json:"queryBackendError"`
			IngestStatus       string `json:"ingestStatus"`
		} `json:"data"`
	}

	fetchHealth := func() (*healthResponse, error) {
		resp, err := ks.AuthenticatedAPIRequest(ctx, http.MethodGet, "/api/v1/monitor/health", nil, token)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()

		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("unexpected status %d for /api/v1/monitor/health: %s", resp.StatusCode, string(body))
		}

		var payload healthResponse
		if err := json.Unmarshal(body, &payload); err != nil {
			return nil, fmt.Errorf("decode /api/v1/monitor/health: %w", err)
		}
		return &payload, nil
	}

	deadline := time.Now().Add(15 * time.Second)
	var last *healthResponse
	var lastErr error
	for time.Now().Before(deadline) {
		last, lastErr = fetchHealth()
		if lastErr == nil && last.Data.QueryBackendStatus == "error" && last.Data.QueryBackendError != "" {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if lastErr != nil {
		logs, _ := ks.Logs(ctx)
		t.Fatalf("failed to fetch monitor health for remote backend outage: %v\nContainer logs:\n%s", lastErr, logs)
	}
	if last == nil || last.Data.QueryBackendStatus != "error" || last.Data.QueryBackendError == "" {
		logs, _ := ks.Logs(ctx)
		t.Fatalf("timed out waiting for remote backend outage health state (last=%+v)\nContainer logs:\n%s", last, logs)
	}
	if last.Data.QueryBackend != "remote-promql" {
		t.Fatalf("expected remote-promql query backend, got %q", last.Data.QueryBackend)
	}
	if last.Data.IngestStatus != "idle" {
		t.Fatalf("expected idle ingest status without ingest traffic, got %q", last.Data.IngestStatus)
	}
}

func TestIntegration_MonitorQueriesVictoriaMetricsBackend(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	victoriaNetwork, err := tcnetwork.New(ctx)
	if err != nil {
		t.Fatalf("failed to create VictoriaMetrics test network: %v", err)
	}
	testcontainers.CleanupNetwork(t, victoriaNetwork)

	victoriaReq := testcontainers.ContainerRequest{
		Image:        "victoriametrics/victoria-metrics:v1.139.0",
		ExposedPorts: []string{"8428/tcp"},
		Cmd: []string{
			"-storageDataPath=/victoria-metrics-data",
			"-retentionPeriod=1d",
			"-httpListenAddr=:8428",
			"-search.maxConcurrentRequests=4",
		},
		Networks: []string{victoriaNetwork.Name},
		NetworkAliases: map[string][]string{
			victoriaNetwork.Name: {"victoriametrics"},
		},
		WaitingFor: wait.ForHTTP("/health").WithPort("8428/tcp").WithStartupTimeout(60 * time.Second),
	}
	victoriaContainer, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: victoriaReq,
		Started:          true,
	})
	if err != nil {
		t.Fatalf("failed to start VictoriaMetrics container: %v", err)
	}
	t.Cleanup(func() {
		if err := victoriaContainer.Terminate(context.Background()); err != nil {
			t.Logf("warning: failed to terminate VictoriaMetrics container: %v", err)
		}
	})

	victoriaHost, err := victoriaContainer.Host(ctx)
	if err != nil {
		t.Fatalf("failed to resolve VictoriaMetrics host: %v", err)
	}
	victoriaPort, err := victoriaContainer.MappedPort(ctx, "8428")
	if err != nil {
		t.Fatalf("failed to resolve VictoriaMetrics port: %v", err)
	}

	sampleTime := time.Now().UTC().Add(-5 * time.Second)
	if err := writeVictoriaMetric(ctx, fmt.Sprintf("http://%s:%s", victoriaHost, victoriaPort.Port()), "vm_live_query_test_total", sampleTime, 12); err != nil {
		t.Fatalf("failed to seed VictoriaMetrics: %v", err)
	}

	ks := testutil.StartkombifyTechstackWithEnvAndRequestMutator(t, ctx, map[string]string{
		"TECHSTACK_MONITOR_QUERY_BACKEND_URL": "http://victoriametrics:8428",
	}, func(req *testcontainers.ContainerRequest) {
		req.Networks = append(req.Networks, victoriaNetwork.Name)
	})
	ks.HealthCheck(t)

	token := ks.Authenticate(t, ctx)

	type metricNamesResponse struct {
		Data []string `json:"data"`
	}
	type monitorHealthResponse struct {
		Data struct {
			QueryBackend       string `json:"queryBackend"`
			QueryBackendStatus string `json:"queryBackendStatus"`
			QueryBackendError  string `json:"queryBackendError"`
		} `json:"data"`
	}

	fetchMetricNames := func() (*metricNamesResponse, error) {
		resp, err := ks.AuthenticatedAPIRequest(ctx, http.MethodGet, "/api/v1/monitor/metrics/names", nil, token)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()

		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("unexpected status %d for /api/v1/monitor/metrics/names: %s", resp.StatusCode, string(body))
		}

		var payload metricNamesResponse
		if err := json.Unmarshal(body, &payload); err != nil {
			return nil, fmt.Errorf("decode /api/v1/monitor/metrics/names: %w", err)
		}
		return &payload, nil
	}

	fetchHealth := func() (*monitorHealthResponse, error) {
		resp, err := ks.AuthenticatedAPIRequest(ctx, http.MethodGet, "/api/v1/monitor/health", nil, token)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()

		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("unexpected status %d for /api/v1/monitor/health: %s", resp.StatusCode, string(body))
		}

		var payload monitorHealthResponse
		if err := json.Unmarshal(body, &payload); err != nil {
			return nil, fmt.Errorf("decode /api/v1/monitor/health: %w", err)
		}
		return &payload, nil
	}

	deadline := time.Now().Add(20 * time.Second)
	var lastNames *metricNamesResponse
	var lastHealth *monitorHealthResponse
	for time.Now().Before(deadline) {
		if names, err := fetchMetricNames(); err == nil {
			lastNames = names
		}
		if health, err := fetchHealth(); err == nil {
			lastHealth = health
		}
		if lastNames != nil && slices.Contains(lastNames.Data, "vm_live_query_test_total") && lastHealth != nil && lastHealth.Data.QueryBackend == "remote-promql" && lastHealth.Data.QueryBackendStatus == "ok" {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}

	if lastNames == nil || !slices.Contains(lastNames.Data, "vm_live_query_test_total") {
		logs, _ := ks.Logs(ctx)
		t.Fatalf("timed out waiting for live VictoriaMetrics query path (last names=%+v last health=%+v)\nContainer logs:\n%s", lastNames, lastHealth, logs)
	}
	if lastHealth == nil {
		logs, _ := ks.Logs(ctx)
		t.Fatalf("timed out waiting for live VictoriaMetrics health state\nContainer logs:\n%s", logs)
	}
	if lastHealth.Data.QueryBackend != "remote-promql" {
		t.Fatalf("expected remote-promql query backend, got %q", lastHealth.Data.QueryBackend)
	}
	if lastHealth.Data.QueryBackendStatus != "ok" {
		t.Fatalf("expected healthy VictoriaMetrics query backend, got status=%q error=%q", lastHealth.Data.QueryBackendStatus, lastHealth.Data.QueryBackendError)
	}
}

func TestIntegration_MonitorHealthReportsRejectedOTLPWhenIngestUnavailable(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	ks := testutil.StartkombifyTechstackWithEnv(t, ctx, map[string]string{
		"TECHSTACK_TSDB_DIR":                  "/proc/techstack-invalid",
		"TECHSTACK_MONITOR_QUERY_BACKEND_URL": "http://127.0.0.1:65534",
	})
	ks.HealthCheck(t)

	conn, err := grpc.DialContext(ctx, ks.GRPCAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithBlock(),
	)
	if err != nil {
		t.Fatalf("failed to connect to gRPC: %v", err)
	}
	defer conn.Close()

	otlpClient := collectormetricspb.NewMetricsServiceClient(conn)
	otlpResp, err := otlpClient.Export(ctx, newIntegrationOTLPRequest(time.Now().UTC()))
	if err != nil {
		logs, _ := ks.Logs(ctx)
		t.Fatalf("OTLP Export failed unexpectedly: %v\nContainer logs:\n%s", err, logs)
	}
	if otlpResp.GetPartialSuccess() == nil || otlpResp.GetPartialSuccess().GetRejectedDataPoints() < 1 {
		logs, _ := ks.Logs(ctx)
		t.Fatalf("expected rejected OTLP datapoints when ingest is unavailable, got %+v\nContainer logs:\n%s", otlpResp.GetPartialSuccess(), logs)
	}
	if otlpResp.GetPartialSuccess().GetErrorMessage() != "monitoring not enabled" {
		t.Fatalf("expected monitoring not enabled error, got %q", otlpResp.GetPartialSuccess().GetErrorMessage())
	}

	token := ks.Authenticate(t, ctx)

	type healthResponse struct {
		Data struct {
			QueryBackendStatus string `json:"queryBackendStatus"`
			IngestStatus       string `json:"ingestStatus"`
			OTLP               struct {
				Status           string `json:"status"`
				RequestsRejected uint64 `json:"requestsRejected"`
				SamplesRejected  uint64 `json:"samplesRejected"`
			} `json:"otlp"`
		} `json:"data"`
	}

	fetchHealth := func() (*healthResponse, error) {
		resp, err := ks.AuthenticatedAPIRequest(ctx, http.MethodGet, "/api/v1/monitor/health", nil, token)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()

		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("unexpected status %d for /api/v1/monitor/health: %s", resp.StatusCode, string(body))
		}

		var payload healthResponse
		if err := json.Unmarshal(body, &payload); err != nil {
			return nil, fmt.Errorf("decode /api/v1/monitor/health: %w", err)
		}
		return &payload, nil
	}

	deadline := time.Now().Add(15 * time.Second)
	var last *healthResponse
	var lastErr error
	for time.Now().Before(deadline) {
		last, lastErr = fetchHealth()
		if lastErr == nil && last.Data.OTLP.RequestsRejected >= 1 && last.Data.OTLP.Status == "error" {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if lastErr != nil {
		logs, _ := ks.Logs(ctx)
		t.Fatalf("failed to fetch monitor health for rejected OTLP flow: %v\nContainer logs:\n%s", lastErr, logs)
	}
	if last == nil || last.Data.OTLP.RequestsRejected < 1 || last.Data.OTLP.Status != "error" {
		logs, _ := ks.Logs(ctx)
		t.Fatalf("timed out waiting for rejected OTLP health state (last=%+v)\nContainer logs:\n%s", last, logs)
	}
	if last.Data.QueryBackendStatus != "error" {
		t.Fatalf("expected degraded remote query backend status, got %q", last.Data.QueryBackendStatus)
	}
	if last.Data.IngestStatus != "unavailable" {
		t.Fatalf("expected unavailable ingest status, got %q", last.Data.IngestStatus)
	}
}

func newIntegrationOTLPRequest(ts time.Time) *collectormetricspb.ExportMetricsServiceRequest {
	return &collectormetricspb.ExportMetricsServiceRequest{
		ResourceMetrics: []*metricspb.ResourceMetrics{{
			Resource: &resourcepb.Resource{Attributes: []*commonpb.KeyValue{
				{Key: "host.name", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: "node-1"}}},
				{Key: "service.name", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: "integration-otel-agent"}}},
			}},
			ScopeMetrics: []*metricspb.ScopeMetrics{{
				Metrics: []*metricspb.Metric{
					{
						Name: "system.cpu.utilization",
						Data: &metricspb.Metric_Gauge{Gauge: &metricspb.Gauge{DataPoints: []*metricspb.NumberDataPoint{{
							Attributes: []*commonpb.KeyValue{{
								Key:   "cpu",
								Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: "0"}},
							}},
							TimeUnixNano: uint64(ts.UnixNano()),
							Value:        &metricspb.NumberDataPoint_AsDouble{AsDouble: 0.72},
						}}}},
					},
					{
						Name: "node.disk.usage.percent",
						Data: &metricspb.Metric_Gauge{Gauge: &metricspb.Gauge{DataPoints: []*metricspb.NumberDataPoint{{
							Attributes: []*commonpb.KeyValue{{
								Key:   "mountpoint",
								Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: "/"}},
							}},
							TimeUnixNano: uint64(ts.UnixNano()),
							Value:        &metricspb.NumberDataPoint_AsDouble{AsDouble: 95},
						}}}},
					},
				},
			}},
		}},
	}
}

func writeVictoriaMetric(ctx context.Context, baseURL, metricName string, ts time.Time, value float64) error {
	request := &prompb.WriteRequest{Timeseries: []prompb.TimeSeries{{
		Labels: []prompb.Label{
			{Name: "__name__", Value: metricName},
			{Name: "host", Value: "victoria-test-node"},
		},
		Samples: []prompb.Sample{{
			Value:     value,
			Timestamp: ts.UnixMilli(),
		}},
	}}}

	body, err := request.Marshal()
	if err != nil {
		return fmt.Errorf("marshal VictoriaMetrics write request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(baseURL, "/")+"/api/v1/write", bytes.NewReader(snappy.Encode(nil, body)))
	if err != nil {
		return fmt.Errorf("create VictoriaMetrics write request: %w", err)
	}
	httpReq.Header.Set("Content-Encoding", "snappy")
	httpReq.Header.Set("Content-Type", "application/x-protobuf")
	httpReq.Header.Set("X-Prometheus-Remote-Write-Version", "0.1.0")

	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("send VictoriaMetrics write request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		responseBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("VictoriaMetrics write returned %d: %s", resp.StatusCode, string(responseBody))
	}
	return nil
}
