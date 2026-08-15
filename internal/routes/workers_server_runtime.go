//nolint:goconst
package routes

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/kombifyio/techstack/internal/runtimeproduct/vmlease"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/runtimeidentity"
	"github.com/kombifyio/techstack/pkg/serverregistry"
	"github.com/kombifyio/techstack/pkg/vmleases"
)

func (h workerRouteHandlers) projectServerEnrollment(ctx context.Context, worker controlplane.Worker, serverID, leaseID string, now time.Time, source string) error {
	return h.projectServerEnrollmentWithMetadata(ctx, worker, serverID, leaseID, now, source, nil)
}

// projectServerEnrollmentWithMetadata is the control-plane half of the
// application-level Connect flow. Metadata here is declared target context
// (for example a configured local address and StackKit), never an observation:
// connection, health, channels and LastHeartbeatAt remain owned by authenticated
// Guard heartbeat/inventory ingestion.
func (h workerRouteHandlers) projectServerEnrollmentWithMetadata(
	ctx context.Context,
	worker controlplane.Worker,
	serverID, leaseID string,
	now time.Time,
	source string,
	metadata map[string]any,
) error {
	if h.serverStore == nil {
		return nil
	}
	runtimeMetadata := mergeAnyMaps(map[string]any{
		"runtime_agent_id": worker.ID,
		"authority":        "guard",
	}, metadata)
	_, err := h.applyServerEnrollment(ctx, controlplane.ServerEnrollment{
		Node: controlplane.Node{
			ID: serverID, TenantID: worker.TenantID, InstanceID: worker.InstanceID, StackID: worker.StackID,
			WorkerID: worker.ID, Name: firstNonEmpty(worker.Hostname, serverID), Role: "foundation", Status: "pending",
			Metadata: map[string]any{"authority": "control-plane", "runtime_agent_id": worker.ID},
		},
		Event: controlplane.ServerEvent{
			TenantID: worker.TenantID, ServerID: serverID,
			Authority: controlplane.ServerEventAuthorityControlPlane,
			Source:    source, SourceID: "worker-enrollment", ObservedAt: now,
			Evidence: map[string]any{"runtime_agent_id": worker.ID},
			Runtime: controlplane.ServerRuntime{
				ID: serverID, TenantID: worker.TenantID, InstanceID: worker.InstanceID,
				StackID: worker.StackID, OwnerSubjectID: worker.OwnerSubjectID,
				WorkerID: worker.ID, NodeID: serverID, LeaseID: leaseID,
				ProviderRef: worker.Provider, Name: firstNonEmpty(worker.Hostname, serverID),
				LifecycleState:      string(serverregistry.LifecycleEnrolling),
				DesiredState:        string(serverregistry.DesiredRunning),
				ConnectionState:     string(serverregistry.ConnectionPending),
				HealthState:         string(serverregistry.HealthUnknown),
				LifecycleReasonCode: "awaiting_guard_heartbeat",
				Metadata:            runtimeMetadata,
			}}})
	return err
}

func (h workerRouteHandlers) applyServerEnrollment(ctx context.Context, command controlplane.ServerEnrollment) (*controlplane.ServerEventResult, error) {
	store, ok := h.serverStore.(controlplane.ServerEnrollmentStore)
	if !ok {
		return nil, fmt.Errorf("canonical server store does not support atomic control-plane enrollment")
	}
	for attempt := 0; attempt < 4; attempt++ {
		current, err := h.serverStore.GetServerRuntime(ctx, command.Event.TenantID, command.Event.ServerID)
		attemptCommand := command
		switch {
		case err == nil:
			attemptCommand.Event.ExpectedRevision = current.Revision
			attemptCommand.Event.Generation = current.Generation
			if routeRuntimeBindingsChanged(*current, attemptCommand.Event.Runtime) {
				attemptCommand.Event.Generation++
			}
			stripControlPlaneObservationPatch(&attemptCommand.Event.Runtime)
		case errors.Is(err, controlplane.ErrNotFound):
			attemptCommand.Event.ExpectedRevision = 0
			attemptCommand.Event.Generation = 1
		default:
			return nil, err
		}
		result, applyErr := store.ApplyServerEnrollment(ctx, attemptCommand)
		if applyErr == nil {
			return result, nil
		}
		if !errors.Is(applyErr, controlplane.ErrConflict) {
			return nil, applyErr
		}
		latest, latestErr := h.serverStore.GetServerRuntime(ctx, command.Event.TenantID, command.Event.ServerID)
		if errors.Is(err, controlplane.ErrNotFound) && latestErr == nil {
			continue
		}
		if err == nil && latestErr == nil && latest.Revision != current.Revision {
			continue
		}
		return nil, applyErr
	}
	return nil, fmt.Errorf("%w: server enrollment contention exceeded retry budget", controlplane.ErrConflict)
}

type guardEventPosition struct {
	Epoch      string
	Sequence   int64
	ObservedAt time.Time
}

func (h workerRouteHandlers) projectServerHeartbeat(ctx context.Context, worker controlplane.Worker, serverID, leaseID string, now time.Time, source string, supplied guardEventPosition) error {
	if h.serverStore == nil {
		return nil
	}
	position, positionErr := validateGuardEventPosition(now, supplied)
	if positionErr != nil {
		return positionErr
	}
	connection, health := serverregistry.DeriveObservedState(now, &now, "")
	result, err := h.applyServerEvent(ctx, controlplane.ServerEvent{
		TenantID: worker.TenantID, ServerID: serverID,
		Authority: controlplane.ServerEventAuthorityGuard,
		Source:    source, SourceID: worker.ID, SourceEpoch: position.Epoch,
		SourceSequence: position.Sequence, ObservedAt: position.ObservedAt,
		ClearConnectionReason: true, ClearHealthReason: true,
		Evidence: map[string]any{"runtime_agent_id": worker.ID},
		Runtime: controlplane.ServerRuntime{
			ID: serverID, TenantID: worker.TenantID, InstanceID: worker.InstanceID,
			StackID: worker.StackID, OwnerSubjectID: worker.OwnerSubjectID,
			WorkerID: worker.ID, NodeID: serverID, LeaseID: leaseID,
			ProviderRef: worker.Provider, Name: firstNonEmpty(worker.Hostname, serverID),
			ConnectionState: string(connection), HealthState: string(health),
			LastHeartbeatAt: &now,
			Channels: []controlplane.ServerChannel{{
				Type: "guard-api", Role: "primary", State: "connected", ObservedAt: &now,
				Metadata: map[string]any{"authenticated": true},
			}},
			Metadata: mergeAnyMaps(map[string]any{
				"runtime_agent_id":    worker.ID,
				"authority":           "guard",
				"observed_host_state": "",
			}, func() map[string]any {
				if convergence := runtimeConvergenceMetadata(worker); convergence != nil {
					return map[string]any{"runtime_convergence": convergence}
				}
				return nil
			}()),
		}})
	if err != nil {
		return err
	}
	return h.promoteObservedEnrollment(ctx, result, now)
}

func (h workerRouteHandlers) projectServerInventory(ctx context.Context, worker controlplane.Worker, serverID, leaseID string, req workerInventoryRequest, now time.Time) (*controlplane.GuardInventoryProjectionResult, error) {
	if h.serverStore == nil {
		return nil, nil
	}
	observedHostState := ""
	for _, service := range req.Services {
		if inventoryServiceHealthState(service, "healthy") == "unhealthy" {
			observedHostState = runtimeHealthDegraded
			break
		}
	}
	position, positionErr := validateGuardEventPosition(now, guardEventPosition{
		Epoch: req.SourceEpoch, Sequence: req.SourceSequence, ObservedAt: req.ObservedAt,
	})
	if positionErr != nil {
		return nil, positionErr
	}
	observedAt := position.ObservedAt
	// The persisted projection is a pure function of the signed observation.
	// Read models derive wall-clock staleness from LastHeartbeatAt separately;
	// receipt-time-dependent state here would make an exact replay diverge.
	connection, health := serverregistry.DeriveObservedState(observedAt, &observedAt, observedHostState)
	node, services := buildInventoryRegistryProjection(worker, serverID, req, 0, observedAt)
	runtimeMetadata := map[string]any{
		"runtime_agent_id":            worker.ID,
		"agent_version":               req.AgentVersion,
		"authority":                   "guard",
		"inventory_source":            "guard-inventory",
		"inventory_observed_at":       observedAt.Format(time.RFC3339Nano),
		"service_projection_expected": projectedInventoryServiceCount(worker.StackID, serverID, req.Services),
		"stackkit_manifest_observed":  req.ManifestObserved,
		"service_discovery_observed":  req.DiscoveryObserved,
		"observed_host_state":         observedHostState,
		"host":                        inventoryHostMap(req.Host),
		"endpoints":                   inventoryEndpoints(req.Endpoints),
	}
	if convergence := runtimeConvergenceMetadata(worker); convergence != nil {
		runtimeMetadata["runtime_convergence"] = convergence
	}
	inventoryPayload := map[string]any{
		"host":       inventoryHostMap(req.Host),
		"services":   inventoryServices(req.Services),
		"channels":   inventoryChannels(req.Channels),
		"endpoints":  inventoryEndpoints(req.Endpoints),
		"deployment": inventoryDeploymentMetadata(req),
	}
	if convergence := runtimeConvergenceMetadata(worker); convergence != nil {
		inventoryPayload["runtime_convergence"] = convergence
	}
	result, err := h.applyGuardInventoryProjection(ctx, controlplane.GuardInventoryProjection{
		ServiceSource:    stackKitsInventorySource,
		ManifestObserved: req.ManifestObserved,
		Node:             node,
		Services:         services,
		Event: controlplane.ServerEvent{
			TenantID: worker.TenantID, ServerID: serverID,
			Authority: controlplane.ServerEventAuthorityGuard,
			Source:    "guard-inventory", SourceID: worker.ID, SourceEpoch: position.Epoch,
			SourceSequence: position.Sequence, ObservedAt: observedAt,
			ClearConnectionReason: true, ClearHealthReason: true,
			Evidence: map[string]any{"runtime_agent_id": worker.ID},
			Runtime: controlplane.ServerRuntime{
				ID: serverID, TenantID: worker.TenantID, InstanceID: worker.InstanceID,
				StackID: worker.StackID, OwnerSubjectID: worker.OwnerSubjectID,
				WorkerID: worker.ID, NodeID: serverID, LeaseID: leaseID,
				ProviderRef: worker.Provider, Name: firstNonEmpty(req.Hostname, worker.Hostname, serverID),
				ConnectionState: string(connection), HealthState: string(health),
				LastHeartbeatAt: &observedAt,
				Channels:        canonicalInventoryChannels(req.Channels, observedAt),
				Metadata:        mergeAnyMaps(runtimeMetadata, inventoryDeploymentMetadata(req)),
			},
			Inventory: &controlplane.ServerInventoryEvent{
				Source:    "guard-inventory",
				Inventory: inventoryPayload,
			},
		},
	})
	if err != nil {
		return nil, err
	}
	if result != nil && result.ServerEvent != nil && (result.ServerEvent.Applied || result.Replayed) {
		if err := h.promoteObservedEnrollment(ctx, result.ServerEvent, now); err != nil {
			return nil, err
		}
	}
	return result, nil
}

func (h workerRouteHandlers) applyGuardInventoryProjection(ctx context.Context, projection controlplane.GuardInventoryProjection) (*controlplane.GuardInventoryProjectionResult, error) {
	projectionStore, ok := h.serverStore.(controlplane.GuardInventoryProjectionStore)
	if !ok {
		return nil, fmt.Errorf("canonical server store does not support atomic Guard inventory projection")
	}
	for attempt := 0; attempt < 4; attempt++ {
		current, err := h.serverStore.GetServerRuntime(ctx, projection.Event.TenantID, projection.Event.ServerID)
		attemptProjection := projection
		switch {
		case err == nil:
			attemptProjection.Event.ExpectedRevision = current.Revision
			attemptProjection.Event.Generation = current.Generation
		case errors.Is(err, controlplane.ErrNotFound):
			attemptProjection.Event.ExpectedRevision = 0
			attemptProjection.Event.Generation = 1
		default:
			return nil, err
		}

		result, applyErr := projectionStore.ApplyGuardInventoryProjection(ctx, attemptProjection)
		if applyErr == nil {
			return result, nil
		}
		if !errors.Is(applyErr, controlplane.ErrConflict) {
			return nil, applyErr
		}
		latest, latestErr := h.serverStore.GetServerRuntime(ctx, projection.Event.TenantID, projection.Event.ServerID)
		if errors.Is(err, controlplane.ErrNotFound) && latestErr == nil {
			continue
		}
		if err == nil && latestErr == nil && latest.Revision != current.Revision {
			continue
		}
		return nil, applyErr
	}
	return nil, fmt.Errorf("%w: Guard inventory contention exceeded retry budget", controlplane.ErrConflict)
}

func projectedInventoryServiceCount(stackID, serverID string, services []workerInventoryService) int {
	identities := make(map[string]bool, len(services))
	for _, service := range services {
		serviceKey := normalizeServiceKey(firstNonEmpty(service.ServiceID, service.Key, service.Name))
		serviceID := runtimeidentity.ServiceID(stackID, serverID, serviceKey, service.Instance)
		if serviceID != "" {
			identities[serviceID] = true
		}
	}
	return len(identities)
}

func (h workerRouteHandlers) applyServerEvent(ctx context.Context, event controlplane.ServerEvent) (*controlplane.ServerEventResult, error) {
	eventStore, ok := h.serverStore.(controlplane.ServerEventStore)
	if !ok {
		return nil, fmt.Errorf("canonical server store does not support atomic events")
	}
	for attempt := 0; attempt < 4; attempt++ {
		current, err := h.serverStore.GetServerRuntime(ctx, event.TenantID, event.ServerID)
		attemptEvent := event
		switch {
		case err == nil:
			attemptEvent.ExpectedRevision = current.Revision
			attemptEvent.Generation = current.Generation
			if attemptEvent.Authority == controlplane.ServerEventAuthorityControlPlane {
				if routeRuntimeBindingsChanged(*current, attemptEvent.Runtime) {
					attemptEvent.Generation++
				}
				stripControlPlaneObservationPatch(&attemptEvent.Runtime)
			}
			if attemptEvent.Authority == controlplane.ServerEventAuthorityGuard {
				// The authenticated worker was already resolved from the control
				// plane. Existing authority fields are never re-projected from an
				// observation payload. A Guard cannot create the aggregate.
				stripGuardAuthorityPatch(&attemptEvent.Runtime)
			}
		case errors.Is(err, controlplane.ErrNotFound):
			attemptEvent.ExpectedRevision = 0
			attemptEvent.Generation = 1
		default:
			return nil, err
		}

		result, applyErr := eventStore.ApplyServerEvent(ctx, attemptEvent)
		if applyErr == nil {
			return result, nil
		}
		if !errors.Is(applyErr, controlplane.ErrConflict) {
			return nil, applyErr
		}
		latest, latestErr := h.serverStore.GetServerRuntime(ctx, event.TenantID, event.ServerID)
		if errors.Is(err, controlplane.ErrNotFound) && latestErr == nil {
			continue
		}
		if err == nil && latestErr == nil && latest.Revision != current.Revision {
			continue
		}
		return nil, applyErr
	}
	return nil, fmt.Errorf("%w: server event contention exceeded retry budget", controlplane.ErrConflict)
}

func stripControlPlaneObservationPatch(patch *controlplane.ServerRuntime) {
	if patch == nil {
		return
	}
	patch.ConnectionState = ""
	patch.HealthState = ""
	patch.ConnectionReasonCode = ""
	patch.HealthReasonCode = ""
	patch.LastHeartbeatAt = nil
	patch.Channels = nil
}

func stripGuardAuthorityPatch(patch *controlplane.ServerRuntime) {
	if patch == nil {
		return
	}
	patch.InstanceID = ""
	patch.StackID = ""
	patch.OwnerSubjectID = ""
	patch.WorkerID = ""
	patch.NodeID = ""
	patch.LeaseID = ""
	patch.ProviderRef = ""
	patch.Name = ""
	patch.LifecycleReasonCode = ""
	patch.DesiredReasonCode = ""
}

func (h workerRouteHandlers) promoteObservedEnrollment(ctx context.Context, observed *controlplane.ServerEventResult, now time.Time) error {
	if observed == nil || observed.Server == nil {
		return nil
	}
	// A Guard event can be durably accepted before the lease projection is
	// repaired. Retries of that same authenticated observation are intentionally
	// returned as an unchanged result; they must still be allowed to converge
	// the lease instead of leaving deployment permanently enrollment-pending.
	server := observed.Server
	if server.ConnectionState != string(serverregistry.ConnectionConnected) && server.ConnectionState != string(serverregistry.ConnectionDegraded) {
		return nil
	}
	switch serverregistry.LifecycleState(server.LifecycleState) {
	case serverregistry.LifecycleEnrolling, serverregistry.LifecycleProvisioning, serverregistry.LifecycleActive:
	default:
		return nil
	}
	// The authenticated Guard observation is the canonical positive enrollment
	// proof. Commit that lifecycle transition before repairing the retired lease
	// projection so a legacy projection failure cannot hold a native server in
	// enrolling after its Guard is already connected and healthy.
	if serverregistry.LifecycleState(server.LifecycleState) != serverregistry.LifecycleActive {
		if _, err := h.applyServerEvent(ctx, controlplane.ServerEvent{
			TenantID: server.TenantID, ServerID: server.ID,
			Authority: controlplane.ServerEventAuthorityControlPlane,
			Source:    "enrollment-controller", SourceID: "enrollment-controller", ObservedAt: now,
			ClearLifecycleReason: true,
			Evidence: map[string]any{
				"guard_server_revision": server.Revision,
				"guard_source_epoch":    server.SourceEpoch,
				"guard_source_sequence": server.SourceSequence,
			},
			Runtime: controlplane.ServerRuntime{LifecycleState: string(serverregistry.LifecycleActive)},
		}); err != nil {
			return err
		}
	}
	// The canonical authenticated Guard observation above is the enrollment
	// authority. A retired lease projection may lag or reject its metadata
	// repair, but it must not turn a healthy heartbeat into HTTP 500 and prevent
	// the following inventory sample from carrying the rollout target.
	_ = h.markManagedRuntimeEnrolled(ctx, server.TenantID, server.LeaseID)
	return nil
}

type managedRuntimeEnrollmentStore interface {
	Get(context.Context, string, vmlease.LeaseID) (*vmlease.Lease, error)
	Patch(context.Context, string, vmlease.LeaseID, vmleases.PatchRequest) (*vmlease.Lease, error)
}

func (h workerRouteHandlers) markManagedRuntimeEnrolled(ctx context.Context, tenantID, leaseID string) error {
	if strings.TrimSpace(tenantID) == "" || strings.TrimSpace(leaseID) == "" {
		return nil
	}
	store, ok := h.managedRuntimeLeases.(managedRuntimeEnrollmentStore)
	if !ok {
		return nil
	}
	lease, err := store.Get(ctx, strings.TrimSpace(tenantID), vmlease.LeaseID(strings.TrimSpace(leaseID)))
	if errors.Is(err, vmleases.ErrNotFound) {
		// Native provider-control leases no longer have a legacy
		// techstack_vm_leases projection. The authenticated Guard event and
		// canonical server binding remain the enrollment authority, so absence
		// from that retired projection must not leave the server enrolling.
		return nil
	}
	if err != nil {
		return fmt.Errorf("load managed runtime lease enrollment: %w", err)
	}
	if lease == nil || strings.EqualFold(strings.TrimSpace(lease.Metadata["runtime_enrollment_status"]), monthlyRuntimeEnrollmentStatusEnrolled) {
		return nil
	}
	if _, err := store.Patch(ctx, strings.TrimSpace(tenantID), vmlease.LeaseID(strings.TrimSpace(leaseID)), vmleases.PatchRequest{
		Metadata: map[string]string{"runtime_enrollment_status": monthlyRuntimeEnrollmentStatusEnrolled},
	}); err != nil {
		return fmt.Errorf("persist managed runtime enrollment: %w", err)
	}
	return nil
}

func validateGuardEventPosition(receiptAt time.Time, position guardEventPosition) (guardEventPosition, error) {
	position.Epoch = strings.TrimSpace(position.Epoch)
	if position.Epoch == "" || position.Sequence <= 0 || position.ObservedAt.IsZero() {
		return guardEventPosition{}, fmt.Errorf("%w: Guard source_epoch, positive source_sequence, and observed_at are required", controlplane.ErrConflict)
	}
	if len(position.Epoch) > 128 {
		return guardEventPosition{}, fmt.Errorf("%w: Guard source_epoch exceeds 128 bytes", controlplane.ErrConflict)
	}
	position.ObservedAt = position.ObservedAt.UTC()
	if position.ObservedAt.After(receiptAt.UTC().Add(5 * time.Minute)) {
		return guardEventPosition{}, fmt.Errorf("%w: Guard observed_at is too far in the future", controlplane.ErrConflict)
	}
	return position, nil
}

func routeRuntimeBindingsChanged(current, patch controlplane.ServerRuntime) bool {
	for _, values := range [][2]string{
		{current.WorkerID, patch.WorkerID}, {current.NodeID, patch.NodeID},
		{current.LeaseID, patch.LeaseID}, {current.ProviderRef, patch.ProviderRef},
	} {
		if incoming := strings.TrimSpace(values[1]); incoming != "" && incoming != strings.TrimSpace(values[0]) {
			return true
		}
	}
	return false
}

func canonicalInventoryChannels(channels []workerInventoryChannel, now time.Time) []controlplane.ServerChannel {
	result := make([]controlplane.ServerChannel, 0, len(channels))
	for index, channel := range channels {
		kind := canonicalGuardChannelType(channel.Kind)
		if kind == "" {
			continue
		}
		role := "fallback"
		if index == 0 || strings.Contains(strings.ToLower(kind), "grpc") || strings.Contains(strings.ToLower(kind), "mtls") {
			role = "primary"
		}
		projected := controlplane.ServerChannel{
			Type: kind, Role: role, State: canonicalGuardChannelState(channel.Status), ObservedAt: &now,
		}
		if provenance := canonicalGuardChannelProvenance(channel.Provenance); provenance != "" {
			projected.Metadata = map[string]any{"provenance": provenance}
		}
		result = append(result, projected)
	}
	if len(result) == 0 {
		result = append(result, controlplane.ServerChannel{
			Type: "guard-api", Role: "primary", State: "connected", ObservedAt: &now,
			Metadata: map[string]any{"authenticated": true},
		})
	}
	return result
}

func canonicalGuardChannelType(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	switch value {
	case "guard-api", "grpc", "grpc-mtls", "ssh", "http", "https", "tunnel", "kombify-relay":
		return value
	default:
		return ""
	}
}

func canonicalGuardChannelState(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	switch value {
	case "connected", "available", "ok", "healthy", "degraded", "stale", "offline", "unavailable", "unknown":
		return value
	default:
		return "unknown"
	}
}

func canonicalGuardChannelProvenance(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	switch value {
	case "guard", "operator", "stackkit-access-manifest", "custom-domain", "lan", "kombify-relay", "tunnel":
		return value
	default:
		return ""
	}
}
