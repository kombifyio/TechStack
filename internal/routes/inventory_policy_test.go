package routes

import (
	"context"
	"errors"
	"testing"
)

func TestSelfHostedInventoryPolicyDerivesOwnerScopeFromAuthenticatedSubject(t *testing.T) {
	policy := NewSelfHostedInventoryPolicy()
	authorization := InventoryAuthorization{
		TenantID: "tenant-1", SubjectID: "owner-1",
		ResourceType: "server", ResourceID: "server-1", Action: InventoryActionRead,
	}
	decision, err := policy.AuthorizeInventory(context.Background(), authorization)
	if err != nil {
		t.Fatalf("owner-scoped read denied: %v", err)
	}
	if !decision.ReadScope.IsOwnerScoped() || decision.ReadScope.TenantID() != "tenant-1" || decision.ReadScope.OwnerSubjectID() != "owner-1" {
		t.Fatalf("derived owner scope = %#v", decision.ReadScope)
	}

	authorization.SubjectID = ""
	if _, err := policy.AuthorizeInventory(context.Background(), authorization); !errors.Is(err, ErrInventoryAccessDenied) {
		t.Fatalf("missing authenticated subject authorized: %v", err)
	}
	authorization.SubjectID = "owner-1"
	authorization.Action = InventoryAction("unknown")
	if _, err := policy.AuthorizeInventory(context.Background(), authorization); !errors.Is(err, ErrInventoryAccessDenied) {
		t.Fatalf("unknown action authorized: %v", err)
	}
}
