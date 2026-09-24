package controlplane

import (
	"context"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/outcome"
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

func TestMemoryStoreLegacyUpsertPreservesServerOutcome(t *testing.T) {
	store := NewMemoryStore()
	now := time.Date(2026, 8, 26, 10, 0, 0, 0, time.UTC)
	store.SetNow(func() time.Time { return now })
	decision := &outcome.Decision{
		Status: outcome.StatusPending, ReasonCode: "awaiting_guard_heartbeat", Retryable: false,
		UserGuidance: &outcome.Guidance{Title: "Connection pending", Body: "Techstack is waiting for Guard.", NextSteps: []outcome.Step{{ID: "wait", Label: "Keep the server online.", Kind: "note"}}},
	}
	created, err := store.UpsertServerRuntime(t.Context(), ServerRuntime{
		ID: "server-1", TenantID: "tenant-1", OwnerSubjectID: "owner-1", LastOutcome: decision,
	})
	if err != nil || created.LastOutcome == nil {
		t.Fatalf("seed outcome: server=%#v err=%v", created, err)
	}

	legacy := *created
	legacy.LastOutcome = nil
	legacy.OutcomeChangedAt = nil
	updated, err := store.UpsertServerRuntime(t.Context(), legacy)
	if err != nil || updated.LastOutcome == nil || updated.LastOutcome.ReasonCode != "awaiting_guard_heartbeat" {
		t.Fatalf("legacy upsert erased outcome: server=%#v err=%v", updated, err)
	}
}
