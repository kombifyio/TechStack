package routes

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/kombifyio/techstack/internal/homeassistant"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/runtimeidentity"
	"github.com/kombifyio/techstack/pkg/serviceregistry"
)

// importHomeAssistant records a read-only link. The submitted credential is
// used only for this probe, never stored in a registry row or workflow input.
func (h registryRouteHandlers) importHomeAssistant(e *httpx.Event) error {
	owner, err := requireAuth(e)
	if err != nil {
		return err
	}
	tenant, err := h.requireRegistryMutationTenant(e, owner, "techstack.registry.services.import")
	if err != nil {
		return err
	}
	var req struct {
		StackID            string `json:"stack_id"`
		ServerID           string `json:"server_id"`
		URL                string `json:"url"`
		Token              string `json:"token"`
		InstallationMethod string `json:"installation_method"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(e.Response, e.Request.Body, 16384))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		return httpx.BadRequest(e, "Invalid Home Assistant connection request", nil)
	}
	if req.InstallationMethod != "haos" && req.InstallationMethod != "container" {
		return httpx.BadRequest(e, "Explicit installation method haos or container required", nil)
	}
	stack, node, ok, err := h.ownedRegistryNode(e, tenant, owner, strings.TrimSpace(req.StackID), strings.TrimSpace(req.ServerID))
	if err != nil || !ok {
		return err
	}
	ctx := e.Request.Context()
	// Use the same service identity as the registry. Re-linking cannot reset a
	// managed service, replace configuration, or change its original endpoint.
	id := runtimeidentity.ServiceID(stack.ID, node.ID, "home-assistant", "default")
	existing, err := h.registryStore.GetService(ctx, tenant, id)
	if err == nil {
		return httpx.Success(e, http.StatusOK, map[string]any{"service": serviceRegistryRecordFromStore(*existing, *stack, *node), "existing": true})
	}
	if !errors.Is(err, controlplane.ErrNotFound) {
		return httpx.NewInternalServerError("Failed to read existing Home Assistant link", nil)
	}
	if !h.allowLocalHomeAssistant {
		return httpx.Error(e, http.StatusServiceUnavailable, "lan_executor_unavailable", "Home Assistant requires an authorized local executor", nil)
	}
	client, err := homeassistant.NewLocalClient(ctx, req.URL, req.Token)
	if err != nil {
		return httpx.BadRequest(e, err.Error(), nil)
	}
	defer client.Close()
	observed, err := client.Observe(ctx)
	if err != nil {
		return httpx.Error(e, http.StatusBadGateway, "home_assistant_probe_failed", err.Error(), nil)
	}
	service, err := h.registryStore.UpsertService(ctx, controlplane.Service{
		ID: id, TenantID: tenant, InstanceID: stack.InstanceID, StackID: stack.ID, NodeID: node.ID, ServiceKey: "home-assistant", Name: "home-assistant", Status: registryObservedState, Source: serviceregistry.SourceObserved, ManagementState: registryObservedState, URL: client.Endpoint(),
		Metadata: map[string]any{"display_name": "Home Assistant", "type": "smart-home", "platform": "home-assistant", "installation_method": req.InstallationMethod, "installation_method_source": "user-declared", "instance_origin": "existing", "configuration_policy": "preserve-user-changes", "home_assistant_observation": observed},
	})
	if err != nil {
		return httpx.NewInternalServerError("Failed to record Home Assistant link", nil)
	}
	return httpx.Success(e, http.StatusOK, map[string]any{"service": serviceRegistryRecordFromStore(*service, *stack, *node), "observation": observed, "management_scope": "observed", "migration_available": false, "migration_unavailable_reason": "A separately authorized native backup, isolated restore and source lifecycle executor is required"})
}
