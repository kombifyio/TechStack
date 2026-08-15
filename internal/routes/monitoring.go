package routes

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	ksapi "github.com/kombifyio/techstack/pkg/api"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/monitoring"
	"github.com/pocketbase/pocketbase/core"
)

const (
	monitoringStatusDegraded = "degraded"
	monitoringStatusError    = "error"
	monitoringStatusUnknown  = "unknown"
)

// MonitoringStatusMetadata describes the active monitoring runtime mode for
// API consumers and debugging agents.
type MonitoringStatusMetadata struct {
	QueryBackend       string
	QueryBackendURL    string
	IngestBackend      string
	CollectorMode      string
	CompatibilityMode  string
	IngestFreshnessTTL time.Duration
	OTLPRequirement    string
	LegacyRequirement  string
	Now                func() time.Time
}

// RegisterMonitoringRoutes adds monitoring API endpoints for PromQL queries,
// label enumeration, and backend diagnostics.
func RegisterMonitoringRoutes(r *httpx.Router, app core.App, backend monitoring.MetricsQueryBackend, metadata MonitoringStatusMetadata, ingestHealth monitoring.IngestHealthProvider) {
	if backend == nil {
		return
	}

	handlers := monitoringRouteHandlers{
		backend:      backend,
		metadata:     metadata,
		ingestHealth: ingestHealth,
		policy:       defaultMonitoringQueryPolicy(),
	}
	r.GET("/api/v1/monitor/metrics/instant", monitoringRequireAuth(handlers.instantMetrics))
	r.GET("/api/v1/monitor/metrics/query", monitoringRequireAuth(handlers.rangeMetrics))
	r.GET("/api/v1/monitor/metrics/labels", monitoringRequireAuth(handlers.labelNames))
	r.GET("/api/v1/monitor/metrics/values", monitoringRequireAuth(handlers.labelValues))
	r.GET("/api/v1/monitor/metrics/names", monitoringRequireAuth(handlers.metricNames))
	r.GET("/api/v1/monitor/status", monitoringRequireAuth(handlers.status))
	r.GET("/api/v1/monitor/health", monitoringRequireAuth(handlers.health))
}

type monitoringRouteHandlers struct {
	backend      monitoring.MetricsQueryBackend
	metadata     MonitoringStatusMetadata
	ingestHealth monitoring.IngestHealthProvider
	policy       monitoringQueryPolicy
}

type monitoringQueryPolicy struct {
	MaxQueryLength int
	MaxRange       time.Duration
	MinStep        time.Duration
	MaxPoints      int
	Timeout        time.Duration
	MaxLabelLength int
}

func defaultMonitoringQueryPolicy() monitoringQueryPolicy {
	return monitoringQueryPolicy{
		MaxQueryLength: 2048,
		MaxRange:       24 * time.Hour,
		MinStep:        5 * time.Second,
		MaxPoints:      5000,
		Timeout:        15 * time.Second,
		MaxLabelLength: 128,
	}
}

func monitoringRequireAuth(next func(*httpx.Event) error) func(*httpx.Event) error {
	return func(e *httpx.Event) error {
		if _, err := requireAuth(e); err != nil {
			return err
		}
		return next(e)
	}
}

func (h monitoringRouteHandlers) instantMetrics(e *httpx.Event) error {
	query := e.Request.URL.Query().Get("q")
	if query == "" {
		return httpx.Error(e, http.StatusBadRequest, ksapi.ErrCodeValidation, "missing 'q' parameter", nil)
	}
	if err := h.policy.validateQuery(query); err != nil {
		return httpx.Error(e, http.StatusBadRequest, ksapi.ErrCodeValidation, err.Error(), nil)
	}

	ctx, cancel := h.policy.context(e.Request.Context())
	defer cancel()
	result, err := h.backend.InstantQuery(ctx, query, monitoringInstantTime(e.Request.URL.Query(), time.Now()))
	if err != nil {
		return httpx.Error(e, http.StatusBadRequest, ksapi.ErrCodeValidation, err.Error(), nil)
	}
	return monitoringQuerySuccess(e, result)
}

func (h monitoringRouteHandlers) rangeMetrics(e *httpx.Event) error {
	query := e.Request.URL.Query().Get("q")
	if query == "" {
		return httpx.Error(e, http.StatusBadRequest, ksapi.ErrCodeValidation, "missing 'q' parameter", nil)
	}
	if err := h.policy.validateQuery(query); err != nil {
		return httpx.Error(e, http.StatusBadRequest, ksapi.ErrCodeValidation, err.Error(), nil)
	}

	start, end, step := monitoringRange(e.Request.URL.Query(), time.Now())
	if err := h.policy.validateRange(start, end, step); err != nil {
		return httpx.Error(e, http.StatusBadRequest, ksapi.ErrCodeValidation, err.Error(), nil)
	}
	ctx, cancel := h.policy.context(e.Request.Context())
	defer cancel()
	result, err := h.backend.RangeQuery(ctx, query, start, end, step)
	if err != nil {
		return httpx.Error(e, http.StatusBadRequest, ksapi.ErrCodeValidation, err.Error(), nil)
	}
	return monitoringQuerySuccess(e, result)
}

func (h monitoringRouteHandlers) labelNames(e *httpx.Event) error {
	return h.stringList(e, h.backend.LabelNames)
}

func (h monitoringRouteHandlers) labelValues(e *httpx.Event) error {
	name := e.Request.URL.Query().Get("name")
	if name == "" {
		return httpx.Error(e, http.StatusBadRequest, ksapi.ErrCodeValidation, "missing 'name' parameter", nil)
	}
	if err := h.policy.validateLabelName(name); err != nil {
		return httpx.Error(e, http.StatusBadRequest, ksapi.ErrCodeValidation, err.Error(), nil)
	}

	ctx, cancel := h.policy.context(e.Request.Context())
	defer cancel()
	vals, err := h.backend.LabelValues(ctx, name)
	if err != nil {
		return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, err.Error(), nil)
	}
	return httpx.Success(e, http.StatusOK, vals)
}

func (h monitoringRouteHandlers) metricNames(e *httpx.Event) error {
	return h.stringList(e, h.backend.MetricNames)
}

func (h monitoringRouteHandlers) stringList(e *httpx.Event, load func(context.Context) ([]string, error)) error {
	ctx, cancel := h.policy.context(e.Request.Context())
	defer cancel()
	names, err := load(ctx)
	if err != nil {
		return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, err.Error(), nil)
	}
	return httpx.Success(e, http.StatusOK, names)
}

func (p monitoringQueryPolicy) context(parent context.Context) (context.Context, context.CancelFunc) {
	if p.Timeout <= 0 {
		return parent, func() {}
	}
	return context.WithTimeout(parent, p.Timeout)
}

func (p monitoringQueryPolicy) validateQuery(query string) error {
	if p.MaxQueryLength > 0 && len(query) > p.MaxQueryLength {
		return fmt.Errorf("query exceeds %d characters", p.MaxQueryLength)
	}
	return nil
}

func (p monitoringQueryPolicy) validateRange(start, end time.Time, step time.Duration) error {
	if !end.After(start) {
		return fmt.Errorf("end must be after start")
	}
	window := end.Sub(start)
	if p.MaxRange > 0 && window > p.MaxRange {
		return fmt.Errorf("range exceeds %s", p.MaxRange)
	}
	if p.MinStep > 0 && step < p.MinStep {
		return fmt.Errorf("step must be at least %s", p.MinStep)
	}
	if step <= 0 {
		return fmt.Errorf("step must be positive")
	}
	if p.MaxPoints > 0 {
		points := int(window/step) + 1
		if points > p.MaxPoints {
			return fmt.Errorf("range would produce %d points, limit is %d", points, p.MaxPoints)
		}
	}
	return nil
}

func (p monitoringQueryPolicy) validateLabelName(name string) error {
	if p.MaxLabelLength > 0 && len(name) > p.MaxLabelLength {
		return fmt.Errorf("label name exceeds %d characters", p.MaxLabelLength)
	}
	return nil
}

func (h monitoringRouteHandlers) status(e *httpx.Event) error {
	stats, err := h.backend.Stats(e.Request.Context())
	if err != nil {
		return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, err.Error(), nil)
	}
	return httpx.Success(e, http.StatusOK, buildMonitoringStatusPayload(stats, h.metadata))
}

func (h monitoringRouteHandlers) health(e *httpx.Event) error {
	payload := buildMonitoringHealthPayload(e.Request.Context(), h.backend, h.metadata, h.ingestHealth)
	return httpx.Success(e, http.StatusOK, payload)
}

func monitoringQuerySuccess(e *httpx.Event, result *monitoring.QueryResult) error {
	return httpx.Success(e, http.StatusOK, map[string]any{
		"resultType": string(result.Value.Type()),
		"result":     result.Value.String(),
	})
}

func monitoringInstantTime(values url.Values, fallback time.Time) time.Time {
	if raw := values.Get("time"); raw != "" {
		if parsed, err := parseTimeParam(raw); err == nil {
			return parsed
		}
	}
	return fallback
}

func monitoringRange(values url.Values, now time.Time) (time.Time, time.Time, time.Duration) {
	start := now.Add(-1 * time.Hour)
	end := now
	step := 15 * time.Second

	if raw := values.Get("start"); raw != "" {
		if parsed, err := parseTimeParam(raw); err == nil {
			start = parsed
		}
	}
	if raw := values.Get("end"); raw != "" {
		if parsed, err := parseTimeParam(raw); err == nil {
			end = parsed
		}
	}
	if raw := values.Get("step"); raw != "" {
		if parsed, err := time.ParseDuration(raw); err == nil {
			step = parsed
		}
	}
	return start, end, step
}

func buildMonitoringStatusPayload(stats *monitoring.TSDBStats, metadata MonitoringStatusMetadata) map[string]any {
	payload := map[string]any{
		"seriesCount":       stats.NumSeries,
		"minTimeMs":         stats.MinTime,
		"maxTimeMs":         stats.MaxTime,
		"retentionDays":     stats.RetentionDays,
		"topMetrics":        stats.TopMetrics,
		"queryBackend":      defaultMonitoringStatusValue(metadata.QueryBackend, "embedded-tsdb"),
		"ingestBackend":     defaultMonitoringStatusValue(metadata.IngestBackend, "embedded-tsdb"),
		"collectorMode":     normalizeMonitoringCollectorMode(metadata.CollectorMode),
		"compatibilityMode": defaultMonitoringStatusValue(metadata.CompatibilityMode, "dual-ingest"),
	}
	if metadata.QueryBackendURL != "" {
		payload["queryBackendURL"] = metadata.QueryBackendURL
	}
	return payload
}

func defaultMonitoringStatusValue(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func normalizeMonitoringCollectorMode(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "direct":
		return "direct"
	case "gateway":
		return "gateway"
	case "mixed":
		return "mixed"
	default:
		return "direct"
	}
}

func buildMonitoringHealthPayload(ctx context.Context, backend monitoring.MetricsQueryBackend, metadata MonitoringStatusMetadata, ingestHealth monitoring.IngestHealthProvider) map[string]any {
	health := evaluateMonitoringHealth(ctx, backend, metadata, ingestHealth)

	payload := map[string]any{
		"queryBackend":       defaultMonitoringStatusValue(metadata.QueryBackend, "embedded-tsdb"),
		"queryBackendStatus": health.queryStatus,
		"ingestBackend":      defaultMonitoringStatusValue(metadata.IngestBackend, "embedded-tsdb"),
		"collectorMode":      normalizeMonitoringCollectorMode(metadata.CollectorMode),
		"compatibilityMode":  defaultMonitoringStatusValue(metadata.CompatibilityMode, "dual-ingest"),
		"ingestStatus":       health.ingestStatus,
		"otlp":               health.otlp,
		"legacyPush":         health.legacyPush,
	}
	if metadata.QueryBackendURL != "" {
		payload["queryBackendURL"] = metadata.QueryBackendURL
	}
	if health.queryError != "" {
		payload["queryBackendError"] = health.queryError
	}
	return payload
}

type monitoringHealthEvaluation struct {
	stats        *monitoring.TSDBStats
	queryStatus  string
	queryError   string
	ingestStatus string
	otlp         monitoring.IngestLaneHealth
	legacyPush   monitoring.IngestLaneHealth
}

func evaluateMonitoringHealth(ctx context.Context, backend monitoring.MetricsQueryBackend, metadata MonitoringStatusMetadata, provider monitoring.IngestHealthProvider) monitoringHealthEvaluation {
	snapshot := monitoring.IngestHealthSnapshot{}
	if provider != nil {
		snapshot = provider.MonitoringIngestHealth()
	}
	now := time.Now().UTC()
	if metadata.Now != nil {
		now = metadata.Now().UTC()
	}
	otlp := normalizeIngestLaneHealth(snapshot.OTLP, metadata, metadata.OTLPRequirement, "required", now)
	legacy := normalizeIngestLaneHealth(snapshot.LegacyPush, metadata, metadata.LegacyRequirement, "optional", now)
	health := monitoringHealthEvaluation{
		queryStatus:  monitoringStatusUnknown,
		ingestStatus: overallIngestStatus(metadata, otlp, legacy),
		otlp:         otlp,
		legacyPush:   legacy,
	}
	if backend == nil {
		return health
	}
	stats, err := backend.Stats(ctx)
	if err != nil {
		health.stats = nil
		health.queryStatus = monitoringStatusError
		health.queryError = err.Error()
		return health
	}
	health.stats = stats
	health.queryStatus = "ok"
	return health
}

func normalizeIngestLaneHealth(lane monitoring.IngestLaneHealth, metadata MonitoringStatusMetadata, requirement, fallbackRequirement string, now time.Time) monitoring.IngestLaneHealth {
	lane.Requirement = normalizeMonitoringLaneRequirement(requirement, fallbackRequirement)
	ttl := metadata.IngestFreshnessTTL
	if ttl <= 0 {
		ttl = 90 * time.Second
	}
	lane.FreshnessTTL = ttl.String()
	if lane.Requirement == "disabled" {
		lane.Status = "disabled"
		return lane
	}
	if lane.Status == "" {
		if defaultMonitoringStatusValue(metadata.IngestBackend, "embedded-tsdb") == "unavailable" {
			lane.Status = "unavailable"
		} else {
			lane.Status = "idle"
		}
	}
	if lane.QueueMode == "" {
		lane.QueueMode = "synchronous"
	}
	if lane.LastSuccessAt != nil && now.Sub(lane.LastSuccessAt.UTC()) > ttl && lane.Status != "error" && lane.Status != "unavailable" {
		lane.Status = "stale"
	}
	return lane
}

func normalizeMonitoringLaneRequirement(value, fallback string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "optional":
		return "optional"
	case "disabled":
		return "disabled"
	case "required":
		return "required"
	default:
		if fallback == "optional" || fallback == "disabled" {
			return fallback
		}
		return "required"
	}
}

func overallIngestStatus(metadata MonitoringStatusMetadata, lanes ...monitoring.IngestLaneHealth) string {
	if defaultMonitoringStatusValue(metadata.IngestBackend, "embedded-tsdb") == "unavailable" {
		return "unavailable"
	}
	activeRequired := false
	allIdle := true
	for _, lane := range lanes {
		if lane.Requirement == "optional" {
			if lane.Status == "degraded" || lane.Status == "error" {
				return monitoringStatusDegraded
			}
			continue
		}
		if lane.Requirement != "required" {
			continue
		}
		activeRequired = true
		switch lane.Status {
		case "ok":
			allIdle = false
		case "idle":
			return monitoringStatusDegraded
		default:
			return monitoringStatusDegraded
		}
	}
	if !activeRequired || allIdle {
		return "idle"
	}
	return "ok"
}

// RegisterAlertRoutes adds alert-related monitoring endpoints.
// Separate from RegisterMonitoringRoutes to allow independent alert engine lifecycle.
func RegisterAlertRoutes(r *httpx.Router, app core.App, engine *monitoring.AlertEngine) {
	if engine == nil {
		return
	}

	// Active alerts
	r.GET("/api/v1/monitor/alerts", func(e *httpx.Event) error {
		if _, err := requireAuth(e); err != nil {
			return err
		}
		return httpx.Success(e, http.StatusOK, engine.ActiveAlerts())
	})

	// All alert rule states
	r.GET("/api/v1/monitor/alerts/rules", func(e *httpx.Event) error {
		if _, err := requireAuth(e); err != nil {
			return err
		}
		return httpx.Success(e, http.StatusOK, engine.AllStates())
	})
}

// parseTimeParam parses a time parameter as RFC3339, Unix timestamp, or relative duration.
func parseTimeParam(s string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	// Try as duration relative to now
	if d, err := time.ParseDuration(s); err == nil {
		return time.Now().Add(d), nil
	}
	return time.Time{}, nil
}
