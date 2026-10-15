package controlplane

import (
	"testing"

	"github.com/kombifyio/techstack/pkg/serviceregistry"
)

// Regression (live 2026-10-02): a service whose placement had been cleared to
// `unknown` made every later Guard inventory from its server fail with
// "unknown placement cannot carry server or managed-workload fields", which
// froze the whole server's service inventory. The Guard observation must bind
// the service back to the server it reports from.
func TestGuardObservationRebindsClearedPlacementToReportingServer(t *testing.T) {
	store := NewMemoryStore()
	ctx := t.Context()
	if _, err := store.UpsertServiceRuntime(ctx, ServiceRuntime{
		ID: "service-1", TenantID: "tenant-1", StackID: "stack-1", ServiceKey: "docker/hermes-webui",
		Source: "observed", ManagementState: "observed", ObservedState: "running",
		Placement: serviceregistry.Placement{TargetKind: serviceregistry.TargetKindUnknown},
	}); err != nil {
		t.Fatal(err)
	}
	got, err := store.ApplyServiceEvent(ctx, ServiceEvent{
		TenantID: "tenant-1", ServiceID: "service-1", Authority: ServiceEventAuthorityGuard,
		Runtime: ServiceRuntime{ServerID: "server-1", ObservedState: "running"},
	})
	if err != nil {
		t.Fatalf("guard observation rejected: %v", err)
	}
	if got.Service.ServerID != "server-1" || got.Service.Placement.TargetKind != serviceregistry.TargetKindServer {
		t.Fatalf("placement = %q on %q, want server on server-1", got.Service.Placement.TargetKind, got.Service.ServerID)
	}
}
