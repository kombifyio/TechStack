package vmleases

import (
	"context"
	"testing"
	"time"

	"github.com/kombifyio/techstack/internal/runtimeproduct/vmlease"
)

func TestMemoryStoreListByTenantFiltersLeases(t *testing.T) {
	store := NewMemoryStore()
	now := time.Now().UTC()
	for _, lease := range []vmlease.Lease{
		testListLease("lease-b", "tenant-1", "owner-1", now),
		testListLease("lease-a", "tenant-1", "owner-1", now),
		testListLease("lease-c", "tenant-2", "owner-2", now),
	} {
		if _, err := store.Upsert(context.Background(), lease, ""); err != nil {
			t.Fatalf("upsert %s: %v", lease.ID, err)
		}
	}

	leases, err := store.ListByTenant(context.Background(), "tenant-1")
	if err != nil {
		t.Fatalf("ListByTenant returned error: %v", err)
	}
	if got := len(leases); got != 2 {
		t.Fatalf("leases = %d, want 2", got)
	}
	if leases[0].ID != "lease-a" || leases[1].ID != "lease-b" {
		t.Fatalf("leases not sorted or filtered: %#v", leases)
	}
}

func testListLease(id, tenantID, ownerID string, now time.Time) vmlease.Lease {
	return vmlease.Lease{
		ID:             vmlease.LeaseID(id),
		Subject:        vmlease.Subject{Kind: vmlease.SubjectUser, ID: ownerID, OrgID: tenantID},
		Resource:       vmlease.ResourceRef{ProviderID: "centron-managed"},
		DesiredState:   vmlease.DesiredStateRunning,
		BillingMode:    vmlease.BillingModeSubscription,
		LifecycleClass: vmlease.LifecycleClassSubscription,
		RestartPolicy:  vmlease.RestartPolicyOnUnexpectedStop,
		RecreatePolicy: vmlease.RecreatePolicyManual,
		ValidFrom:      now.Add(-time.Minute),
		ValidUntil:     now.Add(time.Hour),
		RenewedAt:      now,
	}
}
