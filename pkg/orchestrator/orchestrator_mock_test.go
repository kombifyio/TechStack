package orchestrator

import (
	"context"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/runtimehealth"
	"github.com/kombifyio/techstack/pkg/runtimeidentity"
)

func seedStoreWorkerWithStatus(t *testing.T, store *controlplane.MemoryStore, id, tenantID, ownerID, stackID, status string, approved bool) controlplane.Worker {
	t.Helper()
	now := time.Now().UTC()
	worker := controlplane.Worker{
		ID:             id,
		TenantID:       tenantID,
		StackID:        stackID,
		Hostname:       id + "-host",
		OS:             "linux",
		Arch:           "amd64",
		Status:         status,
		Approved:       approved,
		LastSeenAt:     &now,
		CPUCores:       8,
		RAMMB:          16384,
		OwnerSubjectID: ownerID,
	}
	if approved {
		worker.ApprovedAt = &now
	}
	if _, err := store.UpsertWorkerHeartbeat(context.Background(), worker); err != nil {
		t.Fatalf("seed worker: %v", err)
	}
	return worker
}

func seedDeployEligibleServerRuntime(t *testing.T, store *controlplane.MemoryStore, worker controlplane.Worker, heartbeatAt time.Time) {
	t.Helper()
	_, err := store.UpsertServerRuntime(context.Background(), controlplane.ServerRuntime{
		ID:              "server-" + worker.ID,
		TenantID:        worker.TenantID,
		StackID:         worker.StackID,
		OwnerSubjectID:  worker.OwnerSubjectID,
		WorkerID:        worker.ID,
		Name:            worker.Hostname,
		LifecycleState:  "active",
		ConnectionState: "connected",
		HealthState:     "healthy",
		LastHeartbeatAt: &heartbeatAt,
	})
	if err != nil {
		t.Fatalf("seed canonical server runtime: %v", err)
	}
}

func seedManagedDeployEligibleServerRuntime(
	t *testing.T,
	store *controlplane.MemoryStore,
	tenantID, ownerID, stackID, leaseID string,
	heartbeatAt time.Time,
) {
	t.Helper()
	_, err := store.UpsertServerRuntime(context.Background(), controlplane.ServerRuntime{
		ID:              runtimeidentity.LeaseServerID(leaseID),
		TenantID:        tenantID,
		StackID:         stackID,
		OwnerSubjectID:  ownerID,
		WorkerID:        "guard-" + leaseID,
		LeaseID:         leaseID,
		Name:            "managed-" + leaseID,
		LifecycleState:  "active",
		ConnectionState: "connected",
		HealthState:     "healthy",
		LastHeartbeatAt: &heartbeatAt,
	})
	if err != nil {
		t.Fatalf("seed managed canonical server runtime: %v", err)
	}
}

func TestWorkerIsDeployCandidate(t *testing.T) {
	for _, tc := range []struct {
		name     string
		status   string
		approved bool
		want     bool
	}{
		{name: "approved assignment", status: "approved", approved: true, want: true},
		{name: "connected legacy projection", status: "connected", approved: true, want: true},
		{name: "pending", status: "pending", approved: true},
		{name: "not approved", status: "connected"},
		{name: "unknown status", approved: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			worker := controlplane.Worker{Status: tc.status, Approved: tc.approved}
			if got := workerIsDeployCandidate(worker); got != tc.want {
				t.Fatalf("workerIsDeployCandidate() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestServerRuntimeAllowsWorkerDeploy(t *testing.T) {
	now := time.Now().UTC()
	worker := controlplane.Worker{
		ID: "worker-1", TenantID: "tenant-1", StackID: "stack-1",
		OwnerSubjectID: "owner-1", Status: "approved", Approved: true,
	}
	base := controlplane.ServerRuntime{
		ID: "server-1", TenantID: worker.TenantID, StackID: worker.StackID,
		OwnerSubjectID: worker.OwnerSubjectID, WorkerID: worker.ID,
		LifecycleState: "active", ConnectionState: "connected", HealthState: "healthy",
	}
	for _, tc := range []struct {
		name   string
		mutate func(*controlplane.Worker, *controlplane.ServerRuntime)
		age    time.Duration
		seen   bool
		want   bool
	}{
		{name: "fresh healthy", age: runtimehealth.FreshHeartbeatWindow, seen: true, want: true},
		{name: "fresh degraded", age: time.Second, seen: true, want: true, mutate: func(_ *controlplane.Worker, runtime *controlplane.ServerRuntime) {
			runtime.ConnectionState, runtime.HealthState = "degraded", "degraded"
		}},
		{name: "approval without heartbeat", seen: false},
		{name: "stale heartbeat", age: runtimehealth.FreshHeartbeatWindow + time.Second, seen: true},
		{name: "future heartbeat", age: -31 * time.Second, seen: true},
		{name: "inactive lifecycle", age: time.Second, seen: true, mutate: func(_ *controlplane.Worker, runtime *controlplane.ServerRuntime) {
			runtime.LifecycleState = "enrolling"
		}},
		{name: "pending connection", age: time.Second, seen: true, mutate: func(_ *controlplane.Worker, runtime *controlplane.ServerRuntime) {
			runtime.ConnectionState = "pending"
		}},
		{name: "unknown health", age: time.Second, seen: true, mutate: func(_ *controlplane.Worker, runtime *controlplane.ServerRuntime) {
			runtime.HealthState = "unknown"
		}},
		{name: "wrong worker binding", age: time.Second, seen: true, mutate: func(_ *controlplane.Worker, runtime *controlplane.ServerRuntime) {
			runtime.WorkerID = "worker-other"
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			candidate, runtime := worker, base
			if tc.seen {
				heartbeatAt := now.Add(-tc.age)
				runtime.LastHeartbeatAt = &heartbeatAt
			}
			if tc.mutate != nil {
				tc.mutate(&candidate, &runtime)
			}
			if got := serverRuntimeAllowsWorkerDeploy(candidate, runtime, now); got != tc.want {
				t.Fatalf("serverRuntimeAllowsWorkerDeploy() = %v, want %v", got, tc.want)
			}
		})
	}
}
