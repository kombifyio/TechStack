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
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

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
