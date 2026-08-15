package routes

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/runtimehealth"
	"github.com/pocketbase/pocketbase/tests" // pocketbase-migration-compat: legacy projection characterization only
)

func TestStackOperationsPrefersCanonicalServiceRuntimeAccess(t *testing.T) {
	store := controlplane.NewMemoryStore()
	now := time.Now().UTC()
	if _, err := store.UpsertServerRuntime(t.Context(), controlplane.ServerRuntime{
		ID: "server-1", TenantID: "tenant-1", StackID: "stack-1", OwnerSubjectID: "owner-1",
		Name: "primary", LifecycleState: "active", ConnectionState: "connected", HealthState: serviceHealthHealthy, LastHeartbeatAt: &now,
	}); err != nil {
		t.Fatalf("UpsertServerRuntime: %v", err)
	}
	if _, err := store.UpsertServiceRuntime(t.Context(), controlplane.ServiceRuntime{
		ID: "service-1", TenantID: "tenant-1", StackID: "stack-1", ServerID: "server-1",
		ServiceKey: "vaultwarden", ServiceInstance: "default", Name: "Vaultwarden",
		DesiredState: registryStatusRunning, ObservedState: registryStatusRunning, HealthState: serviceHealthHealthy, ObservedAt: &now,
		Access:       map[string]any{serviceAccessModeKey: serviceAccessRelay, serviceAccessURLKey: "https://vault.owner.kombify.me", "route_id": "route-1"},
		Capabilities: []string{serviceActionRestart}, Source: stackKitsInventorySource,
	}); err != nil {
		t.Fatalf("UpsertServiceRuntime: %v", err)
	}
	h := stackOperationsRouteHandlers{serverStore: store, serviceStore: store, registryStore: store}
	services := h.operationServicesFromRuntimeRegistry(t.Context(), "tenant-1", "stack-1", []stackOperationServer{{ID: "server-1", Hostname: "primary"}})
	if len(services) != 1 {
		t.Fatalf("services = %#v, want one canonical runtime", services)
	}
	service := services[0]
	if service.Status != serviceHealthHealthy || service.URL != "https://vault.owner.kombify.me" || stringFromAnyMap(service.Access, serviceAccessModeKey) != serviceAccessRelay || len(service.AllowedActions) != 1 {
		t.Fatalf("unexpected canonical operations service: %#v", service)
	}
}

func TestStackOperationsServiceObservedStatusSemantics(t *testing.T) {
	now := time.Date(2026, time.July, 12, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name       string
		observedAt string
		status     string
		host       any
		docker     any
		want       string
	}{
		{
			name:       "fresh healthy observation",
			observedAt: now.Add(-time.Minute).Format(time.RFC3339Nano),
			status:     "healthy",
			want:       "healthy",
		},
		{
			name:       "fresh starting observation",
			observedAt: now.Add(-time.Minute).Format(time.RFC3339Nano),
			status:     "starting",
			want:       "starting",
		},
		{
			name:       "stale observation",
			observedAt: now.Add(-runtimehealth.FreshHeartbeatWindow - time.Second).Format(time.RFC3339Nano),
			status:     "healthy",
			want:       registryUnknownStatus,
		},
		{
			name:       "offline observation",
			observedAt: now.Add(-runtimehealth.StaleHeartbeatWindow - time.Second).Format(time.RFC3339Nano),
			status:     "healthy",
			want:       registryUnknownStatus,
		},
		{
			name:       "future observation outside skew allowance",
			observedAt: now.Add(time.Minute + time.Nanosecond).Format(time.RFC3339Nano),
			status:     "healthy",
			want:       registryUnknownStatus,
		},
		{
			name:       "invalid observation timestamp",
			observedAt: "not-a-timestamp",
			status:     "healthy",
			want:       registryUnknownStatus,
		},
		{
			name:       "unreachable host downgrades healthy service",
			observedAt: now.Add(-time.Minute).Format(time.RFC3339Nano),
			status:     "healthy",
			host:       false,
			want:       registryUnknownStatus,
		},
		{
			name:       "unreachable docker downgrades healthy service",
			observedAt: now.Add(-time.Minute).Format(time.RFC3339Nano),
			status:     "healthy",
			docker:     false,
			want:       registryUnknownStatus,
		},
		{
			name:       "unreachable host preserves measured unhealthy service",
			observedAt: now.Add(-time.Minute).Format(time.RFC3339Nano),
			status:     "unhealthy",
			host:       false,
			want:       "unhealthy",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			item := map[string]any{
				"runtime_observation": true,
				"observation_version": stackKitRuntimeObservationV1,
				"observed_at":         tt.observedAt,
				"status":              tt.status,
			}
			if tt.host != nil {
				item["observation_host_reachable"] = tt.host
			}
			if tt.docker != nil {
				item["observation_docker_reachable"] = tt.docker
			}

			if got := stackKitObservedServiceStatus(item, now); got != tt.want {
				t.Fatalf("stackKitObservedServiceStatus() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestStackOperationsServiceObservationRequiresVersionedEvidence(t *testing.T) {
	now := time.Date(2026, time.July, 12, 12, 0, 0, 0, time.UTC)
	fresh := now.Add(-time.Minute).Format(time.RFC3339Nano)

	tests := []struct {
		name string
		item map[string]any
	}{
		{name: "ordinary action output", item: map[string]any{"status": "healthy", "observed_at": fresh}},
		{name: "observation flag is false", item: map[string]any{"runtime_observation": false, "observation_version": stackKitRuntimeObservationV1, "observed_at": fresh, "status": "healthy"}},
		{name: "unknown observation version", item: map[string]any{"runtime_observation": true, "observation_version": "stackkit.runtime-observation/v3", "observed_at": fresh, "status": "healthy"}},
		{name: "unsupported service status", item: map[string]any{"runtime_observation": true, "observation_version": stackKitRuntimeObservationV1, "observed_at": fresh, "status": "running"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := stackKitObservedServiceStatus(tt.item, now); got != registryUnknownStatus {
				t.Fatalf("stackKitObservedServiceStatus() = %q, want %q", got, registryUnknownStatus)
			}
		})
	}
}

func TestStackOperationsAcceptsRuntimeObservationV2(t *testing.T) {
	now := time.Now().UTC()
	item := map[string]any{
		"runtime_observation": true,
		"observation_version": stackKitRuntimeObservationV2,
		"observed_at":         now.Format(time.RFC3339Nano),
		"status":              "healthy",
	}
	if got := stackKitObservedServiceStatus(item, now); got != "healthy" {
		t.Fatalf("runtime observation v2 status = %q, want healthy", got)
	}
}

func TestStackKitOutputAccessRequiresFreshVersionedObservation(t *testing.T) {
	now := time.Now().UTC()
	base := map[string]any{
		"name": "base", "url": "https://base.example.test", "status": "healthy",
		"runtime_observation": true, "observation_version": stackKitRuntimeObservationV1,
	}
	fresh := make(map[string]any, len(base)+1)
	stale := make(map[string]any, len(base)+1)
	for key, value := range base {
		fresh[key] = value
		stale[key] = value
	}
	fresh["observed_at"] = now.Format(time.RFC3339Nano)
	stale["observed_at"] = now.Add(-runtimehealth.FreshHeartbeatWindow - time.Second).Format(time.RFC3339Nano)
	if got := stackKitObservedServiceURL(fresh, now); got != "https://base.example.test" {
		t.Fatalf("fresh observed URL = %q", got)
	}
	if got := stackKitObservedServiceURL(stale, now); got != "" {
		t.Fatalf("stale observed URL stayed available: %q", got)
	}
	if got := stackKitObservedServiceURL(map[string]any{"name": "base", "url": "https://configured.example.test"}, now); got != "" {
		t.Fatalf("configured-only URL became access: %q", got)
	}
}

func TestStackOperationsServiceRegistryHeartbeatAndSourceHonesty(t *testing.T) {
	servers := []stackOperationServer{
		{ID: "node-healthy", AgentID: "agent-healthy", Hostname: "healthy", Health: stackServerHealth{State: "healthy"}},
		{ID: "node-degraded", AgentID: "agent-degraded", Hostname: "degraded", Health: stackServerHealth{State: "degraded"}},
		{ID: "node-stale", AgentID: "agent-stale", Hostname: "stale", Health: stackServerHealth{State: "stale"}},
	}

	for _, serverID := range []string{"node-healthy", "agent-healthy", "node-degraded", "agent-degraded"} {
		if !operationServerHasCurrentHeartbeat(servers, serverID) {
			t.Fatalf("operationServerHasCurrentHeartbeat(%q) = false, want true", serverID)
		}
	}
	for _, serverID := range []string{"node-stale", "agent-stale", "missing"} {
		if operationServerHasCurrentHeartbeat(servers, serverID) {
			t.Fatalf("operationServerHasCurrentHeartbeat(%q) = true, want false", serverID)
		}
	}

	node := controlplane.Node{ID: "node-healthy", WorkerID: "agent-healthy", Name: "healthy"}
	base := controlplane.Service{
		ID: "service-1", NodeID: node.ID, ServiceKey: "immich", Status: "healthy",
		URL: "https://immich.example.test", Metadata: map[string]any{"observed_at": time.Now().UTC().Format(time.RFC3339Nano)},
	}

	actionOutput := base
	actionOutput.Source = stackKitOutputKey
	if got := operationServiceFromControlPlane(actionOutput, node, servers); got.Status != registryUnknownStatus || got.URL != "" {
		t.Fatalf("action-output projection = %#v, want unknown without access", got)
	}

	inventory := base
	inventory.Source = stackKitsInventorySource
	got := operationServiceFromControlPlane(inventory, node, servers)
	if got.Status != "healthy" || got.URL != "https://immich.example.test" || got.TargetServerID != node.ID || got.TargetServer != node.Name {
		t.Fatalf("current inventory projection = %#v, want live status and resolved target", got)
	}

	staleNode := controlplane.Node{ID: "node-stale", Name: "stale"}
	inventory.NodeID = staleNode.ID
	if got := operationServiceFromControlPlane(inventory, staleNode, servers); got.Status != registryUnknownStatus || got.URL != "" {
		t.Fatalf("stale inventory projection = %#v, want unknown without access", got)
	}

	staleObservation := inventory
	staleObservation.NodeID = node.ID
	staleObservation.Metadata = map[string]any{"observed_at": time.Now().UTC().Add(-runtimehealth.FreshHeartbeatWindow - time.Second).Format(time.RFC3339Nano)}
	if got := operationServiceFromControlPlane(staleObservation, node, servers); got.Status != registryUnknownStatus || got.URL != "" {
		t.Fatalf("stale service observation projection = %#v, want unknown without access", got)
	}
}

func TestStackOperationsServiceDedupeNormalizesFirstWinsAndPreservesOrder(t *testing.T) {
	services := []stackOperationService{
		{ID: "first-pocket", Name: " Pocket-ID ", Status: "healthy"},
		{ID: "second-pocket", Name: "pocket_id", Status: "unhealthy"},
		{ID: "first-vault", Name: "VaultWarden", Status: "starting"},
		{ID: "second-vault", Name: " vaultwarden ", Status: "healthy"},
		{ID: "traefik", Name: "traefik", Status: "healthy"},
	}

	got := dedupeServices(services)
	if len(got) != 3 {
		t.Fatalf("dedupeServices() = %#v, want three normalized services", got)
	}
	wantIDs := []string{"first-pocket", "first-vault", "traefik"}
	for index, wantID := range wantIDs {
		if got[index].ID != wantID {
			t.Fatalf("dedupeServices()[%d].ID = %q, want %q; services=%#v", index, got[index].ID, wantID, got)
		}
	}
	if got[0].Status != "healthy" || got[1].Status != "starting" {
		t.Fatalf("dedupeServices() did not preserve the first duplicate values: %#v", got)
	}
}

func TestStackOperationsStoreServiceProjectionPrefersRegistryOverRuntimeSummary(t *testing.T) {
	ctx := context.Background()
	store := controlplane.NewMemoryStore()
	stack := &controlplane.Stack{
		ID:       "stack-store-precedence",
		TenantID: "tenant-1",
		Name:     "Store Precedence",
		Status:   "running",
		RuntimeSummary: map[string]any{
			"stackkit_outputs": map[string]any{
				"services": []any{
					map[string]any{"name": "pocket_id", "url": "https://runtime.example.test"},
					map[string]any{"name": "immich", "url": "https://runtime-immich.example.test"},
				},
			},
		},
	}
	if _, err := store.UpsertNode(ctx, controlplane.Node{
		ID: "node-store-precedence", TenantID: stack.TenantID, StackID: stack.ID, Name: "main",
	}); err != nil {
		t.Fatalf("UpsertNode: %v", err)
	}
	if _, err := store.UpsertService(ctx, controlplane.Service{
		ID:         "registry-pocket",
		TenantID:   stack.TenantID,
		StackID:    stack.ID,
		NodeID:     "node-store-precedence",
		ServiceKey: "Pocket-ID",
		Name:       "pocket_id",
		Status:     "running",
		Source:     "managed",
		URL:        "https://registry.example.test",
	}); err != nil {
		t.Fatalf("UpsertService: %v", err)
	}

	got := (stackOperationsRouteHandlers{registryStore: store}).operationServicesFromStore(ctx, stack.TenantID, stack, nil)
	if len(got) != 1 {
		t.Fatalf("operationServicesFromStore() = %#v, want registry source only", got)
	}
	if got[0].ID != "registry-pocket" || got[0].Name != "pocket_id" || got[0].URL != "https://registry.example.test" {
		t.Fatalf("operationServicesFromStore() did not preserve registry source/key precedence: %#v", got)
	}
}

func TestStackOperationsLegacyServiceProjectionPrefersRegistryOverPocketBase(t *testing.T) {
	app, err := tests.NewTestApp(driftRoutePocketBaseTestDataDir(t))
	if err != nil {
		t.Fatalf("new test app: %v", err)
	}
	defer app.Cleanup()
	ensureStackOperationsTestCollections(t, app)

	stack := createStackOperationsTestStack(t, app, "owner-1", "running")
	stack.Set("tenant_id", "tenant-1")
	if err := app.Save(stack); err != nil { // pocketbase-migration-compat: legacy projection characterization only
		t.Fatalf("save stack tenant: %v", err)
	}
	legacyNode := createStackOperationsTestNode(t, app, stack.Id, "main")
	legacyService := createStackOperationsTestService(t, app, legacyNode.Id, "pocket_id")
	legacyService.Set("url", "https://pocketbase.example.test")
	if err := app.Save(legacyService); err != nil { // pocketbase-migration-compat: legacy projection characterization only
		t.Fatalf("save PocketBase service: %v", err)
	}

	ctx := context.Background()
	store := controlplane.NewMemoryStore()
	if _, err := store.UpsertNode(ctx, controlplane.Node{
		ID: "node-registry-precedence", TenantID: "tenant-1", StackID: stack.Id, Name: "main",
	}); err != nil {
		t.Fatalf("UpsertNode: %v", err)
	}
	if _, err := store.UpsertService(ctx, controlplane.Service{
		ID:         "registry-pocket",
		TenantID:   "tenant-1",
		StackID:    stack.Id,
		NodeID:     "node-registry-precedence",
		ServiceKey: "Pocket-ID",
		Name:       "pocket_id",
		Status:     "running",
		Source:     "managed",
		URL:        "https://registry.example.test",
	}); err != nil {
		t.Fatalf("UpsertService: %v", err)
	}

	got := (stackOperationsRouteHandlers{app: app, registryStore: store}).operationServices(stack, nil, "")
	if len(got) != 1 {
		t.Fatalf("operationServices() = %#v, want registry source only", got)
	}
	if got[0].ID != "registry-pocket" || got[0].Name != "pocket_id" || got[0].URL != "https://registry.example.test" {
		t.Fatalf("operationServices() did not preserve registry source/key precedence: %#v", got)
	}
}

func TestStackOperationsServiceRegistryAdapterErrorAndFallbackSemantics(t *testing.T) {
	ctx := context.Background()
	service := controlplane.Service{
		ID: "registry-service", NodeID: "node-1", ServiceKey: "immich", Status: "healthy", Source: "managed",
	}

	nodeFailure := errors.New("nodes unavailable")
	handler := stackOperationsRouteHandlers{registryStore: stackOperationsRegistryStoreStub{
		nodesErr: nodeFailure,
		services: []controlplane.Service{service},
	}}
	projected, ok := handler.operationServicesFromRegistry(ctx, "tenant-1", "stack-1", nil)
	if !ok || len(projected) != 1 || projected[0].TargetServerID != "node-1" {
		t.Fatalf("node error must not hide otherwise valid services: ok=%v services=%#v", ok, projected)
	}

	serviceFailure := errors.New("services unavailable")
	handler = stackOperationsRouteHandlers{registryStore: stackOperationsRegistryStoreStub{servicesErr: serviceFailure}}
	if services, ok := handler.operationServicesFromRegistry(ctx, "tenant-1", "stack-1", nil); ok || services != nil {
		t.Fatalf("service error must request caller fallback: ok=%v services=%#v", ok, services)
	}

	handler = stackOperationsRouteHandlers{registryStore: stackOperationsRegistryStoreStub{}}
	if services, ok := handler.operationServicesFromRegistry(ctx, "tenant-1", "stack-1", nil); ok || services != nil {
		t.Fatalf("empty registry must request caller fallback: ok=%v services=%#v", ok, services)
	}

	stack := &controlplane.Stack{
		ID: "stack-1",
		RuntimeSummary: map[string]any{"stackkit_outputs": map[string]any{
			"services": []any{map[string]any{"name": "immich", "url": "https://runtime.example.test"}},
		}},
	}
	handler = stackOperationsRouteHandlers{registryStore: stackOperationsRegistryStoreStub{servicesErr: serviceFailure}}
	projected = handler.operationServicesFromStore(ctx, "tenant-1", stack, nil)
	if len(projected) != 1 || projected[0].Name != "immich" || projected[0].URL != "" || projected[0].Status != registryUnknownStatus {
		t.Fatalf("Store service error did not fall back to RuntimeSummary: %#v", projected)
	}
}

func TestStackOperationsServiceProjectionPreservesStoreAndLegacyOrdering(t *testing.T) {
	registry := stackOperationsRegistryStoreStub{
		nodes: []controlplane.Node{{ID: "node-1", Name: "main"}},
		services: []controlplane.Service{
			{ID: "zeta", NodeID: "node-1", ServiceKey: "zeta", Metadata: map[string]any{"display_name": "Zeta"}},
			{ID: "pocket-first", NodeID: "node-1", ServiceKey: "Pocket-ID", Metadata: map[string]any{"display_name": "Zulu"}},
			{ID: "alpha", NodeID: "node-1", ServiceKey: "alpha", Metadata: map[string]any{"display_name": "Alpha"}},
			{ID: "pocket-sorted-first", NodeID: "node-1", ServiceKey: "pocket_id", Metadata: map[string]any{"display_name": "Beta"}},
		},
	}
	handler := stackOperationsRouteHandlers{registryStore: registry}

	storeServices := handler.operationServicesFromStore(context.Background(), "tenant-1", &controlplane.Stack{ID: "stack-1"}, nil)
	assertStackOperationServiceIDs(t, storeServices, []string{"zeta", "pocket-first", "alpha"})

	app, err := tests.NewTestApp(driftRoutePocketBaseTestDataDir(t)) // pocketbase-migration-compat: legacy projection characterization only
	if err != nil {
		t.Fatalf("new test app: %v", err)
	}
	defer app.Cleanup()
	ensureStackOperationsTestCollections(t, app)
	stack := createStackOperationsTestStack(t, app, "owner-1", "running")

	legacyServices := (stackOperationsRouteHandlers{app: app, registryStore: registry}).operationServices(stack, nil, "tenant-1")
	assertStackOperationServiceIDs(t, legacyServices, []string{"alpha", "pocket-sorted-first", "zeta"})
}

func assertStackOperationServiceIDs(t *testing.T, services []stackOperationService, want []string) {
	t.Helper()
	if len(services) != len(want) {
		t.Fatalf("service count = %d, want %d: %#v", len(services), len(want), services)
	}
	for index, wantID := range want {
		if services[index].ID != wantID {
			t.Fatalf("services[%d].ID = %q, want %q: %#v", index, services[index].ID, wantID, services)
		}
	}
}

type stackOperationsRegistryStoreStub struct {
	controlplane.RegistryStore
	nodes       []controlplane.Node
	services    []controlplane.Service
	nodesErr    error
	servicesErr error
}

func (s stackOperationsRegistryStoreStub) ListNodesByStack(context.Context, string, string) ([]controlplane.Node, error) {
	return s.nodes, s.nodesErr
}

func (s stackOperationsRegistryStoreStub) ListServicesByStack(context.Context, string, string) ([]controlplane.Service, error) {
	return s.services, s.servicesErr
}
