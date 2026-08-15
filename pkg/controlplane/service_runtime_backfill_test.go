package controlplane

import (
	"testing"
	"time"
)

func TestMemoryStoreServiceRuntimeReadThroughBackfillIsUnknownAndMeasuredRowWins(t *testing.T) {
	store := NewMemoryStore()
	ctx := t.Context()
	now := time.Date(2026, 7, 17, 14, 0, 0, 0, time.UTC)
	store.SetNow(func() time.Time { return now })
	if _, err := store.UpsertServerRuntime(ctx, ServerRuntime{
		ID: "server-1", TenantID: "tenant-1", StackID: "stack-1", NodeID: "legacy-node-1", Name: "primary",
	}); err != nil {
		t.Fatalf("UpsertServerRuntime: %v", err)
	}
	if _, err := store.UpsertService(ctx, Service{
		ID: "legacy-service-1", TenantID: "tenant-1", StackID: "stack-1", NodeID: "legacy-node-1",
		ServiceKey: " Vaultwarden ", Name: "Vaultwarden", Status: "healthy", Source: "legacy-registry",
	}); err != nil {
		t.Fatalf("UpsertService: %v", err)
	}

	rows, err := store.ListServiceRuntimes(ctx, "tenant-1", "stack-1", "server-1")
	if err != nil || len(rows) != 1 {
		t.Fatalf("backfill rows = %#v err=%v", rows, err)
	}
	backfilled := rows[0]
	if backfilled.ID != "legacy-service-1" || backfilled.ServerID != "server-1" || backfilled.ServiceKey != "vaultwarden" {
		t.Fatalf("unexpected backfill identity: %#v", backfilled)
	}
	if backfilled.HealthState != legacyServiceRuntimeUnknown || backfilled.ObservedState != legacyServiceRuntimeUnknown || backfilled.ObservedAt != nil || backfilled.Access[legacyServiceAccessModeKey] != legacyServiceRuntimeUnavailable {
		t.Fatalf("legacy status became measured state: %#v", backfilled)
	}
	if backfilled.Metadata["backfill"] != true || backfilled.Source != legacyServiceRuntimeBackfillSource {
		t.Fatalf("missing backfill provenance: %#v", backfilled)
	}
	if got, getErr := store.GetServiceRuntime(ctx, "tenant-1", "legacy-service-1"); getErr != nil || got.Source != legacyServiceRuntimeBackfillSource {
		t.Fatalf("GetServiceRuntime backfill = %#v err=%v", got, getErr)
	}

	observedAt := now
	if _, upsertErr := store.UpsertServiceRuntime(ctx, ServiceRuntime{
		ID: "service-stable", TenantID: "tenant-1", StackID: "stack-1", ServerID: "server-1",
		ServiceKey: "vaultwarden", ServiceInstance: "default", Name: "Vaultwarden",
		ObservedState: "running", HealthState: "healthy", ObservedAt: &observedAt, Source: "stackkits-inventory",
	}); upsertErr != nil {
		t.Fatalf("UpsertServiceRuntime: %v", upsertErr)
	}
	rows, err = store.ListServiceRuntimes(ctx, "tenant-1", "stack-1", "server-1")
	if err != nil || len(rows) != 1 || rows[0].ID != "service-stable" {
		t.Fatalf("measured row did not supersede backfill: %#v err=%v", rows, err)
	}
}

func TestLegacyServiceDesiredState(t *testing.T) {
	if got := legacyServiceDesiredState(legacyServiceDesiredStopped); got != legacyServiceDesiredStopped {
		t.Fatalf("stopped desired state = %q", got)
	}
	if got := legacyServiceDesiredState("healthy"); got != legacyServiceDesiredRunning {
		t.Fatalf("healthy desired state = %q", got)
	}
}
