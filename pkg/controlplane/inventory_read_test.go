package controlplane

import (
	"errors"
	"testing"
	"time"
)

func mustOwnerInventoryScope(t *testing.T, tenantID, ownerSubjectID string) InventoryReadScope {
	t.Helper()
	scope, err := NewOwnerInventoryReadScope(tenantID, ownerSubjectID)
	if err != nil {
		t.Fatalf("owner inventory scope: %v", err)
	}
	return scope
}

func TestMemoryInventoryReadStoreFiltersOwnerAndKeepsStablePageWatermark(t *testing.T) {
	store := NewMemoryStore()
	now := time.Date(2026, time.July, 21, 9, 0, 0, 0, time.UTC)
	store.SetNow(func() time.Time { return now })
	for _, stack := range []CreateStackRequest{
		{ID: "stack-owner", TenantID: "tenant-1", OwnerSubjectID: "owner-1", Name: "Owner"},
		{ID: "stack-foreign", TenantID: "tenant-1", OwnerSubjectID: "owner-2", Name: "Foreign"},
		{ID: "stack-other-tenant", TenantID: "tenant-2", OwnerSubjectID: "owner-1", Name: "Other tenant"},
	} {
		if _, err := store.CreateStack(t.Context(), stack); err != nil {
			t.Fatal(err)
		}
		now = now.Add(time.Second)
	}
	for _, server := range []ServerRuntime{
		{ID: "server-a", TenantID: "tenant-1", StackID: "stack-owner", OwnerSubjectID: "owner-1"},
		{ID: "server-b", TenantID: "tenant-1", StackID: "stack-owner", OwnerSubjectID: "owner-1"},
		{ID: "server-foreign", TenantID: "tenant-1", StackID: "stack-foreign", OwnerSubjectID: "owner-2"},
		{ID: "server-other-tenant", TenantID: "tenant-2", StackID: "stack-other-tenant", OwnerSubjectID: "owner-1"},
	} {
		if _, err := store.UpsertServerRuntime(t.Context(), server); err != nil {
			t.Fatal(err)
		}
		now = now.Add(time.Second)
	}

	first, err := store.ListInventoryServers(t.Context(), mustOwnerInventoryScope(t, "tenant-1", "owner-1"), InventoryPageRequest{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Servers) != 1 || first.Servers[0].ID != "server-a" || first.Next == nil || first.Watermark.IsZero() {
		t.Fatalf("first page = %#v", first)
	}
	if compareInventoryPageKeys(*first.Next, first.Watermark) >= 0 {
		t.Fatalf("next key %v must precede watermark %v", *first.Next, first.Watermark)
	}

	serverB, err := store.GetServerRuntime(t.Context(), "tenant-1", "server-b")
	if err != nil {
		t.Fatal(err)
	}
	serverB.Name = "updated between pages"
	if _, upsertErr := store.UpsertServerRuntime(t.Context(), *serverB); upsertErr != nil {
		t.Fatal(upsertErr)
	}
	if _, upsertErr := store.UpsertServerRuntime(t.Context(), ServerRuntime{ID: "server-new", TenantID: "tenant-1", StackID: "stack-owner", OwnerSubjectID: "owner-1"}); upsertErr != nil {
		t.Fatal(upsertErr)
	}
	second, err := store.ListInventoryServers(t.Context(), mustOwnerInventoryScope(t, "tenant-1", "owner-1"), InventoryPageRequest{
		Limit: 1, Watermark: first.Watermark, After: *first.Next,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Servers) != 1 || second.Servers[0].ID != "server-b" || second.Next != nil || second.Watermark != first.Watermark {
		t.Fatalf("second page = %#v, want stable original collection", second)
	}
	if _, err := store.GetInventoryServer(t.Context(), mustOwnerInventoryScope(t, "tenant-1", "owner-1"), "server-foreign"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign owner lookup error = %v, want not found", err)
	}
	if _, err := store.GetInventoryServer(t.Context(), mustOwnerInventoryScope(t, "tenant-1", "owner-1"), "server-other-tenant"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-tenant lookup error = %v, want not found", err)
	}
}

func TestMemoryInventoryReadStoreEnforcesPolicyIssuedTenantScopes(t *testing.T) {
	store := NewMemoryStore()
	for _, server := range []ServerRuntime{
		{ID: "server-owner-1", TenantID: "tenant-1", OwnerSubjectID: "owner-1"},
		{ID: "server-owner-2", TenantID: "tenant-1", OwnerSubjectID: "owner-2"},
		{ID: "server-other-tenant", TenantID: "tenant-2", OwnerSubjectID: "owner-1"},
	} {
		if _, err := store.UpsertServerRuntime(t.Context(), server); err != nil {
			t.Fatal(err)
		}
	}

	collectionScope, err := NewTenantInventoryCollectionReadScope("tenant-1", InventoryReadTargetServerCollection)
	if err != nil {
		t.Fatal(err)
	}
	page, err := store.ListInventoryServers(t.Context(), collectionScope, InventoryPageRequest{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Servers) != 2 {
		t.Fatalf("tenant collection = %#v, want both tenant owners only", page.Servers)
	}
	narrowed, err := collectionScope.RestrictToServer("server-owner-2")
	if err != nil {
		t.Fatal(err)
	}
	targetType, targetID := narrowed.Target()
	if targetType != InventoryReadTargetServer || targetID != "server-owner-2" {
		t.Fatalf("narrowed collection scope target = (%q,%q)", targetType, targetID)
	}

	exactScope, err := NewTenantInventoryObjectReadScope("tenant-1", InventoryReadTargetServer, "server-owner-2")
	if err != nil {
		t.Fatal(err)
	}
	server, err := store.GetInventoryServer(t.Context(), exactScope, "server-owner-2")
	if err != nil || server.OwnerSubjectID != "owner-2" {
		t.Fatalf("exact cross-owner object = %#v err=%v", server, err)
	}
	if _, lookupErr := store.GetInventoryServer(t.Context(), exactScope, "server-owner-1"); !errors.Is(lookupErr, ErrInvalidInventoryReadScope) {
		t.Fatalf("exact scope widening error = %v, want invalid scope", lookupErr)
	}
	if _, restrictErr := exactScope.RestrictToServer("server-owner-1"); !errors.Is(restrictErr, ErrInvalidInventoryReadScope) {
		t.Fatalf("exact scope renarrow error = %v, want invalid scope", restrictErr)
	}
	otherTenantScope, err := NewTenantInventoryObjectReadScope("tenant-1", InventoryReadTargetServer, "server-other-tenant")
	if err != nil {
		t.Fatal(err)
	}
	if _, lookupErr := store.GetInventoryServer(t.Context(), otherTenantScope, "server-other-tenant"); !errors.Is(lookupErr, ErrNotFound) {
		t.Fatalf("cross-tenant exact read error = %v, want not found", lookupErr)
	}

	serviceCollectionScope, err := NewTenantInventoryCollectionReadScope("tenant-1", InventoryReadTargetServiceCollection)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ListInventoryServers(t.Context(), serviceCollectionScope, InventoryPageRequest{Limit: 10}); !errors.Is(err, ErrInvalidInventoryReadScope) {
		t.Fatalf("collection target widening error = %v, want invalid scope", err)
	}
}

func TestMemoryInventoryReadStoreFiltersServicesBeforeReturningRows(t *testing.T) {
	store := NewMemoryStore()
	for _, stack := range []CreateStackRequest{
		{ID: "stack-owner", TenantID: "tenant-1", OwnerSubjectID: "owner-1", Name: "Owner"},
		{ID: "stack-foreign", TenantID: "tenant-1", OwnerSubjectID: "owner-2", Name: "Foreign"},
	} {
		if _, err := store.CreateStack(t.Context(), stack); err != nil {
			t.Fatal(err)
		}
	}
	for _, server := range []ServerRuntime{
		{ID: "server-owner", TenantID: "tenant-1", StackID: "stack-owner", OwnerSubjectID: "owner-1"},
		{ID: "server-foreign", TenantID: "tenant-1", StackID: "stack-foreign", OwnerSubjectID: "owner-2"},
	} {
		if _, err := store.UpsertServerRuntime(t.Context(), server); err != nil {
			t.Fatal(err)
		}
	}
	for _, service := range []ServiceRuntime{
		{ID: "service-owner", TenantID: "tenant-1", StackID: "stack-owner", ServerID: "server-owner", ServiceKey: "app"},
		{ID: "service-foreign", TenantID: "tenant-1", StackID: "stack-foreign", ServerID: "server-foreign", ServiceKey: "app"},
		{ID: "service-confused", TenantID: "tenant-1", StackID: "stack-foreign", ServerID: "server-owner", ServiceKey: "app"},
	} {
		if _, err := store.UpsertServiceRuntime(t.Context(), service); err != nil {
			t.Fatal(err)
		}
	}
	page, err := store.ListInventoryServices(t.Context(), mustOwnerInventoryScope(t, "tenant-1", "owner-1"), "", InventoryPageRequest{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Services) != 1 || page.Services[0].ID != "service-owner" {
		t.Fatalf("owner service page = %#v", page)
	}
}

func TestMemoryInventoryReadStoreDoesNotBackfillLegacyRegistryServices(t *testing.T) {
	store := NewMemoryStore()
	if _, err := store.CreateStack(t.Context(), CreateStackRequest{ID: "stack-1", TenantID: "tenant-1", OwnerSubjectID: "owner-1", Name: "Stack"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertServerRuntime(t.Context(), ServerRuntime{ID: "server-1", TenantID: "tenant-1", StackID: "stack-1", NodeID: "node-1", OwnerSubjectID: "owner-1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertService(t.Context(), Service{ID: "legacy-service", TenantID: "tenant-1", StackID: "stack-1", NodeID: "node-1", ServiceKey: "app", Status: "running"}); err != nil {
		t.Fatal(err)
	}
	page, err := store.ListInventoryServices(t.Context(), mustOwnerInventoryScope(t, "tenant-1", "owner-1"), "", InventoryPageRequest{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Services) != 0 {
		t.Fatalf("legacy registry rows leaked into canonical inventory: %#v", page.Services)
	}
}

func TestMemoryInventoryServiceCursorDoesNotMoveOnConcurrentUpdate(t *testing.T) {
	store := NewMemoryStore()
	now := time.Date(2026, time.July, 21, 12, 0, 0, 0, time.UTC)
	store.SetNow(func() time.Time { return now })
	if _, err := store.CreateStack(t.Context(), CreateStackRequest{
		ID: "stack-1", TenantID: "tenant-1", OwnerSubjectID: "owner-1", Name: "Stack",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertServerRuntime(t.Context(), ServerRuntime{
		ID: "server-1", TenantID: "tenant-1", StackID: "stack-1", OwnerSubjectID: "owner-1",
	}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"service-a", "service-b"} {
		now = now.Add(time.Second)
		if _, err := store.UpsertServiceRuntime(t.Context(), ServiceRuntime{
			ID: id, TenantID: "tenant-1", StackID: "stack-1", ServerID: "server-1", ServiceKey: id,
		}); err != nil {
			t.Fatal(err)
		}
	}

	first, err := store.ListInventoryServices(t.Context(), mustOwnerInventoryScope(t, "tenant-1", "owner-1"), "server-1", InventoryPageRequest{Limit: 1})
	if err != nil || len(first.Services) != 1 || first.Services[0].ID != "service-a" || first.Next == nil {
		t.Fatalf("first page = %#v, err=%v", first, err)
	}
	now = now.Add(time.Minute)
	serviceB, err := store.GetServiceRuntime(t.Context(), "tenant-1", "service-b")
	if err != nil {
		t.Fatal(err)
	}
	serviceB.HealthState = "healthy"
	if _, upsertErr := store.UpsertServiceRuntime(t.Context(), *serviceB); upsertErr != nil {
		t.Fatal(upsertErr)
	}
	if _, upsertErr := store.UpsertServiceRuntime(t.Context(), ServiceRuntime{
		ID: "service-new", TenantID: "tenant-1", StackID: "stack-1", ServerID: "server-1", ServiceKey: "new",
	}); upsertErr != nil {
		t.Fatal(upsertErr)
	}

	second, err := store.ListInventoryServices(t.Context(), mustOwnerInventoryScope(t, "tenant-1", "owner-1"), "server-1", InventoryPageRequest{
		Limit: 1, Watermark: first.Watermark, After: *first.Next,
	})
	if err != nil || len(second.Services) != 1 || second.Services[0].ID != "service-b" || second.Next != nil {
		t.Fatalf("second page = %#v, err=%v", second, err)
	}
}
