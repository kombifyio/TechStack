package controlplane

import (
	"context"
	"errors"
	"testing"
)

func TestMemoryDriftResultsRequireExactTenantAndOwner(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	if _, err := store.CreateStack(ctx, CreateStackRequest{
		ID: "stack-1", TenantID: "tenant-1", OwnerSubjectID: "owner-1", Name: "Homelab",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateDriftResult(ctx, DriftResult{
		ID: "foreign", TenantID: "tenant-1", OwnerSubjectID: "owner-2", StackID: "stack-1",
	}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign CreateDriftResult() error = %v, want ErrNotFound", err)
	}
	created, err := store.CreateDriftResult(ctx, DriftResult{
		ID: "drift-1", TenantID: "tenant-1", OwnerSubjectID: "owner-1", StackID: "stack-1",
		Status: "drifted", AffectedResources: []map[string]any{{"address": "server.web"}},
	})
	if err != nil || created.OwnerSubjectID != "owner-1" {
		t.Fatalf("CreateDriftResult() = %#v, %v", created, err)
	}
	for _, scope := range [][2]string{{"tenant-2", "owner-1"}, {"tenant-1", "owner-2"}} {
		if _, err := store.GetDriftResult(ctx, scope[0], scope[1], created.ID); !errors.Is(err, ErrNotFound) {
			t.Fatalf("GetDriftResult(%q, %q) error = %v, want ErrNotFound", scope[0], scope[1], err)
		}
		if results, total, err := store.ListDriftResults(ctx, scope[0], scope[1], "", 50, 0); err != nil || total != 0 || len(results) != 0 {
			t.Fatalf("ListDriftResults(%q, %q) = %#v, %d, %v", scope[0], scope[1], results, total, err)
		}
		if err := store.DeleteDriftResult(ctx, scope[0], scope[1], created.ID); !errors.Is(err, ErrNotFound) {
			t.Fatalf("DeleteDriftResult(%q, %q) error = %v, want ErrNotFound", scope[0], scope[1], err)
		}
	}
	if err := store.DeleteDriftResult(ctx, "tenant-1", "owner-1", created.ID); err != nil {
		t.Fatal(err)
	}
}
