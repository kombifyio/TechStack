// Package routes provides custom HTTP routes for kombifyTechstack API.
// metrics.go provides the Prometheus /metrics endpoint for observability.
package routes

import (
	"time"

	"github.com/kombifyio/techstack/internal/routes/sessionreauth"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/ril/workflow"
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

	// Durable RIL workflow metrics. Labels are deliberately bounded to the
	// closed workflow/step definitions; run, owner, tenant, and signal IDs are
	// never metric labels.
	WorkflowRunTransitions  *prometheus.CounterVec
	WorkflowRunDuration     *prometheus.HistogramVec
	WorkflowStepTransitions *prometheus.CounterVec
	WorkflowStepDuration    *prometheus.HistogramVec
	WorkflowTimers          *prometheus.CounterVec

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
		WorkflowRunTransitions: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: "techstack", Subsystem: "ril_workflow", Name: "run_transitions_total",
				Help: "Durable RIL workflow run transitions by closed workflow type and state.",
			},
			[]string{"type", "from", "to"},
		),
		WorkflowRunDuration: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Namespace: "techstack", Subsystem: "ril_workflow", Name: "run_duration_seconds",
				Help:    "Terminal durable RIL workflow run duration in seconds.",
				Buckets: []float64{0.1, 1, 5, 15, 30, 60, 300, 900, 3600, 21600, 86400},
			},
			[]string{"type", "status"},
		),
		WorkflowStepTransitions: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: "techstack", Subsystem: "ril_workflow", Name: "step_transitions_total",
				Help: "Durable RIL workflow step transitions by closed workflow and step definition.",
			},
			[]string{"type", "step", "from", "to"},
		),
		WorkflowStepDuration: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Namespace: "techstack", Subsystem: "ril_workflow", Name: "step_duration_seconds",
				Help:    "Terminal durable RIL workflow step attempt duration in seconds.",
				Buckets: []float64{0.01, 0.1, 0.5, 1, 5, 15, 30, 60, 300, 900},
			},
			[]string{"type", "step", "status"},
		),
		WorkflowTimers: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: "techstack", Subsystem: "ril_workflow", Name: "timers_total",
				Help: "Durable RIL workflow timer sweep outcomes by timer kind.",
			},
			[]string{"kind", "outcome"},
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
		m.WorkflowRunTransitions,
		m.WorkflowRunDuration,
		m.WorkflowStepTransitions,
		m.WorkflowStepDuration,
		m.WorkflowTimers,
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

// ObserveRunTransition implements workflow.Observer.
func (m *Metrics) ObserveRunTransition(runType workflow.RunType, from, to workflow.RunStatus, elapsed time.Duration) {
	m.WorkflowRunTransitions.WithLabelValues(string(runType), string(from), string(to)).Inc()
	if elapsed > 0 && workflow.IsTerminal(to) {
		m.WorkflowRunDuration.WithLabelValues(string(runType), string(to)).Observe(elapsed.Seconds())
	}
}

// ObserveStepTransition implements workflow.Observer.
func (m *Metrics) ObserveStepTransition(runType workflow.RunType, step string, from, to workflow.StepStatus, _ int, elapsed time.Duration) {
	m.WorkflowStepTransitions.WithLabelValues(string(runType), step, string(from), string(to)).Inc()
	if elapsed > 0 && (to == workflow.StepCompleted || to == workflow.StepFailed) {
		m.WorkflowStepDuration.WithLabelValues(string(runType), step, string(to)).Observe(elapsed.Seconds())
	}
}

// ObserveTimer implements workflow.Observer.
func (m *Metrics) ObserveTimer(kind workflow.TimerKind, outcome string) {
	m.WorkflowTimers.WithLabelValues(string(kind), outcome).Inc()
}

var _ workflow.Observer = (*Metrics)(nil)

// RegisterMetricsRoutes adds the /metrics endpoint for Prometheus scraping.
// It creates a new registry to avoid polluting the default global registry.
//
// SECURITY: This endpoint returns instance-wide metrics without tenant filtering.
// In SaaS mode, it MUST be restricted to infrastructure-only access (e.g. Prometheus
// scraper auth via Edge/reverse proxy signed as an admin identity). It must NOT be
// exposed to tenant-facing API routes.
func RegisterMetricsRoutes(r *httpx.Router) *Metrics {
	// Create a new registry (don't use global to avoid conflicts)
	reg := prometheus.NewRegistry()

	// Add default Go runtime collectors
	reg.MustRegister(collectors.NewGoCollector())
	reg.MustRegister(collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))

	// Create and register kombifyTechstack metrics
	metrics := NewMetrics(reg)

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

		// Serve metrics
		handler.ServeHTTP(e.Response, e.Request)
		return nil
	})

	// Also expose at /api/v1/metrics for consistency
	r.GET("/api/v1/metrics", func(e *httpx.Event) error {
		if err := requireMetricsAccess(e); err != nil {
			return err
		}

		handler.ServeHTTP(e.Response, e.Request)
		return nil
	})

	return metrics
}

func requireMetricsAccess(e *httpx.Event) error {
	return requireAdmin(e)
}
