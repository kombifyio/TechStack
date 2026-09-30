package routes

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/kombifyio/techstack/internal/routes/tenantguard"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/identity"
	"github.com/kombifyio/techstack/pkg/monitoring"
)

// A Guard heartbeat is only useful in SaaS if its host metrics carry the
// tenant of the authenticated worker: the monitor query adds tenant_id to every
// selector, so an unlabeled series is invisible to its own tenant.
func TestWorkerHeartbeatMetricsAreQueryableOnlyByTheWorkerTenant(t *testing.T) {
	t.Setenv("TECHSTACK_WORKER_AGENT_TOKEN_SECRET", "worker-secret")
	store := controlplane.NewMemoryStore()
	token := enrolledInventoryAgent(t, store)
	backend, err := monitoring.NewMonitorTSDB(monitoring.TSDBConfig{DataDir: t.TempDir()})
	if err != nil {
		t.Fatalf("open monitoring TSDB: %v", err)
	}
	t.Cleanup(func() { _ = backend.Close() })

	body := fmt.Sprintf(`{"source_epoch":"epoch-hb","source_sequence":1,"observed_at":%q,"cpu_percent":42,"memory_used_bytes":512,"memory_total_bytes":1024}`,
		time.Now().UTC().Format(time.RFC3339Nano))
	handler := workerRouteHandlers{wst: store, registryStore: store, serverStore: store, rilStore: store, metricWriter: backend}
	event, recorder := workerRouteTestEvent(http.MethodPost, "/api/v1/workers/runtime-1/heartbeat", body)
	event.Request.SetPathValue("id", "runtime-1")
	event.Request.Header.Set("Authorization", "Bearer "+token)
	// The Guard's tenant hint only selects the worker record its token must
	// match; the sample tenant comes from that record.
	event.Request.Header.Set("X-Kombify-Tenant-ID", "tenant-1")
	if err := handler.heartbeat(event); err != nil || recorder.Code != http.StatusOK {
		t.Fatalf("heartbeat = status %d body=%s err=%v", recorder.Code, recorder.Body.String(), err)
	}

	tenantguard.Configure(true)
	t.Cleanup(func() { tenantguard.Configure(false) })
	monitor := monitoringRouteHandlers{backend: backend, policy: defaultMonitoringQueryPolicy()}
	query := url.QueryEscape(`{__name__=~"node_(cpu|memory|disk)_usage_percent",worker_id="runtime-1"}`)
	read := func(tenantID string) string {
		t.Helper()
		event, recorder := monitoringRouteTestEvent(http.MethodGet, "/api/v1/monitor/metrics/instant?q="+query)
		event.Request = event.Request.WithContext(identity.NewContext(event.Request.Context(), &identity.Identity{UserID: "owner-1", OrgID: tenantID}))
		if err := monitor.instantMetrics(event); err != nil || recorder.Code != http.StatusOK {
			t.Fatalf("instant query for %s = status %d body=%s err=%v", tenantID, recorder.Code, recorder.Body.String(), err)
		}
		return recorder.Body.String()
	}
	if own := read("tenant-1"); !strings.Contains(own, "node_cpu_usage_percent") || !strings.Contains(own, "node_memory_usage_percent") {
		t.Fatalf("worker tenant cannot read its heartbeat metrics: %s", own)
	}
	if foreign := read("tenant-2"); strings.Contains(foreign, "runtime-1") {
		t.Fatalf("another tenant read the worker's heartbeat metrics: %s", foreign)
	}
}

// The Node dashboards draw 24-hour history with the selector the app sends
// (node_cpu_usage_percent{node_id=<server id>,core="total"}); a heartbeat must
// land in exactly that series, or every Node reads "No CPU data".
func TestWorkerHeartbeatCPUHistoryIsFoundByTheNodeDashboardQuery(t *testing.T) {
	t.Setenv("TECHSTACK_WORKER_AGENT_TOKEN_SECRET", "worker-secret")
	store := controlplane.NewMemoryStore()
	token := enrolledInventoryAgent(t, store)
	backend, err := monitoring.NewMonitorTSDB(monitoring.TSDBConfig{DataDir: t.TempDir()})
	if err != nil {
		t.Fatalf("open monitoring TSDB: %v", err)
	}
	t.Cleanup(func() { _ = backend.Close() })

	body := fmt.Sprintf(`{"source_epoch":"epoch-hb","source_sequence":1,"observed_at":%q,"cpu_percent":37}`,
		time.Now().UTC().Format(time.RFC3339Nano))
	handler := workerRouteHandlers{wst: store, registryStore: store, serverStore: store, rilStore: store, metricWriter: backend}
	event, recorder := workerRouteTestEvent(http.MethodPost, "/api/v1/workers/runtime-1/heartbeat", body)
	event.Request.SetPathValue("id", "runtime-1")
	event.Request.Header.Set("Authorization", "Bearer "+token)
	event.Request.Header.Set("X-Kombify-Tenant-ID", "tenant-1")
	if err := handler.heartbeat(event); err != nil || recorder.Code != http.StatusOK {
		t.Fatalf("heartbeat = status %d body=%s err=%v", recorder.Code, recorder.Body.String(), err)
	}
	var ack struct {
		Data struct {
			ServerID string `json:"server_id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &ack); err != nil || ack.Data.ServerID == "" {
		t.Fatalf("heartbeat ack has no server_id: %s err=%v", recorder.Body.String(), err)
	}

	tenantguard.Configure(true)
	t.Cleanup(func() { tenantguard.Configure(false) })
	monitor := monitoringRouteHandlers{backend: backend, policy: defaultMonitoringQueryPolicy()}
	now := time.Now().UTC()
	params := url.Values{}
	params.Set("q", fmt.Sprintf(`node_cpu_usage_percent{node_id=%q,core="total"}`, ack.Data.ServerID))
	params.Set("start", now.Add(-time.Minute).Format(time.RFC3339))
	params.Set("end", now.Add(2*time.Minute).Format(time.RFC3339))
	params.Set("step", "30s")
	query, queryRecorder := monitoringRouteTestEvent(http.MethodGet, "/api/v1/monitor/metrics/query?"+params.Encode())
	query.Request = query.Request.WithContext(identity.NewContext(query.Request.Context(), &identity.Identity{UserID: "owner-1", OrgID: "tenant-1"}))
	if err := monitor.rangeMetrics(query); err != nil || queryRecorder.Code != http.StatusOK {
		t.Fatalf("range query = status %d body=%s err=%v", queryRecorder.Code, queryRecorder.Body.String(), err)
	}
	var history struct {
		Data struct {
			Series []struct {
				Points []struct {
					Value float64 `json:"value"`
				} `json:"points"`
			} `json:"series"`
		} `json:"data"`
	}
	if err := json.Unmarshal(queryRecorder.Body.Bytes(), &history); err != nil {
		t.Fatalf("decode range query: %v", err)
	}
	if len(history.Data.Series) == 0 || len(history.Data.Series[0].Points) < 2 || history.Data.Series[0].Points[0].Value != 37 {
		t.Fatalf("dashboard CPU query found no heartbeat history: %s", queryRecorder.Body.String())
	}
}
