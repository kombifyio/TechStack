package vmleases

import (
	"errors"
	"testing"
	"time"
)

func TestMemoryStoreRejectsCrossTenantLeaseIDCollision(t *testing.T) {
	now := time.Date(2026, 7, 21, 8, 0, 0, 0, time.UTC)
	store := NewMemoryStore()
	tenantA := testLease(now)
	tenantA.Metadata = map[string]string{MetadataKeyResourceGenerationID: "550e8400-e29b-41d4-a716-446655440000"}
	if _, err := store.Upsert(t.Context(), tenantA, "create"); err != nil {
		t.Fatalf("Upsert tenant A: %v", err)
	}

	tenantB := cloneLease(tenantA)
	tenantB.Subject.OrgID = "org-2"
	tenantB.Subject.ID = "user-2"
	tenantB.Metadata[MetadataKeyResourceGenerationID] = "f47ac10b-58cc-4372-a567-0e02b2c3d479"
	if _, err := store.Upsert(t.Context(), tenantB, "create"); !errors.Is(err, ErrLeaseIdentityConflict) {
		t.Fatalf("cross-tenant Upsert error = %v, want ErrLeaseIdentityConflict", err)
	}
	if _, err := store.Update(t.Context(), "org-2", tenantB); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-tenant Update error = %v, want ErrNotFound", err)
	}
	if _, err := store.Get(t.Context(), "org-2", tenantA.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-tenant Get error = %v, want ErrNotFound", err)
	}

	stored, err := store.Get(t.Context(), "org-1", tenantA.ID)
	if err != nil {
		t.Fatalf("Get tenant A: %v", err)
	}
	if got := ResourceGenerationID(*stored); got != ResourceGenerationID(tenantA) {
		t.Fatalf("tenant A generation = %q, want %q", got, ResourceGenerationID(tenantA))
	}
}

func TestMemoryStoreTenantIdempotencyKeysCannotAlias(t *testing.T) {
	now := time.Date(2026, 7, 21, 8, 0, 0, 0, time.UTC)
	store := NewMemoryStore()
	tenantA := testLease(now)
	tenantA.ID = "lease-tenant-a"
	tenantA.Subject.OrgID = "org:a"
	tenantA.Metadata = map[string]string{MetadataKeyResourceGenerationID: "550e8400-e29b-41d4-a716-446655440000"}
	tenantB := cloneLease(tenantA)
	tenantB.ID = "lease-tenant-b"
	tenantB.Subject.OrgID = "org"
	tenantB.Metadata[MetadataKeyResourceGenerationID] = "f47ac10b-58cc-4372-a567-0e02b2c3d479"

	createdA, err := store.Upsert(t.Context(), tenantA, "create")
	if err != nil {
		t.Fatalf("Upsert tenant A: %v", err)
	}
	createdB, err := store.Upsert(t.Context(), tenantB, "a:create")
	if err != nil {
		t.Fatalf("Upsert tenant B with adversarial key: %v", err)
	}
	if createdA.ID != tenantA.ID || createdB.ID != tenantB.ID {
		t.Fatalf("tenant idempotency aliased: tenant A=%s tenant B=%s", createdA.ID, createdB.ID)
	}
}
