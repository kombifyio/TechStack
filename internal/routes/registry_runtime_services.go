package routes

import (
	"time"

	"github.com/kombifyio/techstack/pkg/appcatalog"
	"github.com/kombifyio/techstack/pkg/controlplane"
)

type registryRuntimeProjection func(controlplane.ServiceRuntime, *controlplane.ServerRuntime) serviceRuntimeResponse

// registryRuntimeProjectionForRequest returns the service-card projection of one
// Registry read. Like the service and application lists, it shares one signed
// adoption admission across every row of the request: the admission verifies
// the full signed runtime archive, so evaluating it per row made this call
// scale with the number of services. The closure belongs only to this request.
func (h registryRouteHandlers) registryRuntimeProjectionForRequest(now time.Time) registryRuntimeProjection {
	projector := h.serviceProjector
	if projector == nil {
		projector = &serviceRuntimeHandlers{now: func() time.Time { return now }}
	}
	return projector.responseForRequest()
}

// The Registry BFF uses the same runtime projection as service detail. Legacy
// rows remain visible, but never grant control without measured capabilities.
func (h registryRouteHandlers) projectRegistryRuntimeServices(stack controlplane.Stack, rows []controlplane.ServiceRuntime, servers []controlplane.ServerRuntime, legacy []registryService, project registryRuntimeProjection) []registryService {
	serverMap := make(map[string]*controlplane.ServerRuntime, len(servers))
	for i := range servers {
		serverMap[servers[i].ID] = &servers[i]
		if servers[i].NodeID != "" {
			serverMap[servers[i].NodeID] = &servers[i]
		}
	}
	result := make([]registryService, 0, len(rows)+len(legacy))
	seen := make(map[string]bool, len(rows))
	rows = projectDeclaredServiceControls(rows, serverMap)
	for _, row := range rows {
		server := serverMap[row.ServerID]
		if server != nil {
			row.ServerID = server.ID
		}
		projected := project(row, server)
		name := normalizeServiceKey(row.ServiceKey)
		seen[row.ID] = true
		seen[row.ServerID+":"+name] = true
		if stringFromAnyMap(row.Metadata, serviceSupersededBy) != "" {
			continue
		}
		record := registryService{
			ID: row.ID, Name: name, DisplayName: row.Name,
			ApplicationKey: projected.ApplicationKey, ApplicationName: projected.ApplicationDisplayName,
			Type: canonicalServiceType(name), Status: projected.Health.State,
			HealthState: projected.Health.State, ObservedAt: formatOptionalTime(row.ObservedAt),
			ManagementState: projected.ManagementState, PlacementScope: registryPlacementScopeStack,
			KitDeploymentID: stack.ID, StackName: firstNonEmptyString(stack.Name, stack.ID),
			ServerID: row.ServerID, Port: intFromAnyMap(row.Metadata, "port"),
			URL:            stringFromAnyMap(projected.Access, serviceAccessURLKey),
			AllowedActions: projected.AllowedActions, InventoryRevision: projected.InventoryRevision,
			DesiredState: projected.DesiredState, ObservedState: projected.ObservedState,
			Version: projected.Version, Catalog: projected.Catalog, MutationLock: projected.MutationLock,
			Provenance: projected.Provenance, ActionGuard: projected.ActionGuard,
			EnvironmentClass: projected.PlacementClass,
		}
		if server != nil {
			record.ServerName = firstNonEmptyString(server.Name, server.ID)
		}
		record.MoveAllowed, record.MoveBlockedReason = registryServiceMoveEligibility(record.Status, record.ManagementState)
		result = append(result, record)
	}
	for _, record := range legacy {
		if server := serverMap[record.ServerID]; server != nil {
			record.ServerID = server.ID
			record.ServerName = firstNonEmptyString(server.Name, server.ID)
		}
		if seen[record.ID] || seen[record.ServerID+":"+normalizeServiceKey(record.Name)] {
			continue
		}
		record.AllowedActions = []string{}
		record.Status, record.HealthState, record.URL, record.ObservedAt = "unknown", "unknown", "", ""
		record.DesiredState, record.ObservedState = "unknown", "unknown"
		record.MutationLock = serviceRuntimeMutationLock{State: "unlocked"}
		record.Catalog = appcatalog.Match(appcatalog.Subject{Name: record.Name})
		if record.Catalog == nil {
			record.Catalog = appcatalog.GenericEntry(record.DisplayName)
		}
		result = append(result, record)
	}
	return result
}
