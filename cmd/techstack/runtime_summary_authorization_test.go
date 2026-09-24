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
	ctx, err := resolve(context.Background(), "tenant-1", "owner")
	if err != nil {
		t.Fatal(err)
	}
	grants, _, ok := middleware.AuthorizedEntitlementsFromContext(ctx)
	if !ok || !grants.Has(routes.InventoryEntitlementRead) {
		t.Fatal("stored read grant missing")
	}
	for _, pair := range [][2]string{{"tenant-2", "owner"}, {"tenant-1", "other"}} {
		if _, err := resolve(context.Background(), pair[0], pair[1]); !errors.Is(err, routes.ErrInventoryAccessDenied) {
			t.Fatal("foreign membership accepted")
		}
	}
	store.membership.Status = "revoked"
	if _, err := resolve(context.Background(), "tenant-1", "owner"); !errors.Is(err, routes.ErrInventoryAccessDenied) {
		t.Fatal("revoked membership accepted")
	}
	if len(store.calls) != 0 {
		t.Fatal("read path created membership")
	}
}
