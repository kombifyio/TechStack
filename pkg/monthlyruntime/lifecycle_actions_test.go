package monthlyruntime

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kombifyio/techstack/internal/runtimeproduct/serverruntime"
	"github.com/kombifyio/techstack/internal/runtimeproduct/vmlease"
	"github.com/kombifyio/techstack/pkg/vmleases"
)

type fakeReconciler struct {
	calls   []ReconciliationRequest
	err     error
	durable bool
}

type blockingRuntimeClient struct {
	requests []serverruntime.LeaseRuntimeActionRequest
}

type failingReconnectPatchLeaseService struct {
	*nativeActiveLeaseService
	err error
}

func (s *failingReconnectPatchLeaseService) Patch(context.Context, string, vmlease.LeaseID, vmleases.PatchRequest) (*vmlease.Lease, error) {
	return nil, s.err
}

func (f *blockingRuntimeClient) RuntimeAction(ctx context.Context, req serverruntime.LeaseRuntimeActionRequest) (*serverruntime.LeaseRuntimeActionResponse, error) {
	f.requests = append(f.requests, req)
	<-ctx.Done()
	return nil, ctx.Err()
}

func (f *fakeReconciler) EnqueueProviderReconciliation(_ context.Context, req ReconciliationRequest) error {
	f.calls = append(f.calls, req)
	return f.err
}

func (f *fakeReconciler) DurableReconciliationReady() bool { return f.durable }

func TestServiceForceDecommissionReadyReflectsDurableCustody(t *testing.T) {
	for _, tc := range []struct {
		name       string
		reconciler ReconciliationEnqueuer
		want       bool
	}{
		{name: "missing reconciler", want: false},
		{name: "non-durable reconciler", reconciler: &fakeReconciler{}, want: false},
		{name: "durable reconciler", reconciler: &fakeReconciler{durable: true}, want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := &Service{Reconcile: tc.reconciler}
			if got := svc.ForceDecommissionReady(); got != tc.want {
				t.Fatalf("ForceDecommissionReady() = %v, want %v", got, tc.want)
			}
		})
	}
	var nilService *Service
	if nilService.ForceDecommissionReady() {
		t.Fatal("nil service must not report force decommission ready")
	}
}

func newLeaseServiceWith(t *testing.T, now time.Time, status string) *vmleases.Service {
	t.Helper()
	leases := vmleases.NewService(vmleases.NewMemoryStore(), vmleases.ServiceConfig{Now: func() time.Time { return now }, SnapshotSecret: []byte("secret")})
	if _, err := leases.CreateOrUpdate(context.Background(), vmleases.CreateRequest{Lease: testMonthlyLease(now, status)}); err != nil {
		t.Fatalf("CreateOrUpdate: %v", err)
	}
	return leases
}

func TestServiceDecommissionUnreachableIsBlockedWithoutForce(t *testing.T) {
	now := time.Date(2026, 5, 12, 12, 0, 0, 0, time.UTC)
	leases := newLeaseServiceWith(t, now, enrollmentStatusEnrolled)
	// "no route to host" is unreachable but NOT transient, so no retry delay.
	runtime := &fakeRuntimeClient{err: errors.New("dial tcp 185.64.0.1:8443: connect: no route to host")}
	svc := &Service{Leases: nativeLeaseService(leases), Runtime: runtime, Features: fakeFeatureChecker{enabled: true}}

	_, err := svc.Action(context.Background(), ActionRequest{
		TenantID: "org-1", UserID: "user-1", LeaseID: "lease-1",
		Action: serverruntime.RuntimeActionDecommission,
	})
	if !errors.Is(err, ErrDecommissionBlockedUnreachable) {
		t.Fatalf("err = %v, want ErrDecommissionBlockedUnreachable", err)
	}
	stored, err := leases.Get(context.Background(), "org-1", "lease-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if stored.CancelledAt != nil {
		t.Fatal("lease must NOT be canceled when unreachable decommission is blocked")
	}
	if len(runtime.requests) == 0 {
		t.Fatal("runtime should have been contacted before blocking")
	}
}

func TestServiceDecommissionTimeoutIsBlockedBeforeEdgeTimeout(t *testing.T) {
	oldTimeout := decommissionRuntimeActionTimeout
	oldDelays := decommissionRuntimeRetryDelays
	decommissionRuntimeActionTimeout = 20 * time.Millisecond
	decommissionRuntimeRetryDelays = nil
	t.Cleanup(func() {
		decommissionRuntimeActionTimeout = oldTimeout
		decommissionRuntimeRetryDelays = oldDelays
	})

	now := time.Date(2026, 5, 12, 12, 0, 0, 0, time.UTC)
	leases := newLeaseServiceWith(t, now, enrollmentStatusEnrolled)
	runtime := &blockingRuntimeClient{}
	svc := &Service{Leases: nativeLeaseService(leases), Runtime: runtime, Features: fakeFeatureChecker{enabled: true}}

	startedAt := time.Now()
	_, err := svc.Action(context.Background(), ActionRequest{
		TenantID: "org-1", UserID: "user-1", LeaseID: "lease-1",
		Action: serverruntime.RuntimeActionDecommission,
	})
	if !errors.Is(err, ErrDecommissionBlockedUnreachable) {
		t.Fatalf("err = %v, want ErrDecommissionBlockedUnreachable", err)
	}
	if elapsed := time.Since(startedAt); elapsed > time.Second {
		t.Fatalf("decommission elapsed = %s, want bounded timeout", elapsed)
	}
	if len(runtime.requests) != 1 {
		t.Fatalf("runtime requests = %d, want 1", len(runtime.requests))
	}
	stored, err := leases.Get(context.Background(), "org-1", "lease-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if stored.CancelledAt != nil {
		t.Fatal("lease must NOT be canceled on timeout; caller should force explicitly")
	}
}

func TestServiceForceDecommissionCancelsLeaseAndEnqueuesReconciliation(t *testing.T) {
	now := time.Date(2026, 5, 12, 12, 0, 0, 0, time.UTC)
	leases := newLeaseServiceWith(t, now, enrollmentStatusEnrolled)
	runtime := &fakeRuntimeClient{err: errors.New("dial tcp: no route to host")}
	reconciler := &fakeReconciler{durable: true}
	svc := &Service{Leases: nativeLeaseService(leases), Runtime: runtime, Features: fakeFeatureChecker{enabled: true}, Reconcile: reconciler}

	resp, err := svc.Action(context.Background(), ActionRequest{
		TenantID: "org-1", UserID: "user-1", LeaseID: "lease-1",
		Action: serverruntime.RuntimeActionDecommission, Force: true,
	})
	if err != nil {
		t.Fatalf("force decommission err = %v", err)
	}
	if resp == nil {
		t.Fatal("force decommission returned nil response")
	}
	if len(runtime.requests) != 0 {
		t.Fatalf("force decommission must NOT contact the runtime, got %d calls", len(runtime.requests))
	}
	if len(reconciler.calls) != 1 {
		t.Fatalf("reconciler called %d times, want exactly 1 (leak safety)", len(reconciler.calls))
	}
	if reconciler.calls[0].LeaseID != "lease-1" {
		t.Errorf("reconciliation lease = %q, want lease-1", reconciler.calls[0].LeaseID)
	}
	if reconciler.calls[0].ResourceGenerationDigest == "" {
		t.Fatal("reconciliation request is missing the claimed resource generation digest")
	}
	stored, err := leases.Get(context.Background(), "org-1", "lease-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if stored.CancelledAt == nil {
		t.Fatal("force decommission must cancel the lease")
	}
	if stored.Metadata["force_decommission_requested_at"] == "" {
		t.Error("force_decommission_requested_at metadata not set")
	}
	if stored.Metadata["force_decommission_requested_by"] != "user-1" {
		t.Errorf("force_decommission_requested_by = %q, want user-1", stored.Metadata["force_decommission_requested_by"])
	}
	if resp.ObservedState != runtimeObservedStateReconciliationPending || resp.LeaseState != runtimeObservedStateReconciliationPending {
		t.Fatalf("force response = %#v, want honest reconciliation_pending state", resp)
	}
	wantDigest, err := vmleases.ResourceGenerationDigest("org-1", *stored)
	if err != nil {
		t.Fatalf("ResourceGenerationDigest: %v", err)
	}
	if got := reconciler.calls[0].ResourceGenerationDigest; got != wantDigest || stored.Metadata[vmleases.MetadataKeyDecommissionClaimDigest] != wantDigest {
		t.Fatalf("force generation binding: request=%q claim=%q want=%q", got, stored.Metadata[vmleases.MetadataKeyDecommissionClaimDigest], wantDigest)
	}
	events, err := leases.ListOperations(context.Background(), "org-1", "lease-1", 10)
	if err != nil {
		t.Fatalf("ListOperations: %v", err)
	}
	if len(events) != 1 || events[0].EventType != vmleases.OperationEventDecommission || events[0].Status != vmleases.OperationStatusPending || events[0].ResourceGenerationDigest != wantDigest {
		t.Fatalf("journal = %+v, force must record pending exact reconciliation rather than provider success", events)
	}
}

func TestServiceForceDecommissionAbortsWhenEnqueueFails(t *testing.T) {
	now := time.Date(2026, 5, 12, 12, 0, 0, 0, time.UTC)
	leases := newLeaseServiceWith(t, now, enrollmentStatusEnrolled)
	reconciler := &fakeReconciler{err: errors.New("queue full"), durable: true}
	svc := &Service{Leases: nativeLeaseService(leases), Runtime: &fakeRuntimeClient{}, Features: fakeFeatureChecker{enabled: true}, Reconcile: reconciler}

	_, err := svc.Action(context.Background(), ActionRequest{
		TenantID: "org-1", UserID: "user-1", LeaseID: "lease-1",
		Action: serverruntime.RuntimeActionDecommission, Force: true,
	})
	if err == nil {
		t.Fatal("force decommission should fail when reconciliation enqueue fails")
	}
	stored, err := leases.Get(context.Background(), "org-1", "lease-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if stored.CancelledAt != nil {
		t.Fatal("lease must NOT be canceled when reconciliation enqueue fails (VM leak safety)")
	}
}

func TestServiceForceDecommissionRedispatchesExactClaimOnCanceledLease(t *testing.T) {
	now := time.Date(2026, 5, 12, 12, 0, 0, 0, time.UTC)
	leases := newLeaseServiceWith(t, now, enrollmentStatusEnrolled)
	reconciler := &fakeReconciler{durable: true}
	svc := &Service{Leases: nativeLeaseService(leases), Runtime: &fakeRuntimeClient{}, Features: fakeFeatureChecker{enabled: true}, Reconcile: reconciler}

	first, err := svc.Action(context.Background(), ActionRequest{
		TenantID: "org-1", UserID: "user-1", LeaseID: "lease-1",
		Action: serverruntime.RuntimeActionDecommission, Force: true,
	})
	if err != nil {
		t.Fatalf("first force: %v", err)
	}
	digest := reconciler.calls[0].ResourceGenerationDigest
	second, err := svc.Action(context.Background(), ActionRequest{
		TenantID: "org-1", UserID: "user-1", LeaseID: "lease-1",
		Action: serverruntime.RuntimeActionDecommission, Force: true,
		ExpectedResourceGenerationDigest: digest,
	})
	if err != nil {
		t.Fatalf("redispatch force: %v", err)
	}
	if len(reconciler.calls) != 2 {
		t.Fatalf("reconciler calls = %d, want exact redispatch", len(reconciler.calls))
	}
	if reconciler.calls[1].LeaseID != "lease-1" || reconciler.calls[1].ResourceGenerationDigest != digest {
		t.Fatalf("redispatch = %#v, want same exact lease/digest", reconciler.calls[1])
	}
	if first.ObservedState != runtimeObservedStateReconciliationPending || second.ObservedState != runtimeObservedStateReconciliationPending ||
		first.LeaseState != runtimeObservedStateReconciliationPending || second.LeaseState != runtimeObservedStateReconciliationPending {
		t.Fatalf("responses first=%#v second=%#v, want pending not terminal success", first, second)
	}
}

func TestServiceForceDecommissionFailsClosedWhenCustodyIsNotDurable(t *testing.T) {
	now := time.Date(2026, 5, 12, 12, 0, 0, 0, time.UTC)
	leases := newLeaseServiceWith(t, now, enrollmentStatusEnrolled)
	reconciler := &fakeReconciler{}
	svc := &Service{Leases: nativeLeaseService(leases), Runtime: &fakeRuntimeClient{}, Features: fakeFeatureChecker{enabled: true}, Reconcile: reconciler}

	_, err := svc.Action(context.Background(), ActionRequest{
		TenantID: "org-1", UserID: "user-1", LeaseID: "lease-1",
		Action: serverruntime.RuntimeActionDecommission, Force: true,
	})
	if !errors.Is(err, ErrReconciliationUnavailable) {
		t.Fatalf("err = %v, want ErrReconciliationUnavailable", err)
	}
	if len(reconciler.calls) != 0 {
		t.Fatalf("unsafe reconciler calls = %d, want zero", len(reconciler.calls))
	}
	stored, getErr := leases.Get(context.Background(), "org-1", "lease-1")
	if getErr != nil {
		t.Fatalf("Get: %v", getErr)
	}
	if stored.CancelledAt != nil {
		t.Fatal("unsafe custody must not cancel the lease")
	}
	if stored.Metadata["runtime_observed_state"] == runtimeObservedStateReconciliationPending || stored.Metadata["provider_reconciliation_status"] != "" {
		t.Fatalf("unsafe custody mutated reconciliation metadata: %#v", stored.Metadata)
	}
}

func TestServiceForceDecommissionRefusedWithoutReconciler(t *testing.T) {
	now := time.Date(2026, 5, 12, 12, 0, 0, 0, time.UTC)
	leases := newLeaseServiceWith(t, now, enrollmentStatusEnrolled)
	svc := &Service{Leases: nativeLeaseService(leases), Runtime: &fakeRuntimeClient{}, Features: fakeFeatureChecker{enabled: true}} // no Reconcile

	_, err := svc.Action(context.Background(), ActionRequest{
		TenantID: "org-1", UserID: "user-1", LeaseID: "lease-1",
		Action: serverruntime.RuntimeActionDecommission, Force: true,
	})
	if !errors.Is(err, ErrReconciliationUnavailable) {
		t.Fatalf("err = %v, want ErrReconciliationUnavailable", err)
	}
}

func TestServiceReconnectConvergesToEnrolledAndIsIdempotent(t *testing.T) {
	now := time.Date(2026, 5, 12, 12, 0, 0, 0, time.UTC)
	leases := newLeaseServiceWith(t, now, EnrollmentStatusPending)
	runtime := &fakeRuntimeClient{response: &serverruntime.LeaseRuntimeActionResponse{
		ObservedState: "running",
		Status:        &serverruntime.NodeStatus{ID: "node-1", State: "running"},
		Metadata:      map[string]string{"connection_state": "connected"},
	}}
	svc := &Service{Leases: nativeLeaseService(leases), Runtime: runtime, Features: fakeFeatureChecker{enabled: true}}

	resp, err := svc.Reconnect(context.Background(), ActionRequest{TenantID: "org-1", UserID: "user-1", LeaseID: "lease-1"})
	if err != nil {
		t.Fatalf("Reconnect err = %v", err)
	}
	if resp.EnrollmentStatus != enrollmentStatusEnrolled {
		t.Fatalf("EnrollmentStatus = %q, want enrolled after successful reconnect", resp.EnrollmentStatus)
	}
	if len(runtime.requests) == 0 || runtime.requests[0].Action != serverruntime.RuntimeActionStatus {
		t.Fatalf("reconnect must probe with a status action, got %+v", runtime.requests)
	}
	// Idempotent: a second reconnect on the now-enrolled lease still succeeds.
	resp2, err := svc.Reconnect(context.Background(), ActionRequest{TenantID: "org-1", UserID: "user-1", LeaseID: "lease-1"})
	if err != nil {
		t.Fatalf("second Reconnect err = %v", err)
	}
	if resp2.EnrollmentStatus != enrollmentStatusEnrolled {
		t.Fatalf("second reconnect EnrollmentStatus = %q, want enrolled", resp2.EnrollmentStatus)
	}
}

func TestServiceReconnectRejectsInconclusiveSuccessfulProbe(t *testing.T) {
	now := time.Date(2026, 5, 12, 12, 0, 0, 0, time.UTC)
	leases := newLeaseServiceWith(t, now, EnrollmentStatusPending)
	runtime := &fakeRuntimeClient{response: &serverruntime.LeaseRuntimeActionResponse{
		ObservedState: "offline",
		Status:        &serverruntime.NodeStatus{ID: "node-1", State: "offline"},
		Metadata:      map[string]string{"connection_state": "offline"},
	}}
	svc := &Service{Leases: nativeLeaseService(leases), Runtime: runtime, Features: fakeFeatureChecker{enabled: true}}

	if _, err := svc.Reconnect(context.Background(), ActionRequest{TenantID: "org-1", UserID: "user-1", LeaseID: "lease-1"}); !errors.Is(err, ErrEnrollmentPending) {
		t.Fatalf("Reconnect error = %v, want ErrEnrollmentPending", err)
	}
	stored, err := leases.Get(context.Background(), "org-1", "lease-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if stored.Metadata["runtime_enrollment_status"] != EnrollmentStatusPending {
		t.Fatalf("enrollment status = %q, want pending", stored.Metadata["runtime_enrollment_status"])
	}
}

func TestServiceReconnectReturnsPatchFailure(t *testing.T) {
	now := time.Date(2026, 5, 12, 12, 0, 0, 0, time.UTC)
	leases := newLeaseServiceWith(t, now, EnrollmentStatusPending)
	patchErr := errors.New("persist enrollment")
	authority := &failingReconnectPatchLeaseService{nativeActiveLeaseService: nativeLeaseService(leases), err: patchErr}
	runtime := &fakeRuntimeClient{response: &serverruntime.LeaseRuntimeActionResponse{
		ObservedState: "running",
		Metadata:      map[string]string{"connection_state": "connected"},
	}}
	svc := &Service{Leases: authority, Runtime: runtime, Features: fakeFeatureChecker{enabled: true}}

	if _, err := svc.Reconnect(context.Background(), ActionRequest{TenantID: "org-1", UserID: "user-1", LeaseID: "lease-1"}); !errors.Is(err, patchErr) {
		t.Fatalf("Reconnect error = %v, want patch failure", err)
	}
	stored, err := leases.Get(context.Background(), "org-1", "lease-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if stored.Metadata["runtime_enrollment_status"] != EnrollmentStatusPending {
		t.Fatalf("enrollment status = %q, want pending", stored.Metadata["runtime_enrollment_status"])
	}
}

func TestServiceReconnectReturnsErrorWhenProbeFails(t *testing.T) {
	now := time.Date(2026, 5, 12, 12, 0, 0, 0, time.UTC)
	leases := newLeaseServiceWith(t, now, EnrollmentStatusPending)
	runtime := &fakeRuntimeClient{err: errors.New("dial tcp: no route to host")}
	svc := &Service{Leases: nativeLeaseService(leases), Runtime: runtime, Features: fakeFeatureChecker{enabled: true}}

	if _, err := svc.Reconnect(context.Background(), ActionRequest{TenantID: "org-1", UserID: "user-1", LeaseID: "lease-1"}); err == nil {
		t.Fatal("Reconnect should return the probe error when the runtime is unreachable")
	}
	stored, err := leases.Get(context.Background(), "org-1", "lease-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if stored.Metadata["runtime_enrollment_status"] != EnrollmentStatusPending {
		t.Errorf("enrollment status changed to %q on a failed probe, want pending", stored.Metadata["runtime_enrollment_status"])
	}
}
