package monthlyruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/kombifyio/techstack/internal/runtimeproduct/serverruntime"
	"github.com/kombifyio/techstack/internal/runtimeproduct/vmlease"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/identity"
	"github.com/kombifyio/techstack/pkg/runtimeidentity"
	"github.com/kombifyio/techstack/pkg/vmleases"
)

type fakeRuntimeClient struct {
	requests []serverruntime.LeaseRuntimeActionRequest
	err      error
	response *serverruntime.LeaseRuntimeActionResponse
	onAction func(serverruntime.LeaseRuntimeActionRequest) error
}

func (f *fakeRuntimeClient) RuntimeAction(_ context.Context, req serverruntime.LeaseRuntimeActionRequest) (*serverruntime.LeaseRuntimeActionResponse, error) {
	f.requests = append(f.requests, req)
	if f.onAction != nil {
		if err := f.onAction(req); err != nil {
			return nil, err
		}
	}
	if f.err != nil {
		return nil, f.err
	}
	if f.response != nil {
		resp := *f.response
		if resp.TenantID == "" {
			resp.TenantID = req.TenantID
		}
		if resp.LeaseID == "" {
			resp.LeaseID = req.LeaseID
		}
		if resp.Action == "" {
			resp.Action = req.Action
		}
		if resp.OfferingID == "" {
			resp.OfferingID = req.OfferingID
		}
		return &resp, nil
	}
	return &serverruntime.LeaseRuntimeActionResponse{
		TenantID:     req.TenantID,
		LeaseID:      req.LeaseID,
		Action:       req.Action,
		OfferingID:   req.OfferingID,
		ProviderID:   ProviderCentron,
		ProfileID:    "centron-managed-pvm-monthly",
		EngineVMID:   "engine-1",
		DesiredState: "stopped",
		Status:       &serverruntime.NodeStatus{ID: "node-1", State: "stopped"},
		Metadata:     map[string]string{"simulate_provider_id": ProviderCentron},
	}, nil
}

type sequenceRuntimeClient struct {
	requests []serverruntime.LeaseRuntimeActionRequest
	errs     []error
}

func (f *sequenceRuntimeClient) RuntimeAction(_ context.Context, req serverruntime.LeaseRuntimeActionRequest) (*serverruntime.LeaseRuntimeActionResponse, error) {
	f.requests = append(f.requests, req)
	if len(f.errs) > 0 {
		err := f.errs[0]
		f.errs = f.errs[1:]
		if err != nil {
			return nil, err
		}
	}
	return &serverruntime.LeaseRuntimeActionResponse{
		TenantID:      req.TenantID,
		LeaseID:       req.LeaseID,
		Action:        req.Action,
		OfferingID:    req.OfferingID,
		ProviderID:    ProviderIONOS,
		EngineVMID:    "engine-1",
		DesiredState:  "stopped",
		ObservedState: "not_found",
		LeaseState:    leaseStateCancelled,
		Status:        &serverruntime.NodeStatus{ID: "node-1", State: "not_found"},
	}, nil
}

type fakeFeatureChecker struct {
	enabled bool
}

func (f fakeFeatureChecker) IsEnabled(context.Context, string, string) (bool, error) {
	return f.enabled, nil
}

type recordingFeatureChecker struct {
	enabled bool
	keys    []string
	orgIDs  []string
}

type operationRecordingLeaseAuthority struct {
	LeaseAuthority
	record func(vmleases.OperationEvent) error
}

type failLeaseFinalizationOnceAuthority struct {
	*vmleases.Service
	err    error
	failed bool
}

type failingConfirmedDecommissionReader struct {
	*vmleases.Service
	err error
}

type leaseAuthorityWithoutJournal struct {
	LeaseAuthority
}

type leaseAuthorityWithoutInventory struct {
	LeaseAuthority
}

type storeWithoutOperationJournal struct {
	vmleases.Store
}

type nativeActiveLeaseService struct {
	*vmleases.Service
}

type inventoryStateLeaseService struct {
	*vmleases.Service
	authority vmleases.LeaseExecutionAuthority
	state     vmleases.LeaseAuthorityState
}

func nativeLeaseService(service *vmleases.Service) *nativeActiveLeaseService {
	return &nativeActiveLeaseService{Service: service}
}

func nativeInventoryRecord(lease *vmlease.Lease, err error) (*vmleases.LeaseInventoryRecord, error) {
	if err != nil {
		return nil, err
	}
	// Mirror the production custody predicate: only a running lease is
	// under active provider control.
	state := vmleases.LeaseAuthorityStateNativeInactive
	if lease.DesiredState == vmlease.DesiredStateRunning {
		state = vmleases.LeaseAuthorityStateNativeActive
	}
	return &vmleases.LeaseInventoryRecord{
		Lease:              *lease,
		ExecutionAuthority: vmleases.LeaseExecutionAuthorityTechStackProviderControl,
		AuthorityState:     state,
	}, nil
}

func (s *nativeActiveLeaseService) GetInventory(ctx context.Context, tenantID string, id vmlease.LeaseID) (*vmleases.LeaseInventoryRecord, error) {
	return nativeInventoryRecord(s.Get(ctx, tenantID, id))
}

func (s *inventoryStateLeaseService) GetInventory(ctx context.Context, tenantID string, id vmlease.LeaseID) (*vmleases.LeaseInventoryRecord, error) {
	lease, err := s.Get(ctx, tenantID, id)
	if err != nil {
		return nil, err
	}
	return &vmleases.LeaseInventoryRecord{Lease: *lease, ExecutionAuthority: s.authority, AuthorityState: s.state}, nil
}

func (a *operationRecordingLeaseAuthority) GetInventory(ctx context.Context, tenantID string, id vmlease.LeaseID) (*vmleases.LeaseInventoryRecord, error) {
	return nativeInventoryRecord(a.Get(ctx, tenantID, id))
}

func (a *failLeaseFinalizationOnceAuthority) GetInventory(ctx context.Context, tenantID string, id vmlease.LeaseID) (*vmleases.LeaseInventoryRecord, error) {
	return nativeInventoryRecord(a.Get(ctx, tenantID, id))
}

func (a *failingConfirmedDecommissionReader) GetInventory(ctx context.Context, tenantID string, id vmlease.LeaseID) (*vmleases.LeaseInventoryRecord, error) {
	return nativeInventoryRecord(a.Get(ctx, tenantID, id))
}

func (a *leaseAuthorityWithoutJournal) GetInventory(ctx context.Context, tenantID string, id vmlease.LeaseID) (*vmleases.LeaseInventoryRecord, error) {
	return nativeInventoryRecord(a.Get(ctx, tenantID, id))
}

func (a *operationRecordingLeaseAuthority) RecordOperation(_ context.Context, event vmleases.OperationEvent) error {
	return a.record(event)
}

func (a *operationRecordingLeaseAuthority) RecordOperationStrict(_ context.Context, event vmleases.OperationEvent) error {
	return a.record(event)
}

func (a *failLeaseFinalizationOnceAuthority) Patch(ctx context.Context, tenantID string, id vmlease.LeaseID, req vmleases.PatchRequest) (*vmlease.Lease, error) {
	if req.Cancel && !a.failed {
		a.failed = true
		return nil, a.err
	}
	return a.Service.Patch(ctx, tenantID, id, req)
}

func (a *failingConfirmedDecommissionReader) HasConfirmedDecommission(context.Context, string, vmlease.LeaseID, string) (bool, error) {
	return false, a.err
}

func (f *recordingFeatureChecker) IsEnabled(ctx context.Context, key string, _ string) (bool, error) {
	f.keys = append(f.keys, key)
	if id := identity.FromContext(ctx); id != nil {
		f.orgIDs = append(f.orgIDs, id.OrgID)
	}
	return f.enabled, nil
}

func testMonthlyLease(now time.Time, status string) vmlease.Lease {
	metadata := NormalizeMetadata(map[string]string{}, serverruntime.RuntimeOfferingStandard)
	if status != "" {
		metadata["runtime_enrollment_status"] = status
	}
	return vmlease.Lease{
		ID:             "lease-1",
		Subject:        vmlease.Subject{Kind: vmlease.SubjectUser, ID: "user-1", OrgID: "org-1"},
		Resource:       vmlease.ResourceRef{ProviderID: ProviderCentron, EngineVMID: "node-1"},
		DesiredState:   vmlease.DesiredStateRunning,
		BillingMode:    vmlease.BillingModeSubscription,
		LifecycleClass: vmlease.LifecycleClassSubscription,
		RestartPolicy:  vmlease.RestartPolicyOnUnexpectedStop,
		RecreatePolicy: vmlease.RecreatePolicyManual,
		ValidFrom:      now.Add(-time.Minute),
		ValidUntil:     now.Add(time.Hour),
		RenewedAt:      now,
		Metadata:       metadata,
	}
}

func TestServiceStopPatchesLeaseAndCallsSimulateRuntime(t *testing.T) {
	now := time.Date(2026, 5, 12, 12, 0, 0, 0, time.UTC)
	leases := newLeaseServiceWith(t, now, enrollmentStatusEnrolled)
	runtime := &fakeRuntimeClient{onAction: func(serverruntime.LeaseRuntimeActionRequest) error {
		stored, err := leases.Get(context.Background(), "org-1", "lease-1")
		if err != nil {
			return err
		}
		if stored.CancelledAt != nil {
			return errors.New("runtime saw a pre-cancelled lease")
		}
		return nil
	}}
	svc := &Service{Leases: nativeLeaseService(leases), Runtime: runtime, Features: fakeFeatureChecker{enabled: true}}

	resp, err := svc.Action(context.Background(), ActionRequest{
		TenantID: "org-1",
		UserID:   "user-1",
		LeaseID:  "lease-1",
		Action:   serverruntime.RuntimeActionStop,
	})
	if err != nil {
		t.Fatalf("Action: %v", err)
	}
	if resp.Action != serverruntime.RuntimeActionStop || resp.Status == nil || resp.Status.State != "stopped" {
		t.Fatalf("response = %+v", resp)
	}
	payload, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("Marshal response: %v", err)
	}
	if text := strings.ToLower(string(payload)); strings.Contains(text, "provider") || strings.Contains(text, "centron") || strings.Contains(text, "engine_vm_id") {
		t.Fatalf("public response exposes provider internals: %s", text)
	}
	if resp.EnrollmentStatus != enrollmentStatusEnrolled {
		t.Fatalf("EnrollmentStatus = %q, want enrolled", resp.EnrollmentStatus)
	}
	if len(runtime.requests) != 1 {
		t.Fatalf("runtime requests = %d, want 1", len(runtime.requests))
	}
	got := runtime.requests[0]
	if got.Action != serverruntime.RuntimeActionStop || got.OfferingID != serverruntime.RuntimeOfferingStandard || got.Metadata[MetadataKeyProviderID] != ProviderCentron {
		t.Fatalf("runtime request = %+v", got)
	}
	stored, err := leases.Get(context.Background(), "org-1", "lease-1")
	if err != nil {
		t.Fatalf("stored lease: %v", err)
	}
	if stored.DesiredState != vmlease.DesiredStateStopped {
		t.Fatalf("DesiredState = %q, want stopped", stored.DesiredState)
	}
}

func TestServiceActionPersistsRuntimeTargetMetadata(t *testing.T) {
	now := time.Date(2026, 5, 12, 12, 0, 0, 0, time.UTC)
	leases := newLeaseServiceWith(t, now, enrollmentStatusEnrolled)
	runtime := &fakeRuntimeClient{response: &serverruntime.LeaseRuntimeActionResponse{
		ObservedState: "running",
		LeaseState:    "valid",
		Status:        &serverruntime.NodeStatus{ID: "node-1", State: "running", PublicIP: "203.0.113.44", PrivateIP: "10.0.0.9", UpdatedAt: "2026-05-12T12:01:00Z"},
		SSH:           &serverruntime.SSHInfo{Host: "203.0.113.44", User: "root", Port: 22, NodePublicIP: "203.0.113.44"},
	}}
	svc := &Service{Leases: nativeLeaseService(leases), Runtime: runtime, Features: fakeFeatureChecker{enabled: true}}

	if _, err := svc.Action(context.Background(), ActionRequest{
		TenantID: "org-1",
		UserID:   "user-1",
		LeaseID:  "lease-1",
		Action:   serverruntime.RuntimeActionSSHInfo,
	}); err != nil {
		t.Fatalf("Action: %v", err)
	}

	stored, err := leases.Get(context.Background(), "org-1", "lease-1")
	if err != nil {
		t.Fatalf("stored lease: %v", err)
	}
	want := map[string]string{
		"runtime_ssh_host":          "203.0.113.44",
		"runtime_public_ip":         "203.0.113.44",
		"runtime_private_ip":        "10.0.0.9",
		"runtime_ssh_user":          "root",
		"runtime_ssh_port":          "22",
		"runtime_observed_state":    "running",
		"runtime_lease_state":       "valid",
		"runtime_status_updated_at": "2026-05-12T12:01:00Z",
	}
	for key, value := range want {
		if stored.Metadata[key] != value {
			t.Fatalf("metadata[%s] = %q, want %q in %+v", key, stored.Metadata[key], value, stored.Metadata)
		}
	}
	if stored.Metadata["runtime_enrollment_status"] != enrollmentStatusEnrolled {
		t.Fatalf("enrollment metadata changed: %+v", stored.Metadata)
	}
}

func TestServiceActionPersistsEncryptedRuntimeCredentialsOnly(t *testing.T) {
	now := time.Date(2026, 7, 8, 9, 30, 0, 0, time.UTC)
	leases := newLeaseServiceWith(t, now, enrollmentStatusEnrolled)
	previousEncryptRuntimeCredential := encryptRuntimeCredential
	encryptRuntimeCredential = func(value string) (string, bool) {
		return "enc:v1:test-" + value, true
	}
	t.Cleanup(func() { encryptRuntimeCredential = previousEncryptRuntimeCredential })

	runtime := &fakeRuntimeClient{response: &serverruntime.LeaseRuntimeActionResponse{
		ObservedState: "running",
		LeaseState:    "valid",
		Metadata: map[string]string{ // #nosec G101 -- deterministic test fixture values, not live credentials.
			"private_key":             "raw-metadata-private-key",
			"runtime_ssh_private_key": "raw-metadata-runtime-key",
			"runtime_ssh_password":    "raw-metadata-password",
			"runtime_marker":          "kept",
		},
		Status: &serverruntime.NodeStatus{ID: "node-1", State: "running", PublicIP: "203.0.113.45"},
		SSH: &serverruntime.SSHInfo{
			Host:             "203.0.113.45",
			User:             "root",
			Port:             22,
			PrivateKey:       "ssh-private-key",
			ClientPrivateKey: "ssh-client-key",
			Password:         "ssh-password",
		},
	}}
	svc := &Service{Leases: nativeLeaseService(leases), Runtime: runtime, Features: fakeFeatureChecker{enabled: true}}

	if _, err := svc.Action(context.Background(), ActionRequest{
		TenantID: "org-1",
		UserID:   "user-1",
		LeaseID:  "lease-1",
		Action:   serverruntime.RuntimeActionSSHInfo,
	}); err != nil {
		t.Fatalf("Action: %v", err)
	}

	stored, err := leases.Get(context.Background(), "org-1", "lease-1")
	if err != nil {
		t.Fatalf("stored lease: %v", err)
	}
	if stored.Metadata["runtime_marker"] != "kept" {
		t.Fatalf("runtime_marker = %q, want kept", stored.Metadata["runtime_marker"])
	}
	for _, key := range []string{"private_key", "runtime_ssh_private_key", "runtime_ssh_password"} {
		if stored.Metadata[key] != "" {
			t.Fatalf("metadata[%s] = %q, want raw credential stripped", key, stored.Metadata[key])
		}
	}
	want := map[string]string{ // #nosec G101 -- deterministic encrypted test fixture values.
		"runtime_ssh_private_key_enc":    "enc:v1:test-ssh-private-key",
		"runtime_client_private_key_enc": "enc:v1:test-ssh-client-key",
		"runtime_ssh_password_enc":       "enc:v1:test-ssh-password",
	}
	for key, value := range want {
		if stored.Metadata[key] != value {
			t.Fatalf("metadata[%s] = %q, want %q in %+v", key, stored.Metadata[key], value, stored.Metadata)
		}
	}
}

func TestServiceSSHInfoUsesPersistedLeaseTargetWithoutRuntimeCall(t *testing.T) {
	now := time.Date(2026, 5, 12, 12, 0, 0, 0, time.UTC)
	store := vmleases.NewMemoryStore()
	leases := vmleases.NewService(store, vmleases.ServiceConfig{Now: func() time.Time { return now }, SnapshotSecret: []byte("secret")})
	lease := testMonthlyLease(now, enrollmentStatusEnrolled)
	lease.Metadata["runtime_ssh_host"] = "203.0.113.55"
	lease.Metadata["runtime_public_ip"] = "203.0.113.55"
	lease.Metadata["runtime_private_ip"] = "10.0.0.55"
	lease.Metadata["runtime_ssh_user"] = "root"
	lease.Metadata["runtime_ssh_port"] = "2222"
	lease.Metadata["runtime_observed_state"] = "running"
	lease.Metadata["runtime_lease_state"] = "valid"
	if _, err := leases.CreateOrUpdate(context.Background(), vmleases.CreateRequest{Lease: lease}); err != nil {
		t.Fatalf("CreateOrUpdate: %v", err)
	}
	svc := &Service{Leases: nativeLeaseService(leases), Features: fakeFeatureChecker{enabled: true}}

	resp, err := svc.Action(context.Background(), ActionRequest{
		TenantID: "org-1",
		UserID:   "user-1",
		LeaseID:  "lease-1",
		Action:   serverruntime.RuntimeActionSSHInfo,
	})
	if err != nil {
		t.Fatalf("Action: %v", err)
	}
	if resp.SSH == nil || resp.SSH.Host != "203.0.113.55" || resp.SSH.User != "root" || resp.SSH.Port != 2222 {
		t.Fatalf("SSH response = %+v", resp.SSH)
	}
	if resp.Status == nil || resp.Status.PublicIP != "203.0.113.55" || resp.Status.PrivateIP != "10.0.0.55" {
		t.Fatalf("Status response = %+v", resp.Status)
	}
	journal := store.OperationJournal()
	if len(journal) != 1 || journal[0].Status != vmleases.OperationStatusSSHInfoRequested || journal[0].Actor != "user-1" {
		t.Fatalf("journal = %+v, want ssh_info_requested by user-1", journal)
	}
}

func TestServiceDecommissionCancelsLeaseAndRecordsOperation(t *testing.T) {
	now := time.Date(2026, 5, 12, 12, 0, 0, 0, time.UTC)
	store := vmleases.NewMemoryStore()
	leases := vmleases.NewService(store, vmleases.ServiceConfig{Now: func() time.Time { return now }, SnapshotSecret: []byte("secret")})
	if _, err := leases.CreateOrUpdate(context.Background(), vmleases.CreateRequest{Lease: testMonthlyLease(now, enrollmentStatusEnrolled)}); err != nil {
		t.Fatalf("CreateOrUpdate: %v", err)
	}
	runtime := &fakeRuntimeClient{
		response: &serverruntime.LeaseRuntimeActionResponse{
			LeaseState:    "valid",
			ObservedState: "not_found",
		},
		onAction: func(serverruntime.LeaseRuntimeActionRequest) error {
			stored, err := leases.Get(context.Background(), "org-1", "lease-1")
			if err != nil {
				return err
			}
			if stored.CancelledAt != nil {
				return errors.New("runtime saw a pre-cancelled lease")
			}
			return nil
		}}
	svc := &Service{Leases: nativeLeaseService(leases), Runtime: runtime, Features: fakeFeatureChecker{enabled: true}}

	resp, err := svc.Action(context.Background(), ActionRequest{
		TenantID: "org-1",
		UserID:   "user-1",
		LeaseID:  "lease-1",
		Action:   serverruntime.RuntimeActionDecommission,
	})
	if err != nil {
		t.Fatalf("Action: %v", err)
	}
	if resp.Action != serverruntime.RuntimeActionDecommission {
		t.Fatalf("response action = %q, want decommission", resp.Action)
	}
	if resp.LeaseState != leaseStateCancelled {
		t.Fatalf("response lease_state = %q, want cancelled", resp.LeaseState)
	}
	if resp.DesiredState != "stopped" {
		t.Fatalf("response desired_state = %q, want stopped", resp.DesiredState)
	}
	if resp.ObservedState != "not_found" {
		t.Fatalf("response observed_state = %q, want not_found", resp.ObservedState)
	}
	stored, err := leases.Get(context.Background(), "org-1", "lease-1")
	if err != nil {
		t.Fatalf("stored lease: %v", err)
	}
	if stored.CancelledAt == nil || stored.DesiredState != vmlease.DesiredStateStopped {
		t.Fatalf("stored lease after decommission = %+v", stored)
	}
	if len(runtime.requests) != 1 || runtime.requests[0].Action != serverruntime.RuntimeActionDecommission {
		t.Fatalf("runtime requests = %+v", runtime.requests)
	}
	journal := store.OperationJournal()
	if len(journal) != 1 {
		t.Fatalf("journal entries = %d, want 1", len(journal))
	}
	if got := journal[0]; got.EventType != vmleases.OperationEventDecommission || got.Status != vmleases.OperationStatusDecommissioned || got.Actor != "user-1" {
		t.Fatalf("decommission journal event = %+v", got)
	}
	leaseBeforeDecommission := testMonthlyLease(now, enrollmentStatusEnrolled)
	leaseBeforeDecommission.Metadata[vmleases.MetadataKeyResourceGenerationID] = vmleases.ResourceGenerationID(*stored)
	wantDigest, err := vmleases.ResourceGenerationDigest("org-1", leaseBeforeDecommission)
	if err != nil {
		t.Fatalf("ResourceGenerationDigest: %v", err)
	}
	if got := journal[0].ResourceGenerationDigest; got != wantDigest {
		t.Fatalf("decommission resource generation digest = %q, want %q", got, wantDigest)
	}
}

func TestServiceDecommissionRetryUsesConfirmedJournalWithoutSecondProviderCall(t *testing.T) {
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	store := vmleases.NewMemoryStore()
	leases := vmleases.NewService(store, vmleases.ServiceConfig{Now: func() time.Time { return now }, SnapshotSecret: []byte("secret")})
	if _, err := leases.CreateOrUpdate(t.Context(), vmleases.CreateRequest{Lease: testMonthlyLease(now, enrollmentStatusEnrolled)}); err != nil {
		t.Fatalf("CreateOrUpdate: %v", err)
	}
	finalizationErr := errors.New("injected crash after journal write")
	authority := &failLeaseFinalizationOnceAuthority{Service: leases, err: finalizationErr}
	runtime := &fakeRuntimeClient{response: &serverruntime.LeaseRuntimeActionResponse{
		LeaseState:    leaseStateCancelled,
		ObservedState: "not_found",
	}}
	svc := &Service{Leases: authority, Runtime: runtime, Features: fakeFeatureChecker{enabled: true}}
	req := ActionRequest{
		TenantID: "org-1",
		UserID:   "user-1",
		LeaseID:  "lease-1",
		Action:   serverruntime.RuntimeActionDecommission,
	}

	if _, err := svc.Action(t.Context(), req); !errors.Is(err, finalizationErr) {
		t.Fatalf("first Action error = %v, want injected finalization failure", err)
	}
	if len(runtime.requests) != 1 {
		t.Fatalf("runtime requests after first Action = %d, want 1", len(runtime.requests))
	}
	stored, err := leases.Get(t.Context(), "org-1", "lease-1")
	if err != nil {
		t.Fatalf("Get after first Action: %v", err)
	}
	if stored.CancelledAt != nil {
		t.Fatalf("lease finalized despite injected failure: %+v", stored)
	}
	if journal := store.OperationJournal(); len(journal) != 1 || journal[0].EventType != vmleases.OperationEventDecommission || journal[0].Status != vmleases.OperationStatusDecommissioned {
		t.Fatalf("journal after first Action = %+v, want one confirmed decommission", journal)
	}

	resp, err := svc.Action(t.Context(), req)
	if err != nil {
		t.Fatalf("retry Action: %v", err)
	}
	if len(runtime.requests) != 1 {
		t.Fatalf("runtime requests after retry = %d, confirmed replay made a second provider call", len(runtime.requests))
	}
	if resp.LeaseState != leaseStateCancelled || resp.ObservedState != "not_found" {
		t.Fatalf("retry response = %+v, want canceled/not_found", resp)
	}
	stored, err = leases.Get(t.Context(), "org-1", "lease-1")
	if err != nil {
		t.Fatalf("Get after retry: %v", err)
	}
	if stored.CancelledAt == nil || stored.DesiredState != vmlease.DesiredStateStopped || stored.Metadata["runtime_observed_state"] != "not_found" {
		t.Fatalf("lease was not generation-CAS finalized: %+v", stored)
	}
	if journal := store.OperationJournal(); len(journal) != 1 {
		t.Fatalf("journal after retry = %+v, want no duplicate proof", journal)
	}
}

func TestServiceDecommissionPreflightFailuresStopBeforeProviderCall(t *testing.T) {
	readErr := errors.New("operation journal read failed")
	for _, test := range []struct {
		name    string
		setup   func(*testing.T, time.Time) (LeaseAuthority, *vmleases.Service)
		wantErr error
	}{
		{
			name: "journal read failure",
			setup: func(t *testing.T, now time.Time) (LeaseAuthority, *vmleases.Service) {
				leases := newLeaseServiceWith(t, now, enrollmentStatusEnrolled)
				return &failingConfirmedDecommissionReader{Service: leases, err: readErr}, leases
			},
			wantErr: readErr,
		},
		{
			name: "claimed replay without exact reader",
			setup: func(t *testing.T, now time.Time) (LeaseAuthority, *vmleases.Service) {
				leases := newLeaseServiceWith(t, now, enrollmentStatusEnrolled)
				created, err := leases.Get(t.Context(), "org-1", "lease-1")
				if err != nil {
					t.Fatalf("Get: %v", err)
				}
				digest, err := vmleases.ResourceGenerationDigest("org-1", *created)
				if err != nil {
					t.Fatalf("ResourceGenerationDigest: %v", err)
				}
				if _, err = leases.Patch(t.Context(), "org-1", created.ID, vmleases.PatchRequest{
					ExpectedResourceGenerationDigest: digest, ClaimDecommission: true,
				}); err != nil {
					t.Fatalf("seed generation claim: %v", err)
				}
				return &operationRecordingLeaseAuthority{
					LeaseAuthority: leases,
					record: func(vmleases.OperationEvent) error {
						t.Fatal("preflight failure attempted to record a provider result")
						return nil
					},
				}, leases
			},
			wantErr: ErrDecommissionJournalUnavailable,
		},
		{
			name: "legacy lease without generation",
			setup: func(t *testing.T, now time.Time) (LeaseAuthority, *vmleases.Service) {
				store := vmleases.NewMemoryStore()
				if _, err := store.Upsert(t.Context(), testMonthlyLease(now, enrollmentStatusEnrolled), "legacy-lease"); err != nil {
					t.Fatalf("seed legacy lease: %v", err)
				}
				leases := vmleases.NewService(store, vmleases.ServiceConfig{Now: func() time.Time { return now }, SnapshotSecret: []byte("secret")})
				return nativeLeaseService(leases), leases
			},
			wantErr: ErrDecommissionGenerationUnavailable,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
			authority, leases := test.setup(t, now)
			runtime := &fakeRuntimeClient{}
			svc := &Service{Leases: authority, Runtime: runtime, Features: fakeFeatureChecker{enabled: true}}

			_, err := svc.Action(t.Context(), ActionRequest{
				TenantID: "org-1", UserID: "user-1", LeaseID: "lease-1", Action: serverruntime.RuntimeActionDecommission,
			})
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("Action error = %v, want %v", err, test.wantErr)
			}
			if len(runtime.requests) != 0 {
				t.Fatalf("runtime requests = %+v, preflight failure reached provider", runtime.requests)
			}
			stored, getErr := leases.Get(t.Context(), "org-1", "lease-1")
			if getErr != nil {
				t.Fatalf("Get: %v", getErr)
			}
			if stored.CancelledAt != nil {
				t.Fatalf("preflight failure cancelled lease: %+v", stored)
			}
		})
	}
}

func TestServiceDecommissionJournalFailureDoesNotCancelLease(t *testing.T) {
	now := time.Date(2026, 5, 12, 12, 0, 0, 0, time.UTC)
	leases := newLeaseServiceWith(t, now, enrollmentStatusEnrolled)
	journalErr := errors.New("operation journal unavailable")
	var recorded vmleases.OperationEvent
	authority := &operationRecordingLeaseAuthority{
		LeaseAuthority: leases,
		record: func(event vmleases.OperationEvent) error {
			recorded = event
			stored, err := leases.Get(context.Background(), "org-1", "lease-1")
			if err != nil {
				t.Fatalf("Get while recording: %v", err)
			}
			if stored.CancelledAt != nil {
				t.Fatal("lease was canceled before provider decommission proof was recorded")
			}
			return journalErr
		},
	}
	runtime := &fakeRuntimeClient{}
	svc := &Service{Leases: authority, Runtime: runtime, Features: fakeFeatureChecker{enabled: true}}

	_, err := svc.Action(context.Background(), ActionRequest{
		TenantID: "org-1",
		UserID:   "user-1",
		LeaseID:  "lease-1",
		Action:   serverruntime.RuntimeActionDecommission,
	})
	if !errors.Is(err, journalErr) {
		t.Fatalf("Action error = %v, want journal error", err)
	}
	if recorded.EventType != vmleases.OperationEventDecommission || recorded.Status != vmleases.OperationStatusDecommissioned {
		t.Fatalf("recorded event = %+v, want decommission/decommissioned", recorded)
	}
	if recorded.ResourceGenerationDigest == "" {
		t.Fatalf("recorded event = %+v, want resource generation digest", recorded)
	}
	if len(runtime.requests) != 1 || runtime.requests[0].Action != serverruntime.RuntimeActionDecommission {
		t.Fatalf("runtime requests = %+v, want one successful provider decommission", runtime.requests)
	}
	stored, err := leases.Get(context.Background(), "org-1", "lease-1")
	if err != nil {
		t.Fatalf("stored lease: %v", err)
	}
	if stored.CancelledAt != nil {
		t.Fatalf("journal failure must not cancel lease: %+v", stored)
	}
	if stored.DesiredState != vmlease.DesiredStateRunning {
		t.Fatalf("DesiredState = %q, want running", stored.DesiredState)
	}
}

func TestServiceDecommissionWithoutJournalCapabilityDoesNotCancelLease(t *testing.T) {
	now := time.Date(2026, 5, 12, 12, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name             string
		wrapLeaseService bool
	}{
		{name: "authority omits journal contract", wrapLeaseService: true},
		{name: "underlying store omits journal contract"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var store vmleases.Store = vmleases.NewMemoryStore()
			if !test.wrapLeaseService {
				store = &storeWithoutOperationJournal{Store: store}
			}
			leases := vmleases.NewService(store, vmleases.ServiceConfig{Now: func() time.Time { return now }, SnapshotSecret: []byte("secret")})
			var authority LeaseAuthority = nativeLeaseService(leases)
			if test.wrapLeaseService {
				authority = &leaseAuthorityWithoutJournal{LeaseAuthority: leases}
			}
			if _, err := leases.CreateOrUpdate(t.Context(), vmleases.CreateRequest{Lease: testMonthlyLease(now, enrollmentStatusEnrolled)}); err != nil {
				t.Fatalf("CreateOrUpdate: %v", err)
			}
			runtime := &fakeRuntimeClient{}
			svc := &Service{Leases: authority, Runtime: runtime, Features: fakeFeatureChecker{enabled: true}}

			_, err := svc.Action(t.Context(), ActionRequest{
				TenantID: "org-1", UserID: "user-1", LeaseID: "lease-1", Action: serverruntime.RuntimeActionDecommission,
			})
			if !errors.Is(err, ErrDecommissionJournalUnavailable) {
				t.Fatalf("Action error = %v, want ErrDecommissionJournalUnavailable", err)
			}
			if len(runtime.requests) != 1 || runtime.requests[0].Action != serverruntime.RuntimeActionDecommission {
				t.Fatalf("runtime requests = %+v, want one confirmed decommission", runtime.requests)
			}
			stored, err := leases.Get(t.Context(), "org-1", "lease-1")
			if err != nil {
				t.Fatalf("stored lease: %v", err)
			}
			if stored.CancelledAt != nil || stored.DesiredState != vmlease.DesiredStateRunning {
				t.Fatalf("missing journal capability changed lease: %+v", stored)
			}
		})
	}
}

func TestServiceDecommissionAlreadyCancelledLeaseIsIdempotent(t *testing.T) {
	now := time.Date(2026, 5, 12, 12, 0, 0, 0, time.UTC)
	store := vmleases.NewMemoryStore()
	leases := vmleases.NewService(store, vmleases.ServiceConfig{Now: func() time.Time { return now }, SnapshotSecret: []byte("secret")})
	if _, err := leases.CreateOrUpdate(context.Background(), vmleases.CreateRequest{Lease: testMonthlyLease(now, enrollmentStatusEnrolled)}); err != nil {
		t.Fatalf("CreateOrUpdate: %v", err)
	}
	if _, err := leases.Patch(context.Background(), "org-1", "lease-1", vmleases.PatchRequest{Cancel: true}); err != nil {
		t.Fatalf("pre-cancel lease: %v", err)
	}
	runtime := &fakeRuntimeClient{onAction: func(serverruntime.LeaseRuntimeActionRequest) error {
		t.Fatal("already cancelled decommission should not call runtime")
		return nil
	}}
	svc := &Service{Leases: nativeLeaseService(leases), Runtime: runtime, Features: fakeFeatureChecker{enabled: true}}

	resp, err := svc.Action(context.Background(), ActionRequest{
		TenantID: "org-1",
		UserID:   "user-1",
		LeaseID:  "lease-1",
		Action:   serverruntime.RuntimeActionDecommission,
	})
	if err != nil {
		t.Fatalf("Action: %v", err)
	}
	if resp.Action != serverruntime.RuntimeActionDecommission || resp.LeaseState != leaseStateCancelled {
		t.Fatalf("response = %+v, want cancelled decommission", resp)
	}
	if len(runtime.requests) != 0 {
		t.Fatalf("runtime requests = %+v, want none", runtime.requests)
	}
	journal := store.OperationJournal()
	if len(journal) != 1 || journal[0].EventType != vmleases.OperationEventRuntimeAction || journal[0].Status != vmleases.OperationStatusDecommissioned {
		t.Fatalf("journal = %+v, want fresh idempotent decommission event", journal)
	}
}

func TestServiceDecommissionRuntimeErrorsNeverBecomeProviderProof(t *testing.T) {
	oldDelays := decommissionRuntimeRetryDelays
	decommissionRuntimeRetryDelays = []time.Duration{0}
	defer func() { decommissionRuntimeRetryDelays = oldDelays }()

	now := time.Date(2026, 5, 12, 12, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name string
		err  error
	}{
		{name: "lease cancelled text", err: errors.New(`simulate runtime decommission returned 400: {"error":{"message":"lease_cancelled"}}`)},
		{name: "provider node not found text", err: errors.New(`simulate runtime decommission returned 502: {"error":{"code":502,"message":"destroy ionos-managed node: node not found: cafde37b/e9677a15"}}`)},
		{name: "missing legacy record text", err: errors.New("legacy managed runtime record not found")},
		{name: "generic runtime failure", err: errors.New("simulate decommission failed")},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := vmleases.NewMemoryStore()
			leases := vmleases.NewService(store, vmleases.ServiceConfig{Now: func() time.Time { return now }, SnapshotSecret: []byte("secret")})
			if _, err := leases.CreateOrUpdate(t.Context(), vmleases.CreateRequest{Lease: testMonthlyLease(now, enrollmentStatusEnrolled)}); err != nil {
				t.Fatalf("CreateOrUpdate: %v", err)
			}
			svc := &Service{Leases: nativeLeaseService(leases), Runtime: &fakeRuntimeClient{err: test.err}, Features: fakeFeatureChecker{enabled: true}}

			if _, err := svc.Action(t.Context(), ActionRequest{
				TenantID: "org-1", UserID: "user-1", LeaseID: "lease-1", Action: serverruntime.RuntimeActionDecommission,
			}); err == nil {
				t.Fatal("Action error = nil, runtime failure must fail closed")
			}
			stored, err := leases.Get(t.Context(), "org-1", "lease-1")
			if err != nil {
				t.Fatalf("stored lease: %v", err)
			}
			if stored.CancelledAt != nil || stored.DesiredState != vmlease.DesiredStateRunning {
				t.Fatalf("runtime failure changed lease: %+v", stored)
			}
			journal := store.OperationJournal()
			if len(journal) != 1 || journal[0].EventType != vmleases.OperationEventRuntimeAction || journal[0].Status != vmleases.OperationStatusFailed {
				t.Fatalf("journal = %+v, runtime failure must not create provider proof", journal)
			}
		})
	}
}

func TestServiceDecommissionRetriesTransientRuntimeFailure(t *testing.T) {
	oldDelays := decommissionRuntimeRetryDelays
	decommissionRuntimeRetryDelays = []time.Duration{0}
	defer func() { decommissionRuntimeRetryDelays = oldDelays }()

	now := time.Date(2026, 5, 12, 12, 0, 0, 0, time.UTC)
	leases := newLeaseServiceWith(t, now, enrollmentStatusEnrolled)
	runtime := &sequenceRuntimeClient{errs: []error{
		errors.New(`simulate runtime decommission returned 502: upstream returned HTML error page`),
		nil,
	}}
	svc := &Service{Leases: nativeLeaseService(leases), Runtime: runtime, Features: fakeFeatureChecker{enabled: true}}

	resp, err := svc.Action(context.Background(), ActionRequest{
		TenantID: "org-1",
		UserID:   "user-1",
		LeaseID:  "lease-1",
		Action:   serverruntime.RuntimeActionDecommission,
	})
	if err != nil {
		t.Fatalf("Action: %v", err)
	}
	if resp.LeaseState != leaseStateCancelled {
		t.Fatalf("LeaseState = %q, want cancelled", resp.LeaseState)
	}
	if len(runtime.requests) != 2 {
		t.Fatalf("runtime requests = %d, want 2", len(runtime.requests))
	}
	stored, err := leases.Get(context.Background(), "org-1", "lease-1")
	if err != nil {
		t.Fatalf("stored lease: %v", err)
	}
	if stored.CancelledAt == nil || stored.DesiredState != vmlease.DesiredStateStopped {
		t.Fatalf("stored lease = %+v, want cancelled/stopped", stored)
	}
}

func TestServiceDecommissionBypassesRecoveryGates(t *testing.T) {
	for _, test := range []struct {
		name           string
		enrollment     string
		featureEnabled bool
	}{
		{name: "disabled feature", enrollment: enrollmentStatusEnrolled},
		{name: "pending enrollment", enrollment: EnrollmentStatusPending, featureEnabled: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			now := time.Date(2026, 5, 12, 12, 0, 0, 0, time.UTC)
			leases := newLeaseServiceWith(t, now, test.enrollment)
			runtime := &fakeRuntimeClient{}
			svc := &Service{Leases: nativeLeaseService(leases), Runtime: runtime, Features: fakeFeatureChecker{enabled: test.featureEnabled}}

			resp, err := svc.Action(t.Context(), ActionRequest{
				TenantID: "org-1", UserID: "user-1", LeaseID: "lease-1", Action: serverruntime.RuntimeActionDecommission,
			})
			if err != nil {
				t.Fatalf("Action: %v", err)
			}
			if resp.Action != serverruntime.RuntimeActionDecommission || resp.EnrollmentStatus != test.enrollment {
				t.Fatalf("response = %+v, want decommission with enrollment %q", resp, test.enrollment)
			}
			if len(runtime.requests) != 1 || runtime.requests[0].Action != serverruntime.RuntimeActionDecommission {
				t.Fatalf("runtime requests = %+v, want one decommission", runtime.requests)
			}
			stored, err := leases.Get(t.Context(), "org-1", "lease-1")
			if err != nil {
				t.Fatalf("stored lease: %v", err)
			}
			if stored.CancelledAt == nil || stored.DesiredState != vmlease.DesiredStateStopped {
				t.Fatalf("lease after decommission = %+v, want cancelled/stopped", stored)
			}
		})
	}
}

func TestServiceSSHActionsRecordOperationJournal(t *testing.T) {
	now := time.Date(2026, 5, 12, 12, 0, 0, 0, time.UTC)
	store := vmleases.NewMemoryStore()
	leases := vmleases.NewService(store, vmleases.ServiceConfig{Now: func() time.Time { return now }, SnapshotSecret: []byte("secret")})
	if _, err := leases.CreateOrUpdate(context.Background(), vmleases.CreateRequest{Lease: testMonthlyLease(now, enrollmentStatusEnrolled)}); err != nil {
		t.Fatalf("CreateOrUpdate: %v", err)
	}
	runtime := &fakeRuntimeClient{}
	svc := &Service{Leases: nativeLeaseService(leases), Runtime: runtime, Features: fakeFeatureChecker{enabled: true}}

	for _, tc := range []struct {
		action serverruntime.RuntimeAction
		status string
	}{
		{serverruntime.RuntimeActionEnableSSH, vmleases.OperationStatusSSHEnabled},
		{serverruntime.RuntimeActionDisableSSH, vmleases.OperationStatusSSHDisabled},
	} {
		if _, err := svc.Action(context.Background(), ActionRequest{TenantID: "org-1", UserID: "user-1", LeaseID: "lease-1", Action: tc.action}); err != nil {
			t.Fatalf("Action(%s): %v", tc.action, err)
		}
		journal := store.OperationJournal()
		got := journal[len(journal)-1]
		if got.EventType != vmleases.OperationEventRuntimeAction || got.Status != tc.status || got.Actor != "user-1" {
			t.Fatalf("journal event after %s = %+v, want status %s", tc.action, got, tc.status)
		}
	}
}

func TestServiceRuntimeFailureRecordsFailedOperation(t *testing.T) {
	now := time.Date(2026, 5, 12, 12, 0, 0, 0, time.UTC)
	store := vmleases.NewMemoryStore()
	leases := vmleases.NewService(store, vmleases.ServiceConfig{Now: func() time.Time { return now }, SnapshotSecret: []byte("secret")})
	if _, err := leases.CreateOrUpdate(context.Background(), vmleases.CreateRequest{Lease: testMonthlyLease(now, enrollmentStatusEnrolled)}); err != nil {
		t.Fatalf("CreateOrUpdate: %v", err)
	}
	svc := &Service{Leases: nativeLeaseService(leases), Runtime: &fakeRuntimeClient{err: errors.New("managed runtime refused ssh")}, Features: fakeFeatureChecker{enabled: true}}

	_, err := svc.Action(context.Background(), ActionRequest{
		TenantID: "org-1",
		UserID:   "user-1",
		LeaseID:  "lease-1",
		Action:   serverruntime.RuntimeActionEnableSSH,
	})
	if err == nil {
		t.Fatal("Action error = nil, want simulate failure")
	}
	journal := store.OperationJournal()
	if len(journal) != 1 {
		t.Fatalf("journal entries = %d, want 1", len(journal))
	}
	if got := journal[0]; got.Status != vmleases.OperationStatusFailed || !strings.Contains(got.Error, "managed runtime refused ssh") {
		t.Fatalf("failure journal event = %+v", got)
	}
}

func TestServiceOperationsReturnsVisibleLeaseJournalNewestFirst(t *testing.T) {
	now := time.Date(2026, 5, 12, 12, 0, 0, 0, time.UTC)
	store := vmleases.NewMemoryStore()
	leases := vmleases.NewService(store, vmleases.ServiceConfig{Now: func() time.Time { return now }, SnapshotSecret: []byte("secret")})
	if _, err := leases.CreateOrUpdate(context.Background(), vmleases.CreateRequest{Lease: testMonthlyLease(now, enrollmentStatusEnrolled)}); err != nil {
		t.Fatalf("CreateOrUpdate: %v", err)
	}
	for _, event := range []vmleases.OperationEvent{
		{
			TenantID:  "org-1",
			LeaseID:   "lease-1",
			EventType: vmleases.OperationEventEnrollment,
			Status:    vmleases.OperationStatusEnrolled,
			Actor:     vmleases.OperationActorSystem,
			CreatedAt: now.Add(-2 * time.Minute),
		},
		{
			TenantID:  "org-1",
			LeaseID:   "lease-1",
			EventType: vmleases.OperationEventRuntimeAction,
			Status:    vmleases.OperationStatusSSHEnabled,
			Actor:     "user-1",
			CreatedAt: now.Add(-time.Minute),
		},
		{
			TenantID:  "org-2",
			LeaseID:   "lease-1",
			EventType: vmleases.OperationEventRuntimeAction,
			Status:    vmleases.OperationStatusFailed,
			Actor:     "other-user",
			Error:     "wrong tenant",
			CreatedAt: now,
		},
	} {
		if err := leases.RecordOperation(context.Background(), event); err != nil {
			t.Fatalf("RecordOperation: %v", err)
		}
	}
	svc := &Service{Leases: nativeLeaseService(leases)}

	events, err := svc.Operations(context.Background(), OperationsRequest{
		TenantID: "org-1",
		UserID:   "user-1",
		LeaseID:  "lease-1",
		Limit:    10,
	})
	if err != nil {
		t.Fatalf("Operations: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("events = %d, want 2: %+v", len(events), events)
	}
	if events[0].Status != vmleases.OperationStatusSSHEnabled || events[1].Status != vmleases.OperationStatusEnrolled {
		t.Fatalf("events order/status = %+v", events)
	}
	if text := strings.ToLower(events[0].Error + events[1].Error); strings.Contains(text, "wrong tenant") {
		t.Fatalf("events leaked wrong tenant error: %+v", events)
	}
}

func TestServiceOperationsRejectsWrongUser(t *testing.T) {
	now := time.Date(2026, 5, 12, 12, 0, 0, 0, time.UTC)
	leases := newLeaseServiceWith(t, now, enrollmentStatusEnrolled)
	svc := &Service{Leases: nativeLeaseService(leases)}

	_, err := svc.Operations(context.Background(), OperationsRequest{
		TenantID: "org-1",
		UserID:   "user-2",
		LeaseID:  "lease-1",
	})
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("Operations error = %v, want ErrForbidden", err)
	}
}

func TestServiceOfferingsExposePublicCatalog(t *testing.T) {
	svc := &Service{}
	offerings := svc.Offerings()
	if len(offerings) == 0 {
		t.Fatal("Offerings returned no public offerings")
	}
	payload, err := json.Marshal(offerings)
	if err != nil {
		t.Fatalf("Marshal offerings: %v", err)
	}
	text := strings.ToLower(string(payload))
	if strings.Contains(text, "provider") || strings.Contains(text, "centron") {
		t.Fatalf("public offerings expose provider internals: %s", text)
	}
}

func TestServiceActionAdmissionFailuresStopBeforeRuntime(t *testing.T) {
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name             string
		authority        vmleases.LeaseExecutionAuthority
		state            vmleases.LeaseAuthorityState
		withoutInventory bool
		tenantID         string
		wantErr          error
	}{
		{name: "unbound", state: vmleases.LeaseAuthorityStateUnbound, wantErr: ErrExecutionAuthorityInactive},
		{name: "legacy quarantined", authority: vmleases.LeaseExecutionAuthorityLegacySimulate, state: vmleases.LeaseAuthorityStateLegacyQuarantined, wantErr: ErrExecutionAuthorityInactive},
		{name: "native inactive", authority: vmleases.LeaseExecutionAuthorityTechStackProviderControl, state: vmleases.LeaseAuthorityStateNativeInactive, wantErr: ErrExecutionAuthorityInactive},
		{name: "inventory unavailable", withoutInventory: true, wantErr: vmleases.ErrLeaseInventoryUnavailable},
		{name: "wrong tenant", tenantID: "org-2", wantErr: vmleases.ErrNotFound},
	} {
		t.Run(test.name, func(t *testing.T) {
			leases := newLeaseServiceWith(t, now, enrollmentStatusEnrolled)
			var authority LeaseAuthority = nativeLeaseService(leases)
			if test.withoutInventory {
				authority = &leaseAuthorityWithoutInventory{LeaseAuthority: leases}
			} else if test.state != "" {
				authority = &inventoryStateLeaseService{Service: leases, authority: test.authority, state: test.state}
			}
			tenantID := test.tenantID
			if tenantID == "" {
				tenantID = "org-1"
			}
			runtime := &fakeRuntimeClient{}
			svc := &Service{Leases: authority, Runtime: runtime, Features: fakeFeatureChecker{enabled: true}}
			_, err := svc.Action(t.Context(), ActionRequest{
				TenantID: tenantID, UserID: "user-1", LeaseID: "lease-1", Action: serverruntime.RuntimeActionStatus,
			})
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("Action error = %v, want %v", err, test.wantErr)
			}
			if len(runtime.requests) != 0 {
				t.Fatalf("runtime requests = %+v, rejected admission reached runtime", runtime.requests)
			}
		})
	}
}

func assertCustodyResolved(t *testing.T, svc *Service, leases *vmleases.Service) {
	t.Helper()

	response, err := svc.ResolveCustody(t.Context(), CustodyResolutionRequest{
		TenantID: "org-1", UserID: "user-1", LeaseID: "lease-1", ProviderCleanupConfirmed: true,
	})
	if err != nil {
		t.Fatalf("ResolveCustody: %v", err)
	}
	if response.LeaseState != "resolved" || response.ObservedState != "custody_resolved" {
		t.Fatalf("response = %+v", response)
	}
	stored, err := leases.Get(t.Context(), "org-1", "lease-1")
	if err != nil {
		t.Fatalf("Get resolved lease: %v", err)
	}
	if stored.CancelledAt == nil || stored.DesiredState != vmlease.DesiredStateArchived || stored.Metadata["custody_resolution_status"] != "resolved" {
		t.Fatalf("resolved lease = %+v", stored)
	}
}

func TestServiceResolveCustodyArchivesOnlyConfirmedNonProviderLease(t *testing.T) {
	now := time.Date(2026, 8, 11, 10, 0, 0, 0, time.UTC)
	leases := vmleases.NewService(vmleases.NewMemoryStore(), vmleases.ServiceConfig{Now: func() time.Time { return now }})
	if _, err := leases.CreateOrUpdate(t.Context(), vmleases.CreateRequest{Lease: testMonthlyLease(now, enrollmentStatusEnrolled)}); err != nil {
		t.Fatalf("CreateOrUpdate: %v", err)
	}
	svc := &Service{Leases: &inventoryStateLeaseService{
		Service: leases, authority: vmleases.LeaseExecutionAuthorityLegacySimulate,
		state: vmleases.LeaseAuthorityStateLegacyQuarantined,
	}}

	if _, err := svc.ResolveCustody(t.Context(), CustodyResolutionRequest{
		TenantID: "org-1", UserID: "user-1", LeaseID: "lease-1",
	}); !errors.Is(err, ErrCustodyResolutionConfirmation) {
		t.Fatalf("ResolveCustody without confirmation = %v, want confirmation error", err)
	}
	before, err := leases.Get(t.Context(), "org-1", "lease-1")
	if err != nil {
		t.Fatalf("Get before resolution: %v", err)
	}
	if before.CancelledAt != nil {
		t.Fatal("unconfirmed resolution mutated lease")
	}

	assertCustodyResolved(t, svc, leases)
	if _, err = svc.ResolveCustody(t.Context(), CustodyResolutionRequest{
		TenantID: "org-1", UserID: "user-1", LeaseID: "lease-1", ProviderCleanupConfirmed: true,
	}); err != nil {
		t.Fatalf("idempotent ResolveCustody: %v", err)
	}
}

func TestServiceResolveCustodyArchivesPreGenerationLegacyRecord(t *testing.T) {
	now := time.Date(2026, 8, 11, 12, 30, 0, 0, time.UTC)
	store := vmleases.NewMemoryStore()
	if _, err := store.Upsert(t.Context(), testMonthlyLease(now, enrollmentStatusEnrolled), "legacy-custody"); err != nil {
		t.Fatalf("seed pre-generation lease: %v", err)
	}
	leases := vmleases.NewService(store, vmleases.ServiceConfig{Now: func() time.Time { return now }})
	svc := &Service{Leases: &inventoryStateLeaseService{
		Service: leases, authority: vmleases.LeaseExecutionAuthorityLegacySimulate,
		state: vmleases.LeaseAuthorityStateLegacyQuarantined,
	}}
	before, err := leases.Get(t.Context(), "org-1", "lease-1")
	if err != nil {
		t.Fatalf("Get pre-generation lease: %v", err)
	}
	if generation := vmleases.ResourceGenerationID(*before); generation != "" {
		t.Fatalf("pre-generation fixture unexpectedly has resource_generation_id %q", generation)
	}

	assertCustodyResolved(t, svc, leases)
}

func TestServiceResolveCustodyDoesNotRelaxGenerationGuardForNonLegacyState(t *testing.T) {
	now := time.Date(2026, 8, 11, 12, 30, 0, 0, time.UTC)
	store := vmleases.NewMemoryStore()
	if _, err := store.Upsert(t.Context(), testMonthlyLease(now, enrollmentStatusEnrolled), "non-legacy-no-generation"); err != nil {
		t.Fatalf("seed no-generation lease: %v", err)
	}
	leases := vmleases.NewService(store, vmleases.ServiceConfig{Now: func() time.Time { return now }})
	svc := &Service{Leases: &inventoryStateLeaseService{
		Service: leases, state: vmleases.LeaseAuthorityStateNativeInactive,
	}}

	_, err := svc.ResolveCustody(t.Context(), CustodyResolutionRequest{
		TenantID: "org-1", UserID: "user-1", LeaseID: "lease-1", ProviderCleanupConfirmed: true,
	})
	if !errors.Is(err, vmleases.ErrResourceGenerationUnavailable) {
		t.Fatalf("ResolveCustody non-legacy state = %v, want ErrResourceGenerationUnavailable", err)
	}
	stored, getErr := leases.Get(t.Context(), "org-1", "lease-1")
	if getErr != nil {
		t.Fatalf("Get unchanged lease: %v", getErr)
	}
	if stored.CancelledAt != nil || stored.Metadata["custody_resolution_status"] != "" {
		t.Fatalf("non-legacy lease mutated = %+v", stored)
	}
}

func TestServiceResolveCustodyRejectsProviderManagedLease(t *testing.T) {
	now := time.Date(2026, 8, 11, 10, 0, 0, 0, time.UTC)
	leases := vmleases.NewService(vmleases.NewMemoryStore(), vmleases.ServiceConfig{Now: func() time.Time { return now }})
	if _, err := leases.CreateOrUpdate(t.Context(), vmleases.CreateRequest{Lease: testMonthlyLease(now, enrollmentStatusEnrolled)}); err != nil {
		t.Fatalf("CreateOrUpdate: %v", err)
	}
	svc := &Service{Leases: nativeLeaseService(leases)}
	_, err := svc.ResolveCustody(t.Context(), CustodyResolutionRequest{
		TenantID: "org-1", UserID: "user-1", LeaseID: "lease-1", ProviderCleanupConfirmed: true,
	})
	if !errors.Is(err, ErrCustodyResolutionProviderManaged) {
		t.Fatalf("ResolveCustody = %v, want provider-managed rejection", err)
	}
	stored, getErr := leases.Get(t.Context(), "org-1", "lease-1")
	if getErr != nil {
		t.Fatalf("Get provider-managed lease: %v", getErr)
	}
	if stored.CancelledAt != nil {
		t.Fatal("provider-managed resolution mutated lease")
	}
}

func TestServiceAllowsExactNativeInactiveDecommissionContinuation(t *testing.T) {
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	leases := vmleases.NewService(vmleases.NewMemoryStore(), vmleases.ServiceConfig{Now: func() time.Time { return now }})
	created, err := leases.CreateOrUpdate(t.Context(), vmleases.CreateRequest{Lease: testMonthlyLease(now, enrollmentStatusEnrolled)})
	if err != nil {
		t.Fatalf("CreateOrUpdate: %v", err)
	}
	digest, err := vmleases.ResourceGenerationDigest("org-1", *created)
	if err != nil {
		t.Fatalf("ResourceGenerationDigest: %v", err)
	}
	claimed, err := leases.Patch(t.Context(), "org-1", created.ID, vmleases.PatchRequest{
		ExpectedResourceGenerationDigest: digest,
		ClaimDecommission:                true,
	})
	if err != nil {
		t.Fatalf("claim decommission: %v", err)
	}
	if _, err = leases.Patch(t.Context(), "org-1", claimed.ID, vmleases.PatchRequest{
		ExpectedResourceGenerationDigest: digest,
		Cancel:                           true,
	}); err != nil {
		t.Fatalf("cancel claimed lease: %v", err)
	}
	runtime := &fakeRuntimeClient{}
	svc := &Service{
		Leases: &inventoryStateLeaseService{
			Service:   leases,
			authority: vmleases.LeaseExecutionAuthorityTechStackProviderControl,
			state:     vmleases.LeaseAuthorityStateNativeInactive,
		},
		Runtime: runtime,
	}
	if _, err = svc.Action(t.Context(), ActionRequest{
		TenantID:                         "org-1",
		UserID:                           "user-1",
		LeaseID:                          created.ID,
		Action:                           serverruntime.RuntimeActionDecommission,
		Internal:                         true,
		ReconcileClaimedDecommission:     true,
		ExpectedResourceGenerationDigest: digest,
	}); err != nil {
		t.Fatalf("exact native-inactive continuation: %v", err)
	}
	if len(runtime.requests) != 1 || runtime.requests[0].Action != serverruntime.RuntimeActionDecommission {
		t.Fatalf("runtime requests = %+v, want exact decommission continuation", runtime.requests)
	}
}

func TestServiceAllowsOwnerDecommissionOnNativeInactiveProviderControl(t *testing.T) {
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	leases := vmleases.NewService(vmleases.NewMemoryStore(), vmleases.ServiceConfig{Now: func() time.Time { return now }})
	created, err := leases.CreateOrUpdate(t.Context(), vmleases.CreateRequest{Lease: testMonthlyLease(now, enrollmentStatusEnrolled)})
	if err != nil {
		t.Fatalf("CreateOrUpdate: %v", err)
	}
	digest, err := vmleases.ResourceGenerationDigest("org-1", *created)
	if err != nil {
		t.Fatalf("ResourceGenerationDigest: %v", err)
	}
	if _, err = leases.Patch(t.Context(), "org-1", created.ID, vmleases.PatchRequest{
		ExpectedResourceGenerationDigest: digest,
		ClaimDecommission:                true,
		Cancel:                           true,
	}); err != nil {
		t.Fatalf("cancel claimed lease: %v", err)
	}
	runtime := &fakeRuntimeClient{onAction: func(serverruntime.LeaseRuntimeActionRequest) error {
		t.Fatal("cancelled owner decommission should not call runtime")
		return nil
	}}
	svc := &Service{
		Leases: &inventoryStateLeaseService{
			Service:   leases,
			authority: vmleases.LeaseExecutionAuthorityTechStackProviderControl,
			state:     vmleases.LeaseAuthorityStateNativeInactive,
		},
		Runtime: runtime,
	}
	resp, err := svc.Action(t.Context(), ActionRequest{
		TenantID: "org-1",
		UserID:   "user-1",
		LeaseID:  created.ID,
		Action:   serverruntime.RuntimeActionDecommission,
	})
	if err != nil {
		t.Fatalf("owner native-inactive decommission: %v", err)
	}
	if resp.Action != serverruntime.RuntimeActionDecommission || resp.LeaseState != leaseStateCancelled {
		t.Fatalf("response = %+v, want cancelled decommission", resp)
	}
	if len(runtime.requests) != 0 {
		t.Fatalf("runtime requests = %+v, want none", runtime.requests)
	}
}

func TestServiceBlocksNonEnrolledLease(t *testing.T) {
	now := time.Date(2026, 5, 12, 12, 0, 0, 0, time.UTC)
	leases := vmleases.NewService(vmleases.NewMemoryStore(), vmleases.ServiceConfig{Now: func() time.Time { return now }, SnapshotSecret: []byte("secret")})
	for _, status := range []string{"pending", "retrying", "failed"} {
		t.Run(status, func(t *testing.T) {
			lease := testMonthlyLease(now, status)
			lease.ID = vmlease.LeaseID("lease-" + status)
			if _, err := leases.CreateOrUpdate(context.Background(), vmleases.CreateRequest{Lease: lease}); err != nil {
				t.Fatalf("CreateOrUpdate: %v", err)
			}
			runtime := &fakeRuntimeClient{}
			svc := &Service{Leases: nativeLeaseService(leases), Runtime: runtime, Features: fakeFeatureChecker{enabled: true}}

			_, err := svc.Action(context.Background(), ActionRequest{TenantID: "org-1", UserID: "user-1", LeaseID: lease.ID, Action: serverruntime.RuntimeActionStatus})
			if !errors.Is(err, ErrEnrollmentPending) {
				t.Fatalf("Action error = %v, want ErrEnrollmentPending", err)
			}
			if len(runtime.requests) != 0 {
				t.Fatalf("runtime requests = %d, want none", len(runtime.requests))
			}
		})
	}
}

// Native status reports enrollment from the canonical aggregate and never
// rewrites legacy enrollment metadata, which the production database rejects
// after native cutover (reproduced live: every enrolled status answered 502).
func TestServiceNativeStatusReportsCanonicalEnrollmentWithoutLegacyWrite(t *testing.T) {
	now := time.Now().UTC()
	leases := newLeaseServiceWith(t, now, EnrollmentStatusPending)
	servers := controlplane.NewMemoryStore()
	if _, err := servers.UpsertServerRuntime(t.Context(), controlplane.ServerRuntime{
		ID: runtimeidentity.LeaseServerID("lease-1"), TenantID: "org-1", OwnerSubjectID: "user-1", LeaseID: "lease-1",
		ProviderRef: "centron", LifecycleState: "active", DesiredState: "running",
		ConnectionState: "connected", HealthState: "healthy", LastHeartbeatAt: &now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	svc := &Service{
		Leases: nativeLeaseService(leases), Runtime: &NativeRuntimeClient{Servers: servers},
		Features: fakeFeatureChecker{enabled: true},
	}
	resp, err := svc.Action(t.Context(), ActionRequest{
		TenantID: "org-1", UserID: "user-1", LeaseID: "lease-1", Action: serverruntime.RuntimeActionStatus,
	})
	if err != nil {
		t.Fatal(err)
	}
	stored, err := leases.Get(t.Context(), "org-1", "lease-1")
	if err != nil {
		t.Fatal(err)
	}
	if resp.ObservedState != "running" || resp.EnrollmentStatus != EnrollmentStatusEnrolled ||
		stored.Metadata["runtime_enrollment_status"] != EnrollmentStatusPending {
		t.Fatalf("response=%+v stored enrollment=%q, want canonical enrollment without a legacy metadata write", resp, stored.Metadata["runtime_enrollment_status"])
	}
}

func TestServiceRejectsUnsupportedNativeActionBeforeDesiredStateMutation(t *testing.T) {
	now := time.Now().UTC()
	leases := newLeaseServiceWith(t, now, EnrollmentStatusEnrolled)
	svc := &Service{
		Leases: nativeLeaseService(leases), Runtime: &NativeRuntimeClient{Servers: controlplane.NewMemoryStore()},
		Features: fakeFeatureChecker{enabled: true},
	}
	_, err := svc.Action(t.Context(), ActionRequest{
		TenantID: "org-1", UserID: "user-1", LeaseID: "lease-1", Action: serverruntime.RuntimeActionStop,
	})
	if !errors.Is(err, ErrNativeRuntimeActionUnsupported) {
		t.Fatalf("Action error = %v, want native capability rejection", err)
	}
	stored, err := leases.Get(t.Context(), "org-1", "lease-1")
	if err != nil {
		t.Fatal(err)
	}
	if stored.DesiredState != vmlease.DesiredStateRunning {
		t.Fatalf("DesiredState = %q, want no mutation before unsupported provider action", stored.DesiredState)
	}
}

func TestServiceActionsThatUseRuntimeClientRejectMissingClient(t *testing.T) {
	svc := &Service{Leases: vmleases.NewService(vmleases.NewMemoryStore(), vmleases.ServiceConfig{SnapshotSecret: []byte("secret")})}
	for name, request := range map[string]ActionRequest{
		"decommission without durable provider control": {Action: serverruntime.RuntimeActionDecommission},
		"start":        {Action: serverruntime.RuntimeActionStart},
		"stop":         {Action: serverruntime.RuntimeActionStop},
		"status":       {Action: serverruntime.RuntimeActionStatus},
		"forced start": {Action: serverruntime.RuntimeActionStart, Force: true},
	} {
		t.Run(name, func(t *testing.T) {
			request.TenantID = "org-1"
			request.UserID = "user-1"
			request.LeaseID = "lease-1"
			if _, err := svc.Action(t.Context(), request); !errors.Is(err, ErrRuntimeClient) {
				t.Fatalf("Action error = %v, want ErrRuntimeClient", err)
			}
		})
	}
}

func TestServiceRejectsMissingLeaseAuthorityBeforeRuntimeDependency(t *testing.T) {
	_, err := (&Service{}).Action(t.Context(), ActionRequest{Action: serverruntime.RuntimeActionDecommission, Force: true})
	if !errors.Is(err, vmleases.ErrEnrollmentRequired) {
		t.Fatalf("Action error = %v, want ErrEnrollmentRequired", err)
	}
}

func TestServiceDisabledFeatureAuthorization(t *testing.T) {
	for _, test := range []struct {
		name     string
		action   serverruntime.RuntimeAction
		internal bool
		wantErr  error
	}{
		{name: "external status blocked", action: serverruntime.RuntimeActionStatus, wantErr: ErrFeatureDisabled},
		{name: "authorized internal SSH info allowed", action: serverruntime.RuntimeActionSSHInfo, internal: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			now := time.Date(2026, 5, 12, 12, 0, 0, 0, time.UTC)
			leases := newLeaseServiceWith(t, now, enrollmentStatusEnrolled)
			svc := &Service{Leases: nativeLeaseService(leases), Runtime: &fakeRuntimeClient{}, Features: fakeFeatureChecker{enabled: false}}

			_, err := svc.Action(t.Context(), ActionRequest{
				TenantID: "org-1", UserID: "user-1", LeaseID: "lease-1", Action: test.action, Internal: test.internal,
			})
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("Action error = %v, want %v", err, test.wantErr)
			}
			if test.wantErr != nil && !strings.Contains(err.Error(), FeatureTechStackManagedRuntime) {
				t.Fatalf("Action error = %v, want disabled feature key", err)
			}
		})
	}
}

func TestServiceRequiresProviderSpecificIONOSFeature(t *testing.T) {
	now := time.Date(2026, 5, 12, 12, 0, 0, 0, time.UTC)
	leases := vmleases.NewService(vmleases.NewMemoryStore(), vmleases.ServiceConfig{Now: func() time.Time { return now }, SnapshotSecret: []byte("secret")})
	lease := testMonthlyLease(now, enrollmentStatusEnrolled)
	lease.Resource.ProviderID = ProviderIONOS
	lease.Metadata = NormalizeMetadata(map[string]string{MetadataKeyProviderID: ProviderIONOS}, serverruntime.RuntimeOfferingStandard)
	lease.Metadata["runtime_enrollment_status"] = enrollmentStatusEnrolled
	if _, err := leases.CreateOrUpdate(context.Background(), vmleases.CreateRequest{Lease: lease}); err != nil {
		t.Fatalf("CreateOrUpdate: %v", err)
	}
	features := &recordingFeatureChecker{enabled: true}
	svc := &Service{Leases: nativeLeaseService(leases), Runtime: &fakeRuntimeClient{}, Features: features}

	if _, err := svc.Action(context.Background(), ActionRequest{TenantID: "org-1", UserID: "user-1", LeaseID: "lease-1", Action: serverruntime.RuntimeActionStatus}); err != nil {
		t.Fatalf("Action: %v", err)
	}

	want := []string{
		FeatureTechStackManagedRuntime,
		FeatureTechStackManagedRuntimeCloudKit,
		FeatureTechStackManagedRuntimeIONOS,
	}
	if strings.Join(features.keys, ",") != strings.Join(want, ",") {
		t.Fatalf("feature keys = %v, want %v", features.keys, want)
	}
	if strings.Join(features.orgIDs, ",") != "org-1,org-1,org-1" {
		t.Fatalf("feature org IDs = %v, want org-1 for background lease checks", features.orgIDs)
	}
}

func TestManagedRuntimeEntitlementDenialDetailsAreActionable(t *testing.T) {
	details := ManagedRuntimeEntitlementDenialDetails(
		ProviderIONOS,
		EntitlementReasonFeatureDisabled,
		RequiredFeatureKeysForProvider(ProviderIONOS),
		[]string{FeatureTechStackManagedRuntimeIONOS},
	)

	if got := details["error_code"]; got != ManagedRuntimeEntitlementErrorCode {
		t.Fatalf("error_code = %v, want %s", got, ManagedRuntimeEntitlementErrorCode)
	}
	if got := details["reason_code"]; got != EntitlementReasonFeatureDisabled {
		t.Fatalf("reason_code = %v, want %s", got, EntitlementReasonFeatureDisabled)
	}
	if got := details["provider_id"]; got != ProviderIONOS {
		t.Fatalf("provider_id = %v, want %s", got, ProviderIONOS)
	}
	missing, ok := details["missing_features"].([]string)
	if !ok || strings.Join(missing, ",") != FeatureTechStackManagedRuntimeIONOS {
		t.Fatalf("missing_features = %#v, want provider feature", details["missing_features"])
	}
	guidance, ok := details["user_guidance"].(map[string]any)
	if !ok || strings.TrimSpace(fmt.Sprint(guidance["body"])) == "" {
		t.Fatalf("user_guidance = %#v, want body", details["user_guidance"])
	}
}

func TestActionRuntimeFailuresPreserveLeaseProjection(t *testing.T) {
	legacyMissingErr := errors.New("legacy managed runtime record not found")
	for _, test := range []struct {
		name         string
		action       serverruntime.RuntimeAction
		runtimeErr   error
		metadata     map[string]string
		rejectVMGone bool
	}{
		{
			name: "legacy SSH inventory missing", action: serverruntime.RuntimeActionSSHInfo, runtimeErr: legacyMissingErr,
			metadata: map[string]string{"runtime_ssh_host": "203.0.113.99", "node_public_ip": "203.0.113.99", "runtime_ssh_user": "root"},
		},
		{name: "legacy decommission inventory missing", action: serverruntime.RuntimeActionDecommission, runtimeErr: legacyMissingErr},
		{
			name: "transient provider node missing", action: serverruntime.RuntimeActionStatus, rejectVMGone: true,
			runtimeErr: errors.New(`simulate runtime status returned 502: {"error":{"code":502,"message":"status centron-managed node: node not found: 44078"}}`),
			metadata:   map[string]string{"runtime_ssh_host": "203.0.113.77"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			now := time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC)
			leases := vmleases.NewService(vmleases.NewMemoryStore(), vmleases.ServiceConfig{Now: func() time.Time { return now }, SnapshotSecret: []byte("secret")})
			lease := testMonthlyLease(now, enrollmentStatusEnrolled)
			for key, value := range test.metadata {
				lease.Metadata[key] = value
			}
			if _, err := leases.CreateOrUpdate(t.Context(), vmleases.CreateRequest{Lease: lease}); err != nil {
				t.Fatalf("CreateOrUpdate: %v", err)
			}
			svc := &Service{Leases: nativeLeaseService(leases), Runtime: &fakeRuntimeClient{err: test.runtimeErr}}

			_, err := svc.Action(t.Context(), ActionRequest{
				TenantID: "org-1", UserID: "user-1", LeaseID: "lease-1", Action: test.action, Internal: true,
			})
			if err == nil {
				t.Fatal("Action error = nil, want runtime failure")
			}
			if test.rejectVMGone && errors.Is(err, ErrRuntimeVMGone) {
				t.Fatalf("Action error = %v, transient provider lookup must not be classified as VM gone", err)
			}
			stored, getErr := leases.Get(t.Context(), "org-1", "lease-1")
			if getErr != nil {
				t.Fatalf("Get: %v", getErr)
			}
			if got := stored.Metadata["runtime_enrollment_status"]; got != enrollmentStatusEnrolled {
				t.Fatalf("runtime_enrollment_status = %q, want enrolled projection preserved", got)
			}
			if stored.CancelledAt != nil {
				t.Fatalf("failed action cancelled lease: %+v", stored)
			}
			for key, want := range test.metadata {
				if got := stored.Metadata[key]; got != want {
					t.Fatalf("metadata[%q] = %q, want %q preserved", key, got, want)
				}
			}
		})
	}
}
