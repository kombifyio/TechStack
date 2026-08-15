package routes

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/monitoring"
)

func TestBuildMonitoringStatusPayload_PreservesLegacyFieldsAndAddsModeMetadata(t *testing.T) {
	stats := &monitoring.TSDBStats{
		NumSeries:     12,
		MinTime:       1000,
		MaxTime:       2000,
		RetentionDays: 7,
		TopMetrics: []monitoring.MetricCount{{
			Name:  "system_cpu_utilization",
			Count: 1,
		}},
	}
	payload := buildMonitoringStatusPayload(stats, MonitoringStatusMetadata{
		QueryBackend:      "remote-promql",
		QueryBackendURL:   "https://victoriametrics.internal/api",
		IngestBackend:     "embedded-tsdb",
		CollectorMode:     "gateway",
		CompatibilityMode: "dual-ingest",
	})

	if got := payload["seriesCount"]; got != uint64(12) {
		t.Fatalf("expected seriesCount 12, got %v", got)
	}
	if got := payload["queryBackend"]; got != "remote-promql" {
		t.Fatalf("expected queryBackend remote-promql, got %v", got)
	}
	if got := payload["queryBackendURL"]; got != "https://victoriametrics.internal/api" {
		t.Fatalf("expected queryBackendURL to be preserved, got %v", got)
	}
	if got := payload["ingestBackend"]; got != "embedded-tsdb" {
		t.Fatalf("expected ingestBackend embedded-tsdb, got %v", got)
	}
	if got := payload["collectorMode"]; got != "gateway" {
		t.Fatalf("expected collectorMode gateway, got %v", got)
	}
	if got := payload["compatibilityMode"]; got != "dual-ingest" {
		t.Fatalf("expected compatibilityMode dual-ingest, got %v", got)
	}
	if _, ok := payload["topMetrics"]; !ok {
		t.Fatal("expected legacy topMetrics field to remain present")
	}
}

func TestBuildMonitoringStatusPayload_Defaults(t *testing.T) {
	payload := buildMonitoringStatusPayload(&monitoring.TSDBStats{}, MonitoringStatusMetadata{})

	if got := payload["queryBackend"]; got != "embedded-tsdb" {
		t.Fatalf("expected default queryBackend embedded-tsdb, got %v", got)
	}
	if got := payload["ingestBackend"]; got != "embedded-tsdb" {
		t.Fatalf("expected default ingestBackend embedded-tsdb, got %v", got)
	}
	if got := payload["collectorMode"]; got != "direct" {
		t.Fatalf("expected default collectorMode direct, got %v", got)
	}
	if got := payload["compatibilityMode"]; got != "dual-ingest" {
		t.Fatalf("expected default compatibilityMode dual-ingest, got %v", got)
	}
	if _, ok := payload["queryBackendURL"]; ok {
		t.Fatal("expected queryBackendURL to be omitted when empty")
	}
}

func TestBuildMonitoringHealthPayload(t *testing.T) {
	backend := staticHealthBackend{statsErr: errors.New("dial tcp backend: connect: connection refused")}
	payload := buildMonitoringHealthPayload(context.Background(), backend, MonitoringStatusMetadata{
		QueryBackend:      "remote-promql",
		QueryBackendURL:   "https://victoriametrics.internal/select/0/prometheus",
		IngestBackend:     "embedded-tsdb",
		CollectorMode:     "gateway",
		CompatibilityMode: "dual-ingest",
	}, staticIngestHealthProvider{snapshot: monitoring.IngestHealthSnapshot{
		OTLP:       monitoring.IngestLaneHealth{Status: "ok", QueueMode: "synchronous", RequestsAccepted: 3, SamplesAccepted: 12},
		LegacyPush: monitoring.IngestLaneHealth{Status: "degraded", QueueMode: "synchronous", RequestsAccepted: 1, RequestsRejected: 1, ParseErrors: 1, LastError: "agent_id is required"},
	}})

	if got := payload["queryBackendStatus"]; got != "error" {
		t.Fatalf("expected queryBackendStatus error, got %v", got)
	}
	if got := payload["queryBackendError"]; got != "dial tcp backend: connect: connection refused" {
		t.Fatalf("expected queryBackendError to be surfaced, got %v", got)
	}
	if got := payload["collectorMode"]; got != "gateway" {
		t.Fatalf("expected collectorMode gateway, got %v", got)
	}
	if got := payload["ingestStatus"]; got != "degraded" {
		t.Fatalf("expected ingestStatus degraded, got %v", got)
	}
	otlp, ok := payload["otlp"].(monitoring.IngestLaneHealth)
	if !ok {
		t.Fatalf("expected otlp payload to be monitoring.IngestLaneHealth, got %T", payload["otlp"])
	}
	if otlp.RequestsAccepted != 3 {
		t.Fatalf("expected otlp RequestsAccepted 3, got %d", otlp.RequestsAccepted)
	}
	legacy, ok := payload["legacyPush"].(monitoring.IngestLaneHealth)
	if !ok {
		t.Fatalf("expected legacyPush payload to be monitoring.IngestLaneHealth, got %T", payload["legacyPush"])
	}
	if legacy.ParseErrors != 1 {
		t.Fatalf("expected legacy parseErrors 1, got %d", legacy.ParseErrors)
	}
}

func TestBuildMonitoringHealthPayload_DefaultsToUnavailable(t *testing.T) {
	payload := buildMonitoringHealthPayload(context.Background(), staticHealthBackend{}, MonitoringStatusMetadata{IngestBackend: "unavailable", CompatibilityMode: "query-only"}, nil)

	if got := payload["ingestStatus"]; got != "unavailable" {
		t.Fatalf("expected ingestStatus unavailable, got %v", got)
	}
	if got := payload["collectorMode"]; got != "direct" {
		t.Fatalf("expected default collectorMode direct, got %v", got)
	}
	otlp := payload["otlp"].(monitoring.IngestLaneHealth)
	if otlp.Status != "unavailable" {
		t.Fatalf("expected otlp status unavailable, got %s", otlp.Status)
	}
	legacy := payload["legacyPush"].(monitoring.IngestLaneHealth)
	if legacy.Status != "unavailable" {
		t.Fatalf("expected legacyPush status unavailable, got %s", legacy.Status)
	}
}

func TestBuildMonitoringHealthPayload_MapsStaleLaneToDegradedIngest(t *testing.T) {
	payload := buildMonitoringHealthPayload(context.Background(), staticHealthBackend{}, MonitoringStatusMetadata{}, staticIngestHealthProvider{snapshot: monitoring.IngestHealthSnapshot{
		OTLP:       monitoring.IngestLaneHealth{Status: "stale"},
		LegacyPush: monitoring.IngestLaneHealth{Status: "ok"},
	}})

	if got := payload["ingestStatus"]; got != "degraded" {
		t.Fatalf("expected stale lane to degrade overall ingest, got %v", got)
	}
	otlp := payload["otlp"].(monitoring.IngestLaneHealth)
	if otlp.Status != "stale" {
		t.Fatalf("expected lane-level stale status to remain visible, got %s", otlp.Status)
	}
}

func TestBuildMonitoringHealthPayload_AgesRequiredLaneButIgnoresOptionalIdle(t *testing.T) {
	now := time.Date(2026, time.August, 13, 12, 0, 0, 0, time.UTC)
	old := now.Add(-91 * time.Second)
	payload := buildMonitoringHealthPayload(context.Background(), staticHealthBackend{}, MonitoringStatusMetadata{
		IngestFreshnessTTL: 90 * time.Second,
		OTLPRequirement:    "required",
		LegacyRequirement:  "optional",
		Now:                func() time.Time { return now },
	}, staticIngestHealthProvider{snapshot: monitoring.IngestHealthSnapshot{
		OTLP:       monitoring.IngestLaneHealth{Status: "ok", LastSuccessAt: &old},
		LegacyPush: monitoring.IngestLaneHealth{Status: "idle"},
	}})

	if got := payload["ingestStatus"]; got != "degraded" {
		t.Fatalf("expected stale required OTLP lane to degrade ingest, got %v", got)
	}
	otlp := payload["otlp"].(monitoring.IngestLaneHealth)
	if otlp.Status != "stale" || otlp.Requirement != "required" || otlp.FreshnessTTL != "1m30s" {
		t.Fatalf("unexpected OTLP freshness projection: %#v", otlp)
	}
	legacy := payload["legacyPush"].(monitoring.IngestLaneHealth)
	if legacy.Requirement != "optional" || legacy.Status != "idle" {
		t.Fatalf("unexpected optional legacy projection: %#v", legacy)
	}
}

func TestBuildMonitoringHealthPayload_OptionalIdleDoesNotDegradeRequiredHealthyLane(t *testing.T) {
	payload := buildMonitoringHealthPayload(context.Background(), staticHealthBackend{}, MonitoringStatusMetadata{
		OTLPRequirement:   "required",
		LegacyRequirement: "optional",
	}, staticIngestHealthProvider{snapshot: monitoring.IngestHealthSnapshot{
		OTLP:       monitoring.IngestLaneHealth{Status: "ok"},
		LegacyPush: monitoring.IngestLaneHealth{Status: "idle"},
	}})

	if got := payload["ingestStatus"]; got != "ok" {
		t.Fatalf("expected optional idle lane not to degrade required healthy lane, got %v", got)
	}
}

func TestEvaluateMonitoringHealthCallsStatsOnce(t *testing.T) {
	backend := &countingHealthBackend{}
	health := evaluateMonitoringHealth(context.Background(), backend, MonitoringStatusMetadata{}, nil)

	if backend.calls != 1 {
		t.Fatalf("expected one query backend Stats call, got %d", backend.calls)
	}
	if health.queryStatus != "ok" {
		t.Fatalf("expected reachable query backend, got %s", health.queryStatus)
	}
}

func TestBuildMonitoringStatusPayload_NormalizesCollectorMode(t *testing.T) {
	payload := buildMonitoringStatusPayload(&monitoring.TSDBStats{}, MonitoringStatusMetadata{CollectorMode: "unexpected"})

	if got := payload["collectorMode"]; got != "direct" {
		t.Fatalf("expected invalid collectorMode to normalize to direct, got %v", got)
	}
}

func TestMonitoringRange_DefaultsAndOverrides(t *testing.T) {
	now := time.Date(2026, 5, 15, 10, 0, 0, 0, time.UTC)
	start, end, step := monitoringRange(url.Values{}, now)

	if !start.Equal(now.Add(-1 * time.Hour)) {
		t.Fatalf("expected default start one hour before now, got %s", start)
	}
	if !end.Equal(now) {
		t.Fatalf("expected default end now, got %s", end)
	}
	if step != 15*time.Second {
		t.Fatalf("expected default step 15s, got %s", step)
	}

	values := url.Values{
		"start": []string{"2026-05-15T08:00:00Z"},
		"end":   []string{"2026-05-15T09:00:00Z"},
		"step":  []string{"30s"},
	}
	start, end, step = monitoringRange(values, now)

	if !start.Equal(time.Date(2026, 5, 15, 8, 0, 0, 0, time.UTC)) {
		t.Fatalf("expected explicit start to be parsed, got %s", start)
	}
	if !end.Equal(time.Date(2026, 5, 15, 9, 0, 0, 0, time.UTC)) {
		t.Fatalf("expected explicit end to be parsed, got %s", end)
	}
	if step != 30*time.Second {
		t.Fatalf("expected explicit step 30s, got %s", step)
	}
}

func TestMonitoringQueryPolicy_ValidatesQueryAndRangeBudgets(t *testing.T) {
	policy := defaultMonitoringQueryPolicy()
	now := time.Date(2026, 5, 17, 12, 0, 0, 0, time.UTC)

	if err := policy.validateQuery(strings.Repeat("x", policy.MaxQueryLength+1)); err == nil {
		t.Fatal("expected overlong query to fail")
	}
	if err := policy.validateRange(now.Add(-25*time.Hour), now, 30*time.Second); err == nil {
		t.Fatal("expected excessive range to fail")
	}
	if err := policy.validateRange(now.Add(-time.Hour), now, time.Second); err == nil {
		t.Fatal("expected too-small step to fail")
	}
	if err := policy.validateRange(now.Add(-8*time.Hour), now, 5*time.Second); err == nil {
		t.Fatal("expected over-budget point count to fail")
	}
	if err := policy.validateLabelName(strings.Repeat("x", policy.MaxLabelLength+1)); err == nil {
		t.Fatal("expected overlong label name to fail")
	}
}

func TestMonitoringQueryPolicy_AllowsDashboardSizedQueries(t *testing.T) {
	policy := defaultMonitoringQueryPolicy()
	now := time.Date(2026, 5, 17, 12, 0, 0, 0, time.UTC)

	if err := policy.validateQuery(`rate(system_cpu_seconds_total[5m])`); err != nil {
		t.Fatalf("expected dashboard query to pass: %v", err)
	}
	if err := policy.validateRange(now.Add(-time.Hour), now, 15*time.Second); err != nil {
		t.Fatalf("expected dashboard range to pass: %v", err)
	}
	if err := policy.validateLabelName("__name__"); err != nil {
		t.Fatalf("expected metric label to pass: %v", err)
	}
}

func TestMonitoringQueryPolicy_AppliesBackendTimeout(t *testing.T) {
	policy := defaultMonitoringQueryPolicy()
	ctx, cancel := policy.context(context.Background())
	defer cancel()

	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("expected query policy context to have a deadline")
	}
	if until := time.Until(deadline); until <= 0 || until > policy.Timeout {
		t.Fatalf("deadline is outside expected timeout window: %s", until)
	}
}

func TestMonitoringInstantMetricsRejectsMissingQueryBeforeBackend(t *testing.T) {
	handler := monitoringRouteHandlers{
		backend: staticHealthBackend{},
		policy:  defaultMonitoringQueryPolicy(),
	}
	event, recorder := monitoringRouteTestEvent(http.MethodGet, "/api/v1/monitor/metrics/instant")

	if err := handler.instantMetrics(event); err != nil {
		t.Fatalf("instantMetrics returned unexpected error: %v", err)
	}
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body=%s, want 400", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "missing 'q' parameter") {
		t.Fatalf("expected missing query validation error, got: %s", recorder.Body.String())
	}
}

func monitoringRouteTestEvent(method, target string) (*httpx.Event, *httptest.ResponseRecorder) {
	req := httptest.NewRequest(method, target, nil)
	rec := httptest.NewRecorder()
	return &httpx.Event{Request: req, Response: rec}, rec
}

type staticHealthBackend struct {
	statsErr error
}

type countingHealthBackend struct {
	staticHealthBackend
	calls int
}

func (b *countingHealthBackend) Stats(context.Context) (*monitoring.TSDBStats, error) {
	b.calls++
	return &monitoring.TSDBStats{}, nil
}

func (s staticHealthBackend) InstantQuery(context.Context, string, time.Time) (*monitoring.QueryResult, error) {
	return nil, nil
}

func (s staticHealthBackend) RangeQuery(context.Context, string, time.Time, time.Time, time.Duration) (*monitoring.QueryResult, error) {
	return nil, nil
}

func (s staticHealthBackend) LabelNames(context.Context) ([]string, error) {
	return nil, nil
}

func (s staticHealthBackend) LabelValues(context.Context, string) ([]string, error) {
	return nil, nil
}

func (s staticHealthBackend) MetricNames(context.Context) ([]string, error) {
	return nil, nil
}

func (s staticHealthBackend) Stats(context.Context) (*monitoring.TSDBStats, error) {
	if s.statsErr != nil {
		return nil, s.statsErr
	}
	return &monitoring.TSDBStats{}, nil
}

type staticIngestHealthProvider struct {
	snapshot monitoring.IngestHealthSnapshot
}

func (s staticIngestHealthProvider) MonitoringIngestHealth() monitoring.IngestHealthSnapshot {
	return s.snapshot
}
