package controlplane

import (
	"strings"

	"github.com/kombifyio/techstack/pkg/serviceregistry"
)

const (
	legacyServiceAccessModeKey         = "mode"
	legacyServiceDesiredRunning        = "running"
	legacyServiceDesiredStopped        = "stopped"
	legacyServiceRuntimeBackfillSource = serviceregistry.SourceLegacyRegistryBackfill
	legacyServiceRuntimeUnknown        = "unknown"
	legacyServiceRuntimeUnavailable    = "unavailable"
	legacyServiceTypeKey               = "type"
)

// resolvedLegacyServiceOwnership normalizes the provenance and ownership of one
// legacy RegistryStore row before it is written. The legacy contract has no
// management field of its own, so an unset value is resolved through the same
// canonical rule the 074 backfill uses - including the two pre-`source` markers
// (a legacy `observed` status and the hand-imported `custom` type) that the
// three deleted read-time derivations used to look at.
func resolvedLegacyServiceOwnership(service Service) Service {
	service.Status = firstNonEmpty(strings.TrimSpace(service.Status), serviceregistry.SourceObserved)
	rawSource := firstNonEmpty(strings.TrimSpace(service.Source), serviceregistry.SourceObserved)
	management := strings.TrimSpace(service.ManagementState)
	if management == "" {
		serviceType, _ := service.Metadata[legacyServiceTypeKey].(string)
		management = string(serviceregistry.ManagementStateForLegacyRecord(rawSource, service.Status, serviceType))
	} else {
		management = string(serviceregistry.CanonicalManagementState(management))
	}
	service.Source = serviceregistry.CanonicalSource(rawSource)
	service.ManagementState = management
	return service
}

// backfilledServiceRuntime keeps legacy rows visible during the bounded
// projection migration without claiming that their historic status is fresh
// evidence. A measured canonical row with the same identity always wins.
func backfilledServiceRuntime(service Service, serverID string) ServiceRuntime {
	return ServiceRuntime{
		ID: service.ID, TenantID: service.TenantID, InstanceID: service.InstanceID,
		StackID: service.StackID, ServerID: serverID,
		Placement:  serviceregistry.Placement{TargetKind: serviceregistry.TargetKindServer},
		ServiceKey: strings.ToLower(strings.TrimSpace(service.ServiceKey)), ServiceInstance: "default",
		Name: service.Name, DesiredState: legacyServiceDesiredState(service.Status),
		ObservedState: legacyServiceRuntimeUnknown, HealthState: legacyServiceRuntimeUnknown,
		ManagementState: resolvedLegacyServiceOwnership(service).ManagementState,
		Access:          map[string]any{legacyServiceAccessModeKey: legacyServiceRuntimeUnavailable, guardServiceAccessReasonKey: "legacy_backfill_requires_observation"},
		Capabilities:    []string{}, Source: legacyServiceRuntimeBackfillSource,
		Metadata: mergeMaps(service.Metadata, map[string]any{
			"backfill": true, "legacy_status": service.Status, "legacy_source": service.Source,
		}),
		CreatedAt: service.CreatedAt, UpdatedAt: service.UpdatedAt,
	}
}

func legacyServiceDesiredState(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "stopped", "exited", "dead", "archived", "decommissioned":
		return legacyServiceDesiredStopped
	default:
		return legacyServiceDesiredRunning
	}
}

func serviceRuntimeIdentity(stackID, serverID, serviceKey, instance string) string {
	return strings.Join([]string{
		strings.TrimSpace(stackID), strings.TrimSpace(serverID),
		strings.ToLower(strings.TrimSpace(serviceKey)),
		strings.ToLower(firstNonEmpty(strings.TrimSpace(instance), "default")),
	}, "\x00")
}

func legacyServiceServerID(service Service, servers map[string]ServerRuntime) string {
	if strings.TrimSpace(service.NodeID) == "" || strings.TrimSpace(service.ServiceKey) == "" {
		return ""
	}
	var selected ServerRuntime
	for _, server := range servers {
		if server.TenantID != service.TenantID || server.StackID != service.StackID || (server.ID != service.NodeID && server.NodeID != service.NodeID) {
			continue
		}
		candidateExact := server.ID == service.NodeID
		selectedExact := selected.ID == service.NodeID
		if selected.ID == "" || (candidateExact && !selectedExact) || (candidateExact == selectedExact && newerServerRuntime(server, selected)) {
			selected = server
		}
	}
	return selected.ID
}

func newerServerRuntime(candidate, selected ServerRuntime) bool {
	if candidate.UpdatedAt.Equal(selected.UpdatedAt) {
		return candidate.ID < selected.ID
	}
	return candidate.UpdatedAt.After(selected.UpdatedAt)
}

func mergeMaps(base, overlay map[string]any) map[string]any {
	result := cloneMap(base)
	for key, value := range overlay {
		result[key] = value
	}
	return result
}
