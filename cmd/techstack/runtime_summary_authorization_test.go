package main

import (
	"context"
	"errors"
	"github.com/kombifyio/techstack/internal/routes"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/middleware"
	"testing"
)

func TestRuntimeSummaryAuthorizationUsesOnlyExactActiveMembership(t *testing.T) {
	store := &stubAuthStore{membership: &controlplane.Membership{TenantID: "tenant-1", UserID: "owner", Status: "active", Metadata: map[string]any{"entitlements": []string{routes.InventoryEntitlementRead}}}}
	resolve := runtimeSummaryAuthorizationContext(store)
	ctx, tenant, err := resolve(context.Background(), "tenant-1", "owner")
	if err != nil || tenant != "tenant-1" {
		t.Fatal(err)
	}
	grants, _, ok := middleware.AuthorizedEntitlementsFromContext(ctx)
	if !ok || !grants.Has(routes.InventoryEntitlementRead) {
		t.Fatal("stored read grant missing")
	}
	for _, pair := range [][2]string{{"tenant-2", "owner"}, {"tenant-1", "other"}} {
		if _, _, err := resolve(context.Background(), pair[0], pair[1]); !errors.Is(err, routes.ErrInventoryAccessDenied) {
			t.Fatal("foreign membership accepted")
		}
	}
	store.membership.Status = "revoked"
	if _, _, err := resolve(context.Background(), "tenant-1", "owner"); !errors.Is(err, routes.ErrInventoryAccessDenied) {
		t.Fatal("revoked membership accepted")
	}
	if len(store.calls) != 0 {
		t.Fatal("read path created membership")
	}
}

// Cloud signs "usr:<subject>" for an identity without an organization claim;
// the summary then reads the tenant the browser session resolves, and only
// from the signed subject's own active memberships.
func TestRuntimeSummaryPersonalTenantFollowsOwnMembership(t *testing.T) {
	store := &stubAuthStore{memberships: []controlplane.Membership{
		{TenantID: "org_home", UserID: "owner", Status: "active", Metadata: map[string]any{"entitlements": []string{routes.InventoryEntitlementRead}}},
		{TenantID: "org_other", UserID: "someone-else", Status: "active"},
	}}
	resolve := runtimeSummaryAuthorizationContext(store)
	_, tenant, err := resolve(context.Background(), "usr:owner", "owner")
	if err != nil || tenant != "org_home" {
		t.Fatalf("personal tenant resolved to %q: %v", tenant, err)
	}
	if _, _, err := resolve(context.Background(), "usr:stranger", "stranger"); !errors.Is(err, routes.ErrInventoryAccessDenied) {
		t.Fatal("identity without membership was admitted")
	}
	if _, _, err := resolve(context.Background(), "org_home", "someone-else"); !errors.Is(err, routes.ErrInventoryAccessDenied) {
		t.Fatal("signed org tenant fell back to another membership")
	}
}
