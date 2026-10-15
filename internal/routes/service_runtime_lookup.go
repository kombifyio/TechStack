package routes

import (
	"context"
	"errors"
	"time"

	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/serviceregistry"
)

// Legacy node references are resolved against the same server authority used by
// inventory. The read does not persist a new placement or grant a capability.
func (h *serviceRuntimeHandlers) loadServiceRuntime(ctx context.Context, tenantID, serviceID string) (*controlplane.ServiceRuntime, error) {
	service, err := h.loadPlacedServiceRuntime(ctx, tenantID, serviceID)
	if err != nil || service.Source == stackKitOutputKey {
		return service, err
	}
	rows, err := h.store.ListServiceRuntimes(ctx, tenantID, service.StackID, "")
	if err != nil {
		return nil, err
	}
	servers, err := h.servers.ListServerRuntimesByTenant(ctx, tenantID, service.StackID)
	if err != nil {
		return nil, err
	}
	serverMap := map[string]*controlplane.ServerRuntime{}
	for i := range servers {
		serverMap[servers[i].ID] = &servers[i]
		if servers[i].NodeID != "" {
			serverMap[servers[i].NodeID] = &servers[i]
		}
	}
	for _, projected := range projectDeclaredServiceControls(rows, serverMap) {
		if projected.ID == service.ID {
			return &projected, nil
		}
	}
	return service, nil
}

func (h *serviceRuntimeHandlers) loadPlacedServiceRuntime(ctx context.Context, tenantID, serviceID string) (*controlplane.ServiceRuntime, error) {
	service, err := h.store.GetServiceRuntime(ctx, tenantID, serviceID)
	if errors.Is(err, controlplane.ErrNotFound) {
		service, err = h.serviceFromStackKitOutput(ctx, tenantID, serviceID)
	}
	if err != nil {
		return nil, err
	}
	if service.ServerID == "" {
		return service, nil
	}
	if _, serverErr := h.servers.GetServerRuntime(ctx, tenantID, service.ServerID); serverErr == nil {
		return service, nil
	} else if !errors.Is(serverErr, controlplane.ErrNotFound) {
		return nil, serverErr
	}
	servers, err := h.servers.ListServerRuntimesByTenant(ctx, tenantID, service.StackID)
	if err != nil {
		return nil, err
	}
	for _, server := range servers {
		if server.NodeID == service.ServerID {
			copy := *service
			copy.ServerID = server.ID
			return &copy, nil
		}
	}
	return service, nil
}

// Job-output-only cards can be inspected. Historic output never becomes a
// measured runtime or an actionable capability just because the card was read.
func (h *serviceRuntimeHandlers) serviceFromStackKitOutput(ctx context.Context, tenantID, serviceID string) (*controlplane.ServiceRuntime, error) {
	if h.jobs == nil {
		return nil, controlplane.ErrNotFound
	}
	stacks, err := h.stacks.ListStacksByTenant(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	var found *controlplane.ServiceRuntime
	for _, stack := range stacks {
		servers, listErr := h.servers.ListServerRuntimesByTenant(ctx, tenantID, stack.ID)
		if listErr != nil {
			return nil, listErr
		}
		var nodes []controlplane.Node
		if registry, ok := h.store.(controlplane.RegistryStore); ok {
			nodes, err = registry.ListNodesByStack(ctx, tenantID, stack.ID)
			if err != nil && !errors.Is(err, controlplane.ErrNotFound) {
				return nil, err
			}
		}
		registryServers, _ := registryServersForStack(servers, nodes, nil, time.Now().UTC())
		outputs := stackKitOutputsFromLatestDeployJob(ctx, h.jobs, tenantID, stack.ID)
		for _, record := range registryServicesFromStackKitOutputs(outputs, stack, registryServers) {
			if record.ID != serviceID {
				continue
			}
			if found != nil {
				return nil, controlplane.ErrConflict
			}
			found = &controlplane.ServiceRuntime{
				ID: serviceID, TenantID: tenantID, StackID: stack.ID, ServerID: record.ServerID,
				ServiceKey: record.Name, ServiceInstance: "default", Name: record.DisplayName,
				ManagementState: stackKitOutputManagementState,
				DesiredState:    "unknown", ObservedState: "unknown", HealthState: "unknown",
				Placement: serviceregistry.NormalizePlacement(record.ServerID, serviceregistry.Placement{}),
				Source:    stackKitOutputKey, Capabilities: []string{},
				Access:    serviceAccess(serviceAccessUnavailable, "", "", "legacy_backfill_requires_observation", ""),
				CreatedAt: stack.CreatedAt, UpdatedAt: stack.UpdatedAt,
			}
		}
	}
	if found != nil {
		return found, nil
	}
	return nil, controlplane.ErrNotFound
}
