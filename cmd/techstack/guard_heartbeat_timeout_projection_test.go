package main

import (
	"context"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/jobs"
	"github.com/kombifyio/techstack/pkg/serverregistry"
)

// Regression (platform-blicm): a managed server whose Guard never connected
// stayed "enrolling" after its provision job had given up waiting.
func TestGuardHeartbeatTimeoutFailsTheWaitingServer(t *testing.T) {
	ctx := context.Background()
	store := controlplane.NewMemoryStore()
	for _, server := range []struct {
		id, stack string
	}{{"server-waiting", "stack-a"}, {"server-other-stack", "stack-b"}} {
		if _, err := store.ApplyServerEvent(ctx, controlplane.ServerEvent{
			TenantID: "tenant-a", ServerID: server.id,
			Authority: controlplane.ServerEventAuthorityControlPlane,
			Source:    "test", SourceID: "seed", ObservedAt: time.Now().UTC(),
			Runtime: controlplane.ServerRuntime{
				ID: server.id, TenantID: "tenant-a", StackID: server.stack, OwnerSubjectID: "owner-a",
				Name: server.id, LifecycleState: string(serverregistry.LifecycleEnrolling),
				DesiredState: string(serverregistry.DesiredRunning), ConnectionState: string(serverregistry.ConnectionPending),
				HealthState: string(serverregistry.HealthUnknown), LifecycleReasonCode: "awaiting_guard_heartbeat",
			},
		}); err != nil {
			t.Fatalf("seed %s: %v", server.id, err)
		}
	}

	job := jobs.JobSnapshot{
		ID: "job-provision", Type: jobs.JobTypeProvision, TargetID: "stack-a", State: jobs.JobStateFailed,
		Payload: map[string]interface{}{"tenant_id": "tenant-a"},
		Result:  map[string]interface{}{jobs.GuardHeartbeatTimeoutReason: true},
	}
	if err := (guardHeartbeatTimeoutProjector{store: store}).ObserveTerminalJob(ctx, job); err != nil {
		t.Fatalf("ObserveTerminalJob() error = %v", err)
	}

	waiting, err := store.GetServerRuntime(ctx, "tenant-a", "server-waiting")
	if err != nil {
		t.Fatal(err)
	}
	if waiting.LifecycleState != string(serverregistry.LifecycleFailed) || waiting.LifecycleReasonCode != jobs.GuardHeartbeatTimeoutReason {
		t.Fatalf("waiting server = %s/%s, want failed/%s", waiting.LifecycleState, waiting.LifecycleReasonCode, jobs.GuardHeartbeatTimeoutReason)
	}
	other, err := store.GetServerRuntime(ctx, "tenant-a", "server-other-stack")
	if err != nil {
		t.Fatal(err)
	}
	if other.LifecycleState != string(serverregistry.LifecycleEnrolling) {
		t.Fatalf("another stack's server was changed to %s", other.LifecycleState)
	}
}
