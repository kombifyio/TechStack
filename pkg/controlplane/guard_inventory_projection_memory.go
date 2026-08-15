package controlplane

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/kombifyio/techstack/pkg/serverregistry"
)

// ApplyGuardInventoryProjection atomically commits one Guard event and every
// reader-visible inventory projection while holding the MemoryStore write
// lock. All fallible admission happens before the first map mutation.
func (s *MemoryStore) ApplyGuardInventoryProjection(_ context.Context, command GuardInventoryProjection) (*GuardInventoryProjectionResult, error) {
	prepared, err := prepareGuardInventoryProjection(command)
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	current, exists := s.servers[prepared.command.Event.ServerID]
	if !exists {
		return nil, fmt.Errorf("%w: Guard inventory requires a canonical server", ErrConflict)
	}
	if err := validateGuardInventoryCanonicalServer(current, prepared.command); err != nil {
		return nil, err
	}
	if guardInventoryProjectionFenced(current, prepared.command.Event) {
		return s.replayOrFenceGuardInventoryProjectionLocked(current, prepared)
	}
	if err := s.validateGuardInventoryProjectionBindingsLocked(prepared.command); err != nil {
		return nil, err
	}
	retained := map[string]struct{}{}
	if prepared.command.ManifestObserved {
		retained = s.guardInventoryRetainedServiceIDsLocked(prepared.command)
	}
	observation := guardInventoryServerObservationEventWithExpectedServices(
		prepared.command.Event, int64(len(prepared.command.Services)+len(retained)),
	)

	result, err := s.applyServerEventLocked(observation)
	if err != nil {
		return nil, err
	}
	if result == nil || result.Server == nil {
		return nil, fmt.Errorf("%w: Guard inventory event returned no server projection", ErrConflict)
	}
	if !result.Applied {
		return s.replayOrFenceGuardInventoryProjectionLocked(*result.Server, prepared)
	}

	// prepareGuardInventoryProjection requires an inventory observation, so the
	// accepted server event always assigns the revision before this point.
	revision := result.Server.InventoryRevision
	if result.Inventory != nil {
		revision = result.Inventory.Revision
	}
	projection := guardInventoryProjectionAtRevision(prepared, revision)
	s.persistGuardInventoryProjectionLocked(projection, s.now())
	result.Server.Metadata = cloneGuardCanonicalMap(result.Server.Metadata)
	if result.Inventory != nil {
		result.Inventory.Inventory = cloneGuardCanonicalMap(result.Inventory.Inventory)
	}
	return &GuardInventoryProjectionResult{ServerEvent: result}, nil
}

func guardInventoryProjectionFenced(current ServerRuntime, event ServerEvent) bool {
	if current.LifecycleState == string(serverregistry.LifecycleDecommissioning) ||
		current.LifecycleState == string(serverregistry.LifecycleDecommissioned) {
		return true
	}
	return guardEventIsStale(current, event)
}

func (s *MemoryStore) replayOrFenceGuardInventoryProjectionLocked(
	current ServerRuntime,
	prepared *preparedGuardInventoryProjection,
) (*GuardInventoryProjectionResult, error) {
	result := &ServerEventResult{Server: cloneServerRuntime(current), Applied: false}
	if current.LifecycleState == string(serverregistry.LifecycleDecommissioning) ||
		current.LifecycleState == string(serverregistry.LifecycleDecommissioned) ||
		!guardInventoryProjectionAtCurrentPosition(current, prepared.command.Event) {
		return &GuardInventoryProjectionResult{ServerEvent: result}, nil
	}

	snapshot, found := s.guardInventorySnapshotAtRevisionLocked(current.ID, current.InventoryRevision)
	if !found {
		return nil, fmt.Errorf("%w: current Guard position has no durable inventory snapshot", ErrConflict)
	}
	durableDigest, found := guardInventoryProjectionDigestFromInventory(snapshot.Inventory)
	if !found || durableDigest != prepared.digest {
		return nil, fmt.Errorf("%w: current Guard position has a different inventory projection", ErrConflict)
	}
	result.Inventory = snapshot
	return &GuardInventoryProjectionResult{ServerEvent: result, Replayed: true}, nil
}

func (s *MemoryStore) guardInventorySnapshotAtRevisionLocked(serverID string, revision int64) (*ServerInventorySnapshot, bool) {
	rows := s.serverInventory[serverID]
	for index := len(rows) - 1; index >= 0; index-- {
		if rows[index].Revision != revision {
			continue
		}
		copy := rows[index]
		copy.Inventory = cloneGuardCanonicalMap(rows[index].Inventory)
		return &copy, true
	}
	return nil, false
}

func (s *MemoryStore) validateGuardInventoryProjectionBindingsLocked(command GuardInventoryProjection) error {
	if existing, ok := s.nodes[command.Node.ID]; ok &&
		(existing.TenantID != command.Node.TenantID || existing.StackID != command.Node.StackID ||
			(existing.InstanceID != "" && existing.InstanceID != command.Node.InstanceID) ||
			(existing.WorkerID != "" && existing.WorkerID != command.Node.WorkerID)) {
		return fmt.Errorf("%w: Guard node id is already bound to another runtime", ErrConflict)
	}
	for _, projection := range command.Services {
		// Provenance is compared per row, not against the batch default: one
		// Guard observation legitimately carries both StackKit services and
		// services merely discovered on the host.
		serviceSource := projection.Runtime.Source
		if existing, ok := s.svcs[projection.Legacy.ID]; ok &&
			(existing.TenantID != projection.Legacy.TenantID || existing.StackID != projection.Legacy.StackID ||
				existing.NodeID != projection.Legacy.NodeID ||
				(strings.TrimSpace(existing.Source) != "" && !strings.EqualFold(existing.Source, serviceSource))) {
			return fmt.Errorf("%w: Guard service id %q is already bound to another projection", ErrConflict, projection.Legacy.ID)
		}
		if existing, ok := s.serviceRuntime[projection.Runtime.ID]; ok &&
			(existing.TenantID != projection.Runtime.TenantID || existing.StackID != projection.Runtime.StackID ||
				existing.ServerID != projection.Runtime.ServerID ||
				serviceRuntimeIdentity(existing.StackID, existing.ServerID, existing.ServiceKey, existing.ServiceInstance) !=
					serviceRuntimeIdentity(projection.Runtime.StackID, projection.Runtime.ServerID, projection.Runtime.ServiceKey, projection.Runtime.ServiceInstance) ||
				(strings.TrimSpace(existing.Source) != "" && !strings.EqualFold(existing.Source, serviceSource))) {
			return fmt.Errorf("%w: Guard service runtime id %q is already bound to another projection", ErrConflict, projection.Runtime.ID)
		}
	}
	return nil
}

func (s *MemoryStore) persistGuardInventoryProjectionLocked(command GuardInventoryProjection, now time.Time) {
	node := command.Node
	if existing, ok := s.nodes[node.ID]; ok {
		node.CreatedAt = existing.CreatedAt
		node.InstanceID = existing.InstanceID
		node.StackID = existing.StackID
		node.WorkerID = existing.WorkerID
		node.Name = existing.Name
		node.Role = existing.Role
	} else {
		node.CreatedAt = now
		node.Role = firstNonEmpty(node.Role, "foundation")
	}
	node.Status = firstNonEmpty(node.Status, "pending")
	node.UpdatedAt = now
	node.Metadata = cloneGuardCanonicalMap(node.Metadata)
	s.nodes[node.ID] = node

	observed := make(map[string]struct{}, len(command.Services))
	for _, projection := range command.Services {
		observed[projection.Legacy.ID] = struct{}{}
		s.persistGuardInventoryServiceLocked(projection, now)
	}
	if command.ManifestObserved {
		// Absence of service evidence is not evidence of absence: only a
		// manifest with at least one observed service may prune stale rows.
		if len(observed) > 0 {
			s.pruneGuardInventoryServicesLocked(command, observed, now)
		} else {
			s.markGuardInventoryServicesUnavailableLocked(command, now)
		}
	}
}

// guardInventoryRevisionKey stamps retained rows with the accepted inventory
// revision so the read model treats them as part of the current projection.
const (
	guardInventoryRevisionKey    = "inventory_revision"
	guardServiceAccessReasonKey  = "reason_code"
	guardServiceNoEvidenceReason = "inventory_evidence_missing"
)

// markGuardInventoryServicesUnavailableLocked mirrors the Postgres projection:
// a manifest observed without service evidence keeps existing rows but flips
// their access to unavailable instead of deleting history.
func (s *MemoryStore) markGuardInventoryServicesUnavailableLocked(command GuardInventoryProjection, now time.Time) {
	tenantID, stackID, serverID := command.Event.TenantID, command.Event.Runtime.StackID, command.Event.ServerID
	revision, _ := inventoryMetadataInt64(command.Node.Metadata, guardInventoryRevisionKey)
	for serviceID, service := range s.svcs {
		if service.TenantID != tenantID || service.StackID != stackID || service.NodeID != serverID ||
			!strings.EqualFold(strings.TrimSpace(service.Source), command.ServiceSource) {
			continue
		}
		service.URL = ""
		service.Metadata = mergeMaps(service.Metadata, map[string]any{guardInventoryRevisionKey: revision})
		service.UpdatedAt = now
		s.svcs[serviceID] = service

		runtime, exists := s.serviceRuntime[serviceID]
		if !exists {
			runtime = backfilledServiceRuntime(service, serverID)
			runtime.Source = service.Source
		}
		access := map[string]any{
			legacyServiceAccessModeKey:  legacyServiceRuntimeUnavailable,
			guardServiceAccessReasonKey: guardServiceNoEvidenceReason,
		}
		if state := guardServiceRetainedControlState(service); state != "" {
			access = guardServiceUnavailableAccess(state)
		}
		runtime.ServerID = serverID
		runtime.DesiredState = firstNonEmpty(runtime.DesiredState, legacyServiceDesiredState(service.Status))
		runtime.Access = access
		runtime.Metadata = mergeMaps(runtime.Metadata, map[string]any{guardInventoryRevisionKey: revision})
		runtime.UpdatedAt = now
		s.serviceRuntime[serviceID] = runtime
	}
}

func (s *MemoryStore) persistGuardInventoryServiceLocked(projection GuardInventoryServiceProjection, now time.Time) {
	legacy := projection.Legacy
	runtime := projection.Runtime
	legacy.Metadata = cloneGuardCanonicalMap(legacy.Metadata)
	runtime.Access = cloneGuardCanonicalMap(runtime.Access)
	runtime.Metadata = cloneGuardCanonicalMap(runtime.Metadata)
	runtime.Capabilities = append([]string(nil), runtime.Capabilities...)
	runtime.ObservedAt = cloneTime(runtime.ObservedAt)

	existingLegacy, legacyExists := s.svcs[legacy.ID]
	if legacyExists {
		legacy.CreatedAt = existingLegacy.CreatedAt
	} else {
		legacy.CreatedAt = now
	}
	legacy.UpdatedAt = now

	existingRuntime, runtimeExists := s.serviceRuntime[runtime.ID]
	runtime.ServiceInstance = firstNonEmpty(runtime.ServiceInstance, "default")
	runtime = canonicalServiceRuntimeStates(runtime)
	if runtimeExists {
		runtime.CreatedAt = existingRuntime.CreatedAt
		runtime.DesiredState = existingRuntime.DesiredState
	} else {
		runtime.CreatedAt = now
	}
	runtime.UpdatedAt = now
	// Status describes what the runtime is doing and is derived from the
	// observed dimension; health stays independent.
	legacy.Status = derivedServiceStatus(existingLegacy, runtime.ObservedState)
	// Ownership and provenance are resolved once, on the runtime projection.
	// The legacy row mirrors them so a reader of either projection gets the
	// same answer, exactly as UpsertServiceRuntime and the Postgres aggregate
	// already do.
	legacy.Source = runtime.Source
	legacy.ManagementState = runtime.ManagementState

	controlState := ""
	if legacyExists {
		controlState = guardServiceRetainedControlState(existingLegacy)
	}
	if controlState != "" {
		legacy.Status = existingLegacy.Status
		legacy.MigrationStatus = existingLegacy.MigrationStatus
		legacy.URL = ""
		legacy.Metadata = mergeMaps(existingLegacy.Metadata, legacy.Metadata)
		if runtimeExists {
			runtime.Metadata = mergeMaps(existingRuntime.Metadata, runtime.Metadata)
		}
		runtime.Access = guardServiceUnavailableAccess(controlState)
	}

	s.svcs[legacy.ID] = legacy
	s.serviceRuntime[runtime.ID] = runtime
}

func (s *MemoryStore) guardInventoryRetainedServiceIDsLocked(command GuardInventoryProjection) map[string]struct{} {
	observed := make(map[string]struct{}, len(command.Services))
	for _, projection := range command.Services {
		observed[projection.Legacy.ID] = struct{}{}
	}
	retained := make(map[string]struct{})
	for serviceID, service := range s.svcs {
		if service.TenantID != command.Event.TenantID || service.StackID != command.Event.Runtime.StackID ||
			service.NodeID != command.Event.ServerID || !strings.EqualFold(strings.TrimSpace(service.Source), command.ServiceSource) {
			continue
		}
		if _, present := observed[serviceID]; present {
			continue
		}
		// With observed services, only control-state rows survive the prune.
		// With zero observed services, no prune runs (absence of evidence is
		// not evidence of absence) and every existing row is retained.
		if len(observed) > 0 && guardServiceRetainedControlState(service) == "" {
			continue
		}
		retained[serviceID] = struct{}{}
	}
	return retained
}

func (s *MemoryStore) pruneGuardInventoryServicesLocked(command GuardInventoryProjection, observed map[string]struct{}, now time.Time) {
	tenantID, stackID, serverID := command.Event.TenantID, command.Event.Runtime.StackID, command.Event.ServerID
	revision, _ := inventoryMetadataInt64(command.Node.Metadata, "inventory_revision")
	for serviceID, service := range s.svcs {
		if service.TenantID != tenantID || service.StackID != stackID || service.NodeID != serverID ||
			!strings.EqualFold(strings.TrimSpace(service.Source), command.ServiceSource) {
			continue
		}
		if _, retained := observed[serviceID]; retained {
			continue
		}
		if state := guardServiceRetainedControlState(service); state != "" {
			service.URL = ""
			service.Metadata = mergeMaps(service.Metadata, map[string]any{"inventory_revision": revision})
			service.UpdatedAt = now
			s.svcs[serviceID] = service

			runtime, exists := s.serviceRuntime[serviceID]
			if !exists {
				runtime = backfilledServiceRuntime(service, serverID)
				runtime.Source = service.Source
			}
			runtime.ServerID = serverID
			runtime.DesiredState = firstNonEmpty(runtime.DesiredState, legacyServiceDesiredState(service.Status))
			runtime.Access = guardServiceUnavailableAccess(state)
			runtime.Metadata = mergeMaps(runtime.Metadata, map[string]any{"inventory_revision": revision})
			runtime.UpdatedAt = now
			s.serviceRuntime[serviceID] = runtime
			continue
		}
		delete(s.svcs, serviceID)
		if runtime, ok := s.serviceRuntime[serviceID]; ok && runtime.TenantID == tenantID && runtime.StackID == stackID &&
			runtime.ServerID == serverID && strings.EqualFold(runtime.Source, command.ServiceSource) {
			delete(s.serviceRuntime, serviceID)
		}
	}
	for serviceID, service := range s.serviceRuntime {
		if service.TenantID != tenantID || service.StackID != stackID || service.ServerID != serverID ||
			!strings.EqualFold(strings.TrimSpace(service.Source), command.ServiceSource) {
			continue
		}
		if _, retained := observed[serviceID]; retained {
			continue
		}
		if legacy, ok := s.svcs[serviceID]; ok && guardServiceRetainedControlState(legacy) != "" {
			continue
		}
		delete(s.serviceRuntime, serviceID)
	}
}

var _ GuardInventoryProjectionStore = (*MemoryStore)(nil)
