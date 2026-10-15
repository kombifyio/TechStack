package routes

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/kombifyio/techstack/internal/routes/tenantguard"
	"github.com/kombifyio/techstack/pkg/drivehealth"
	"github.com/kombifyio/techstack/pkg/guardtelemetry"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/identity"
	"github.com/kombifyio/techstack/pkg/monitoring"
)

func TestDriveHealthRoutesScopeHardwareAndAlertsToAuthenticatedTenant(t *testing.T) {
	tenantguard.Configure(true)
	t.Cleanup(func() { tenantguard.Configure(false) })
	now := time.Now().UTC()
	store := monitoring.NewMemoryDriveHealthStore()
	service := &monitoring.DriveHealthService{Store: store, Now: func() time.Time { return now }}
	for _, tenant := range []string{"tenant-a", "tenant-b"} {
		id := guardtelemetry.Identity{TenantID: tenant, ServerID: "shared-server", WorkerID: "guard"}
		if err := service.Ingest(t.Context(), id, "epoch", 1, drivehealth.Collection{ObservedAt: now, Status: drivehealth.Inaccessible, Reason: "permission_denied", Drives: []drivehealth.Reading{}}); err != nil {
			t.Fatal(err)
		}
	}
	router := httpx.NewRouter()
	RegisterDriveHealthRoutes(router, service)
	customer := &identity.Identity{UserID: "auth0|owner", OrgID: "tenant-a"}
	response := hostedCall(router, http.MethodGet, "/api/v1/monitor/drive-health?server_id=shared-server", customer)
	if response.Code != http.StatusOK {
		t.Fatalf("evidence read %d %s", response.Code, response.Body.String())
	}
	var parsed struct {
		Data monitoring.DriveHealthResponse `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.Data.Servers[0].CollectionStatus != "inaccessible" {
		t.Fatal("collection failure did not reach the scoped API")
	}
	alerts := hostedCall(router, http.MethodGet, "/api/v1/monitor/drive-health/alerts?server_id=shared-server", customer)
	if alerts.Code != http.StatusOK {
		t.Fatalf("alert read %d %s", alerts.Code, alerts.Body.String())
	}
	var events struct {
		Data struct {
			Alerts []monitoring.DriveAlert `json:"alerts"`
		} `json:"data"`
	}
	if err := json.Unmarshal(alerts.Body.Bytes(), &events); err != nil {
		t.Fatal(err)
	}
	owned, err := service.Read(t.Context(), "tenant-a", "shared-server")
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := service.Read(t.Context(), "tenant-b", "shared-server")
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events.Data.Alerts {
		if event.ID == foreign.Servers[0].Alerts[0].ID {
			t.Fatal("API returned another tenant's episode")
		}
	}
	if events.Data.Alerts[0].ID != owned.Servers[0].Alerts[0].ID {
		t.Fatal("API omitted the caller's own alert episode")
	}
	for _, path := range []string{"/api/v1/monitor/drive-health", "/api/v1/monitor/drive-health/alerts"} {
		if rec := hostedCall(router, http.MethodGet, path, nil); rec.Code != http.StatusUnauthorized {
			t.Fatalf("anonymous read %d", rec.Code)
		}
		if rec := hostedCall(router, http.MethodGet, path, &identity.Identity{UserID: "auth0|owner"}); rec.Code != http.StatusForbidden {
			t.Fatalf("tenant-less read %d", rec.Code)
		}
	}
	unavailable := httpx.NewRouter()
	RegisterDriveHealthRoutes(unavailable, nil)
	for _, path := range []string{"/api/v1/monitor/drive-health", "/api/v1/monitor/drive-health/alerts"} {
		if rec := hostedCall(unavailable, http.MethodGet, path, customer); rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("missing persistence returned %d instead of structured unavailable", rec.Code)
		}
		if rec := hostedCall(unavailable, http.MethodGet, path, nil); rec.Code != http.StatusUnauthorized {
			t.Fatal("unavailable persistence bypassed authentication")
		}
	}
	// Self-host registration uses its explicit installation scope.
	tenantguard.Configure(false)
	selfHosted := httpx.NewRouter()
	RegisterDriveHealthRoutes(selfHosted, service, "tenant-a")
	response = hostedCall(selfHosted, http.MethodGet, "/api/v1/monitor/drive-health?server_id=shared-server", &identity.Identity{UserID: "local-owner"})
	if response.Code != http.StatusOK {
		t.Fatal(response.Body.String())
	}
	if err := json.Unmarshal(response.Body.Bytes(), &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.Data.Servers[0].CollectionStatus != "inaccessible" {
		t.Fatal("self-host installation tenant was not resolved")
	}
}
