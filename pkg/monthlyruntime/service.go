package monthlyruntime

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/kombifyio/techstack/internal/providercatalog"
	"github.com/kombifyio/techstack/internal/runtimeproduct/serverruntime"
	"github.com/kombifyio/techstack/internal/runtimeproduct/vmlease"
	"github.com/kombifyio/techstack/pkg/auth"
	"github.com/kombifyio/techstack/pkg/identity"
	"github.com/kombifyio/techstack/pkg/vmleases"
)

const (
	enrollmentStatusEnrolled     = "enrolled"
	runtimeObservedStateNotFound = "not_found"
	runtimeActionTimeout         = 3 * time.Minute
)

var decommissionRuntimeActionTimeout = 45 * time.Second
var encryptRuntimeCredential = encryptRuntimeCredentialIfPossible

var decommissionRuntimeRetryDelays = []time.Duration{
	5 * time.Second,
	15 * time.Second,
	30 * time.Second,
}

const (
	ManagedRuntimeEntitlementDeniedMessage = "Managed monthly runtime is not enabled for this account"
	ManagedRuntimeEntitlementErrorCode     = "managed_runtime_feature_disabled"
	ManagedRuntimeCapability               = FeatureTechStackManagedRuntime
	FeatureTechStackManagedRuntime         = "techstack.managed.runtime"
	FeatureTechStackManagedRuntimeCloudKit = "techstack.managed.runtime.cloudkit"
	FeatureTechStackManagedRuntimeCentron  = "techstack.managed.runtime.centron"
	FeatureTechStackManagedRuntimeIONOS    = "techstack.managed.runtime.ionos"
	EntitlementReasonCheckerUnavailable    = "feature_checker_unavailable"
	EntitlementReasonFeatureDisabled       = "required_feature_disabled"
	EntitlementReasonFeatureCheckFailed    = "feature_check_failed"
)

var (
	ErrForbidden                        = errors.New("monthlyruntime: lease is not visible to user")
	ErrFeatureDisabled                  = errors.New("monthlyruntime: monthly runtime feature disabled")
	ErrInvalidLease                     = errors.New("monthlyruntime: lease is not a monthly runtime lease")
	ErrEnrollmentPending                = errors.New("monthlyruntime: lease is not enrolled")
	ErrRuntimeClient                    = errors.New("monthlyruntime: simulate runtime client not configured")
	ErrExecutionAuthorityInactive       = errors.New("monthlyruntime: lease is not active under TechStack provider control")
	ErrCustodyResolutionConfirmation    = errors.New("monthlyruntime: custody resolution requires explicit provider-cleanup confirmation")
	ErrCustodyResolutionProviderManaged = errors.New("monthlyruntime: provider-managed custody must be decommissioned through provider control")
	// ErrDecommissionJournalUnavailable prevents a confirmed provider teardown
	// from being hidden behind a canceled lease when its durable proof cannot be
	// recorded.
	ErrDecommissionJournalUnavailable = errors.New("monthlyruntime: decommission operation journal unavailable")
	// ErrDecommissionGenerationUnavailable prevents provider teardown unless
	// the exact current lease resource generation can be bound into its proof.
	ErrDecommissionGenerationUnavailable = errors.New("monthlyruntime: decommission resource generation unavailable")
	// ErrRuntimeVMGone is returned when the runtime reports that the managed VM
	// behind an ENROLLED lease no longer exists at the provider (reaped by the
	// orphan sweeper, decommissioned out of band, or lost provider-side). The
	// lease is a ghost: polling it can never succeed, so callers must treat this
	// as terminal and re-provision instead of waiting.
	ErrRuntimeVMGone = errors.New("monthlyruntime: managed VM no longer exists at the provider")
	// ErrDecommissionBlockedProtected is returned when a user-facing
	// decommission (including force) targets a protected demo anchor lease.
	// Internal control-plane calls (req.Internal) bypass the protection.
	ErrDecommissionBlockedProtected = errors.New("monthlyruntime: decommission blocked, lease is protected")
	// ErrDemoRestricted is returned when a runtime action is disabled for the
	// shared public demo account (e.g. SSH access).
	ErrDemoRestricted = errors.New("monthlyruntime: action disabled for the demo account")
)

type FeatureDisabledError struct {
	FeatureKey string
}

func (e *FeatureDisabledError) Error() string {
	if strings.TrimSpace(e.FeatureKey) == "" {
		return ErrFeatureDisabled.Error()
	}
	return fmt.Sprintf("%s: %s", ErrFeatureDisabled, e.FeatureKey)
}

func (e *FeatureDisabledError) Unwrap() error {
	return ErrFeatureDisabled
}

type LeaseAuthority interface {
	Get(ctx context.Context, tenantID string, id vmlease.LeaseID) (*vmlease.Lease, error)
	Patch(ctx context.Context, tenantID string, id vmlease.LeaseID, req vmleases.PatchRequest) (*vmlease.Lease, error)
}

type LeaseInventoryReader interface {
	GetInventory(ctx context.Context, tenantID string, id vmlease.LeaseID) (*vmleases.LeaseInventoryRecord, error)
}

type RuntimeClient interface {
	RuntimeAction(ctx context.Context, req serverruntime.LeaseRuntimeActionRequest) (*serverruntime.LeaseRuntimeActionResponse, error)
}

type OperationRecorder interface {
	RecordOperation(ctx context.Context, event vmleases.OperationEvent) error
}

type StrictOperationRecorder interface {
	RecordOperationStrict(ctx context.Context, event vmleases.OperationEvent) error
}

type ConfirmedDecommissionReader interface {
	HasConfirmedDecommission(ctx context.Context, tenantID string, leaseID vmlease.LeaseID, resourceGenerationDigest string) (bool, error)
}

type OperationReader interface {
	ListOperations(ctx context.Context, tenantID string, leaseID vmlease.LeaseID, limit int) ([]vmleases.OperationEvent, error)
}

type FeatureChecker interface {
	IsEnabled(ctx context.Context, featureKey string, userID string) (bool, error)
}

type Service struct {
	Leases   LeaseAuthority
	Runtime  RuntimeClient
	Features FeatureChecker
	// CleanupReadback is the native provider-control read-only projection.
	// It is optional for legacy/self-host compatibility, but a missing source
	// never fabricates cleanup success.
	CleanupReadback CleanupReadbackSource
	// Reconcile schedules provider-resource teardown after a forced
	// decommission. When nil, force decommission is refused (see
	// ErrReconciliationUnavailable) so a canceled lease never leaks a VM.
	Reconcile ReconciliationEnqueuer
}

func RequiredFeatureKeysForProvider(providerID string) []string {
	featureKeys := []string{
		FeatureTechStackManagedRuntime,
		FeatureTechStackManagedRuntimeCloudKit,
	}
	if providerID == ProviderIONOS {
		featureKeys = append(featureKeys, FeatureTechStackManagedRuntimeIONOS)
	} else {
		featureKeys = append(featureKeys, FeatureTechStackManagedRuntimeCentron)
	}
	return featureKeys
}

func ManagedRuntimeEntitlementDenialDetails(providerID, reasonCode string, requiredFeatures, missingFeatures []string) map[string]any {
	providerID = strings.ToLower(strings.TrimSpace(providerID))
	if providerID == "" {
		providerID = ProviderCentron
	}
	requiredFeatures = compactStringSlice(requiredFeatures)
	missingFeatures = compactStringSlice(missingFeatures)
	if len(requiredFeatures) == 0 {
		requiredFeatures = RequiredFeatureKeysForProvider(providerID)
	}
	if len(missingFeatures) == 0 && reasonCode != EntitlementReasonCheckerUnavailable {
		missingFeatures = requiredFeatures
	}
	if strings.TrimSpace(reasonCode) == "" {
		reasonCode = EntitlementReasonFeatureDisabled
	}
	return map[string]any{
		"phase":             "managed_runtime_entitlement",
		"phase_label":       "Managed runtime availability",
		"error_code":        ManagedRuntimeEntitlementErrorCode,
		"reason_code":       reasonCode,
		"capability":        ManagedRuntimeCapability,
		"provider_id":       providerID,
		"required_features": requiredFeatures,
		"missing_features":  missingFeatures,
		"retryable":         false,
		"user_guidance": map[string]any{
			"title": "Managed server is not active for this account",
			"body":  "This account is not currently entitled to provision kombify-managed monthly runtime servers for this provider.",
			"next_steps": []string{
				"Use a user-owned server connection instead.",
				"Ask a workspace admin or kombify support to enable the monthly runtime and provider entitlement.",
			},
		},
		"remediation": "Use a user-owned server connection, or enable the required monthly runtime entitlement and provider feature for this account.",
		"support_context": map[string]any{
			"feature_source": "Stripe/FGA/Flagship entitlement chain",
			"cost_bearing":   true,
		},
	}
}

func compactStringSlice(values []string) []string {
	out := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

type ActionRequest struct {
	TenantID                         string
	UserID                           string
	LeaseID                          vmlease.LeaseID
	Action                           serverruntime.RuntimeAction
	ExpectedResourceGenerationDigest string
	// ReconcileClaimedDecommission allows a durable internal reconciliation job
	// to finish provider teardown after the exact claimed generation was
	// force-cancelled. It is rejected for user-facing calls.
	ReconcileClaimedDecommission bool
	// Internal marks a control-plane call where managed-runtime entitlement was
	// already enforced upstream (e.g. at stack/lease creation). It skips the
	// per-request feature re-check, which cannot see SaaS edge entitlement
	// headers in background jobs. User-facing routes must leave this false.
	Internal bool
	// Force, on a Decommission action, cancels the lease and reconciles the
	// provider resource out-of-band without contacting an unreachable runtime.
	Force bool
}

type OperationsRequest struct {
	TenantID string
	UserID   string
	LeaseID  vmlease.LeaseID
	Limit    int
}

type CustodyResolutionRequest struct {
	TenantID                 string
	UserID                   string
	LeaseID                  vmlease.LeaseID
	ProviderCleanupConfirmed bool
}

type RuntimeResponse struct {
	TenantID          string                          `json:"tenant_id,omitempty"`
	LeaseID           string                          `json:"lease_id"`
	Action            serverruntime.RuntimeAction     `json:"action"`
	RuntimeOfferingID serverruntime.RuntimeOfferingID `json:"runtime_offering_id,omitempty"`
	DesiredState      string                          `json:"desired_state,omitempty"`
	ObservedState     string                          `json:"observed_state,omitempty"`
	LeaseState        string                          `json:"lease_state,omitempty"`
	LeaseReason       string                          `json:"lease_reason,omitempty"`
	EnrollmentStatus  string                          `json:"enrollment_status,omitempty"`
	SSHEnabled        bool                            `json:"ssh_enabled,omitempty"`
	Status            *serverruntime.NodeStatus       `json:"status,omitempty"`
	SSH               *serverruntime.SSHInfo          `json:"ssh,omitempty"`
}

func (s *Service) Offerings() []Offering {
	return Catalog()
}

func (s *Service) Operations(ctx context.Context, req OperationsRequest) ([]vmleases.OperationEvent, error) {
	if s == nil || s.Leases == nil {
		return nil, vmleases.ErrEnrollmentRequired
	}
	tenantID := strings.TrimSpace(req.TenantID)
	if tenantID == "" {
		return nil, vmleases.ErrTenantRequired
	}
	lease, err := s.Leases.Get(ctx, tenantID, req.LeaseID)
	if err != nil {
		return nil, err
	}
	if !canAccessLease(*lease, strings.TrimSpace(req.UserID), tenantID) {
		return nil, ErrForbidden
	}
	reader, ok := s.Leases.(OperationReader)
	if !ok {
		return []vmleases.OperationEvent{}, nil
	}
	return reader.ListOperations(ctx, tenantID, req.LeaseID, normalizedOperationLimit(req.Limit))
}

// ResolveCustody archives a legacy or unbound lease after the owner explicitly
// confirms that no provider resource remains. It never contacts a provider and
// is therefore forbidden for provider-control-owned generations; those must use
// the normal generation-bound decommission lifecycle.
func (s *Service) ResolveCustody(ctx context.Context, req CustodyResolutionRequest) (*RuntimeResponse, error) {
	if s == nil || s.Leases == nil {
		return nil, vmleases.ErrEnrollmentRequired
	}
	if !req.ProviderCleanupConfirmed {
		return nil, ErrCustodyResolutionConfirmation
	}
	tenantID := strings.TrimSpace(req.TenantID)
	if tenantID == "" {
		return nil, vmleases.ErrTenantRequired
	}
	inventory, ok := s.Leases.(LeaseInventoryReader)
	if !ok {
		return nil, vmleases.ErrLeaseInventoryUnavailable
	}
	record, err := inventory.GetInventory(ctx, tenantID, req.LeaseID)
	if err != nil {
		return nil, err
	}
	lease := &record.Lease
	actor := strings.TrimSpace(req.UserID)
	if !canAccessLease(*lease, actor, tenantID) {
		return nil, ErrForbidden
	}
	if strings.TrimSpace(lease.Metadata["custody_resolution_status"]) == "resolved" {
		return custodyResolutionResponse(tenantID, *lease), nil
	}
	if record.ExecutionAuthority == vmleases.LeaseExecutionAuthorityTechStackProviderControl {
		return nil, ErrCustodyResolutionProviderManaged
	}
	legacyCustody := record.AuthorityState == vmleases.LeaseAuthorityStateUnbound ||
		record.AuthorityState == vmleases.LeaseAuthorityStateLegacyQuarantined
	digest, err := vmleases.ResourceGenerationDigest(tenantID, *lease)
	if err != nil && !(legacyCustody && errors.Is(err, vmleases.ErrResourceGenerationUnavailable)) {
		return nil, err
	}
	// Pre-generation legacy custody records intentionally have no
	// resource_generation_id. Once the authority-aware inventory has proved
	// that such a record is not provider-control-owned, resolving it is a local
	// archival operation and must not be blocked by a provider-generation guard
	// that cannot exist for this historical row. Newer records retain the exact
	// digest check below.
	if legacyCustody && errors.Is(err, vmleases.ErrResourceGenerationUnavailable) {
		digest = ""
	}
	archived := vmlease.DesiredStateArchived
	resolvedAt := time.Now().UTC().Format(time.RFC3339)
	patched, err := s.Leases.Patch(ctx, tenantID, req.LeaseID, vmleases.PatchRequest{
		DesiredState:                     &archived,
		Cancel:                           true,
		ExpectedResourceGenerationDigest: digest,
		Metadata: map[string]string{
			"custody_resolution_status": "resolved",
			"custody_resolution_reason": "owner_confirmed_provider_cleanup",
			"custody_resolved_at":       resolvedAt,
			"custody_resolved_by":       actor,
		},
	})
	if err != nil {
		return nil, err
	}
	if recorder, ok := s.Leases.(OperationRecorder); ok {
		_ = recorder.RecordOperation(ctx, vmleases.OperationEvent{
			TenantID: tenantID, LeaseID: req.LeaseID, EventType: vmleases.OperationEventDecommission,
			Status: vmleases.OperationStatusCustodyResolved, Actor: actor,
			ResourceGenerationDigest: digest,
		})
	}
	return custodyResolutionResponse(tenantID, *patched), nil
}

func custodyResolutionResponse(tenantID string, lease vmlease.Lease) *RuntimeResponse {
	return &RuntimeResponse{
		TenantID: tenantID, LeaseID: string(lease.ID), Action: serverruntime.RuntimeActionDecommission,
		DesiredState: string(lease.DesiredState), ObservedState: "custody_resolved", LeaseState: "resolved",
	}
}

func (s *Service) Action(ctx context.Context, req ActionRequest) (*RuntimeResponse, error) {
	prepared, err := s.prepareAction(ctx, req)
	if err != nil {
		return nil, err
	}
	if resp, handled, replayErr := s.confirmedDecommissionReplayShortCircuit(ctx, req, prepared); handled {
		return resp, replayErr
	}
	if resp, handled, shortCircuitErr := s.decommissionShortCircuit(ctx, prepared.tenantID, prepared.lease, prepared.offeringID, req); handled {
		return resp, shortCircuitErr
	}
	if resp, handled, shortCircuitErr := s.persistedSSHInfoShortCircuit(ctx, req, prepared); handled {
		return resp, shortCircuitErr
	}
	return s.executeRuntimeAction(ctx, req, prepared)
}

type preparedRuntimeAction struct {
	tenantID                     string
	lease                        *vmlease.Lease
	offeringID                   serverruntime.RuntimeOfferingID
	confirmedDecommissionDigest  string
	decommissionClaimPreexisting bool
}

func (s *Service) prepareAction(ctx context.Context, req ActionRequest) (*preparedRuntimeAction, error) {
	if err := s.validateActionDependencies(req); err != nil {
		return nil, err
	}
	tenantID, lease, offeringID, err := s.loadAuthorizedActionLease(ctx, req)
	if err != nil {
		return nil, err
	}
	if err = s.validateForceReconciliation(req); err != nil {
		return nil, err
	}
	if err = s.ensureActionFeatures(ctx, req, *lease); err != nil {
		return nil, err
	}
	lease, confirmedDigest, claimPreexisting, err := s.prepareDecommissionGeneration(ctx, tenantID, lease, req)
	if err != nil {
		return nil, err
	}
	return &preparedRuntimeAction{
		tenantID:                     tenantID,
		lease:                        lease,
		offeringID:                   offeringID,
		confirmedDecommissionDigest:  confirmedDigest,
		decommissionClaimPreexisting: claimPreexisting,
	}, nil
}

// validateActionDependencies refuses an action whose execution path needs a
// dependency this service was not given.
//
// Runtime may be the native Runtime Inventory projection in production or a
// bounded legacy client in tests/self-hosted compatibility paths. Provider
// mutations never depend on that client: Techstack provider control remains the
// sole executable authority.
//
// Native ordinary and forced decommission enqueue generation-bound provider
// reconciliation before canceling the lease. When that durable path is absent,
// the legacy ordinary path still requires Runtime while force reaches the
// reconciliation-specific error instead of reporting a misleading client 503.
func (s *Service) validateActionDependencies(req ActionRequest) error {
	if s == nil || s.Leases == nil {
		return vmleases.ErrEnrollmentRequired
	}
	if s.Runtime != nil || actionRunsWithoutRuntimeClient(req) ||
		(req.Action == serverruntime.RuntimeActionDecommission && s.ForceDecommissionReady()) {
		return nil
	}
	return ErrRuntimeClient
}

// actionRunsWithoutRuntimeClient reports whether this request is served by a
// path that never calls the runtime client.
func actionRunsWithoutRuntimeClient(req ActionRequest) bool {
	if req.Action == serverruntime.RuntimeActionSSHInfo {
		return true
	}
	return req.Action == serverruntime.RuntimeActionDecommission && req.Force
}

func (s *Service) loadAuthorizedActionLease(ctx context.Context, req ActionRequest) (string, *vmlease.Lease, serverruntime.RuntimeOfferingID, error) {
	tenantID := strings.TrimSpace(req.TenantID)
	if tenantID == "" {
		return "", nil, "", vmleases.ErrTenantRequired
	}
	inventory, ok := s.Leases.(LeaseInventoryReader)
	if !ok {
		return "", nil, "", vmleases.ErrLeaseInventoryUnavailable
	}
	record, err := inventory.GetInventory(ctx, tenantID, req.LeaseID)
	if err != nil {
		return "", nil, "", err
	}
	lease := &record.Lease
	if !canAccessLease(*lease, strings.TrimSpace(req.UserID), tenantID) {
		return "", nil, "", ErrForbidden
	}
	if !record.NativeActive() && !nativeInactiveDecommissionContinuation(*record, req) {
		return "", nil, "", ErrExecutionAuthorityInactive
	}
	offeringID, err := validateMonthlyRuntimeLease(*lease, req.Action)
	if err != nil {
		return "", nil, "", err
	}
	if guardErr := demoGuardForAction(*lease, req); guardErr != nil {
		return "", nil, "", guardErr
	}
	return tenantID, lease, offeringID, nil
}

func nativeInactiveDecommissionContinuation(record vmleases.LeaseInventoryRecord, req ActionRequest) bool {
	return record.ExecutionAuthority == vmleases.LeaseExecutionAuthorityTechStackProviderControl &&
		record.AuthorityState == vmleases.LeaseAuthorityStateNativeInactive &&
		req.Action == serverruntime.RuntimeActionDecommission &&
		req.Internal &&
		req.ReconcileClaimedDecommission &&
		strings.TrimSpace(req.ExpectedResourceGenerationDigest) != ""
}

func (s *Service) validateForceReconciliation(req ActionRequest) error {
	if req.Action != serverruntime.RuntimeActionDecommission || !req.Force {
		return nil
	}
	if !s.ForceDecommissionReady() {
		// Check this before claiming the resource generation. A 503 response
		// from the fail-closed production path must not mutate even metadata.
		return ErrReconciliationUnavailable
	}
	return nil
}

func (s *Service) ensureActionFeatures(ctx context.Context, req ActionRequest, lease vmlease.Lease) error {
	if req.Action == serverruntime.RuntimeActionDecommission || req.Internal {
		return nil
	}
	return s.ensureFeatures(ctx, strings.TrimSpace(req.UserID), lease)
}

func (s *Service) prepareDecommissionGeneration(ctx context.Context, tenantID string, lease *vmlease.Lease, req ActionRequest) (*vmlease.Lease, string, bool, error) {
	if req.Action != serverruntime.RuntimeActionDecommission {
		return lease, "", false, nil
	}
	confirmedDigest, err := vmleases.ResourceGenerationDigest(tenantID, *lease)
	if err != nil {
		return nil, "", false, fmt.Errorf("%w: %v", ErrDecommissionGenerationUnavailable, err)
	}
	if err = validateDecommissionGenerationRequest(req, *lease, confirmedDigest); err != nil {
		return nil, "", false, err
	}
	claimPreexisting := strings.TrimSpace(lease.Metadata[vmleases.MetadataKeyDecommissionClaimDigest]) != ""
	if lease.CancelledAt != nil {
		return lease, confirmedDigest, claimPreexisting, nil
	}
	lease, err = s.Leases.Patch(ctx, tenantID, req.LeaseID, vmleases.PatchRequest{
		ExpectedResourceGenerationDigest: confirmedDigest,
		ClaimDecommission:                true,
	})
	if err != nil {
		return nil, "", false, err
	}
	return lease, confirmedDigest, claimPreexisting, nil
}

// confirmedDecommissionReplayShortCircuit closes the crash window between a
// durable provider-success journal write and lease finalization. The exact
// generation proof is checked after the generation claim and before any
// runtime/provider call. A replay only performs the generation-CAS authority
// update; it never asks the provider to decommission the same resource again.
func (s *Service) confirmedDecommissionReplayShortCircuit(ctx context.Context, req ActionRequest, prepared *preparedRuntimeAction) (*RuntimeResponse, bool, error) {
	if req.Action != serverruntime.RuntimeActionDecommission || prepared.lease.CancelledAt != nil {
		return nil, false, nil
	}
	reader, ok := s.Leases.(ConfirmedDecommissionReader)
	if !ok {
		if prepared.decommissionClaimPreexisting {
			return nil, true, ErrDecommissionJournalUnavailable
		}
		return nil, false, nil
	}
	confirmed, err := reader.HasConfirmedDecommission(ctx, prepared.tenantID, req.LeaseID, prepared.confirmedDecommissionDigest)
	if err != nil {
		if errors.Is(err, vmleases.ErrOperationJournalUnavailable) && !prepared.decommissionClaimPreexisting {
			return nil, false, nil
		}
		if errors.Is(err, vmleases.ErrOperationJournalUnavailable) {
			return nil, true, fmt.Errorf("%w: %v", ErrDecommissionJournalUnavailable, err)
		}
		return nil, true, err
	}
	if !confirmed {
		return nil, false, nil
	}
	lease, err := s.Leases.Patch(ctx, prepared.tenantID, req.LeaseID, vmleases.PatchRequest{
		Cancel:                           true,
		ExpectedResourceGenerationDigest: prepared.confirmedDecommissionDigest,
		Metadata: map[string]string{
			"runtime_last_action":    string(serverruntime.RuntimeActionDecommission),
			"runtime_lease_state":    leaseStateCancelled,
			"runtime_observed_state": runtimeObservedStateNotFound,
		},
	})
	if err != nil {
		return nil, true, err
	}
	resp := cancelledDecommissionResponse(prepared.tenantID, *lease, prepared.offeringID, req.Action)
	logRuntimeAction(prepared.tenantID, req.LeaseID, strings.TrimSpace(req.UserID), req.Action, resp, nil)
	return publicRuntimeResponse(resp, *lease), true, nil
}

func validateDecommissionGenerationRequest(req ActionRequest, lease vmlease.Lease, confirmedDigest string) error {
	expectedDigest := strings.TrimSpace(req.ExpectedResourceGenerationDigest)
	if expectedDigest != "" && expectedDigest != confirmedDigest {
		return vmleases.ErrResourceGenerationSuperseded
	}
	if !req.ReconcileClaimedDecommission {
		return nil
	}
	if !req.Internal || expectedDigest == "" {
		return ErrForbidden
	}
	if strings.TrimSpace(lease.Metadata[vmleases.MetadataKeyDecommissionClaimDigest]) != expectedDigest {
		return vmleases.ErrResourceGenerationSuperseded
	}
	return nil
}

func (s *Service) persistedSSHInfoShortCircuit(ctx context.Context, req ActionRequest, prepared *preparedRuntimeAction) (*RuntimeResponse, bool, error) {
	if req.Action != serverruntime.RuntimeActionSSHInfo || req.Internal {
		return nil, false, nil
	}
	resp := persistedSSHInfoResponse(prepared.tenantID, *prepared.lease, prepared.offeringID, req.Action)
	if resp == nil {
		return nil, false, nil
	}
	actor := strings.TrimSpace(req.UserID)
	logRuntimeAction(prepared.tenantID, req.LeaseID, actor, req.Action, resp, nil)
	if err := s.recordRuntimeAction(ctx, prepared.tenantID, req.LeaseID, actor, req.Action, nil); err != nil {
		return nil, true, err
	}
	return publicRuntimeResponse(resp, *prepared.lease), true, nil
}

func (s *Service) executeRuntimeAction(ctx context.Context, req ActionRequest, prepared *preparedRuntimeAction) (*RuntimeResponse, error) {
	if s.Runtime == nil {
		return nil, ErrRuntimeClient
	}
	lease, err := s.patchRuntimeDesiredState(ctx, prepared.tenantID, req.LeaseID, prepared.lease, req.Action, prepared.confirmedDecommissionDigest)
	if err != nil {
		return nil, err
	}
	actionCtx, cancel := context.WithTimeout(ctx, runtimeActionTimeoutFor(req.Action))
	defer cancel()
	runtimeReq := serverruntime.LeaseRuntimeActionRequest{
		TenantID:   prepared.tenantID,
		OwnerID:    strings.TrimSpace(lease.Subject.ID),
		LeaseID:    string(lease.ID),
		Action:     req.Action,
		OfferingID: prepared.offeringID,
		Metadata:   cloneMetadata(lease.Metadata),
	}
	resp, runtimeErr := s.runtimeActionWithRetry(actionCtx, runtimeReq)
	actor := strings.TrimSpace(req.UserID)
	logRuntimeAction(prepared.tenantID, req.LeaseID, actor, req.Action, resp, runtimeErr)
	if recordErr := s.recordRuntimeActionResult(ctx, prepared.tenantID, req, actor, prepared.confirmedDecommissionDigest, runtimeErr); recordErr != nil {
		return nil, recordErr
	}
	if runtimeErr != nil {
		return nil, decommissionResultError(req.Action, runtimeErr)
	}
	return s.finishRuntimeAction(ctx, prepared.tenantID, req, lease, resp, prepared.confirmedDecommissionDigest)
}

func (s *Service) recordRuntimeActionResult(ctx context.Context, tenantID string, req ActionRequest, actor, confirmedDecommissionDigest string, runtimeErr error) error {
	if runtimeErr == nil && req.Action == serverruntime.RuntimeActionDecommission {
		// This distinct event is the durable proof that the normal provider
		// decommission path returned successfully. Persist it before canceling
		// the lease so a journal failure cannot hide an unproven teardown behind
		// a canceled lease. Force and already-canceled short circuits keep their
		// runtime_action events and never reach this branch.
		return s.recordConfirmedDecommission(ctx, tenantID, req.LeaseID, actor, confirmedDecommissionDigest)
	}
	recordErr := s.recordRuntimeAction(ctx, tenantID, req.LeaseID, actor, req.Action, runtimeErr)
	if runtimeErr == nil {
		return recordErr
	}
	return nil
}

func (s *Service) finishRuntimeAction(ctx context.Context, tenantID string, req ActionRequest, lease *vmlease.Lease, resp *serverruntime.LeaseRuntimeActionResponse, confirmedDecommissionDigest string) (*RuntimeResponse, error) {
	var err error
	if req.Action == serverruntime.RuntimeActionDecommission {
		lease, err = s.Leases.Patch(ctx, tenantID, req.LeaseID, vmleases.PatchRequest{
			Cancel:                           true,
			ExpectedResourceGenerationDigest: confirmedDecommissionDigest,
		})
		if err != nil {
			return nil, err
		}
	}
	if metadata := runtimeActionMetadata(resp, req.Action); len(metadata) > 0 {
		lease, err = s.Leases.Patch(ctx, tenantID, req.LeaseID, vmleases.PatchRequest{
			Metadata:                         metadata,
			ExpectedResourceGenerationDigest: confirmedDecommissionDigest,
		})
		if err != nil {
			return nil, err
		}
	}
	return publicRuntimeResponse(resp, *lease), nil
}

func runtimeActionTimeoutFor(action serverruntime.RuntimeAction) time.Duration {
	if action == serverruntime.RuntimeActionDecommission {
		return decommissionRuntimeActionTimeout
	}
	return runtimeActionTimeout
}

func (s *Service) patchRuntimeDesiredState(ctx context.Context, tenantID string, leaseID vmlease.LeaseID, lease *vmlease.Lease, action serverruntime.RuntimeAction, expectedResourceGenerationDigest string) (*vmlease.Lease, error) {
	desired, ok := desiredStateForRuntimeAction(action)
	if !ok {
		return lease, nil
	}
	return s.Leases.Patch(ctx, tenantID, leaseID, vmleases.PatchRequest{
		DesiredState:                     &desired,
		ExpectedResourceGenerationDigest: expectedResourceGenerationDigest,
	})
}

func desiredStateForRuntimeAction(action serverruntime.RuntimeAction) (vmlease.DesiredState, bool) {
	switch action {
	case serverruntime.RuntimeActionStart:
		return vmlease.DesiredStateRunning, true
	case serverruntime.RuntimeActionStop:
		return vmlease.DesiredStateStopped, true
	default:
		return "", false
	}
}

func (s *Service) runtimeActionWithRetry(ctx context.Context, req serverruntime.LeaseRuntimeActionRequest) (*serverruntime.LeaseRuntimeActionResponse, error) {
	resp, err := s.Runtime.RuntimeAction(ctx, req)
	if err == nil || req.Action != serverruntime.RuntimeActionDecommission || !isTransientRuntimeActionError(err) {
		return resp, err
	}
	lastErr := err
	for _, delay := range decommissionRuntimeRetryDelays {
		if delay > 0 {
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil, ctx.Err()
			case <-timer.C:
			}
		}
		resp, err = s.Runtime.RuntimeAction(ctx, req)
		if err == nil || !isTransientRuntimeActionError(err) {
			return resp, err
		}
		lastErr = err
	}
	return nil, lastErr
}

func isTransientRuntimeActionError(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	for _, pattern := range []string{
		"returned 502",
		"returned 503",
		"returned 504",
		"bad gateway",
		"service unavailable",
		"gateway timeout",
		"connection refused",
		"connection reset",
		"server closed idle connection",
		"context deadline exceeded",
		"i/o timeout",
		"timeout awaiting response headers",
		"eof",
	} {
		if strings.Contains(text, pattern) {
			return true
		}
	}
	return false
}

func cancelledDecommissionResponse(tenantID string, lease vmlease.Lease, offeringID serverruntime.RuntimeOfferingID, action serverruntime.RuntimeAction) *serverruntime.LeaseRuntimeActionResponse {
	state := firstNonEmpty(lease.Metadata["runtime_observed_state"], runtimeObservedStateNotFound)
	leaseState := leaseStateCancelled
	if state == runtimeObservedStateReconciliationPending {
		leaseState = state
	}
	return &serverruntime.LeaseRuntimeActionResponse{
		TenantID:      tenantID,
		LeaseID:       string(lease.ID),
		Action:        action,
		OfferingID:    offeringID,
		DesiredState:  string(lease.DesiredState),
		ObservedState: state,
		LeaseState:    leaseState,
		Status:        &serverruntime.NodeStatus{ID: strings.TrimSpace(lease.Resource.EngineVMID), State: state},
	}
}

func normalizedOperationLimit(limit int) int {
	if limit <= 0 {
		return 50
	}
	if limit > 100 {
		return 100
	}
	return limit
}

func logRuntimeAction(tenantID string, leaseID vmlease.LeaseID, actor string, action serverruntime.RuntimeAction, resp *serverruntime.LeaseRuntimeActionResponse, cause error) {
	args := []any{
		"tenant_id", strings.TrimSpace(tenantID),
		"lease_id", string(leaseID),
		"actor", strings.TrimSpace(actor),
		"action", string(action),
	}
	if cause != nil {
		slog.Error("monthly_runtime_action_failed", append(args, "error", cause.Error())...)
		return
	}
	if resp != nil {
		args = append(args,
			"desired_state", strings.TrimSpace(resp.DesiredState),
			"observed_state", strings.TrimSpace(resp.ObservedState),
			"lease_state", strings.TrimSpace(resp.LeaseState),
			"ssh_enabled", resp.SSHEnabled,
		)
	}
	slog.Info("monthly_runtime_action_completed", args...)
}

func validateMonthlyRuntimeLease(lease vmlease.Lease, action serverruntime.RuntimeAction) (serverruntime.RuntimeOfferingID, error) {
	if !IsMonthlyRuntimeMetadata(lease.Metadata) {
		return "", ErrInvalidLease
	}
	offeringID := OfferingIDFromMetadata(lease.Metadata)
	if _, ok := OfferingByID(offeringID); !ok {
		return "", ErrInvalidLease
	}
	if err := providercatalog.ValidateNoLegacyProviderFields(
		lease.Metadata[MetadataKeyLeaseProvider],
		lease.Metadata[MetadataKeySimulateProviderID],
	); err != nil {
		return "", fmt.Errorf("%w: %v", ErrInvalidLease, err)
	}
	if _, err := providercatalog.ResolveCanonicalProviderID(
		strings.TrimSpace(lease.Resource.ProviderID),
		strings.TrimSpace(lease.Metadata[MetadataKeyProviderID]),
	); err != nil {
		return "", fmt.Errorf("%w: %v", ErrInvalidLease, err)
	}
	if action == serverruntime.RuntimeActionDecommission {
		return offeringID, nil
	}
	if strings.TrimSpace(lease.Metadata["runtime_enrollment_status"]) != enrollmentStatusEnrolled {
		return "", ErrEnrollmentPending
	}
	return offeringID, nil
}

func (s *Service) ensureFeatures(ctx context.Context, userID string, lease vmlease.Lease) error {
	if s.Features == nil {
		return nil
	}
	providerID := strings.TrimSpace(lease.Resource.ProviderID)
	if providerID == "" {
		providerID = ProviderFromMetadata(lease.Metadata)
	}
	featureCtx := monthlyRuntimeFeatureContext(ctx, userID, lease)
	for _, featureKey := range RequiredFeatureKeysForProvider(providerID) {
		enabled, err := s.Features.IsEnabled(featureCtx, featureKey, userID)
		if err != nil {
			return err
		}
		if !enabled {
			return &FeatureDisabledError{FeatureKey: featureKey}
		}
	}
	return nil
}

func monthlyRuntimeFeatureContext(ctx context.Context, userID string, lease vmlease.Lease) context.Context {
	if strings.TrimSpace(lease.Subject.OrgID) == "" {
		return ctx
	}
	if id := identity.FromContext(ctx); id != nil && strings.TrimSpace(id.OrgID) != "" {
		return ctx
	}
	return identity.NewContext(ctx, &identity.Identity{
		UserID: strings.TrimSpace(userID),
		OrgID:  strings.TrimSpace(lease.Subject.OrgID),
	})
}

func canAccessLease(lease vmlease.Lease, userID, tenantID string) bool {
	if strings.TrimSpace(lease.Subject.OrgID) != tenantID {
		return false
	}
	if userID == "" {
		return false
	}
	if strings.TrimSpace(lease.Subject.ID) == userID {
		return true
	}
	return lease.Subject.Kind == vmlease.SubjectOrg
}

func (s *Service) recordRuntimeAction(ctx context.Context, tenantID string, leaseID vmlease.LeaseID, actor string, action serverruntime.RuntimeAction, cause error) error {
	recorder, ok := s.Leases.(OperationRecorder)
	if !ok {
		return nil
	}
	status := operationStatusForAction(action)
	errorText := ""
	if cause != nil {
		status = vmleases.OperationStatusFailed
		errorText = cause.Error()
	}
	return recorder.RecordOperation(ctx, vmleases.OperationEvent{
		TenantID:  tenantID,
		LeaseID:   leaseID,
		EventType: vmleases.OperationEventRuntimeAction,
		Status:    status,
		Actor:     actor,
		Error:     errorText,
	})
}

func (s *Service) recordReconciliationPending(ctx context.Context, tenantID string, leaseID vmlease.LeaseID, actor, resourceGenerationDigest string) error {
	recorder, ok := s.Leases.(OperationRecorder)
	if !ok {
		return nil
	}
	return recorder.RecordOperation(ctx, vmleases.OperationEvent{
		TenantID:                 tenantID,
		LeaseID:                  leaseID,
		EventType:                vmleases.OperationEventDecommission,
		Status:                   vmleases.OperationStatusPending,
		Actor:                    actor,
		ResourceGenerationDigest: resourceGenerationDigest,
	})
}

func (s *Service) recordConfirmedDecommission(ctx context.Context, tenantID string, leaseID vmlease.LeaseID, actor, resourceGenerationDigest string) error {
	recorder, ok := s.Leases.(StrictOperationRecorder)
	if !ok {
		return ErrDecommissionJournalUnavailable
	}
	err := recorder.RecordOperationStrict(ctx, vmleases.OperationEvent{
		TenantID:                 tenantID,
		LeaseID:                  leaseID,
		EventType:                vmleases.OperationEventDecommission,
		Status:                   vmleases.OperationStatusDecommissioned,
		Actor:                    actor,
		ResourceGenerationDigest: resourceGenerationDigest,
	})
	if errors.Is(err, vmleases.ErrOperationJournalUnavailable) {
		return fmt.Errorf("%w: %v", ErrDecommissionJournalUnavailable, err)
	}
	return err
}

func operationStatusForAction(action serverruntime.RuntimeAction) string {
	switch action {
	case serverruntime.RuntimeActionStart:
		return vmleases.OperationStatusStarted
	case serverruntime.RuntimeActionStop:
		return vmleases.OperationStatusStopped
	case serverruntime.RuntimeActionEnableSSH:
		return vmleases.OperationStatusSSHEnabled
	case serverruntime.RuntimeActionDisableSSH:
		return vmleases.OperationStatusSSHDisabled
	case serverruntime.RuntimeActionSSHInfo:
		return vmleases.OperationStatusSSHInfoRequested
	case serverruntime.RuntimeActionDecommission:
		return vmleases.OperationStatusDecommissioned
	default:
		return vmleases.OperationStatusStatusRequested
	}
}

func runtimeActionMetadata(resp *serverruntime.LeaseRuntimeActionResponse, action serverruntime.RuntimeAction) map[string]string {
	if resp == nil {
		return nil
	}
	metadata := cloneRuntimeActionMetadata(resp.Metadata)
	if metadata == nil {
		metadata = map[string]string{}
	}
	put := func(key, value string) {
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if key != "" && value != "" {
			metadata[key] = value
		}
	}
	put("runtime_last_action", string(action))
	put("runtime_observed_state", resp.ObservedState)
	put("runtime_lease_state", resp.LeaseState)
	if resp.Status != nil {
		put("runtime_status_updated_at", resp.Status.UpdatedAt)
		put("runtime_public_ip", resp.Status.PublicIP)
		put("node_public_ip", resp.Status.PublicIP)
		put("public_ip", resp.Status.PublicIP)
		put("runtime_private_ip", resp.Status.PrivateIP)
		put("node_private_ip", resp.Status.PrivateIP)
	}
	if resp.SSH != nil {
		host := firstNonEmpty(resp.SSH.Host, resp.SSH.DisplayHost, resp.SSH.NodePublicIP, resp.SSH.NodePrivateIP)
		statusPublicIP := ""
		if resp.Status != nil {
			statusPublicIP = resp.Status.PublicIP
		}
		publicIP := firstNonEmpty(resp.SSH.NodePublicIP, statusPublicIP, host)
		privateIP := resp.SSH.NodePrivateIP
		put("runtime_ssh_host", host)
		put("runtime_host", host)
		put("runtime_public_ip", publicIP)
		put("node_public_ip", publicIP)
		put("public_ip", publicIP)
		put("runtime_private_ip", privateIP)
		put("node_private_ip", privateIP)
		put("runtime_ssh_user", resp.SSH.User)
		if resp.SSH.Port > 0 {
			put("runtime_ssh_port", strconv.Itoa(resp.SSH.Port))
		}
		putEncryptedRuntimeCredential(metadata, "runtime_ssh_private_key_enc", resp.SSH.PrivateKey)
		putEncryptedRuntimeCredential(metadata, "runtime_client_private_key_enc", resp.SSH.ClientPrivateKey)
		putEncryptedRuntimeCredential(metadata, "runtime_ssh_password_enc", resp.SSH.Password)
	}
	if len(metadata) == 0 {
		return nil
	}
	return metadata
}

func cloneRuntimeActionMetadata(metadata map[string]string) map[string]string {
	out := cloneMetadata(metadata)
	for key := range out {
		if runtimeActionMetadataSensitiveKey(key) || runtimeActionMetadataProviderIdentityKey(key) {
			delete(out, key)
		}
	}
	return out
}

func runtimeActionMetadataProviderIdentityKey(key string) bool {
	switch strings.TrimSpace(key) {
	case MetadataKeyProviderID, MetadataKeyLeaseProvider, MetadataKeySimulateProviderID:
		return true
	default:
		return false
	}
}

func runtimeActionMetadataSensitiveKey(key string) bool {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "private_key",
		"ssh_private_key",
		"runtime_ssh_private_key",
		"client_private_key",
		"runtime_client_private_key",
		"password",
		"ssh_password",
		"runtime_ssh_password",
		"key_path",
		"ssh_key_path",
		"runtime_ssh_key_path":
		return true
	default:
		return false
	}
}

func putEncryptedRuntimeCredential(metadata map[string]string, key, value string) {
	if metadata == nil {
		return
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return
	}
	encrypted, ok := encryptRuntimeCredential(value)
	if !ok {
		return
	}
	if encrypted = strings.TrimSpace(encrypted); encrypted != "" {
		metadata[key] = encrypted
	}
}

func encryptRuntimeCredentialIfPossible(value string) (string, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", false
	}
	if auth.IsEncrypted(value) {
		return value, true
	}
	encryptor := auth.GetEncryptor()
	if encryptor == nil {
		return "", false
	}
	encrypted, err := encryptor.Encrypt(value)
	if err != nil {
		return "", false
	}
	return encrypted, true
}

func persistedSSHInfoResponse(tenantID string, lease vmlease.Lease, offeringID serverruntime.RuntimeOfferingID, action serverruntime.RuntimeAction) *serverruntime.LeaseRuntimeActionResponse {
	host := firstNonEmpty(
		lease.Metadata["runtime_ssh_host"],
		lease.Metadata["runtime_host"],
		lease.Metadata["runtime_public_ip"],
		lease.Metadata["node_public_ip"],
		lease.Metadata["public_ip"],
	)
	if host == "" {
		return nil
	}
	port := runtimeSSHPort(lease.Metadata["runtime_ssh_port"])
	user := firstNonEmpty(lease.Metadata["runtime_ssh_user"], "root")
	publicIP := firstNonEmpty(lease.Metadata["runtime_public_ip"], lease.Metadata["node_public_ip"], lease.Metadata["public_ip"], host)
	privateIP := firstNonEmpty(lease.Metadata["runtime_private_ip"], lease.Metadata["node_private_ip"])
	state := firstNonEmpty(lease.Metadata["runtime_observed_state"], string(lease.DesiredState), "unknown")
	updatedAt := firstNonEmpty(lease.Metadata["runtime_status_updated_at"], lease.RenewedAt.Format(time.RFC3339))
	return &serverruntime.LeaseRuntimeActionResponse{
		TenantID:      strings.TrimSpace(tenantID),
		LeaseID:       string(lease.ID),
		Action:        action,
		OfferingID:    offeringID,
		DesiredState:  string(lease.DesiredState),
		ObservedState: state,
		LeaseState:    firstNonEmpty(lease.Metadata["runtime_lease_state"], "valid"),
		SSHEnabled:    true,
		Status: &serverruntime.NodeStatus{
			ID:        strings.TrimSpace(lease.Resource.EngineVMID),
			State:     state,
			PublicIP:  publicIP,
			PrivateIP: privateIP,
			UpdatedAt: updatedAt,
		},
		SSH: &serverruntime.SSHInfo{
			Host:          host,
			DisplayHost:   host,
			User:          user,
			Port:          port,
			NodePublicIP:  publicIP,
			NodePrivateIP: privateIP,
			Command:       fmt.Sprintf("ssh -p %d %s@%s", port, user, host),
		},
		Metadata: cloneMetadata(lease.Metadata),
	}
}

func runtimeSSHPort(value string) int {
	if parsed, err := strconv.Atoi(strings.TrimSpace(value)); err == nil && parsed > 0 {
		return parsed
	}
	return 22
}

func publicRuntimeResponse(resp *serverruntime.LeaseRuntimeActionResponse, lease vmlease.Lease) *RuntimeResponse {
	out := &RuntimeResponse{
		TenantID:          strings.TrimSpace(lease.Subject.OrgID),
		LeaseID:           string(lease.ID),
		RuntimeOfferingID: OfferingIDFromMetadata(lease.Metadata),
		DesiredState:      string(lease.DesiredState),
		EnrollmentStatus:  strings.TrimSpace(lease.Metadata["runtime_enrollment_status"]),
	}
	if resp == nil {
		return out
	}
	out.TenantID = firstNonEmpty(resp.TenantID, out.TenantID)
	out.LeaseID = firstNonEmpty(resp.LeaseID, out.LeaseID)
	out.Action = resp.Action
	out.RuntimeOfferingID = firstRuntimeOfferingID(resp.OfferingID, out.RuntimeOfferingID)
	out.DesiredState = firstNonEmpty(resp.DesiredState, out.DesiredState)
	out.ObservedState = strings.TrimSpace(resp.ObservedState)
	out.LeaseState = strings.TrimSpace(resp.LeaseState)
	out.LeaseReason = strings.TrimSpace(resp.LeaseReason)
	if lease.CancelledAt != nil {
		out.DesiredState = string(lease.DesiredState)
		if out.ObservedState != runtimeObservedStateReconciliationPending {
			out.LeaseState = leaseStateCancelled
		}
	}
	out.SSHEnabled = resp.SSHEnabled
	out.Status = resp.Status
	out.SSH = resp.SSH
	return out
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func firstRuntimeOfferingID(values ...serverruntime.RuntimeOfferingID) serverruntime.RuntimeOfferingID {
	for _, value := range values {
		if strings.TrimSpace(string(value)) != "" {
			return value
		}
	}
	return ""
}
