package monitoring

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/prometheus/prometheus/promql"
	"github.com/prometheus/prometheus/promql/parser"
)

func TestHTTPPromQLBackend_InstantQueryVector(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/query" {
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "success",
			"data": map[string]any{
				"resultType": "vector",
				"result": []map[string]any{{
					"metric": map[string]string{"__name__": "system_cpu_utilization", "host_name": "node-1"},
					"value":  []any{1714550400.5, "0.72"},
				}},
			},
		})
	}))
	defer server.Close()

	backend, err := NewHTTPPromQLBackend(HTTPPromQLBackendConfig{BaseURL: server.URL})
	if err != nil {
		t.Fatalf("NewHTTPPromQLBackend failed: %v", err)
	}

	result, err := backend.InstantQuery(context.Background(), `system_cpu_utilization`, time.Unix(1714550400, 0))
	if err != nil {
		t.Fatalf("InstantQuery failed: %v", err)
	}
	if result.Value.Type() != parser.ValueTypeVector {
		t.Fatalf("expected vector result type, got %s", result.Value.Type())
	}
	vec, ok := result.Value.(promql.Vector)
	if !ok || len(vec) != 1 {
		t.Fatalf("expected one vector sample, got %T with len=%d", result.Value, len(vec))
	}
	if got := vec[0].Metric.Get("host_name"); got != "node-1" {
		t.Fatalf("expected host_name label node-1, got %q", got)
	}
	if vec[0].F != 0.72 {
		t.Fatalf("expected value 0.72, got %v", vec[0].F)
	}
}

func TestHTTPPromQLBackend_RangeQueryMatrix(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/query_range" {
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "success",
			"data": map[string]any{
				"resultType": "matrix",
				"result": []map[string]any{{
					"metric": map[string]string{"__name__": "node_load1", "host": "node-1"},
					"values": [][]any{{1714550400.0, "1.0"}, {1714550460.0, "1.5"}},
				}},
			},
		})
	}))
	defer server.Close()

	backend, err := NewHTTPPromQLBackend(HTTPPromQLBackendConfig{BaseURL: server.URL})
	if err != nil {
		t.Fatalf("NewHTTPPromQLBackend failed: %v", err)
	}

	result, err := backend.RangeQuery(context.Background(), `node_load1`, time.Unix(1714550400, 0), time.Unix(1714550460, 0), time.Minute)
	if err != nil {
		t.Fatalf("RangeQuery failed: %v", err)
	}
	if result.Value.Type() != parser.ValueTypeMatrix {
		t.Fatalf("expected matrix result type, got %s", result.Value.Type())
	}
	matrix, ok := result.Value.(promql.Matrix)
	if !ok || len(matrix) != 1 {
		t.Fatalf("expected one matrix series, got %T with len=%d", result.Value, len(matrix))
	}
	if len(matrix[0].Floats) != 2 {
		t.Fatalf("expected 2 matrix points, got %d", len(matrix[0].Floats))
	}
}

func TestHTTPPromQLBackend_MetadataAndStats(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/labels":
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "success", "data": []string{"__name__", "host", "ingest_protocol"}})
		case "/api/v1/label/__name__/values":
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "success", "data": []string{"system_cpu_utilization", "legacy_node_temperature_celsius"}})
		case "/api/v1/label/ingest_protocol/values":
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "success", "data": []string{"otlp", "legacy"}})
		case "/api/v1/status/tsdb":
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "success", "data": map[string]any{"seriesCount": 12, "minTimeMs": 1000, "maxTimeMs": 2000}})
		default:
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
	}))
	defer server.Close()

	backend, err := NewHTTPPromQLBackend(HTTPPromQLBackendConfig{BaseURL: server.URL})
	if err != nil {
		t.Fatalf("NewHTTPPromQLBackend failed: %v", err)
	}

	names, err := backend.LabelNames(context.Background())
	if err != nil {
		t.Fatalf("LabelNames failed: %v", err)
	}
	if len(names) != 3 {
		t.Fatalf("expected 3 label names, got %d", len(names))
	}

	values, err := backend.LabelValues(context.Background(), "ingest_protocol")
	if err != nil {
		t.Fatalf("LabelValues failed: %v", err)
	}
	if len(values) != 2 || values[0] != "otlp" {
		t.Fatalf("unexpected ingest_protocol values: %v", values)
	}

	metrics, err := backend.MetricNames(context.Background())
	if err != nil {
		t.Fatalf("MetricNames failed: %v", err)
	}
	if len(metrics) != 2 {
		t.Fatalf("expected 2 metric names, got %d", len(metrics))
	}

	stats, err := backend.Stats(context.Background())
	if err != nil {
		t.Fatalf("Stats failed: %v", err)
	}
	if stats.NumSeries != 12 {
		t.Fatalf("expected NumSeries 12, got %d", stats.NumSeries)
	}
	if len(stats.TopMetrics) != 2 {
		t.Fatalf("expected 2 top metrics, got %d", len(stats.TopMetrics))
	}
}

func TestLookupUint64ClampsAndRejectsInvalidValues(t *testing.T) {
	payload := map[string]any{
		"float_ok":       42.9,
		"float_negative": -1.0,
		"float_overflow": math.MaxFloat64,
		"int_ok":         int64(7),
		"int_negative":   int64(-3),
		"uint_ok":        uint64(9),
	}

	tests := []struct {
		key  string
		want uint64
	}{
		{"float_ok", 42},
		{"float_negative", 0},
		{"float_overflow", ^uint64(0)},
		{"int_ok", 7},
		{"int_negative", 0},
		{"uint_ok", 0},
		{"missing", 0},
	}

	for _, tt := range tests {
		if got := lookupUint64(payload, tt.key); got != tt.want {
			t.Fatalf("lookupUint64(%q) = %d, want %d", tt.key, got, tt.want)
		}
	}
}

func TestHTTPPromQLBackend_VictoriaMetricsPrefixAndStats(t *testing.T) {
	requestedPaths := make([]string, 0, 3)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestedPaths = append(requestedPaths, r.URL.Path)
		switch r.URL.Path {
		case "/select/0/prometheus/api/v1/status/tsdb":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status": "success",
				"data": map[string]any{
					"headStats": map[string]any{
						"numSeries": 24,
						"minTime":   1714550400000,
						"maxTime":   1714636800000,
					},
				},
			})
		case "/select/0/prometheus/api/v1/label/__name__/values":
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "success", "data": []string{"vm_http_requests_total", "vm_rows_added_total"}})
		case "/select/0/prometheus/api/v1/query":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status": "success",
				"data": map[string]any{
					"resultType": "vector",
					"result": []map[string]any{{
						"metric": map[string]string{"__name__": "vm_http_requests_total", "job": "victoriametrics"},
						"value":  []any{1714636800.0, "12"},
					}},
				},
			})
		default:
			t.Fatalf("unexpected VictoriaMetrics-prefixed path %q", r.URL.Path)
		}
	}))
	defer server.Close()

	backend, err := NewHTTPPromQLBackend(HTTPPromQLBackendConfig{BaseURL: server.URL + "/select/0/prometheus"})
	if err != nil {
		t.Fatalf("NewHTTPPromQLBackend failed: %v", err)
	}

	stats, err := backend.Stats(context.Background())
	if err != nil {
		t.Fatalf("Stats failed: %v", err)
	}
	if stats.NumSeries != 24 {
		t.Fatalf("expected NumSeries 24 from VictoriaMetrics headStats payload, got %d", stats.NumSeries)
	}
	if stats.RetentionDays != 1 {
		t.Fatalf("expected RetentionDays 1, got %v", stats.RetentionDays)
	}
	if len(stats.TopMetrics) != 2 {
		t.Fatalf("expected 2 top metrics, got %d", len(stats.TopMetrics))
	}

	result, err := backend.InstantQuery(context.Background(), `vm_http_requests_total`, time.Unix(1714636800, 0))
	if err != nil {
		t.Fatalf("InstantQuery failed: %v", err)
	}
	vector, ok := result.Value.(promql.Vector)
	if !ok || len(vector) != 1 {
		t.Fatalf("expected one vector sample, got %T with len=%d", result.Value, len(vector))
	}
	if got := vector[0].Metric.Get("job"); got != "victoriametrics" {
		t.Fatalf("expected VictoriaMetrics job label, got %q", got)
	}

	for _, expectedPath := range []string{
		"/select/0/prometheus/api/v1/status/tsdb",
		"/select/0/prometheus/api/v1/label/__name__/values",
		"/select/0/prometheus/api/v1/query",
	} {
		if !slices.Contains(requestedPaths, expectedPath) {
			t.Fatalf("expected request path %q in %v", expectedPath, requestedPaths)
		}
	}
}

func TestInstantQuery_BackendParity(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	embedded := newTestTSDB(t)
	if err := embedded.Write([]MetricSample{{
		Name:      "test_cpu_usage",
		Value:     42.5,
		Labels:    map[string]string{"host": "node-1", "core": "0"},
		Timestamp: now,
	}}); err != nil {
		t.Fatalf("embedded Write failed: %v", err)
	}

	embeddedResult, err := embedded.InstantQuery(ctx, `test_cpu_usage{host="node-1"}`, now)
	if err != nil {
		t.Fatalf("embedded InstantQuery failed: %v", err)
	}
	embeddedVector, ok := embeddedResult.Value.(promql.Vector)
	if !ok || len(embeddedVector) != 1 {
		t.Fatalf("expected embedded vector with one sample, got %T len=%d", embeddedResult.Value, len(embeddedVector))
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "success",
			"data": map[string]any{
				"resultType": "vector",
				"result": []map[string]any{{
					"metric": map[string]string{"__name__": "test_cpu_usage", "host": "node-1", "core": "0"},
					"value":  []any{float64(now.Unix()), "42.5"},
				}},
			},
		})
	}))
	defer server.Close()

	remote, err := NewHTTPPromQLBackend(HTTPPromQLBackendConfig{BaseURL: server.URL})
	if err != nil {
		t.Fatalf("NewHTTPPromQLBackend failed: %v", err)
	}
	remoteResult, err := remote.InstantQuery(ctx, `test_cpu_usage{host="node-1"}`, now)
	if err != nil {
		t.Fatalf("remote InstantQuery failed: %v", err)
	}
	remoteVector, ok := remoteResult.Value.(promql.Vector)
	if !ok || len(remoteVector) != 1 {
		t.Fatalf("expected remote vector with one sample, got %T len=%d", remoteResult.Value, len(remoteVector))
	}

	if embeddedResult.Value.Type() != remoteResult.Value.Type() {
		t.Fatalf("expected matching result types, got embedded=%s remote=%s", embeddedResult.Value.Type(), remoteResult.Value.Type())
	}
	if embeddedVector[0].Metric.Get("host") != remoteVector[0].Metric.Get("host") {
		t.Fatalf("expected matching host labels, got embedded=%q remote=%q", embeddedVector[0].Metric.Get("host"), remoteVector[0].Metric.Get("host"))
	}
	if embeddedVector[0].Metric.Get("core") != remoteVector[0].Metric.Get("core") {
		t.Fatalf("expected matching core labels, got embedded=%q remote=%q", embeddedVector[0].Metric.Get("core"), remoteVector[0].Metric.Get("core"))
	}
	if embeddedVector[0].F != remoteVector[0].F {
		t.Fatalf("expected matching sample values, got embedded=%v remote=%v", embeddedVector[0].F, remoteVector[0].F)
	}
}

func TestAlertEngine_BackendParity(t *testing.T) {
	now := time.Now().UTC()
	rule := AlertRule{Name: "TestHigh", Expr: `test_value > 50`, For: 0, Severity: "warning", Message: "value too high"}

	remoteServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "success",
			"data": map[string]any{
				"resultType": "vector",
				"result": []map[string]any{{
					"metric": map[string]string{"__name__": "test_value", "host": "node-1"},
					"value":  []any{float64(now.Unix()), "100"},
				}},
			},
		})
	}))
	defer remoteServer.Close()

	remoteBackend, err := NewHTTPPromQLBackend(HTTPPromQLBackendConfig{BaseURL: remoteServer.URL})
	if err != nil {
		t.Fatalf("NewHTTPPromQLBackend failed: %v", err)
	}

	embeddedBackend := newAlertTestTSDB(t)
	if err := embeddedBackend.Write([]MetricSample{{
		Name:      "test_value",
		Value:     100,
		Labels:    map[string]string{"host": "node-1"},
		Timestamp: now,
	}}); err != nil {
		t.Fatalf("embedded Write failed: %v", err)
	}

	backends := []struct {
		name    string
		backend MetricsQueryBackend
	}{
		{name: "embedded-tsdb", backend: embeddedBackend},
		{name: "remote-promql", backend: remoteBackend},
	}

	for _, tt := range backends {
		t.Run(tt.name, func(t *testing.T) {
			notifier := &mockNotifier{}
			engine := NewAlertEngine(tt.backend, notifier, []AlertRule{rule}, AlertEngineConfig{})
			engine.evaluate(context.Background())

			active := engine.ActiveAlerts()
			if len(active) != 1 {
				t.Fatalf("expected one active alert, got %d", len(active))
			}
			if active[0].Rule.Name != "TestHigh" {
				t.Fatalf("expected TestHigh alert, got %s", active[0].Rule.Name)
			}
			received := notifier.received()
			if len(received) != 1 {
				t.Fatalf("expected one sent notification, got %d", len(received))
			}
			if received[0].RuleName != "TestHigh" {
				t.Fatalf("expected notification for TestHigh, got %s", received[0].RuleName)
			}
		})
	}
}

func TestHTTPPromQLBackend_LabelNamesAreSortedCompatible(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "success", "data": []string{"host", "__name__", "ingest_protocol"}})
	}))
	defer server.Close()

	backend, err := NewHTTPPromQLBackend(HTTPPromQLBackendConfig{BaseURL: server.URL})
	if err != nil {
		t.Fatalf("NewHTTPPromQLBackend failed: %v", err)
	}

	names, err := backend.LabelNames(context.Background())
	if err != nil {
		t.Fatalf("LabelNames failed: %v", err)
	}
	for _, required := range []string{"__name__", "host", "ingest_protocol"} {
		if !slices.Contains(names, required) {
			t.Fatalf("expected label %q in %v", required, names)
		}
	}
}
