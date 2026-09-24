package routes

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
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
	"github.com/stretchr/testify/assert"
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
			"version":     stackKitRuntimeObservationV2,
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
	if len(stale) != 1 || stale[0].Status != "unknown" || stale[0].URL != "" {
		t.Fatalf("stale observation must not remain healthy or accessible: %#v", stale)
	}
}

func TestStackOperationsUsesDurableStoreForAuthenticatedFallbackTenant(t *testing.T) {
	stackID := "stack-owner-fallback"
	store := controlplane.NewMemoryStore()
	if _, err := store.CreateStack(t.Context(), controlplane.CreateStackRequest{
		ID: stackID, TenantID: "owner-1", OwnerSubjectID: "owner-1", Name: "Durable stack", Status: "provisioning",
	}); err != nil {
		t.Fatalf("CreateStack: %v", err)
	}
	if _, err := store.CreateJob(t.Context(), controlplane.UpsertJobRequest{
		ID: "job-durable-running", TenantID: "owner-1", StackID: stackID, Type: "provision", State: "pending",
		Progress: 42, Step: "provider_create", Message: "Creating server",
	}); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	if _, err := store.StartJob(t.Context(), "owner-1", "job-durable-running", time.Now().UTC()); err != nil {
		t.Fatalf("StartJob: %v", err)
	}

	router := httpx.NewRouter()
	RegisterStackOperationsRoutesWithStores(router, nil, MonitoringStatusMetadata{}, nil, nil, StackOperationsRouteStores{
		Stacks: store, Servers: store, Services: store, Workers: store, Registry: store, Jobs: store,
	}, fakeManagedRuntimeLeaseLister{})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/stacks/"+stackID+"/operations", nil)
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
	if _, err := store.UpsertServerRuntime(ctx, controlplane.ServerRuntime{
		ID: "storage-node", TenantID: "tenant-1", StackID: "stack-store-handoff", OwnerSubjectID: "owner-1",
		WorkerID: "storage-node", NodeID: "storage-node", Name: "storage-node", LifecycleState: "active",
		ConnectionState: "online", HealthState: "healthy", DesiredState: "running",
		Metadata: map[string]any{
			nodehandoff.KeyServerNodeRole: "storage", nodehandoff.KeyRequestedServices: []string{"files", "immich"},
			nodehandoff.KeyServerRemoteHost: "10.10.10.31",
		},
	}); err != nil {
		t.Fatalf("UpsertServerRuntime: %v", err)
	}

	event, recorder := stackOperationsRouteTestEvent(http.MethodGet, "/api/v1/stacks/stack-store-handoff/operations", "stack-store-handoff", "owner-1")
	event.Request = event.Request.WithContext(identity.NewContext(event.Request.Context(), &identity.Identity{UserID: "owner-1", OrgID: "tenant-1"}))
	if err := (stackOperationsRouteHandlers{stackStore: store, serverStore: store, serviceStore: store, workerStore: store, registryStore: store, jobStore: store, managedRuntimeLeases: fakeManagedRuntimeLeaseLister{}}).operations(event); err != nil {
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

func TestStackDashboardPathsFailClosedWithoutCanonicalStores(t *testing.T) {
	for _, test := range []struct {
		method, target string
		invoke         func(stackOperationsRouteHandlers, *httpx.Event) error
	}{
		{method: http.MethodGet, target: "/api/v1/monitor/cockpit", invoke: func(h stackOperationsRouteHandlers, e *httpx.Event) error { return h.monitorCockpit(e) }},
		{method: http.MethodGet, target: "/api/v1/stacks/stack-1/operations", invoke: func(h stackOperationsRouteHandlers, e *httpx.Event) error { return h.operations(e) }},
		{method: http.MethodGet, target: "/api/v1/stacks/stack-1/servers/server-1", invoke: func(h stackOperationsRouteHandlers, e *httpx.Event) error { return h.serverDetails(e) }},
		{method: http.MethodPost, target: "/api/v1/stacks/stack-1/workers/worker-1/assign", invoke: func(h stackOperationsRouteHandlers, e *httpx.Event) error { return h.assignWorker(e) }},
	} {
		t.Run(test.target, func(t *testing.T) {
			event, recorder := stackOperationsRouteTestEvent(test.method, test.target, "stack-1", "owner-1")
			event.Request.SetPathValue("serverId", "server-1")
			event.Request.SetPathValue("workerId", "worker-1")
			_ = test.invoke(stackOperationsRouteHandlers{}, event)
			assert.Equal(t, http.StatusServiceUnavailable, recorder.Code)
		})
	}
}

func TestMonitorCockpitAggregatesOneOwnersHomelab(t *testing.T) {
	ctx := t.Context()
	store := controlplane.NewMemoryStore()
	now := time.Now().UTC()
	for _, deployment := range []struct {
		id, owner, node, service string
	}{
		{id: "deployment-a", owner: "owner-1", node: "node-a", service: "vaultwarden"},
		{id: "deployment-b", owner: "owner-1", node: "node-b", service: "immich"},
		{id: "deployment-foreign", owner: "owner-2", node: "foreign-node", service: "foreign-service"},
	} {
		if _, err := store.CreateStack(ctx, controlplane.CreateStackRequest{
			ID: deployment.id, TenantID: "owner-1", OwnerSubjectID: deployment.owner,
			HomelabID: "homelab-" + deployment.owner, Name: deployment.id, Status: "running",
		}); err != nil {
			t.Fatalf("create StackKit deployment: %v", err)
		}
		if _, err := store.UpsertServerRuntime(ctx, controlplane.ServerRuntime{
			ID: deployment.node, TenantID: "owner-1", StackID: deployment.id, OwnerSubjectID: deployment.owner,
			Name: deployment.node, LifecycleState: "active", DesiredState: "active",
			ConnectionState: "connected", HealthState: "healthy", LastHeartbeatAt: &now,
		}); err != nil {
			t.Fatalf("create Node: %v", err)
		}
		if _, err := store.UpsertServiceRuntime(ctx, controlplane.ServiceRuntime{
			ID: deployment.service, TenantID: "owner-1", StackID: deployment.id, ServerID: deployment.node,
			ServiceKey: deployment.service, Name: deployment.service, DesiredState: "running",
			ObservedState: "running", HealthState: "healthy", ObservedAt: &now,
		}); err != nil {
			t.Fatalf("create service: %v", err)
		}
		if _, err := store.CreateJob(ctx, controlplane.UpsertJobRequest{
			ID: "job-" + deployment.id, TenantID: "owner-1", StackID: deployment.id,
			Type: "deploy", State: "completed", Progress: 100,
		}); err != nil {
			t.Fatalf("create deployment job: %v", err)
		}
	}

	event, recorder := stackOperationsRouteTestEvent(http.MethodGet, "/api/v1/monitor/cockpit", "", "owner-1")
	if err := (stackOperationsRouteHandlers{
		stackStore: store, serverStore: store, serviceStore: store, workerStore: store,
		registryStore: store, jobStore: store, managedRuntimeLeases: fakeManagedRuntimeLeaseLister{},
	}).monitorCockpit(event); err != nil {
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
	if envelope.Data.HomelabID != "homelab-owner-1" || envelope.Data.KitDeploymentCount != 2 {
		t.Fatalf("homelab scope = %#v, want two owned StackKit deployments", envelope.Data)
	}
	if got := len(envelope.Data.Servers); got != 2 {
		t.Fatalf("servers = %d, want both owned Nodes and no foreign Node", got)
	}
	if got := len(envelope.Data.Services); got != 2 {
		t.Fatalf("services = %d, want services from both owned deployments", got)
	}
	if got := len(envelope.Data.Jobs); got != 2 {
		t.Fatalf("jobs = %d, want jobs from both owned deployments", got)
	}
	if envelope.Data.KPIs.RegisteredServers != 2 || envelope.Data.KPIs.RunningServices != 2 {
		t.Fatalf("unexpected cockpit KPIs: %#v", envelope.Data.KPIs)
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

func TestStackDashboardPathsFailClosedWhenRuntimeAuthorityIsUnavailable(t *testing.T) {
	store := controlplane.NewMemoryStore()
	seedControlPlaneStack(t, store, "tenant-1", "owner-1", "stack-store")
	canonical := stackOperationsRouteHandlers{
		stackStore: store, serverStore: store, serviceStore: store, workerStore: store,
		registryStore: store, jobStore: store, activityStore: store,
		managedRuntimeLeases: fakeManagedRuntimeLeaseLister{},
	}
	failingLeases := canonical
	failingLeases.managedRuntimeLeases = failingManagedRuntimeLeaseLister{}
	failingServers := canonical
	failingServers.serverStore = failingListServerRuntimeStore{ServerRuntimeStore: store, err: fmt.Errorf("canonical store unavailable")}

	testCases := []struct {
		name, target, privateError string
		handler                    stackOperationsRouteHandlers
		invoke                     func(stackOperationsRouteHandlers, *httpx.Event) error
	}{
		{name: "operations lease authority", target: "/api/v1/stacks/stack-store/operations", privateError: "lease store unavailable", handler: failingLeases, invoke: func(h stackOperationsRouteHandlers, e *httpx.Event) error { return h.operations(e) }},
		{name: "cockpit lease authority", target: "/api/v1/monitor/cockpit?kit_deployment_id=stack-store", privateError: "lease store unavailable", handler: failingLeases, invoke: func(h stackOperationsRouteHandlers, e *httpx.Event) error { return h.monitorCockpit(e) }},
		{name: "server details lease authority", target: "/api/v1/stacks/stack-store/servers/server-1", privateError: "lease store unavailable", handler: failingLeases, invoke: func(h stackOperationsRouteHandlers, e *httpx.Event) error { return h.serverDetails(e) }},
		{name: "operations canonical server evidence", target: "/api/v1/stacks/stack-store/operations", privateError: "canonical store unavailable", handler: failingServers, invoke: func(h stackOperationsRouteHandlers, e *httpx.Event) error { return h.operations(e) }},
	}

	for _, test := range testCases {
		t.Run(test.name, func(t *testing.T) {
			event, recorder := stackOperationsRouteTestEvent(http.MethodGet, test.target, "stack-store", "owner-1")
			event.Request.SetPathValue("serverId", "server-1")
			event.Request = event.Request.WithContext(identity.NewContext(event.Request.Context(), &identity.Identity{UserID: "owner-1", OrgID: "tenant-1"}))
			if err := test.invoke(test.handler, event); err != nil {
				t.Fatalf("handler returned router error: %v", err)
			}
			details := decodeErrorDetails(t, recorder)
			if recorder.Code != http.StatusServiceUnavailable || details["reason_code"] != "managed_runtime_authority_unavailable" ||
				strings.Contains(recorder.Body.String(), test.privateError) {
				t.Fatalf("status=%d details=%#v, want redacted authority-unavailable response", recorder.Code, details)
			}
		})
	}
}

func TestStackOperationsDeduplicatesLeaseAndWorkerServerIdentity(t *testing.T) {
	for _, tc := range []struct {
		name, workerIP, wantIP, wantSource, wantProvider, wantStatus string
		canonical                                                    bool
	}{
		{name: "canonical server wins", workerIP: "198.51.100.9", wantIP: "203.0.113.10", wantSource: "canonical-server", wantProvider: "ionos-observed", wantStatus: "connected", canonical: true},
		{name: "worker observation wins before canonical backfill", workerIP: "203.0.113.10", wantIP: "203.0.113.10", wantSource: workerRegistryInventorySource, wantProvider: "worker-observed", wantStatus: "healthy"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			store := controlplane.NewMemoryStore()
			if _, err := store.CreateStack(ctx, controlplane.CreateStackRequest{ID: "stack-dedup", TenantID: "tenant-1", OwnerSubjectID: "owner-1", Name: "Dedup", Status: "running"}); err != nil {
				t.Fatalf("CreateStack: %v", err)
			}
			leaseID := "lease-ionos-1"
			serverID := runtimeidentity.LeaseServerID(leaseID)
			now := time.Now().UTC()
			if _, err := store.UpsertWorkerHeartbeat(ctx, controlplane.Worker{
				ID: "worker-observation", TenantID: "tenant-1", OwnerSubjectID: "owner-1", Hostname: "agent-observed",
				IP: tc.workerIP, Approved: true, LastSeenAt: &now, Provider: "worker-observed",
				Capabilities: map[string]any{"server_id": serverID, "lease_id": leaseID},
			}); err != nil {
				t.Fatalf("UpsertWorkerHeartbeat: %v", err)
			}
			if tc.canonical {
				if _, err := store.UpsertServerRuntime(ctx, controlplane.ServerRuntime{
					ID: serverID, TenantID: "tenant-1", StackID: "stack-dedup", OwnerSubjectID: "owner-1", WorkerID: "worker-observation", LeaseID: leaseID,
					Name: "agent-observed", ProviderRef: "ionos-observed", LifecycleState: "active", ConnectionState: "connected", HealthState: "healthy", LastHeartbeatAt: &now,
					Metadata: map[string]any{"host": map[string]any{"public_ip": tc.wantIP}},
				}); err != nil {
					t.Fatalf("UpsertServerRuntime: %v", err)
				}
			}
			lease := createStackOperationsTestLease(leaseID, "tenant-1", "owner-1", "stack-dedup", monthlyRuntimeEnrollmentStatusEnrolled)
			lease.Metadata["public_ip"] = "192.0.2.20"
			event, recorder := stackOperationsRouteTestEvent(http.MethodGet, "/api/v1/stacks/stack-dedup/operations", "stack-dedup", "owner-1")
			event.Request = event.Request.WithContext(identity.NewContext(event.Request.Context(), &identity.Identity{UserID: "owner-1", OrgID: "tenant-1"}))
			if err := (stackOperationsRouteHandlers{
				stackStore: store, serverStore: store, serviceStore: store, workerStore: store, registryStore: store, jobStore: store,
				managedRuntimeLeases: fakeManagedRuntimeLeaseLister{leases: []vmlease.Lease{lease}},
			}).operations(event); err != nil {
				t.Fatalf("operations: %v", err)
			}
			var envelope struct {
				Data stackOperationsPayload `json:"data"`
			}
			if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if len(envelope.Data.Servers) != 1 {
				t.Fatalf("server cards = %#v, want one deduplicated server", envelope.Data.Servers)
			}
			server := envelope.Data.Servers[0]
			if server.ServerID != serverID || server.LeaseID != leaseID || server.IP != tc.wantIP || server.Source != tc.wantSource || server.Status != tc.wantStatus || server.Capabilities["provider"] != tc.wantProvider {
				t.Fatalf("deduplicated server = %#v", server)
			}
		})
	}
}

func TestStackOperationsAssignsGuardConnectedControlPlaneWorker(t *testing.T) {
	store := controlplane.NewMemoryStore()
	now := time.Now().UTC()
	if _, err := store.CreateStack(context.Background(), controlplane.CreateStackRequest{
		ID:             "stack-1",
		TenantID:       "owner-1",
		OwnerSubjectID: "owner-1",
		Name:           "Ops Stack",
		Status:         "pending",
	}); err != nil {
		t.Fatalf("CreateStack: %v", err)
	}
	worker, err := store.UpsertWorkerHeartbeat(context.Background(), controlplane.Worker{
		ID:             "worker-1",
		TenantID:       "owner-1",
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
	assignErr := (stackOperationsRouteHandlers{stackStore: store, serverStore: store, workerStore: store}).assignWorker(event)
	if assignErr != nil {
		t.Fatalf("assign returned router error: %v", assignErr)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s, want 200", recorder.Code, recorder.Body.String())
	}
	updated, err := store.GetWorker(context.Background(), "owner-1", "worker-1")
	if err != nil {
		t.Fatalf("GetWorker: %v", err)
	}
	if updated.StackID != "stack-1" {
		t.Fatalf("worker stack_id = %q, want stack-1", updated.StackID)
	}
	runtime, err := store.GetServerRuntime(context.Background(), "owner-1", serverID)
	if err != nil {
		t.Fatalf("GetServerRuntime: %v", err)
	}
	if runtime.StackID != "stack-1" || runtime.WorkerID != "worker-1" || runtime.Revision != 3 || runtime.Generation != 1 || runtime.SourceAuthority != controlplane.ServerEventAuthorityGuard {
		t.Fatalf("canonical runtime assignment = %#v, want Guard-observed CAS-bound stack at revision 3/generation 1", runtime)
	}

	var envelope struct {
		Data struct {
			KitDeploymentID string               `json:"kit_deployment_id"`
			WorkerID        string               `json:"worker_id"`
			Server          stackOperationServer `json:"server"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if envelope.Data.KitDeploymentID != "stack-1" || envelope.Data.WorkerID != "worker-1" {
		t.Fatalf("unexpected assignment response: %+v", envelope.Data)
	}
	if envelope.Data.Server.Assignment != "stack" || envelope.Data.Server.KitDeploymentID != "stack-1" {
		t.Fatalf("server assignment = %+v, want stack assignment", envelope.Data.Server)
	}

	repeatEvent, repeatRecorder := stackOperationsRouteTestEvent(http.MethodPost, "/api/v1/stacks/stack-1/workers/worker-1/assign", "stack-1", "owner-1")
	repeatEvent.Request.SetPathValue("workerId", "worker-1")
	repeatAssignErr := (stackOperationsRouteHandlers{stackStore: store, serverStore: store, workerStore: store}).assignWorker(repeatEvent)
	if repeatAssignErr != nil {
		t.Fatalf("repeat assign returned router error: %v", repeatAssignErr)
	}
	if repeatRecorder.Code != http.StatusOK {
		t.Fatalf("repeat status = %d body=%s, want 200", repeatRecorder.Code, repeatRecorder.Body.String())
	}
	runtime, err = store.GetServerRuntime(context.Background(), "owner-1", serverID)
	if err != nil {
		t.Fatalf("GetServerRuntime(repeat): %v", err)
	}
	if runtime.Revision != 3 {
		t.Fatalf("idempotent assignment appended revision %d, want unchanged revision 3", runtime.Revision)
	}
}

func TestStackOperationsAssignmentConflictsLeaveCanonicalBindingsUnchanged(t *testing.T) {
	for _, test := range []struct {
		name            string
		existingStackID string
	}{
		{name: "canonical runtime is missing"},
		{name: "canonical runtime belongs to another stack", existingStackID: "stack-other"},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := controlplane.NewMemoryStore()
			ctx := t.Context()
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
			serverID := ""
			if test.existingStackID != "" {
				serverID = seedStackOperationsAssignableRuntime(t, store, *worker, test.existingStackID)
			}

			event, recorder := stackOperationsRouteTestEvent(http.MethodPost, "/api/v1/stacks/stack-1/workers/worker-1/assign", "stack-1", "owner-1")
			event.Request.SetPathValue("workerId", "worker-1")
			event.Request = event.Request.WithContext(identity.NewContext(event.Request.Context(), &identity.Identity{UserID: "owner-1", OrgID: "tenant-1"}))
			if err := (stackOperationsRouteHandlers{stackStore: store, serverStore: store, workerStore: store}).assignWorker(event); err != nil {
				t.Fatalf("assign returned router error: %v", err)
			}
			if recorder.Code != http.StatusConflict {
				t.Fatalf("status = %d body=%s, want 409", recorder.Code, recorder.Body.String())
			}
			updatedWorker, err := store.GetWorker(ctx, "tenant-1", "worker-1")
			if err != nil {
				t.Fatalf("GetWorker: %v", err)
			}
			if updatedWorker.StackID != "" {
				t.Fatalf("worker stack_id = %q after conflict, want unchanged", updatedWorker.StackID)
			}
			if serverID != "" {
				updatedRuntime, err := store.GetServerRuntime(ctx, "tenant-1", serverID)
				if err != nil {
					t.Fatalf("GetServerRuntime: %v", err)
				}
				if updatedRuntime.StackID != test.existingStackID || updatedRuntime.Revision != 1 {
					t.Fatalf("canonical binding changed: %#v", updatedRuntime)
				}
			}
		})
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

func TestStackOperationsCanonicalNotFoundDoesNotFallback(t *testing.T) {
	store := controlplane.NewMemoryStore()
	event, _ := stackOperationsRouteTestEvent(http.MethodGet, "/api/v1/stacks/stack-1/operations", "stack-1", "owner-1")
	err := (stackOperationsRouteHandlers{stackStore: store, serverStore: store, serviceStore: store, workerStore: store, registryStore: store, jobStore: store}).operations(event)
	assert.Equal(t, http.StatusNotFound, err.(*httpx.APIError).Status)
}

func TestStackOperationsUsesControlPlaneStoresForOperationsPayload(t *testing.T) {
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
	if _, err := store.UpdateStackRuntime(ctx, "tenant-1", "stack-store", controlplane.RuntimeUpdate{
		RuntimeSummary: map[string]any{"stackkit_outputs": map[string]any{
			"services": []any{
				map[string]any{"name": "coolify", "url": "https://stale-runtime.example.test"},
				map[string]any{"name": "immich", "url": "https://stale-runtime-immich.example.test"},
			},
		}},
	}); err != nil {
		t.Fatalf("UpdateStackRuntime: %v", err)
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
	if _, err := store.UpsertServerRuntime(ctx, controlplane.ServerRuntime{
		ID: "node-1", TenantID: "tenant-1", StackID: "stack-store", OwnerSubjectID: "owner-1",
		WorkerID: "worker-1", NodeID: "node-1", Name: "main", LifecycleState: "active",
		ConnectionState: "online", HealthState: "healthy", DesiredState: "running",
	}); err != nil {
		t.Fatalf("UpsertServerRuntime: %v", err)
	}
	registryService := controlplane.Service{
		ID:         "service-coolify",
		TenantID:   "tenant-1",
		StackID:    "stack-store",
		NodeID:     "node-1",
		ServiceKey: "coolify",
		Name:       "coolify",
		Status:     "running",
		Source:     "managed",
		URL:        "https://coolify.home",
		Metadata:   map[string]any{"type": "paas", "display_name": "Coolify"},
	}
	if _, err := store.UpsertService(ctx, registryService); err != nil {
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
		stackStore:   store,
		serverStore:  store,
		serviceStore: store,
		workerStore:  store,
		registryStore: stackOperationsRegistryStoreStub{
			nodesErr: fmt.Errorf("nodes unavailable"),
			services: []controlplane.Service{
				registryService,
				{ID: "duplicate-coolify", ServiceKey: "COOLIFY", Name: "COOLIFY", Status: "unhealthy"},
			},
		},
		jobStore:             store,
		activityStore:        store,
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
	if envelope.Data.Stack["id"] != "stack-store" || envelope.Data.Stack["kit_deployment_id"] != "stack-store" {
		t.Fatalf("stack identity = %#v, want stack-store", envelope.Data.Stack)
	}
	var sawCanonical, sawPendingLease bool
	for _, server := range envelope.Data.Servers {
		sawCanonical = sawCanonical || server.ID == "node-1" && server.Source == "canonical-server"
		sawPendingLease = sawPendingLease || server.ID == "lease:lease-pending" && server.Source == managedRuntimeInventorySource
	}
	if !sawCanonical || !sawPendingLease {
		t.Fatalf("servers = %#v, want canonical runtime plus pending lease", envelope.Data.Servers)
	}
	if envelope.Data.KPIs.HealthyServers != 1 {
		t.Fatalf("healthy servers = %d, want canonical healthy server", envelope.Data.KPIs.HealthyServers)
	}
	if got := len(envelope.Data.Services); got != 1 {
		t.Fatalf("services = %d, want 1", got)
	}
	if service := envelope.Data.Services[0]; service.ID != registryService.ID || service.Name != "coolify" || service.Type != "paas" || service.Status != registryUnknownStatus || service.URL != "" {
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
		stackStore: store, serverStore: store, serviceStore: store, workerStore: store, registryStore: store, jobStore: store,
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
		stackStore: store, serverStore: store, serviceStore: store, workerStore: store, registryStore: store, jobStore: store,
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
		stackStore: store, serverStore: store, serviceStore: store, workerStore: store, registryStore: store, jobStore: store,
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
	servers, _, err := (stackOperationsRouteHandlers{serverStore: store}).canonicalOperationServers(t.Context(), "owner-1", "tenant-1", "stack-1")
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
			ConnectionState: "revoked", HealthState: "unknown", LeaseID: "lease-ended",
			Metadata: map[string]any{"runtime_slot_key": "worker-a", "runtime_slot_generation": "3"},
		},
	} {
		if _, err := store.UpsertServerRuntime(t.Context(), runtime); err != nil {
			t.Fatalf("UpsertServerRuntime(%s): %v", runtime.ID, err)
		}
	}

	servers, terminal, err := (stackOperationsRouteHandlers{serverStore: store}).canonicalOperationServers(t.Context(), "owner-1", "tenant-1", "stack-1")
	if err != nil {
		t.Fatalf("canonicalOperationServers: %v", err)
	}
	if len(servers) != 1 || servers[0].ID != "server-active" {
		t.Fatalf("servers = %#v, want only current actionable inventory", servers)
	}
	if len(terminal) != 1 || terminal[0].ID != "server-ended" || terminal[0].LeaseID != "lease-ended" ||
		terminal[0].Capabilities["runtime_slot_key"] != "worker-a" || terminal[0].Status != "decommissioned" {
		t.Fatalf("terminal recreate discovery = %#v, want exact retired generation without live inventory state", terminal)
	}
	legacy := []stackOperationServer{{ID: "agent-ended", AgentID: "agent-ended", ServerID: "server-ended", Source: workerRegistryInventorySource}}
	if merged := mergeCanonicalOperationServers(servers, terminal, legacy); len(merged) != 1 || merged[0].ID != "server-active" {
		t.Fatalf("terminal canonical identity was resurrected by legacy projection: %#v", merged)
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
	if _, err := store.UpdateStackRuntime(ctx, "tenant-1", "stack-failed-prep", controlplane.RuntimeUpdate{
		RuntimeSummary: map[string]any{"stackkit_outputs": map[string]any{
			"services": []any{map[string]any{"name": "immich", "url": "https://stale-runtime.example.test"}},
		}},
	}); err != nil {
		t.Fatalf("UpdateStackRuntime: %v", err)
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
		serverStore:          store,
		serviceStore:         store,
		workerStore:          store,
		registryStore:        stackOperationsRegistryStoreStub{servicesErr: fmt.Errorf("services unavailable")},
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
	if got := envelope.Data.Services; len(got) != 1 || got[0].Name != "immich" || got[0].URL != "" || got[0].Status != registryUnknownStatus {
		t.Fatalf("services = %#v, want safe RuntimeSummary fallback after registry failure", got)
	}
	server := envelope.Data.Servers[0]
	if server.Capabilities["last_job_id"] != "job-failed-prep" ||
		server.Capabilities["failure_reason"] != "target_bootstrap_timeout" ||
		server.Capabilities["diagnostics_available"] != true {
		t.Fatalf("managed server missing failure annotations: %#v", server)
	}
}

func TestLatestStackFailureFollowsCurrentOperationAttempt(t *testing.T) {
	now := time.Now().UTC()
	oldDeployFailure := controlplane.Job{
		ID: "job-rollout", Type: "deploy", State: "failed", Error: "StackKits artifact generation failed",
		CreatedAt: now.Add(-72 * time.Hour), UpdatedAt: now.Add(-72 * time.Hour),
	}
	oldUntypedFailure := controlplane.Job{
		ID: "job-stale-failure", State: "failed", Error: "SQLSTATE 42501",
		CreatedAt: now.Add(-time.Minute), UpdatedAt: now.Add(-time.Minute),
	}
	for _, test := range []struct {
		name, wantID, wantType, wantReason string
		jobs                               []controlplane.Job
	}{
		{
			name: "newer unrelated operation preserves failure", wantID: "job-rollout", wantType: "deploy",
			jobs: []controlplane.Job{{ID: "job-register-server", Type: "update", State: "completed", CreatedAt: now, UpdatedAt: now}, oldDeployFailure},
		},
		{
			name: "newer same operation supersedes failure",
			jobs: []controlplane.Job{{ID: "job-current", Type: "deploy", State: "completed", CreatedAt: now, UpdatedAt: now}, oldDeployFailure},
		},
		{
			name: "waiting current attempt suppresses stale failure",
			jobs: []controlplane.Job{{ID: "job-current", State: "waiting", CreatedAt: now, UpdatedAt: now}, oldUntypedFailure},
		},
		{
			name: "failed current attempt becomes latest", wantID: "job-current", wantReason: "provider.partial_create",
			jobs: []controlplane.Job{{ID: "job-current", State: "failed", Error: "provider.partial_create", CreatedAt: now, UpdatedAt: now}, oldUntypedFailure},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := latestStackFailureFromJobs(test.jobs)
			if test.wantID == "" {
				if got != nil {
					t.Fatalf("latest failure = %#v, want none", got)
				}
				return
			}
			if got == nil || got.JobID != test.wantID || (test.wantType != "" && got.Type != test.wantType) || (test.wantReason != "" && got.Reason != test.wantReason) {
				t.Fatalf("latest failure = %#v, want job %q type %q reason %q", got, test.wantID, test.wantType, test.wantReason)
			}
		})
	}
}

func TestStackServerDetailsUsesControlPlaneStores(t *testing.T) {
	ctx := context.Background()
	store := controlplane.NewMemoryStore()
	now := time.Now().UTC()
	if _, err := store.CreateStack(ctx, controlplane.CreateStackRequest{
		ID:             "stack-store",
		TenantID:       "owner-1",
		OwnerSubjectID: "owner-1",
		Name:           "Store Stack",
		Mode:           "easy",
		Status:         "running",
	}); err != nil {
		t.Fatalf("CreateStack: %v", err)
	}
	if _, err := store.UpsertNode(ctx, controlplane.Node{
		ID:       "node-1",
		TenantID: "owner-1",
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
	if _, err := store.UpsertServerRuntime(ctx, controlplane.ServerRuntime{
		ID: "node-1", TenantID: "owner-1", StackID: "stack-store", OwnerSubjectID: "owner-1",
		WorkerID: "worker-1", NodeID: "node-1", Name: "main", LifecycleState: "active",
		ConnectionState: "connected", HealthState: "healthy", DesiredState: "running", LastHeartbeatAt: &now,
	}); err != nil {
		t.Fatalf("UpsertServerRuntime: %v", err)
	}
	if _, err := store.UpsertServiceRuntime(ctx, controlplane.ServiceRuntime{
		ID: "service-vaultwarden", TenantID: "owner-1", StackID: "stack-store", ServerID: "node-1",
		ServiceKey: "vaultwarden", ServiceInstance: "default", Name: "Vaultwarden",
		DesiredState: registryStatusRunning, ObservedState: registryStatusRunning,
		HealthState: serviceHealthHealthy, ObservedAt: &now,
		Access:       map[string]any{serviceAccessModeKey: serviceAccessRelay, serviceAccessURLKey: "https://vault.owner.kombify.me", "route_id": "route-1"},
		Capabilities: []string{serviceActionRestart}, Source: stackKitsInventorySource,
	}); err != nil {
		t.Fatalf("UpsertServiceRuntime: %v", err)
	}
	if _, err := store.AppendActivity(ctx, controlplane.ActivityEvent{
		ID: "activity-node-1", TenantID: "owner-1", StackID: "stack-store", ServerScopeKey: "node-1",
		Action: "service_observed", Severity: "info", Message: "Service observation recorded",
	}); err != nil {
		t.Fatalf("AppendActivity: %v", err)
	}

	event, recorder := stackOperationsRouteTestEvent(http.MethodGet, "/api/v1/stacks/stack-store/servers/node-1", "stack-store", "owner-1")
	event.Request.SetPathValue("serverId", "node-1")
	if err := (stackOperationsRouteHandlers{
		stackStore:           store,
		serverStore:          store,
		serviceStore:         store,
		workerStore:          store,
		registryStore:        store,
		jobStore:             store,
		activityStore:        store,
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
	if envelope.Data.Server.ID != "node-1" || envelope.Data.Server.Source != "canonical-server" {
		t.Fatalf("unexpected server details: %#v", envelope.Data.Server)
	}
	if got := len(envelope.Data.Services); got != 1 {
		t.Fatalf("services = %d, want 1", got)
	}
	if got := len(envelope.Data.Logs); got != 1 || envelope.Data.Logs[0]["id"] != "activity-node-1" {
		t.Fatalf("canonical activity logs = %#v", envelope.Data.Logs)
	}
	service := envelope.Data.Services[0]
	if service.Name != "vaultwarden" || service.Status != serviceHealthHealthy || service.URL != "https://vault.owner.kombify.me" ||
		stringFromAnyMap(service.Access, serviceAccessModeKey) != serviceAccessRelay ||
		!slices.Equal(service.AllowedActions, []string{serviceActionFreeze, serviceActionRestart}) {
		t.Fatalf("unexpected service projection: %#v", service)
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
		URL:        "https://kuma.home",
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

func TestStackScopedAlertsSeparateUnscopedAlerts(t *testing.T) {
	servers := []stackOperationServer{
		{ID: "worker-1", Hostname: "node-a", AgentID: "agent-a", Assignment: "stack"},
		{ID: "worker-2", Hostname: "node-b", AgentID: "agent-b", Assignment: "unassigned"},
	}
	stackLabels := map[string]string{"agent_id": "agent-a"}
	now := time.Now()
	alerts, unscoped := stackScopedAlertsFromStates([]monitoring.AlertState{
		{
			Rule:     monitoring.AlertRule{Name: "StackCPU", Severity: "warning", Message: "high", Labels: stackLabels},
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
	if alert := alerts[0]; alert.Name != "StackCPU" || alert.Severity != "warning" || alert.Message != "high" || alert.Value != 99 || alert.Status != "firing" {
		t.Fatalf("unexpected scoped alert: %#v", alert)
	}
	stackLabels["agent_id"] = "changed"
	if alerts[0].Labels["agent_id"] != "agent-a" {
		t.Fatalf("alert labels were not isolated: %#v", alerts[0].Labels)
	}
	if unscoped != 1 {
		t.Fatalf("unscoped alerts = %d, want global count only", unscoped)
	}
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
		stackStore: store, serverStore: store, serviceStore: store, workerStore: store, registryStore: store, jobStore: store,
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
	if got := activeStackFailure(failure, []stackOperationServer{{Hostname: "srv-live"}}, nil); got != nil {
		t.Fatalf("unnamed destroy stayed on a live Node: %#v", got)
	}
	bound := &stackLatestFailure{Type: "destroy", State: "failed", LeaseID: "lease-live"}
	if got := activeStackFailure(bound, []stackOperationServer{{LeaseID: "lease-other"}}, nil); got != nil {
		t.Fatalf("destroy of a gone lease stayed on the dashboard: %#v", got)
	}
	if got := activeStackFailure(bound, []stackOperationServer{{LeaseID: "lease-live"}}, nil); got != bound {
		t.Fatalf("destroy of the current lease was dropped: %#v", got)
	}
	rollout := &stackLatestFailure{Type: "deploy", State: "failed"}
	if got := activeStackFailure(rollout, nil, nil); got != rollout {
		t.Fatalf("activeStackFailure removed unrelated failure: %#v", got)
	}

	stale := stackReadiness{Status: "error", Message: "The last operation failed."}
	got := reconcileResolvedDestroyReadiness(stale, failure, nil, nil, nil)
	if got.Status != "waiting_for_server" || got.CanStart || got.ReviewRequired {
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
