package routes

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/middleware"
)

// A rename is a write: another owner, another tenant, or a principal without
// the signed write entitlement is denied without revealing existence, and no
// display name is written. FGA allows everything here, so the SQL/store owner
// and tenant binding is what must hold.
func TestInventoryServerRenameFailsClosedOutsideOwnerTenantAndWriteGrant(t *testing.T) {
	store := controlplane.NewMemoryStore()
	for _, server := range []controlplane.ServerRuntime{
		{ID: "server-1", TenantID: "tenant-1", OwnerSubjectID: "owner-1", Name: "projected"},
		{ID: "server-t2", TenantID: "tenant-2", OwnerSubjectID: "owner-1", Name: "projected"},
	} {
		if _, err := store.UpsertServerRuntime(t.Context(), server); err != nil {
			t.Fatal(err)
		}
	}
	router := httpx.NewRouter()
	RegisterInventoryRoutes(router, InventoryRouteConfig{
		ReadStore: store, Policy: NewInventoryFGAPolicy(&fakeInventoryRelationshipChecker{allowed: true}), Now: time.Now, Version: "test",
	})
	rename := func(ownerID, tenantID, serverID string, entitlements ...string) *httptest.ResponseRecorder {
		event, recorder := registryRouteStoreTestEvent(http.MethodPatch, "/api/v1/inventory/servers/"+serverID, ownerID, tenantID, map[string]any{"display_name": "Basement NAS"})
		router.ServeHTTP(recorder, event.Request.WithContext(middleware.WithSignedEntitlements(event.Request.Context(), entitlements...)))
		return recorder
	}

	membershipOnly := func() *httptest.ResponseRecorder {
		event, recorder := registryRouteStoreTestEvent(http.MethodPatch, "/api/v1/inventory/servers/server-1", "owner-1", "tenant-1", map[string]any{"display_name": "Basement NAS"})
		router.ServeHTTP(recorder, event.Request.WithContext(middleware.WithMembershipEntitlements(event.Request.Context(), InventoryEntitlementWrite)))
		return recorder
	}

	for name, recorder := range map[string]*httptest.ResponseRecorder{
		"membership write, no signed grant": membershipOnly(),
		"other owner":                       rename("owner-2", "tenant-1", "server-1", InventoryEntitlementWrite),
		"other tenant":                      rename("owner-1", "tenant-1", "server-t2", InventoryEntitlementWrite),
		"no write entitlement":              rename("owner-1", "tenant-1", "server-1", InventoryEntitlementRead, InventoryEntitlementOperate),
	} {
		if recorder.Code != http.StatusForbidden || !strings.Contains(recorder.Body.String(), "inventory_access_denied") || strings.Contains(recorder.Body.String(), "projected") {
			t.Errorf("%s: rename = %d %s, want 403 inventory_access_denied without the server", name, recorder.Code, recorder.Body.String())
		}
	}
	for _, stored := range [][2]string{{"tenant-1", "server-1"}, {"tenant-2", "server-t2"}} {
		server, err := store.GetServerRuntime(t.Context(), stored[0], stored[1])
		if err != nil || server.DisplayName != "" {
			t.Fatalf("denied rename wrote %s: %+v, %v", stored[1], server, err)
		}
	}

	if recorder := rename("owner-1", "tenant-1", "server-1", InventoryEntitlementWrite); recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"display_name":"Basement NAS"`) {
		t.Fatalf("owner rename = %d %s", recorder.Code, recorder.Body.String())
	}
}
