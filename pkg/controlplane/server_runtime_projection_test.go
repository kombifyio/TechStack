package controlplane

import (
	"context"
	"testing"
)

func TestMemoryStoreEnsureServerRuntimeProjectionNeverOverwritesObservedServer(t *testing.T) {
	t.Parallel()

	store := NewMemoryStore()
	projection := ServerRuntime{
		ID: "server-1", TenantID: "tenant-1", StackID: "stack-1", OwnerSubjectID: "owner-1",
		LeaseID: "lease-1", Name: "Projected", LifecycleState: "planned", ConnectionState: "pending", HealthState: "unknown",
	}
	createdServer, created, err := store.EnsureServerRuntimeProjection(context.Background(), projection)
	if err != nil || !created || createdServer.LifecycleState != "planned" {
		t.Fatalf("first projection = %#v created=%v err=%v", createdServer, created, err)
	}
	observed := *createdServer
	observed.Name = "Observed agent"
	observed.LifecycleState = "active"
	observed.ConnectionState = "connected"
	observed.HealthState = "healthy"
	if _, upsertErr := store.UpsertServerRuntime(context.Background(), observed); upsertErr != nil {
		t.Fatal(upsertErr)
	}

	got, created, err := store.EnsureServerRuntimeProjection(context.Background(), projection)
	if err != nil || created {
		t.Fatalf("replay created=%v err=%v", created, err)
	}
	if got.Name != "Observed agent" || got.LifecycleState != "active" || got.ConnectionState != "connected" || got.HealthState != "healthy" {
		t.Fatalf("authority projection overwrote observed server: %#v", got)
	}
}
