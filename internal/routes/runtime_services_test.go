package routes

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/jobs"
	"github.com/kombifyio/techstack/pkg/runtimehealth"
	"github.com/kombifyio/techstack/pkg/serverregistry"
)

type recordingServiceActionOrchestrator struct {
	requests []jobs.StackKitLifecycleRequest
	store    controlplane.JobStore
	enqueued map[string]bool
}

func (o *recordingServiceActionOrchestrator) EnqueueStackKitLifecycle(ctx context.Context, request jobs.StackKitLifecycleRequest) (string, error) {
	if o.enqueued == nil {
		o.enqueued = map[string]bool{}
	}
	if o.enqueued[request.DurableJobID] {
		return request.DurableJobID, nil
	}
	if o.store != nil {
		if existing, err := o.store.GetJob(ctx, request.TenantID, request.DurableJobID); err == nil {
			if !jobs.MatchesStackKitServiceActionReceipt(existing.Result, request) {
				return "", controlplane.ErrConflict
			}
		} else if errors.Is(err, controlplane.ErrNotFound) {
			_, err = o.store.CreateJob(ctx, controlplane.UpsertJobRequest{
				ID: request.DurableJobID, TenantID: request.TenantID, StackID: request.StackID,
				Type: string(jobs.JobTypeStackKitLifecycle), State: "pending",
				Result: map[string]any{"service_action_receipt": jobs.StackKitServiceActionReceipt(request)},
			})
			if err != nil {
				return "", err
			}
		} else {
			return "", err
		}
	}
	o.requests = append(o.requests, request)
	o.enqueued[request.DurableJobID] = true
	return request.DurableJobID, nil
}

func TestServiceRuntimeActionDerivesAuthorityAndReplaysIdempotently(t *testing.T) {
	store := controlplane.NewMemoryStore()
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	observedAt := now.Add(-10 * time.Second)
	if _, err := store.CreateStack(t.Context(), controlplane.CreateStackRequest{ID: "stack-1", TenantID: "tenant-1", OwnerSubjectID: "owner-1", Name: "Stack", Config: map[string]any{"stackkit": "cloud-kit"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertServerRuntime(t.Context(), controlplane.ServerRuntime{ID: "server-1", TenantID: "tenant-1", StackID: "stack-1", OwnerSubjectID: "owner-1", WorkerID: "agent-1", InventoryRevision: 7, ConnectionState: string(serverregistry.ConnectionConnected), LastHeartbeatAt: &observedAt}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertServiceRuntime(t.Context(), controlplane.ServiceRuntime{ID: "service-1", TenantID: "tenant-1", StackID: "stack-1", ServerID: "server-1", ServiceKey: "coolify", StackKitVersion: "cloud-kit@v0.15.9", ObservedAt: &observedAt, Capabilities: []string{"restart"}, Metadata: map[string]any{"inventory_revision": int64(7)}}); err != nil {
		t.Fatal(err)
	}
	orch := &recordingServiceActionOrchestrator{store: store}
	for attempt := 0; attempt < 2; attempt++ {
		// Reconstruct the handler to prove replay survives a process-local
		// handler restart and is owned by the durable job receipt.
		h := serviceRuntimeHandlers{store: store, stacks: store, servers: store, jobs: store, now: func() time.Time { return now }, orch: orch}
		event, recorder := registryRouteStoreTestEvent(http.MethodPost, "/api/v1/registry/services/service-1/actions", "owner-1", "tenant-1", map[string]any{"action": "restart", "expected_inventory_revision": 7, "owner_approved": true})
		event.Request.SetPathValue("serviceId", "service-1")
		event.Request.Header.Set("Idempotency-Key", "restart-1")
		if err := h.action(event); err != nil {
			t.Fatal(err)
		}
		if recorder.Code != http.StatusAccepted {
			t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
		}
	}
	if len(orch.requests) != 1 {
		t.Fatalf("dispatch count=%d want 1", len(orch.requests))
	}
	request := orch.requests[0]
	if request.StackID != "stack-1" || request.AgentID != "agent-1" || request.ServiceKey != "coolify" || request.StackKit != "cloud-kit" || request.Operation != jobs.StackKitLifecycleServiceRestart {
		t.Fatalf("derived request=%#v", request)
	}
	conflicting := serviceRuntimeHandlers{store: store, stacks: store, servers: store, jobs: store, now: func() time.Time { return now }, orch: orch}
	event, recorder := registryRouteStoreTestEvent(http.MethodPost, "/api/v1/registry/services/service-1/actions", "owner-1", "tenant-1", map[string]any{"action": "restart", "expected_inventory_revision": 7, "owner_approved": true})
	event.Request.SetPathValue("serviceId", "service-1")
	event.Request.Header.Set("Idempotency-Key", "restart-2")
	if err := conflicting.action(event); err != nil || recorder.Code != http.StatusConflict || len(orch.requests) != 1 {
		t.Fatalf("competing action status=%d requests=%d err=%v", recorder.Code, len(orch.requests), err)
	}
	// A new process has no in-memory queue identity. Replaying the same pending
	// receipt must rebuild exactly that job instead of leaving it stranded.
	recoveredOrch := &recordingServiceActionOrchestrator{store: store}
	recovered := serviceRuntimeHandlers{store: store, stacks: store, servers: store, jobs: store, now: func() time.Time { return now }, orch: recoveredOrch}
	event, recorder = registryRouteStoreTestEvent(http.MethodPost, "/api/v1/registry/services/service-1/actions", "owner-1", "tenant-1", map[string]any{"action": "restart", "expected_inventory_revision": 7, "owner_approved": true})
	event.Request.SetPathValue("serviceId", "service-1")
	event.Request.Header.Set("Idempotency-Key", "restart-1")
	if err := recovered.action(event); err != nil || recorder.Code != http.StatusAccepted || len(recoveredOrch.requests) != 1 {
		t.Fatalf("pending crash recovery status=%d requests=%d err=%v", recorder.Code, len(recoveredOrch.requests), err)
	}
}

func TestServiceRuntimeLogsReturnBoundedRedactedCompletedPage(t *testing.T) {
	store := controlplane.NewMemoryStore()
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	if _, err := store.CreateStack(t.Context(), controlplane.CreateStackRequest{ID: "stack-1", TenantID: "tenant-1", OwnerSubjectID: "owner-1", Name: "Stack"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertServiceRuntime(t.Context(), controlplane.ServiceRuntime{ID: "service-1", TenantID: "tenant-1", StackID: "stack-1", ServerID: "server-1", ServiceKey: "coolify", Name: "Coolify"}); err != nil {
		t.Fatal(err)
	}
	receipt := map[string]any{
		"schema_version": "techstack.service-action-receipt/v1", "service_id": "service-1", "service_key": "coolify",
		"action": "logs", "request_digest": strings.Repeat("a", 64), "log_cursor": "",
	}
	if _, err := store.CreateJob(t.Context(), controlplane.UpsertJobRequest{ID: "job-logs-1", TenantID: "tenant-1", StackID: "stack-1", Type: string(jobs.JobTypeStackKitLifecycle), State: "pending", Result: map[string]any{"service_action_receipt": receipt}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.StartJob(t.Context(), "tenant-1", "job-logs-1", now); err != nil {
		t.Fatal(err)
	}
	result := map[string]any{
		"operation": jobs.StackKitLifecycleServiceLogs, "service_action_receipt": receipt,
		"service_logs": map[string]any{
			"nextCursor": "cursor-next",
			"entries": []any{
				map[string]any{"timestamp": now.Format(time.RFC3339Nano), "message": "password=super-secret-value"},
				map[string]any{"timestamp": now.Add(time.Second).Format(time.RFC3339Nano), "message": "ready"},
			},
		},
	}
	if _, err := store.CompleteJob(t.Context(), "tenant-1", "job-logs-1", result, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	event, recorder := registryRouteStoreTestEvent(http.MethodGet, "/api/v1/registry/services/service-1/logs?limit=1", "owner-1", "tenant-1", nil)
	event.Request.SetPathValue("serviceId", "service-1")
	h := serviceRuntimeHandlers{store: store, stacks: store, servers: store, jobs: store, now: func() time.Time { return now }}
	if err := h.logs(event); err != nil {
		t.Fatal(err)
	}
	var envelope struct {
		Data serviceLogsResponse `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if len(envelope.Data.Entries) != 1 || strings.Contains(envelope.Data.Entries[0].Message, "super-secret-value") || envelope.Data.NextCursor != "cursor-next" {
		t.Fatalf("logs response = %#v", envelope.Data)
	}
}

func TestServiceRuntimeRoutesReturnPersistedStateAndOwnerScope(t *testing.T) {
	store := controlplane.NewMemoryStore()
	ctx := t.Context()
	now := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
	for _, stack := range []controlplane.CreateStackRequest{
		{ID: "stack-owner", TenantID: "tenant-1", OwnerSubjectID: "owner-1", Name: "Owner"},
		{ID: "stack-foreign", TenantID: "tenant-1", OwnerSubjectID: "owner-2", Name: "Foreign"},
	} {
		if _, err := store.CreateStack(ctx, stack); err != nil {
			t.Fatalf("CreateStack: %v", err)
		}
	}
	heartbeat := now.Add(-30 * time.Second)
	for _, server := range []controlplane.ServerRuntime{
		{ID: "server-owner", TenantID: "tenant-1", StackID: "stack-owner", OwnerSubjectID: "owner-1", Name: "Owner", LifecycleState: "active", ConnectionState: "connected", HealthState: serviceHealthHealthy, LastHeartbeatAt: &heartbeat},
		{ID: "server-foreign", TenantID: "tenant-1", StackID: "stack-foreign", OwnerSubjectID: "owner-2", Name: "Foreign", LifecycleState: "active", ConnectionState: "connected", HealthState: serviceHealthHealthy, LastHeartbeatAt: &heartbeat},
	} {
		if _, err := store.UpsertServerRuntime(ctx, server); err != nil {
			t.Fatalf("UpsertServerRuntime: %v", err)
		}
	}
	observedAt := now.Add(-10 * time.Second)
	for _, service := range []controlplane.ServiceRuntime{
		{ID: "service-owner", TenantID: "tenant-1", StackID: "stack-owner", ServerID: "server-owner", ServiceKey: "vaultwarden", ServiceInstance: "default", Name: "Vaultwarden", DesiredState: registryStatusRunning, ObservedState: registryStatusRunning, HealthState: serviceHealthHealthy, ObservedAt: &observedAt, Access: map[string]any{serviceAccessModeKey: serviceAccessRelay, serviceAccessURLKey: "https://vault.owner.kombify.me", "route_id": "route-owner"}, Capabilities: []string{serviceActionRestart}, Source: stackKitsInventorySource},
		{ID: "service-foreign", TenantID: "tenant-1", StackID: "stack-foreign", ServerID: "server-foreign", ServiceKey: "other", Name: "Other", ObservedAt: &observedAt},
	} {
		if _, err := store.UpsertServiceRuntime(ctx, service); err != nil {
			t.Fatalf("UpsertServiceRuntime: %v", err)
		}
	}

	h := serviceRuntimeHandlers{store: store, stacks: store, servers: store, now: func() time.Time { return now }}
	event, recorder := registryRouteStoreTestEvent(http.MethodGet, "/api/v1/services", "owner-1", "tenant-1", nil)
	if err := h.list(event); err != nil {
		t.Fatalf("list: %v", err)
	}
	var envelope struct {
		Data []serviceRuntimeResponse `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(envelope.Data) != 1 || envelope.Data[0].ID != "service-owner" {
		t.Fatalf("owner scope leaked services: %#v", envelope.Data)
	}
	if envelope.Data[0].TechstackID != "stack-owner" {
		t.Fatalf("techstack_id = %q, want stack-owner", envelope.Data[0].TechstackID)
	}
	if strings.Contains(recorder.Body.String(), `"stack_id"`) {
		t.Fatalf("canonical service response exposed ambiguous stack_id: %s", recorder.Body.String())
	}
	if envelope.Data[0].Health.State != serviceHealthHealthy || stringFromAnyMap(envelope.Data[0].Access, serviceAccessModeKey) != serviceAccessRelay || len(envelope.Data[0].AllowedActions) != 1 {
		t.Fatalf("fresh service projection = %#v", envelope.Data[0])
	}

	// The read path returns persisted state: once the registry sweeper
	// demotes the server (durable write), the service projection masks to
	// unknown with the persisted connection reason. Wall-clock recompute at
	// read time is gone.
	if _, err := store.UpsertServerRuntime(ctx, controlplane.ServerRuntime{
		ID: "server-owner", TenantID: "tenant-1", StackID: "stack-owner", OwnerSubjectID: "owner-1",
		Name: "Owner", LifecycleState: "active", ConnectionState: "stale", HealthState: "unknown",
		LastHeartbeatAt: &heartbeat,
	}); err != nil {
		t.Fatalf("demote server: %v", err)
	}
	event, recorder = registryRouteStoreTestEvent(http.MethodGet, "/api/v1/services", "owner-1", "tenant-1", nil)
	if err := h.list(event); err != nil {
		t.Fatalf("stale list: %v", err)
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode stale: %v", err)
	}
	service := envelope.Data[0]
	if service.Health.State != monitoringStatusUnknown || service.ObservedState != monitoringStatusUnknown || service.Health.ReasonCode != "server_connection_stale" {
		t.Fatalf("service on demoted server stayed green: %#v", service)
	}
	if stringFromAnyMap(service.Access, serviceAccessModeKey) != serviceAccessUnavailable || stringFromAnyMap(service.Access, "reason_code") != "server_connection_stale" {
		t.Fatalf("service access on demoted server stayed available: %#v", service.Access)
	}
}

// TestServiceRuntimeAccessGatesOnPersistedServerConnection: the projection
// masks services when the PERSISTED server connection is not connected or
// degraded. Heartbeat age alone no longer changes the read model; the sweeper
// persists that demotion.
func TestServiceRuntimeAccessGatesOnPersistedServerConnection(t *testing.T) {
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	observedAt := now.Add(-10 * time.Second)
	staleHeartbeat := now.Add(-runtimehealth.FreshHeartbeatWindow - time.Second)
	service := controlplane.ServiceRuntime{
		ID: "service-direct", ServerID: "server-stale", ObservedAt: &observedAt,
		ObservedState: registryStatusRunning, HealthState: serviceHealthHealthy,
		Access: map[string]any{
			serviceAccessModeKey: serviceAccessDirect, serviceAccessURLKey: "https://base.example.test",
		},
	}
	server := controlplane.ServerRuntime{
		ID: "server-stale", ConnectionState: string(serverregistry.ConnectionStale),
		LastHeartbeatAt: &staleHeartbeat,
	}
	got := (serviceRuntimeHandlers{now: func() time.Time { return now }}).response(service, &server)
	if got.Health.ReasonCode != "server_connection_stale" || stringFromAnyMap(got.Access, serviceAccessModeKey) != serviceAccessUnavailable {
		t.Fatalf("direct access survived persisted stale server: %#v", got)
	}

	// A persisted connected server keeps the service projection honest to the
	// stored dimensions even when the raw heartbeat evidence has aged: the
	// sweeper, not the read path, owns that demotion.
	server.ConnectionState = string(serverregistry.ConnectionConnected)
	got = (serviceRuntimeHandlers{now: func() time.Time { return now }}).response(service, &server)
	if got.Health.ReasonCode != "" || got.ObservedState != registryStatusRunning {
		t.Fatalf("read path recomputed heartbeat freshness: %#v", got)
	}
}

func TestServiceRuntimeDetailHidesForeignOwner(t *testing.T) {
	store := controlplane.NewMemoryStore()
	if _, err := store.CreateStack(t.Context(), controlplane.CreateStackRequest{ID: "stack-foreign", TenantID: "tenant-1", OwnerSubjectID: "owner-2", Name: "Foreign"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertServiceRuntime(t.Context(), controlplane.ServiceRuntime{ID: "service-foreign", TenantID: "tenant-1", StackID: "stack-foreign", ServerID: "server-foreign", ServiceKey: "db", Name: "DB"}); err != nil {
		t.Fatal(err)
	}
	event, recorder := registryRouteStoreTestEvent(http.MethodGet, "/api/v1/services/service-foreign", "owner-1", "tenant-1", nil)
	event.Request.SetPathValue("serviceId", "service-foreign")
	h := serviceRuntimeHandlers{store: store, stacks: store, servers: store, now: time.Now}
	if err := h.get(event); err != nil {
		t.Fatalf("get: %v", err)
	}
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestServiceRuntimeRoutesExposeLegacyBackfillWithoutHealthClaim(t *testing.T) {
	store := controlplane.NewMemoryStore()
	ctx := t.Context()
	now := time.Date(2026, 7, 17, 14, 0, 0, 0, time.UTC)
	if _, err := store.CreateStack(ctx, controlplane.CreateStackRequest{ID: "stack-1", TenantID: "tenant-1", OwnerSubjectID: "owner-1", Name: "Stack"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertServerRuntime(ctx, controlplane.ServerRuntime{
		ID: "server-1", TenantID: "tenant-1", StackID: "stack-1", OwnerSubjectID: "owner-1", NodeID: "legacy-node-1", Name: "primary",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertService(ctx, controlplane.Service{
		ID: "legacy-service-1", TenantID: "tenant-1", StackID: "stack-1", NodeID: "legacy-node-1",
		ServiceKey: "vaultwarden", Name: "Vaultwarden", Status: serviceHealthHealthy, Source: "legacy-registry",
	}); err != nil {
		t.Fatal(err)
	}

	h := serviceRuntimeHandlers{store: store, stacks: store, servers: store, now: func() time.Time { return now }}
	event, recorder := registryRouteStoreTestEvent(http.MethodGet, "/api/v1/services", "owner-1", "tenant-1", nil)
	if err := h.list(event); err != nil {
		t.Fatalf("list: %v", err)
	}
	var envelope struct {
		Data []serviceRuntimeResponse `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil || len(envelope.Data) != 1 {
		t.Fatalf("response = %s err=%v", recorder.Body.String(), err)
	}
	service := envelope.Data[0]
	if service.Health.State != monitoringStatusUnknown || service.ObservedState != monitoringStatusUnknown || service.Health.ObservedAt != nil {
		t.Fatalf("backfill claimed health: %#v", service)
	}
	if stringFromAnyMap(service.Access, serviceAccessModeKey) != serviceAccessUnavailable || service.Provenance["migration_mode"] != "read_through_backfill" {
		t.Fatalf("missing safe backfill projection: %#v", service)
	}
}

func TestSanitizedServiceAccessPreservesOnlyTrustedObservedDirectEndpoints(t *testing.T) {
	tests := []struct {
		name    string
		access  map[string]any
		wantURL string
	}{
		{
			name: "Guard observed home endpoint",
			access: map[string]any{
				serviceAccessModeKey: serviceAccessDirect, serviceAccessURLKey: "http://auth.home.localhost",
				serviceAccessObservedKey: true, serviceAccessSourceKey: serviceStackKitManifest,
			},
			wantURL: "http://auth.home.localhost",
		},
		{
			name: "Guard observed HTTPS relay domain",
			access: map[string]any{
				serviceAccessModeKey: serviceAccessDirect, serviceAccessURLKey: "https://base.demo.kombify.me",
				serviceAccessObservedKey: true, serviceAccessSourceKey: serviceStackKitManifest,
			},
			wantURL: "https://base.demo.kombify.me",
		},
		{
			name: "unmarked home endpoint",
			access: map[string]any{
				serviceAccessModeKey: serviceAccessDirect, serviceAccessURLKey: "http://auth.home.localhost",
			},
		},
		{
			name: "observed private IP",
			access: map[string]any{
				serviceAccessModeKey: serviceAccessDirect, serviceAccessURLKey: "http://192.168.178.155",
				serviceAccessObservedKey: true, serviceAccessSourceKey: serviceStackKitManifest,
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := sanitizedServiceAccess(tc.access)
			if gotURL := stringFromAnyMap(got, serviceAccessURLKey); gotURL != tc.wantURL {
				t.Fatalf("url = %q, want %q: %#v", gotURL, tc.wantURL, got)
			}
			wantMode := serviceAccessUnavailable
			if tc.wantURL != "" {
				wantMode = serviceAccessDirect
			}
			if gotMode := stringFromAnyMap(got, serviceAccessModeKey); gotMode != wantMode {
				t.Fatalf("mode = %q, want %q: %#v", gotMode, wantMode, got)
			}
		})
	}
}
