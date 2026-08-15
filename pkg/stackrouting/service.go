package stackrouting

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/kombifyio/techstack/internal/providercatalog"
	"github.com/kombifyio/techstack/internal/runtimeproduct/vmlease"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/monthlyruntime"
	"github.com/kombifyio/techstack/pkg/runtimeidentity"
	"github.com/kombifyio/techstack/pkg/serverregistry"
)

var dnsLabelPattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)

const cloudflareDNSProvider = "cloudflare"

type Service struct {
	Stacks     controlplane.StackStore
	Servers    controlplane.ServerRuntimeStore
	Store      Store
	Leases     ManagedLeaseLister
	Dispatcher RolloutDispatcher
}

func (s Service) Get(ctx context.Context, principal Principal, stackID string) (*View, error) {
	stack, err := s.requireOwnedStack(ctx, principal, stackID)
	if err != nil {
		return nil, err
	}
	state, err := s.requireStore().Get(ctx, principal.TenantID, stack.ID)
	if err != nil {
		return nil, err
	}
	if state.OwnerSubjectID != principal.OwnerSubjectID {
		return nil, ErrForbidden
	}
	return viewFor(state, false), nil
}

func (s Service) Ensure(ctx context.Context, principal Principal, stackID string, input EnsureInput, opts MutationOptions) (*View, error) {
	stack, err := s.requireOwnedStack(ctx, principal, stackID)
	if err != nil {
		return nil, err
	}
	input, err = normalizeEnsureInput(input)
	if err != nil {
		return nil, err
	}
	key, err := normalizeIdempotencyKey(opts.IdempotencyKey)
	if err != nil {
		return nil, err
	}
	if _, err = s.resolveRoutingServer(ctx, principal, stack, input); err != nil {
		return nil, err
	}
	result, err := s.persistDesiredRouting(ctx, principal, stack.ID, input, key, opts.ExpectedRevision)
	if err != nil {
		return nil, err
	}
	// A durable idempotency receipt is the complete result of the original
	// mutation. Pending and terminal replays are observation-only. A receipt
	// still at not_requested may recover a lost dispatch response through the
	// dispatcher's own durable key; retrying a failed rollout requires a new key.
	if result.Replay && !recoverableUndispatchedReplay(result.State, input.EnsureRollout) {
		return viewFor(result.State, true), nil
	}
	if !input.EnsureRollout || rolloutAlreadyAccepted(result.State) {
		return viewFor(result.State, result.Replay), nil
	}
	result.State, err = s.dispatchRoutingRollout(ctx, principal, stack.ID, input, key, result.State)
	if err != nil {
		return nil, err
	}
	return viewFor(result.State, result.Replay), nil
}

func normalizeIdempotencyKey(value string) (string, error) {
	key := strings.TrimSpace(value)
	if key == "" || len(key) > 256 {
		return "", fmt.Errorf("%w: Idempotency-Key is required and must not exceed 256 characters", ErrInvalid)
	}
	return key, nil
}

func (s Service) resolveRoutingServer(ctx context.Context, principal Principal, stack *controlplane.Stack, input EnsureInput) (*controlplane.ServerRuntime, error) {
	if s.Servers == nil {
		return nil, fmt.Errorf("%w: server runtime store is not configured", ErrUnavailable)
	}
	server, err := s.Servers.GetServerRuntime(ctx, principal.TenantID, input.ServerID)
	if err == nil {
		return s.validateRoutingServer(ctx, principal, stack, input, server, false)
	}
	if !errors.Is(err, controlplane.ErrNotFound) {
		return nil, fmt.Errorf("%w: load target server: %v", ErrUnavailable, err)
	}
	if input.LeaseID == "" {
		return nil, ErrNotFound
	}
	lease, err := s.requireManagedLease(ctx, principal, stack.ID, input.LeaseID)
	if err != nil {
		return nil, err
	}
	server, err = s.ensureLeaseServerProjection(ctx, principal, stack, input, lease)
	if err != nil {
		return nil, err
	}
	return s.validateRoutingServer(ctx, principal, stack, input, server, true)
}

func (s Service) validateRoutingServer(ctx context.Context, principal Principal, stack *controlplane.Stack, input EnsureInput, server *controlplane.ServerRuntime, managedLeaseValidated bool) (*controlplane.ServerRuntime, error) {
	if server == nil {
		return nil, fmt.Errorf("%w: target server was not returned", ErrUnavailable)
	}
	if server.OwnerSubjectID != principal.OwnerSubjectID {
		return nil, ErrForbidden
	}
	if server.StackID != stack.ID {
		return nil, fmt.Errorf("%w: server and lease must belong to the exact stack target", ErrInvalid)
	}
	if input.RequireLease || managedServerRequiresLease(*server) {
		if err := s.validateManagedRoutingTarget(ctx, principal, stack.ID, input, server, managedLeaseValidated); err != nil {
			return nil, err
		}
		return server, nil
	}
	if input.LeaseID != strings.TrimSpace(server.LeaseID) {
		return nil, fmt.Errorf("%w: lease_id must match the exact server target", ErrInvalid)
	}
	return server, nil
}

func (s Service) validateManagedRoutingTarget(ctx context.Context, principal Principal, stackID string, input EnsureInput, server *controlplane.ServerRuntime, leaseValidated bool) error {
	serverLeaseID := strings.TrimSpace(server.LeaseID)
	if serverLeaseID == "" || input.LeaseID == "" || serverLeaseID != input.LeaseID {
		return fmt.Errorf("%w: managed routing requires the exact server lease", ErrInvalid)
	}
	expectedServerID := runtimeidentity.LeaseServerID(input.LeaseID)
	if expectedServerID == "" || input.ServerID != expectedServerID {
		return fmt.Errorf("%w: server_id must equal the canonical identity of the exact lease", ErrInvalid)
	}
	if leaseValidated {
		return nil
	}
	_, err := s.requireManagedLease(ctx, principal, stackID, input.LeaseID)
	return err
}

func (s Service) persistDesiredRouting(ctx context.Context, principal Principal, stackID string, input EnsureInput, idempotencyKey string, expectedRevision *int64) (*PutResult, error) {
	state := DesiredState{
		TenantID:       principal.TenantID,
		StackID:        stackID,
		OwnerSubjectID: principal.OwnerSubjectID,
		ServerID:       input.ServerID,
		LeaseID:        input.LeaseID,
		Mode:           input.Mode,
		Domain:         input.Domain,
		Provenance:     input.Provenance,
		RolloutStatus:  RolloutNotRequested,
	}
	hash, err := requestHash(state, input.EnsureRollout)
	if err != nil {
		return nil, fmt.Errorf("%w: hash routing request: %v", ErrUnavailable, err)
	}
	result, err := s.requireStore().Put(ctx, PutRequest{
		DesiredState: state, IdempotencyKey: idempotencyKey,
		RequestHash: hash, ExpectedRevision: expectedRevision,
	})
	if err != nil {
		return nil, err
	}
	if result == nil || result.State == nil {
		return nil, fmt.Errorf("%w: routing store returned no desired state", ErrUnavailable)
	}
	return result, nil
}

func recoverableUndispatchedReplay(state *DesiredState, ensureRollout bool) bool {
	return ensureRollout && state != nil && state.RolloutStatus == RolloutNotRequested && state.RolloutJobID == ""
}

func rolloutAlreadyAccepted(state *DesiredState) bool {
	if state == nil {
		return false
	}
	if state.RolloutStatus == RolloutCompleted {
		return true
	}
	return state.RolloutStatus == RolloutPending && state.RolloutJobID != ""
}

func (s Service) dispatchRoutingRollout(ctx context.Context, principal Principal, stackID string, input EnsureInput, idempotencyKey string, state *DesiredState) (*DesiredState, error) {
	if s.Dispatcher == nil {
		return nil, fmt.Errorf("%w: exact-target routing rollout dispatcher is not configured", ErrUnavailable)
	}
	dispatched, err := s.Dispatcher.DispatchRoutingRollout(ctx, RolloutRequest{
		TenantID: principal.TenantID, OwnerSubjectID: principal.OwnerSubjectID,
		StackID: stackID, ServerID: input.ServerID, LeaseID: input.LeaseID,
		RoutingRevision: state.Revision, IdempotencyKey: idempotencyKey,
	})
	if err != nil {
		return nil, normalizeDispatchError(err)
	}
	if dispatched == nil || strings.TrimSpace(dispatched.JobID) == "" {
		return nil, fmt.Errorf("%w: routing rollout dispatcher returned no job", ErrUnavailable)
	}
	return s.requireStore().MarkRolloutDispatched(ctx, principal.TenantID, stackID, state.Revision, dispatched.JobID)
}

func normalizeDispatchError(err error) error {
	for _, contractErr := range []error{ErrNotFound, ErrForbidden, ErrInvalid, ErrRevisionConflict, ErrIdempotencyConflict} {
		if errors.Is(err, contractErr) {
			return err
		}
	}
	return fmt.Errorf("%w: dispatch exact routing rollout: %v", ErrUnavailable, err)
}

func (s Service) requireManagedLease(ctx context.Context, principal Principal, stackID, leaseID string) (*vmlease.Lease, error) {
	leaseID = strings.TrimSpace(leaseID)
	if leaseID == "" {
		return nil, fmt.Errorf("%w: lease_id is required for managed routing", ErrInvalid)
	}
	if s.Leases == nil {
		return nil, fmt.Errorf("%w: managed lease authority is not configured", ErrUnavailable)
	}
	leases, err := s.Leases.ListByTenant(ctx, principal.TenantID)
	if err != nil {
		return nil, fmt.Errorf("%w: load managed lease authority: %v", ErrUnavailable, err)
	}
	for i := range leases {
		lease := leases[i]
		if strings.TrimSpace(string(lease.ID)) != leaseID {
			continue
		}
		if err := validateManagedLease(principal, stackID, lease); err != nil {
			return nil, err
		}
		copy := lease
		return &copy, nil
	}
	return nil, ErrNotFound
}

func (s Service) ListTargets(ctx context.Context, principal Principal, stackID string) ([]Target, error) {
	stack, err := s.requireOwnedStack(ctx, principal, stackID)
	if err != nil {
		return nil, err
	}
	if s.Leases == nil {
		return nil, fmt.Errorf("%w: managed lease authority is not configured", ErrUnavailable)
	}
	leases, err := s.Leases.ListByTenant(ctx, principal.TenantID)
	if err != nil {
		return nil, fmt.Errorf("%w: load managed lease authority: %v", ErrUnavailable, err)
	}
	targets := make([]Target, 0, len(leases))
	for i := range leases {
		lease := leases[i]
		if !managedLeaseVisibleToPrincipal(lease, principal) || strings.TrimSpace(lease.Metadata["stack_id"]) != stack.ID {
			continue
		}
		if !monthlyruntime.IsMonthlyRuntimeMetadata(lease.Metadata) || !lease.ActiveAt(time.Now().UTC()) || !isPrimaryRoutingRole(lease.Metadata["role"]) {
			continue
		}
		targets = append(targets, targetFromManagedLease(stack.ID, s.readExactServerProjection(ctx, principal, stack.ID, lease), lease))
	}
	sort.Slice(targets, func(i, j int) bool {
		if targets[i].Role != targets[j].Role {
			return targets[i].Role < targets[j].Role
		}
		return targets[i].LeaseID < targets[j].LeaseID
	})
	return targets, nil
}

func (s Service) ResolveTarget(ctx context.Context, principal Principal, stackID, serverID, leaseID string) (*Target, error) {
	stack, err := s.requireOwnedStack(ctx, principal, stackID)
	if err != nil {
		return nil, err
	}
	serverID = strings.TrimSpace(serverID)
	leaseID = strings.TrimSpace(leaseID)
	if serverID == "" || leaseID == "" {
		return nil, fmt.Errorf("%w: exact server_id and lease_id are required", ErrInvalid)
	}
	lease, err := s.requireManagedLease(ctx, principal, stack.ID, leaseID)
	if err != nil {
		return nil, err
	}
	if expected := runtimeidentity.LeaseServerID(leaseID); expected == "" || serverID != expected {
		return nil, fmt.Errorf("%w: server_id must equal the canonical identity of the exact lease", ErrInvalid)
	}
	server := s.readExactServerProjection(ctx, principal, stack.ID, *lease)
	target := targetFromManagedLease(stack.ID, server, *lease)
	return &target, nil
}

func (s Service) readExactServerProjection(ctx context.Context, principal Principal, stackID string, lease vmlease.Lease) *controlplane.ServerRuntime {
	if s.Servers == nil {
		return nil
	}
	serverID := runtimeidentity.LeaseServerID(string(lease.ID))
	server, err := s.Servers.GetServerRuntime(ctx, principal.TenantID, serverID)
	if err != nil || server == nil || server.ID != serverID || server.OwnerSubjectID != principal.OwnerSubjectID || server.StackID != stackID || server.LeaseID != string(lease.ID) {
		return nil
	}
	return server
}

func validateManagedLease(principal Principal, stackID string, lease vmlease.Lease) error {
	if !managedLeaseVisibleToPrincipal(lease, principal) {
		return ErrForbidden
	}
	if strings.TrimSpace(lease.Metadata["stack_id"]) != strings.TrimSpace(stackID) {
		return fmt.Errorf("%w: managed lease belongs to a different stack", ErrInvalid)
	}
	if !monthlyruntime.IsMonthlyRuntimeMetadata(lease.Metadata) || !lease.ActiveAt(time.Now().UTC()) {
		return fmt.Errorf("%w: managed lease is not active monthly-runtime authority", ErrInvalid)
	}
	if !isPrimaryRoutingRole(lease.Metadata["role"]) {
		return fmt.Errorf("%w: managed lease is not a primary routing target", ErrInvalid)
	}
	return nil
}

func managedLeaseVisibleToPrincipal(lease vmlease.Lease, principal Principal) bool {
	if strings.TrimSpace(lease.Subject.OrgID) != strings.TrimSpace(principal.TenantID) {
		return false
	}
	if strings.TrimSpace(lease.Subject.ID) == strings.TrimSpace(principal.OwnerSubjectID) {
		return true
	}
	return lease.Subject.Kind == vmlease.SubjectOrg
}

func isPrimaryRoutingRole(role string) bool {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "", "foundation", "control-plane", "primary", "main":
		return true
	default:
		return false
	}
}

func targetFromManagedLease(stackID string, server *controlplane.ServerRuntime, lease vmlease.Lease) Target {
	role := strings.ToLower(strings.TrimSpace(lease.Metadata["role"]))
	if role == "" {
		role = "foundation"
	}
	target := Target{
		StackID: stackID, ServerID: runtimeidentity.LeaseServerID(string(lease.ID)), LeaseID: string(lease.ID),
		Role: role, Provider: firstNonEmpty(lease.Resource.ProviderID, lease.Metadata["lease_provider"], lease.Metadata["simulate_provider_id"]),
		Address: firstNonEmpty(lease.Metadata["runtime_public_ip"], lease.Metadata["node_public_ip"], lease.Metadata["public_ip"], lease.Metadata["runtime_ssh_host"]),
	}
	if server != nil {
		target.LifecycleState = server.LifecycleState
		target.ConnectionState = server.ConnectionState
		derivedConnection, _ := serverregistry.DeriveObservedState(time.Now().UTC(), server.LastHeartbeatAt, anyString(server.Metadata["observed_host_state"]))
		observationFresh := derivedConnection == serverregistry.ConnectionConnected || derivedConnection == serverregistry.ConnectionDegraded
		observedAddress := firstNonEmpty(nestedString(server.Metadata, "host", "public_ip"), anyString(server.Metadata["runtime_public_ip"]), anyString(server.Metadata["node_public_ip"]), anyString(server.Metadata["public_ip"]))
		if observationFresh && observedAddress != "" {
			target.Address = observedAddress
		}
	}
	return target
}

func nestedString(values map[string]any, parent, key string) string {
	if values == nil {
		return ""
	}
	switch nested := values[parent].(type) {
	case map[string]any:
		return anyString(nested[key])
	case map[string]string:
		return strings.TrimSpace(nested[key])
	default:
		return ""
	}
}

func anyString(value any) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(value))
}

func (s Service) ensureLeaseServerProjection(ctx context.Context, principal Principal, stack *controlplane.Stack, input EnsureInput, lease *vmlease.Lease) (*controlplane.ServerRuntime, error) {
	if stack == nil || lease == nil {
		return nil, fmt.Errorf("%w: exact managed routing target is incomplete", ErrInvalid)
	}
	expectedServerID := runtimeidentity.LeaseServerID(string(lease.ID))
	if expectedServerID == "" || input.ServerID != expectedServerID {
		return nil, fmt.Errorf("%w: server_id must equal the canonical identity of the exact lease", ErrInvalid)
	}
	projectionStore, ok := s.Servers.(controlplane.ServerRuntimeProjectionStore)
	if !ok {
		return nil, fmt.Errorf("%w: server projection store cannot create an authority-backed target", ErrUnavailable)
	}
	projection := leaseServerProjection(principal, stack, lease, expectedServerID)
	server, _, err := projectionStore.EnsureServerRuntimeProjection(ctx, projection)
	if err != nil {
		return nil, fmt.Errorf("%w: ensure canonical server projection: %v", ErrUnavailable, err)
	}
	return validateLeaseServerProjection(server, principal, stack.ID, string(lease.ID), expectedServerID)
}

func leaseServerProjection(principal Principal, stack *controlplane.Stack, lease *vmlease.Lease, serverID string) controlplane.ServerRuntime {
	lifecycleState := "planned"
	reasonCode := "awaiting_server_allocation"
	if firstNonEmpty(lease.Resource.EngineVMID, lease.Resource.VMID, lease.Resource.SimulationID) != "" {
		lifecycleState = "enrolling"
		reasonCode = "awaiting_agent_enrollment"
	}
	providerID := firstNonEmpty(lease.Resource.ProviderID, lease.Metadata["lease_provider"], lease.Metadata["simulate_provider_id"])
	providerResourceID := firstNonEmpty(lease.Resource.EngineVMID, lease.Resource.VMID, lease.Resource.SimulationID)
	providerRef := providerID
	if providerID != "" && providerResourceID != "" {
		providerRef = providerID + ":" + providerResourceID
	}
	name := strings.TrimSpace(stack.Name)
	if name == "" {
		name = "Managed runtime"
	} else {
		name += " primary"
	}
	return controlplane.ServerRuntime{
		ID: serverID, TenantID: principal.TenantID, StackID: stack.ID,
		OwnerSubjectID: principal.OwnerSubjectID, LeaseID: string(lease.ID), ProviderRef: providerRef,
		Name: name, LifecycleState: lifecycleState, DesiredState: "active",
		ConnectionState: "pending", HealthState: "unknown", ReasonCode: reasonCode,
		ConnectionChangedAt: time.Now().UTC(), Metadata: map[string]any{
			"authority": "runtime-lease", "projection_source": "routing-read-through",
			"projection_backfill": true, "stack_id": stack.ID, "lease_provider": providerID,
			"runtime_lane":              firstNonEmpty(lease.Metadata["runtime_lane"], "monthly-runtime"),
			"runtime_enrollment_status": strings.TrimSpace(lease.Metadata["runtime_enrollment_status"]),
		},
	}
}

func validateLeaseServerProjection(server *controlplane.ServerRuntime, principal Principal, stackID, leaseID, expectedServerID string) (*controlplane.ServerRuntime, error) {
	if server == nil || server.ID != expectedServerID || server.TenantID != principal.TenantID {
		return nil, fmt.Errorf("%w: canonical server projection conflict", ErrInvalid)
	}
	if server.OwnerSubjectID != principal.OwnerSubjectID {
		return nil, ErrForbidden
	}
	if server.StackID != stackID || server.LeaseID != leaseID {
		return nil, fmt.Errorf("%w: canonical server projection belongs to a different target", ErrInvalid)
	}
	return server, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func (s Service) requireOwnedStack(ctx context.Context, principal Principal, stackID string) (*controlplane.Stack, error) {
	principal.TenantID = strings.TrimSpace(principal.TenantID)
	principal.OwnerSubjectID = strings.TrimSpace(principal.OwnerSubjectID)
	stackID = strings.TrimSpace(stackID)
	if principal.TenantID == "" || principal.OwnerSubjectID == "" || stackID == "" {
		return nil, fmt.Errorf("%w: tenant, owner, and stack id are required", ErrInvalid)
	}
	if s.Stacks == nil {
		return nil, fmt.Errorf("%w: stack store is not configured", ErrUnavailable)
	}
	stack, err := s.Stacks.GetStack(ctx, principal.TenantID, stackID)
	if err != nil {
		if errors.Is(err, controlplane.ErrNotFound) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("%w: load stack: %v", ErrUnavailable, err)
	}
	if stack.OwnerSubjectID != principal.OwnerSubjectID {
		return nil, ErrForbidden
	}
	return stack, nil
}

func (s Service) requireStore() Store {
	if s.Store == nil {
		return unavailableStore{}
	}
	return s.Store
}

type unavailableStore struct{}

func (unavailableStore) Get(context.Context, string, string) (*DesiredState, error) {
	return nil, fmt.Errorf("%w: routing store is not configured", ErrUnavailable)
}

func (unavailableStore) Put(context.Context, PutRequest) (*PutResult, error) {
	return nil, fmt.Errorf("%w: routing store is not configured", ErrUnavailable)
}

func (unavailableStore) MarkRolloutDispatched(context.Context, string, string, int64, string) (*DesiredState, error) {
	return nil, fmt.Errorf("%w: routing store is not configured", ErrUnavailable)
}

func (unavailableStore) MarkRolloutFinished(context.Context, string, string, int64, string, string, string) (*DesiredState, error) {
	return nil, fmt.Errorf("%w: routing store is not configured", ErrUnavailable)
}

func normalizeEnsureInput(input EnsureInput) (EnsureInput, error) {
	input.ServerID = strings.TrimSpace(input.ServerID)
	input.LeaseID = strings.TrimSpace(input.LeaseID)
	input.Mode = strings.ToLower(strings.TrimSpace(input.Mode))
	input.Domain = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(input.Domain), "."))
	input.Provenance.Source = strings.TrimSpace(input.Provenance.Source)
	input.Provenance.DNSProvider = strings.ToLower(strings.TrimSpace(input.Provenance.DNSProvider))
	input.Provenance.ZoneID = strings.TrimSpace(input.Provenance.ZoneID)
	input.Provenance.ExternalDomainID = strings.TrimSpace(input.Provenance.ExternalDomainID)
	if input.Mode == "" {
		input.Mode = ModeCustomDomain
	}
	if input.ServerID == "" {
		return input, fmt.Errorf("%w: server_id is required", ErrInvalid)
	}
	if input.Mode != ModeCustomDomain {
		return input, fmt.Errorf("%w: mode must be %q", ErrInvalid, ModeCustomDomain)
	}
	if err := validateDomain(input.Domain); err != nil {
		return input, err
	}
	if input.Provenance.Source == "" || input.Provenance.DNSProvider == "" {
		return input, fmt.Errorf("%w: provenance.source and provenance.dns_provider are required", ErrInvalid)
	}
	if len(input.Provenance.Source) > 128 || len(input.Provenance.DNSProvider) > 64 || len(input.Provenance.ZoneID) > 256 || len(input.Provenance.ExternalDomainID) > 256 {
		return input, fmt.Errorf("%w: provenance field exceeds its maximum length", ErrInvalid)
	}
	if input.Provenance.DNSProvider == cloudflareDNSProvider && input.Provenance.ZoneID == "" {
		return input, fmt.Errorf("%w: provenance.zone_id is required for Cloudflare", ErrInvalid)
	}
	return input, nil
}

func managedServerRequiresLease(server controlplane.ServerRuntime) bool {
	if strings.TrimSpace(server.LeaseID) != "" {
		return true
	}
	if isManagedProviderIdentity(strings.SplitN(server.ProviderRef, ":", 2)[0]) {
		return true
	}
	for _, key := range []string{"runtime_lane", "server_mode", "server_connection_mode", "server_provisioning_mode", "lease_provider"} {
		value := strings.ToLower(strings.TrimSpace(fmt.Sprint(server.Metadata[key])))
		if isManagedProviderIdentity(value) {
			return true
		}
		switch value {
		case "monthly-runtime", "managed-cloud", "managed-subscription", "kombify-cloud":
			return true
		}
	}
	return false
}

// isManagedProviderIdentity reports whether a stored provider label denotes a
// managed-runtime provider. Persisted rows carry both the canonical vendor
// identity and the historical composite alias ("ionos-managed"), so this must
// fail closed on either; providercatalog owns both vocabularies.
func isManagedProviderIdentity(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return false
	}
	_, err := providercatalog.CanonicalProviderID(value)
	return err == nil || errors.Is(err, providercatalog.ErrCompositeProviderID)
}

func validateDomain(domain string) error {
	if domain == "" || len(domain) > 253 || strings.ContainsAny(domain, "/:@_ \\") {
		return fmt.Errorf("%w: domain must be a DNS hostname", ErrInvalid)
	}
	labels := strings.Split(domain, ".")
	if len(labels) < 2 {
		return fmt.Errorf("%w: domain must include a public suffix", ErrInvalid)
	}
	for _, label := range labels {
		if !dnsLabelPattern.MatchString(label) {
			return fmt.Errorf("%w: domain contains an invalid DNS label", ErrInvalid)
		}
	}
	return nil
}

func requestHash(state DesiredState, ensureRollout bool) (string, error) {
	payload := struct {
		StackID        string     `json:"stack_id"`
		OwnerSubjectID string     `json:"owner_subject_id"`
		ServerID       string     `json:"server_id"`
		LeaseID        string     `json:"lease_id"`
		Mode           string     `json:"mode"`
		Domain         string     `json:"domain"`
		Provenance     Provenance `json:"provenance"`
		EnsureRollout  bool       `json:"ensure_rollout"`
	}{state.StackID, state.OwnerSubjectID, state.ServerID, state.LeaseID, state.Mode, state.Domain, state.Provenance, ensureRollout}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func viewFor(state *DesiredState, replay bool) *View {
	if state == nil {
		return nil
	}
	observedStatus := "unknown"
	if state.RolloutStatus == RolloutPending || state.RolloutStatus == RolloutCompleted {
		// A completed rollout proves only that StackKits finished. DNS, TLS, and
		// service reachability remain pending until an independent observer records
		// convergence for this exact revision.
		observedStatus = "pending"
	}
	return &View{
		Desired:          *cloneDesiredState(state),
		Observed:         ObservedState{Status: observedStatus, Applied: false, AppliedRevision: 0},
		Rollout:          RolloutState{Status: state.RolloutStatus, JobID: state.RolloutJobID, ReasonCode: state.ReasonCode},
		IdempotentReplay: replay,
	}
}

func cloneDesiredState(in *DesiredState) *DesiredState {
	if in == nil {
		return nil
	}
	out := *in
	return &out
}
