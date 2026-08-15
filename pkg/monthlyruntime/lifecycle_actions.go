package monthlyruntime

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/kombifyio/techstack/internal/runtimeproduct/serverruntime"
	"github.com/kombifyio/techstack/internal/runtimeproduct/vmlease"
	"github.com/kombifyio/techstack/pkg/vmleases"
)

var (
	// ErrDecommissionBlockedUnreachable is returned when a (non-forced)
	// decommission cannot be confirmed because the runtime stayed unreachable
	// across retries. The route layer maps it to 409 + a force offer.
	ErrDecommissionBlockedUnreachable = errors.New("monthlyruntime: decommission blocked, runtime unreachable")
	// ErrReconciliationUnavailable is returned when a force decommission is
	// requested but no reconciliation enqueuer is wired — forcing without one
	// would cancel the lease and leak the provider VM.
	ErrReconciliationUnavailable = errors.New("monthlyruntime: force decommission unavailable, reconciliation not configured")
)

const (
	runtimeObservedStateReconciliationPending = "reconciliation_pending"
	leaseStateCancelled                       = "cancelled"
)

// ReconciliationEnqueuer schedules a background job to reconcile (tear down) a
// provider resource whose lease was force-canceled without a confirmed runtime
// decommission, so the underlying VM does not keep billing. Implementations must
// be defensive: a reconciliation job only tears down a lease it finds
// force-canceled.
type ReconciliationEnqueuer interface {
	EnqueueProviderReconciliation(ctx context.Context, req ReconciliationRequest) error
}

// DurableReconciliationEnqueuer is the capability required before force may
// cancel a lease. Merely accepting an in-process queue item is not sufficient:
// custody has to survive process loss and fence concurrent workers. The
// production adapter currently reports false until that contract exists.
type DurableReconciliationEnqueuer interface {
	ReconciliationEnqueuer
	DurableReconciliationReady() bool
}

func durableReconciliationEnqueuer(candidate ReconciliationEnqueuer) (DurableReconciliationEnqueuer, bool) {
	reconciler, ok := candidate.(DurableReconciliationEnqueuer)
	return reconciler, ok && reconciler.DurableReconciliationReady()
}

// ForceDecommissionReady reports whether forced decommission can establish
// durable, generation-bound provider reconciliation custody. Route/UI callers
// use the same capability check as the action path so they never offer an
// operation that production will deterministically reject.
func (s *Service) ForceDecommissionReady() bool {
	if s == nil {
		return false
	}
	_, ready := durableReconciliationEnqueuer(s.Reconcile)
	return ready
}

// ReconciliationRequest identifies the lease/stack to reconcile after a forced
// decommission.
type ReconciliationRequest struct {
	TenantID                 string
	OwnerID                  string
	LeaseID                  string
	StackID                  string
	ResourceGenerationDigest string
	Reason                   string
}

// IsUnreachableRuntimeError reports whether err indicates the runtime host could
// not be reached (network/host-level), as opposed to an application-level
// rejection. It is evaluated ONLY after runtimeActionWithRetry has exhausted
// its transient retries, so a match means the runtime stayed unreachable and a
// decommission cannot be confirmed against it. It matches only connectivity
// signals, never generic application errors, so a plain 500/validation failure
// still surfaces as a real error rather than a spurious force offer. No
// rejected runtime response is treated as a successful decommission.
func IsUnreachableRuntimeError(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	for _, pattern := range []string{
		"connection refused",
		"connection reset",
		"no route to host",
		"network is unreachable",
		"host is unreachable",
		"dial tcp",
		"no such host",
		"i/o timeout",
		"context deadline exceeded",
		"timeout awaiting response headers",
		"tls handshake timeout",
		"eof",
	} {
		if strings.Contains(text, pattern) {
			return true
		}
	}
	return false
}

// decommissionShortCircuit handles the decommission cases that do NOT run a
// runtime action: an already-canceled lease (idempotent) and a forced
// decommission (cancel + reconcile without contacting the runtime). Force is
// evaluated first so an exact failed/pending claim can be explicitly
// redispatched instead of being mistaken for terminal success. handled is
// true when it produced a terminal result; Action falls through to the normal
// runtime path otherwise.
func (s *Service) decommissionShortCircuit(ctx context.Context, tenantID string, lease *vmlease.Lease, offeringID serverruntime.RuntimeOfferingID, req ActionRequest) (*RuntimeResponse, bool, error) {
	if req.Action != serverruntime.RuntimeActionDecommission {
		return nil, false, nil
	}
	if _, ready := durableReconciliationEnqueuer(s.Reconcile); ready {
		resp, err := s.enqueueNativeDecommission(ctx, tenantID, lease, offeringID, req)
		return resp, true, err
	}
	if lease.CancelledAt != nil && !req.ReconcileClaimedDecommission {
		actor := strings.TrimSpace(req.UserID)
		resp := cancelledDecommissionResponse(tenantID, *lease, offeringID, req.Action)
		logRuntimeAction(tenantID, lease.ID, actor, req.Action, resp, nil)
		if err := s.recordRuntimeAction(ctx, tenantID, lease.ID, actor, req.Action, nil); err != nil {
			return nil, true, err
		}
		return publicRuntimeResponse(resp, *lease), true, nil
	}
	return nil, false, nil
}

// decommissionResultError maps a failed decommission runtime action to the
// blocked-unreachable sentinel (409 at the route) when the runtime stayed
// unreachable across retries; every other error passes through unchanged.
func decommissionResultError(action serverruntime.RuntimeAction, err error) error {
	if action == serverruntime.RuntimeActionDecommission && IsUnreachableRuntimeError(err) {
		return ErrDecommissionBlockedUnreachable
	}
	return err
}

// enqueueNativeDecommission transfers exact, generation-bound teardown custody
// to Techstack provider control. Both ordinary and forced decommission use this
// path in production: contacting a node-side runtime cannot prove that the
// provider resource and billable capacity are gone. Reconcile first persists
// the native generation-bound teardown operation (or proves terminal absence);
// the lease patch below mirrors its accepted state into the public runtime
// projection and operation journal.
func (s *Service) enqueueNativeDecommission(ctx context.Context, tenantID string, lease *vmlease.Lease, offeringID serverruntime.RuntimeOfferingID, req ActionRequest) (*RuntimeResponse, error) {
	reconciler, ready := durableReconciliationEnqueuer(s.Reconcile)
	if !ready {
		return nil, ErrReconciliationUnavailable
	}
	actor := strings.TrimSpace(req.UserID)
	claimDigest := strings.TrimSpace(lease.Metadata[vmleases.MetadataKeyDecommissionClaimDigest])
	currentDigest, err := vmleases.ResourceGenerationDigest(tenantID, *lease)
	if err != nil || claimDigest == "" || claimDigest != currentDigest {
		return nil, vmleases.ErrResourceGenerationSuperseded
	}
	reason := "owner_requested_decommission"
	if req.Force {
		reason = "force_decommission_unreachable"
	}
	if enqueueErr := reconciler.EnqueueProviderReconciliation(ctx, ReconciliationRequest{
		TenantID:                 tenantID,
		OwnerID:                  strings.TrimSpace(lease.Subject.ID),
		LeaseID:                  string(lease.ID),
		StackID:                  strings.TrimSpace(lease.Metadata["stack_id"]),
		ResourceGenerationDigest: claimDigest,
		Reason:                   reason,
	}); enqueueErr != nil {
		return nil, fmt.Errorf("monthlyruntime: provider decommission reconciliation enqueue failed: %w", enqueueErr)
	}
	metadata := map[string]string{
		"provider_decommission_requested_at": time.Now().UTC().Format(time.RFC3339),
		"provider_decommission_requested_by": actor,
		"provider_reconciliation_status":     EnrollmentStatusPending,
		"runtime_observed_state":             runtimeObservedStateReconciliationPending,
	}
	if req.Force {
		metadata["force_decommission_requested_at"] = metadata["provider_decommission_requested_at"]
		metadata["force_decommission_requested_by"] = actor
	}
	patched, err := s.Leases.Patch(ctx, tenantID, lease.ID, vmleases.PatchRequest{
		Cancel:                           lease.CancelledAt == nil,
		ExpectedResourceGenerationDigest: claimDigest,
		Metadata:                         metadata,
	})
	if err != nil {
		return nil, err
	}
	// Native provider control may already have cancelled the indexed lease in
	// the same transaction that persisted its provider operation. Patch applies
	// an idempotent cancel first, then metadata separately because a cancelled
	// lease short-circuits another Cancel request by design.
	patched, err = s.Leases.Patch(ctx, tenantID, lease.ID, vmleases.PatchRequest{
		ExpectedResourceGenerationDigest: claimDigest,
		Metadata:                         metadata,
	})
	if err != nil {
		return nil, err
	}
	lease = patched
	resp := cancelledDecommissionResponse(tenantID, *lease, offeringID, req.Action)
	logRuntimeAction(tenantID, lease.ID, actor, req.Action, resp, nil)
	if recErr := s.recordReconciliationPending(ctx, tenantID, lease.ID, actor, claimDigest); recErr != nil {
		return nil, recErr
	}
	return publicRuntimeResponse(resp, *lease), nil
}

// Reconnect re-runs the enrollment probe for a managed runtime, converging its
// runtime_enrollment_status back toward enrolled. It is the recovery path
// surfaced when a runtime is Stalled. It is idempotent: a probe that proves a
// live connection marks the lease enrolled (a no-op when already enrolled);
// an inconclusive probe keeps the runtime stalled.
func (s *Service) Reconnect(ctx context.Context, req ActionRequest) (*RuntimeResponse, error) {
	if s == nil || s.Leases == nil {
		return nil, vmleases.ErrEnrollmentRequired
	}
	if s.Runtime == nil {
		return nil, ErrRuntimeClient
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
	if !canAccessLease(*lease, strings.TrimSpace(req.UserID), tenantID) {
		return nil, ErrForbidden
	}
	if !record.NativeActive() {
		return nil, ErrExecutionAuthorityInactive
	}
	// Reconnect must work on a not-yet-enrolled (stalled) lease, so resolve the
	// offering directly instead of via validateMonthlyRuntimeLease (which gates
	// non-decommission actions on enrolled status).
	if !IsMonthlyRuntimeMetadata(lease.Metadata) {
		return nil, ErrInvalidLease
	}
	offeringID := OfferingIDFromMetadata(lease.Metadata)
	if _, ok := OfferingByID(offeringID); !ok {
		return nil, ErrInvalidLease
	}
	actionCtx, cancel := context.WithTimeout(ctx, runtimeActionTimeout)
	defer cancel()
	resp, err := s.Runtime.RuntimeAction(actionCtx, serverruntime.LeaseRuntimeActionRequest{
		TenantID:   tenantID,
		OwnerID:    strings.TrimSpace(lease.Subject.ID),
		LeaseID:    string(lease.ID),
		Action:     serverruntime.RuntimeActionStatus,
		OfferingID: offeringID,
		Metadata:   cloneMetadata(lease.Metadata),
	})
	if err != nil {
		return nil, err
	}
	if !reconnectProvesEnrollment(resp) {
		return nil, ErrEnrollmentPending
	}
	// The probe proved connection: converge enrollment to enrolled (idempotent).
	if strings.TrimSpace(lease.Metadata["runtime_enrollment_status"]) != enrollmentStatusEnrolled {
		patched, perr := s.Leases.Patch(ctx, tenantID, lease.ID, vmleases.PatchRequest{
			Metadata: map[string]string{"runtime_enrollment_status": enrollmentStatusEnrolled},
		})
		if perr != nil {
			return nil, perr
		}
		if patched == nil {
			return nil, ErrEnrollmentPending
		}
		lease = patched
	}
	return publicRuntimeResponse(resp, *lease), nil
}

func reconnectProvesEnrollment(resp *serverruntime.LeaseRuntimeActionResponse) bool {
	if resp == nil {
		return false
	}
	if strings.EqualFold(strings.TrimSpace(resp.Metadata["connection_state"]), "connected") {
		return true
	}
	states := []string{resp.ObservedState}
	if resp.Status != nil {
		states = append(states, resp.Status.State)
	}
	for _, state := range states {
		switch strings.ToLower(strings.TrimSpace(state)) {
		case "connected", "healthy", "running":
			return true
		}
	}
	return false
}
