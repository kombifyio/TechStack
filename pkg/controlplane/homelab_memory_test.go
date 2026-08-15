package controlplane

import (
	"context"
	"errors"
	"testing"
)

func TestMemoryStoreHomelabOwnerSingleton(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()

	first, err := store.CreateHomelab(ctx, CreateHomelabRequest{
		ID:             "hl-1",
		TenantID:       "tenant-1",
		OwnerSubjectID: "auth0|user-1",
		Name:           "My Homelab",
	})
	if err != nil {
		t.Fatalf("CreateHomelab: %v", err)
	}

	if _, conflictErr := store.CreateHomelab(ctx, CreateHomelabRequest{
		ID:             "hl-2",
		TenantID:       "tenant-1",
		OwnerSubjectID: "auth0|user-1",
		Name:           "Second Homelab",
	}); !errors.Is(conflictErr, ErrConflict) {
		t.Fatalf("expected ErrConflict for second active homelab, got %v", conflictErr)
	}

	// A different owner in the same tenant gets their own homelab.
	if _, otherOwnerErr := store.CreateHomelab(ctx, CreateHomelabRequest{
		ID:             "hl-3",
		TenantID:       "tenant-1",
		OwnerSubjectID: "auth0|user-2",
		Name:           "Other Homelab",
	}); otherOwnerErr != nil {
		t.Fatalf("CreateHomelab for second owner: %v", otherOwnerErr)
	}

	got, err := store.GetHomelabByOwner(ctx, "tenant-1", "auth0|user-1")
	if err != nil {
		t.Fatalf("GetHomelabByOwner: %v", err)
	}
	if got.ID != first.ID {
		t.Fatalf("expected %q, got %q", first.ID, got.ID)
	}
}

func TestMemoryStoreHomelabTenantScoping(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()

	if _, err := store.CreateHomelab(ctx, CreateHomelabRequest{
		ID:             "hl-1",
		TenantID:       "tenant-1",
		OwnerSubjectID: "auth0|user-1",
		Name:           "My Homelab",
	}); err != nil {
		t.Fatalf("CreateHomelab: %v", err)
	}

	if _, err := store.GetHomelabByOwner(ctx, "tenant-2", "auth0|user-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound across tenants, got %v", err)
	}
	if _, err := store.UpdateHomelabIntent(ctx, "tenant-2", "hl-1", map[string]any{}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound for cross-tenant update, got %v", err)
	}
}

func TestMemoryStoreGetOrCreateHomelabIsIdempotentPerOwner(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()

	first, err := store.GetOrCreateHomelabForOwner(ctx, CreateHomelabRequest{
		ID:             "hl-1",
		TenantID:       "tenant-1",
		OwnerSubjectID: "auth0|user-1",
		Name:           "My Homelab",
	})
	if err != nil {
		t.Fatalf("GetOrCreateHomelabForOwner: %v", err)
	}

	second, err := store.GetOrCreateHomelabForOwner(ctx, CreateHomelabRequest{
		ID:             "hl-other",
		TenantID:       "tenant-1",
		OwnerSubjectID: "auth0|user-1",
		Name:           "Renamed",
	})
	if err != nil {
		t.Fatalf("second GetOrCreateHomelabForOwner: %v", err)
	}
	if second.ID != first.ID || second.Name != first.Name {
		t.Fatalf("expected existing homelab unchanged, got %#v", second)
	}
}

func TestMemoryStoreUpdateHomelabIntentClonesPayload(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()

	if _, err := store.CreateHomelab(ctx, CreateHomelabRequest{
		ID:             "hl-1",
		TenantID:       "tenant-1",
		OwnerSubjectID: "auth0|user-1",
		Name:           "My Homelab",
	}); err != nil {
		t.Fatalf("CreateHomelab: %v", err)
	}

	intent := map[string]any{"goals": []any{"photos"}}
	updated, err := store.UpdateHomelabIntent(ctx, "tenant-1", "hl-1", intent)
	if err != nil {
		t.Fatalf("UpdateHomelabIntent: %v", err)
	}
	intent["goals"] = "mutated"
	updated.Intent["injected"] = true

	fresh, err := store.GetHomelabByOwner(ctx, "tenant-1", "auth0|user-1")
	if err != nil {
		t.Fatalf("GetHomelabByOwner: %v", err)
	}
	goals, ok := fresh.Intent["goals"].([]any)
	if !ok || len(goals) != 1 || goals[0] != "photos" {
		t.Fatalf("stored intent aliased caller memory: %#v", fresh.Intent)
	}
	if _, leaked := fresh.Intent["injected"]; leaked {
		t.Fatalf("returned clone aliased stored intent: %#v", fresh.Intent)
	}
}

func TestMemoryStoreUpdateHomelabNameRecordsThatTheOwnerChoseIt(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()

	created, err := store.CreateHomelab(ctx, CreateHomelabRequest{
		ID:             "hl-1",
		TenantID:       "tenant-1",
		OwnerSubjectID: "auth0|user-1",
		Name:           defaultGeneratedHomelabTestName,
	})
	if err != nil {
		t.Fatalf("CreateHomelab: %v", err)
	}
	if created.NamedAt != nil {
		t.Fatalf("a generated name must not be recorded as chosen: %v", created.NamedAt)
	}

	// Renaming to exactly the generated word is a real choice: readers must be
	// able to tell it apart from the untouched default, which a string compare
	// against the name can never do.
	renamed, err := store.UpdateHomelabName(ctx, "tenant-1", "hl-1", defaultGeneratedHomelabTestName)
	if err != nil {
		t.Fatalf("UpdateHomelabName: %v", err)
	}
	if renamed.NamedAt == nil {
		t.Fatal("rename did not record that the owner chose the name")
	}
	if renamed.Name != defaultGeneratedHomelabTestName {
		t.Fatalf("name = %q, want %q", renamed.Name, defaultGeneratedHomelabTestName)
	}

	fresh, err := store.GetHomelabByOwner(ctx, "tenant-1", "auth0|user-1")
	if err != nil {
		t.Fatalf("GetHomelabByOwner: %v", err)
	}
	if fresh.NamedAt == nil {
		t.Fatal("chosen-name marker did not survive the read")
	}
}

// defaultGeneratedHomelabTestName mirrors the name both generators emit
// (migration 044's backfill and the wizard lane's defaultCreateStackName).
const defaultGeneratedHomelabTestName = "homelab"
