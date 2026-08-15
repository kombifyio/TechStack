package controlplane

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/kombifyio/techstack/pkg/serviceregistry"
)

const (
	guardProjectionEnvelopeKey = "_guard_projection"
	guardProjectionSchema      = "guard-inventory-projection/v1"

	guardServiceStateArchived            = "archived"
	guardServiceStateDeploying           = "deploying"
	guardServiceStateMigrating           = "migrating"
	guardServiceStatePendingVerification = "pending_verification"
)

// GuardInventoryServiceProjection binds the legacy compatibility row and the
// canonical measured runtime row produced from one Guard service observation.
// The two projections are committed together and must describe one identity.
type GuardInventoryServiceProjection struct {
	Legacy  Service
	Runtime ServiceRuntime
}

// GuardInventoryProjection is one authenticated Guard inventory observation
// and every registry projection derived from it. Implementations must commit
// the aggregate, snapshot, node, service rows, and authoritative prune as one
// atomic write.
type GuardInventoryProjection struct {
	Event            ServerEvent
	Node             Node
	Services         []GuardInventoryServiceProjection
	ManifestObserved bool
	ServiceSource    string
}

// GuardInventoryProjectionResult distinguishes an exact idempotent replay
// from a stale observation that was fenced without changing durable state.
type GuardInventoryProjectionResult struct {
	ServerEvent *ServerEventResult
	Replayed    bool
}

// GuardInventoryProjectionStore is the atomic persistence seam for a complete
// Guard inventory observation.
type GuardInventoryProjectionStore interface {
	ApplyGuardInventoryProjection(context.Context, GuardInventoryProjection) (*GuardInventoryProjectionResult, error)
}

type preparedGuardInventoryProjection struct {
	command GuardInventoryProjection
	digest  string
}

// prepareGuardInventoryProjection validates and canonicalizes the complete
// command before any adapter is allowed to mutate durable state.
func prepareGuardInventoryProjection(command GuardInventoryProjection) (*preparedGuardInventoryProjection, error) {
	normalized, err := normalizeGuardInventoryProjection(command)
	if err != nil {
		return nil, err
	}
	digest, err := guardInventoryProjectionDigest(normalized)
	if err != nil {
		return nil, err
	}
	deployment := normalized.Event.Inventory.Inventory["deployment"].(map[string]any)
	deployment[guardProjectionEnvelopeKey] = guardInventoryProjectionEnvelope(normalized, digest)
	normalized.Event.Inventory.Inventory["deployment"] = deployment
	return &preparedGuardInventoryProjection{command: normalized, digest: digest}, nil
}

//nolint:gocyclo // The projection admission intentionally keeps every cross-row binding in one place.
func normalizeGuardInventoryProjection(command GuardInventoryProjection) (GuardInventoryProjection, error) {
	event := command.Event
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
		return GuardInventoryProjection{}, fmt.Errorf("%w: Guard inventory observed_at is required", ErrConflict)
	}
	event.ObservedAt = event.ObservedAt.UTC()
	if event.Authority != ServerEventAuthorityGuard {
		return GuardInventoryProjection{}, fmt.Errorf("%w: Guard inventory requires Guard authority", ErrConflict)
	}
	if event.Runtime.LifecycleState != "" || event.Runtime.DesiredState != "" || event.Runtime.DecommissionedAt != nil ||
		event.Runtime.LifecycleReasonCode != "" || event.Runtime.DesiredReasonCode != "" ||
		event.ClearLifecycleReason || event.ClearDesiredReason {
		return GuardInventoryProjection{}, fmt.Errorf("%w: Guard inventory cannot carry lifecycle or desired-state mutations", ErrConflict)
	}
	if event.TenantID == "" || event.ServerID == "" || event.SourceID == "" || event.SourceEpoch == "" || event.SourceSequence <= 0 || event.Generation <= 0 {
		return GuardInventoryProjection{}, fmt.Errorf("%w: Guard inventory identity and source position are required", ErrConflict)
	}
	if event.Inventory == nil {
		return GuardInventoryProjection{}, fmt.Errorf("%w: Guard inventory snapshot is required", ErrConflict)
	}
	event.Inventory = &ServerInventoryEvent{
		Source:    strings.TrimSpace(event.Inventory.Source),
		Inventory: cloneMap(event.Inventory.Inventory),
	}
	if event.Inventory.Source == "" {
		event.Inventory.Source = event.Source
	}
	var err error
	event.Evidence, err = canonicalGuardJSONMap(event.Evidence, "guard_projection.event.evidence")
	if err != nil {
		return GuardInventoryProjection{}, err
	}
	event.Inventory.Inventory, err = canonicalGuardJSONMap(event.Inventory.Inventory, "guard_projection.event.inventory")
	if err != nil {
		return GuardInventoryProjection{}, err
	}
	if rawServices, present := event.Inventory.Inventory["services"]; present {
		observed, ok := rawServices.([]any)
		if !ok {
			return GuardInventoryProjection{}, fmt.Errorf("%w: Guard inventory services must be an array", ErrConflict)
		}
		sort.SliceStable(observed, func(i, j int) bool {
			left, _ := json.Marshal(observed[i])
			right, _ := json.Marshal(observed[j])
			return bytes.Compare(left, right) < 0
		})
		event.Inventory.Inventory["services"] = observed
	} else {
		event.Inventory.Inventory["services"] = []any{}
	}
	deployment, deploymentErr := guardProjectionDeployment(event.Inventory.Inventory)
	if deploymentErr != nil {
		return GuardInventoryProjection{}, deploymentErr
	}
	if _, supplied := deployment[guardProjectionEnvelopeKey]; supplied {
		return GuardInventoryProjection{}, fmt.Errorf("%w: Guard projection envelope is repository-owned", ErrConflict)
	}
	event.Inventory.Inventory["deployment"] = deployment
	event.Runtime = normalizeGuardProjectionRuntime(event.Runtime)
	event.Runtime.Metadata, err = canonicalGuardJSONMap(event.Runtime.Metadata, "guard_projection.event.runtime.metadata")
	if err != nil {
		return GuardInventoryProjection{}, err
	}
	for index := range event.Runtime.Channels {
		event.Runtime.Channels[index].Metadata, err = canonicalGuardJSONMap(event.Runtime.Channels[index].Metadata, fmt.Sprintf("guard_projection.event.runtime.channels[%d].metadata", index))
		if err != nil {
			return GuardInventoryProjection{}, err
		}
	}

	node := normalizeGuardProjectionNode(command.Node)
	node.Metadata, err = canonicalGuardJSONMap(node.Metadata, "guard_projection.node.metadata")
	if err != nil {
		return GuardInventoryProjection{}, err
	}
	serviceSource := strings.ToLower(strings.TrimSpace(command.ServiceSource))
	if serviceSource == "" {
		return GuardInventoryProjection{}, fmt.Errorf("%w: Guard inventory service source is required", ErrConflict)
	}

	if event.Runtime.ID != event.ServerID || event.Runtime.TenantID != event.TenantID ||
		event.Runtime.StackID == "" || event.Runtime.WorkerID != event.SourceID ||
		(event.Runtime.NodeID != "" && event.Runtime.NodeID != event.ServerID) {
		return GuardInventoryProjection{}, fmt.Errorf("%w: Guard event runtime bindings do not match the authenticated source", ErrConflict)
	}
	if node.ID != event.ServerID || node.TenantID != event.TenantID || node.StackID != event.Runtime.StackID ||
		node.WorkerID != event.SourceID || node.InstanceID != event.Runtime.InstanceID {
		return GuardInventoryProjection{}, fmt.Errorf("%w: Guard node bindings do not match the server runtime", ErrConflict)
	}

	services := make([]GuardInventoryServiceProjection, 0, len(command.Services))
	serviceIDs := make(map[string]struct{}, len(command.Services))
	serviceIdentities := make(map[string]struct{}, len(command.Services))
	for index, candidate := range command.Services {
		projection, projectionErr := normalizeGuardInventoryServiceProjection(candidate, event, node, serviceSource, index)
		if projectionErr != nil {
			return GuardInventoryProjection{}, projectionErr
		}
		if _, duplicate := serviceIDs[projection.Legacy.ID]; duplicate {
			return GuardInventoryProjection{}, fmt.Errorf("%w: duplicate Guard inventory service id %q", ErrConflict, projection.Legacy.ID)
		}
		identity := serviceRuntimeIdentity(projection.Runtime.StackID, projection.Runtime.ServerID, projection.Runtime.ServiceKey, projection.Runtime.ServiceInstance)
		if _, duplicate := serviceIdentities[identity]; duplicate {
			return GuardInventoryProjection{}, fmt.Errorf("%w: duplicate Guard inventory service identity", ErrConflict)
		}
		serviceIDs[projection.Legacy.ID] = struct{}{}
		serviceIdentities[identity] = struct{}{}
		services = append(services, projection)
	}
	sort.Slice(services, func(i, j int) bool {
		if services[i].Legacy.ID != services[j].Legacy.ID {
			return services[i].Legacy.ID < services[j].Legacy.ID
		}
		return serviceRuntimeIdentity(services[i].Runtime.StackID, services[i].Runtime.ServerID, services[i].Runtime.ServiceKey, services[i].Runtime.ServiceInstance) <
			serviceRuntimeIdentity(services[j].Runtime.StackID, services[j].Runtime.ServerID, services[j].Runtime.ServiceKey, services[j].Runtime.ServiceInstance)
	})
	if err := validateSecretFreeObservation(struct {
		Node     Node                              `json:"node"`
		Services []GuardInventoryServiceProjection `json:"services"`
	}{Node: node, Services: services}, "guard_projection"); err != nil {
		return GuardInventoryProjection{}, err
	}

	if rawServices, present := event.Inventory.Inventory["services"]; present {
		observed, ok := rawServices.([]any)
		if !ok || len(observed) != len(services) {
			return GuardInventoryProjection{}, fmt.Errorf("%w: Guard inventory snapshot and service projections differ", ErrConflict)
		}
	} else if len(services) != 0 {
		return GuardInventoryProjection{}, fmt.Errorf("%w: Guard inventory snapshot omits projected services", ErrConflict)
	}

	event.Runtime.Metadata["service_projection_expected"] = int64(len(services))
	node.Metadata["manifest_observed"] = command.ManifestObserved
	delete(node.Metadata, "inventory_revision")

	return GuardInventoryProjection{
		Event: event, Node: node, Services: services,
		ManifestObserved: command.ManifestObserved, ServiceSource: serviceSource,
	}, nil
}

func normalizeGuardInventoryServiceProjection(
	candidate GuardInventoryServiceProjection,
	event ServerEvent,
	node Node,
	serviceSource string,
	index int,
) (GuardInventoryServiceProjection, error) {
	legacy := candidate.Legacy
	runtime := candidate.Runtime
	legacy.ID = strings.TrimSpace(legacy.ID)
	legacy.TenantID = strings.TrimSpace(legacy.TenantID)
	legacy.InstanceID = strings.TrimSpace(legacy.InstanceID)
	legacy.StackID = strings.TrimSpace(legacy.StackID)
	legacy.NodeID = strings.TrimSpace(legacy.NodeID)
	legacy.ServiceKey = strings.ToLower(strings.TrimSpace(legacy.ServiceKey))
	legacy.Name = strings.TrimSpace(legacy.Name)
	legacy.Status = strings.TrimSpace(legacy.Status)
	legacy.Source = strings.ToLower(strings.TrimSpace(legacy.Source))
	legacy.URL = strings.TrimSpace(legacy.URL)
	legacy.MigrationStatus = strings.ToLower(strings.TrimSpace(legacy.MigrationStatus))
	legacy.CreatedAt = time.Time{}
	legacy.UpdatedAt = time.Time{}

	runtime.ID = strings.TrimSpace(runtime.ID)
	runtime.TenantID = strings.TrimSpace(runtime.TenantID)
	runtime.InstanceID = strings.TrimSpace(runtime.InstanceID)
	runtime.StackID = strings.TrimSpace(runtime.StackID)
	runtime.ServerID = strings.TrimSpace(runtime.ServerID)
	runtime.ServiceKey = strings.ToLower(strings.TrimSpace(runtime.ServiceKey))
	runtime.ServiceInstance = strings.ToLower(firstNonEmpty(strings.TrimSpace(runtime.ServiceInstance), "default"))
	runtime.Name = strings.TrimSpace(runtime.Name)
	runtime.DesiredState = strings.TrimSpace(runtime.DesiredState)
	runtime.ObservedState = strings.TrimSpace(runtime.ObservedState)
	runtime.HealthState = strings.TrimSpace(runtime.HealthState)
	runtime.StackKitVersion = strings.TrimSpace(runtime.StackKitVersion)
	runtime.Source = strings.ToLower(strings.TrimSpace(runtime.Source))
	runtime.CreatedAt = time.Time{}
	runtime.UpdatedAt = time.Time{}
	runtime.Capabilities = canonicalGuardStrings(runtime.Capabilities)

	var err error
	legacy.Metadata, err = canonicalGuardJSONMap(legacy.Metadata, fmt.Sprintf("guard_projection.services[%d].legacy.metadata", index))
	if err != nil {
		return GuardInventoryServiceProjection{}, err
	}
	runtime.Access, err = canonicalGuardJSONMap(runtime.Access, fmt.Sprintf("guard_projection.services[%d].runtime.access", index))
	if err != nil {
		return GuardInventoryServiceProjection{}, err
	}
	runtime.Metadata, err = canonicalGuardJSONMap(runtime.Metadata, fmt.Sprintf("guard_projection.services[%d].runtime.metadata", index))
	if err != nil {
		return GuardInventoryServiceProjection{}, err
	}
	delete(legacy.Metadata, "inventory_revision")
	delete(runtime.Metadata, "inventory_revision")

	if legacy.ID == "" || runtime.ID == "" || legacy.ID != runtime.ID ||
		legacy.TenantID != event.TenantID || runtime.TenantID != event.TenantID ||
		legacy.StackID != event.Runtime.StackID || runtime.StackID != event.Runtime.StackID ||
		legacy.NodeID != node.ID || runtime.ServerID != event.ServerID ||
		legacy.InstanceID != event.Runtime.InstanceID || runtime.InstanceID != event.Runtime.InstanceID ||
		legacy.ServiceKey == "" || runtime.ServiceKey == "" || legacy.ServiceKey != runtime.ServiceKey {
		return GuardInventoryServiceProjection{}, fmt.Errorf("%w: Guard service projection %d has inconsistent identity bindings", ErrConflict, index)
	}
	// One Guard observation carries both halves of the service model: StackKit
	// services that have a declared contract and services merely discovered on
	// the host. The batch source is therefore the DEFAULT provenance, not a
	// uniform one. A row that declares its own provenance must stay inside the
	// closed vocabulary and both projections of the same physical row must
	// agree on it.
	declaredSource, sourceErr := guardInventoryServiceSource(legacy, runtime, serviceSource)
	if sourceErr != nil {
		return GuardInventoryServiceProjection{}, sourceErr
	}
	if legacy.MigrationStatus != "" || guardServiceRetainedControlState(legacy) != "" {
		return GuardInventoryServiceProjection{}, fmt.Errorf("%w: Guard service projection %q cannot declare control-plane lifecycle state", ErrConflict, legacy.ID)
	}
	legacy.Source = declaredSource
	runtime.Source = declaredSource
	if runtime.ObservedAt == nil || !runtime.ObservedAt.Equal(event.ObservedAt) {
		return GuardInventoryServiceProjection{}, fmt.Errorf("%w: Guard service projection %q observed_at does not match its event", ErrConflict, legacy.ID)
	}
	runtime.ObservedAt = cloneTime(&event.ObservedAt)
	return GuardInventoryServiceProjection{Legacy: legacy, Runtime: runtime}, nil
}

// guardInventoryServiceSource resolves the provenance of one projected service.
// Both projections of a physical row must state the same provenance, an
// unstated one inherits the batch default, and anything outside the closed
// vocabulary is refused rather than folded: Guard is an authenticated source,
// so a provenance it cannot name is a contract violation, not a value to guess.
func guardInventoryServiceSource(legacy Service, runtime ServiceRuntime, batchSource string) (string, error) {
	declared := ""
	for _, candidate := range []string{legacy.Source, runtime.Source} {
		if candidate == "" {
			continue
		}
		if declared != "" && declared != candidate {
			return "", fmt.Errorf("%w: Guard service projection %q has inconsistent source", ErrConflict, legacy.ID)
		}
		declared = candidate
	}
	if declared == "" {
		return batchSource, nil
	}
	if err := serviceregistry.ValidateSource(declared); err != nil {
		return "", fmt.Errorf("%w: Guard service projection %q: %v", ErrConflict, legacy.ID, err)
	}
	return declared, nil
}

func normalizeGuardProjectionRuntime(runtime ServerRuntime) ServerRuntime {
	runtime.ID = strings.TrimSpace(runtime.ID)
	runtime.TenantID = strings.TrimSpace(runtime.TenantID)
	runtime.InstanceID = strings.TrimSpace(runtime.InstanceID)
	runtime.StackID = strings.TrimSpace(runtime.StackID)
	runtime.OwnerSubjectID = strings.TrimSpace(runtime.OwnerSubjectID)
	runtime.WorkerID = strings.TrimSpace(runtime.WorkerID)
	runtime.NodeID = strings.TrimSpace(runtime.NodeID)
	runtime.LeaseID = strings.TrimSpace(runtime.LeaseID)
	runtime.ProviderRef = strings.TrimSpace(runtime.ProviderRef)
	runtime.Name = strings.TrimSpace(runtime.Name)
	runtime.CreatedAt = time.Time{}
	runtime.UpdatedAt = time.Time{}
	runtime.LastHeartbeatAt = cloneTime(runtime.LastHeartbeatAt)
	runtime.SourceObservedAt = cloneTime(runtime.SourceObservedAt)
	runtime.DecommissionedAt = cloneTime(runtime.DecommissionedAt)
	runtime.Channels = cloneServerChannels(runtime.Channels)
	return runtime
}

func normalizeGuardProjectionNode(node Node) Node {
	node.ID = strings.TrimSpace(node.ID)
	node.TenantID = strings.TrimSpace(node.TenantID)
	node.InstanceID = strings.TrimSpace(node.InstanceID)
	node.StackID = strings.TrimSpace(node.StackID)
	node.WorkerID = strings.TrimSpace(node.WorkerID)
	node.Name = strings.TrimSpace(node.Name)
	node.Role = strings.TrimSpace(node.Role)
	node.Status = strings.TrimSpace(node.Status)
	node.Address = strings.TrimSpace(node.Address)
	node.CreatedAt = time.Time{}
	node.UpdatedAt = time.Time{}
	return node
}

func canonicalGuardStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, duplicate := seen[value]; duplicate {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func canonicalGuardJSONMap(value map[string]any, path string) (map[string]any, error) {
	if value == nil {
		return map[string]any{}, nil
	}
	if err := validateSecretFreeObservation(value, path); err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("%w: %s is not canonical JSON: %v", ErrConflict, path, err)
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	var decoded map[string]any
	if err := decoder.Decode(&decoded); err != nil {
		return nil, fmt.Errorf("%w: %s is not canonical JSON: %v", ErrConflict, path, err)
	}
	return normalizeGuardJSONNumbers(decoded).(map[string]any), nil
}

func normalizeGuardJSONNumbers(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, nested := range typed {
			out[key] = normalizeGuardJSONNumbers(nested)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for index, nested := range typed {
			out[index] = normalizeGuardJSONNumbers(nested)
		}
		return out
	case json.Number:
		text := typed.String()
		if !strings.ContainsAny(text, ".eE") {
			if integer, err := strconv.ParseInt(text, 10, 64); err == nil {
				return integer
			}
		}
		if decimal, err := strconv.ParseFloat(text, 64); err == nil {
			return decimal
		}
		return text
	default:
		return typed
	}
}

func guardInventoryProjectionDigest(command GuardInventoryProjection) (string, error) {
	canonical := command
	canonical.Event.ExpectedRevision = 0
	canonical.Event.Runtime.CreatedAt = time.Time{}
	canonical.Event.Runtime.UpdatedAt = time.Time{}
	canonical.Node.CreatedAt = time.Time{}
	canonical.Node.UpdatedAt = time.Time{}
	if canonical.Event.Inventory != nil {
		canonical.Event.Inventory = &ServerInventoryEvent{
			Source: strings.TrimSpace(canonical.Event.Inventory.Source), Inventory: cloneMap(canonical.Event.Inventory.Inventory),
		}
		if deployment, ok := canonical.Event.Inventory.Inventory["deployment"].(map[string]any); ok {
			deployment = cloneGuardCanonicalMap(deployment)
			delete(deployment, guardProjectionEnvelopeKey)
			canonical.Event.Inventory.Inventory["deployment"] = deployment
		}
	}
	for index := range canonical.Services {
		canonical.Services[index].Legacy.CreatedAt = time.Time{}
		canonical.Services[index].Legacy.UpdatedAt = time.Time{}
		canonical.Services[index].Runtime.CreatedAt = time.Time{}
		canonical.Services[index].Runtime.UpdatedAt = time.Time{}
	}
	encoded, err := json.Marshal(struct {
		Schema  string                   `json:"schema"`
		Command GuardInventoryProjection `json:"command"`
	}{Schema: guardProjectionSchema, Command: canonical})
	if err != nil {
		return "", fmt.Errorf("%w: Guard inventory projection cannot be hashed: %v", ErrConflict, err)
	}
	digest := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

func guardInventoryProjectionEnvelope(command GuardInventoryProjection, digest string) map[string]any {
	return map[string]any{
		"schema":            guardProjectionSchema,
		"digest":            digest,
		"source_epoch":      command.Event.SourceEpoch,
		"source_sequence":   command.Event.SourceSequence,
		"generation":        command.Event.Generation,
		"manifest_observed": command.ManifestObserved,
		"service_source":    command.ServiceSource,
	}
}

func guardInventoryProjectionDigestFromInventory(inventory map[string]any) (string, bool) {
	deployment, ok := inventory["deployment"].(map[string]any)
	if !ok {
		return "", false
	}
	envelope, ok := deployment[guardProjectionEnvelopeKey].(map[string]any)
	if !ok || strings.TrimSpace(stringFromGuardJSON(envelope["schema"])) != guardProjectionSchema {
		return "", false
	}
	digest := strings.TrimSpace(stringFromGuardJSON(envelope["digest"]))
	if !strings.HasPrefix(digest, "sha256:") || len(digest) != len("sha256:")+sha256.Size*2 {
		return "", false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(digest, "sha256:"))
	return digest, err == nil
}

func guardProjectionDeployment(inventory map[string]any) (map[string]any, error) {
	value, present := inventory["deployment"]
	if !present || value == nil {
		return map[string]any{}, nil
	}
	deployment, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%w: Guard inventory deployment must be an object", ErrConflict)
	}
	return deployment, nil
}

// guardInventoryProjectionAtRevision clones a prepared command and stamps the
// exact accepted inventory revision onto every reader-visible projection.
// Adapters call it only after the aggregate preparation has assigned the next
// revision, but before committing any child row.
func guardInventoryProjectionAtRevision(prepared *preparedGuardInventoryProjection, revision int64) GuardInventoryProjection {
	command := clonePreparedGuardInventoryProjection(prepared.command)
	command.Node.Metadata["inventory_revision"] = revision
	for index := range command.Services {
		command.Services[index].Legacy.Metadata["inventory_revision"] = revision
		command.Services[index].Runtime.Metadata["inventory_revision"] = revision
	}
	return command
}

func clonePreparedGuardInventoryProjection(command GuardInventoryProjection) GuardInventoryProjection {
	cloned := command
	cloned.Event.Evidence = cloneGuardCanonicalMap(command.Event.Evidence)
	cloned.Event.Runtime = *cloneServerRuntime(command.Event.Runtime)
	cloned.Event.Runtime.Metadata = cloneGuardCanonicalMap(command.Event.Runtime.Metadata)
	for index := range cloned.Event.Runtime.Channels {
		cloned.Event.Runtime.Channels[index].Metadata = cloneGuardCanonicalMap(command.Event.Runtime.Channels[index].Metadata)
	}
	if command.Event.Inventory != nil {
		cloned.Event.Inventory = &ServerInventoryEvent{
			Source:    command.Event.Inventory.Source,
			Inventory: cloneGuardCanonicalMap(command.Event.Inventory.Inventory),
		}
	}
	cloned.Node = *cloneNode(command.Node)
	cloned.Node.Metadata = cloneGuardCanonicalMap(command.Node.Metadata)
	cloned.Services = make([]GuardInventoryServiceProjection, len(command.Services))
	for index, service := range command.Services {
		cloned.Services[index] = GuardInventoryServiceProjection{
			Legacy:  *cloneService(service.Legacy),
			Runtime: *cloneServiceRuntime(service.Runtime),
		}
		cloned.Services[index].Legacy.Metadata = cloneGuardCanonicalMap(service.Legacy.Metadata)
		cloned.Services[index].Runtime.Access = cloneGuardCanonicalMap(service.Runtime.Access)
		cloned.Services[index].Runtime.Metadata = cloneGuardCanonicalMap(service.Runtime.Metadata)
	}
	return cloned
}

func cloneGuardCanonicalMap(value map[string]any) map[string]any {
	if value == nil {
		return map[string]any{}
	}
	return cloneGuardCanonicalValue(value).(map[string]any)
}

func cloneGuardCanonicalValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, nested := range typed {
			out[key] = cloneGuardCanonicalValue(nested)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for index, nested := range typed {
			out[index] = cloneGuardCanonicalValue(nested)
		}
		return out
	default:
		return typed
	}
}

func stringFromGuardJSON(value any) string {
	text, _ := value.(string)
	return text
}

func guardInventoryProjectionAtCurrentPosition(current ServerRuntime, event ServerEvent) bool {
	return current.SourceAuthority == ServerEventAuthorityGuard && current.Generation == event.Generation &&
		current.SourceID == event.SourceID && current.SourceEpoch == event.SourceEpoch && current.SourceSequence == event.SourceSequence
}

// validateGuardInventoryCanonicalServer proves that the authenticated
// projection still targets the exact control-plane binding held under the
// aggregate lock. Guard cannot fill missing authority fields or adopt a
// partially bound legacy server.
func validateGuardInventoryCanonicalServer(current ServerRuntime, command GuardInventoryProjection) error {
	event, binding := command.Event, command.Event.Runtime
	if current.TenantID != event.TenantID || current.ID != event.ServerID ||
		current.Generation != event.Generation || current.WorkerID == "" ||
		current.WorkerID != event.SourceID || current.WorkerID != binding.WorkerID {
		return fmt.Errorf("%w: Guard inventory does not match the canonical server authority", ErrConflict)
	}
	checks := []struct {
		name         string
		current      string
		incoming     string
		requireBound bool
	}{
		{name: "instance", current: current.InstanceID, incoming: binding.InstanceID},
		{name: "stack", current: current.StackID, incoming: binding.StackID, requireBound: true},
		{name: "owner", current: current.OwnerSubjectID, incoming: binding.OwnerSubjectID, requireBound: true},
		{name: "lease", current: current.LeaseID, incoming: binding.LeaseID},
		{name: "node", current: current.NodeID, incoming: binding.NodeID, requireBound: true},
		{name: "provider", current: current.ProviderRef, incoming: binding.ProviderRef},
	}
	for _, check := range checks {
		if check.requireBound {
			if check.current == "" || check.current != check.incoming {
				return fmt.Errorf("%w: Guard inventory canonical server %s binding is missing or different", ErrConflict, check.name)
			}
			continue
		}
		// Guard observes the host, not control-plane placement. It never learns
		// the provider ref, the instance id, or a lease it was not handed, so an
		// empty incoming value means "not asserted" and must not be read as a
		// request to clear or change the canonical binding. Asserting a
		// different non-empty value stays a conflict. This mirrors the generic
		// Guard admission path in validateGuardBindings; without it every
		// provider-bound server rejects Guard inventory forever.
		if check.incoming != "" && check.incoming != check.current {
			return fmt.Errorf("%w: Guard inventory canonical server %s binding is missing or different", ErrConflict, check.name)
		}
	}
	return nil
}

func guardServiceRetainedControlState(service Service) string {
	for _, state := range []string{service.MigrationStatus, service.Status} {
		switch strings.ToLower(strings.TrimSpace(state)) {
		case guardServiceStateMigrating, guardServiceStateDeploying,
			guardServiceStatePendingVerification, guardServiceStateArchived:
			return strings.ToLower(strings.TrimSpace(state))
		}
	}
	return ""
}

func guardServiceUnavailableAccess(state string) map[string]any {
	reason := "migration_in_progress"
	if state == guardServiceStateArchived {
		reason = "service_archived"
	}
	return map[string]any{"mode": "unavailable", "reason_code": reason}
}

// guardInventoryServerObservationEvent removes authenticated placement
// bindings after the complete projection has been validated and digested. The
// generic aggregate command therefore receives only fields owned by Guard and
// can neither create nor update control-plane identity bindings.
func guardInventoryServerObservationEvent(event ServerEvent) ServerEvent {
	observation := event
	observation.Runtime.InstanceID = ""
	observation.Runtime.StackID = ""
	observation.Runtime.OwnerSubjectID = ""
	observation.Runtime.WorkerID = ""
	observation.Runtime.NodeID = ""
	observation.Runtime.LeaseID = ""
	observation.Runtime.ProviderRef = ""
	observation.Runtime.Name = ""
	observation.Runtime.LifecycleState = ""
	observation.Runtime.DesiredState = ""
	observation.Runtime.LifecycleReasonCode = ""
	observation.Runtime.DesiredReasonCode = ""
	observation.Runtime.DecommissionedAt = nil
	observation.ClearLifecycleReason = false
	observation.ClearDesiredReason = false
	return observation
}

func guardInventoryServerObservationEventWithExpectedServices(event ServerEvent, expected int64) ServerEvent {
	observation := guardInventoryServerObservationEvent(event)
	observation.Runtime.Metadata = cloneGuardCanonicalMap(observation.Runtime.Metadata)
	observation.Runtime.Metadata["service_projection_expected"] = expected
	return observation
}
