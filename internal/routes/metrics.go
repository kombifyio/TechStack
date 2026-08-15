// Package routes provides custom HTTP routes for kombifyTechstack API.
// metrics.go provides the Prometheus /metrics endpoint for observability.
package routes

import (
	"github.com/kombifyio/techstack/internal/routes/sessionreauth"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/pocketbase/pocketbase/core"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

const (
	monthlyRuntimeEnrollmentStatusPending  = "pending"
	monthlyRuntimeEnrollmentStatusRetrying = "retrying"
	monthlyRuntimeEnrollmentStatusFailed   = "failed"
	monthlyRuntimeEnrollmentStatusEnrolled = "enrolled"
)

var monthlyRuntimeEnrollmentStatuses = []string{
	monthlyRuntimeEnrollmentStatusPending,
	monthlyRuntimeEnrollmentStatusRetrying,
	monthlyRuntimeEnrollmentStatusFailed,
	monthlyRuntimeEnrollmentStatusEnrolled,
}

// Metrics holds all custom kombifyTechstack Prometheus metrics.
type Metrics struct {
	// HTTP request metrics
	HTTPRequestsTotal   *prometheus.CounterVec
	HTTPRequestDuration *prometheus.HistogramVec

	// Jobs metrics
	JobsTotal       *prometheus.CounterVec
	JobsActive      prometheus.Gauge
	JobsDuration    *prometheus.HistogramVec
	JobQueueSize    prometheus.Gauge
	JobQueueLatency *prometheus.HistogramVec

	// Agent metrics
	AgentsConnected   prometheus.Gauge
	AgentHeartbeats   *prometheus.CounterVec
	AgentCommandsSent *prometheus.CounterVec

	// Stack metrics
	StacksTotal  prometheus.Gauge
	StacksActive prometheus.Gauge

	// OpenTofu metrics
	TofuRunsTotal    *prometheus.CounterVec
	TofuRunsDuration *prometheus.HistogramVec

	// Database metrics (from PocketBase)
	DBConnectionsActive prometheus.Gauge
	DBQueriesTotal      prometheus.Counter

	// Monthly Runtime metrics
	MonthlyRuntimeEnrollments *prometheus.GaugeVec

	// Session recovery metrics (reason_code session_reprojection_required)
	SessionReprojectionsTotal *prometheus.CounterVec

	// S7: gRPC Queue Backpressure metrics
	GRPCQueueSize     *prometheus.GaugeVec   // Current queue size by queue name
	GRPCQueueCapacity *prometheus.GaugeVec   // Max queue capacity by queue name
	GRPCQueueEnqueued *prometheus.CounterVec // Total items enqueued by queue name
	GRPCQueueDequeued *prometheus.CounterVec // Total items dequeued by queue name
	GRPCQueueRejected *prometheus.CounterVec // Total items rejected (queue full) by queue name
	GRPCQueueDropped  *prometheus.CounterVec // Total items dropped (overflow) by queue name
}

// NewMetrics creates and registers all kombifyTechstack Prometheus metrics.
func NewMetrics(reg prometheus.Registerer) *Metrics {
	m := &Metrics{
		// HTTP metrics
		HTTPRequestsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: "techstack",
				Subsystem: "http",
				Name:      "requests_total",
				Help:      "Total number of HTTP requests processed.",
			},
			[]string{"method", "path", "status"},
		),
		HTTPRequestDuration: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Namespace: "techstack",
				Subsystem: "http",
				Name:      "request_duration_seconds",
				Help:      "HTTP request latency in seconds.",
				Buckets:   []float64{0.01, 0.05, 0.1, 0.25, 0.5, 1.0, 2.5, 5.0, 10.0},
			},
			[]string{"method", "path"},
		),

		// Jobs metrics
		JobsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: "techstack",
				Subsystem: "jobs",
				Name:      "total",
				Help:      "Total number of jobs by type and final state.",
			},
			[]string{"type", "state"},
		),
		JobsActive: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "techstack",
			Subsystem: "jobs",
			Name:      "active",
			Help:      "Number of currently active (running) jobs.",
		}),
		JobsDuration: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Namespace: "techstack",
				Subsystem: "jobs",
				Name:      "duration_seconds",
				Help:      "Job execution duration in seconds.",
				Buckets:   []float64{1, 5, 10, 30, 60, 120, 300, 600, 1800},
			},
			[]string{"type"},
		),
		JobQueueSize: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "techstack",
			Subsystem: "jobs",
			Name:      "queue_size",
			Help:      "Number of jobs waiting in queue.",
		}),
		JobQueueLatency: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Namespace: "techstack",
				Subsystem: "jobs",
				Name:      "queue_latency_seconds",
				Help:      "Time spent waiting in queue before execution.",
				Buckets:   []float64{0.1, 0.5, 1, 2, 5, 10, 30, 60},
			},
			[]string{"type"},
		),

		// Agent metrics
		AgentsConnected: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "techstack",
			Subsystem: "agents",
			Name:      "connected",
			Help:      "Number of currently connected agents.",
		}),
		AgentHeartbeats: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: "techstack",
				Subsystem: "agents",
				Name:      "heartbeats_total",
				Help:      "Total number of agent heartbeats received.",
			},
			[]string{"agent_id"},
		),
		AgentCommandsSent: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: "techstack",
				Subsystem: "agents",
				Name:      "commands_sent_total",
				Help:      "Total number of commands sent to agents.",
			},
			[]string{"agent_id", "command_type"},
		),

		// Stack metrics
		StacksTotal: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "techstack",
			Subsystem: "stacks",
			Name:      "total",
			Help:      "Total number of stacks.",
		}),
		StacksActive: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "techstack",
			Subsystem: "stacks",
			Name:      "active",
			Help:      "Number of active (provisioned) stacks.",
		}),

		// OpenTofu metrics
		TofuRunsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: "techstack",
				Subsystem: "tofu",
				Name:      "runs_total",
				Help:      "Total OpenTofu runs by operation and result.",
			},
			[]string{"operation", "result"},
		),
		TofuRunsDuration: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Namespace: "techstack",
				Subsystem: "tofu",
				Name:      "run_duration_seconds",
				Help:      "OpenTofu run duration in seconds.",
				Buckets:   []float64{1, 5, 10, 30, 60, 120, 300, 600},
			},
			[]string{"operation"},
		),

		// Database metrics
		DBConnectionsActive: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "techstack",
			Subsystem: "db",
			Name:      "connections_active",
			Help:      "Number of active database connections.",
		}),
		DBQueriesTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "techstack",
			Subsystem: "db",
			Name:      "queries_total",
			Help:      "Total number of database queries executed.",
		}),
		MonthlyRuntimeEnrollments: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: "techstack",
				Subsystem: "monthly_runtime",
				Name:      "enrollments",
				Help:      "Number of Monthly Runtime VM lease enrollment outbox items by status.",
			},
			[]string{"status"},
		),
		SessionReprojectionsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: "techstack",
				Subsystem: "auth",
				Name:      "session_reprojections_total",
				Help:      "Signature-valid sessions whose tenant projection failed, by tenant and outcome (recovered server-side vs. reauth_required signalled to the client).",
			},
			[]string{"tenant", "outcome"},
		),

		// S7: gRPC Queue Backpressure metrics
		GRPCQueueSize: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: "techstack",
				Subsystem: "grpc_queue",
				Name:      "size",
				Help:      "Current number of items in the gRPC command queue.",
			},
			[]string{"queue"},
		),
		GRPCQueueCapacity: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: "techstack",
				Subsystem: "grpc_queue",
				Name:      "capacity",
				Help:      "Maximum capacity of the gRPC command queue.",
			},
			[]string{"queue"},
		),
		GRPCQueueEnqueued: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: "techstack",
				Subsystem: "grpc_queue",
				Name:      "enqueued_total",
				Help:      "Total number of items enqueued to the gRPC command queue.",
			},
			[]string{"queue"},
		),
		GRPCQueueDequeued: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: "techstack",
				Subsystem: "grpc_queue",
				Name:      "dequeued_total",
				Help:      "Total number of items dequeued from the gRPC command queue.",
			},
			[]string{"queue"},
		),
		GRPCQueueRejected: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: "techstack",
				Subsystem: "grpc_queue",
				Name:      "rejected_total",
				Help:      "Total number of items rejected due to queue being full.",
			},
			[]string{"queue"},
		),
		GRPCQueueDropped: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: "techstack",
				Subsystem: "grpc_queue",
				Name:      "dropped_total",
				Help:      "Total number of items dropped due to overflow strategy.",
			},
			[]string{"queue"},
		),
	}

	// Register all metrics
	reg.MustRegister(
		m.HTTPRequestsTotal,
		m.HTTPRequestDuration,
		m.JobsTotal,
		m.JobsActive,
		m.JobsDuration,
		m.JobQueueSize,
		m.JobQueueLatency,
		m.AgentsConnected,
		m.AgentHeartbeats,
		m.AgentCommandsSent,
		m.StacksTotal,
		m.StacksActive,
		m.TofuRunsTotal,
		m.TofuRunsDuration,
		m.DBConnectionsActive,
		m.DBQueriesTotal,
		m.MonthlyRuntimeEnrollments,
		m.SessionReprojectionsTotal,
		// S7: gRPC Queue Backpressure metrics
		m.GRPCQueueSize,
		m.GRPCQueueCapacity,
		m.GRPCQueueEnqueued,
		m.GRPCQueueDequeued,
		m.GRPCQueueRejected,
		m.GRPCQueueDropped,
	)

	return m
}

// MetricsCollector implements prometheus.Collector to collect dynamic metrics
// from PocketBase collections (jobs, workers, stacks).
type MetricsCollector struct {
	app     core.App
	metrics *Metrics
}

// NewMetricsCollector creates a collector that queries PocketBase for live data.
func NewMetricsCollector(app core.App, metrics *Metrics) *MetricsCollector {
	return &MetricsCollector{app: app, metrics: metrics}
}

// Describe implements prometheus.Collector.
func (c *MetricsCollector) Describe(ch chan<- *prometheus.Desc) {
	// Metrics are already registered, nothing to describe here
}

// Collect implements prometheus.Collector and fetches live data from PocketBase.
// NOTE: Queries are intentionally unscoped (no tenant filtering) because metrics
// are infrastructure-level, not tenant-level. See RegisterMetricsRoutes for access control.
func (c *MetricsCollector) Collect(ch chan<- prometheus.Metric) {
	// Count active jobs
	if jobs, err := c.app.FindCollectionByNameOrId("jobs"); err == nil {
		if records, err := c.app.FindRecordsByFilter(jobs.Id, "state = 'running'", "", 0, 0); err == nil {
			c.metrics.JobsActive.Set(float64(len(records)))
		}
		// Count queued jobs
		if queued, err := c.app.FindRecordsByFilter(jobs.Id, "state = 'pending'", "", 0, 0); err == nil {
			c.metrics.JobQueueSize.Set(float64(len(queued)))
		}
	}

	// Count connected workers using the actual worker collection.
	if workers, err := c.app.FindCollectionByNameOrId("workers"); err == nil {
		if records, err := c.app.FindRecordsByFilter(workers.Id, "status = 'approved'", "", 0, 0); err == nil {
			c.metrics.AgentsConnected.Set(float64(len(records)))
		}
	}

	// Count stacks
	if stacks, err := c.app.FindCollectionByNameOrId("stacks"); err == nil {
		if allStacks, err := c.app.FindRecordsByFilter(stacks.Id, "", "", 0, 0); err == nil {
			c.metrics.StacksTotal.Set(float64(len(allStacks)))
		}
		if activeStacks, err := c.app.FindRecordsByFilter(stacks.Id, "status = 'provisioning' || status = 'running'", "", 0, 0); err == nil {
			c.metrics.StacksActive.Set(float64(len(activeStacks)))
		}
	}

	if _, err := c.app.FindCollectionByNameOrId("vm_lease_enrollment_outbox"); err == nil {
		for _, status := range monthlyRuntimeEnrollmentStatuses {
			records, err := c.app.FindRecordsByFilter(
				"vm_lease_enrollment_outbox",
				"status = {:status}",
				"",
				0,
				0,
				map[string]any{"status": status},
			)
			if err == nil {
				c.metrics.MonthlyRuntimeEnrollments.WithLabelValues(status).Set(float64(len(records)))
			}
		}
	}
}

// RegisterMetricsRoutes adds the /metrics endpoint for Prometheus scraping.
// It creates a new registry to avoid polluting the default global registry.
//
// SECURITY: This endpoint returns instance-wide metrics without tenant filtering.
// In SaaS mode, it MUST be restricted to infrastructure-only access (e.g. Prometheus
// scraper auth via Edge/reverse proxy signed as an admin identity). It must NOT be
// exposed to tenant-facing API routes. The MetricsCollector.Collect method
// intentionally queries all records across tenants to provide accurate operational
// metrics for monitoring.
func RegisterMetricsRoutes(r *httpx.Router, app core.App) *Metrics {
	// Create a new registry (don't use global to avoid conflicts)
	reg := prometheus.NewRegistry()

	// Add default Go runtime collectors
	reg.MustRegister(collectors.NewGoCollector())
	reg.MustRegister(collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))

	// Create and register kombifyTechstack metrics
	metrics := NewMetrics(reg)

	// Create collector for dynamic data
	collector := NewMetricsCollector(app, metrics)
	reg.MustRegister(collector)

	// Feed the session-recovery classifier's tenant-labeled occurrences into
	// this registry (middleware and response classifier live outside routes).
	sessionreauth.SetCounterHook(func(tenantID, outcome string) {
		metrics.SessionReprojectionsTotal.WithLabelValues(tenantID, outcome).Inc()
	})

	// Create handler with our custom registry
	handler := promhttp.HandlerFor(reg, promhttp.HandlerOpts{
		EnableOpenMetrics: true,
	})

	// Register the /metrics endpoint
	r.GET("/metrics", func(e *httpx.Event) error {
		if err := requireMetricsAccess(e); err != nil {
			return err
		}

		// Collect fresh data before serving
		collector.Collect(nil)

		// Serve metrics
		handler.ServeHTTP(e.Response, e.Request)
		return nil
	})

	// Also expose at /api/v1/metrics for consistency
	r.GET("/api/v1/metrics", func(e *httpx.Event) error {
		if err := requireMetricsAccess(e); err != nil {
			return err
		}

		collector.Collect(nil)
		handler.ServeHTTP(e.Response, e.Request)
		return nil
	})

	return metrics
}

func requireMetricsAccess(e *httpx.Event) error {
	return requireAdmin(e)
}
