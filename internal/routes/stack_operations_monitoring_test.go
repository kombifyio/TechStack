package routes

import (
	"context"
	"errors"
	"testing"

	"github.com/kombifyio/techstack/pkg/monitoring"
	"github.com/pocketbase/pocketbase/core"
)

func TestBuildStackKPIsCountsOnlyOperationalResources(t *testing.T) {
	servers := []stackOperationServer{
		{Assignment: "stack", Health: stackServerHealth{State: "healthy"}},
		{Assignment: "stack", Health: stackServerHealth{State: "degraded"}},
		{Assignment: "unassigned", Health: stackServerHealth{State: "healthy"}},
	}
	services := []stackOperationService{
		{Status: "healthy"},
		{Status: "running"},
		{Status: "degraded"},
	}
	alerts := []stackOperationAlertState{{Name: "HighCPU"}, {Name: "DiskFull"}}

	got := buildStackKPIs(servers, services, alerts)
	if got.RegisteredServers != 3 || got.HealthyServers != 1 || got.RunningServices != 2 || got.ActiveAlerts != 2 {
		t.Fatalf("unexpected KPIs: %#v", got)
	}
}

func TestAlertBelongsToStackUsesFirstRecognizedScope(t *testing.T) {
	scope := stackAlertServerScope([]stackOperationServer{
		{ID: " server-a ", Hostname: " Node-A ", AgentID: " agent-a ", Assignment: "stack"},
		{ID: "server-b", Hostname: "node-b", AgentID: "agent-b", Assignment: "unassigned"},
	})

	tests := []struct {
		name       string
		labels     map[string]string
		wantBelong bool
		wantScoped bool
	}{
		{name: "no labels", labels: nil},
		{name: "unknown labels", labels: map[string]string{"service": "api"}},
		{name: "stack match", labels: map[string]string{"stack_id": "stack-1"}, wantBelong: true, wantScoped: true},
		{name: "stack mismatch wins over matching agent", labels: map[string]string{"stack_id": "stack-2", "agent_id": "agent-a"}, wantScoped: true},
		{name: "agent match", labels: map[string]string{"agent_id": "agent-a"}, wantBelong: true, wantScoped: true},
		{name: "first agent alias wins", labels: map[string]string{"agent_id": "agent-other", "agent": "agent-a"}, wantScoped: true},
		{name: "worker match", labels: map[string]string{"worker_id": "server-a"}, wantBelong: true, wantScoped: true},
		{name: "hostname match ignores case", labels: map[string]string{"hostname": "NODE-A"}, wantBelong: true, wantScoped: true},
		{name: "unassigned server is outside stack", labels: map[string]string{"agent_id": "agent-b"}, wantScoped: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			gotBelong, gotScoped := alertBelongsToStack(test.labels, "stack-1", scope)
			if gotBelong != test.wantBelong || gotScoped != test.wantScoped {
				t.Fatalf("alertBelongsToStack() = (%t, %t), want (%t, %t)", gotBelong, gotScoped, test.wantBelong, test.wantScoped)
			}
		})
	}
}

func TestStackScopedAlertsClonesMatchingLabels(t *testing.T) {
	labels := map[string]string{"agent_id": "agent-a"}
	alerts, unscoped := stackScopedAlertsFromStates([]monitoring.AlertState{{
		Rule: monitoring.AlertRule{
			Name:     "HighCPU",
			Severity: "critical",
			Message:  "high CPU",
			Labels:   labels,
		},
		Value: 95,
	}}, "stack-1", []stackOperationServer{{AgentID: "agent-a", Assignment: "stack"}})

	if len(alerts) != 1 || unscoped != 0 {
		t.Fatalf("alerts = %d, unscoped = %d, want 1 and 0", len(alerts), unscoped)
	}
	labels["agent_id"] = "changed"
	if alerts[0].Labels["agent_id"] != "agent-a" {
		t.Fatalf("alert labels were not cloned: %#v", alerts[0].Labels)
	}
	if alerts[0].Name != "HighCPU" || alerts[0].Severity != "critical" || alerts[0].Message != "high CPU" || alerts[0].Value != 95 || alerts[0].Status != "firing" {
		t.Fatalf("unexpected alert projection: %#v", alerts[0])
	}
}

func TestOperationAlertsWithoutEngine(t *testing.T) {
	alerts, unscoped := (stackOperationsRouteHandlers{}).operationAlerts("stack-1", nil)
	if alerts != nil || unscoped != 0 {
		t.Fatalf("operationAlerts() = (%#v, %d), want (nil, 0)", alerts, unscoped)
	}
}

func TestMonitoringSummaryKeepsQueryAndIngestStatesIndependent(t *testing.T) {
	backendError := errors.New("stats failed")
	tests := []struct {
		name             string
		backend          monitoring.MetricsQueryBackend
		metadata         MonitoringStatusMetadata
		ingestHealth     monitoring.IngestHealthProvider
		wantStatus       string
		wantQueryStatus  string
		wantIngestStatus string
		wantProof        string
		wantRangeProof   string
		wantOTLPStatus   string
		wantSeries       uint64
		wantMessage      string
	}{
		{
			name:             "query and ingest unavailable",
			metadata:         MonitoringStatusMetadata{IngestBackend: "unavailable", CompatibilityMode: "query-only"},
			wantStatus:       "unknown",
			wantQueryStatus:  "unknown",
			wantIngestStatus: "unavailable",
			wantProof:        "vector:pending",
			wantRangeProof:   "matrix:pending",
			wantOTLPStatus:   "unavailable",
			wantMessage:      "metrics unavailable",
		},
		{
			name:             "query error preserves healthy ingest",
			backend:          stackOperationsMonitoringBackend{err: backendError},
			ingestHealth:     staticIngestHealthProvider{snapshot: monitoring.IngestHealthSnapshot{OTLP: monitoring.IngestLaneHealth{Status: "ok"}, LegacyPush: monitoring.IngestLaneHealth{Status: "ok"}}},
			wantStatus:       "degraded",
			wantQueryStatus:  "error",
			wantIngestStatus: "ok",
			wantProof:        "vector:pending",
			wantRangeProof:   "matrix:pending",
			wantOTLPStatus:   "ok",
			wantMessage:      backendError.Error(),
		},
		{
			name:             "reachable query leaves ingest unavailable",
			backend:          stackOperationsMonitoringBackend{},
			metadata:         MonitoringStatusMetadata{IngestBackend: "unavailable", CompatibilityMode: "query-only"},
			wantStatus:       "ok",
			wantQueryStatus:  "ok",
			wantIngestStatus: "unavailable",
			wantProof:        "vector:pending",
			wantRangeProof:   "matrix:pending",
			wantOTLPStatus:   "unavailable",
			wantMessage:      "metrics backend reachable",
		},
		{
			name:             "reachable query leaves degraded ingest degraded",
			backend:          stackOperationsMonitoringBackend{stats: &monitoring.TSDBStats{}},
			ingestHealth:     staticIngestHealthProvider{snapshot: monitoring.IngestHealthSnapshot{OTLP: monitoring.IngestLaneHealth{Status: "degraded"}, LegacyPush: monitoring.IngestLaneHealth{Status: "ok"}}},
			wantStatus:       "ok",
			wantQueryStatus:  "ok",
			wantIngestStatus: "degraded",
			wantProof:        "vector:pending",
			wantRangeProof:   "matrix:pending",
			wantOTLPStatus:   "degraded",
			wantMessage:      "metrics backend reachable",
		},
		{
			name:             "reachable query treats stale OTLP as degraded ingest",
			backend:          stackOperationsMonitoringBackend{stats: &monitoring.TSDBStats{}},
			ingestHealth:     staticIngestHealthProvider{snapshot: monitoring.IngestHealthSnapshot{OTLP: monitoring.IngestLaneHealth{Status: "stale"}, LegacyPush: monitoring.IngestLaneHealth{Status: "ok"}}},
			wantStatus:       "ok",
			wantQueryStatus:  "ok",
			wantIngestStatus: "degraded",
			wantProof:        "vector:pending",
			wantRangeProof:   "matrix:pending",
			wantOTLPStatus:   "stale",
			wantMessage:      "metrics backend reachable",
		},
		{
			name:             "reachable query with required idle ingest",
			backend:          stackOperationsMonitoringBackend{},
			wantStatus:       "ok",
			wantQueryStatus:  "ok",
			wantIngestStatus: "degraded",
			wantProof:        "vector:pending",
			wantRangeProof:   "matrix:pending",
			wantOTLPStatus:   "idle",
			wantMessage:      "metrics backend reachable",
		},
		{
			name:             "reachable query with healthy ingest and series",
			backend:          stackOperationsMonitoringBackend{stats: &monitoring.TSDBStats{NumSeries: 7}},
			ingestHealth:     staticIngestHealthProvider{snapshot: monitoring.IngestHealthSnapshot{OTLP: monitoring.IngestLaneHealth{Status: "ok"}, LegacyPush: monitoring.IngestLaneHealth{Status: "ok"}}},
			wantStatus:       "ok",
			wantQueryStatus:  "ok",
			wantIngestStatus: "ok",
			wantProof:        "vector:non-empty",
			wantRangeProof:   "matrix:non-empty",
			wantOTLPStatus:   "ok",
			wantSeries:       7,
			wantMessage:      "metrics backend reachable",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			alerts := monitoring.NewAlertEngine(nil, nil, []monitoring.AlertRule{{Name: "HighCPU"}, {Name: "DiskFull"}}, monitoring.AlertEngineConfig{})
			h := stackOperationsRouteHandlers{backend: test.backend, metadata: test.metadata, alerts: alerts, ingestHealth: test.ingestHealth}

			got := h.monitoringSummary(context.Background())
			if got.Status != test.wantStatus || got.QueryBackendStatus != test.wantQueryStatus || got.IngestStatus != test.wantIngestStatus {
				t.Fatalf("unexpected backend state: %#v", got)
			}
			if got.QueryProof != test.wantProof || got.RangeProof != test.wantRangeProof || got.OTLPStatus != test.wantOTLPStatus || got.SeriesCount != test.wantSeries || got.Message != test.wantMessage {
				t.Fatalf("unexpected monitoring proof: %#v", got)
			}
			wantIngestBackend := defaultMonitoringStatusValue(test.metadata.IngestBackend, "embedded-tsdb")
			wantCompatibilityMode := defaultMonitoringStatusValue(test.metadata.CompatibilityMode, "dual-ingest")
			if got.QueryBackend != "embedded-tsdb" || got.IngestBackend != wantIngestBackend || got.CollectorMode != "direct" || got.CompatibilityMode != wantCompatibilityMode {
				t.Fatalf("unexpected monitoring defaults: %#v", got)
			}
			if got.AlertRuleCount != 2 {
				t.Fatalf("alert rule count = %d, want 2", got.AlertRuleCount)
			}
		})
	}
}

func TestActivityLogMatchesServerUsesMetadataPrecedence(t *testing.T) {
	worker := stackOperationServer{ID: "server-a", AgentID: "agent-a", Hostname: "node-a"}
	managed := stackOperationServer{
		ID:       "lease:lease-a",
		AgentID:  "agent-managed",
		Hostname: "managed-a",
		Source:   managedRuntimeInventorySource,
		LeaseID:  "lease-a",
	}

	tests := []struct {
		name     string
		metadata any
		server   stackOperationServer
		want     bool
	}{
		{name: "missing metadata", server: worker},
		{name: "worker match", metadata: map[string]any{"worker_id": "server-a"}, server: worker, want: true},
		{name: "server alias match", metadata: map[string]any{"server_id": "server-a"}, server: worker, want: true},
		{name: "node alias match", metadata: map[string]any{"node_id": "server-a"}, server: worker, want: true},
		{name: "worker mismatch wins over matching agent", metadata: map[string]any{"worker_id": "other", "agent_id": "agent-a"}, server: worker},
		{name: "agent match", metadata: map[string]any{"agent_id": "agent-a"}, server: worker, want: true},
		{name: "agent alias match", metadata: map[string]any{"agent": "agent-a"}, server: worker, want: true},
		{name: "host match", metadata: map[string]any{"host": "node-a"}, server: worker, want: true},
		{name: "hostname ignores case", metadata: map[string]any{"hostname": "NODE-A"}, server: worker, want: true},
		{name: "unknown metadata", metadata: map[string]any{"service": "api"}, server: worker},
		{name: "managed lease match wins over worker mismatch", metadata: map[string]any{"lease_id": "lease-a", "worker_id": "other"}, server: managed, want: true},
		{name: "managed runtime lease alias match", metadata: map[string]any{"runtime_lease_id": "lease-a"}, server: managed, want: true},
		{name: "managed lease mismatch can fall through", metadata: map[string]any{"lease_id": "other", "agent_id": "agent-managed"}, server: managed, want: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			record := core.NewRecord(core.NewBaseCollection("activity_log"))
			if test.metadata != nil {
				record.Set("metadata", test.metadata)
			}
			if got := activityLogMatchesServer(record, test.server); got != test.want {
				t.Fatalf("activityLogMatchesServer() = %t, want %t", got, test.want)
			}
		})
	}
}

type stackOperationsMonitoringBackend struct {
	staticHealthBackend
	stats *monitoring.TSDBStats
	err   error
}

func (b stackOperationsMonitoringBackend) Stats(context.Context) (*monitoring.TSDBStats, error) {
	return b.stats, b.err
}
