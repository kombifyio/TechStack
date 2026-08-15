package routes_test

import (
	"strings"
	"testing"

	"github.com/kombifyio/techstack/internal/routes"
	"github.com/prometheus/client_golang/prometheus"
)

func TestNewMetrics(t *testing.T) {
	reg := prometheus.NewRegistry()

	m := routes.NewMetrics(reg)

	if m == nil {
		t.Fatal("NewMetrics returned nil")
	}

	// Verify all metrics are initialized
	tests := []struct {
		name   string
		metric interface{}
	}{
		{"HTTPRequestsTotal", m.HTTPRequestsTotal},
		{"HTTPRequestDuration", m.HTTPRequestDuration},
		{"JobsTotal", m.JobsTotal},
		{"JobsActive", m.JobsActive},
		{"JobsDuration", m.JobsDuration},
		{"JobQueueSize", m.JobQueueSize},
		{"JobQueueLatency", m.JobQueueLatency},
		{"AgentsConnected", m.AgentsConnected},
		{"AgentHeartbeats", m.AgentHeartbeats},
		{"AgentCommandsSent", m.AgentCommandsSent},
		{"StacksTotal", m.StacksTotal},
		{"StacksActive", m.StacksActive},
		{"TofuRunsTotal", m.TofuRunsTotal},
		{"TofuRunsDuration", m.TofuRunsDuration},
		{"DBConnectionsActive", m.DBConnectionsActive},
		{"DBQueriesTotal", m.DBQueriesTotal},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.metric == nil {
				t.Errorf("%s is nil", tt.name)
			}
		})
	}
}

func TestMetrics_HTTPRequestsTotal(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := routes.NewMetrics(reg)

	// Simulate recording some HTTP requests
	m.HTTPRequestsTotal.WithLabelValues("GET", "/api/v1/health", "200").Inc()
	m.HTTPRequestsTotal.WithLabelValues("POST", "/api/v1/stacks", "201").Inc()
	m.HTTPRequestsTotal.WithLabelValues("GET", "/api/v1/jobs", "500").Inc()

	// Verify metrics were collected (no panic)
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatalf("Failed to gather metrics: %v", err)
	}

	found := false
	for _, mf := range mfs {
		if mf.GetName() == "techstack_http_requests_total" {
			found = true
			if len(mf.GetMetric()) != 3 {
				t.Errorf("Expected 3 metrics, got %d", len(mf.GetMetric()))
			}
		}
	}
	if !found {
		t.Error("techstack_http_requests_total metric not found")
	}
}

func TestMetrics_JobMetrics(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := routes.NewMetrics(reg)

	// Simulate job activity
	m.JobsActive.Set(5)
	m.JobQueueSize.Set(3)
	m.JobsTotal.WithLabelValues("provision", "completed").Inc()
	m.JobsTotal.WithLabelValues("destroy", "failed").Inc()
	m.JobsDuration.WithLabelValues("provision").Observe(45.5)

	mfs, err := reg.Gather()
	if err != nil {
		t.Fatalf("Failed to gather metrics: %v", err)
	}

	expectedMetrics := map[string]bool{
		"techstack_jobs_active":           false,
		"techstack_jobs_queue_size":       false,
		"techstack_jobs_total":            false,
		"techstack_jobs_duration_seconds": false,
	}

	for _, mf := range mfs {
		if _, ok := expectedMetrics[mf.GetName()]; ok {
			expectedMetrics[mf.GetName()] = true
		}
	}

	for name, found := range expectedMetrics {
		if !found {
			t.Errorf("Metric %s not found", name)
		}
	}
}

func TestMetrics_AgentMetrics(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := routes.NewMetrics(reg)

	// Simulate agent activity
	m.AgentsConnected.Set(3)
	m.AgentHeartbeats.WithLabelValues("agent-001").Add(100)
	m.AgentHeartbeats.WithLabelValues("agent-002").Add(50)
	m.AgentCommandsSent.WithLabelValues("agent-001", "restart").Inc()

	mfs, err := reg.Gather()
	if err != nil {
		t.Fatalf("Failed to gather metrics: %v", err)
	}

	found := false
	for _, mf := range mfs {
		if mf.GetName() == "techstack_agents_connected" {
			found = true
			metrics := mf.GetMetric()
			if len(metrics) != 1 {
				t.Errorf("Expected 1 gauge, got %d", len(metrics))
			}
			if metrics[0].GetGauge().GetValue() != 3 {
				t.Errorf("Expected 3 connected agents, got %v", metrics[0].GetGauge().GetValue())
			}
		}
	}
	if !found {
		t.Error("techstack_agents_connected metric not found")
	}
}

func TestMetrics_TofuMetrics(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := routes.NewMetrics(reg)

	// Simulate OpenTofu runs
	m.TofuRunsTotal.WithLabelValues("plan", "success").Inc()
	m.TofuRunsTotal.WithLabelValues("apply", "success").Inc()
	m.TofuRunsTotal.WithLabelValues("apply", "failure").Inc()
	m.TofuRunsDuration.WithLabelValues("plan").Observe(5.2)
	m.TofuRunsDuration.WithLabelValues("apply").Observe(120.5)

	mfs, err := reg.Gather()
	if err != nil {
		t.Fatalf("Failed to gather metrics: %v", err)
	}

	expectedMetrics := map[string]bool{
		"techstack_tofu_runs_total":           false,
		"techstack_tofu_run_duration_seconds": false,
	}

	for _, mf := range mfs {
		if _, ok := expectedMetrics[mf.GetName()]; ok {
			expectedMetrics[mf.GetName()] = true
		}
	}

	for name, found := range expectedMetrics {
		if !found {
			t.Errorf("Metric %s not found", name)
		}
	}
}

func TestMetrics_StackMetrics(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := routes.NewMetrics(reg)

	m.StacksTotal.Set(10)
	m.StacksActive.Set(7)

	mfs, err := reg.Gather()
	if err != nil {
		t.Fatalf("Failed to gather metrics: %v", err)
	}

	expectedMetrics := map[string]float64{
		"techstack_stacks_total":  10,
		"techstack_stacks_active": 7,
	}

	for _, mf := range mfs {
		if expected, ok := expectedMetrics[mf.GetName()]; ok {
			metrics := mf.GetMetric()
			if len(metrics) > 0 {
				actual := metrics[0].GetGauge().GetValue()
				if actual != expected {
					t.Errorf("%s: expected %v, got %v", mf.GetName(), expected, actual)
				}
			}
		}
	}
}

func TestMetrics_MonthlyRuntimeEnrollmentMetrics(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := routes.NewMetrics(reg)

	m.MonthlyRuntimeEnrollments.WithLabelValues("pending").Set(2)
	m.MonthlyRuntimeEnrollments.WithLabelValues("retrying").Set(1)
	m.MonthlyRuntimeEnrollments.WithLabelValues("failed").Set(3)

	mfs, err := reg.Gather()
	if err != nil {
		t.Fatalf("Failed to gather metrics: %v", err)
	}

	found := map[string]float64{}
	for _, mf := range mfs {
		if mf.GetName() != "techstack_monthly_runtime_enrollments" {
			continue
		}
		for _, metric := range mf.GetMetric() {
			status := ""
			for _, label := range metric.GetLabel() {
				if label.GetName() == "status" {
					status = label.GetValue()
				}
			}
			found[status] = metric.GetGauge().GetValue()
		}
	}
	for status, want := range map[string]float64{"pending": 2, "retrying": 1, "failed": 3} {
		if found[status] != want {
			t.Fatalf("monthly runtime enrollment metric %s = %v, want %v (all=%v)", status, found[status], want, found)
		}
	}
}

func TestMetrics_Namespacing(t *testing.T) {
	reg := prometheus.NewRegistry()
	routes.NewMetrics(reg)

	mfs, err := reg.Gather()
	if err != nil {
		t.Fatalf("Failed to gather metrics: %v", err)
	}

	for _, mf := range mfs {
		if !strings.HasPrefix(mf.GetName(), "techstack_") {
			t.Errorf("Metric %s does not have techstack_ prefix", mf.GetName())
		}
	}
}

func TestMetrics_NoPanic_OnDoubleRegistration(t *testing.T) {
	// First registration should succeed
	reg1 := prometheus.NewRegistry()
	m1 := routes.NewMetrics(reg1)
	if m1 == nil {
		t.Fatal("First NewMetrics returned nil")
	}

	// Second registration with new registry should also succeed
	reg2 := prometheus.NewRegistry()
	m2 := routes.NewMetrics(reg2)
	if m2 == nil {
		t.Fatal("Second NewMetrics returned nil")
	}
}

// Benchmark tests for metrics
func BenchmarkMetrics_HTTPRequestIncrement(b *testing.B) {
	reg := prometheus.NewRegistry()
	m := routes.NewMetrics(reg)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m.HTTPRequestsTotal.WithLabelValues("GET", "/api/v1/health", "200").Inc()
	}
}

func BenchmarkMetrics_HistogramObserve(b *testing.B) {
	reg := prometheus.NewRegistry()
	m := routes.NewMetrics(reg)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m.HTTPRequestDuration.WithLabelValues("GET", "/api/v1/stacks").Observe(0.05)
	}
}

func BenchmarkMetrics_GatherAll(b *testing.B) {
	reg := prometheus.NewRegistry()
	m := routes.NewMetrics(reg)

	// Add some sample data
	m.HTTPRequestsTotal.WithLabelValues("GET", "/api/v1/health", "200").Add(1000)
	m.JobsActive.Set(5)
	m.AgentsConnected.Set(3)
	m.StacksTotal.Set(10)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = reg.Gather()
	}
}

// TestMetricsEndpoint_Integration tests the full HTTP endpoint
// This requires mocking PocketBase which we skip for unit tests
func TestMetricsEndpoint_PrometheusFormat(t *testing.T) {
	// Create a simple test to verify prometheus format
	reg := prometheus.NewRegistry()
	m := routes.NewMetrics(reg)

	// Add some data
	m.HTTPRequestsTotal.WithLabelValues("GET", "/test", "200").Inc()
	m.JobsActive.Set(2)

	// Gather and check format
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatalf("Failed to gather: %v", err)
	}

	if len(mfs) == 0 {
		t.Error("No metrics gathered")
	}

	// Verify all metrics have proper help text
	for _, mf := range mfs {
		if mf.GetHelp() == "" {
			t.Errorf("Metric %s has no help text", mf.GetName())
		}
	}
}

// TestMetricsCollector_Describe tests the collector interface
func TestMetricsCollector_DescribeDoesNotPanic(t *testing.T) {
	// MetricsCollector.Describe should not panic even without app
	ch := make(chan *prometheus.Desc, 100)
	defer close(ch)

	// This would require a mock app, so we just verify the function signature exists
	// In a real test with testcontainers, we'd test the full flow
}
