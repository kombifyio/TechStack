package routes

import (
	"github.com/kombifyio/techstack/internal/homeassistant"
	"net/http"
	"testing"
)

// Tenant and preservation boundaries must reject writes before native dispatch.
func TestHomeAssistantLifecyclePreservesObservedAndRejectsForeignOwner(t *testing.T) {
	c := HomeAssistantOperationRoutes{Bindings: []homeassistant.OwnerBinding{{OwnerID: "owner-1", TenantID: "tenant-1", Instance: &homeassistant.InstanceBinding{BindingRef: "ha", Origin: "existing", ManagementScope: "observed"}}}}
	for _, owner := range []string{"owner-1", "other"} {
		for _, action := range []string{"backup", "restore", "update"} {
			e, rr := registryRouteStoreTestEvent(http.MethodPost, "/api/v1/home-assistant/instances/ha/"+action, owner, "tenant-1", map[string]any{"operation_id": "attempt"})
			e.Request.SetPathValue("binding", "ha")
			err := c.execute(action)(e)
			if err == nil && rr.Code < 400 {
				t.Fatal("unauthorized lifecycle mutation accepted")
			}
		}
	}
	e, _ := registryRouteStoreTestEvent(http.MethodGet, "/api/v1/home-assistant/migrations/missing", "owner-1", "tenant-1", nil)
	if _, err := (HomeAssistantMigrationRoutes{}).owned(e); err == nil {
		t.Fatal("missing migration returned no error")
	}
}
