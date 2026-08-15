package controlplane

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/kombifyio/techstack/pkg/serverregistry"
)

const (
	// ServerEventAuthorityControlPlane owns identity bindings, desired state,
	// lifecycle, and decommissioning decisions.
	ServerEventAuthorityControlPlane = serverregistry.AuthorityControlPlane
	// ServerEventAuthorityGuard owns authenticated runtime observations only.
	ServerEventAuthorityGuard = serverregistry.AuthorityGuard

	serverEventHealthDimension = "health"
	serverEventGenerationKey   = "generation"
	serverEventNameBinding     = "name"
	serverEventHostKey         = "host"
)

type ServerInventoryEvent = serverregistry.InventoryObservation
type ServerEvent = serverregistry.Command
type ServerEventResult = serverregistry.Result

type preparedServerEvent struct {
	server      ServerRuntime
	transitions []ServerStateTransition
	inventory   *ServerInventorySnapshot
	outbox      *ServerRegistryOutboxItem
	applied     bool
}

//nolint:gocyclo // Admission validates the complete command envelope before authority dispatch.
func prepareServerEvent(current *ServerRuntime, event ServerEvent, now time.Time, sourceEpochSeen bool) (*preparedServerEvent, error) {
	event.TenantID = strings.TrimSpace(event.TenantID)
	event.ServerID = strings.TrimSpace(event.ServerID)
	event.Authority = strings.TrimSpace(event.Authority)
	event.Source = strings.TrimSpace(event.Source)
	event.SourceID = strings.TrimSpace(event.SourceID)
	event.SourceEpoch = strings.TrimSpace(event.SourceEpoch)
	if event.Source == "" {
		event.Source = event.Authority
	}
	if event.ObservedAt.IsZero() {
		event.ObservedAt = now.UTC()
	} else {
		event.ObservedAt = event.ObservedAt.UTC()
	}
	if event.TenantID == "" || event.ServerID == "" {
		return nil, fmt.Errorf("controlplane: server event tenant and server id required")
	}
	if event.Runtime.TenantID != "" && strings.TrimSpace(event.Runtime.TenantID) != event.TenantID {
		return nil, fmt.Errorf("%w: server event tenant does not match runtime patch", ErrConflict)
	}
	if event.Runtime.ID != "" && strings.TrimSpace(event.Runtime.ID) != event.ServerID {
		return nil, fmt.Errorf("%w: server event id does not match runtime patch", ErrConflict)
	}
	if event.Authority != ServerEventAuthorityControlPlane && event.Authority != ServerEventAuthorityGuard {
		return nil, fmt.Errorf("%w: unsupported server event authority %q", ErrConflict, event.Authority)
	}
	if strings.TrimSpace(event.Runtime.ReasonCode) != "" {
		return nil, fmt.Errorf("%w: unscoped server reason is not an authority command", ErrConflict)
	}
	if event.SourceID == "" {
		return nil, fmt.Errorf("%w: server event source id required", ErrConflict)
	}
	if len(event.Source) > 128 || len(event.SourceID) > 256 || len(event.SourceEpoch) > 128 {
		return nil, fmt.Errorf("%w: server event source checkpoint exceeds its bounded length", ErrConflict)
	}

	if current == nil {
		if event.ExpectedRevision != 0 {
			return nil, fmt.Errorf("%w: expected server revision %d for creation", ErrConflict, event.ExpectedRevision)
		}
	} else {
		if current.TenantID != event.TenantID || current.ID != event.ServerID {
			return nil, fmt.Errorf("%w: server event aggregate identity mismatch", ErrConflict)
		}
		if event.ExpectedRevision != current.Revision {
			return nil, fmt.Errorf("%w: server revision is %d, expected %d", ErrConflict, current.Revision, event.ExpectedRevision)
		}
	}

	if event.Authority == ServerEventAuthorityGuard {
		return prepareGuardServerEvent(current, event, now, sourceEpochSeen)
	}
	return prepareControlPlaneServerEvent(current, event, now)
}

//nolint:gocyclo // Guard admission keeps generation, custody, staleness, and observation checks together.
func prepareGuardServerEvent(current *ServerRuntime, event ServerEvent, now time.Time, sourceEpochSeen bool) (*preparedServerEvent, error) {
	if event.SourceEpoch == "" || event.SourceSequence <= 0 {
		return nil, fmt.Errorf("%w: Guard source epoch and positive sequence required", ErrConflict)
	}
	if current == nil {
		return nil, fmt.Errorf("%w: Guard cannot create a runtime-server identity", ErrConflict)
	}
	patch := event.Runtime
	if patch.LifecycleState != "" || patch.DesiredState != "" || patch.DecommissionedAt != nil ||
		patch.LifecycleReasonCode != "" || patch.DesiredReasonCode != "" ||
		event.ClearLifecycleReason || event.ClearDesiredReason {
		return nil, fmt.Errorf("%w: Guard cannot mutate lifecycle, desired state, their reasons, or decommissioning", ErrConflict)
	}
	if serverregistry.RuntimeTargetIntentPresent(patch.RuntimeTarget) {
		return nil, fmt.Errorf("%w: Guard cannot mutate runtime target classification", ErrConflict)
	}

	if event.Generation != current.Generation {
		return nil, fmt.Errorf("%w: Guard generation is %d, current generation is %d", ErrConflict, event.Generation, current.Generation)
	}
	if current.WorkerID == "" || current.WorkerID != event.SourceID {
		return nil, fmt.Errorf("%w: Guard source does not own this server generation", ErrConflict)
	}
	if current.LifecycleState == string(serverregistry.LifecycleDecommissioning) ||
		current.LifecycleState == string(serverregistry.LifecycleDecommissioned) {
		return unchangedServerEvent(current), nil
	}
	if event.SourceEpoch != current.SourceEpoch && sourceEpochSeen {
		return unchangedServerEvent(current), nil
	}
	if guardEventIsStale(*current, event) {
		return unchangedServerEvent(current), nil
	}
	if err := validateGuardBindings(*current, patch); err != nil {
		return nil, err
	}
	if err := validateSecretFreeObservation(event.Evidence, "evidence"); err != nil {
		return nil, err
	}
	if err := validateGuardObservationPayload(patch, event.Inventory); err != nil {
		return nil, err
	}

	next := baseServerRuntime(current, event, now)
	applyObservedServerPatch(&next, patch, event)
	next.SourceAuthority = ServerEventAuthorityGuard
	next.SourceID = event.SourceID
	next.SourceEpoch = event.SourceEpoch
	next.SourceSequence = event.SourceSequence
	next.SourceObservedAt = cloneTime(&event.ObservedAt)
	if err := validateServerEventHead(next); err != nil {
		return nil, err
	}
	return finalizePreparedServerEvent(current, next, event, now), nil
}

//nolint:gocyclo // Control-plane admission keeps generation and lifecycle CAS validation atomic.
func prepareControlPlaneServerEvent(current *ServerRuntime, event ServerEvent, now time.Time) (*preparedServerEvent, error) {
	patch := event.Runtime
	if err := validateSecretFreeObservation(event.Evidence, "evidence"); err != nil {
		return nil, err
	}
	if err := validateSecretFreeObservation(patch.Metadata, "metadata"); err != nil {
		return nil, err
	}
	if err := validateControlPlaneObservationPatch(current, patch, event); err != nil {
		return nil, err
	}
	next := baseServerRuntime(current, event, now)
	bindingsChanged := current != nil && runtimeBindingsChanged(*current, patch)
	targetGeneration := event.Generation
	if targetGeneration == 0 {
		targetGeneration = next.Generation
	}
	if current == nil {
		if targetGeneration != 1 {
			return nil, fmt.Errorf("%w: first server generation must be 1", ErrConflict)
		}
	} else if bindingsChanged {
		if targetGeneration != current.Generation+1 {
			return nil, fmt.Errorf("%w: binding changes require the next server generation", ErrConflict)
		}
	} else if targetGeneration != current.Generation {
		return nil, fmt.Errorf("%w: server generation cannot change without a binding change", ErrConflict)
	}
	if current != nil && patch.LifecycleState != "" && patch.LifecycleState != current.LifecycleState {
		if err := serverregistry.ValidateLifecycleTransition(current.LifecycleState, patch.LifecycleState); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrConflict, err)
		}
	}
	applyControlPlaneServerPatch(&next, patch, event)
	next.Generation = targetGeneration
	if bindingsChanged {
		// A new binding generation fences every observation emitted by the old
		// Guard, even when a delayed HTTP request arrives later. Resetting the
		// projection is registry-owned invalidation, not a new control-plane
		// observation about the replacement Guard.
		next.SourceAuthority = ""
		next.SourceID = ""
		next.SourceEpoch = ""
		next.SourceSequence = 0
		next.SourceObservedAt = nil
		next.ConnectionState = string(serverregistry.ConnectionPending)
		next.HealthState = string(serverregistry.HealthUnknown)
		next.ConnectionReasonCode = ""
		next.HealthReasonCode = ""
		next.ConnectionChangedAt = event.ObservedAt
		next.HealthChangedAt = event.ObservedAt
		next.LastHeartbeatAt = nil
		next.Channels = nil
	}
	if err := validateServerEventHead(next); err != nil {
		return nil, err
	}
	return finalizePreparedServerEvent(current, next, event, now), nil
}

//nolint:gocyclo // The aggregate validator enumerates independent state invariants.
func validateServerEventHead(server ServerRuntime) error {
	if strings.TrimSpace(server.OwnerSubjectID) == "" {
		return fmt.Errorf("%w: server owner subject is required", ErrConflict)
	}
	if err := serverregistry.ValidateLifecycleState(server.LifecycleState); err != nil {
		return fmt.Errorf("%w: %v", ErrConflict, err)
	}
	if err := serverregistry.ValidateConnectionState(server.ConnectionState); err != nil {
		return fmt.Errorf("%w: %v", ErrConflict, err)
	}
	if err := serverregistry.ValidateHealthState(server.HealthState); err != nil {
		return fmt.Errorf("%w: %v", ErrConflict, err)
	}
	if err := serverregistry.ValidateDesiredState(server.DesiredState); err != nil {
		return fmt.Errorf("%w: %v", ErrConflict, err)
	}
	if err := serverregistry.ValidateRuntimeTarget(server.RuntimeTarget, server.LeaseID); err != nil {
		return fmt.Errorf("%w: %v", ErrConflict, err)
	}
	for dimension, reason := range map[string]string{
		"lifecycle": server.LifecycleReasonCode,
		"desired":   server.DesiredReasonCode, "connection": server.ConnectionReasonCode,
		serverEventHealthDimension: server.HealthReasonCode,
	} {
		if reason != strings.TrimSpace(reason) || len(reason) > 128 {
			return fmt.Errorf("%w: server %s reason code must be trimmed and at most 128 bytes", ErrConflict, dimension)
		}
	}
	switch serverregistry.LifecycleState(strings.TrimSpace(server.LifecycleState)) {
	case serverregistry.LifecycleDecommissioning:
		if server.DesiredState != string(serverregistry.DesiredAbsent) {
			return fmt.Errorf("%w: decommissioning requires desired state absent", ErrConflict)
		}
		if server.DecommissionedAt != nil {
			return fmt.Errorf("%w: decommissioned timestamp requires terminal lifecycle evidence", ErrConflict)
		}
	case serverregistry.LifecycleDecommissioned:
		if server.DesiredState != string(serverregistry.DesiredAbsent) {
			return fmt.Errorf("%w: decommissioned requires desired state absent", ErrConflict)
		}
		if server.DecommissionedAt == nil {
			return fmt.Errorf("%w: decommissioned requires terminal timestamp evidence", ErrConflict)
		}
	default:
		if server.DecommissionedAt != nil {
			return fmt.Errorf("%w: decommissioned timestamp is only valid for terminal lifecycle", ErrConflict)
		}
	}
	return nil
}

func baseServerRuntime(current *ServerRuntime, event ServerEvent, now time.Time) ServerRuntime {
	var next ServerRuntime
	if current != nil {
		next = *cloneServerRuntime(*current)
	} else {
		next = ServerRuntime{ID: event.ServerID, TenantID: event.TenantID, Revision: 0, Generation: 1}
		serverRuntimeDefaults(&next, now)
		// Defaults are shared with legacy projection writes, where a new row is
		// revision 1. Event creation increments below, so its pre-event head is
		// deliberately revision 0.
		next.Revision = 0
		next.CreatedAt = now
	}
	next.ID = event.ServerID
	next.TenantID = event.TenantID
	return next
}

func applyObservedServerPatch(next *ServerRuntime, patch ServerRuntime, event ServerEvent) {
	// Identity and lifecycle fields remain exactly as projected by the control
	// plane. Guard observations own only connection, health, heartbeat,
	// channels, metadata, and the optional inventory snapshot.
	connectionChanged := false
	healthChanged := false
	if patch.ConnectionState != "" {
		connectionChanged = next.ConnectionState != patch.ConnectionState
		next.ConnectionState = patch.ConnectionState
	}
	if patch.HealthState != "" {
		healthChanged = next.HealthState != patch.HealthState
		next.HealthState = patch.HealthState
	}
	connectionReason, healthReason := patch.ConnectionReasonCode, patch.HealthReasonCode
	connectionChanged = setDimensionReason(&next.ConnectionReasonCode, connectionReason, event.ClearConnectionReason) || connectionChanged
	healthChanged = setDimensionReason(&next.HealthReasonCode, healthReason, event.ClearHealthReason) || healthChanged
	if connectionChanged {
		next.ConnectionChangedAt = event.ObservedAt
	}
	if healthChanged {
		next.HealthChangedAt = event.ObservedAt
	}
	if patch.LastHeartbeatAt != nil {
		next.LastHeartbeatAt = cloneTime(patch.LastHeartbeatAt)
	}
	if len(patch.Channels) > 0 {
		next.Channels = cloneServerChannels(patch.Channels)
	}
	if len(patch.Metadata) > 0 {
		next.Metadata = mergeMaps(next.Metadata, patch.Metadata)
	}
}

func applyControlPlaneServerPatch(next *ServerRuntime, patch ServerRuntime, event ServerEvent) {
	next.InstanceID = firstNonEmpty(patch.InstanceID, next.InstanceID)
	next.StackID = firstNonEmpty(patch.StackID, next.StackID)
	next.OwnerSubjectID = firstNonEmpty(patch.OwnerSubjectID, next.OwnerSubjectID)
	next.WorkerID = firstNonEmpty(patch.WorkerID, next.WorkerID)
	next.NodeID = firstNonEmpty(patch.NodeID, next.NodeID)
	next.LeaseID = firstNonEmpty(patch.LeaseID, next.LeaseID)
	next.ProviderRef = firstNonEmpty(patch.ProviderRef, next.ProviderRef)
	if serverregistry.RuntimeTargetIntentPresent(patch.RuntimeTarget) {
		target := serverregistry.NormalizeRuntimeTarget(patch.RuntimeTarget)
		if target.EnvironmentClass != serverregistry.EnvironmentUnknown && target.ObservedAt == nil {
			observedAt := event.ObservedAt.UTC()
			target.ObservedAt = &observedAt
		}
		next.RuntimeTarget = target
	} else if next.RuntimeTarget.EnvironmentClass == serverregistry.EnvironmentUnknown {
		if target, ok := serverregistry.HostingerExternalVPSTarget(next.ProviderRef, event.ObservedAt); ok {
			next.RuntimeTarget = target
		}
	}
	next.Name = firstNonEmpty(patch.Name, next.Name, next.ID)
	lifecycleChanged := false
	desiredChanged := false
	if patch.LifecycleState != "" {
		lifecycleChanged = next.LifecycleState != patch.LifecycleState
		next.LifecycleState = patch.LifecycleState
	}
	if patch.DesiredState != "" {
		desiredChanged = next.DesiredState != patch.DesiredState
		next.DesiredState = patch.DesiredState
	}
	lifecycleReason, desiredReason := patch.LifecycleReasonCode, patch.DesiredReasonCode
	lifecycleChanged = setDimensionReason(&next.LifecycleReasonCode, lifecycleReason, event.ClearLifecycleReason) || lifecycleChanged
	desiredChanged = setDimensionReason(&next.DesiredReasonCode, desiredReason, event.ClearDesiredReason) || desiredChanged
	if lifecycleChanged {
		next.LifecycleChangedAt = event.ObservedAt
	}
	if desiredChanged {
		next.DesiredChangedAt = event.ObservedAt
	}
	if patch.LastHeartbeatAt != nil {
		next.LastHeartbeatAt = cloneTime(patch.LastHeartbeatAt)
	}
	if len(patch.Channels) > 0 {
		next.Channels = cloneServerChannels(patch.Channels)
	}
	if len(patch.Metadata) > 0 {
		next.Metadata = mergeMaps(next.Metadata, patch.Metadata)
	}
	if patch.DecommissionedAt != nil {
		next.DecommissionedAt = cloneTime(patch.DecommissionedAt)
	}
}

func setDimensionReason(current *string, incoming string, clear bool) bool {
	before := *current
	switch {
	case clear:
		*current = ""
	case incoming != "":
		*current = incoming
	}
	return before != *current
}

func finalizePreparedServerEvent(current *ServerRuntime, next ServerRuntime, event ServerEvent, now time.Time) *preparedServerEvent {
	next.Revision++
	next.UpdatedAt = now
	serverRuntimeDefaults(&next, now)
	transitions := serverEventTransitions(current, next, event)
	var inventory *ServerInventorySnapshot
	if event.Inventory != nil {
		next.InventoryRevision++
		inventory = &ServerInventorySnapshot{
			TenantID: event.TenantID, ServerID: event.ServerID,
			Revision:   next.InventoryRevision,
			Source:     firstNonEmpty(strings.TrimSpace(event.Inventory.Source), event.Source),
			ObservedAt: event.ObservedAt, Inventory: cloneMap(event.Inventory.Inventory),
		}
	}
	outbox := &ServerRegistryOutboxItem{
		TenantID:          next.TenantID,
		ServerID:          next.ID,
		AggregateRevision: next.Revision,
		Generation:        next.Generation,
		Authority:         event.Authority,
		Source:            event.Source,
		EventType:         "server.aggregate.updated",
		OccurredAt:        event.ObservedAt,
		Payload: map[string]any{
			jobServerIDKey:           next.ID,
			"aggregate_revision":     next.Revision,
			serverEventGenerationKey: next.Generation,
			"lifecycle_state":        next.LifecycleState,
			"desired_state":          next.DesiredState,
			"connection_state":       next.ConnectionState,
			"health_state":           next.HealthState,
			"inventory_revision":     next.InventoryRevision,
			"source_authority":       event.Authority,
		},
	}
	return &preparedServerEvent{
		server: next, transitions: transitions, inventory: inventory, outbox: outbox, applied: true,
	}
}

func unchangedServerEvent(current *ServerRuntime) *preparedServerEvent {
	return &preparedServerEvent{server: *cloneServerRuntime(*current), applied: false}
}

func serverEventTransitions(previous *ServerRuntime, current ServerRuntime, event ServerEvent) []ServerStateTransition {
	type dimension struct {
		name, from, to, fromReason, toReason string
	}
	dimensions := []dimension{
		{name: "lifecycle", to: current.LifecycleState, toReason: current.LifecycleReasonCode},
		{name: "desired", to: current.DesiredState, toReason: current.DesiredReasonCode},
		{name: "connection", to: current.ConnectionState, toReason: current.ConnectionReasonCode},
		{name: serverEventHealthDimension, to: current.HealthState, toReason: current.HealthReasonCode},
	}
	if previous != nil {
		dimensions[0].from = previous.LifecycleState
		dimensions[0].fromReason = previous.LifecycleReasonCode
		dimensions[1].from = previous.DesiredState
		dimensions[1].fromReason = previous.DesiredReasonCode
		dimensions[2].from = previous.ConnectionState
		dimensions[2].fromReason = previous.ConnectionReasonCode
		dimensions[3].from = previous.HealthState
		dimensions[3].fromReason = previous.HealthReasonCode
	}
	evidence := mergeMaps(event.Evidence, map[string]any{
		"server_revision":        current.Revision,
		serverEventGenerationKey: current.Generation,
		"source_authority":       event.Authority,
		"source_id":              event.SourceID,
	})
	if event.SourceEpoch != "" {
		evidence["source_epoch"] = event.SourceEpoch
		evidence["source_sequence"] = event.SourceSequence
	}
	out := make([]ServerStateTransition, 0, len(dimensions))
	for _, item := range dimensions {
		if item.to == "" || (item.from == item.to && item.fromReason == item.toReason) {
			continue
		}
		transitionEvidence := cloneMap(evidence)
		if item.fromReason != item.toReason {
			transitionEvidence["reason_changed"] = true
			if item.fromReason != "" {
				transitionEvidence["previous_reason_code"] = item.fromReason
			}
		}
		out = append(out, ServerStateTransition{
			TenantID: current.TenantID, ServerID: current.ID,
			Dimension: item.name, FromState: item.from, ToState: item.to,
			ReasonCode: item.toReason, Source: event.Source,
			ObservedAt: event.ObservedAt, Evidence: transitionEvidence,
		})
	}
	return out
}

func validateGuardBindings(current ServerRuntime, patch ServerRuntime) error {
	checks := []struct{ name, current, incoming string }{
		{"instance", current.InstanceID, patch.InstanceID},
		{"stack", current.StackID, patch.StackID},
		{"owner", current.OwnerSubjectID, patch.OwnerSubjectID},
		{"worker", current.WorkerID, patch.WorkerID},
		{"node", current.NodeID, patch.NodeID},
		{"lease", current.LeaseID, patch.LeaseID},
		{"provider", current.ProviderRef, patch.ProviderRef},
		{serverEventNameBinding, current.Name, patch.Name},
	}
	for _, check := range checks {
		if incoming := strings.TrimSpace(check.incoming); incoming != "" && incoming != strings.TrimSpace(check.current) {
			return fmt.Errorf("%w: Guard cannot change server %s binding", ErrConflict, check.name)
		}
	}
	return nil
}

//nolint:gocyclo // The authority validator enumerates the complete forbidden observation surface.
func validateControlPlaneObservationPatch(current *ServerRuntime, patch ServerRuntime, event ServerEvent) error {
	if event.Inventory != nil {
		return fmt.Errorf("%w: control plane cannot write Guard inventory", ErrConflict)
	}
	if patch.ConnectionReasonCode != "" || patch.HealthReasonCode != "" ||
		event.ClearConnectionReason || event.ClearHealthReason {
		return fmt.Errorf("%w: control plane cannot mutate Guard connection or health reasons", ErrConflict)
	}
	if current == nil {
		if connection := strings.TrimSpace(patch.ConnectionState); connection != "" && connection != string(serverregistry.ConnectionPending) {
			return fmt.Errorf("%w: initial connection state must be pending until Guard evidence arrives", ErrConflict)
		}
		if health := strings.TrimSpace(patch.HealthState); health != "" && health != string(serverregistry.HealthUnknown) {
			return fmt.Errorf("%w: initial health state must be unknown until Guard evidence arrives", ErrConflict)
		}
		if patch.LastHeartbeatAt != nil || len(patch.Channels) > 0 {
			return fmt.Errorf("%w: initial heartbeat and channels require Guard evidence", ErrConflict)
		}
		return nil
	}
	if strings.TrimSpace(patch.ConnectionState) != "" || strings.TrimSpace(patch.HealthState) != "" ||
		patch.LastHeartbeatAt != nil || len(patch.Channels) > 0 {
		return fmt.Errorf("%w: control plane cannot mutate Guard observation state", ErrConflict)
	}
	return nil
}

//nolint:gocyclo // The schema validator enumerates admitted metadata, channel, and inventory fields.
func validateGuardObservationPayload(patch ServerRuntime, inventory *ServerInventoryEvent) error {
	allowedMetadata := map[string]bool{
		"agent_version": true, "authority": true, "domain": true, "endpoints": true,
		serverEventHostKey: true, "inventory_observed_at": true, "inventory_source": true,
		"observed_host_state": true, "runtime_agent_id": true, "runtime_convergence": true,
		// The two provenance halves of the service model. Both are measured by
		// the Guard on the host, so they belong to observation authority: they
		// say whether a StackKit manifest was found and whether unmanaged
		// service discovery ran at all, which is what makes an empty service
		// set explainable rather than merely empty.
		"service_discovery_observed": true, "stackkit_manifest_observed": true,
		"service_projection_expected": true, "stackkit": true,
		"stackkit_mode": true, "stackkit_version": true,
	}
	for key := range patch.Metadata {
		if !allowedMetadata[strings.ToLower(strings.TrimSpace(key))] {
			return fmt.Errorf("%w: Guard metadata key %q is outside observation authority", ErrConflict, key)
		}
	}
	if err := validateSecretFreeObservation(patch.Metadata, "metadata"); err != nil {
		return err
	}
	for _, channel := range patch.Channels {
		if !validGuardChannelType(channel.Type) || !validGuardChannelRole(channel.Role) || !validGuardChannelState(channel.State) {
			return fmt.Errorf("%w: Guard channel type, role, or state is outside the canonical schema", ErrConflict)
		}
		if strings.TrimSpace(channel.EndpointRef) != "" {
			return fmt.Errorf("%w: Guard channels must use server-side access references", ErrConflict)
		}
		for key, value := range channel.Metadata {
			switch strings.ToLower(strings.TrimSpace(key)) {
			case "authenticated":
				if _, ok := value.(bool); !ok {
					return fmt.Errorf("%w: Guard channel authenticated metadata must be boolean", ErrConflict)
				}
			case "provenance":
				provenance, ok := value.(string)
				if !ok || !validGuardChannelProvenance(provenance) {
					return fmt.Errorf("%w: Guard channel provenance is not a canonical value", ErrConflict)
				}
			default:
				return fmt.Errorf("%w: Guard channel metadata key %q is not secret-free", ErrConflict, key)
			}
		}
		if err := validateSecretFreeObservation(channel.Metadata, "channel.metadata"); err != nil {
			return err
		}
	}
	if inventory != nil {
		for key := range inventory.Inventory {
			switch strings.ToLower(strings.TrimSpace(key)) {
			case serverEventHostKey, "services", "channels", "endpoints", "deployment", "runtime_convergence":
			default:
				return fmt.Errorf("%w: Guard inventory key %q is outside the canonical schema", ErrConflict, key)
			}
		}
		if err := validateSecretFreeObservation(inventory.Inventory, "inventory"); err != nil {
			return err
		}
	}
	return nil
}

func validateSecretFreeObservation(value any, path string) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("%w: %s is not a JSON observation: %v", ErrConflict, path, err)
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	var normalized any
	if err := decoder.Decode(&normalized); err != nil {
		return fmt.Errorf("%w: %s is not a JSON observation: %v", ErrConflict, path, err)
	}
	return validateSecretFreeJSON(normalized, path)
}

//nolint:gocyclo // Recursive validation checks each supported JSON shape and secret pattern.
func validateSecretFreeJSON(value any, path string) error {
	switch typed := value.(type) {
	case map[string]any:
		for key, nested := range typed {
			normalized := strings.ToLower(strings.TrimSpace(key))
			for _, forbidden := range []string{"password", "passwd", "secret", "token", "credential", "private_key", "auth_hint"} {
				if strings.Contains(normalized, forbidden) {
					return fmt.Errorf("%w: %s.%s is not secret-free", ErrConflict, path, key)
				}
			}
			if err := validateSecretFreeJSON(nested, path+"."+key); err != nil {
				return err
			}
		}
	case []any:
		for index, nested := range typed {
			if err := validateSecretFreeJSON(nested, fmt.Sprintf("%s[%d]", path, index)); err != nil {
				return err
			}
		}
	case string:
		trimmed := strings.TrimSpace(typed)
		lower := strings.ToLower(trimmed)
		if strings.Contains(lower, "-----begin ") && strings.Contains(lower, "private key-----") {
			return fmt.Errorf("%w: %s contains private key material", ErrConflict, path)
		}
		if parsed, err := url.Parse(trimmed); err == nil && parsed.IsAbs() &&
			(parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "") {
			return fmt.Errorf("%w: %s contains credential-bearing URL material", ErrConflict, path)
		}
	}
	return nil
}

func validGuardChannelProvenance(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "guard", "operator", "stackkit-access-manifest", "custom-domain", "lan", "kombify-relay", "tunnel":
		return true
	default:
		return false
	}
}

func validGuardChannelType(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "guard-api", "grpc", "grpc-mtls", "ssh", "http", "https", "tunnel", "kombify-relay":
		return true
	default:
		return false
	}
}

func validGuardChannelRole(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "primary", "fallback":
		return true
	default:
		return false
	}
}

func validGuardChannelState(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case string(serverregistry.ConnectionConnected), "available", "ok", string(serverregistry.HealthHealthy),
		string(serverregistry.ConnectionDegraded), string(serverregistry.ConnectionStale),
		string(serverregistry.ConnectionOffline), "unavailable", string(serverregistry.HealthUnknown):
		return true
	default:
		return false
	}
}

func runtimeBindingsChanged(current ServerRuntime, patch ServerRuntime) bool {
	checks := [][2]string{
		{current.WorkerID, patch.WorkerID},
		{current.NodeID, patch.NodeID},
		{current.LeaseID, patch.LeaseID},
		{current.ProviderRef, patch.ProviderRef},
	}
	for _, check := range checks {
		if incoming := strings.TrimSpace(check[1]); incoming != "" && incoming != strings.TrimSpace(check[0]) {
			return true
		}
	}
	return false
}

func guardEventIsStale(current ServerRuntime, event ServerEvent) bool {
	if current.SourceEpoch == "" {
		return false
	}
	if current.SourceID != event.SourceID || current.SourceAuthority != ServerEventAuthorityGuard {
		return true
	}
	if current.SourceEpoch == event.SourceEpoch {
		return event.SourceSequence <= current.SourceSequence ||
			(current.SourceObservedAt != nil && event.ObservedAt.Before(*current.SourceObservedAt))
	}
	// A process restart starts a fresh epoch at sequence 1. Once accepted, a
	// delayed request from a superseded epoch (which necessarily has sequence
	// greater than 1, except its first request) cannot take ownership back.
	return event.SourceSequence != 1 ||
		(current.SourceObservedAt != nil && !event.ObservedAt.After(*current.SourceObservedAt))
}
