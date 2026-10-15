package routes

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kombifyio/techstack/internal/routes/tenantguard"
	"github.com/kombifyio/techstack/pkg/grpcserver"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/identity"
	"github.com/kombifyio/techstack/pkg/logger"
	"github.com/kombifyio/techstack/pkg/monitoring"
)

func hostedCall(router *httpx.Router, method, target string, id *identity.Identity) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, target, nil)
	request = request.WithContext(identity.NewContext(context.Background(), id))
	recorder := httptest.NewRecorder()
	router.BuildMux().ServeHTTP(recorder, request)
	return recorder
}

// The connected-agent table is process-wide; a hosted customer must only ever
// reach the agents their own enrollment bound to their tenant.
func TestHostedAgentRoutesNeverReachAnotherTenantsAgent(t *testing.T) {
	tenantguard.Configure(true)
	t.Cleanup(func() { tenantguard.Configure(false) })
	server, err := grpcserver.New(grpcserver.Config{ListenAddr: ":0"}, logger.New("error", "text"))
	if err != nil {
		t.Fatalf("grpc server: %v", err)
	}
	for _, agent := range []*grpcserver.ConnectedAgent{
		{ID: "agent-a", Hostname: "tenant-a-host", Tenant: "tenant-1"},
		{ID: "agent-b", Hostname: "tenant-b-secret-host", Tenant: "tenant-2"},
		{ID: "agent-unbound", Hostname: "unbound-secret-host"},
	} {
		if err := server.RegisterAgent(agent); err != nil {
			t.Fatalf("register agent: %v", err)
		}
	}
	router := httpx.NewRouter()
	RegisterAgentRoutes(router, server)
	customer := &identity.Identity{UserID: "auth0|owner-1", OrgID: "tenant-1"}

	list := hostedCall(router, http.MethodGet, "/api/v1/agents", customer)
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), "tenant-a-host") ||
		strings.Contains(list.Body.String(), "secret-host") {
		t.Fatalf("agent list = %d %s, want only the caller's agent", list.Code, list.Body.String())
	}
	for _, foreign := range []struct{ method, target string }{
		{http.MethodGet, "/api/v1/agents/agent-b"},
		{http.MethodGet, "/api/v1/agents/agent-b/logs"},
		{http.MethodGet, "/api/v1/agents/agent-unbound"},
		{http.MethodDelete, "/api/v1/agents/agent-b"},
	} {
		if rec := hostedCall(router, foreign.method, foreign.target, customer); rec.Code != http.StatusNotFound {
			t.Fatalf("%s %s = %d %s, want 404", foreign.method, foreign.target, rec.Code, rec.Body.String())
		}
	}
	if _, stillThere := server.GetAgent("agent-b"); !stillThere {
		t.Fatal("a foreign tenant's DELETE deregistered another tenant's agent")
	}
	if rec := hostedCall(router, http.MethodGet, "/api/v1/agents", &identity.Identity{UserID: "auth0|owner-1"}); rec.Code != http.StatusForbidden {
		t.Fatalf("tenant-less agent list = %d, want 403", rec.Code)
	}
}

// The alert engine evaluates one fleet-wide rule set, so a firing state carries
// a value measured on some tenant's host.
func TestHostedAlertRoutesHideFleetStatesFromCustomers(t *testing.T) {
	tenantguard.Configure(true)
	t.Cleanup(func() { tenantguard.Configure(false) })
	tsdb, err := monitoring.NewMonitorTSDB(monitoring.TSDBConfig{DataDir: t.TempDir()})
	if err != nil {
		t.Fatalf("open monitoring TSDB: %v", err)
	}
	t.Cleanup(func() { _ = tsdb.Close() })
	if err := tsdb.Write([]monitoring.MetricSample{{
		Name: "cpu_usage", Value: 97, Timestamp: time.Now(),
		Labels: map[string]string{"tenant_id": "tenant-2", "hostname": "tenant-b-secret-host"},
	}}); err != nil {
		t.Fatalf("seed TSDB: %v", err)
	}
	engine := monitoring.NewAlertEngine(tsdb, nil, []monitoring.AlertRule{
		{Name: "FleetCPUHot", Expr: `cpu_usage > 90`, Severity: "critical", Message: "cpu hot"},
		{Name: "TenantOneRule", Expr: `cpu_usage > 90`, Severity: "warning", Message: "own rule", Labels: map[string]string{"tenant_id": "tenant-1"}},
	}, monitoring.AlertEngineConfig{Interval: 5 * time.Millisecond})
	ctx, stop := context.WithCancel(context.Background())
	t.Cleanup(stop)
	go engine.Run(ctx)
	deadline := time.Now().Add(10 * time.Second)
	for len(engine.ActiveAlerts()) < 2 {
		if time.Now().After(deadline) {
			t.Fatal("alert engine never fired the seeded rules")
		}
		time.Sleep(5 * time.Millisecond)
	}
	router := httpx.NewRouter()
	RegisterAlertRoutes(router, engine)
	customer := &identity.Identity{UserID: "auth0|owner-1", OrgID: "tenant-1"}
	staff := &identity.Identity{UserID: "auth0|staff-1", OrgID: "tenant-1", Roles: []string{"admin"}}

	for _, target := range []string{"/api/v1/monitor/alerts", "/api/v1/monitor/alerts/rules"} {
		own := hostedCall(router, http.MethodGet, target, customer).Body.String()
		if strings.Contains(own, "FleetCPUHot") || !strings.Contains(own, "TenantOneRule") {
			t.Fatalf("customer %s = %s, want only the rule labelled with their tenant", target, own)
		}
		if fleet := hostedCall(router, http.MethodGet, target, staff).Body.String(); !strings.Contains(fleet, "FleetCPUHot") {
			t.Fatalf("staff %s = %s, want the fleet view", target, fleet)
		}
		if rec := hostedCall(router, http.MethodGet, target, &identity.Identity{UserID: "auth0|owner-1"}); rec.Code != http.StatusForbidden {
			t.Fatalf("tenant-less %s = %d, want 403", target, rec.Code)
		}
	}
}

// The monitoring summary on the stack views reads the whole store's size.
func TestHostedMonitoringSummaryDoesNotDisclosePlatformSeriesCount(t *testing.T) {
	tsdb, err := monitoring.NewMonitorTSDB(monitoring.TSDBConfig{DataDir: t.TempDir()})
	if err != nil {
		t.Fatalf("open monitoring TSDB: %v", err)
	}
	t.Cleanup(func() { _ = tsdb.Close() })
	if err := tsdb.Write([]monitoring.MetricSample{{
		Name: "cpu_usage", Value: 1, Timestamp: time.Now(), Labels: map[string]string{"tenant_id": "tenant-2"},
	}}); err != nil {
		t.Fatalf("seed TSDB: %v", err)
	}
	handlers := stackOperationsRouteHandlers{backend: tsdb}

	if got := handlers.monitoringSummary(context.Background()).SeriesCount; got == 0 {
		t.Fatal("self-hosted summary lost its series count")
	}
	tenantguard.Configure(true)
	t.Cleanup(func() { tenantguard.Configure(false) })
	if got := handlers.monitoringSummary(context.Background()).SeriesCount; got != 0 {
		t.Fatalf("hosted summary series count = %d, want it withheld", got)
	}
}
