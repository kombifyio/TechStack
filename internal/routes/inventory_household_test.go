package routes

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/kombifyio/techstack/internal/routes/tenantguard"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/middleware"
)

// A household member lists the owner's servers only while FGA confirms the
// surface grant Administration projected for them; without it, and for the
// owner's data under any other selector, the request is refused.
func TestHouseholdMemberListsOwnerServersOnlyWithTheGrant(t *testing.T) {
	tenantguard.Configure(true)
	t.Cleanup(func() { tenantguard.Configure(false) })
	store := controlplane.NewMemoryStore()
	if _, err := store.CreateStack(t.Context(), controlplane.CreateStackRequest{
		ID: "stack-1", TenantID: "usr:owner-1", OwnerSubjectID: "owner-1", Name: "Stack",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertServerRuntime(t.Context(), controlplane.ServerRuntime{
		ID: "server-1", TenantID: "usr:owner-1", StackID: "stack-1", OwnerSubjectID: "owner-1",
	}); err != nil {
		t.Fatal(err)
	}
	checker := &fakeInventoryRelationshipChecker{allowed: true}
	h := inventoryHandlers{app: &inventoryApplication{read: store, policy: NewInventoryFGAPolicy(checker), now: time.Now}, version: "test"}

	list := func(allowed bool) (int, []byte) {
		checker.allowed = allowed
		event, recorder := registryRouteStoreTestEvent(http.MethodGet, "/api/v1/inventory/servers", "member-1", "usr:member-1", nil)
		event.Request.Header.Set(householdOwnerHeader, "owner-1")
		event.Request = event.Request.WithContext(middleware.WithSignedEntitlements(event.Request.Context(), InventoryEntitlementRead))
		if err := h.httpListServers(event); err != nil {
			t.Fatal(err)
		}
		return recorder.Code, recorder.Body.Bytes()
	}

	code, body := list(true)
	var response struct {
		Data struct {
			Servers []struct {
				ID string `json:"id"`
			} `json:"servers"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &response); err != nil || code != http.StatusOK || len(response.Data.Servers) == 0 || response.Data.Servers[0].ID != "server-1" {
		t.Fatalf("granted member list = %d %s", code, body)
	}
	if checker.user != "user:member-1" || checker.object != "surface:b64_dXNyOm93bmVyLTE/inventory/servers" {
		t.Fatalf("FGA checked (%q,%q)", checker.user, checker.object)
	}
	if code, body := list(false); code != http.StatusForbidden {
		t.Fatalf("member without grant = %d %s", code, body)
	}
}
