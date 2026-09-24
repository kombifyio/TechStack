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

	"github.com/prometheus/prometheus/model/labels"

	"github.com/kombifyio/techstack/internal/routes/tenantguard"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/identity"
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

func TestMonitoringMetricAPIsScopeEveryReadToSignedTenant(t *testing.T) {
	tenantguard.Configure(true)
	t.Cleanup(func() { tenantguard.Configure(false) })
	backend, err := monitoring.NewMonitorTSDB(monitoring.TSDBConfig{DataDir: t.TempDir()})
	if err != nil {
		t.Fatalf("open monitoring TSDB: %v", err)
	}
	t.Cleanup(func() { _ = backend.Close() })
	now := time.Now().UTC()
	if err := backend.Write([]monitoring.MetricSample{
		{Name: "cpu_usage", Value: 1, Timestamp: now.Add(-30 * time.Second), Labels: map[string]string{"tenant_id": "tenant-1", "hostname": "tenant-a-host", "job": "guard", "instance": "same", "tenant_a_label": "present"}},
		{Name: "cpu_usage", Value: 2, Timestamp: now.Add(-30 * time.Second), Labels: map[string]string{"tenant_id": "tenant-2", "hostname": "tenant-b-secret-host", "job": "guard", "instance": "same", "tenant_b_secret_label": "secret"}},
		{Name: "cpu_usage", Value: 3, Timestamp: now.Add(-30 * time.Second), Labels: map[string]string{"hostname": "unlabeled-secret-host", "job": "guard", "instance": "same", "unlabeled_secret_label": "secret"}},
		{Name: "target_info", Value: 1, Timestamp: now.Add(-30 * time.Second), Labels: map[string]string{"tenant_id": "tenant-1", "job": "guard", "instance": "same", "region": "tenant-a-region"}},
		{Name: "target_info", Value: 1, Timestamp: now.Add(-30 * time.Second), Labels: map[string]string{"tenant_id": "tenant-2", "job": "guard", "instance": "same", "region": "tenant-b-secret-region"}},
		{Name: "target_info", Value: 1, Timestamp: now.Add(-30 * time.Second), Labels: map[string]string{"job": "guard", "instance": "same", "region": "unlabeled-secret-region"}},
		{Name: "tenant_a_only_metric", Value: 1, Timestamp: now.Add(-30 * time.Second), Labels: map[string]string{"tenant_id": "tenant-1"}},
		{Name: "tenant_b_secret_metric", Value: 1, Timestamp: now.Add(-30 * time.Second), Labels: map[string]string{"tenant_id": "tenant-2"}},
		{Name: "unlabeled_secret_metric", Value: 1, Timestamp: now.Add(-30 * time.Second)},
	}); err != nil {
		t.Fatalf("seed monitoring TSDB: %v", err)
	}
	handler := monitoringRouteHandlers{backend: backend, policy: defaultMonitoringQueryPolicy()}

	cases := []struct {
		name      string
		target    string
		handle    func(*httpx.Event) error
		visible   []string
		invisible []string
	}{
		{name: "instant", target: `/api/v1/monitor/metrics/instant?q=cpu_usage&tenant_id=tenant-2`, handle: handler.instantMetrics, visible: []string{"tenant-a-host"}, invisible: []string{"tenant-b-secret", "unlabeled-secret"}},
		{name: "range", target: `/api/v1/monitor/metrics/query?q=cpu_usage`, handle: handler.rangeMetrics, visible: []string{"tenant-a-host"}, invisible: []string{"tenant-b-secret", "unlabeled-secret"}},
		{name: "label names", target: `/api/v1/monitor/metrics/labels`, handle: handler.labelNames, visible: []string{"tenant_a_label"}, invisible: []string{"tenant_b_secret_label", "unlabeled_secret_label"}},
		{name: "label values", target: `/api/v1/monitor/metrics/values?name=hostname`, handle: handler.labelValues, visible: []string{"tenant-a-host"}, invisible: []string{"tenant-b-secret-host", "unlabeled-secret-host"}},
		{name: "metric names", target: `/api/v1/monitor/metrics/names`, handle: handler.metricNames, visible: []string{"tenant_a_only_metric"}, invisible: []string{"tenant_b_secret_metric", "unlabeled_secret_metric"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			event, recorder := monitoringRouteTestEvent(http.MethodGet, tc.target)
			event.Request.Header.Set("X-Tenant-ID", "tenant-2")
			event.Request = event.Request.WithContext(identity.NewContext(event.Request.Context(), &identity.Identity{
				UserID: "auth0|owner-1",
				OrgID:  "tenant-1",
			}))
			if err := tc.handle(event); err != nil {
				t.Fatalf("handler returned error: %v", err)
			}
			if recorder.Code != http.StatusOK {
				t.Fatalf("status = %d body=%s, want 200", recorder.Code, recorder.Body.String())
			}
			body := recorder.Body.String()
			for _, value := range tc.visible {
				if !strings.Contains(body, value) {
					t.Fatalf("response omits owned value %q: %s", value, body)
				}
			}
			for _, value := range tc.invisible {
				if strings.Contains(body, value) {
					t.Fatalf("response exposed foreign or unattributed value %q: %s", value, body)
				}
			}
		})
	}

	infoEvent, infoRecorder := monitoringRouteTestEvent(http.MethodGet, `/api/v1/monitor/metrics/instant?q=info(cpu_usage)`)
	infoEvent.Request = infoEvent.Request.WithContext(identity.NewContext(infoEvent.Request.Context(), &identity.Identity{UserID: "auth0|owner-1", OrgID: "tenant-1"}))
	if err := handler.instantMetrics(infoEvent); err != nil || infoRecorder.Code != http.StatusBadRequest {
		t.Fatalf("experimental implicit info lookup = status %d err %v, want 400", infoRecorder.Code, err)
	}

	event, _ := monitoringRouteTestEvent(http.MethodGet, `/api/v1/monitor/metrics/instant?q=cpu_usage&tenant_id=tenant-1`)
	event.Request.Header.Set("X-Tenant-ID", "tenant-1")
	event.Request = event.Request.WithContext(identity.NewContext(event.Request.Context(), &identity.Identity{UserID: "auth0|owner-1"}))
	err = handler.instantMetrics(event)
	var apiErr *httpx.APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusForbidden {
		t.Fatalf("tenant-less SaaS metric read error = %v, want 403", err)
	}

	statsCalls, ingestCalls := 0, 0
	diagnosticHandler := monitoringRouteHandlers{
		backend:      staticHealthBackend{statsCalls: &statsCalls},
		ingestHealth: staticIngestHealthProvider{calls: &ingestCalls},
	}
	for _, diagnostic := range []struct {
		target string
		handle func(*httpx.Event) error
	}{
		{target: "/api/v1/monitor/status", handle: diagnosticHandler.status},
		{target: "/api/v1/monitor/health", handle: diagnosticHandler.health},
	} {
		event, _ := monitoringRouteTestEvent(http.MethodGet, diagnostic.target)
		event.Request = event.Request.WithContext(identity.NewContext(event.Request.Context(), &identity.Identity{UserID: "auth0|owner-1", OrgID: "tenant-1"}))
		err = diagnostic.handle(event)
		if !errors.As(err, &apiErr) || apiErr.Status != http.StatusForbidden {
			t.Fatalf("hosted diagnostic %s error = %v, want 403", diagnostic.target, err)
		}
	}
	if statsCalls != 0 || ingestCalls != 0 {
		t.Fatalf("hosted diagnostics invoked global backend: stats=%d ingest=%d", statsCalls, ingestCalls)
	}
}

func monitoringRouteTestEvent(method, target string) (*httpx.Event, *httptest.ResponseRecorder) {
	req := httptest.NewRequest(method, target, nil)
	rec := httptest.NewRecorder()
	return &httpx.Event{Request: req, Response: rec}, rec
}

type staticHealthBackend struct {
	statsErr   error
	statsCalls *int
}

func (s staticHealthBackend) InstantQuery(context.Context, string, time.Time) (*monitoring.QueryResult, error) {
	return nil, nil
}

func (s staticHealthBackend) RangeQuery(context.Context, string, time.Time, time.Time, time.Duration) (*monitoring.QueryResult, error) {
	return nil, nil
}

func (s staticHealthBackend) LabelNames(context.Context, ...*labels.Matcher) ([]string, error) {
	return nil, nil
}

func (s staticHealthBackend) LabelValues(context.Context, string, ...*labels.Matcher) ([]string, error) {
	return nil, nil
}

func (s staticHealthBackend) MetricNames(context.Context, ...*labels.Matcher) ([]string, error) {
	return nil, nil
}

func (s staticHealthBackend) Stats(context.Context) (*monitoring.TSDBStats, error) {
	if s.statsCalls != nil {
		(*s.statsCalls)++
	}
	if s.statsErr != nil {
		return nil, s.statsErr
	}
	return &monitoring.TSDBStats{}, nil
}

type staticIngestHealthProvider struct {
	snapshot monitoring.IngestHealthSnapshot
	calls    *int
}

func (s staticIngestHealthProvider) MonitoringIngestHealth() monitoring.IngestHealthSnapshot {
	if s.calls != nil {
		(*s.calls)++
	}
	return s.snapshot
}
