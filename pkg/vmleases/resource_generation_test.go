package vmleases

import (
	"errors"
	"testing"
	"time"

	"github.com/kombifyio/techstack/internal/runtimeproduct/vmlease"
	"github.com/google/uuid"
)

func TestCreateOrUpdateBindsAndPreservesAuthorityGeneration(t *testing.T) {
	now := time.Date(2026, 7, 21, 8, 0, 0, 0, time.UTC)
	store := NewMemoryStore()
	svc := NewService(store, inventoryTestServiceConfig(func() time.Time { return now }))
	req := CreateRequest{Lease: testLease(now), IdempotencyKey: "inventory-1"}
	req.Lease.Metadata = map[string]string{MetadataKeyResourceGenerationID: "caller-controlled"}
	created, err := svc.CreateOrUpdate(t.Context(), req)
	if err != nil {
		t.Fatalf("CreateOrUpdate: %v", err)
	}
	if _, parseErr := uuid.Parse(ResourceGenerationID(*created)); parseErr != nil {
		t.Fatalf("generation %q is not a UUID: %v", ResourceGenerationID(*created), parseErr)
	}
	if ResourceGenerationID(*created) == "caller-controlled" {
		t.Fatal("caller-controlled generation was accepted")
	}
	retried, err := svc.CreateOrUpdate(t.Context(), req)
	if err != nil {
		t.Fatalf("idempotent retry: %v", err)
	}
	if ResourceGenerationID(*retried) != ResourceGenerationID(*created) {
		t.Fatalf("retry generation = %q, want %q", ResourceGenerationID(*retried), ResourceGenerationID(*created))
	}
}

func TestResourceGenerationDigestIsResourceBound(t *testing.T) {
	lease := testLease(time.Date(2026, 7, 21, 8, 0, 0, 0, time.UTC))
	lease.Metadata = map[string]string{MetadataKeyResourceGenerationID: "550e8400-e29b-41d4-a716-446655440000"}
	first, err := ResourceGenerationDigest("org-1", lease)
	if err != nil {
		t.Fatal(err)
	}
	lease.Resource.EngineVMID = "different"
	second, err := ResourceGenerationDigest("org-1", lease)
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("digest did not bind provider resource identity")
	}
}

func TestMemoryStoreRejectsGenerationMutationAndStaleCancellation(t *testing.T) {
	now := time.Date(2026, 7, 21, 8, 0, 0, 0, time.UTC)
	store := NewMemoryStore()
	svc := NewService(store, inventoryTestServiceConfig(func() time.Time { return now }))
	created, err := svc.CreateOrUpdate(t.Context(), CreateRequest{Lease: testLease(now)})
	if err != nil {
		t.Fatal(err)
	}
	mutated := cloneLease(*created)
	mutated.Metadata[MetadataKeyResourceGenerationID] = "f47ac10b-58cc-4372-a567-0e02b2c3d479"
	if _, updateErr := store.Update(t.Context(), "org-1", mutated); !errors.Is(updateErr, ErrResourceGenerationImmutable) {
		t.Fatalf("generation mutation error = %v", updateErr)
	}
	cancelled, err := svc.Patch(t.Context(), "org-1", created.ID, PatchRequest{Cancel: true})
	if err != nil || cancelled.CancelledAt == nil {
		t.Fatalf("cancel = %+v, %v", cancelled, err)
	}
	stale := cloneLease(*created)
	stale.DesiredState = vmlease.DesiredStateRunning
	if _, err := store.Update(t.Context(), "org-1", stale); !errors.Is(err, ErrLeaseCancelled) {
		t.Fatalf("stale update error = %v", err)
	}
}

func TestDecommissionClaimRequiresExactDigestAndIsImmutable(t *testing.T) {
	now := time.Date(2026, 7, 21, 8, 0, 0, 0, time.UTC)
	svc := NewService(NewMemoryStore(), inventoryTestServiceConfig(func() time.Time { return now }))
	created, err := svc.CreateOrUpdate(t.Context(), CreateRequest{Lease: testLease(now)})
	if err != nil {
		t.Fatal(err)
	}
	digest, err := ResourceGenerationDigest("org-1", *created)
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := svc.Patch(t.Context(), "org-1", created.ID, PatchRequest{ClaimDecommission: true, ExpectedResourceGenerationDigest: digest})
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if claimed.Metadata[MetadataKeyDecommissionClaimDigest] != digest {
		t.Fatalf("claim = %q, want %q", claimed.Metadata[MetadataKeyDecommissionClaimDigest], digest)
	}
	if _, err := svc.Patch(t.Context(), "org-1", created.ID, PatchRequest{Metadata: map[string]string{MetadataKeyDecommissionClaimDigest: ""}}); !errors.Is(err, ErrDecommissionClaimImmutable) {
		t.Fatalf("claim mutation error = %v", err)
	}
}

func TestMemoryOperationJournalPersistsGenerationDigest(t *testing.T) {
	store := NewMemoryStore()
	event := OperationEvent{TenantID: "org-1", LeaseID: "lease-1", EventType: OperationEventDecommission, Status: OperationStatusDecommissioned, ResourceGenerationDigest: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}
	if err := store.AppendOperation(t.Context(), event); err != nil {
		t.Fatal(err)
	}
	if ok, err := store.HasConfirmedDecommission(t.Context(), "org-1", "lease-1", event.ResourceGenerationDigest); err != nil || !ok {
		t.Fatalf("HasConfirmedDecommission = %v, %v", ok, err)
	}
}
