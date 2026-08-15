package routes

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/runtimehealth"
	"github.com/kombifyio/techstack/pkg/runtimeidentity"
	"github.com/kombifyio/techstack/pkg/serviceregistry"
)

func TestDeriveInventoryServiceAccessRequiresRegisteredRelayContract(t *testing.T) {
	credentialURL := "https://user:" + strings.Join([]string{"sec", "ret"}, "") + "@example.test"
	tests := []struct {
		name string
		svc  workerInventoryService
		mode string
	}{
		{
			name: "public endpoint is direct",
			svc:  workerInventoryService{Endpoints: []workerInventoryEndpoint{{URL: "https://vault.example.test", Visibility: "public"}}},
			mode: serviceAccessDirect,
		},
		{
			name: "registered StackKit tunnel is relay",
			svc:  workerInventoryService{Endpoints: []workerInventoryEndpoint{{URL: "https://vault.owner.kombify.me", Visibility: "private", TargetType: "tunnel", RouteID: "route-1", Provenance: serviceStackKit}}},
			mode: serviceAccessRelay,
		},
		{
			name: "Guard-probed kombify.me endpoint is observed direct access",
			svc: workerInventoryService{Endpoints: []workerInventoryEndpoint{{
				URL: "https://base.demo.kombify.me", Visibility: "public", TargetType: serviceAccessDirect,
				Provenance: serviceStackKitManifest, Health: serviceHealthHealthy,
			}}},
			mode: serviceAccessDirect,
		},
		{
			name: "Guard-probed home.localhost endpoint is local direct access",
			svc: workerInventoryService{Endpoints: []workerInventoryEndpoint{{
				URL: "http://base.home.localhost", Visibility: "local", TargetType: serviceAccessDirect,
				Provenance: serviceStackKitManifest, Health: "reachable",
			}}},
			mode: serviceAccessDirect,
		},
		{
			name: "unhealthy Guard endpoint is unavailable",
			svc: workerInventoryService{Endpoints: []workerInventoryEndpoint{{
				URL: "https://base.demo.kombify.me", Visibility: "public", TargetType: serviceAccessDirect,
				Provenance: serviceStackKitManifest, Health: serviceHealthUnhealthy,
			}}},
			mode: serviceAccessUnavailable,
		},
		{
			name: "arbitrary relay host is unavailable",
			svc:  workerInventoryService{Endpoints: []workerInventoryEndpoint{{URL: "https://attacker.example/relay", TargetType: "tunnel", RouteID: "route-1", Provenance: serviceStackKit}}},
			mode: serviceAccessUnavailable,
		},
		{
			name: "kombify relay URL without route registration is unavailable",
			svc:  workerInventoryService{URL: "https://unregistered.kombify.me"},
			mode: serviceAccessUnavailable,
		},
		{
			name: "private direct target is unavailable",
			svc:  workerInventoryService{URL: "http://169.254.169.254/latest/meta-data"},
			mode: serviceAccessUnavailable,
		},
		{
			name: "credential-bearing URL is unavailable",
			svc:  workerInventoryService{URL: credentialURL},
			mode: serviceAccessUnavailable,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			access := deriveInventoryServiceAccess(tt.svc)
			if got := stringFromAnyMap(access, serviceAccessModeKey); got != tt.mode {
				t.Fatalf("mode = %q, want %q: %#v", got, tt.mode, access)
			}
			if stringFromAnyMap(access, serviceAccessURLKey) == "http://169.254.169.254/latest/meta-data" {
				t.Fatal("metadata target leaked into access projection")
			}
		})
	}
}

func TestInventoryProjectsStableCanonicalServiceRuntime(t *testing.T) {
	store := controlplane.NewMemoryStore()
	h := workerRouteHandlers{registryStore: store}
	now := time.Date(2026, 7, 17, 11, 0, 0, 0, time.UTC)
	worker := controlplane.Worker{ID: "guard-1", TenantID: "tenant-1", StackID: "stack-1", OwnerSubjectID: "owner-1"}
	event, _ := registryRouteStoreTestEvent(http.MethodPost, "/api/v1/workers/guard-1/inventory", "owner-1", "tenant-1", nil)
	req := workerInventoryRequest{Hostname: "node-1", Services: []workerInventoryService{{
		ID: "container-old", ServiceID: "vaultwarden", Name: "Vaultwarden", Instance: "default",
		Status: serviceHealthHealthy, StackKit: "basement-kit@1.2.3", Actions: []string{serviceActionRestart, "shell", "stop", serviceActionRestart},
		Endpoints: []workerInventoryEndpoint{{URL: "https://vault.owner.kombify.me", TargetType: "tunnel", RouteID: "route-1", Provenance: serviceStackKit}},
	}}}
	req.Services[0].normalize()
	if err := h.upsertInventoryRegistry(event, worker, "server-1", req, 0, now); err != nil {
		t.Fatalf("first inventory: %v", err)
	}
	req.Services[0].ID = "container-recreated"
	if err := h.upsertInventoryRegistry(event, worker, "server-1", req, 0, now.Add(time.Minute)); err != nil {
		t.Fatalf("second inventory: %v", err)
	}
	rows, err := store.ListServiceRuntimes(t.Context(), "tenant-1", "stack-1", "server-1")
	if err != nil || len(rows) != 1 {
		t.Fatalf("service runtimes = %#v err=%v, want one stable row", rows, err)
	}
	service := rows[0]
	if service.ID != runtimeidentity.ServiceID("stack-1", "server-1", "vaultwarden", "default") || service.StackKitVersion != "basement-kit@1.2.3" {
		t.Fatalf("unexpected runtime identity/version: %#v", service)
	}
	if len(service.Capabilities) != 2 || service.Capabilities[0] != serviceActionRestart || service.Capabilities[1] != "stop" {
		t.Fatalf("unsafe actions survived: %#v", service.Capabilities)
	}
	if stringFromAnyMap(service.Metadata, "reported_service_id") != "container-recreated" {
		t.Fatalf("reported identity was not retained as evidence: %#v", service.Metadata)
	}
}

func TestInventoryProjectsAuthGatewayAsReachableLinkWithoutClaimingHealthy(t *testing.T) {
	store := controlplane.NewMemoryStore()
	h := workerRouteHandlers{registryStore: store}
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	worker := controlplane.Worker{ID: "guard-1", TenantID: "tenant-1", StackID: "stack-1", OwnerSubjectID: "owner-1"}
	event, _ := registryRouteStoreTestEvent(http.MethodPost, "/api/v1/workers/guard-1/inventory", "owner-1", "tenant-1", nil)
	service := workerInventoryService{
		ServiceID: "auth", Name: "TinyAuth", Status: "reachable",
		Health: map[string]any{"source": "http-probe", "status": "reachable", "auth_or_redirect_required": true},
		Endpoints: []workerInventoryEndpoint{{
			URL: "http://auth.home.localhost", Visibility: "local", TargetType: serviceAccessDirect,
			Provenance: serviceStackKitManifest, Health: "reachable",
		}},
	}
	service.normalize()
	if err := h.upsertInventoryRegistry(event, worker, "server-1", workerInventoryRequest{ManifestObserved: true, Services: []workerInventoryService{service}}, 0, now); err != nil {
		t.Fatal(err)
	}
	serviceID := runtimeidentity.ServiceID("stack-1", "server-1", "auth", "default")
	runtime, err := store.GetServiceRuntime(t.Context(), "tenant-1", serviceID)
	if err != nil {
		t.Fatal(err)
	}
	// `reachable` proves the gateway answers, not that a health probe passed.
	// The canonical StackKits health vocabulary expresses that as `starting`,
	// which is exactly what runtimehealth already returns for a live but
	// unproven service. It must never be projected as healthy.
	if runtime.HealthState != string(serviceregistry.HealthStarting) {
		t.Fatalf("auth gateway health = %q, want starting", runtime.HealthState)
	}
	if runtime.ObservedState != string(serviceregistry.ObservedRunning) {
		t.Fatalf("auth gateway observed state = %q, want running", runtime.ObservedState)
	}
	if stringFromAnyMap(runtime.Access, serviceAccessModeKey) != serviceAccessDirect || stringFromAnyMap(runtime.Access, serviceAccessURLKey) != "http://auth.home.localhost" {
		t.Fatalf("auth gateway access = %#v, want observed direct link", runtime.Access)
	}
}

func TestInventorySnapshotRemovesServiceMissingFromNextObservation(t *testing.T) {
	store := controlplane.NewMemoryStore()
	h := workerRouteHandlers{registryStore: store}
	worker := controlplane.Worker{ID: "guard-1", TenantID: "tenant-1", StackID: "stack-1", OwnerSubjectID: "owner-1"}
	event, _ := registryRouteStoreTestEvent(http.MethodPost, "/api/v1/workers/guard-1/inventory", "owner-1", "tenant-1", nil)
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	service := workerInventoryService{
		ServiceID: "base", Name: "Base", Status: serviceHealthHealthy,
		Health: map[string]any{"healthy": true, "status": serviceHealthHealthy},
		Endpoints: []workerInventoryEndpoint{{
			URL: "https://base.example.test", Visibility: "public", Health: serviceHealthHealthy,
		}},
	}
	service.normalize()
	if err := h.upsertInventoryRegistry(event, worker, "server-1", workerInventoryRequest{ManifestObserved: true, Services: []workerInventoryService{service}}, 0, now); err != nil {
		t.Fatal(err)
	}
	serviceID := runtimeidentity.ServiceID("stack-1", "server-1", "base", "default")
	if _, err := store.GetServiceRuntime(t.Context(), "tenant-1", serviceID); err != nil {
		t.Fatalf("initial service missing: %v", err)
	}

	// A next observation that still carries service evidence prunes what it no
	// longer sees.
	other := workerInventoryService{
		ServiceID: "other", Name: "Other", Status: serviceHealthHealthy,
		Health: map[string]any{"healthy": true, "status": serviceHealthHealthy},
		Endpoints: []workerInventoryEndpoint{{
			URL: "https://other.example.test", Visibility: "public", Health: serviceHealthHealthy,
		}},
	}
	other.normalize()
	if err := h.upsertInventoryRegistry(event, worker, "server-1", workerInventoryRequest{ManifestObserved: true, Services: []workerInventoryService{other}}, 0, now.Add(30*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetServiceRuntime(t.Context(), "tenant-1", serviceID); !errors.Is(err, controlplane.ErrNotFound) {
		t.Fatalf("service absent from fresh snapshot still persisted: %v", err)
	}

	// Absence of ALL service evidence is not evidence of absence: an empty
	// manifest (e.g. written by a failed or partial apply) keeps the last
	// observed service instead of zeroing the projection.
	if err := h.upsertInventoryRegistry(event, worker, "server-1", workerInventoryRequest{ManifestObserved: true, Services: []workerInventoryService{}}, 0, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	otherID := runtimeidentity.ServiceID("stack-1", "server-1", "other", "default")
	if _, err := store.GetServiceRuntime(t.Context(), "tenant-1", otherID); err != nil {
		t.Fatalf("empty snapshot removed the retained service: %v", err)
	}
	rows, err := store.ListServicesByStack(t.Context(), "tenant-1", "stack-1")
	if err != nil || len(rows) != 1 {
		t.Fatalf("legacy service projection after empty snapshot: rows=%#v err=%v, want one retained row", rows, err)
	}
}

func TestMissingManifestSnapshotKeepsServiceUntilObservationExpires(t *testing.T) {
	store := controlplane.NewMemoryStore()
	h := workerRouteHandlers{registryStore: store}
	worker := controlplane.Worker{ID: "guard-1", TenantID: "tenant-1", StackID: "stack-1", OwnerSubjectID: "owner-1"}
	event, _ := registryRouteStoreTestEvent(http.MethodPost, "/api/v1/workers/guard-1/inventory", "owner-1", "tenant-1", nil)
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	service := workerInventoryService{
		ServiceID: "base", Name: "Base", Status: serviceHealthHealthy,
		Health: map[string]any{"healthy": true, "status": serviceHealthHealthy},
		Endpoints: []workerInventoryEndpoint{{
			URL: "https://base.example.test", Visibility: "public", Health: serviceHealthHealthy,
		}},
	}
	service.normalize()
	if err := h.upsertInventoryRegistry(event, worker, "server-1", workerInventoryRequest{ManifestObserved: true, Services: []workerInventoryService{service}}, 0, now); err != nil {
		t.Fatal(err)
	}
	serviceID := runtimeidentity.ServiceID("stack-1", "server-1", "base", "default")

	// A missing manifest is absence of evidence, not an authoritative empty
	// StackKit snapshot. Keep the row so freshness can turn it unknown.
	if err := h.upsertInventoryRegistry(event, worker, "server-1", workerInventoryRequest{ManifestObserved: false, Services: []workerInventoryService{}}, 0, now.Add(30*time.Second)); err != nil {
		t.Fatal(err)
	}
	legacy, err := store.GetService(t.Context(), "tenant-1", serviceID)
	if err != nil {
		t.Fatalf("service disappeared after non-authoritative snapshot: %v", err)
	}
	if _, err := store.GetServiceRuntime(t.Context(), "tenant-1", serviceID); err != nil {
		t.Fatalf("service runtime disappeared after non-authoritative snapshot: %v", err)
	}

	checkAt := now.Add(runtimehealth.FreshHeartbeatWindow + time.Second)
	heartbeatAt := checkAt
	record := serviceRegistryRecordFromStoreWithHealth(
		*legacy,
		controlplane.Stack{ID: "stack-1", TenantID: "tenant-1", Name: "Stack"},
		controlplane.Node{ID: "server-1", Status: string(runtimehealth.ServerHealthy)},
		controlplane.Worker{ID: worker.ID, LastSeenAt: &heartbeatAt},
		checkAt,
	)
	if record.Status != string(runtimehealth.ServiceUnknown) || record.URL != "" {
		t.Fatalf("expired retained service = %#v, want unknown without access", record)
	}
}

func TestInventorySnapshotsPreserveActiveMigrationStatesAndDisableAccess(t *testing.T) {
	store := controlplane.NewMemoryStore()
	stack, stackErr := store.CreateStack(t.Context(), controlplane.CreateStackRequest{
		ID: "stack-1", TenantID: "tenant-1", OwnerSubjectID: "owner-1", Name: "Stack", Status: registryStatusRunning,
	})
	if stackErr != nil {
		t.Fatal(stackErr)
	}
	workerHandler := workerRouteHandlers{registryStore: store}
	sourceWorker := controlplane.Worker{ID: "guard-source", TenantID: "tenant-1", StackID: stack.ID, OwnerSubjectID: "owner-1"}
	targetWorker := controlplane.Worker{ID: "guard-target", TenantID: "tenant-1", StackID: stack.ID, OwnerSubjectID: "owner-1"}
	sourceEvent, _ := registryRouteStoreTestEvent(http.MethodPost, "/api/v1/workers/guard-source/inventory", "owner-1", "tenant-1", nil)
	targetEvent, _ := registryRouteStoreTestEvent(http.MethodPost, "/api/v1/workers/guard-target/inventory", "owner-1", "tenant-1", nil)
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	service := workerInventoryService{
		ServiceID: "base", Name: "Base", Status: serviceHealthHealthy,
		Health: map[string]any{"healthy": true, "status": serviceHealthHealthy},
		Endpoints: []workerInventoryEndpoint{{
			URL: "https://base.example.test", Visibility: "public", Health: serviceHealthHealthy,
		}},
	}
	service.normalize()
	if inventoryErr := workerHandler.upsertInventoryRegistry(sourceEvent, sourceWorker, "server-source", workerInventoryRequest{ManifestObserved: true, Services: []workerInventoryService{service}}, 0, now); inventoryErr != nil {
		t.Fatal(inventoryErr)
	}
	// Register the target node without claiming that a StackKit manifest was
	// observed there yet.
	if inventoryErr := workerHandler.upsertInventoryRegistry(targetEvent, targetWorker, "server-target", workerInventoryRequest{ManifestObserved: false, Services: []workerInventoryService{}}, 0, now); inventoryErr != nil {
		t.Fatal(inventoryErr)
	}

	sourceID := runtimeidentity.ServiceID(stack.ID, "server-source", "base", "default")
	targetID := runtimeidentity.ServiceID(stack.ID, "server-target", "base", "default")
	// Seed the durable states a future runtime executor owns. The public
	// migration route is deliberately unavailable until that executor exists;
	// inventory still must preserve in-flight states already in persistence.
	source, sourceErr := store.GetService(t.Context(), "tenant-1", sourceID)
	if sourceErr != nil {
		t.Fatal(sourceErr)
	}
	source.Status = registryStatusMigrating
	source.MigrationStatus = registryStatusMigrating
	source.URL = ""
	if _, seedSourceErr := store.UpsertService(t.Context(), *source); seedSourceErr != nil {
		t.Fatal(seedSourceErr)
	}
	if _, seedTargetErr := store.UpsertService(t.Context(), controlplane.Service{
		ID: targetID, TenantID: "tenant-1", StackID: stack.ID, NodeID: "server-target",
		ServiceKey: "base", Name: "Base", Status: registryStatusPendingVerification,
		MigrationStatus: registryStatusPendingVerification, Source: stackKitsInventorySource,
		Metadata: map[string]any{"migrated_from_service_id": sourceID},
	}); seedTargetErr != nil {
		t.Fatal(seedTargetErr)
	}

	// An authoritative target snapshot that does not contain the service yet
	// must not delete the control-plane pending-verification handoff.
	if inventoryErr := workerHandler.upsertInventoryRegistry(targetEvent, targetWorker, "server-target", workerInventoryRequest{ManifestObserved: true, Services: []workerInventoryService{}}, 0, now.Add(10*time.Second)); inventoryErr != nil {
		t.Fatal(inventoryErr)
	}
	if target, targetErr := store.GetService(t.Context(), "tenant-1", targetID); targetErr != nil || target.Status != registryStatusPendingVerification {
		t.Fatalf("pending target after empty snapshot = %#v err=%v", target, targetErr)
	}

	// Fresh source and target probes record measured health, but may not replace
	// the workflow state or expose either service while migration is active.
	if inventoryErr := workerHandler.upsertInventoryRegistry(sourceEvent, sourceWorker, "server-source", workerInventoryRequest{ManifestObserved: true, Services: []workerInventoryService{service}}, 11, now.Add(20*time.Second)); inventoryErr != nil {
		t.Fatal(inventoryErr)
	}
	if inventoryErr := workerHandler.upsertInventoryRegistry(targetEvent, targetWorker, "server-target", workerInventoryRequest{ManifestObserved: true, Services: []workerInventoryService{service}}, 12, now.Add(20*time.Second)); inventoryErr != nil {
		t.Fatal(inventoryErr)
	}

	for _, expected := range []struct {
		id       string
		status   string
		revision int64
	}{
		{id: sourceID, status: registryStatusMigrating, revision: 11},
		{id: targetID, status: registryStatusPendingVerification, revision: 12},
	} {
		legacy, legacyErr := store.GetService(t.Context(), "tenant-1", expected.id)
		if legacyErr != nil {
			t.Fatalf("GetService(%s): %v", expected.id, legacyErr)
		}
		if legacy.Status != expected.status || legacy.MigrationStatus != expected.status || legacy.URL != "" {
			t.Fatalf("migration projection %s = %#v", expected.id, legacy)
		}
		if revision, ok := legacy.Metadata["inventory_revision"].(int64); !ok || revision != expected.revision {
			t.Fatalf("legacy migration revision %s = %#v, want %d", expected.id, legacy.Metadata["inventory_revision"], expected.revision)
		}
		runtime, runtimeErr := store.GetServiceRuntime(t.Context(), "tenant-1", expected.id)
		if runtimeErr != nil {
			t.Fatalf("GetServiceRuntime(%s): %v", expected.id, runtimeErr)
		}
		if mode := stringFromAnyMap(runtime.Access, serviceAccessModeKey); mode != serviceAccessUnavailable {
			t.Fatalf("runtime access %s = %#v, want unavailable", expected.id, runtime.Access)
		}
		if revision, ok := runtime.Metadata["inventory_revision"].(int64); !ok || revision != expected.revision {
			t.Fatalf("runtime migration revision %s = %#v, want %d", expected.id, runtime.Metadata["inventory_revision"], expected.revision)
		}
	}
}

func TestRegistryHidesStaleInventoryServiceHealthAndLink(t *testing.T) {
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	observedAt := now.Add(-runtimehealth.FreshHeartbeatWindow - time.Second)
	service := controlplane.Service{
		ID: "service-1", TenantID: "tenant-1", StackID: "stack-1", NodeID: "server-1",
		ServiceKey: "base", Name: "Base", Status: serviceHealthHealthy, Source: stackKitsInventorySource,
		URL: "https://base.example.test",
		Metadata: map[string]any{
			"observed_at": observedAt.Format(time.RFC3339Nano), "reported_status": serviceHealthHealthy,
			"health": map[string]any{"healthy": true},
		},
	}
	workerHeartbeat := now
	record := serviceRegistryRecordFromStoreWithHealth(
		service,
		controlplane.Stack{ID: "stack-1", TenantID: "tenant-1", Name: "Stack"},
		controlplane.Node{ID: "server-1", Status: string(runtimehealth.ServerHealthy)},
		controlplane.Worker{ID: "guard-1", LastSeenAt: &workerHeartbeat},
		now,
	)
	if record.HealthState != string(runtimehealth.ServiceUnknown) || record.Status != string(runtimehealth.ServiceUnknown) {
		t.Fatalf("stale service stayed green: %#v", record)
	}
	if record.URL != "" {
		t.Fatalf("stale service link remained accessible: %q", record.URL)
	}
}

func TestInventoryDoesNotPersistCredentialBearingServiceURL(t *testing.T) {
	store := controlplane.NewMemoryStore()
	h := workerRouteHandlers{registryStore: store}
	now := time.Date(2026, 7, 17, 11, 0, 0, 0, time.UTC)
	worker := controlplane.Worker{ID: "guard-1", TenantID: "tenant-1", StackID: "stack-1", OwnerSubjectID: "owner-1"}
	credentialURL := "https://admin:" + strings.Join([]string{"super", "-secret"}, "") + "@example.test"
	service := workerInventoryService{ServiceID: "admin", Name: "Admin", Status: serviceHealthHealthy, URL: credentialURL}
	service.normalize()
	event, _ := registryRouteStoreTestEvent(http.MethodPost, "/api/v1/workers/guard-1/inventory", "owner-1", "tenant-1", nil)
	if err := h.upsertInventoryRegistry(event, worker, "server-1", workerInventoryRequest{Services: []workerInventoryService{service}}, 0, now); err != nil {
		t.Fatalf("inventory: %v", err)
	}
	serviceID := runtimeidentity.ServiceID("stack-1", "server-1", "admin", "default")
	legacy, err := store.GetService(t.Context(), "tenant-1", serviceID)
	if err != nil {
		t.Fatalf("GetService: %v", err)
	}
	if legacy.URL != "" {
		t.Fatalf("credential URL persisted in registry: %q", legacy.URL)
	}
	inventoryJSON, _ := json.Marshal(inventoryServices([]workerInventoryService{service}))
	if strings.Contains(string(inventoryJSON), "super-secret") {
		t.Fatalf("credential URL persisted in inventory: %s", inventoryJSON)
	}
	runtime, err := store.GetServiceRuntime(t.Context(), "tenant-1", serviceID)
	if err != nil || stringFromAnyMap(runtime.Access, serviceAccessModeKey) != serviceAccessUnavailable {
		t.Fatalf("runtime access = %#v err=%v", runtime, err)
	}
}
