package routes

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/kombifyio/techstack/internal/runtimeproduct/vmlease"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/runtimeconvergence"
	"github.com/kombifyio/techstack/pkg/serverregistry"
	"github.com/kombifyio/techstack/pkg/vmleases"
)

type managedRuntimeEnrollmentRecorder struct {
	lease       vmlease.Lease
	patches     []vmleases.PatchRequest
	patchErrors int
	getError    error
}

func (r *managedRuntimeEnrollmentRecorder) ListByTenant(context.Context, string) ([]vmlease.Lease, error) {
	return []vmlease.Lease{r.lease}, nil
}

func (r *managedRuntimeEnrollmentRecorder) Get(context.Context, string, vmlease.LeaseID) (*vmlease.Lease, error) {
	if r.getError != nil {
		return nil, r.getError
	}
	copy := r.lease
	copy.Metadata = map[string]string{}
	for key, value := range r.lease.Metadata {
		copy.Metadata[key] = value
	}
	return &copy, nil
}

func TestNativeGuardObservationActivatesWithoutLegacyLeaseProjection(t *testing.T) {
	store := controlplane.NewMemoryStore()
	handler := workerRouteHandlers{
		serverStore:           store,
		managedRuntimeLeases: &managedRuntimeEnrollmentRecorder{getError: vmleases.ErrNotFound},
	}
	now := time.Date(2026, 8, 14, 17, 30, 0, 0, time.UTC)
	worker := controlplane.Worker{
		ID: "guard-native", TenantID: "tenant-1", StackID: "stack-1",
		OwnerSubjectID: "owner-1", Hostname: "runtime-1", Provider: "centron",
	}
	if err := handler.projectServerEnrollment(t.Context(), worker, "server-1", "lease-native", now, "pairing-redemption"); err != nil {
		t.Fatal(err)
	}
	heartbeatAt := now.Add(time.Minute)
	if err := handler.projectServerHeartbeat(t.Context(), worker, "server-1", "lease-native", heartbeatAt, "guard-heartbeat", guardEventPosition{
		Epoch: "epoch-native", Sequence: 1, ObservedAt: heartbeatAt,
	}); err != nil {
		t.Fatal(err)
	}
	server, err := store.GetServerRuntime(t.Context(), "tenant-1", "server-1")
	if err != nil {
		t.Fatal(err)
	}
	if server.LifecycleState != string(serverregistry.LifecycleActive) || server.ConnectionState != string(serverregistry.ConnectionConnected) {
		t.Fatalf("native server state = %#v, want active and connected", server)
	}
}

func (r *managedRuntimeEnrollmentRecorder) Patch(_ context.Context, _ string, _ vmlease.LeaseID, request vmleases.PatchRequest) (*vmlease.Lease, error) {
	r.patches = append(r.patches, request)
	if r.patchErrors > 0 {
		r.patchErrors--
		return nil, fmt.Errorf("temporary lease patch failure")
	}
	for key, value := range request.Metadata {
		if r.lease.Metadata == nil {
			r.lease.Metadata = map[string]string{}
		}
		r.lease.Metadata[key] = value
	}
	return &r.lease, nil
}

func TestWorkerEvidenceProjectsCanonicalServerLifecycle(t *testing.T) {
	store := controlplane.NewMemoryStore()
	handler := workerRouteHandlers{serverStore: store}
	now := time.Date(2026, 7, 17, 8, 0, 0, 0, time.UTC)
	worker := controlplane.Worker{
		ID: "guard-1", TenantID: "tenant-1", StackID: "stack-1",
		OwnerSubjectID: "owner-1", Hostname: "runtime-1", Provider: "centron",
	}

	if err := handler.projectServerEnrollment(t.Context(), worker, "server-1", "lease-1", now, "pairing-redemption"); err != nil {
		t.Fatalf("project enrollment: %v", err)
	}
	server, err := store.GetServerRuntime(t.Context(), "tenant-1", "server-1")
	if err != nil {
		t.Fatalf("get enrolled server: %v", err)
	}
	if server.LifecycleState != string(serverregistry.LifecycleEnrolling) || server.ConnectionState != string(serverregistry.ConnectionPending) || server.HealthState != string(serverregistry.HealthUnknown) {
		t.Fatalf("unexpected enrollment projection: %#v", server)
	}
	node, err := store.GetNode(t.Context(), "tenant-1", "server-1")
	if err != nil || server.NodeID != "server-1" || node.Role != "foundation" || node.Status != "pending" || node.WorkerID != worker.ID {
		t.Fatalf("atomic enrollment node = %#v server=%#v err=%v", node, server, err)
	}

	heartbeatAt := now.Add(time.Minute)
	if projectErr := handler.projectServerHeartbeat(t.Context(), worker, "server-1", "lease-1", heartbeatAt, "guard-heartbeat", guardEventPosition{
		Epoch: "epoch-a", Sequence: 1, ObservedAt: heartbeatAt,
	}); projectErr != nil {
		t.Fatalf("project heartbeat: %v", projectErr)
	}
	server, err = store.GetServerRuntime(t.Context(), "tenant-1", "server-1")
	if err != nil {
		t.Fatalf("get active server: %v", err)
	}
	if server.LifecycleState != string(serverregistry.LifecycleActive) || server.ConnectionState != string(serverregistry.ConnectionConnected) || server.HealthState != string(serverregistry.HealthHealthy) {
		t.Fatalf("heartbeat did not activate server: %#v", server)
	}
	if server.LastHeartbeatAt == nil || !server.LastHeartbeatAt.Equal(heartbeatAt) || server.ReasonCode != "" {
		t.Fatalf("heartbeat evidence not recorded correctly: %#v", server)
	}
	if server.Revision != 3 || server.Generation != 1 || server.SourceEpoch == "" || server.SourceSequence <= 0 {
		t.Fatalf("heartbeat event fencing not recorded correctly: %#v", server)
	}

	transitions, err := store.ListServerTransitions(t.Context(), "tenant-1", "server-1", 100)
	if err != nil {
		t.Fatalf("list transitions: %v", err)
	}
	if len(transitions) != 7 {
		t.Fatalf("transitions = %d, want initial and heartbeat transitions: %#v", len(transitions), transitions)
	}
}

func TestGuardObservationConvergesManagedLeaseEnrollment(t *testing.T) {
	store := controlplane.NewMemoryStore()
	leases := &managedRuntimeEnrollmentRecorder{lease: vmlease.Lease{
		ID:       "lease-1",
		Metadata: map[string]string{"runtime_enrollment_status": "pending"},
	}}
	handler := workerRouteHandlers{serverStore: store, managedRuntimeLeases: leases}
	now := time.Date(2026, 8, 14, 13, 0, 0, 0, time.UTC)
	worker := controlplane.Worker{
		ID: "guard-1", TenantID: "tenant-1", StackID: "stack-1", OwnerSubjectID: "owner-1", Hostname: "runtime-1",
	}
	if err := handler.projectServerEnrollment(t.Context(), worker, "server-1", "lease-1", now, "pairing-redemption"); err != nil {
		t.Fatal(err)
	}
	if err := handler.projectServerHeartbeat(t.Context(), worker, "server-1", "lease-1", now.Add(time.Minute), "guard-heartbeat", guardEventPosition{
		Epoch: "epoch-a", Sequence: 1, ObservedAt: now.Add(time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	if got := leases.lease.Metadata["runtime_enrollment_status"]; got != "enrolled" {
		t.Fatalf("lease enrollment status = %q, want enrolled", got)
	}
	if len(leases.patches) != 1 || leases.patches[0].Metadata["runtime_enrollment_status"] != "enrolled" {
		t.Fatalf("lease patches = %#v, want one enrolled transition", leases.patches)
	}
	server, err := store.GetServerRuntime(t.Context(), "tenant-1", "server-1")
	if err != nil {
		t.Fatal(err)
	}
	if server.LifecycleState != string(serverregistry.LifecycleActive) || server.ConnectionState != string(serverregistry.ConnectionConnected) {
		t.Fatalf("server state = %#v, want active and connected", server)
	}
	if err := handler.projectServerHeartbeat(t.Context(), worker, "server-1", "lease-1", now.Add(2*time.Minute), "guard-heartbeat", guardEventPosition{
		Epoch: "epoch-a", Sequence: 2, ObservedAt: now.Add(2 * time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	if len(leases.patches) != 1 {
		t.Fatalf("repeated connected observation renewed enrollment %d times, want one transition", len(leases.patches))
	}
}

func TestGuardObservationRetriesLeaseEnrollmentAfterProjectionFailure(t *testing.T) {
	store := controlplane.NewMemoryStore()
	leases := &managedRuntimeEnrollmentRecorder{
		lease: vmlease.Lease{
			ID:       "lease-1",
			Metadata: map[string]string{"runtime_enrollment_status": "pending"},
		},
		patchErrors: 1,
	}
	handler := workerRouteHandlers{serverStore: store, managedRuntimeLeases: leases}
	now := time.Date(2026, 8, 14, 14, 0, 0, 0, time.UTC)
	worker := controlplane.Worker{
		ID: "guard-1", TenantID: "tenant-1", StackID: "stack-1", OwnerSubjectID: "owner-1", Hostname: "runtime-1",
	}
	if err := handler.projectServerEnrollment(t.Context(), worker, "server-1", "lease-1", now, "pairing-redemption"); err != nil {
		t.Fatal(err)
	}
	position := guardEventPosition{Epoch: "epoch-a", Sequence: 1, ObservedAt: now.Add(time.Minute)}
	if err := handler.projectServerHeartbeat(t.Context(), worker, "server-1", "lease-1", position.ObservedAt, "guard-heartbeat", position); err == nil {
		t.Fatal("first heartbeat unexpectedly succeeded despite lease projection failure")
	}
	if err := handler.projectServerHeartbeat(t.Context(), worker, "server-1", "lease-1", position.ObservedAt, "guard-heartbeat", position); err != nil {
		t.Fatalf("replayed heartbeat did not retry lease enrollment: %v", err)
	}
	if got := leases.lease.Metadata["runtime_enrollment_status"]; got != "enrolled" {
		t.Fatalf("lease enrollment status = %q, want enrolled", got)
	}
	if len(leases.patches) != 2 {
		t.Fatalf("lease patch attempts = %d, want failed attempt plus retry", len(leases.patches))
	}
	server, err := store.GetServerRuntime(t.Context(), "tenant-1", "server-1")
	if err != nil {
		t.Fatal(err)
	}
	if server.LifecycleState != string(serverregistry.LifecycleActive) || server.ConnectionState != string(serverregistry.ConnectionConnected) {
		t.Fatalf("server state = %#v, want active and connected after retry", server)
	}
}

func TestWorkerHeartbeatProjectsRuntimeConvergenceWithoutRawErrors(t *testing.T) {
	store := controlplane.NewMemoryStore()
	handler := workerRouteHandlers{serverStore: store}
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	convergence := runtimeconvergence.Aggregate(now,
		runtimeconvergence.Component{Name: runtimeconvergence.TechstackRuntimeComponent, State: runtimeconvergence.ComponentReady, ObservedAt: now},
		runtimeconvergence.Component{Name: runtimeconvergence.StackKitsRuntimeComponent, State: runtimeconvergence.ComponentFailed, ObservedAt: now, ErrorCode: runtimeconvergence.StackKitsRuntimeUnavailableError},
	)
	worker := controlplane.Worker{
		ID: "guard-1", TenantID: "tenant-1", StackID: "stack-1", OwnerSubjectID: "owner-1", Hostname: "runtime-1",
		Resources:    map[string]any{"runtime_convergence": runtimeconvergence.Map(convergence)},
		Capabilities: map[string]any{"runtime_convergence": runtimeconvergence.Map(convergence)},
	}
	if err := handler.projectServerEnrollment(t.Context(), worker, "server-1", "lease-1", now, "pairing-redemption"); err != nil {
		t.Fatal(err)
	}
	if err := handler.projectServerHeartbeat(t.Context(), worker, "server-1", "lease-1", now.Add(time.Second), "guard-heartbeat", guardEventPosition{Epoch: "epoch-a", Sequence: 1, ObservedAt: now.Add(time.Second)}); err != nil {
		t.Fatal(err)
	}
	server, err := store.GetServerRuntime(t.Context(), "tenant-1", "server-1")
	if err != nil {
		t.Fatal(err)
	}
	projected, ok := server.Metadata["runtime_convergence"].(map[string]any)
	if !ok || projected["state"] != runtimeconvergence.StateDegraded {
		t.Fatalf("runtime convergence projection = %#v", server.Metadata["runtime_convergence"])
	}
	if strings.Contains(fmt.Sprint(projected), "connection refused") {
		t.Fatalf("raw executor error leaked into server metadata: %#v", projected)
	}
}

// TestRepeatedHeartbeatUpdatesEvidenceWithoutNoOpTransitions locks K2
// acceptance item 4: an unchanged heartbeat advances last_heartbeat_at and the
// source checkpoint but must not mint server_state_transitions rows.
func TestRepeatedHeartbeatUpdatesEvidenceWithoutNoOpTransitions(t *testing.T) {
	store := controlplane.NewMemoryStore()
	handler := workerRouteHandlers{serverStore: store}
	now := time.Date(2026, 8, 12, 8, 0, 0, 0, time.UTC)
	worker := controlplane.Worker{
		ID: "guard-1", TenantID: "tenant-1", StackID: "stack-1",
		OwnerSubjectID: "owner-1", Hostname: "runtime-1",
	}
	if err := handler.projectServerEnrollment(t.Context(), worker, "server-1", "lease-1", now, "pairing-redemption"); err != nil {
		t.Fatal(err)
	}
	firstBeat := now.Add(30 * time.Second)
	if err := handler.projectServerHeartbeat(t.Context(), worker, "server-1", "lease-1", firstBeat, "guard-heartbeat", guardEventPosition{
		Epoch: "epoch-a", Sequence: 1, ObservedAt: firstBeat,
	}); err != nil {
		t.Fatal(err)
	}
	transitionsAfterFirst, err := store.ListServerTransitions(t.Context(), "tenant-1", "server-1", 100)
	if err != nil {
		t.Fatal(err)
	}

	secondBeat := firstBeat.Add(30 * time.Second)
	if err := handler.projectServerHeartbeat(t.Context(), worker, "server-1", "lease-1", secondBeat, "guard-heartbeat", guardEventPosition{
		Epoch: "epoch-a", Sequence: 2, ObservedAt: secondBeat,
	}); err != nil {
		t.Fatal(err)
	}
	transitionsAfterSecond, err := store.ListServerTransitions(t.Context(), "tenant-1", "server-1", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(transitionsAfterSecond) != len(transitionsAfterFirst) {
		t.Fatalf("unchanged heartbeat minted transitions: %d -> %d (%#v)", len(transitionsAfterFirst), len(transitionsAfterSecond), transitionsAfterSecond)
	}
	server, err := store.GetServerRuntime(t.Context(), "tenant-1", "server-1")
	if err != nil {
		t.Fatal(err)
	}
	if server.LastHeartbeatAt == nil || !server.LastHeartbeatAt.Equal(secondBeat) || server.SourceSequence != 2 {
		t.Fatalf("second heartbeat did not advance evidence: %#v", server)
	}
	if server.ConnectionState != string(serverregistry.ConnectionConnected) || server.HealthState != string(serverregistry.HealthHealthy) {
		t.Fatalf("second heartbeat changed state: %#v", server)
	}
}

func TestLateWorkerHeartbeatCannotResurrectDecommissioningServer(t *testing.T) {
	store := controlplane.NewMemoryStore()
	handler := workerRouteHandlers{serverStore: store}
	now := time.Date(2026, 7, 17, 10, 0, 0, 0, time.UTC)
	worker := controlplane.Worker{
		ID: "guard-1", TenantID: "tenant-1", StackID: "stack-1",
		OwnerSubjectID: "owner-1", Hostname: "runtime-1",
	}
	if err := handler.projectServerEnrollment(t.Context(), worker, "server-1", "lease-1", now, "pairing-redemption"); err != nil {
		t.Fatal(err)
	}
	if err := handler.projectServerHeartbeat(t.Context(), worker, "server-1", "lease-1", now.Add(time.Minute), "guard-heartbeat", guardEventPosition{
		Epoch: "epoch-a", Sequence: 1, ObservedAt: now.Add(time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	active, err := store.GetServerRuntime(t.Context(), "tenant-1", "server-1")
	if err != nil {
		t.Fatal(err)
	}
	decommissioning, err := store.ApplyServerEvent(t.Context(), controlplane.ServerEvent{
		TenantID: "tenant-1", ServerID: "server-1", ExpectedRevision: active.Revision, Generation: active.Generation,
		Authority: controlplane.ServerEventAuthorityControlPlane, Source: "lifecycle-controller", SourceID: "lifecycle-controller",
		ObservedAt: now.Add(2 * time.Minute), Runtime: controlplane.ServerRuntime{
			LifecycleState: string(serverregistry.LifecycleDecommissioning),
			DesiredState:   string(serverregistry.DesiredAbsent),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if heartbeatErr := handler.projectServerHeartbeat(t.Context(), worker, "server-1", "lease-1", now.Add(3*time.Minute), "guard-heartbeat", guardEventPosition{
		Epoch: "epoch-a", Sequence: 2, ObservedAt: now.Add(3 * time.Minute),
	}); heartbeatErr != nil {
		t.Fatal(heartbeatErr)
	}
	got, err := store.GetServerRuntime(t.Context(), "tenant-1", "server-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.LifecycleState != string(serverregistry.LifecycleDecommissioning) || got.Revision != decommissioning.Server.Revision {
		t.Fatalf("late heartbeat resurrected or mutated server: %#v", got)
	}
}

func TestGuardHeartbeatCannotRevertControlPlaneOwnerTransfer(t *testing.T) {
	store := controlplane.NewMemoryStore()
	handler := workerRouteHandlers{serverStore: store}
	now := time.Date(2026, 7, 17, 11, 0, 0, 0, time.UTC)
	staleWorker := controlplane.Worker{
		ID: "guard-1", TenantID: "tenant-1", InstanceID: "instance-1", StackID: "stack-1",
		OwnerSubjectID: "owner-old", Hostname: "runtime-1", Provider: "centron",
	}
	if err := handler.projectServerEnrollment(t.Context(), staleWorker, "server-1", "lease-1", now, "pairing-redemption"); err != nil {
		t.Fatal(err)
	}
	current, err := store.GetServerRuntime(t.Context(), "tenant-1", "server-1")
	if err != nil {
		t.Fatal(err)
	}
	transferred, err := store.ApplyServerEvent(t.Context(), controlplane.ServerEvent{
		TenantID: "tenant-1", ServerID: "server-1", ExpectedRevision: current.Revision, Generation: current.Generation,
		Authority: controlplane.ServerEventAuthorityControlPlane, Source: "owner-transfer", SourceID: "owner-transfer",
		ObservedAt: now.Add(time.Minute), Runtime: controlplane.ServerRuntime{OwnerSubjectID: "owner-new"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if heartbeatErr := handler.projectServerHeartbeat(t.Context(), staleWorker, "server-1", "lease-stale", now.Add(2*time.Minute), "guard-heartbeat", guardEventPosition{
		Epoch: "epoch-a", Sequence: 1, ObservedAt: now.Add(2 * time.Minute),
	}); heartbeatErr != nil {
		t.Fatalf("stale worker authority fields caused heartbeat conflict: %v", heartbeatErr)
	}
	got, err := store.GetServerRuntime(t.Context(), "tenant-1", "server-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.OwnerSubjectID != "owner-new" || got.StackID != "stack-1" || got.LeaseID != "lease-1" || got.ProviderRef != "centron" {
		t.Fatalf("Guard reverted control-plane bindings after owner transfer: %#v (transfer %#v)", got, transferred.Server)
	}
}

func TestWorkerInventoryRevisionsAndDegradesCanonicalServer(t *testing.T) {
	store := controlplane.NewMemoryStore()
	handler := workerRouteHandlers{serverStore: store}
	now := time.Date(2026, 7, 17, 9, 0, 0, 0, time.UTC)
	worker := controlplane.Worker{ID: "guard-1", TenantID: "tenant-1", StackID: "stack-1", OwnerSubjectID: "owner-1"}
	req := workerInventoryRequest{
		SourceEpoch: "epoch-a", SourceSequence: 1, ObservedAt: now,
		Hostname: "runtime-1", AgentVersion: "1.2.3",
		StackKit: "cloud-kit", StackKitVersion: "4.2.1", StackKitMode: "simple", Domain: "base.demo.kombify.me",
		Host: workerInventoryHost{
			Hostname: "runtime-1", OS: "ubuntu", OSVersion: "24.04", Arch: "amd64",
			PublicIP: "203.0.113.10", CPUCores: 4, RAMMB: 8192, DiskGB: 120,
		},
		Services:  []workerInventoryService{{ServiceID: "db", Status: "unhealthy"}},
		Channels:  []workerInventoryChannel{{Kind: "grpc-mtls", Status: "connected", Provenance: "guard"}, {Kind: "ssh", Status: "available", Provenance: "operator"}},
		Endpoints: []workerInventoryEndpoint{{URL: "https://base.demo.kombify.me", Visibility: "public", Provenance: "stackkit-access-manifest"}},
	}

	if err := handler.projectServerEnrollment(t.Context(), worker, "server-1", "", now.Add(-time.Minute), "pairing-redemption"); err != nil {
		t.Fatalf("project enrollment: %v", err)
	}
	if _, err := handler.projectServerInventory(t.Context(), worker, "server-1", "", req, now); err != nil {
		t.Fatalf("project inventory: %v", err)
	}
	server, err := store.GetServerRuntime(t.Context(), "tenant-1", "server-1")
	if err != nil {
		t.Fatalf("get server: %v", err)
	}
	if server.InventoryRevision != 1 || server.HealthState != string(serverregistry.HealthDegraded) || len(server.Channels) != 2 || server.Channels[0].Role != "primary" {
		t.Fatalf("unexpected inventory projection: %#v", server)
	}
	host, ok := mapFromJSONAny(server.Metadata["host"])
	if !ok || stringFromAnyMap(host, "os_version") != "24.04" || stringFromAnyMap(host, "public_ip") != "203.0.113.10" {
		t.Fatalf("canonical host metadata = %#v", server.Metadata)
	}
	if stringFromAnyMap(server.Metadata, "inventory_source") != "guard-inventory" || stringFromAnyMap(server.Metadata, "stackkit") != "cloud-kit" || stringFromAnyMap(server.Metadata, "stackkit_version") != "4.2.1" || stringFromAnyMap(server.Metadata, "domain") != "base.demo.kombify.me" {
		t.Fatalf("canonical deployment metadata = %#v", server.Metadata)
	}
	if expected, ok := numericInt(server.Metadata["service_projection_expected"]); !ok || expected != 1 {
		t.Fatalf("service projection gate metadata = %#v, want expected count 1", server.Metadata)
	}

	req.Services[0].Status = "healthy"
	req.SourceSequence = 2
	req.ObservedAt = now.Add(time.Minute)
	if _, projectErr := handler.projectServerInventory(t.Context(), worker, "server-1", "", req, now.Add(time.Minute)); projectErr != nil {
		t.Fatalf("project second inventory: %v", projectErr)
	}
	server, err = store.GetServerRuntime(t.Context(), "tenant-1", "server-1")
	if err != nil {
		t.Fatalf("get second server state: %v", err)
	}
	if server.InventoryRevision != 2 || server.HealthState != string(serverregistry.HealthHealthy) {
		t.Fatalf("second inventory did not advance revision and health: %#v", server)
	}
}

func TestGuardObservationRequiresExplicitEpochSequenceAndTime(t *testing.T) {
	store := controlplane.NewMemoryStore()
	handler := workerRouteHandlers{serverStore: store}
	now := time.Date(2026, 7, 21, 15, 0, 0, 0, time.UTC)
	worker := controlplane.Worker{ID: "guard-1", TenantID: "tenant-1", StackID: "stack-1", OwnerSubjectID: "owner-1"}
	if err := handler.projectServerEnrollment(t.Context(), worker, "server-1", "", now, "enrollment"); err != nil {
		t.Fatal(err)
	}
	if err := handler.projectServerHeartbeat(t.Context(), worker, "server-1", "", now, "guard-heartbeat", guardEventPosition{}); err == nil {
		t.Fatal("heartbeat without an explicit source position was accepted")
	}
	server, err := store.GetServerRuntime(t.Context(), "tenant-1", "server-1")
	if err != nil {
		t.Fatal(err)
	}
	if server.Revision != 1 || server.ConnectionState != string(serverregistry.ConnectionPending) {
		t.Fatalf("invalid observation mutated aggregate: %#v", server)
	}
}

func TestCanonicalInventoryChannelsDropsUnboundedLabelsAndSecrets(t *testing.T) {
	now := time.Date(2026, 7, 21, 14, 30, 0, 0, time.UTC)
	channels := canonicalInventoryChannels([]workerInventoryChannel{
		{Kind: "grpc-mtls", Status: "CONNECTED", Provenance: "guard"},
		{Kind: "credential-token", Status: "secret", Provenance: "opaque-secret"},
		{Kind: "ssh", Status: "unexpected", Provenance: "opaque-secret"},
	}, now)
	if len(channels) != 2 {
		t.Fatalf("canonical channels = %#v, want two admitted channel types", channels)
	}
	if channels[0].Type != "grpc-mtls" || channels[0].State != "connected" || channels[0].Metadata["provenance"] != "guard" {
		t.Fatalf("canonical primary channel = %#v", channels[0])
	}
	if channels[1].Type != "ssh" || channels[1].State != "unknown" || len(channels[1].Metadata) != 0 {
		t.Fatalf("unsafe labels survived canonicalization: %#v", channels[1])
	}
}
