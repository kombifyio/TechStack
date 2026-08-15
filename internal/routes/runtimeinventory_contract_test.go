package routes

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestRuntimeInventoryContract(t *testing.T) {
	now := time.Date(2026, 8, 2, 9, 0, 0, 0, time.UTC)
	payload := inventoryServerList{
		ObservedAt: now, Freshness: inventoryFreshness{State: inventoryFresh, StaleAfterSeconds: 60},
		InventoryRevision: 7, CollectionCursor: "cursor-7", PageSize: 1,
		Servers: []inventoryServer{{
			ID: "server-1", StackID: "stack-1", Name: "Windows runtime", ObservedAt: &now,
			Freshness: inventoryFreshness{State: inventoryFresh, StaleAfterSeconds: 60}, InventoryRevision: 7,
			Addresses: inventoryAddresses{LocalIPs: []string{"192.0.2.10"}}, Platform: inventoryPlatform{OS: "windows", Arch: "amd64"},
			Lifecycle: inventoryLifecycleProjection{State: "active"}, Desired: inventoryDesiredProjection{State: "running"},
			Lease: inventoryLeaseProjection{State: "active"}, Cleanup: inventoryCleanupProjection{State: inventoryCleanupStateUnverified},
			Connection: inventoryObservedState{State: "connected", ObservedAt: &now}, Health: inventoryObservedState{State: "healthy", ObservedAt: &now},
		}},
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"observed_at", "freshness", "inventory_revision", "collection_cursor", "page_size", "servers"} {
		if _, ok := decoded[required]; !ok {
			t.Fatalf("generated inventory projection missing %q: %s", required, encoded)
		}
	}
	for _, forbidden := range []string{"credential", "secret", "token", "provider_payload", "transport_endpoint"} {
		if strings.Contains(strings.ToLower(string(encoded)), forbidden) {
			t.Fatalf("generated inventory projection exposes %q: %s", forbidden, encoded)
		}
	}
}
