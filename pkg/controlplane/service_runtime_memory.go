package controlplane

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/kombifyio/techstack/pkg/serviceregistry"
)

func (s *MemoryStore) UpsertServiceRuntime(_ context.Context, service ServiceRuntime) (*ServiceRuntime, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	placementOmitted := strings.TrimSpace(service.ServerID) == "" && !serviceregistry.PlacementIntentPresent(service.Placement)
	if existing, ok := s.serviceRuntime[service.ID]; ok {
		service.CreatedAt = existing.CreatedAt
		if placementOmitted {
			service.ServerID = existing.ServerID
			service.Placement = serviceregistry.ClonePlacement(existing.Placement)
		}
	} else {
		service.CreatedAt = now
	}
	service.ServiceInstance = firstNonEmpty(service.ServiceInstance, "default")
	service = canonicalServiceRuntimeStates(service)
	if err := serviceregistry.ValidatePlacement(service.ServerID, service.Placement); err != nil {
		return nil, fmt.Errorf("controlplane: invalid service placement: %w", err)
	}
	if existing, ok := s.serviceRuntime[service.ID]; ok {
		// A measured observation never overwrites stored user intent. This
		// mirrors the aggregate write boundary used by the Postgres store.
		service.DesiredState = existing.DesiredState
		// Ownership is sticky unless the provenance itself changed, exactly as
		// resolveServiceManagementState decides it in the aggregate.
		if strings.EqualFold(existing.Source, service.Source) {
			service.ManagementState = string(serviceregistry.CanonicalManagementState(existing.ManagementState))
		}
	}
	service.UpdatedAt = now
	service.Access = cloneMap(service.Access)
	service.Metadata = cloneMap(service.Metadata)
	service.Capabilities = append([]string(nil), service.Capabilities...)
	s.serviceRuntime[service.ID] = service
	if legacy, ok := s.svcs[service.ID]; ok {
		legacy.Status = derivedServiceStatus(legacy, service.ObservedState)
		legacy.Source = service.Source
		legacy.ManagementState = service.ManagementState
		legacy.URL, _ = service.Access["url"].(string)
		mergedMetadata := cloneMap(legacy.Metadata)
		for key, value := range service.Metadata {
			mergedMetadata[key] = value
		}
		legacy.Metadata = mergedMetadata
		legacy.UpdatedAt = now
		s.svcs[service.ID] = legacy
	}
	return cloneServiceRuntime(service), nil
}

// SetServiceMutationLock mirrors the aggregate guardrail write. It applies the
// same rules the Postgres boundary enforces: unlocking clears reason, actor and
// timestamp together, and the timestamp only moves when the state changed.
func (s *MemoryStore) SetServiceMutationLock(
	_ context.Context,
	tenantID, serviceID string,
	lock ServiceMutationLock,
	_ string,
) (*ServiceRuntime, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	service, ok := s.serviceRuntime[serviceID]
	if !ok || service.TenantID != tenantID {
		return nil, ErrNotFound
	}
	state := serviceregistry.CanonicalMutationLockState(string(lock.State))
	changed := serviceregistry.CanonicalMutationLockState(string(service.MutationLock.State)) != state
	if state == serviceregistry.MutationUnlocked {
		service.MutationLock = ServiceMutationLock{State: state}
	} else {
		service.MutationLock.State = state
		service.MutationLock.ReasonCode = lock.ReasonCode
		service.MutationLock.Actor = lock.Actor
		if changed || service.MutationLock.ChangedAt == nil {
			stamp := s.now().UTC()
			service.MutationLock.ChangedAt = &stamp
		}
	}
	if changed {
		service.UpdatedAt = s.now()
	}
	s.serviceRuntime[serviceID] = service
	return cloneServiceRuntime(service), nil
}

func (s *MemoryStore) GetServiceRuntime(_ context.Context, tenantID, serviceID string) (*ServiceRuntime, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	service, ok := s.serviceRuntime[serviceID]
	if ok && service.TenantID == tenantID {
		return cloneServiceRuntime(service), nil
	}
	legacy, ok := s.svcs[serviceID]
	if !ok || legacy.TenantID != tenantID {
		return nil, ErrNotFound
	}
	serverID := legacyServiceServerID(legacy, s.servers)
	if serverID == "" {
		return nil, ErrNotFound
	}
	return cloneServiceRuntime(backfilledServiceRuntime(legacy, serverID)), nil
}

func (s *MemoryStore) ListServiceRuntimes(_ context.Context, tenantID, stackID, serverID string) ([]ServiceRuntime, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]ServiceRuntime, 0)
	canonicalIdentities := make(map[string]bool, len(s.serviceRuntime))
	canonicalIDs := make(map[string]bool, len(s.serviceRuntime))
	for _, service := range s.serviceRuntime {
		if service.TenantID != tenantID {
			continue
		}
		canonicalIDs[service.ID] = true
		canonicalIdentities[serviceRuntimeIdentity(service.StackID, service.ServerID, service.ServiceKey, service.ServiceInstance)] = true
		if (stackID != "" && service.StackID != stackID) || (serverID != "" && service.ServerID != serverID) {
			continue
		}
		out = append(out, *cloneServiceRuntime(service))
	}
	for _, legacy := range s.svcs {
		if legacy.TenantID != tenantID || canonicalIDs[legacy.ID] || (stackID != "" && legacy.StackID != stackID) {
			continue
		}
		mappedServerID := legacyServiceServerID(legacy, s.servers)
		if mappedServerID == "" || (serverID != "" && mappedServerID != serverID) {
			continue
		}
		backfilled := backfilledServiceRuntime(legacy, mappedServerID)
		if canonicalIdentities[serviceRuntimeIdentity(backfilled.StackID, backfilled.ServerID, backfilled.ServiceKey, backfilled.ServiceInstance)] {
			continue
		}
		out = append(out, *cloneServiceRuntime(backfilled))
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].StackID != out[j].StackID {
			return out[i].StackID < out[j].StackID
		}
		if out[i].ServiceKey != out[j].ServiceKey {
			return out[i].ServiceKey < out[j].ServiceKey
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}
