package routes

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kombifyio/techstack/internal/runtimeproduct/serverruntime"
	"github.com/kombifyio/techstack/internal/runtimeproduct/vmlease"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/identity"
	"github.com/kombifyio/techstack/pkg/monitoring"
	"github.com/kombifyio/techstack/pkg/nodehandoff"
	"github.com/kombifyio/techstack/pkg/runtimehealth"
	"github.com/kombifyio/techstack/pkg/runtimeidentity"
	"github.com/kombifyio/techstack/pkg/serverregistry"
	"github.com/kombifyio/techstack/pkg/vmleases"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tests"
)

func TestStackKitOutputServicesRetainMetadataWithoutExposingConfiguredLinks(t *testing.T) {
	outputs := map[string]any{
		"service_links": map[string]any{
			"login_gateway": map[string]any{
				"url":          "https://login.example.test",
				"display_name": "First Login",
				"type":         "identity",
				"status":       "running",
			},
			"vaultwarden": "https://vault.example.test",
		},
	}
	operationServices := operationServicesFromStackKitOutputs(outputs, []stackOperationServer{{
		ID:       "node-1",
		Hostname: "main",
	}})
	if len(operationServices) != 2 {
		t.Fatalf("operation services = %#v, want two", operationServices)
	}
	operationByName := map[string]stackOperationService{}
	for _, service := range operationServices {
		operationByName[service.Name] = service
	}
	if operationByName["login_gateway"].URL != "" || operationByName["login_gateway"].Status != registryUnknownStatus || operationByName["login_gateway"].DisplayName != "First Login" {
		t.Fatalf("unexpected login gateway operation service: %#v", operationByName["login_gateway"])
	}
	if operationByName["vaultwarden"].URL != "" || operationByName["vaultwarden"].Status != registryUnknownStatus {
		t.Fatalf("unexpected vaultwarden operation service: %#v", operationByName["vaultwarden"])
	}

	registryServices := registryServicesFromStackKitOutputs(outputs, controlplane.Stack{
		ID:   "stack-1",
		Name: "Managed Stack",
	}, []registryServer{{
		ID:   "node-1",
		Name: "main",
	}})
	if len(registryServices) != 2 {
		t.Fatalf("registry services = %#v, want two", registryServices)
	}
	registryByName := map[string]registryService{}
	for _, service := range registryServices {
		registryByName[service.Name] = service
	}
	if registryByName["login_gateway"].URL != "" || registryByName["login_gateway"].Status != registryUnknownStatus || registryByName["login_gateway"].ApplicationName != "First Login" {
		t.Fatalf("unexpected login gateway registry service: %#v", registryByName["login_gateway"])
	}
	if registryByName["vaultwarden"].URL != "" || registryByName["vaultwarden"].Status != registryUnknownStatus {
		t.Fatalf("unexpected vaultwarden registry service: %#v", registryByName["vaultwarden"])
	}
}

func TestStackKitRuntimeObservationProjectsFreshMeasuredServiceHealth(t *testing.T) {
	now := time.Now().UTC()
	outputs := map[string]any{
		"observation": map[string]any{
			"version":     stackKitRuntimeObservationV1,
			"observed_at": now.Format(time.RFC3339Nano),
			"host":        map[string]any{"reachable": true, "docker_reachable": true},
			"platform":    map[string]any{"server_id": "node-1"},
			"services": []any{map[string]any{
				"name": "vaultwarden", "status": "healthy", "platform_app_id": "app-vault", "health_path": "/health",
				"probe": map[string]any{"url": "https://vault.example.test/health", "reached": true, "status_code": 200},
			}},
		},
	}
	operations := operationServicesFromStackKitOutputs(outputs, []stackOperationServer{{ID: "node-1", Hostname: "main"}})
	if len(operations) != 1 || operations[0].Status != "healthy" || operations[0].URL != "https://vault.example.test/health" {
		t.Fatalf("fresh observation must project measured service health: %#v", operations)
	}
	registry := registryServicesFromStackKitOutputs(outputs, controlplane.Stack{ID: "stack-1", Name: "Managed Stack"}, []registryServer{{ID: "node-1", Name: "main"}})
	if len(registry) != 1 || registry[0].Status != "healthy" || registry[0].HealthState != "healthy" || registry[0].ObservedAt == "" {
		t.Fatalf("registry must retain fresh observation health: %#v", registry)
	}

	observation := outputs["observation"].(map[string]any)
	observation["observed_at"] = now.Add(-6 * time.Minute).Format(time.RFC3339Nano)
	stale := registryServicesFromStackKitOutputs(outputs, controlplane.Stack{ID: "stack-1"}, []registryServer{{ID: "node-1"}})
	if len(stale) != 1 || stale[0].Status != "unknown" {
		t.Fatalf("stale observation must not remain healthy: %#v", stale)
	}
}

func TestStackOperationsFiltersOwnerAndStackWorkers(t *testing.T) {
	app, err := tests.NewTestApp(driftRoutePocketBaseTestDataDir(t))
	if err != nil {
		t.Fatalf("new test app: %v", err)
	}
	defer app.Cleanup()
	ensureStackOperationsTestCollections(t, app)

	stack := createStackOperationsTestStack(t, app, "owner-1", "pending")
	otherStack := createStackOperationsTestStack(t, app, "owner-1", "pending")
	createStackOperationsTestWorker(t, app, "owner-1", stack.Id, "stack-node", true)
	createStackOperationsTestWorker(t, app, "owner-1", "", "legacy-node", true)
	createStackOperationsTestWorker(t, app, "owner-1", otherStack.Id, "other-stack-node", true)
	createStackOperationsTestWorker(t, app, "owner-2", stack.Id, "other-owner-node", true)

	event, recorder := stackOperationsRouteTestEvent(http.MethodGet, "/api/v1/stacks/"+stack.Id+"/operations", stack.Id, "owner-1")
	if err := (stackOperationsRouteHandlers{app: app, managedRuntimeLeases: fakeManagedRuntimeLeaseLister{}}).operations(event); err != nil {
		t.Fatalf("operations returned router error: %v", err)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s, want 200", recorder.Code, recorder.Body.String())
	}

	var envelope struct {
		Data stackOperationsPayload `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got := len(envelope.Data.Servers); got != 2 {
		t.Fatalf("server count = %d, want scoped + legacy unassigned workers only", got)
	}
	names := map[string]bool{}
	for _, server := range envelope.Data.Servers {
		names[server.Hostname] = true
		if server.Health.CPUPercent.Status != "unknown" {
			t.Fatalf("missing metrics should degrade to unknown CPU, got %q", server.Health.CPUPercent.Status)
		}
	}
	if !names["stack-node"] || !names["legacy-node"] {
		t.Fatalf("expected stack-node and legacy-node, got %v", names)
	}
	if names["other-stack-node"] || names["other-owner-node"] {
		t.Fatalf("operation payload leaked another stack or owner: %v", names)
	}
	if envelope.Data.Readiness.Approved != 1 {
		t.Fatalf("readiness approved = %d, want only stack-assigned approved worker", envelope.Data.Readiness.Approved)
	}
	if envelope.Data.Readiness.Available != 1 || envelope.Data.Readiness.Unassigned != 1 {
		t.Fatalf("unassigned counts = available:%d unassigned:%d, want 1/1", envelope.Data.Readiness.Available, envelope.Data.Readiness.Unassigned)
	}
	if envelope.Data.Readiness.Connected != 0 || envelope.Data.Readiness.CanStart {
		t.Fatalf("legacy worker approval must not substitute for canonical Guard connection evidence: %#v", envelope.Data.Readiness)
	}
}

func TestStackOperationsFallsBackToLegacyStackWhenStoreProjectionMissing(t *testing.T) {
	app, err := tests.NewTestApp(driftRoutePocketBaseTestDataDir(t))
	if err != nil {
		t.Fatalf("new test app: %v", err)
	}
	defer app.Cleanup()
	ensureStackOperationsTestCollections(t, app)

	stack := createStackOperationsTestStack(t, app, "owner-1", "running")
	stack.Set("tenant_id", "default")
	if err := app.Save(stack); err != nil {
		t.Fatalf("save legacy stack tenant: %v", err)
	}
	event, recorder := stackOperationsRouteTestEvent(http.MethodGet, "/api/v1/stacks/"+stack.Id+"/operations", stack.Id, "owner-1")
	event.Request = event.Request.WithContext(identity.NewContext(event.Request.Context(), &identity.Identity{
		UserID: "owner-1",
		OrgID:  "default",
	}))

	if err := (stackOperationsRouteHandlers{app: app, stackStore: controlplane.NewMemoryStore(), managedRuntimeLeases: fakeManagedRuntimeLeaseLister{}}).operations(event); err != nil {
		t.Fatalf("operations returned router error: %v", err)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s, want 200", recorder.Code, recorder.Body.String())
	}
	var envelope struct {
		Data stackOperationsPayload `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if envelope.Data.Stack["id"] != stack.Id {
		t.Fatalf("stack id = %v, want %s", envelope.Data.Stack["id"], stack.Id)
	}
}

func TestStackOperationsUsesDurableStoreForAuthenticatedFallbackTenant(t *testing.T) {
	app, err := tests.NewTestApp(driftRoutePocketBaseTestDataDir(t))
	if err != nil {
		t.Fatalf("new test app: %v", err)
	}
	defer app.Cleanup()
	ensureStackOperationsTestCollections(t, app)

	// Keep a legacy row with the same identity present: before the durable
	// projection selection, this request returned it successfully but omitted
	// the active durable job. An authenticated user without an explicit org is
	// scoped to its fallback tenant (the owner subject) by requestTenantID.
	legacy := createStackOperationsTestStack(t, app, "owner-1", "running")
	legacy.Set("tenant_id", "owner-1")
	if err := app.Save(legacy); err != nil {
		t.Fatalf("save legacy stack: %v", err)
	}

	store := controlplane.NewMemoryStore()
	if _, err := store.CreateStack(t.Context(), controlplane.CreateStackRequest{
		ID: legacy.Id, TenantID: "owner-1", OwnerSubjectID: "owner-1", Name: "Durable stack", Status: "provisioning",
	}); err != nil {
		t.Fatalf("CreateStack: %v", err)
	}
	if _, err := store.CreateJob(t.Context(), controlplane.UpsertJobRequest{
		ID: "job-durable-running", TenantID: "owner-1", StackID: legacy.Id, Type: "provision", State: "pending",
		Progress: 42, Step: "provider_create", Message: "Creating server",
	}); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	if _, err := store.StartJob(t.Context(), "owner-1", "job-durable-running", time.Now().UTC()); err != nil {
		t.Fatalf("StartJob: %v", err)
	}

	router := httpx.NewRouter()
	RegisterStackOperationsRoutesWithStores(router, app, nil, MonitoringStatusMetadata{}, nil, nil, StackOperationsRouteStores{
		Stacks: store,
		Jobs:   store,
	}, fakeManagedRuntimeLeaseLister{})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/stacks/"+legacy.Id+"/operations", nil)
	req = req.WithContext(identity.NewContext(req.Context(), &identity.Identity{UserID: "owner-1"}))
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s, want 200", recorder.Code, recorder.Body.String())
	}
	var envelope struct {
		Data stackOperationsPayload `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if envelope.Data.Stack["name"] != "Durable stack" {
		t.Fatalf("stack = %#v, want durable store projection", envelope.Data.Stack)
	}
	if envelope.Data.CurrentJob == nil || envelope.Data.CurrentJob.ID != "job-durable-running" || envelope.Data.CurrentJob.State != "running" {
		t.Fatalf("current job = %#v, want running durable job", envelope.Data.CurrentJob)
	}
}

func TestStackOperationsLegacyFallbackDoesNotCrossTenantBoundary(t *testing.T) {
	app, err := tests.NewTestApp(driftRoutePocketBaseTestDataDir(t))
	if err != nil {
		t.Fatalf("new test app: %v", err)
	}
	defer app.Cleanup()
	ensureStackOperationsTestCollections(t, app)

	stack := createStackOperationsTestStack(t, app, "owner-1", "running")
	stack.Set("name", "Tenant B private stack")
	stack.Set("tenant_id", "tenant-b")
	if err := app.Save(stack); err != nil {
		t.Fatalf("save tenant B stack: %v", err)
	}

	router := httpx.NewRouter()
	store := controlplane.NewMemoryStore()
	RegisterStackOperationsRoutesWithStores(router, app, nil, MonitoringStatusMetadata{}, nil, nil, StackOperationsRouteStores{Stacks: store}, fakeManagedRuntimeLeaseLister{})
	event, recorder := registryRouteStoreTestEvent(http.MethodGet, "/api/v1/stacks/"+stack.Id+"/operations", "owner-1", "tenant-a", nil)
	router.ServeHTTP(recorder, event.Request)

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d body=%s, want tenant-scoped 404", recorder.Code, recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), "Tenant B private stack") {
		t.Fatalf("cross-tenant legacy stack leaked in response: %s", recorder.Body.String())
	}
}

func TestStackOperationsLegacyWorkerProjectionDoesNotCrossTenantBoundary(t *testing.T) {
	app, err := tests.NewTestApp(driftRoutePocketBaseTestDataDir(t))
	if err != nil {
		t.Fatalf("new test app: %v", err)
	}
	defer app.Cleanup()
	ensureStackOperationsTestCollections(t, app)

	stack := createStackOperationsTestStack(t, app, "owner-1", "running")
	stack.Set("tenant_id", "tenant-a")
	if err := app.Save(stack); err != nil {
		t.Fatalf("save tenant A stack: %v", err)
	}
	workerA := createStackOperationsTestWorker(t, app, "owner-1", "", "tenant-a-worker", true)
	workerA.Set("tenant_id", "tenant-a")
	if err := app.Save(workerA); err != nil {
		t.Fatalf("save tenant A worker: %v", err)
	}
	workerB := createStackOperationsTestWorker(t, app, "owner-1", "", "tenant-b-worker", true)
	workerB.Set("tenant_id", "tenant-b")
	if err := app.Save(workerB); err != nil {
		t.Fatalf("save tenant B worker: %v", err)
	}
	createStackOperationsTestWorker(t, app, "owner-1", "", "tenantless-worker", true)

	router := httpx.NewRouter()
	store := controlplane.NewMemoryStore()
	RegisterStackOperationsRoutesWithStores(router, app, nil, MonitoringStatusMetadata{}, nil, nil, StackOperationsRouteStores{Stacks: store}, fakeManagedRuntimeLeaseLister{})
	event, recorder := registryRouteStoreTestEvent(http.MethodGet, "/api/v1/stacks/"+stack.Id+"/operations", "owner-1", "tenant-a", nil)
	router.ServeHTTP(recorder, event.Request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s, want 200", recorder.Code, recorder.Body.String())
	}
	var envelope struct {
		Data stackOperationsPayload `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(envelope.Data.Servers) != 1 || envelope.Data.Servers[0].Hostname != "tenant-a-worker" {
		t.Fatalf("hosted legacy worker scope = %#v, want tenant A worker only", envelope.Data.Servers)
	}
}

func TestStackOperationsProjectsWorkerHandoffCapabilities(t *testing.T) {
	app, err := tests.NewTestApp(driftRoutePocketBaseTestDataDir(t))
	if err != nil {
		t.Fatalf("new test app: %v", err)
	}
	defer app.Cleanup()
	ensureStackOperationsTestCollections(t, app)

	stack := createStackOperationsTestStack(t, app, "owner-1", "pending")
	worker := createStackOperationsTestWorker(t, app, "owner-1", stack.Id, "storage-node", true)
	worker.Set("type", "storage")
	worker.Set("tags", nodehandoff.MergeTags("rack-a", map[string]any{
		nodehandoff.KeyServerNodeRole:         "storage",
		nodehandoff.KeyRequestedServices:      []string{"files", "immich"},
		nodehandoff.KeyServerRemoteHost:       "10.10.10.30",
		nodehandoff.KeyServerRemoteUser:       "ubuntu",
		nodehandoff.KeyServerRemotePort:       2222,
		nodehandoff.KeyServerRemoteCredential: "ssh-key:storage-1",
		nodehandoff.KeyServerRemoteUseSudo:    true,
	}))
	if err := app.Save(worker); err != nil {
		t.Fatalf("save worker: %v", err)
	}

	event, recorder := stackOperationsRouteTestEvent(http.MethodGet, "/api/v1/stacks/"+stack.Id+"/operations", stack.Id, "owner-1")
	if err := (stackOperationsRouteHandlers{app: app, managedRuntimeLeases: fakeManagedRuntimeLeaseLister{}}).operations(event); err != nil {
		t.Fatalf("operations returned router error: %v", err)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s, want 200", recorder.Code, recorder.Body.String())
	}

	var envelope struct {
		Data stackOperationsPayload `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got := len(envelope.Data.Servers); got != 1 {
		t.Fatalf("server count = %d, want 1", got)
	}
	server := envelope.Data.Servers[0]
	if server.Role != "storage" || server.Capabilities[nodehandoff.KeyServerNodeRole] != "storage" {
		t.Fatalf("server handoff role not projected: %#v", server)
	}
	services := nodehandoff.ServiceKeysFromAny(server.Capabilities[nodehandoff.KeyRequestedServices])
	if strings.Join(services, ",") != "files,immich" {
		t.Fatalf("requested services = %#v, want files/immich", services)
	}
	if server.Capabilities[nodehandoff.KeyServerRemoteHost] != "10.10.10.30" || server.Capabilities[nodehandoff.KeyServerRemotePort] != float64(2222) {
		t.Fatalf("remote handoff not projected: %#v", server.Capabilities)
	}
}

func TestStackOperationsProjectsStoreWorkerHandoffCapabilities(t *testing.T) {
	ctx := context.Background()
	store := controlplane.NewMemoryStore()
	now := time.Now().UTC()
	if _, err := store.CreateStack(ctx, controlplane.CreateStackRequest{
		ID:             "stack-store-handoff",
		TenantID:       "tenant-1",
		OwnerSubjectID: "owner-1",
		Name:           "Store Handoff",
		Mode:           "easy",
		Status:         "pending",
	}); err != nil {
		t.Fatalf("CreateStack: %v", err)
	}
	if _, err := store.UpsertWorkerHeartbeat(ctx, controlplane.Worker{
		ID:             "worker-storage",
		TenantID:       "tenant-1",
		StackID:        "stack-store-handoff",
		OwnerSubjectID: "owner-1",
		Hostname:       "storage-node",
		IP:             "10.10.10.31",
		OS:             "linux",
		Arch:           "amd64",
		Status:         "approved",
		Approved:       true,
		ApprovedAt:     &now,
		LastSeenAt:     &now,
		Type:           "storage",
		Tags:           map[string]any{"raw": "rack-b"},
		Capabilities: map[string]any{
			nodehandoff.KeyServerNodeRole:    "storage",
			nodehandoff.KeyRequestedServices: []string{"files", "immich"},
		},
		Resources: map[string]any{
			nodehandoff.KeyServerRemoteHost:       "10.10.10.31",
			nodehandoff.KeyServerRemoteUser:       "ubuntu",
			nodehandoff.KeyServerRemotePort:       2222,
			nodehandoff.KeyServerRemoteCredential: "ssh-key:storage-2",
		},
	}); err != nil {
		t.Fatalf("UpsertWorkerHeartbeat: %v", err)
	}

	event, recorder := stackOperationsRouteTestEvent(http.MethodGet, "/api/v1/stacks/stack-store-handoff/operations", "stack-store-handoff", "owner-1")
	event.Request = event.Request.WithContext(identity.NewContext(event.Request.Context(), &identity.Identity{UserID: "owner-1", OrgID: "tenant-1"}))
	if err := (stackOperationsRouteHandlers{stackStore: store, workerStore: store, managedRuntimeLeases: fakeManagedRuntimeLeaseLister{}}).operations(event); err != nil {
		t.Fatalf("operations returned router error: %v", err)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s, want 200", recorder.Code, recorder.Body.String())
	}

	var envelope struct {
		Data stackOperationsPayload `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got := len(envelope.Data.Servers); got != 1 {
		t.Fatalf("server count = %d, want 1", got)
	}
	capabilities := envelope.Data.Servers[0].Capabilities
	if capabilities[nodehandoff.KeyServerNodeRole] != "storage" || capabilities[nodehandoff.KeyServerRemoteHost] != "10.10.10.31" {
		t.Fatalf("store worker handoff not projected: %#v", capabilities)
	}
	services := nodehandoff.ServiceKeysFromAny(capabilities[nodehandoff.KeyRequestedServices])
	if strings.Join(services, ",") != "files,immich" {
		t.Fatalf("store requested services = %#v, want files/immich", services)
	}
}

func TestMonitorCockpitAggregatesSelectedOwnedStack(t *testing.T) {
	app, err := tests.NewTestApp(driftRoutePocketBaseTestDataDir(t))
	if err != nil {
		t.Fatalf("new test app: %v", err)
	}
	defer app.Cleanup()
	ensureStackOperationsTestCollections(t, app)

	stack := createStackOperationsTestStack(t, app, "owner-1", "running")
	otherStack := createStackOperationsTestStack(t, app, "owner-2", "running")
	createStackOperationsTestWorker(t, app, "owner-1", stack.Id, "node-a", true)
	node := createStackOperationsTestNode(t, app, stack.Id, "node-a")
	createStackOperationsTestService(t, app, node.Id, "vaultwarden")
	createStackOperationsTestJob(t, app, stack.Id, "service-migration", "completed")

	event, recorder := stackOperationsRouteTestEvent(http.MethodGet, "/api/v1/monitor/cockpit?techstack_id="+stack.Id, stack.Id, "owner-1")
	if err := (stackOperationsRouteHandlers{app: app, managedRuntimeLeases: fakeManagedRuntimeLeaseLister{}}).monitorCockpit(event); err != nil {
		t.Fatalf("monitor cockpit returned router error: %v", err)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s, want 200", recorder.Code, recorder.Body.String())
	}

	var envelope struct {
		Data monitorCockpitPayload `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if envelope.Data.TechstackID != stack.Id {
		t.Fatalf("techstack_id = %q, want %q", envelope.Data.TechstackID, stack.Id)
	}
	if strings.Contains(recorder.Body.String(), `"selected_stack_id"`) {
		t.Fatalf("monitor cockpit exposed ambiguous selected_stack_id: %s", recorder.Body.String())
	}
	if got := len(envelope.Data.Stacks); got != 1 {
		t.Fatalf("owned stack count = %d, want 1", got)
	}
	if envelope.Data.Stack["id"] == otherStack.Id {
		t.Fatal("cockpit leaked foreign stack")
	}
	if got := len(envelope.Data.Servers); got != 1 {
		t.Fatalf("servers = %d, want 1", got)
	}
	if got := len(envelope.Data.Services); got != 1 {
		t.Fatalf("services = %d, want 1", got)
	}
	if got := len(envelope.Data.Jobs); got != 1 {
		t.Fatalf("jobs = %d, want 1", got)
	}
	if envelope.Data.KPIs.RegisteredServers != 1 || envelope.Data.KPIs.RunningServices != 1 {
		t.Fatalf("unexpected cockpit KPIs: %#v", envelope.Data.KPIs)
	}
}

func TestMonitorCockpitProjectsPersistedEnrollmentWait(t *testing.T) {
	app, err := tests.NewTestApp(driftRoutePocketBaseTestDataDir(t))
	if err != nil {
		t.Fatalf("new test app: %v", err)
	}
	defer app.Cleanup()
	ensureStackOperationsTestCollections(t, app)

	stack := createStackOperationsTestStack(t, app, "owner-1", "provisioning")
	nextResumeAt := "2026-07-19T08:15:00Z"
	job := createStackOperationsTestJob(t, app, stack.Id, "managed_runtime_enrollment", "pending")
	job.Set("progress", 45)
	job.Set("result", map[string]any{
		"job_wait": map[string]any{
			"state":          "waiting",
			"reason":         "waiting_enrollment",
			"next_resume_at": nextResumeAt,
		},
	})
	if err := app.Save(job); err != nil { // pocketbase-migration-compat: verifies legacy JSONField projection
		t.Fatalf("save waiting job: %v", err)
	}

	event, recorder := stackOperationsRouteTestEvent(http.MethodGet, "/api/v1/monitor/cockpit?techstack_id="+stack.Id, stack.Id, "owner-1")
	if err := (stackOperationsRouteHandlers{app: app, managedRuntimeLeases: fakeManagedRuntimeLeaseLister{}}).monitorCockpit(event); err != nil {
		t.Fatalf("monitor cockpit returned router error: %v", err)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s, want 200", recorder.Code, recorder.Body.String())
	}

	var envelope struct {
		Data monitorCockpitPayload `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(envelope.Data.Jobs) != 1 {
		t.Fatalf("jobs = %#v, want persisted waiting job", envelope.Data.Jobs)
	}
	got := envelope.Data.Jobs[0]
	if got.State != "waiting" || got.WaitReason != "waiting_enrollment" || got.NextResumeAt != nextResumeAt || got.Progress != 45 {
		t.Fatalf("waiting job projection = %#v", got)
	}
}

func TestMonitorCockpitStoreJobProjectsPersistedEnrollmentWait(t *testing.T) {
	nextResumeAt := "2026-07-19T08:15:00Z"
	got := monitorCockpitJobFromStore(controlplane.Job{
		ID:       "job-waiting",
		Type:     "provision",
		State:    "pending",
		Progress: 45,
		Result: map[string]any{
			"job_wait": map[string]any{
				"state":          "waiting",
				"reason":         "waiting_enrollment",
				"next_resume_at": nextResumeAt,
			},
		},
	})

	if got.State != "waiting" || got.WaitReason != "waiting_enrollment" || got.NextResumeAt != nextResumeAt || got.Progress != 45 {
		t.Fatalf("store waiting job projection = %#v", got)
	}
}

func TestStackOperationsProjectsManagedMonthlyRuntimeLease(t *testing.T) {
	app, err := tests.NewTestApp(driftRoutePocketBaseTestDataDir(t))
	if err != nil {
		t.Fatalf("new test app: %v", err)
	}
	defer app.Cleanup()
	ensureStackOperationsTestCollections(t, app)

	stack := createStackOperationsTestStack(t, app, "owner-1", "pending")
	stack.Set("server_provisioning_mode", "kombify-cloud")
	stack.Set("server_mode", "monthly-runtime")
	stack.Set("runtime_lane", "monthly-runtime")
	stack.Set("lease_id", "lease-1")
	if err := app.Save(stack); err != nil {
		t.Fatalf("save managed stack fields: %v", err)
	}
	lister := fakeManagedRuntimeLeaseLister{leases: []vmlease.Lease{
		createStackOperationsTestLease("lease-1", "owner-1", "owner-1", stack.Id, "enrolled"),
		createStackOperationsTestLease("lease-other-stack", "owner-1", "owner-1", "other-stack", "enrolled"),
		createStackOperationsTestLease("lease-other-owner", "owner-2", "owner-2", stack.Id, "enrolled"),
	}}

	event, recorder := stackOperationsRouteTestEvent(http.MethodGet, "/api/v1/stacks/"+stack.Id+"/operations", stack.Id, "owner-1")
	if err := (stackOperationsRouteHandlers{app: app, managedRuntimeLeases: lister}).operations(event); err != nil {
		t.Fatalf("operations returned router error: %v", err)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s, want 200", recorder.Code, recorder.Body.String())
	}

	var envelope struct {
		Data stackOperationsPayload `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got := len(envelope.Data.Servers); got != 1 {
		t.Fatalf("server count = %d, want projected monthly runtime only", got)
	}
	server := envelope.Data.Servers[0]
	if server.ID != "lease:lease-1" || server.ServerID != runtimeidentity.LeaseServerID("lease-1") || server.Source != managedRuntimeInventorySource || server.LeaseID != "lease-1" {
		t.Fatalf("unexpected managed runtime server: %#v", server)
	}
	if !server.Approved || server.Assignment != "stack" || !server.Assignable {
		t.Fatalf("managed runtime approval/assignment flags wrong: %#v", server)
	}
	if server.Health.CPUPercent.Status != "ok" || server.Health.MemoryPercent.Status != "ok" || server.Health.DiskPercent.Status != "ok" {
		t.Fatalf("managed runtime health metrics should come from lease metadata: %#v", server.Health)
	}
	if envelope.Data.Readiness.CanStart || envelope.Data.Readiness.ReviewRequired || envelope.Data.Readiness.Approved != 1 || envelope.Data.Readiness.Connected != 0 || envelope.Data.Readiness.Status != "waiting_for_managed_runtime" {
		t.Fatalf("enrolled lease without canonical Guard heartbeat must remain disconnected: %#v", envelope.Data.Readiness)
	}
	if len(envelope.Data.Services) != 0 || envelope.Data.KPIs.RunningServices != 0 {
		t.Fatalf("managed operations must not synthesize service rows: services=%#v kpis=%#v", envelope.Data.Services, envelope.Data.KPIs)
	}
}

func TestStackDashboardPathsFailClosedWhenManagedRuntimeAuthorityFails(t *testing.T) {
	app, err := tests.NewTestApp(driftRoutePocketBaseTestDataDir(t))
	if err != nil {
		t.Fatalf("new test app: %v", err)
	}
	defer app.Cleanup()
	ensureStackOperationsTestCollections(t, app)
	stack := createStackOperationsTestStack(t, app, "owner-1", "pending")

	store := controlplane.NewMemoryStore()
	seedControlPlaneStack(t, store, "tenant-1", "owner-1", "stack-store")

	testCases := []struct {
		name      string
		target    string
		stackID   string
		storePath bool
		invoke    func(stackOperationsRouteHandlers, *httpx.Event) error
	}{
		{name: "legacy operations", target: "/api/v1/stacks/" + stack.Id + "/operations", stackID: stack.Id, invoke: func(h stackOperationsRouteHandlers, e *httpx.Event) error { return h.operations(e) }},
		{name: "legacy cockpit", target: "/api/v1/monitor/cockpit?techstack_id=" + stack.Id, stackID: stack.Id, invoke: func(h stackOperationsRouteHandlers, e *httpx.Event) error { return h.monitorCockpit(e) }},
		{name: "legacy server details", target: "/api/v1/stacks/" + stack.Id + "/servers/server-1", stackID: stack.Id, invoke: func(h stackOperationsRouteHandlers, e *httpx.Event) error { return h.serverDetails(e) }},
		{name: "store operations", target: "/api/v1/stacks/stack-store/operations", stackID: "stack-store", storePath: true, invoke: func(h stackOperationsRouteHandlers, e *httpx.Event) error { return h.operations(e) }},
		{name: "store cockpit", target: "/api/v1/monitor/cockpit?techstack_id=stack-store", stackID: "stack-store", storePath: true, invoke: func(h stackOperationsRouteHandlers, e *httpx.Event) error { return h.monitorCockpit(e) }},
		{name: "store server details", target: "/api/v1/stacks/stack-store/servers/server-1", stackID: "stack-store", storePath: true, invoke: func(h stackOperationsRouteHandlers, e *httpx.Event) error { return h.serverDetails(e) }},
	}

	for _, test := range testCases {
		t.Run(test.name, func(t *testing.T) {
			event, recorder := stackOperationsRouteTestEvent(http.MethodGet, test.target, test.stackID, "owner-1")
			event.Request.SetPathValue("serverId", "server-1")
			handler := stackOperationsRouteHandlers{app: app, managedRuntimeLeases: failingManagedRuntimeLeaseLister{}}
			if test.storePath {
				event.Request = event.Request.WithContext(identity.NewContext(event.Request.Context(), &identity.Identity{UserID: "owner-1", OrgID: "tenant-1"}))
				handler.stackStore = store
				handler.workerStore = store
				handler.registryStore = store
				handler.jobStore = store
			}
			if err := test.invoke(handler, event); err != nil {
				t.Fatalf("handler returned router error: %v", err)
			}
			if recorder.Code != http.StatusServiceUnavailable {
				t.Fatalf("status = %d body=%s, want 503", recorder.Code, recorder.Body.String())
			}
			if !strings.Contains(recorder.Body.String(), "managed_runtime_authority_unavailable") || strings.Contains(recorder.Body.String(), "lease store unavailable") {
				t.Fatalf("unexpected fail-closed response: %s", recorder.Body.String())
			}
		})
	}
}

func TestOperationServerDedupCollapsesLeaseAndStaleRowsIntoCanonicalServer(t *testing.T) {
	leaseID := "lease-ionos-1"
	serverID := runtimeidentity.LeaseServerID(leaseID)
	canonical := stackOperationServer{
		ID: serverID, ServerID: serverID, LeaseID: leaseID, Source: "canonical-server",
		Hostname: "agent-observed", Status: "connected", IP: "203.0.113.10",
		Capabilities: map[string]any{"provider": "ionos-observed"},
		Health: stackServerHealth{
			State: "healthy", CPUPercent: metricKnown(20, "%"), MemoryPercent: metricUnknown("%"),
			DiskPercent: metricUnknown("%"), UptimeSeconds: metricUnknown("s"),
		},
	}
	stale := stackOperationServer{
		ID: "stale-error-row", Source: workerRegistryInventorySource, Status: "error", IP: "198.51.100.9",
		Capabilities: map[string]any{"server_id": serverID, "lease_id": leaseID, "provider": "stale-provider"},
		Health:       stackServerHealth{State: "unhealthy", CPUPercent: metricKnown(99, "%"), MemoryPercent: metricUnknown("%"), DiskPercent: metricUnknown("%"), UptimeSeconds: metricUnknown("s")},
	}
	leaseProjection := stackOperationServer{
		ID: "lease:" + leaseID, ServerID: serverID, LeaseID: leaseID, Source: managedRuntimeInventorySource,
		IP: "192.0.2.20", Capabilities: map[string]any{"region": "de-fra", "lease_id": leaseID},
		Health: stackServerHealth{CPUPercent: metricUnknown("%"), MemoryPercent: metricKnown(42, "%"), DiskPercent: metricUnknown("%"), UptimeSeconds: metricUnknown("s")},
	}

	got := mergeCanonicalOperationServers([]stackOperationServer{canonical}, []stackOperationServer{stale, leaseProjection})
	if len(got) != 1 {
		t.Fatalf("server cards = %d, want exactly one: %#v", len(got), got)
	}
	server := got[0]
	if server.ID != serverID || server.ServerID != serverID || server.LeaseID != leaseID || server.Status != "connected" || server.IP != "203.0.113.10" || server.Health.State != "healthy" {
		t.Fatalf("canonical observation did not win: %#v", server)
	}
	if server.Health.CPUPercent.Value == nil || *server.Health.CPUPercent.Value != 20 || server.Health.MemoryPercent.Value == nil || *server.Health.MemoryPercent.Value != 42 || server.Capabilities["region"] != "de-fra" || server.Capabilities["provider"] != "ionos-observed" {
		t.Fatalf("lease fallback merge = %#v", server)
	}
}

func TestOperationServerDedupCollapsesLeaseAndStaleRowsWithoutCanonicalProjection(t *testing.T) {
	leaseID := "lease-ionos-1"
	serverID := runtimeidentity.LeaseServerID(leaseID)
	observed := stackOperationServer{
		ID: "worker-observation", ServerID: serverID, LeaseID: leaseID,
		Source: workerRegistryInventorySource, Status: "connected", IP: "203.0.113.10",
		Health: stackServerHealth{State: "healthy", CPUPercent: metricKnown(20, "%"), MemoryPercent: metricUnknown("%"), DiskPercent: metricUnknown("%"), UptimeSeconds: metricUnknown("s")},
	}
	leaseProjection := stackOperationServer{
		ID: "lease:" + leaseID, ServerID: serverID, LeaseID: leaseID,
		Source: managedRuntimeInventorySource, IP: "192.0.2.20",
		Health: stackServerHealth{CPUPercent: metricUnknown("%"), MemoryPercent: metricKnown(42, "%"), DiskPercent: metricUnknown("%"), UptimeSeconds: metricUnknown("s")},
	}

	got := mergeCanonicalOperationServers(nil, []stackOperationServer{observed, leaseProjection})
	if len(got) != 1 {
		t.Fatalf("server cards = %d, want exactly one before canonical backfill: %#v", len(got), got)
	}
	if got[0].Status != "connected" || got[0].IP != "203.0.113.10" || got[0].Health.MemoryPercent.Value == nil || *got[0].Health.MemoryPercent.Value != 42 {
		t.Fatalf("observed row should win with lease fallback: %#v", got[0])
	}
}

func TestStackOperationsDoesNotMarkManagedLeaseHealthyWithoutRuntimeTarget(t *testing.T) {
	app, err := tests.NewTestApp(driftRoutePocketBaseTestDataDir(t))
	if err != nil {
		t.Fatalf("new test app: %v", err)
	}
	defer app.Cleanup()
	ensureStackOperationsTestCollections(t, app)

	stack := createStackOperationsTestStack(t, app, "owner-1", "pending")
	stack.Set("server_provisioning_mode", "kombify-cloud")
	stack.Set("server_mode", "monthly-runtime")
	stack.Set("runtime_lane", "monthly-runtime")
	stack.Set("lease_id", "lease-1")
	if err := app.Save(stack); err != nil {
		t.Fatalf("save managed stack fields: %v", err)
	}
	lease := createStackOperationsTestLease("lease-1", "owner-1", "owner-1", stack.Id, "enrolled")
	delete(lease.Metadata, "public_ip")
	lister := fakeManagedRuntimeLeaseLister{leases: []vmlease.Lease{lease}}

	event, recorder := stackOperationsRouteTestEvent(http.MethodGet, "/api/v1/stacks/"+stack.Id+"/operations", stack.Id, "owner-1")
	if err := (stackOperationsRouteHandlers{app: app, managedRuntimeLeases: lister}).operations(event); err != nil {
		t.Fatalf("operations returned router error: %v", err)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s, want 200", recorder.Code, recorder.Body.String())
	}

	var envelope struct {
		Data stackOperationsPayload `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got := len(envelope.Data.Servers); got != 1 {
		t.Fatalf("server count = %d, want managed runtime projection", got)
	}
	server := envelope.Data.Servers[0]
	if server.Health.State == "healthy" || server.Status == "healthy" {
		t.Fatalf("managed lease without runtime target must not be healthy: %#v", server)
	}
	if server.Approved || server.Assignable || envelope.Data.Readiness.Approved != 0 {
		t.Fatalf("managed lease without runtime target must not satisfy readiness: server=%#v readiness=%#v", server, envelope.Data.Readiness)
	}
	if server.IP != "" || envelope.Data.KPIs.HealthyServers != 0 {
		t.Fatalf("managed lease without runtime target must not expose IP or healthy KPI: server=%#v kpis=%#v", server, envelope.Data.KPIs)
	}
}

func TestStackOperationsKeepsUnenrolledManagedRuntimeLeasesNonAssignable(t *testing.T) {
	app, err := tests.NewTestApp(driftRoutePocketBaseTestDataDir(t))
	if err != nil {
		t.Fatalf("new test app: %v", err)
	}
	defer app.Cleanup()
	ensureStackOperationsTestCollections(t, app)

	stack := createStackOperationsTestStack(t, app, "owner-1", "pending")
	stack.Set("server_provisioning_mode", "kombify-cloud")
	stack.Set("server_mode", "monthly-runtime")
	stack.Set("runtime_lane", "monthly-runtime")
	stack.Set("lease_id", "lease-pending")
	if err := app.Save(stack); err != nil {
		t.Fatalf("save managed stack fields: %v", err)
	}
	lister := fakeManagedRuntimeLeaseLister{leases: []vmlease.Lease{
		createStackOperationsTestLease("lease-pending", "owner-1", "owner-1", stack.Id, "pending"),
		createStackOperationsTestLease("lease-failed", "owner-1", "owner-1", stack.Id, "failed"),
	}}

	event, recorder := stackOperationsRouteTestEvent(http.MethodGet, "/api/v1/stacks/"+stack.Id+"/operations", stack.Id, "owner-1")
	if err := (stackOperationsRouteHandlers{app: app, managedRuntimeLeases: lister}).operations(event); err != nil {
		t.Fatalf("operations returned router error: %v", err)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s, want 200", recorder.Code, recorder.Body.String())
	}

	var envelope struct {
		Data stackOperationsPayload `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got := len(envelope.Data.Servers); got != 2 {
		t.Fatalf("server count = %d, want pending and failed managed runtime rows", got)
	}
	for _, server := range envelope.Data.Servers {
		if server.Source != managedRuntimeInventorySource || server.Assignable || server.Approved {
			t.Fatalf("unenrolled managed runtime server should be visible but non-assignable: %#v", server)
		}
	}
	if envelope.Data.Readiness.CanStart || envelope.Data.Readiness.Approved != 0 {
		t.Fatalf("unenrolled managed runtime should not satisfy readiness: %#v", envelope.Data.Readiness)
	}
}

func TestStackServerDetailsReturnsManagedRuntimeProjection(t *testing.T) {
	app, err := tests.NewTestApp(driftRoutePocketBaseTestDataDir(t))
	if err != nil {
		t.Fatalf("new test app: %v", err)
	}
	defer app.Cleanup()
	ensureStackOperationsTestCollections(t, app)

	stack := createStackOperationsTestStack(t, app, "owner-1", "pending")
	stack.Set("server_provisioning_mode", "kombify-cloud")
	stack.Set("server_mode", "monthly-runtime")
	stack.Set("runtime_lane", "monthly-runtime")
	stack.Set("lease_id", "lease-1")
	if err := app.Save(stack); err != nil {
		t.Fatalf("save managed stack fields: %v", err)
	}
	lister := fakeManagedRuntimeLeaseLister{leases: []vmlease.Lease{
		createStackOperationsTestLease("lease-1", "owner-1", "owner-1", stack.Id, "enrolled"),
	}}
	createStackOperationsTestDeployJob(t, app, stack.Id, map[string]any{
		"services": []map[string]any{
			{
				"name":         "pocketid",
				"display_name": "Pocket ID",
				"type":         "identity",
				"status":       "running",
				"url":          "https://id.home.localhost",
				"port":         1411,
			},
			{
				"name":         "coolify",
				"display_name": "Coolify",
				"type":         "paas",
				"status":       "running",
				"url":          "https://coolify.home.localhost",
			},
		},
	})

	event, recorder := stackOperationsRouteTestEvent(http.MethodGet, "/api/v1/stacks/"+stack.Id+"/servers/lease:lease-1", stack.Id, "owner-1")
	event.Request.SetPathValue("serverId", "lease:lease-1")
	if err := (stackOperationsRouteHandlers{app: app, managedRuntimeLeases: lister}).serverDetails(event); err != nil {
		t.Fatalf("serverDetails returned router error: %v", err)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s, want 200", recorder.Code, recorder.Body.String())
	}

	var envelope struct {
		Data stackServerDetailsPayload `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if envelope.Data.Server.Source != managedRuntimeInventorySource || envelope.Data.Server.LeaseID != "lease-1" {
		t.Fatalf("unexpected server details: %#v", envelope.Data.Server)
	}
	if len(envelope.Data.Checks) != 0 {
		t.Fatalf("managed runtime projection should not synthesize worker prechecks, got %#v", envelope.Data.Checks)
	}
	if got := len(envelope.Data.Services); got != 2 {
		t.Fatalf("managed runtime details services = %d, want StackKit output services: %#v", got, envelope.Data.Services)
	}
	var pocketID *stackOperationService
	for i := range envelope.Data.Services {
		if envelope.Data.Services[i].Name == "pocket_id" {
			pocketID = &envelope.Data.Services[i]
			break
		}
	}
	if pocketID == nil || pocketID.TargetServerID != "lease:lease-1" || pocketID.Port != 1411 {
		t.Fatalf("unexpected mapped Pocket ID service: %#v", envelope.Data.Services)
	}
}

func TestStackOperationsAssignsUnscopedWorker(t *testing.T) {
	app, err := tests.NewTestApp(driftRoutePocketBaseTestDataDir(t))
	if err != nil {
		t.Fatalf("new test app: %v", err)
	}
	defer app.Cleanup()
	ensureStackOperationsTestCollections(t, app)

	stack := createStackOperationsTestStack(t, app, "owner-1", "pending")
	worker := createStackOperationsTestWorker(t, app, "owner-1", "", "legacy-node", true)

	event, recorder := stackOperationsRouteTestEvent(http.MethodPost, "/api/v1/stacks/"+stack.Id+"/workers/"+worker.Id+"/assign", stack.Id, "owner-1")
	event.Request.SetPathValue("workerId", worker.Id)
	assignErr := (stackOperationsRouteHandlers{app: app}).assignWorker(event)
	if assignErr != nil {
		t.Fatalf("assign returned router error: %v", assignErr)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s, want 200", recorder.Code, recorder.Body.String())
	}
	updated, err := app.FindRecordById("workers", worker.Id)
	if err != nil {
		t.Fatalf("find worker: %v", err)
	}
	if got := updated.GetString("stack_id"); got != stack.Id {
		t.Fatalf("worker stack_id = %q, want %q", got, stack.Id)
	}
}

func TestStackOperationsAssignsGuardConnectedControlPlaneWorker(t *testing.T) {
	store := controlplane.NewMemoryStore()
	now := time.Now().UTC()
	if _, err := store.CreateStack(context.Background(), controlplane.CreateStackRequest{
		ID:             "stack-1",
		TenantID:       "tenant-1",
		OwnerSubjectID: "owner-1",
		Name:           "Ops Stack",
		Status:         "pending",
	}); err != nil {
		t.Fatalf("CreateStack: %v", err)
	}
	worker, err := store.UpsertWorkerHeartbeat(context.Background(), controlplane.Worker{
		ID:             "worker-1",
		TenantID:       "tenant-1",
		OwnerSubjectID: "owner-1",
		Hostname:       "node-a",
		Status:         "approved",
		Approved:       true,
		ApprovedAt:     &now,
		LastSeenAt:     &now,
		CPUCores:       4,
		RAMMB:          8192,
		DiskGB:         80,
		Type:           "main",
		Provider:       "local",
		Tags:           map[string]any{"raw": "local-e2e"},
	})
	if err != nil {
		t.Fatalf("UpsertWorkerHeartbeat: %v", err)
	}
	serverID := seedStackOperationsAssignableRuntime(t, store, *worker, "")
	guardAt := now.Add(time.Second)
	worker.Status = "connected"
	worker.LastSeenAt = &guardAt
	worker, err = store.UpsertWorkerHeartbeat(context.Background(), *worker)
	if err != nil {
		t.Fatalf("UpsertWorkerHeartbeat(connected): %v", err)
	}
	seedStackOperationsGuardConnection(t, store, *worker, serverID, guardAt)

	event, recorder := stackOperationsRouteTestEvent(http.MethodPost, "/api/v1/stacks/stack-1/workers/worker-1/assign", "stack-1", "owner-1")
	event.Request.SetPathValue("workerId", "worker-1")
	event.Request = event.Request.WithContext(identity.NewContext(event.Request.Context(), &identity.Identity{UserID: "owner-1", OrgID: "tenant-1"}))
	assignErr := (stackOperationsRouteHandlers{stackStore: store, serverStore: store, workerStore: store}).assignWorker(event)
	if assignErr != nil {
		t.Fatalf("assign returned router error: %v", assignErr)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s, want 200", recorder.Code, recorder.Body.String())
	}
	updated, err := store.GetWorker(context.Background(), "tenant-1", "worker-1")
	if err != nil {
		t.Fatalf("GetWorker: %v", err)
	}
	if updated.StackID != "stack-1" {
		t.Fatalf("worker stack_id = %q, want stack-1", updated.StackID)
	}
	runtime, err := store.GetServerRuntime(context.Background(), "tenant-1", serverID)
	if err != nil {
		t.Fatalf("GetServerRuntime: %v", err)
	}
	if runtime.StackID != "stack-1" || runtime.WorkerID != "worker-1" || runtime.Revision != 3 || runtime.Generation != 1 || runtime.SourceAuthority != controlplane.ServerEventAuthorityGuard {
		t.Fatalf("canonical runtime assignment = %#v, want Guard-observed CAS-bound stack at revision 3/generation 1", runtime)
	}

	var envelope struct {
		Data struct {
			StackID  string               `json:"stack_id"`
			WorkerID string               `json:"worker_id"`
			Server   stackOperationServer `json:"server"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if envelope.Data.StackID != "stack-1" || envelope.Data.WorkerID != "worker-1" {
		t.Fatalf("unexpected assignment response: %+v", envelope.Data)
	}
	if envelope.Data.Server.Assignment != "stack" || envelope.Data.Server.TechstackID != "stack-1" {
		t.Fatalf("server assignment = %+v, want stack assignment", envelope.Data.Server)
	}

	repeatEvent, repeatRecorder := stackOperationsRouteTestEvent(http.MethodPost, "/api/v1/stacks/stack-1/workers/worker-1/assign", "stack-1", "owner-1")
	repeatEvent.Request.SetPathValue("workerId", "worker-1")
	repeatEvent.Request = repeatEvent.Request.WithContext(identity.NewContext(repeatEvent.Request.Context(), &identity.Identity{UserID: "owner-1", OrgID: "tenant-1"}))
	repeatAssignErr := (stackOperationsRouteHandlers{stackStore: store, serverStore: store, workerStore: store}).assignWorker(repeatEvent)
	if repeatAssignErr != nil {
		t.Fatalf("repeat assign returned router error: %v", repeatAssignErr)
	}
	if repeatRecorder.Code != http.StatusOK {
		t.Fatalf("repeat status = %d body=%s, want 200", repeatRecorder.Code, repeatRecorder.Body.String())
	}
	runtime, err = store.GetServerRuntime(context.Background(), "tenant-1", serverID)
	if err != nil {
		t.Fatalf("GetServerRuntime(repeat): %v", err)
	}
	if runtime.Revision != 3 {
		t.Fatalf("idempotent assignment appended revision %d, want unchanged revision 3", runtime.Revision)
	}
}

func TestWorkerAssignmentStatusAllowed(t *testing.T) {
	for _, test := range []struct {
		status string
		want   bool
	}{
		{status: "approved", want: true},
		{status: "connected", want: true},
		{status: "pending"},
		{status: "online"},
		{status: "offline"},
		{status: ""},
	} {
		if got := workerAssignmentStatusAllowed(test.status); got != test.want {
			t.Fatalf("workerAssignmentStatusAllowed(%q) = %v, want %v", test.status, got, test.want)
		}
	}
}

func TestStackOperationsAssignmentFailsClosedWithoutCanonicalRuntime(t *testing.T) {
	store := controlplane.NewMemoryStore()
	ctx := context.Background()
	if _, err := store.CreateStack(ctx, controlplane.CreateStackRequest{
		ID: "stack-1", TenantID: "tenant-1", OwnerSubjectID: "owner-1", Name: "Ops Stack", Status: "pending",
	}); err != nil {
		t.Fatalf("CreateStack: %v", err)
	}
	now := time.Now().UTC()
	if _, err := store.UpsertWorkerHeartbeat(ctx, controlplane.Worker{
		ID: "worker-1", TenantID: "tenant-1", OwnerSubjectID: "owner-1",
		Status: "approved", Approved: true, ApprovedAt: &now, LastSeenAt: &now,
	}); err != nil {
		t.Fatalf("UpsertWorkerHeartbeat: %v", err)
	}

	event, recorder := stackOperationsRouteTestEvent(http.MethodPost, "/api/v1/stacks/stack-1/workers/worker-1/assign", "stack-1", "owner-1")
	event.Request.SetPathValue("workerId", "worker-1")
	event.Request = event.Request.WithContext(identity.NewContext(event.Request.Context(), &identity.Identity{UserID: "owner-1", OrgID: "tenant-1"}))
	assignErr := (stackOperationsRouteHandlers{stackStore: store, serverStore: store, workerStore: store}).assignWorker(event)
	if assignErr != nil {
		t.Fatalf("assign returned router error: %v", assignErr)
	}
	if recorder.Code != http.StatusConflict {
		t.Fatalf("status = %d body=%s, want 409", recorder.Code, recorder.Body.String())
	}
	worker, err := store.GetWorker(ctx, "tenant-1", "worker-1")
	if err != nil {
		t.Fatalf("GetWorker: %v", err)
	}
	if worker.StackID != "" {
		t.Fatalf("worker stack_id = %q after failed canonical bind, want unchanged", worker.StackID)
	}
}

func TestStackOperationsAssignmentRejectsForeignCanonicalBinding(t *testing.T) {
	store := controlplane.NewMemoryStore()
	ctx := context.Background()
	if _, err := store.CreateStack(ctx, controlplane.CreateStackRequest{
		ID: "stack-1", TenantID: "tenant-1", OwnerSubjectID: "owner-1", Name: "Ops Stack", Status: "pending",
	}); err != nil {
		t.Fatalf("CreateStack: %v", err)
	}
	now := time.Now().UTC()
	worker, err := store.UpsertWorkerHeartbeat(ctx, controlplane.Worker{
		ID: "worker-1", TenantID: "tenant-1", OwnerSubjectID: "owner-1",
		Status: "approved", Approved: true, ApprovedAt: &now, LastSeenAt: &now,
	})
	if err != nil {
		t.Fatalf("UpsertWorkerHeartbeat: %v", err)
	}
	serverID := seedStackOperationsAssignableRuntime(t, store, *worker, "stack-other")

	event, recorder := stackOperationsRouteTestEvent(http.MethodPost, "/api/v1/stacks/stack-1/workers/worker-1/assign", "stack-1", "owner-1")
	event.Request.SetPathValue("workerId", "worker-1")
	event.Request = event.Request.WithContext(identity.NewContext(event.Request.Context(), &identity.Identity{UserID: "owner-1", OrgID: "tenant-1"}))
	assignErr := (stackOperationsRouteHandlers{stackStore: store, serverStore: store, workerStore: store}).assignWorker(event)
	if assignErr != nil {
		t.Fatalf("assign returned router error: %v", assignErr)
	}
	if recorder.Code != http.StatusConflict {
		t.Fatalf("status = %d body=%s, want 409", recorder.Code, recorder.Body.String())
	}
	updatedWorker, err := store.GetWorker(ctx, "tenant-1", "worker-1")
	if err != nil {
		t.Fatalf("GetWorker: %v", err)
	}
	updatedRuntime, err := store.GetServerRuntime(ctx, "tenant-1", serverID)
	if err != nil {
		t.Fatalf("GetServerRuntime: %v", err)
	}
	if updatedWorker.StackID != "" || updatedRuntime.StackID != "stack-other" || updatedRuntime.Revision != 1 {
		t.Fatalf("foreign binding changed: worker=%#v runtime=%#v", updatedWorker, updatedRuntime)
	}
}

func seedStackOperationsAssignableRuntime(t *testing.T, store *controlplane.MemoryStore, worker controlplane.Worker, stackID string) string {
	t.Helper()
	serverID := runtimeServerIDForWorker(worker.ID)
	result, err := store.ApplyServerEvent(t.Context(), controlplane.ServerEvent{
		TenantID: worker.TenantID, ServerID: serverID, Generation: 1,
		Authority: controlplane.ServerEventAuthorityControlPlane,
		Source:    "worker-enrollment", SourceID: "worker-enrollment", ObservedAt: time.Now().UTC(),
		Runtime: controlplane.ServerRuntime{
			StackID: stackID, OwnerSubjectID: worker.OwnerSubjectID, WorkerID: worker.ID,
			Name: worker.Hostname, LifecycleState: string(serverregistry.LifecycleEnrolling),
		},
	})
	if err != nil {
		t.Fatalf("ApplyServerEvent(seed worker runtime): %v", err)
	}
	if result == nil || result.Server == nil {
		t.Fatal("ApplyServerEvent(seed worker runtime) returned no aggregate")
	}
	return serverID
}

func seedStackOperationsGuardConnection(t *testing.T, store *controlplane.MemoryStore, worker controlplane.Worker, serverID string, observedAt time.Time) {
	t.Helper()
	current, err := store.GetServerRuntime(t.Context(), worker.TenantID, serverID)
	if err != nil {
		t.Fatalf("GetServerRuntime(before Guard): %v", err)
	}
	result, err := store.ApplyServerEvent(t.Context(), controlplane.ServerEvent{
		TenantID: worker.TenantID, ServerID: serverID,
		ExpectedRevision: current.Revision, Generation: current.Generation,
		Authority: controlplane.ServerEventAuthorityGuard,
		Source:    "guard-heartbeat", SourceID: worker.ID, SourceEpoch: "guard-epoch-1", SourceSequence: 1,
		ObservedAt: observedAt,
		Runtime: controlplane.ServerRuntime{
			ConnectionState: string(serverregistry.ConnectionConnected),
			HealthState:     string(serverregistry.HealthHealthy),
			LastHeartbeatAt: &observedAt,
		},
	})
	if err != nil {
		t.Fatalf("ApplyServerEvent(Guard heartbeat): %v", err)
	}
	if result == nil || result.Server == nil || result.Server.ConnectionState != string(serverregistry.ConnectionConnected) {
		t.Fatalf("ApplyServerEvent(Guard heartbeat) = %#v", result)
	}
}

func TestStackOperationsUsesControlPlaneStoresForOperationsPayload(t *testing.T) {
	app, err := tests.NewTestApp(driftRoutePocketBaseTestDataDir(t))
	if err != nil {
		t.Fatalf("new test app: %v", err)
	}
	defer app.Cleanup()
	ensureStackOperationsTestCollections(t, app)

	ctx := context.Background()
	store := controlplane.NewMemoryStore()
	now := time.Now().UTC()
	if _, err := store.CreateStack(ctx, controlplane.CreateStackRequest{
		ID:             "stack-store",
		TenantID:       "tenant-1",
		OwnerSubjectID: "owner-1",
		Name:           "Store Stack",
		Mode:           "easy",
		Status:         "running",
		Config: map[string]any{
			"stackkit":    "basement-kit",
			"min_servers": 1,
		},
	}); err != nil {
		t.Fatalf("CreateStack: %v", err)
	}
	if _, err := store.UpsertWorkerHeartbeat(ctx, controlplane.Worker{
		ID:             "worker-1",
		TenantID:       "tenant-1",
		StackID:        "stack-store",
		OwnerSubjectID: "owner-1",
		Hostname:       "main",
		IP:             "127.0.0.1",
		OS:             "linux",
		Arch:           "amd64",
		Status:         "approved",
		Approved:       true,
		ApprovedAt:     &now,
		LastSeenAt:     &now,
		CPUCores:       8,
		RAMMB:          8192,
		DiskGB:         100,
		Type:           "main",
		Provider:       "local",
	}); err != nil {
		t.Fatalf("UpsertWorkerHeartbeat: %v", err)
	}
	if _, err := store.UpsertNode(ctx, controlplane.Node{
		ID:       "node-1",
		TenantID: "tenant-1",
		StackID:  "stack-store",
		WorkerID: "worker-1",
		Name:     "main",
		Role:     "main",
		Status:   "running",
		Address:  "127.0.0.1",
		Metadata: map[string]any{
			"source":                 "stackkit_outputs",
			"cpu_cores":              8,
			"ram_mb":                 8192,
			"disk_gb":                100,
			"runtime_cpu_percent":    "12.5",
			"runtime_memory_percent": "34.5",
			"runtime_disk_percent":   "56.5",
		},
	}); err != nil {
		t.Fatalf("UpsertNode: %v", err)
	}
	if _, err := store.UpsertService(ctx, controlplane.Service{
		ID:         "service-coolify",
		TenantID:   "tenant-1",
		StackID:    "stack-store",
		NodeID:     "node-1",
		ServiceKey: "coolify",
		Name:       "coolify",
		Status:     "running",
		Source:     "managed",
		URL:        "http://coolify.home.localhost",
		Metadata:   map[string]any{"type": "paas", "display_name": "Coolify"},
	}); err != nil {
		t.Fatalf("UpsertService: %v", err)
	}
	if _, err := store.UpsertJob(ctx, controlplane.UpsertJobRequest{
		ID:       "job-1",
		TenantID: "tenant-1",
		StackID:  "stack-store",
		Type:     "deploy",
		State:    "completed",
		Progress: 100,
		Step:     "finalize",
		Message:  "Rollout complete",
	}); err != nil {
		t.Fatalf("UpsertJob: %v", err)
	}

	event, recorder := stackOperationsRouteTestEvent(http.MethodGet, "/api/v1/stacks/stack-store/operations", "stack-store", "owner-1")
	event.Request = event.Request.WithContext(identity.NewContext(event.Request.Context(), &identity.Identity{UserID: "owner-1", OrgID: "tenant-1"}))
	pendingLease := createStackOperationsTestLease("lease-pending", "tenant-1", "owner-1", "stack-store", "pending")
	pendingLease.Metadata["public_ip"] = ""
	if err := (stackOperationsRouteHandlers{
		app:                  app,
		stackStore:           store,
		workerStore:          store,
		registryStore:        store,
		jobStore:             store,
		managedRuntimeLeases: fakeManagedRuntimeLeaseLister{leases: []vmlease.Lease{pendingLease}},
	}).operations(event); err != nil {
		t.Fatalf("operations returned router error: %v", err)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s, want 200", recorder.Code, recorder.Body.String())
	}

	var envelope struct {
		Data stackOperationsPayload `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode response: %v body=%s", err, recorder.Body.String())
	}
	if envelope.Data.Stack["id"] != "stack-store" {
		t.Fatalf("stack id = %#v, want stack-store", envelope.Data.Stack["id"])
	}
	if got := len(envelope.Data.Servers); got != 2 {
		t.Fatalf("servers = %d, want registry node plus pending managed lease", got)
	}
	var sawPendingLease, sawRegistryNode bool
	for _, server := range envelope.Data.Servers {
		switch server.ID {
		case "lease:lease-pending":
			if server.Source != managedRuntimeInventorySource || server.Approved {
				t.Fatalf("unexpected pending managed runtime projection: %#v", server)
			}
			sawPendingLease = true
		case "node-1":
			if server.Health.State != "unknown" || !server.Approved {
				t.Fatalf("unexpected registry node projection: %#v", server)
			}
			sawRegistryNode = true
		}
	}
	if !sawPendingLease || !sawRegistryNode {
		t.Fatalf("servers missing expected projections: %#v", envelope.Data.Servers)
	}
	if envelope.Data.Readiness.Connected != 0 || envelope.Data.Readiness.CanStart {
		t.Fatalf("registry node and worker timestamps must not substitute for canonical Guard connection evidence: %#v", envelope.Data.Readiness)
	}
	if envelope.Data.KPIs.HealthyServers != 0 {
		t.Fatalf("healthy servers = %d, want 0 without canonical Guard health evidence", envelope.Data.KPIs.HealthyServers)
	}
	if got := len(envelope.Data.Services); got != 1 {
		t.Fatalf("services = %d, want 1", got)
	}
	if service := envelope.Data.Services[0]; service.Name != "coolify" || service.Type != "paas" || service.URL == "" {
		t.Fatalf("unexpected service projection: %#v", service)
	}
	if envelope.Data.Readiness.Status != "running" {
		t.Fatalf("readiness status = %q, want running", envelope.Data.Readiness.Status)
	}
}

func TestStackOperationsShowsPlannedCanonicalServerImmediately(t *testing.T) {
	ctx := context.Background()
	store := controlplane.NewMemoryStore()
	if _, err := store.CreateStack(ctx, controlplane.CreateStackRequest{
		ID: "stack-planned", TenantID: "tenant-1", OwnerSubjectID: "owner-1",
		Name: "Planned", Status: "provisioning",
	}); err != nil {
		t.Fatalf("CreateStack: %v", err)
	}
	if _, err := store.UpsertServerRuntime(ctx, controlplane.ServerRuntime{
		ID: "server-planned", TenantID: "tenant-1", StackID: "stack-planned", OwnerSubjectID: "owner-1",
		Name: "Planned primary", LifecycleState: "planned", DesiredState: "active",
		ConnectionState: "pending", HealthState: "unknown", ReasonCode: "awaiting_pairing",
	}); err != nil {
		t.Fatalf("UpsertServerRuntime: %v", err)
	}
	if _, err := store.CreateJob(ctx, controlplane.UpsertJobRequest{
		ID: "job-planned", TenantID: "tenant-1", StackID: "stack-planned", Type: "provision", State: "pending", Progress: 12,
		Step: "create_lease", Message: "Allocating server", Result: map[string]any{
			"runtime_lifecycle": map[string]any{
				"version": "techstack.runtime-lifecycle/v1", "current_phase": "server_allocate",
				"phases": []any{map[string]any{"id": "server_allocate", "status": "running", "message": "Allocating server"}},
			},
		},
	}); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	if _, err := store.StartJob(ctx, "tenant-1", "job-planned", time.Now().UTC()); err != nil {
		t.Fatalf("StartJob: %v", err)
	}

	event, recorder := stackOperationsRouteTestEvent(http.MethodGet, "/api/v1/stacks/stack-planned/operations", "stack-planned", "owner-1")
	event.Request = event.Request.WithContext(identity.NewContext(event.Request.Context(), &identity.Identity{UserID: "owner-1", OrgID: "tenant-1"}))
	if err := (stackOperationsRouteHandlers{
		stackStore: store, serverStore: store, workerStore: store, registryStore: store, jobStore: store,
		managedRuntimeLeases: fakeManagedRuntimeLeaseLister{},
	}).operations(event); err != nil {
		t.Fatalf("operations: %v", err)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	var envelope struct {
		Data stackOperationsPayload `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(envelope.Data.Servers) != 1 {
		t.Fatalf("servers = %#v, want planned canonical server", envelope.Data.Servers)
	}
	server := envelope.Data.Servers[0]
	if server.ID != "server-planned" || server.Source != "canonical-server" || server.Status != "pending" || server.Health.State != "unknown" || server.Approved {
		t.Fatalf("planned server must stay honest: %#v", server)
	}
	if envelope.Data.CurrentJob == nil || envelope.Data.CurrentJob.ID != "job-planned" || envelope.Data.CurrentJob.Progress != 12 {
		t.Fatalf("current job = %#v, want persisted creation job", envelope.Data.CurrentJob)
	}
	if envelope.Data.RuntimeLifecycle == nil || envelope.Data.RuntimeLifecycle.CurrentPhase != "server_allocate" || len(envelope.Data.RuntimeLifecycle.Phases) != 1 {
		t.Fatalf("runtime lifecycle = %#v, want persisted server_allocate phase", envelope.Data.RuntimeLifecycle)
	}
}

func TestStackOperationsProjectsCanonicalServerInventoryMetadata(t *testing.T) {
	ctx := t.Context()
	store := controlplane.NewMemoryStore()
	if _, err := store.CreateStack(ctx, controlplane.CreateStackRequest{
		ID: "stack-observed", TenantID: "tenant-1", OwnerSubjectID: "owner-1",
		Name: "Observed Cloud", Status: "running",
		Config: map[string]any{
			"stackkit_catalog_ref": "cloud-kit",
			"context":              "cloud",
			"paas":                 "coolify",
			"metadata": map[string]any{
				"stackkit_mode": "simple",
				"compute_tier":  "standard",
			},
		},
	}); err != nil {
		t.Fatalf("CreateStack: %v", err)
	}
	now := time.Now().UTC()
	if _, err := store.UpsertServerRuntime(ctx, controlplane.ServerRuntime{
		ID: "server-observed", TenantID: "tenant-1", StackID: "stack-observed", OwnerSubjectID: "owner-1",
		WorkerID: "guard-observed", Name: "configured-name", LifecycleState: "active", DesiredState: "active",
		ConnectionState: "connected", HealthState: "healthy", LastHeartbeatAt: &now,
		Metadata: map[string]any{
			"inventory_source": "guard-inventory",
			"stackkit":         "cloud-kit",
			"stackkit_mode":    "simple",
			"domain":           "base.demo.kombify.me",
			"host": map[string]any{
				"hostname": "ionos-demo-1", "os": "ubuntu", "os_version": "24.04", "arch": "amd64",
				"public_ip": "203.0.113.10", "private_ip": "10.0.0.10", "local_ip": "192.168.1.10",
				"cpu_cores": 4, "ram_mb": 8192, "disk_gb": 120, "cpu_percent": 12.5,
				"memory_used_bytes": 4096, "memory_total_bytes": 8192,
				"disk_used_bytes": 25, "disk_total_bytes": 100, "uptime_seconds": 99,
			},
			"endpoints": []map[string]any{{
				"url": "https://base.demo.kombify.me", "visibility": "public",
				"health": "healthy", "provenance": "stackkit-access-manifest",
			}},
		},
	}); err != nil {
		t.Fatalf("UpsertServerRuntime: %v", err)
	}
	if _, err := store.UpsertServiceRuntime(ctx, controlplane.ServiceRuntime{
		ID: "service-coolify", TenantID: "tenant-1", StackID: "stack-observed", ServerID: "server-observed",
		ServiceKey: "coolify", Name: "Coolify", DesiredState: "running", ObservedState: "running",
		HealthState: "healthy", ObservedAt: &now, StackKitVersion: "4.2.1",
		Access: map[string]any{"mode": "direct", "url": "https://coolify.demo.kombify.me"}, Source: "stackkits-inventory",
	}); err != nil {
		t.Fatalf("UpsertServiceRuntime: %v", err)
	}

	event, recorder := stackOperationsRouteTestEvent(http.MethodGet, "/api/v1/stacks/stack-observed/operations", "stack-observed", "owner-1")
	event.Request = event.Request.WithContext(identity.NewContext(event.Request.Context(), &identity.Identity{UserID: "owner-1", OrgID: "tenant-1"}))
	if err := (stackOperationsRouteHandlers{
		stackStore: store, serverStore: store, serviceStore: store, workerStore: store, registryStore: store,
		managedRuntimeLeases: fakeManagedRuntimeLeaseLister{},
	}).operations(event); err != nil {
		t.Fatalf("operations: %v", err)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	var envelope struct {
		Data stackOperationsPayload `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(envelope.Data.Servers) != 1 {
		t.Fatalf("servers = %#v, want one canonical server", envelope.Data.Servers)
	}
	server := envelope.Data.Servers[0]
	if server.Source != "canonical-server" || server.Hostname != "ionos-demo-1" || server.IP != "203.0.113.10" {
		t.Fatalf("canonical identity projection = %#v", server)
	}
	if server.OS != "ubuntu" || server.OSVersion != "24.04" || server.Arch != "amd64" {
		t.Fatalf("host platform projection = %#v", server)
	}
	if len(server.HostAddresses) != 3 || server.HostAddresses[0].Provenance != "guard-inventory" {
		t.Fatalf("host addresses = %#v, want observed public/private/local addresses", server.HostAddresses)
	}
	if server.Health.State != "healthy" || metricValue(server.Health.CPUPercent) != 12.5 || metricValue(server.Health.MemoryPercent) != 50 || metricValue(server.Health.DiskPercent) != 25 || metricValue(server.Health.UptimeSeconds) != 99 {
		t.Fatalf("measured canonical health = %#v", server.Health)
	}
	if server.Capabilities["cpu_cores"] != float64(4) || server.Capabilities["ram_mb"] != float64(8192) || server.Capabilities["disk_gb"] != float64(120) {
		t.Fatalf("resource capabilities = %#v", server.Capabilities)
	}
	if strings.Join(server.Domains, ",") != "base.demo.kombify.me,coolify.demo.kombify.me" || len(server.ServiceEndpoints) != 2 {
		t.Fatalf("service addresses = domains:%#v endpoints:%#v", server.Domains, server.ServiceEndpoints)
	}
	if server.StackKit == nil || server.StackKit.Name != "cloud-kit" || server.StackKit.CatalogRef != "cloud-kit" || server.StackKit.Version != "4.2.1" || server.StackKit.Mode != "simple" || server.StackKit.Context != "cloud" || server.StackKit.PaaS != "coolify" || server.StackKit.ComputeTier != "standard" || server.StackKit.State != "observed" {
		t.Fatalf("stackkit deployment = %#v", server.StackKit)
	}
	if envelope.Data.Readiness.Connected != 1 {
		t.Fatalf("fresh canonical Guard heartbeat must qualify exactly one connected server: %#v", envelope.Data.Readiness)
	}
}

func TestStackOperationsAppliesQuarantinedAuthorityAfterCanonicalMerge(t *testing.T) {
	ctx := t.Context()
	store := controlplane.NewMemoryStore()
	if _, err := store.CreateStack(ctx, controlplane.CreateStackRequest{
		ID: "stack-quarantined", TenantID: "tenant-1", OwnerSubjectID: "owner-1",
		Name: "Quarantined", Status: "configured",
	}); err != nil {
		t.Fatalf("CreateStack: %v", err)
	}
	lease := createStackOperationsTestLease("lease-quarantined", "tenant-1", "owner-1", "stack-quarantined", monthlyRuntimeEnrollmentStatusEnrolled)
	now := time.Now().UTC()
	if _, err := store.UpsertServerRuntime(ctx, controlplane.ServerRuntime{
		ID: "server-quarantined", TenantID: "tenant-1", StackID: "stack-quarantined", OwnerSubjectID: "owner-1",
		LeaseID: string(lease.ID), Name: "quarantined-server", LifecycleState: "active", DesiredState: "active",
		ConnectionState: "connected", HealthState: "healthy", LastHeartbeatAt: &now,
	}); err != nil {
		t.Fatalf("UpsertServerRuntime: %v", err)
	}
	record := vmleases.LeaseInventoryRecord{
		Lease:              lease,
		ExecutionAuthority: vmleases.LeaseExecutionAuthorityLegacySimulate,
		AuthorityState:     vmleases.LeaseAuthorityStateLegacyQuarantined,
	}

	event, recorder := stackOperationsRouteTestEvent(http.MethodGet, "/api/v1/stacks/stack-quarantined/operations", "stack-quarantined", "owner-1")
	event.Request = event.Request.WithContext(identity.NewContext(event.Request.Context(), &identity.Identity{UserID: "owner-1", OrgID: "tenant-1"}))
	if err := (stackOperationsRouteHandlers{
		stackStore: store, serverStore: store, workerStore: store, registryStore: store,
		managedRuntimeLeases: fakeManagedRuntimeLeaseLister{inventory: []vmleases.LeaseInventoryRecord{record}},
	}).operations(event); err != nil {
		t.Fatalf("operations: %v", err)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	var envelope struct {
		Data stackOperationsPayload `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(envelope.Data.Servers) != 1 {
		t.Fatalf("servers = %#v, want one merged canonical server", envelope.Data.Servers)
	}
	server := envelope.Data.Servers[0]
	if server.Approved || server.Assignable || server.Status != managedRuntimeStatusQuarantined || server.Health.State != managedRuntimeStatusQuarantined || server.PreCheck != "blocked" {
		t.Fatalf("quarantined authority was bypassed by canonical row: %#v", server)
	}
	if envelope.Data.Readiness.Connected != 0 || envelope.Data.Readiness.CanStart {
		t.Fatalf("quarantined canonical server became rollout-ready: %#v", envelope.Data.Readiness)
	}
}

func TestStackOperationsFailsClosedWhenCanonicalServerEvidenceIsUnavailable(t *testing.T) {
	ctx := t.Context()
	store := controlplane.NewMemoryStore()
	if _, err := store.CreateStack(ctx, controlplane.CreateStackRequest{
		ID: "stack-evidence-error", TenantID: "tenant-1", OwnerSubjectID: "owner-1",
		Name: "Evidence error", Status: "configured",
	}); err != nil {
		t.Fatalf("CreateStack: %v", err)
	}
	event, recorder := stackOperationsRouteTestEvent(http.MethodGet, "/api/v1/stacks/stack-evidence-error/operations", "stack-evidence-error", "owner-1")
	event.Request = event.Request.WithContext(identity.NewContext(event.Request.Context(), &identity.Identity{UserID: "owner-1", OrgID: "tenant-1"}))
	if err := (stackOperationsRouteHandlers{
		stackStore:  store,
		serverStore: failingListServerRuntimeStore{ServerRuntimeStore: store, err: fmt.Errorf("canonical store unavailable")},
		workerStore: store, registryStore: store,
		managedRuntimeLeases: fakeManagedRuntimeLeaseLister{},
	}).operations(event); err != nil {
		t.Fatalf("operations: %v", err)
	}
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d body=%s, want 503", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "managed_runtime_authority_unavailable") {
		t.Fatalf("response does not expose retryable authority-unavailable outcome: %s", recorder.Body.String())
	}
}

// TestCanonicalOperationServerReportsPersistedStateWithoutPlatformDefaults
// locks the honest-state contract for the cockpit projection: the registry
// sweeper is the single demotion authority (#577) and writes stale/offline
// durably through ApplyServerEvent, so the read path publishes exactly what is
// persisted. Recomputing freshness here would make /api/v1/monitor/cockpit
// disagree with /api/v1/servers and the transitions log for the same server.
func TestCanonicalOperationServerReportsPersistedStateWithoutPlatformDefaults(t *testing.T) {
	store := controlplane.NewMemoryStore()
	lastSeen := time.Now().UTC().Add(-10 * time.Minute)
	// The sweeper has already demoted this row; the read path must publish it.
	if _, err := store.UpsertServerRuntime(t.Context(), controlplane.ServerRuntime{
		ID: "server-demoted", TenantID: "tenant-1", StackID: "stack-1", OwnerSubjectID: "owner-1",
		Name: "server-demoted", LifecycleState: "active", DesiredState: "active",
		ConnectionState: "offline", HealthState: "unknown", LastHeartbeatAt: &lastSeen,
	}); err != nil {
		t.Fatalf("UpsertServerRuntime: %v", err)
	}
	// The sweeper has not demoted this row yet. An old heartbeat must not be
	// turned into a read-time demotion that never reaches the aggregate head.
	if _, err := store.UpsertServerRuntime(t.Context(), controlplane.ServerRuntime{
		ID: "server-persisted-connected", TenantID: "tenant-1", StackID: "stack-1", OwnerSubjectID: "owner-1",
		Name: "server-persisted-connected", LifecycleState: "active", DesiredState: "active",
		ConnectionState: "connected", HealthState: "healthy", LastHeartbeatAt: &lastSeen,
	}); err != nil {
		t.Fatalf("UpsertServerRuntime: %v", err)
	}
	servers, err := (stackOperationsRouteHandlers{serverStore: store}).canonicalOperationServers(t.Context(), "owner-1", "tenant-1", "stack-1")
	if err != nil {
		t.Fatalf("canonicalOperationServers: %v", err)
	}
	if len(servers) != 2 {
		t.Fatalf("servers = %#v, want two", servers)
	}
	byID := map[string]stackOperationServer{}
	for _, server := range servers {
		byID[server.ID] = server
	}
	demoted, ok := byID["server-demoted"]
	if !ok {
		t.Fatalf("servers = %#v, want the demoted server", servers)
	}
	if demoted.Status != "offline" || demoted.Health.State != "offline" || demoted.Health.Source != "canonical-server" {
		t.Fatalf("persisted offline state must be published verbatim: %#v", demoted)
	}
	if demoted.heartbeatAt == nil || !demoted.heartbeatAt.Equal(lastSeen) {
		t.Fatalf("canonical readiness heartbeat = %v, want exact ServerRuntime.LastHeartbeatAt %s", demoted.heartbeatAt, lastSeen)
	}
	if demoted.OS != "" || demoted.OSVersion != "" || demoted.Arch != "" {
		t.Fatalf("missing inventory must not synthesize platform metadata: %#v", demoted)
	}
	connected, ok := byID["server-persisted-connected"]
	if !ok {
		t.Fatalf("servers = %#v, want the persisted-connected server", servers)
	}
	if connected.Status != "connected" || connected.Health.State != "healthy" {
		t.Fatalf("read path must not recompute freshness; the sweeper owns demotion: %#v", connected)
	}
}

func TestCanonicalOperationServersExcludeDecommissionedHistory(t *testing.T) {
	store := controlplane.NewMemoryStore()
	for _, runtime := range []controlplane.ServerRuntime{
		{
			ID: "server-active", TenantID: "tenant-1", StackID: "stack-1", OwnerSubjectID: "owner-1",
			Name: "server-active", LifecycleState: "active", DesiredState: "active",
			ConnectionState: "connected", HealthState: "healthy",
		},
		{
			ID: "server-ended", TenantID: "tenant-1", StackID: "stack-1", OwnerSubjectID: "owner-1",
			Name: "server-ended", LifecycleState: "decommissioned", DesiredState: "absent",
			ConnectionState: "revoked", HealthState: "unknown",
		},
	} {
		if _, err := store.UpsertServerRuntime(t.Context(), runtime); err != nil {
			t.Fatalf("UpsertServerRuntime(%s): %v", runtime.ID, err)
		}
	}

	servers, err := (stackOperationsRouteHandlers{serverStore: store}).canonicalOperationServers(t.Context(), "owner-1", "tenant-1", "stack-1")
	if err != nil {
		t.Fatalf("canonicalOperationServers: %v", err)
	}
	if len(servers) != 1 || servers[0].ID != "server-active" {
		t.Fatalf("servers = %#v, want only current actionable inventory", servers)
	}
}

func TestServerEndpointHealthRequiresFreshServerAndObservation(t *testing.T) {
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name       string
		server     stackOperationServer
		wantHealth string
	}{
		{
			name: "fresh observation on healthy server remains healthy",
			server: stackOperationServer{
				Health:           stackServerHealth{State: string(runtimehealth.ServerHealthy), UpdatedAt: now.Format(time.RFC3339Nano)},
				ServiceEndpoints: []stackServerEndpoint{{URL: "https://base.example.test", Health: serviceHealthHealthy, ObservedAt: now.Format(time.RFC3339Nano)}},
			},
			wantHealth: serviceHealthHealthy,
		},
		{
			name: "offline server invalidates old green endpoint",
			server: stackOperationServer{
				Health:           stackServerHealth{State: string(runtimehealth.ServerOffline), UpdatedAt: now.Format(time.RFC3339Nano)},
				ServiceEndpoints: []stackServerEndpoint{{URL: "https://base.example.test", Health: serviceHealthHealthy, ObservedAt: now.Format(time.RFC3339Nano)}},
			},
			wantHealth: monitoringStatusUnknown,
		},
		{
			name: "stale endpoint observation invalidates old green status",
			server: stackOperationServer{
				Health:           stackServerHealth{State: string(runtimehealth.ServerHealthy), UpdatedAt: now.Format(time.RFC3339Nano)},
				ServiceEndpoints: []stackServerEndpoint{{URL: "https://base.example.test", Health: serviceHealthHealthy, ObservedAt: now.Add(-runtimehealth.FreshHeartbeatWindow - time.Second).Format(time.RFC3339Nano)}},
			},
			wantHealth: monitoringStatusUnknown,
		},
		{
			name: "missing endpoint observation cannot borrow fresh server health",
			server: stackOperationServer{
				Health:           stackServerHealth{State: string(runtimehealth.ServerHealthy), UpdatedAt: now.Format(time.RFC3339Nano)},
				LastSeen:         now.Format(time.RFC3339Nano),
				ServiceEndpoints: []stackServerEndpoint{{URL: "https://base.example.test", Health: serviceHealthHealthy}},
			},
			wantHealth: monitoringStatusUnknown,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			applyServerEndpointFreshness(&test.server, now)
			if got := test.server.ServiceEndpoints[0].Health; got != test.wantHealth {
				t.Fatalf("endpoint health = %q, want %q", got, test.wantHealth)
			}
		})
	}
}

func metricValue(metric stackMetricValue) float64 {
	if metric.Value == nil {
		return -1
	}
	return *metric.Value
}

func TestCanonicalServerPreCheckStateDerivedFromRuntime(t *testing.T) {
	for name, tc := range map[string]struct {
		runtime controlplane.ServerRuntime
		want    string
	}{
		"managed lease":     {controlplane.ServerRuntime{LeaseID: "lease-1", LifecycleState: "enrolling"}, "managed"},
		"active enrollment": {controlplane.ServerRuntime{LifecycleState: "active"}, "passed"},
		"enrolling":         {controlplane.ServerRuntime{LifecycleState: "enrolling"}, "pending"},
		"failed":            {controlplane.ServerRuntime{LifecycleState: "failed"}, "failed"},
		"decommissioned":    {controlplane.ServerRuntime{LifecycleState: "decommissioned"}, "not_applicable"},
	} {
		if got := canonicalServerPreCheckState(tc.runtime); got != tc.want {
			t.Fatalf("%s: precheck = %q, want %q", name, got, tc.want)
		}
	}
}

func TestLatestStackFailureIgnoresNotApplicableBootstrapReceipt(t *testing.T) {
	// Mirrors the live 2026-07-29 demo failure: the rollout action failed, but
	// the persisted target_bootstrap receipt is a success record whose
	// not_applicable reason must not surface as the failure reason.
	failure := stackLatestFailureFromJob(controlplane.Job{
		ID:    "job-rollout-410",
		Type:  "deploy",
		State: "failed",
		Step:  "stackkit_rollout",
		Error: "StackKits rollout failed: runtime action stackkit_rollout returned 410: legacy_runtime_action_retired",
		Result: map[string]any{
			"target_bootstrap": map[string]any{
				"status":      "ready",
				"reason_code": "target_bootstrap_not_applicable",
				"message":     "StackKits has no governed host preparation for a canonical v2 StackSpec",
			},
			"runtime_diagnostics": map[string]any{
				"status": "collected",
				"reason": "runtime_action_failed",
				"action": "stackkit_rollout",
			},
		},
	})
	if failure == nil {
		t.Fatal("expected a latest failure projection")
	}
	if failure.Reason != "runtime_action_failed" {
		t.Fatalf("reason = %q, want runtime_action_failed (a success receipt must not mask the failure)", failure.Reason)
	}
	if strings.Contains(failure.Message, "no governed host preparation") {
		t.Fatalf("message %q leaked from the success bootstrap receipt", failure.Message)
	}
}

func TestStackOperationsStorePayloadIncludesLatestPostLeaseFailure(t *testing.T) {
	ctx := context.Background()
	store := controlplane.NewMemoryStore()
	if _, err := store.CreateStack(ctx, controlplane.CreateStackRequest{
		ID:             "stack-failed-prep",
		TenantID:       "tenant-1",
		OwnerSubjectID: "owner-1",
		Name:           "Failed Prep Stack",
		Status:         "provisioning",
	}); err != nil {
		t.Fatalf("CreateStack: %v", err)
	}
	if _, err := store.UpsertJob(ctx, controlplane.UpsertJobRequest{
		ID:           "job-failed-prep",
		TenantID:     "tenant-1",
		StackID:      "stack-failed-prep",
		Type:         "deploy",
		State:        "failed",
		Progress:     83,
		Step:         "stackkit_prepare",
		Message:      "Managed runtime target bootstrap failed",
		Error:        "context deadline exceeded",
		ErrorDetails: "phase=apt_wait status=begin",
		Result: map[string]any{
			"lease_id":          "lease-mwhrsh04v3hl2qo",
			"runtime_public_ip": "188.64.59.141",
			"runtime_phase":     "lease_ready",
			"target_bootstrap": map[string]any{
				"status":         "failed",
				"reason_code":    "target_bootstrap_timeout",
				"message":        "context deadline exceeded",
				"attempts":       1,
				"output_snippet": "phase=apt_wait status=begin\nRuntime diagnostics: token=<redacted>",
			},
			"runtime_diagnostics": map[string]any{
				"status":   "collected",
				"reason":   "target_bootstrap_timeout",
				"action":   "target_bootstrap",
				"commands": []any{map[string]any{"name": "cloud-init"}},
			},
		},
	}); err != nil {
		t.Fatalf("UpsertJob: %v", err)
	}
	lease := createStackOperationsTestLease("lease-mwhrsh04v3hl2qo", "tenant-1", "owner-1", "stack-failed-prep", "enrolled")
	lease.Metadata["public_ip"] = "188.64.59.141"

	event, recorder := stackOperationsRouteTestEvent(http.MethodGet, "/api/v1/stacks/stack-failed-prep/operations", "stack-failed-prep", "owner-1")
	event.Request = event.Request.WithContext(identity.NewContext(event.Request.Context(), &identity.Identity{UserID: "owner-1", OrgID: "tenant-1"}))
	if err := (stackOperationsRouteHandlers{
		stackStore:           store,
		workerStore:          store,
		registryStore:        store,
		jobStore:             store,
		managedRuntimeLeases: fakeManagedRuntimeLeaseLister{leases: []vmlease.Lease{lease}},
	}).operations(event); err != nil {
		t.Fatalf("operations returned router error: %v", err)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s, want 200", recorder.Code, recorder.Body.String())
	}

	var envelope struct {
		Data stackOperationsPayload `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode response: %v body=%s", err, recorder.Body.String())
	}
	if envelope.Data.LatestFailure == nil {
		t.Fatalf("latestFailure missing from operations payload: %#v", envelope.Data)
	}
	if envelope.Data.LatestFailure.JobID != "job-failed-prep" ||
		envelope.Data.LatestFailure.Step != "stackkit_prepare" ||
		envelope.Data.LatestFailure.Reason != "target_bootstrap_timeout" ||
		!envelope.Data.LatestFailure.DiagnosticsAvailable {
		t.Fatalf("unexpected latest failure: %#v", envelope.Data.LatestFailure)
	}
	if got := envelope.Data.LatestFailure.TargetBootstrap["output_hint"]; !strings.Contains(fmt.Sprint(got), "phase=apt_wait") {
		t.Fatalf("target bootstrap output hint missing apt_wait: %#v", envelope.Data.LatestFailure.TargetBootstrap)
	}
	if got := len(envelope.Data.Servers); got != 1 {
		t.Fatalf("servers = %d, want managed runtime projection", got)
	}
	server := envelope.Data.Servers[0]
	if server.Capabilities["last_job_id"] != "job-failed-prep" ||
		server.Capabilities["failure_reason"] != "target_bootstrap_timeout" ||
		server.Capabilities["diagnostics_available"] != true {
		t.Fatalf("managed server missing failure annotations: %#v", server)
	}
}

// A newer job of a DIFFERENT type is not a retry of the failed one. Suppressing
// the failure in that case left the dashboard in a dead end: the stack stayed
// in status error while reporting no failure, so the only message it could
// render was "the last operation failed, open the latest job" — pointing at the
// unrelated job that had succeeded.
func TestLatestStackFailureSurvivesNewerUnrelatedJob(t *testing.T) {
	now := time.Now().UTC()
	jobs := []controlplane.Job{
		{
			ID:        "job-register-server",
			Type:      "update",
			State:     "completed",
			Message:   "Server registration prepared",
			CreatedAt: now,
			UpdatedAt: now,
		},
		{
			ID:        "job-rollout",
			Type:      "deploy",
			State:     "failed",
			Error:     "StackKits artifact generation failed",
			CreatedAt: now.Add(-72 * time.Hour),
			UpdatedAt: now.Add(-72 * time.Hour),
		},
	}

	got := latestStackFailureFromJobs(jobs)
	if got == nil || got.JobID != "job-rollout" || got.Type != "deploy" {
		t.Fatalf("latest failure = %#v, want the still-current deploy failure", got)
	}

	// A newer attempt at the SAME operation does supersede it.
	jobs[0].Type = "deploy"
	if superseded := latestStackFailureFromJobs(jobs); superseded != nil {
		t.Fatalf("latest failure = %#v, want nil once a newer deploy owns the lifecycle", superseded)
	}
}

func TestLatestStackFailureDoesNotLeakAcrossNewerAttempt(t *testing.T) {
	now := time.Now().UTC()
	jobs := []controlplane.Job{
		{
			ID:        "job-current",
			State:     "waiting",
			CreatedAt: now,
			UpdatedAt: now,
		},
		{
			ID:        "job-stale-failure",
			State:     "failed",
			Error:     "SQLSTATE 42501",
			CreatedAt: now.Add(-time.Minute),
			UpdatedAt: now.Add(-time.Minute),
		},
	}

	if got := latestStackFailureFromJobs(jobs); got != nil {
		t.Fatalf("latest failure = %#v, want nil while newer attempt owns the lifecycle", got)
	}

	jobs[0].State = "failed"
	jobs[0].Error = "provider.partial_create"
	got := latestStackFailureFromJobs(jobs)
	if got == nil || got.JobID != "job-current" || got.Reason != "provider.partial_create" {
		t.Fatalf("latest failure = %#v, want current failed attempt", got)
	}
}

func TestStackOperationsFallsBackToPocketBaseWithoutTenantContext(t *testing.T) {
	app, err := tests.NewTestApp(driftRoutePocketBaseTestDataDir(t))
	if err != nil {
		t.Fatalf("new test app: %v", err)
	}
	defer app.Cleanup()
	ensureStackOperationsTestCollections(t, app)
	stack := createStackOperationsTestStack(t, app, "owner-1", "provisioning")
	stack.Set("server_provisioning_mode", "kombify-cloud")
	stack.Set("server_mode", "monthly-runtime")
	stack.Set("runtime_lane", "monthly-runtime")
	stack.Set("runtime_phase", "lease_ready")
	stack.Set("lease_id", "lease-pocketbase-retained")
	if err := app.Save(stack); err != nil {
		t.Fatalf("save stack runtime fields: %v", err)
	}

	store := controlplane.NewMemoryStore()
	lease := createStackOperationsTestLease("lease-pocketbase-retained", "owner-1", "owner-1", stack.Id, "enrolled")
	lease.Metadata["public_ip"] = "203.0.113.42"
	event, recorder := stackOperationsRouteTestEvent(http.MethodGet, "/api/v1/stacks/"+stack.Id+"/operations", stack.Id, "owner-1")

	if err := (stackOperationsRouteHandlers{
		app:                  app,
		stackStore:           store,
		managedRuntimeLeases: fakeManagedRuntimeLeaseLister{leases: []vmlease.Lease{lease}},
	}).operations(event); err != nil {
		t.Fatalf("operations returned router error: %v", err)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s, want 200", recorder.Code, recorder.Body.String())
	}

	var envelope struct {
		Data stackOperationsPayload `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode response: %v body=%s", err, recorder.Body.String())
	}
	if got := envelope.Data.Stack["id"]; got != stack.Id {
		t.Fatalf("stack id = %v, want %s", got, stack.Id)
	}
	if got := len(envelope.Data.Servers); got != 1 {
		t.Fatalf("servers = %d, want managed runtime projection", got)
	}
	server := envelope.Data.Servers[0]
	if server.Source != managedRuntimeInventorySource || server.LeaseID != "lease-pocketbase-retained" || server.IP != "203.0.113.42" {
		t.Fatalf("unexpected managed server projection: %#v", server)
	}
}

func TestStackServerDetailsUsesControlPlaneStores(t *testing.T) {
	ctx := context.Background()
	store := controlplane.NewMemoryStore()
	if _, err := store.CreateStack(ctx, controlplane.CreateStackRequest{
		ID:             "stack-store",
		TenantID:       "tenant-1",
		OwnerSubjectID: "owner-1",
		Name:           "Store Stack",
		Mode:           "easy",
		Status:         "running",
	}); err != nil {
		t.Fatalf("CreateStack: %v", err)
	}
	if _, err := store.UpsertNode(ctx, controlplane.Node{
		ID:       "node-1",
		TenantID: "tenant-1",
		StackID:  "stack-store",
		WorkerID: "worker-1",
		Name:     "main",
		Role:     "main",
		Status:   "running",
		Address:  "techstack-local-runtime",
		Metadata: map[string]any{
			"source":    "stackkit_outputs",
			"cpu_cores": 8,
			"ram_mb":    8192,
			"disk_gb":   100,
		},
	}); err != nil {
		t.Fatalf("UpsertNode: %v", err)
	}
	if _, err := store.UpsertService(ctx, controlplane.Service{
		ID:         "service-pocket-id",
		TenantID:   "tenant-1",
		StackID:    "stack-store",
		NodeID:     "node-1",
		ServiceKey: "pocket_id",
		Name:       "pocket_id",
		Status:     "running",
		Source:     "stackkit_outputs",
		URL:        "http://id.home.localhost",
		Metadata: map[string]any{
			"display_name": "Pocket ID",
			"type":         "identity",
			"port":         80,
		},
	}); err != nil {
		t.Fatalf("UpsertService: %v", err)
	}

	event, recorder := stackOperationsRouteTestEvent(http.MethodGet, "/api/v1/stacks/stack-store/servers/node-1", "stack-store", "owner-1")
	event.Request.SetPathValue("serverId", "node-1")
	event.Request = event.Request.WithContext(identity.NewContext(event.Request.Context(), &identity.Identity{UserID: "owner-1", OrgID: "tenant-1"}))
	if err := (stackOperationsRouteHandlers{
		stackStore:           store,
		registryStore:        store,
		managedRuntimeLeases: fakeManagedRuntimeLeaseLister{},
	}).serverDetails(event); err != nil {
		t.Fatalf("serverDetails returned router error: %v", err)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s, want 200", recorder.Code, recorder.Body.String())
	}

	var envelope struct {
		Data stackServerDetailsPayload `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode response: %v body=%s", err, recorder.Body.String())
	}
	if envelope.Data.Stack["id"] != "stack-store" {
		t.Fatalf("stack id = %#v, want stack-store", envelope.Data.Stack["id"])
	}
	if envelope.Data.Server.ID != "node-1" || envelope.Data.Server.Source != "registry-store" {
		t.Fatalf("unexpected server details: %#v", envelope.Data.Server)
	}
	if got := len(envelope.Data.Services); got != 1 {
		t.Fatalf("services = %d, want 1", got)
	}
	service := envelope.Data.Services[0]
	if service.Name != "pocket_id" || service.DisplayName != "Pocket ID" || service.Port != 80 || service.Status != registryUnknownStatus || service.URL != "" {
		t.Fatalf("unexpected service projection: %#v", service)
	}
}

func TestStackOperationsLegacyPathUsesRegistryStoreProjection(t *testing.T) {
	app, err := tests.NewTestApp(driftRoutePocketBaseTestDataDir(t))
	if err != nil {
		t.Fatalf("new test app: %v", err)
	}
	defer app.Cleanup()
	ensureStackOperationsTestCollections(t, app)

	ctx := context.Background()
	store := controlplane.NewMemoryStore()
	stack := createStackOperationsTestStack(t, app, "owner-1", "provisioning")
	stack.Set("tenant_id", "tenant-1")
	stack.Set("name", "Legacy Store Stack")
	stack.Set("server_provisioning_mode", "kombify-cloud")
	if err := app.Save(stack); err != nil {
		t.Fatalf("save stack tenant: %v", err)
	}
	if _, err := store.UpsertNode(ctx, controlplane.Node{
		ID:       "node-legacy-store",
		TenantID: "tenant-1",
		StackID:  stack.Id,
		Name:     "legacy-store-node",
		Role:     "main",
		Status:   "online",
		Address:  "techstack-local-runtime",
		Metadata: map[string]any{
			"source":                 "stackkit_outputs",
			"cpu_cores":              10,
			"ram_mb":                 4096,
			"disk_gb":                10,
			"runtime_cpu_percent":    "1.5",
			"runtime_memory_percent": "12.5",
			"runtime_disk_percent":   "6.5",
		},
	}); err != nil {
		t.Fatalf("UpsertNode: %v", err)
	}
	if _, err := store.UpsertService(ctx, controlplane.Service{
		ID:         "service-home",
		TenantID:   "tenant-1",
		StackID:    stack.Id,
		NodeID:     "node-legacy-store",
		ServiceKey: "homepage",
		Name:       "homepage",
		Status:     "running",
		Source:     "stackkit_outputs",
		URL:        "http://home.home.localhost",
		Metadata:   map[string]any{"display_name": "Base Hub", "type": "custom"},
	}); err != nil {
		t.Fatalf("UpsertService: %v", err)
	}

	event, recorder := stackOperationsRouteTestEvent(http.MethodGet, "/api/v1/stacks/"+stack.Id+"/operations", stack.Id, "owner-1")
	event.Request = event.Request.WithContext(identity.NewContext(event.Request.Context(), &identity.Identity{UserID: "owner-1", OrgID: "tenant-1"}))
	if err := (stackOperationsRouteHandlers{app: app, registryStore: store, managedRuntimeLeases: fakeManagedRuntimeLeaseLister{}}).operations(event); err != nil {
		t.Fatalf("operations returned router error: %v", err)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s, want 200", recorder.Code, recorder.Body.String())
	}

	var envelope struct {
		Data stackOperationsPayload `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode response: %v body=%s", err, recorder.Body.String())
	}
	if got := len(envelope.Data.Servers); got != 1 {
		t.Fatalf("servers = %d, want registry store node", got)
	}
	if server := envelope.Data.Servers[0]; server.ID != "node-legacy-store" || server.Assignment != "stack" || server.Health.State != "provisioned" {
		t.Fatalf("unexpected server projection: %#v", server)
	}
	if got := len(envelope.Data.Services); got != 1 {
		t.Fatalf("services = %d, want registry store service", got)
	}
	if envelope.Data.KPIs.RunningServices != 0 || envelope.Data.KPIs.HealthyServers != 0 {
		t.Fatalf("unexpected KPIs: %#v", envelope.Data.KPIs)
	}
}

func TestRegistryServicesUsesControlPlaneStoreResponse(t *testing.T) {
	ctx := context.Background()
	store := controlplane.NewMemoryStore()
	if _, err := store.CreateStack(ctx, controlplane.CreateStackRequest{
		ID:             "stack-registry",
		TenantID:       "tenant-1",
		OwnerSubjectID: "owner-1",
		Name:           "Registry Stack",
		Status:         "running",
	}); err != nil {
		t.Fatalf("CreateStack: %v", err)
	}
	if _, err := store.UpsertNode(ctx, controlplane.Node{
		ID:       "node-1",
		TenantID: "tenant-1",
		StackID:  "stack-registry",
		WorkerID: "worker-1",
		Name:     "main",
		Role:     "main",
	}); err != nil {
		t.Fatalf("UpsertNode: %v", err)
	}
	if _, err := store.UpsertService(ctx, controlplane.Service{
		ID:         "svc-1",
		TenantID:   "tenant-1",
		StackID:    "stack-registry",
		NodeID:     "node-1",
		ServiceKey: "uptime-kuma",
		Name:       "uptime-kuma",
		Status:     "running",
		Source:     "managed",
		URL:        "http://kuma.home.localhost",
		Metadata:   map[string]any{"type": "monitoring"},
	}); err != nil {
		t.Fatalf("UpsertService: %v", err)
	}

	event, recorder := stackOperationsRouteTestEvent(http.MethodGet, "/api/v1/registry/services", "", "owner-1")
	event.Request = event.Request.WithContext(identity.NewContext(event.Request.Context(), &identity.Identity{UserID: "owner-1", OrgID: "tenant-1"}))
	if err := (registryRouteHandlers{stackStore: store, registryStore: store}).services(event); err != nil {
		t.Fatalf("services returned router error: %v", err)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s, want 200", recorder.Code, recorder.Body.String())
	}

	var envelope struct {
		Data registryPayload `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode registry response: %v body=%s", err, recorder.Body.String())
	}
	if got := len(envelope.Data.Stacks); got != 1 {
		t.Fatalf("stacks = %d, want 1", got)
	}
	if got := len(envelope.Data.Services); got != 1 {
		t.Fatalf("services = %d, want 1", got)
	}
	if service := envelope.Data.Services[0]; service.Name != "uptime_kuma" || service.Type != "monitoring" {
		t.Fatalf("unexpected registry service: %#v", service)
	}
}

func TestStackServerDetailsFiltersServicesAndLogsToServer(t *testing.T) {
	app, err := tests.NewTestApp(driftRoutePocketBaseTestDataDir(t))
	if err != nil {
		t.Fatalf("new test app: %v", err)
	}
	defer app.Cleanup()
	ensureStackOperationsTestCollections(t, app)

	stack := createStackOperationsTestStack(t, app, "owner-1", "pending")
	serverA := createStackOperationsTestWorker(t, app, "owner-1", stack.Id, "node-a", true)
	serverB := createStackOperationsTestWorker(t, app, "owner-1", stack.Id, "node-b", true)
	nodeA := createStackOperationsTestNode(t, app, stack.Id, "node-a")
	nodeB := createStackOperationsTestNode(t, app, stack.Id, "node-b")
	createStackOperationsTestService(t, app, nodeA.Id, "traefik")
	createStackOperationsTestService(t, app, nodeB.Id, "immich")
	createStackOperationsTestActivity(t, app, stack.Id, "service_started", map[string]any{"worker_id": serverA.Id})
	createStackOperationsTestActivity(t, app, stack.Id, "service_stopped", map[string]any{"worker_id": serverB.Id})
	createStackOperationsTestActivity(t, app, stack.Id, "provision_started", map[string]any{})

	event, recorder := stackOperationsRouteTestEvent(http.MethodGet, "/api/v1/stacks/"+stack.Id+"/servers/"+serverA.Id, stack.Id, "owner-1")
	event.Request.SetPathValue("serverId", serverA.Id)
	if err := (stackOperationsRouteHandlers{app: app, managedRuntimeLeases: fakeManagedRuntimeLeaseLister{}}).serverDetails(event); err != nil {
		t.Fatalf("serverDetails returned router error: %v", err)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s, want 200", recorder.Code, recorder.Body.String())
	}

	var envelope struct {
		Data stackServerDetailsPayload `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got := len(envelope.Data.Services); got != 1 {
		t.Fatalf("services = %d, want 1", got)
	}
	if got := envelope.Data.Services[0].Name; got != "traefik" {
		t.Fatalf("service name = %q, want traefik", got)
	}
	if got := len(envelope.Data.Logs); got != 1 {
		t.Fatalf("logs = %d, want only server metadata-matched log", got)
	}
	if _, ok := envelope.Data.Logs[0]["worker_id"]; ok {
		t.Fatal("server logs must not synthesize worker_id")
	}
}

func TestStackScopedAlertsSeparateUnscopedAlerts(t *testing.T) {
	servers := []stackOperationServer{
		{ID: "worker-1", Hostname: "node-a", AgentID: "agent-a", Assignment: "stack"},
		{ID: "worker-2", Hostname: "node-b", AgentID: "agent-b", Assignment: "unassigned"},
	}
	now := time.Now()
	alerts, unscoped := stackScopedAlertsFromStates([]monitoring.AlertState{
		{
			Rule:     monitoring.AlertRule{Name: "StackCPU", Severity: "warning", Message: "high", Labels: map[string]string{"agent_id": "agent-a"}},
			Active:   true,
			Value:    99,
			FiredAt:  &now,
			LastEval: now,
		},
		{
			Rule:     monitoring.AlertRule{Name: "OtherCPU", Severity: "warning", Message: "high", Labels: map[string]string{"agent_id": "agent-b"}},
			Active:   true,
			Value:    88,
			FiredAt:  &now,
			LastEval: now,
		},
		{
			Rule:     monitoring.AlertRule{Name: "GlobalDisk", Severity: "warning", Message: "disk"},
			Active:   true,
			Value:    77,
			FiredAt:  &now,
			LastEval: now,
		},
	}, "stack-1", servers)

	if got := len(alerts); got != 1 {
		t.Fatalf("scoped alerts = %d, want 1", got)
	}
	if alerts[0].Name != "StackCPU" {
		t.Fatalf("scoped alert = %q, want StackCPU", alerts[0].Name)
	}
	if unscoped != 1 {
		t.Fatalf("unscoped alerts = %d, want global count only", unscoped)
	}
}

func ensureStackOperationsTestCollections(t *testing.T, app core.App) {
	t.Helper()
	ensureDriftRouteTestCollection(t, app, "stacks",
		&core.TextField{Name: "name"},
		&core.TextField{Name: "owner_id"},
		&core.TextField{Name: "tenant_id"},
		&core.SelectField{Name: "status", Values: []string{"pending", "provisioning", "running", "stopped", "error"}},
		&core.TextField{Name: "runtime_phase"},
		&core.TextField{Name: "server_mode"},
		&core.TextField{Name: "runtime_lane"},
		&core.TextField{Name: "lease_id"},
		&core.TextField{Name: "verification_status"},
		&core.TextField{Name: "server_provisioning_mode"},
		&core.JSONField{Name: "user_config"},
		&core.AutodateField{Name: "created", OnCreate: true},
		&core.AutodateField{Name: "updated", OnUpdate: true},
	)
	ensureWorkerRouteTestCollections(t, app)
	ensureDriftRouteTestCollection(t, app, "nodes",
		&core.TextField{Name: "name"},
		&core.TextField{Name: "hostname"},
		&core.TextField{Name: "stack_id"},
	)
	ensureDriftRouteTestCollection(t, app, "services",
		&core.TextField{Name: "name"},
		&core.TextField{Name: "display_name"},
		&core.TextField{Name: "type"},
		&core.TextField{Name: "status"},
		&core.TextField{Name: "node_id"},
		&core.TextField{Name: "url"},
		&core.NumberField{Name: "port"},
	)
	ensureDriftRouteTestCollection(t, app, preCheckResultsCollection,
		&core.TextField{Name: preCheckWorkerIDField},
		&core.TextField{Name: preCheckStackIDField},
		&core.TextField{Name: preCheckTypeField},
		&core.BoolField{Name: preCheckBlockingField},
		&core.TextField{Name: preCheckStatusField},
		&core.TextField{Name: preCheckOwnerIDField},
	)
	ensureDriftRouteTestCollection(t, app, "activity_log",
		&core.TextField{Name: "action"},
		&core.TextField{Name: "details"},
		&core.TextField{Name: "stack_id"},
		&core.TextField{Name: "status"},
		&core.JSONField{Name: "metadata"},
		&core.AutodateField{Name: "created", OnCreate: true},
	)
	ensureDriftRouteTestCollection(t, app, "jobs",
		&core.TextField{Name: "type"},
		&core.TextField{Name: "state"},
		&core.NumberField{Name: "progress"},
		&core.TextField{Name: "step"},
		&core.TextField{Name: "current_step"},
		&core.TextField{Name: "message"},
		&core.TextField{Name: "stack_id"},
		&core.JSONField{Name: "result"},
		&core.AutodateField{Name: "created", OnCreate: true},
		&core.AutodateField{Name: "updated", OnUpdate: true},
	)
}

func createStackOperationsTestStack(t *testing.T, app core.App, ownerID, status string) *core.Record {
	t.Helper()
	collection, err := app.FindCollectionByNameOrId("stacks")
	if err != nil {
		t.Fatalf("find stacks: %v", err)
	}
	record := core.NewRecord(collection)
	record.Set("name", "Ops Stack")
	record.Set("owner_id", ownerID)
	record.Set("status", status)
	record.Set("user_config", map[string]any{
		"services": []string{"pocket_id", "traefik", "monitoring", "vaultwarden", "immich"},
	})
	if err := app.Save(record); err != nil {
		t.Fatalf("save stack: %v", err)
	}
	return record
}

func createStackOperationsTestWorker(t *testing.T, app core.App, ownerID, stackID, hostname string, approved bool) *core.Record {
	t.Helper()
	collection, err := app.FindCollectionByNameOrId("workers")
	if err != nil {
		t.Fatalf("find workers: %v", err)
	}
	record := core.NewRecord(collection)
	record.Set("owner_id", ownerID)
	record.Set("stack_id", stackID)
	record.Set("hostname", hostname)
	record.Set("token_hash", hostname+"-token")
	record.Set("status", "approved")
	record.Set("approved", approved)
	record.Set("type", "worker")
	record.Set("os", "linux")
	record.Set("arch", "amd64")
	record.Set("last_seen", time.Now())
	if err := app.Save(record); err != nil {
		t.Fatalf("save worker: %v", err)
	}
	return record
}

func createStackOperationsTestNode(t *testing.T, app core.App, stackID, hostname string) *core.Record {
	t.Helper()
	collection, err := app.FindCollectionByNameOrId("nodes")
	if err != nil {
		t.Fatalf("find nodes: %v", err)
	}
	record := core.NewRecord(collection)
	record.Set("name", hostname)
	record.Set("hostname", hostname)
	record.Set("stack_id", stackID)
	if err := app.Save(record); err != nil {
		t.Fatalf("save node: %v", err)
	}
	return record
}

func createStackOperationsTestService(t *testing.T, app core.App, nodeID, name string) *core.Record {
	t.Helper()
	collection, err := app.FindCollectionByNameOrId("services")
	if err != nil {
		t.Fatalf("find services: %v", err)
	}
	record := core.NewRecord(collection)
	record.Set("name", name)
	record.Set("display_name", name)
	record.Set("type", "service")
	record.Set("status", "running")
	record.Set("node_id", nodeID)
	if err := app.Save(record); err != nil {
		t.Fatalf("save service: %v", err)
	}
	return record
}

func createStackOperationsTestActivity(t *testing.T, app core.App, stackID, action string, metadata map[string]any) *core.Record {
	t.Helper()
	collection, err := app.FindCollectionByNameOrId("activity_log")
	if err != nil {
		t.Fatalf("find activity_log: %v", err)
	}
	record := core.NewRecord(collection)
	record.Set("action", action)
	record.Set("details", action)
	record.Set("stack_id", stackID)
	record.Set("status", "completed")
	record.Set("metadata", metadata)
	if err := app.Save(record); err != nil {
		t.Fatalf("save activity: %v", err)
	}
	return record
}

func createStackOperationsTestJob(t *testing.T, app core.App, stackID, step, state string) *core.Record {
	t.Helper()
	collection, err := app.FindCollectionByNameOrId("jobs")
	if err != nil {
		t.Fatalf("find jobs: %v", err)
	}
	record := core.NewRecord(collection)
	record.Set("type", "update")
	record.Set("state", state)
	record.Set("progress", 100)
	record.Set("step", step)
	record.Set("current_step", step)
	record.Set("message", step)
	record.Set("stack_id", stackID)
	if err := app.Save(record); err != nil {
		t.Fatalf("save job: %v", err)
	}
	return record
}

func createStackOperationsTestDeployJob(t *testing.T, app core.App, stackID string, outputs map[string]any) *core.Record {
	t.Helper()
	collection, err := app.FindCollectionByNameOrId("jobs")
	if err != nil {
		t.Fatalf("find jobs: %v", err)
	}
	record := core.NewRecord(collection)
	record.Set("type", "deploy")
	record.Set("state", "completed")
	record.Set("progress", 100)
	record.Set("step", "finalize")
	record.Set("current_step", "finalize")
	record.Set("message", "StackKit rollout completed")
	record.Set("stack_id", stackID)
	record.Set("result", map[string]any{
		"stackkit_outputs": outputs,
	})
	if err := app.Save(record); err != nil {
		t.Fatalf("save deploy job: %v", err)
	}
	return record
}

func stackOperationsRouteTestEvent(method, target, stackID, ownerID string) (*httpx.Event, *httptest.ResponseRecorder) {
	req := httptest.NewRequest(method, target, nil)
	req.SetPathValue("id", stackID)
	if ownerID != "" {
		req = req.WithContext(identity.NewContext(context.Background(), &identity.Identity{UserID: ownerID}))
	}
	rec := httptest.NewRecorder()
	return &httpx.Event{Request: req, Response: rec}, rec
}

type fakeManagedRuntimeLeaseLister struct {
	leases    []vmlease.Lease
	inventory []vmleases.LeaseInventoryRecord
}

type failingListServerRuntimeStore struct {
	controlplane.ServerRuntimeStore
	err error
}

func (s failingListServerRuntimeStore) ListServerRuntimesByTenant(context.Context, string, string) ([]controlplane.ServerRuntime, error) {
	return nil, s.err
}

func (f fakeManagedRuntimeLeaseLister) ListByTenant(_ context.Context, _ string) ([]vmlease.Lease, error) {
	out := make([]vmlease.Lease, len(f.leases))
	copy(out, f.leases)
	return out, nil
}

func (f fakeManagedRuntimeLeaseLister) ListInventoryByTenant(_ context.Context, _ string) ([]vmleases.LeaseInventoryRecord, error) {
	if f.inventory != nil {
		return append([]vmleases.LeaseInventoryRecord(nil), f.inventory...), nil
	}
	records := make([]vmleases.LeaseInventoryRecord, 0, len(f.leases))
	for _, lease := range f.leases {
		record := nativeActiveManagedRuntimeRecord(lease)
		if lease.CancelledAt != nil || lease.DesiredState != vmlease.DesiredStateRunning {
			record.AuthorityState = vmleases.LeaseAuthorityStateNativeInactive
		}
		records = append(records, record)
	}
	return records, nil
}

func nativeActiveManagedRuntimeRecord(lease vmlease.Lease) vmleases.LeaseInventoryRecord {
	return vmleases.LeaseInventoryRecord{
		Lease:              lease,
		ExecutionAuthority: vmleases.LeaseExecutionAuthorityTechStackProviderControl,
		AuthorityState:     vmleases.LeaseAuthorityStateNativeActive,
	}
}

func createStackOperationsTestLease(id, tenantID, ownerID, stackID, enrollment string) vmlease.Lease {
	now := time.Now().UTC()
	return vmlease.Lease{
		ID:      vmlease.LeaseID(id),
		Subject: vmlease.Subject{Kind: vmlease.SubjectUser, ID: ownerID, OrgID: tenantID},
		Resource: vmlease.ResourceRef{
			ProviderID: "centron-managed",
			Region:     "de-fra",
		},
		DesiredState:   vmlease.DesiredStateRunning,
		BillingMode:    vmlease.BillingModeSubscription,
		LifecycleClass: vmlease.LifecycleClassSubscription,
		RestartPolicy:  vmlease.RestartPolicyOnUnexpectedStop,
		RecreatePolicy: vmlease.RecreatePolicyManual,
		ValidFrom:      now.Add(-time.Minute),
		ValidUntil:     now.Add(30 * 24 * time.Hour),
		RenewedAt:      now,
		Metadata: map[string]string{
			"stack_id":                  stackID,
			"stack_name":                "Ops Stack",
			"runtime_lane":              serverruntime.RuntimeLaneMonthly,
			"server_mode":               serverruntime.RuntimeLaneMonthly,
			"runtime_offering_id":       string(serverruntime.RuntimeOfferingStandard),
			"runtime_enrollment_status": enrollment,
			"lease_provider":            "centron-managed",
			"public_ip":                 "203.0.113.10",
			"runtime_cpu_percent":       "12.5",
			"runtime_memory_percent":    "34.5",
			"runtime_disk_percent":      "56.5",
			"runtime_uptime_seconds":    "789",
		},
	}
}

// A lease is a custody and billing record, not proof that a machine exists.
// Live regression (2026-07-31, stack homelab): two quarantined leases whose
// VMs had been deleted at the provider rendered as two healthy-looking server
// cards with a stale address and full action buttons.
func TestStackOperationsKeepsLeasesWithoutMachinesOutOfServers(t *testing.T) {
	ctx := t.Context()
	store := controlplane.NewMemoryStore()
	if _, err := store.CreateStack(ctx, controlplane.CreateStackRequest{
		ID: "stack-ghost", TenantID: "tenant-1", OwnerSubjectID: "owner-1",
		Name: "Ghost", Status: "error",
	}); err != nil {
		t.Fatalf("CreateStack: %v", err)
	}
	// Two sibling leases of one stack, no canonical server row for either.
	first := createStackOperationsTestLease("lease-stack-ghost", "tenant-1", "owner-1", "stack-ghost", monthlyRuntimeEnrollmentStatusPending)
	first.Metadata["public_ip"] = "85.215.38.99"
	second := createStackOperationsTestLease("lease-stack-ghost-foundation-97268281", "tenant-1", "owner-1", "stack-ghost", monthlyRuntimeEnrollmentStatusPending)
	records := []vmleases.LeaseInventoryRecord{
		{Lease: first, ExecutionAuthority: vmleases.LeaseExecutionAuthorityLegacySimulate, AuthorityState: vmleases.LeaseAuthorityStateLegacyQuarantined},
		{Lease: second, ExecutionAuthority: vmleases.LeaseExecutionAuthorityLegacySimulate, AuthorityState: vmleases.LeaseAuthorityStateLegacyQuarantined},
	}

	event, recorder := stackOperationsRouteTestEvent(http.MethodGet, "/api/v1/stacks/stack-ghost/operations", "stack-ghost", "owner-1")
	event.Request = event.Request.WithContext(identity.NewContext(event.Request.Context(), &identity.Identity{UserID: "owner-1", OrgID: "tenant-1"}))
	if err := (stackOperationsRouteHandlers{
		stackStore: store, serverStore: store, workerStore: store, registryStore: store,
		managedRuntimeLeases: fakeManagedRuntimeLeaseLister{inventory: records},
	}).operations(event); err != nil {
		t.Fatalf("operations: %v", err)
	}
	var envelope struct {
		Data stackOperationsPayload `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(envelope.Data.Servers) != 0 {
		t.Fatalf("servers = %#v, want none: no machine backs either lease", envelope.Data.Servers)
	}
	if len(envelope.Data.CustodyLeases) != 2 {
		t.Fatalf("custodyLeases = %#v, want both leases reported as custody records", envelope.Data.CustodyLeases)
	}
	// The custody records stay distinguishable; the old 6-character lease-id
	// prefix collapsed sibling leases of one stack onto one label.
	if envelope.Data.CustodyLeases[0].Label == envelope.Data.CustodyLeases[1].Label {
		t.Fatalf("sibling leases share the label %q", envelope.Data.CustodyLeases[0].Label)
	}
	for _, custody := range envelope.Data.CustodyLeases {
		if custody.Reason != managedRuntimeAbsenceNoCustody {
			t.Fatalf("reason = %q, want %q", custody.Reason, managedRuntimeAbsenceNoCustody)
		}
		if len(custody.AllowedActions) != 1 || custody.AllowedActions[0] != "resolve_custody" {
			t.Fatalf("allowed actions = %#v, want resolve_custody", custody.AllowedActions)
		}
	}
	if envelope.Data.KPIs.RegisteredServers != 0 {
		t.Fatalf("registered servers = %d, want 0", envelope.Data.KPIs.RegisteredServers)
	}
}

func TestStackOperationsCustodyActionsFollowExecutionAuthority(t *testing.T) {
	lease := createStackOperationsTestLease("lease-enrollment-failed", "tenant-1", "owner-1", "stack-failed", monthlyRuntimeEnrollmentStatusFailed)
	item := managedRuntimeInventoryItemFromLease(vmleases.LeaseInventoryRecord{
		Lease:              lease,
		ExecutionAuthority: vmleases.LeaseExecutionAuthorityTechStackProviderControl,
		AuthorityState:     vmleases.LeaseAuthorityStateNativeActive,
	})
	backed, custody := splitManagedRuntimeLeasesByMachineEvidence([]managedRuntimeInventoryItem{item}, nil)
	if len(backed) != 0 || len(custody) != 1 {
		t.Fatalf("backed=%#v custody=%#v", backed, custody)
	}
	if custody[0].Reason != managedRuntimeAbsenceEnrollFailed || len(custody[0].AllowedActions) != 1 || custody[0].AllowedActions[0] != "decommission" {
		t.Fatalf("custody = %#v, want provider decommission action", custody[0])
	}

	lease.Metadata["custody_resolution_status"] = "resolved"
	item = managedRuntimeInventoryItemFromLease(vmleases.LeaseInventoryRecord{
		Lease: lease, AuthorityState: vmleases.LeaseAuthorityStateLegacyQuarantined,
	})
	backed, custody = splitManagedRuntimeLeasesByMachineEvidence([]managedRuntimeInventoryItem{item}, nil)
	if len(backed) != 0 || len(custody) != 0 {
		t.Fatalf("resolved custody remains active: backed=%#v custody=%#v", backed, custody)
	}
}

func TestStackOperationsDropsObsoleteDestroyFailureAfterCleanup(t *testing.T) {
	failure := &stackLatestFailure{Type: "destroy", State: "failed"}
	if got := activeStackFailure(failure, nil, nil); got != nil {
		t.Fatalf("activeStackFailure = %#v, want stale destroy failure removed", got)
	}
	if got := activeStackFailure(failure, nil, []stackCustodyLease{{LeaseID: "lease-1"}}); got != failure {
		t.Fatalf("activeStackFailure removed unresolved cleanup: %#v", got)
	}
	rollout := &stackLatestFailure{Type: "deploy", State: "failed"}
	if got := activeStackFailure(rollout, nil, nil); got != rollout {
		t.Fatalf("activeStackFailure removed unrelated failure: %#v", got)
	}

	stale := stackReadiness{Status: "error", Message: "The last operation failed."}
	got := reconcileResolvedDestroyReadiness(stale, failure, nil, nil, nil)
	if got.Status != "waiting_for_server" || got.CanStart || got.ReviewRequired ||
		!strings.Contains(got.Message, "local server") ||
		!strings.Contains(got.Message, "your own server or VPS") ||
		!strings.Contains(got.Message, "Managed VPS") {
		t.Fatalf("resolved destroy readiness = %#v, want neutral empty-runtime state", got)
	}
	if unresolved := reconcileResolvedDestroyReadiness(stale, failure, failure, nil, []stackCustodyLease{{LeaseID: "lease-1"}}); unresolved != stale {
		t.Fatalf("unresolved destroy readiness changed: %#v", unresolved)
	}
	if unrelated := reconcileResolvedDestroyReadiness(stale, rollout, rollout, nil, nil); unrelated != stale {
		t.Fatalf("unrelated readiness changed: %#v", unrelated)
	}
}

// A provider read that found the VM gone must remove the server, even while
// the lease is still native-active and wants to run.
func TestStackOperationsDropsServerWhenProviderReportsVMGone(t *testing.T) {
	lease := createStackOperationsTestLease("lease-gone", "tenant-1", "owner-1", "stack-gone", monthlyRuntimeEnrollmentStatusEnrolled)
	lease.Metadata["runtime_observed_state"] = managedRuntimeObservedStateNotFound
	item := managedRuntimeInventoryItemFromLease(vmleases.LeaseInventoryRecord{
		Lease:              lease,
		ExecutionAuthority: vmleases.LeaseExecutionAuthorityTechStackProviderControl,
		AuthorityState:     vmleases.LeaseAuthorityStateNativeActive,
	})
	backed, custody := splitManagedRuntimeLeasesByMachineEvidence([]managedRuntimeInventoryItem{item}, nil)
	if len(backed) != 0 || len(custody) != 1 {
		t.Fatalf("backed=%#v custody=%#v, want the lease reported as custody only", backed, custody)
	}
	if custody[0].Reason != managedRuntimeAbsenceProviderGone {
		t.Fatalf("reason = %q, want %q", custody[0].Reason, managedRuntimeAbsenceProviderGone)
	}

	// A live canonical server row is the strongest evidence and still wins.
	backed, custody = splitManagedRuntimeLeasesByMachineEvidence(
		[]managedRuntimeInventoryItem{item},
		[]stackOperationServer{{LeaseID: item.LeaseID, Capabilities: map[string]any{"lifecycle_state": "active"}}},
	)
	if len(backed) != 1 || len(custody) != 0 {
		t.Fatalf("backed=%#v custody=%#v, want the canonical server to keep the lease", backed, custody)
	}
}
