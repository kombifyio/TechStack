package controlplane

import (
	"fmt"
	"strings"
	"time"

	"github.com/kombifyio/techstack/pkg/serviceregistry"
)

const (
	// ServiceEventAuthorityControlPlane owns user intent: desired state,
	// identity bindings, and the control-plane workflow columns.
	ServiceEventAuthorityControlPlane = "control-plane"
	// ServiceEventAuthorityGuard owns authenticated runtime observations only.
	ServiceEventAuthorityGuard = "guard"

	serviceDimensionDesired    = "desired"
	serviceDimensionObserved   = "observed"
	serviceDimensionHealth     = "health"
	serviceDimensionManagement = "management"

	serviceInstanceDefault = "default"
	serviceSourceObserved  = serviceregistry.SourceObserved
	serviceEvidenceSource  = "source"
)

// ServiceStateTransition is one immutable, per-dimension state change of a
// service aggregate. It mirrors ServerStateTransition and is only ever written
// for a dimension that actually changed.
type ServiceStateTransition struct {
	ID         int64
	TenantID   string
	ServiceID  string
	Dimension  string
	FromState  string
	ToState    string
	ReasonCode string
	Source     string
	ObservedAt time.Time
	Evidence   map[string]any
	CreatedAt  time.Time
}

// ServiceEvent is an authority-labeled mutation against one service aggregate.
//
// Guard events carry measured observation only; they may seed desired state
// when the aggregate is created but can never mutate an existing intent.
// Control-plane events carry intent and identity but never observation.
type ServiceEvent struct {
	TenantID  string
	ServiceID string
	// ExpectedRevision is a strict compare-and-swap assertion when set: zero
	// means "the aggregate must not exist yet". A nil value accepts whatever
	// revision the aggregate row lock produced, because a measured observation
	// cannot know the revision it is racing and is already serialized by that
	// lock.
	ExpectedRevision *int64
	Authority        string
	Source           string
	ObservedAt       time.Time
	// Runtime is a partial patch. Empty string fields are "not asserted" and
	// retain the stored value.
	Runtime ServiceRuntime
	// NodeID, URL, and MigrationStatus are the legacy compatibility columns of
	// the same physical row. They are written by the same boundary so no caller
	// has to touch the services table directly.
	NodeID          string
	URL             string
	MigrationStatus string
	ReasonCode      string
	Evidence        map[string]any
	// EnforceIdentityBinding rejects an event whose identity bindings differ
	// from the stored aggregate instead of silently rebinding the row.
	EnforceIdentityBinding bool
}

// ServiceEventResult reports the exact write the boundary performed. Applied is
// false when the command was a byte-identical replay: the head keeps its
// revision and no transition row is written.
type ServiceEventResult struct {
	Service     *ServiceRuntime
	Revision    int64
	Status      string
	Transitions []ServiceStateTransition
	Applied     bool
}

// serviceAggregateHead is the stored service row, including the legacy
// compatibility columns that are not part of the canonical runtime projection.
type serviceAggregateHead struct {
	Runtime         ServiceRuntime
	Revision        int64
	Status          string
	MigrationStatus string
	NodeID          string
	URL             string
	Exists          bool
}

type preparedServiceEvent struct {
	head        serviceAggregateHead
	transitions []ServiceStateTransition
	applied     bool
}

//nolint:gocyclo // Admission validates the complete command envelope before authority dispatch.
func prepareServiceEvent(current *serviceAggregateHead, event ServiceEvent, now time.Time) (*preparedServiceEvent, error) {
	event.TenantID = strings.TrimSpace(event.TenantID)
	event.ServiceID = strings.TrimSpace(event.ServiceID)
	event.Authority = strings.TrimSpace(event.Authority)
	event.Source = strings.TrimSpace(event.Source)
	event.ReasonCode = strings.TrimSpace(event.ReasonCode)
	if event.Source == "" {
		event.Source = event.Authority
	}
	if event.ObservedAt.IsZero() {
		event.ObservedAt = now.UTC()
	} else {
		event.ObservedAt = event.ObservedAt.UTC()
	}
	if event.TenantID == "" || event.ServiceID == "" {
		return nil, fmt.Errorf("controlplane: service event tenant and service id required")
	}
	if event.Authority != ServiceEventAuthorityControlPlane && event.Authority != ServiceEventAuthorityGuard {
		return nil, fmt.Errorf("%w: unsupported service event authority %q", ErrConflict, event.Authority)
	}
	if len(event.Source) > 128 || len(event.ReasonCode) > 128 {
		return nil, fmt.Errorf("%w: service event source and reason code are bounded to 128 bytes", ErrConflict)
	}
	patch := event.Runtime
	if trimmed := strings.TrimSpace(patch.TenantID); trimmed != "" && trimmed != event.TenantID {
		return nil, fmt.Errorf("%w: service event tenant does not match runtime patch", ErrConflict)
	}
	if trimmed := strings.TrimSpace(patch.ID); trimmed != "" && trimmed != event.ServiceID {
		return nil, fmt.Errorf("%w: service event id does not match runtime patch", ErrConflict)
	}
	if event.Authority == ServiceEventAuthorityControlPlane &&
		(strings.TrimSpace(patch.ObservedState) != "" || strings.TrimSpace(patch.HealthState) != "") {
		return nil, fmt.Errorf("%w: control plane cannot mutate measured service observation", ErrConflict)
	}
	if event.Authority == ServiceEventAuthorityGuard && serviceregistry.PlacementIntentPresent(patch.Placement) {
		return nil, fmt.Errorf("%w: Guard cannot mutate service placement", ErrConflict)
	}

	if err := validateServiceEventRevision(current, event); err != nil {
		return nil, err
	}
	if current != nil && current.Exists {
		if current.Runtime.TenantID != event.TenantID || current.Runtime.ID != event.ServiceID {
			return nil, fmt.Errorf("%w: service event aggregate identity mismatch", ErrConflict)
		}
		if event.EnforceIdentityBinding {
			if err := validateServiceIdentityBinding(*current, event); err != nil {
				return nil, err
			}
		}
	}

	next := baseServiceHead(current, event, now)
	applyServicePatch(&next, current, event)
	if err := validateServiceDesiredIntentApplies(next, event); err != nil {
		return nil, err
	}
	if err := validateServiceEventHead(next); err != nil {
		return nil, err
	}
	transitions := serviceEventTransitions(current, next, event)
	if len(transitions) == 0 && current != nil && current.Exists && serviceHeadUnchanged(*current, next) {
		return &preparedServiceEvent{head: *current, applied: false}, nil
	}
	next.Revision++
	next.Runtime.UpdatedAt = now.UTC()
	return &preparedServiceEvent{head: next, transitions: transitions, applied: true}, nil
}

func validateServiceEventRevision(current *serviceAggregateHead, event ServiceEvent) error {
	if event.ExpectedRevision == nil {
		return nil
	}
	if current == nil || !current.Exists {
		if *event.ExpectedRevision != 0 {
			return fmt.Errorf("%w: expected service revision %d for creation", ErrConflict, *event.ExpectedRevision)
		}
		return nil
	}
	if *event.ExpectedRevision != current.Revision {
		return fmt.Errorf("%w: service revision is %d, expected %d", ErrConflict, current.Revision, *event.ExpectedRevision)
	}
	return nil
}

// validateServiceIdentityBinding proves the command still targets the exact
// stored identity. It mirrors the tenant-bound upsert guard the Guard
// inventory projection used before the aggregate owned the write.
//
//nolint:gocyclo // The binding guard enumerates every identity column of the row.
func validateServiceIdentityBinding(current serviceAggregateHead, event ServiceEvent) error {
	patch := event.Runtime
	if stack := strings.TrimSpace(patch.StackID); stack != "" && stack != current.Runtime.StackID {
		return fmt.Errorf("%w: service identity belongs to another stack", ErrConflict)
	}
	if node := strings.TrimSpace(event.NodeID); node != "" && current.NodeID != "" && node != current.NodeID {
		return fmt.Errorf("%w: service identity belongs to another node", ErrConflict)
	}
	if server := strings.TrimSpace(patch.ServerID); server != "" && current.Runtime.ServerID != "" &&
		server != current.Runtime.ServerID {
		return fmt.Errorf("%w: service identity belongs to another server", ErrConflict)
	}
	if key := normalizeServiceState(patch.ServiceKey); key != "" && key != normalizeServiceState(current.Runtime.ServiceKey) {
		return fmt.Errorf("%w: service identity belongs to another service key", ErrConflict)
	}
	if instance := normalizeServiceState(patch.ServiceInstance); instance != "" &&
		instance != normalizeServiceState(current.Runtime.ServiceInstance) {
		return fmt.Errorf("%w: service identity belongs to another service instance", ErrConflict)
	}
	if source := normalizeServiceState(patch.Source); source != "" && normalizeServiceState(current.Runtime.Source) != "" &&
		source != normalizeServiceState(current.Runtime.Source) {
		return fmt.Errorf("%w: service identity belongs to another inventory source", ErrConflict)
	}
	if serviceregistry.PlacementIntentPresent(patch.Placement) &&
		!serviceregistry.PlacementEqual(current.Runtime.ServerID, current.Runtime.Placement, patch.Placement) {
		return fmt.Errorf("%w: service identity belongs to another runtime target", ErrConflict)
	}
	return nil
}

func baseServiceHead(current *serviceAggregateHead, event ServiceEvent, now time.Time) serviceAggregateHead {
	if current != nil && current.Exists {
		next := *current
		next.Runtime = *cloneServiceRuntime(current.Runtime)
		next.Runtime.Placement = serviceregistry.NormalizePlacement(next.Runtime.ServerID, next.Runtime.Placement)
		return next
	}
	return serviceAggregateHead{
		Runtime: ServiceRuntime{
			ID: event.ServiceID, TenantID: event.TenantID,
			ServiceInstance: serviceInstanceDefault,
			DesiredState:    string(serviceregistry.DesiredRunning),
			ObservedState:   string(serviceregistry.ObservedUnknown),
			HealthState:     string(serviceregistry.HealthUnknown),
			// Fail-closed: an aggregate that has not stated its ownership yet
			// must not claim we manage it. applyServicePatch resolves the real
			// value from the asserted provenance before the head is validated.
			ManagementState: string(serviceregistry.ManagementObserved),
			Source:          serviceSourceObserved,
			CreatedAt:       now.UTC(),
		},
		Revision: 0,
		Exists:   true,
	}
}

// resolveServiceManagementState is the single ownership rule of the aggregate.
//
//   - An explicit control-plane assertion wins. That is the adoption path: a
//     human decision to take a discovered service under contract.
//   - Otherwise ownership follows the inventory provenance, but only while the
//     aggregate is created or when the provenance actually changes. A stored
//     ownership value is sticky, so the 074 backfill (which also honors the
//     legacy status/type markers the source column cannot express) is not
//     silently re-derived away by the next routine observation.
//
// Guard never asserts ownership: a measured observation proves what is running,
// not who declared it.
func resolveServiceManagementState(next *serviceAggregateHead, current *serviceAggregateHead, event ServiceEvent) {
	if assertion := strings.TrimSpace(event.Runtime.ManagementState); assertion != "" &&
		event.Authority == ServiceEventAuthorityControlPlane {
		next.Runtime.ManagementState = string(serviceregistry.CanonicalManagementState(assertion))
		return
	}
	creating := current == nil || !current.Exists
	stored := ""
	if !creating {
		stored = strings.TrimSpace(current.Runtime.ManagementState)
	}
	// An empty stored value is "not resolved yet" (a row written before
	// migration 074), not a claim of ownership, so it is derived rather than
	// carried forward.
	if creating || stored == "" || !strings.EqualFold(current.Runtime.Source, next.Runtime.Source) {
		next.Runtime.ManagementState = string(serviceregistry.ManagementStateForSource(next.Runtime.Source))
		return
	}
	next.Runtime.ManagementState = string(serviceregistry.CanonicalManagementState(stored))
}

//nolint:gocyclo // The patch enumerates the independent columns of one physical row.
func applyServicePatch(next *serviceAggregateHead, current *serviceAggregateHead, event ServiceEvent) {
	patch := event.Runtime
	creating := current == nil || !current.Exists
	next.Runtime.ID = event.ServiceID
	next.Runtime.TenantID = event.TenantID
	next.Runtime.InstanceID = firstNonEmpty(strings.TrimSpace(patch.InstanceID), next.Runtime.InstanceID)
	next.Runtime.StackID = firstNonEmpty(strings.TrimSpace(patch.StackID), next.Runtime.StackID)
	applyServicePlacement(&next.Runtime, patch, event)
	next.Runtime.ServiceKey = firstNonEmpty(strings.TrimSpace(patch.ServiceKey), next.Runtime.ServiceKey)
	next.Runtime.ServiceInstance = firstNonEmpty(strings.TrimSpace(patch.ServiceInstance), next.Runtime.ServiceInstance, serviceInstanceDefault)
	next.Runtime.Name = firstNonEmpty(strings.TrimSpace(patch.Name), next.Runtime.Name, next.Runtime.ServiceKey, next.Runtime.ID)
	next.Runtime.Source = serviceregistry.CanonicalSource(
		firstNonEmpty(strings.TrimSpace(patch.Source), next.Runtime.Source, serviceSourceObserved))
	next.Runtime.StackKitVersion = firstNonEmpty(strings.TrimSpace(patch.StackKitVersion), next.Runtime.StackKitVersion)
	next.NodeID = firstNonEmpty(strings.TrimSpace(event.NodeID), next.NodeID)

	// Ownership is resolved before intent, because intent only exists for a
	// service we actually declared.
	resolveServiceManagementState(next, current, event)

	// Desired state is user intent. Guard may seed it while the aggregate is
	// created but can never overwrite a stored intent, which is exactly what
	// the previous projection expressed as "desired_state = services.desired_state".
	//
	// An observed service has no declared contract, so there is no intent to
	// carry: a Guard-carried seed is ignored here and an explicit control-plane
	// write is rejected in prepareServiceEvent with a reason code.
	if intent := strings.TrimSpace(patch.DesiredState); intent != "" &&
		(creating || event.Authority == ServiceEventAuthorityControlPlane) &&
		serviceregistry.DesiredStateApplicable(serviceregistry.ManagementState(next.Runtime.ManagementState)) {
		next.Runtime.DesiredState = string(serviceregistry.CanonicalDesiredState(intent))
	}
	if observed := strings.TrimSpace(patch.ObservedState); observed != "" {
		next.Runtime.ObservedState = string(serviceregistry.CanonicalObservedState(observed))
	}
	if health := strings.TrimSpace(patch.HealthState); health != "" {
		next.Runtime.HealthState = string(serviceregistry.CanonicalHealthState(health))
	}
	if patch.ObservedAt != nil {
		next.Runtime.ObservedAt = cloneTime(patch.ObservedAt)
	}
	if patch.Access != nil {
		next.Runtime.Access = cloneMap(patch.Access)
	}
	if patch.Capabilities != nil {
		next.Runtime.Capabilities = append([]string(nil), patch.Capabilities...)
	}

	// The legacy compatibility columns keep their control-plane workflow value
	// whenever one is held: an inventory sample must not turn an active
	// migration green or re-enable its link.
	if retained := serviceregistry.RetainedWorkflowState(next.MigrationStatus, next.Status); retained != "" {
		next.URL = ""
		next.Runtime.Access = guardServiceUnavailableAccess(retained)
		next.Runtime.Metadata = mergeMaps(next.Runtime.Metadata, patch.Metadata)
		return
	}
	// Status describes what the runtime is doing and is derived from the
	// observed dimension. It is never the health value: a running service with
	// a failing probe stays `running` with health `unhealthy`.
	next.Status = serviceregistry.DeriveStatus(serviceregistry.ObservedState(next.Runtime.ObservedState))
	next.URL = strings.TrimSpace(event.URL)
	next.MigrationStatus = strings.TrimSpace(event.MigrationStatus)
	if patch.Metadata != nil {
		next.Runtime.Metadata = cloneMap(patch.Metadata)
	}
}

// applyServicePlacement keeps service placement independent from management
// and measured runtime state. A control-plane move is explicit through
// target_kind; a routine Guard observation can only retain the server binding
// it is reporting from.
func applyServicePlacement(next *ServiceRuntime, patch ServiceRuntime, event ServiceEvent) {
	if next == nil {
		return
	}
	if !serviceregistry.PlacementIntentPresent(patch.Placement) {
		next.ServerID = firstNonEmpty(strings.TrimSpace(patch.ServerID), next.ServerID)
		next.Placement = serviceregistry.NormalizePlacement(next.ServerID, next.Placement)
		return
	}

	placement := serviceregistry.NormalizePlacement(strings.TrimSpace(patch.ServerID), patch.Placement)
	switch placement.TargetKind {
	case serviceregistry.TargetKindServer:
		next.ServerID = firstNonEmpty(strings.TrimSpace(patch.ServerID), next.ServerID)
		placement = serviceregistry.NormalizePlacement(next.ServerID, placement)
	case serviceregistry.TargetKindManagedWorkload:
		next.ServerID = ""
		if placement.ObservedAt == nil {
			observedAt := event.ObservedAt.UTC()
			placement.ObservedAt = &observedAt
		}
	case serviceregistry.TargetKindUnknown:
		next.ServerID = ""
	}
	next.Placement = placement
}

// validateServiceDesiredIntentApplies fails a declared target state closed when
// the service has no declared contract to compare it against. Inventing a
// desired state for a discovered foreign service would make drift detection
// report against fiction, so the write is refused with a reason code instead.
func validateServiceDesiredIntentApplies(head serviceAggregateHead, event ServiceEvent) error {
	if event.Authority != ServiceEventAuthorityControlPlane {
		return nil
	}
	if strings.TrimSpace(event.Runtime.DesiredState) == "" {
		return nil
	}
	if serviceregistry.DesiredStateApplicable(serviceregistry.ManagementState(head.Runtime.ManagementState)) {
		return nil
	}
	return fmt.Errorf(
		"%w: %s: service %q is observed, not managed, so it has no declared desired state",
		ErrConflict, serviceregistry.ReasonDesiredStateNotApplicable, head.Runtime.ID,
	)
}

// validateServiceEventHead rejects any head that would violate the database
// state-machine constraints before a single row is written.
func validateServiceEventHead(head serviceAggregateHead) error {
	if strings.TrimSpace(head.Runtime.StackID) == "" {
		return fmt.Errorf("%w: service stack binding is required", ErrConflict)
	}
	if strings.TrimSpace(head.Runtime.ServiceKey) == "" {
		return fmt.Errorf("%w: service key is required", ErrConflict)
	}
	if err := serviceregistry.ValidateDesiredState(head.Runtime.DesiredState); err != nil {
		return fmt.Errorf("%w: %v", ErrConflict, err)
	}
	if err := serviceregistry.ValidateObservedState(head.Runtime.ObservedState); err != nil {
		return fmt.Errorf("%w: %v", ErrConflict, err)
	}
	if err := serviceregistry.ValidateHealthState(head.Runtime.HealthState); err != nil {
		return fmt.Errorf("%w: %v", ErrConflict, err)
	}
	if err := serviceregistry.ValidateManagementState(head.Runtime.ManagementState); err != nil {
		return fmt.Errorf("%w: %v", ErrConflict, err)
	}
	if err := serviceregistry.ValidateSource(head.Runtime.Source); err != nil {
		return fmt.Errorf("%w: %v", ErrConflict, err)
	}
	if err := serviceregistry.ValidatePlacement(head.Runtime.ServerID, head.Runtime.Placement); err != nil {
		return fmt.Errorf("%w: %v", ErrConflict, err)
	}
	if strings.TrimSpace(head.Status) == "" {
		return fmt.Errorf("%w: service status projection is required", ErrConflict)
	}
	return nil
}

// serviceEventTransitions emits at most one row per dimension and only for a
// dimension whose state actually changed. A no-op observation writes nothing.
func serviceEventTransitions(current *serviceAggregateHead, next serviceAggregateHead, event ServiceEvent) []ServiceStateTransition {
	type dimension struct{ name, from, to string }
	dimensions := []dimension{
		{name: serviceDimensionDesired, to: next.Runtime.DesiredState},
		{name: serviceDimensionObserved, to: next.Runtime.ObservedState},
		{name: serviceDimensionHealth, to: next.Runtime.HealthState},
		{name: serviceDimensionManagement, to: next.Runtime.ManagementState},
	}
	if current != nil && current.Exists {
		dimensions[0].from = current.Runtime.DesiredState
		dimensions[1].from = current.Runtime.ObservedState
		dimensions[2].from = current.Runtime.HealthState
		dimensions[3].from = current.Runtime.ManagementState
	}
	// An observed service has no declared target, so its stored desired_state is
	// not a contract and must never enter the timeline as if it were one.
	if !serviceregistry.DesiredStateApplicable(serviceregistry.ManagementState(next.Runtime.ManagementState)) {
		dimensions[0].to = dimensions[0].from
	}
	evidence := mergeMaps(event.Evidence, map[string]any{
		"service_revision":    next.Revision + 1,
		"source_authority":    event.Authority,
		serviceEvidenceSource: event.Source,
	})
	out := make([]ServiceStateTransition, 0, len(dimensions))
	for _, item := range dimensions {
		if item.to == "" || item.from == item.to {
			continue
		}
		out = append(out, ServiceStateTransition{
			TenantID: next.Runtime.TenantID, ServiceID: next.Runtime.ID,
			Dimension: item.name, FromState: item.from, ToState: item.to,
			ReasonCode: event.ReasonCode, Source: event.Source,
			ObservedAt: event.ObservedAt, Evidence: cloneMap(evidence),
		})
	}
	return out
}

// serviceHeadUnchanged reports whether an accepted command would rewrite the
// stored row with exactly the same durable content. Such a replay keeps its
// revision so idempotent inventory retries do not inflate the CAS fence.
//
//nolint:gocyclo // The replay guard compares every durable column of the row.
func serviceHeadUnchanged(current serviceAggregateHead, next serviceAggregateHead) bool {
	if current.Status != next.Status || current.MigrationStatus != next.MigrationStatus ||
		current.NodeID != next.NodeID || current.URL != next.URL {
		return false
	}
	left, right := current.Runtime, next.Runtime
	if left.InstanceID != right.InstanceID || left.StackID != right.StackID ||
		left.ServerID != right.ServerID || left.ServiceKey != right.ServiceKey ||
		left.ServiceInstance != right.ServiceInstance || left.Name != right.Name ||
		left.Source != right.Source || left.StackKitVersion != right.StackKitVersion ||
		left.DesiredState != right.DesiredState || left.ObservedState != right.ObservedState ||
		left.HealthState != right.HealthState || left.ManagementState != right.ManagementState {
		return false
	}
	if !serviceregistry.PlacementEqual(left.ServerID, left.Placement, right.Placement) {
		return false
	}
	if !equalTimePointers(left.ObservedAt, right.ObservedAt) {
		return false
	}
	if !equalStringSlices(left.Capabilities, right.Capabilities) {
		return false
	}
	return equalJSONObjects(left.Access, right.Access) && equalJSONObjects(left.Metadata, right.Metadata)
}

func normalizeServiceState(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

// canonicalServiceRuntimeStates projects the three service dimensions onto the
// canonical vocabulary the database CHECK constraints enforce.
func canonicalServiceRuntimeStates(service ServiceRuntime) ServiceRuntime {
	service.DesiredState = string(serviceregistry.CanonicalDesiredState(service.DesiredState))
	service.ObservedState = string(serviceregistry.CanonicalObservedState(service.ObservedState))
	service.HealthState = string(serviceregistry.CanonicalHealthState(service.HealthState))
	service.Source = serviceregistry.CanonicalSource(service.Source)
	if strings.TrimSpace(service.ManagementState) == "" {
		service.ManagementState = string(serviceregistry.ManagementStateForSource(service.Source))
	} else {
		service.ManagementState = string(serviceregistry.CanonicalManagementState(service.ManagementState))
	}
	service.ServerID = strings.TrimSpace(service.ServerID)
	service.Placement = serviceregistry.NormalizePlacement(service.ServerID, service.Placement)
	return service
}

// derivedServiceStatus returns the legacy status projection for one observed
// state, letting a retained control-plane workflow state win.
func derivedServiceStatus(legacy Service, observedState string) string {
	if retained := serviceregistry.RetainedWorkflowState(legacy.MigrationStatus, legacy.Status); retained != "" {
		return legacy.Status
	}
	return serviceregistry.DeriveStatus(serviceregistry.ObservedState(observedState))
}

func equalTimePointers(left, right *time.Time) bool {
	if left == nil || right == nil {
		return left == right
	}
	return left.UTC().Equal(right.UTC())
}

func equalStringSlices(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

// equalJSONObjects compares two observation payloads by their canonical JSON
// encoding. A decoded database row and a freshly built patch use different Go
// number types for the same value, so a structural comparison would report a
// difference that the database never sees.
func equalJSONObjects(left, right map[string]any) bool {
	leftJSON, leftErr := marshalObject(left)
	rightJSON, rightErr := marshalObject(right)
	if leftErr != nil || rightErr != nil {
		return false
	}
	return string(leftJSON) == string(rightJSON)
}
