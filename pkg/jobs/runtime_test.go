package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kombifyio/go-common/servicecall"
	"github.com/kombifyio/techstack/internal/providercatalog"
	"github.com/kombifyio/techstack/internal/runtimeproduct/runtimeaction"
	"github.com/kombifyio/techstack/internal/runtimeproduct/serverruntime"
	"github.com/kombifyio/techstack/internal/runtimeproduct/vmlease"
	"github.com/kombifyio/techstack/pkg/auth"
	"github.com/kombifyio/techstack/pkg/monthlyruntime"
	"github.com/kombifyio/techstack/pkg/vmleases"
)

type fakeLeaseAuthority struct {
	requests []vmleases.CreateRequest
}

type fakeMonthlyRuntimeClient struct{}

// nativeRuntimeLeaseAuthority is a test-only authority projection. Production
// authority is immutable database state; tests deliberately do not gain a
// public mutator merely to manufacture a native binding.
type nativeRuntimeLeaseAuthority struct {
	*vmleases.Service
}

func (a nativeRuntimeLeaseAuthority) GetInventory(ctx context.Context, tenantID string, leaseID vmlease.LeaseID) (*vmleases.LeaseInventoryRecord, error) {
	lease, err := a.Get(ctx, tenantID, leaseID)
	if err != nil {
		return nil, err
	}
	state := vmleases.LeaseAuthorityStateNativeActive
	if lease.CancelledAt != nil || lease.DesiredState != vmlease.DesiredStateRunning {
		state = vmleases.LeaseAuthorityStateNativeInactive
	}
	return &vmleases.LeaseInventoryRecord{
		Lease:              *lease,
		ExecutionAuthority: vmleases.LeaseExecutionAuthorityTechStackProviderControl,
		AuthorityState:     state,
	}, nil
}

func (f *fakeLeaseAuthority) CreateOrUpdate(_ context.Context, req vmleases.CreateRequest) (*vmlease.Lease, error) {
	f.requests = append(f.requests, req)
	lease := req.Lease
	return &lease, nil
}

func (fakeMonthlyRuntimeClient) RuntimeAction(_ context.Context, req serverruntime.LeaseRuntimeActionRequest) (*serverruntime.LeaseRuntimeActionResponse, error) {
	return &serverruntime.LeaseRuntimeActionResponse{
		TenantID: req.TenantID,
		LeaseID:  req.LeaseID,
		Action:   req.Action,
		Status:   &serverruntime.NodeStatus{ID: "node-1", State: "running"},
	}, nil
}

type recordingMonthlyRuntimeClient struct {
	requests []serverruntime.LeaseRuntimeActionRequest
}

func (f *recordingMonthlyRuntimeClient) RuntimeAction(_ context.Context, req serverruntime.LeaseRuntimeActionRequest) (*serverruntime.LeaseRuntimeActionResponse, error) {
	f.requests = append(f.requests, req)
	return &serverruntime.LeaseRuntimeActionResponse{
		TenantID:      req.TenantID,
		LeaseID:       req.LeaseID,
		Action:        req.Action,
		OfferingID:    req.OfferingID,
		ObservedState: "not_found",
		LeaseState:    "cancelled",
		Status:        &serverruntime.NodeStatus{ID: req.Metadata["engine_vm_id"], State: "not_found"},
	}, nil
}

type sshInfoMonthlyRuntimeClient struct {
	requests []serverruntime.LeaseRuntimeActionRequest
	resp     *serverruntime.LeaseRuntimeActionResponse
	err      error
}

func (f *sshInfoMonthlyRuntimeClient) RuntimeAction(_ context.Context, req serverruntime.LeaseRuntimeActionRequest) (*serverruntime.LeaseRuntimeActionResponse, error) {
	f.requests = append(f.requests, req)
	if f.err != nil {
		return nil, f.err
	}
	if f.resp != nil {
		return f.resp, nil
	}
	return &serverruntime.LeaseRuntimeActionResponse{TenantID: req.TenantID, LeaseID: req.LeaseID, Action: req.Action}, nil
}

func TestVMLeaseManagerAdapterCreatesMonthlyRuntimeLease(t *testing.T) {
	now := time.Date(2026, 5, 12, 10, 0, 0, 0, time.UTC)
	authority := &fakeLeaseAuthority{}
	adapter := NewVMLeaseManagerAdapter(authority)
	adapter.Now = func() time.Time { return now }

	result, err := adapter.CreateOrBindLease(context.Background(), ManagedLeaseRequest{
		StackID:   "stack-1",
		StackName: "Stack 1",
		StackKit:  DefaultBasementKitRef,
		TenantID:  "org-1",
		OwnerID:   "user-1",
		Provider:  DefaultMonthlyRuntimeProvider,
		Metadata: map[string]string{
			metadataKeyServerMode: serverModeManagedCloud,
		},
	})
	if err != nil {
		t.Fatalf("CreateOrBindLease: %v", err)
	}
	if result.Provider != DefaultMonthlyRuntimeProvider {
		t.Fatalf("Provider = %q, want %q", result.Provider, DefaultMonthlyRuntimeProvider)
	}
	if len(authority.requests) != 1 {
		t.Fatalf("requests = %d, want 1", len(authority.requests))
	}
	req := authority.requests[0]
	if req.Lease.Resource.ProviderID != DefaultMonthlyRuntimeProvider {
		t.Fatalf("lease provider = %q", req.Lease.Resource.ProviderID)
	}
	if req.Lease.Metadata[metadataKeyServerMode] != serverModeMonthlyRuntime {
		t.Fatalf("server_mode = %q, want monthly-runtime", req.Lease.Metadata[metadataKeyServerMode])
	}
	if req.Lease.Metadata[metadataKeyRuntimeLane] != serverModeMonthlyRuntime {
		t.Fatalf("runtime_lane = %q, want monthly-runtime", req.Lease.Metadata[metadataKeyRuntimeLane])
	}
	if req.Lease.Metadata[metadataKeyProviderID] != DefaultMonthlyRuntimeProvider {
		t.Fatalf("provider_id = %q", req.Lease.Metadata[metadataKeyProviderID])
	}
	if req.Lease.Metadata[metadataKeyLeaseProvider] != "" || req.Lease.Metadata[metadataKeySimulateProviderID] != "" {
		t.Fatalf("fresh lease emitted legacy provider fields: %+v", req.Lease.Metadata)
	}
	if req.Lease.Metadata[metadataKeySimulateLifecycle] != simulateLifecyclePVM {
		t.Fatalf("simulate_node_lifecycle = %q", req.Lease.Metadata[metadataKeySimulateLifecycle])
	}
	if req.Lease.Metadata[metadataKeyBillingCadence] != billingCadenceMonthly {
		t.Fatalf("billing_cadence = %q", req.Lease.Metadata[metadataKeyBillingCadence])
	}
}

func TestVMLeaseManagerAdapterRejectsLegacyProviderBeforeAuthorityWrite(t *testing.T) {
	t.Parallel()

	tests := []ManagedLeaseRequest{
		{StackID: "stack-alias", TenantID: "org-1", OwnerID: "user-1", Provider: "ionos-managed"},
		{StackID: "stack-legacy-field", TenantID: "org-1", OwnerID: "user-1", Provider: "ionos", Metadata: map[string]string{metadataKeyLeaseProvider: "ionos-managed"}},
	}
	for _, request := range tests {
		authority := &fakeLeaseAuthority{}
		adapter := NewVMLeaseManagerAdapter(authority)
		if _, err := adapter.CreateOrBindLease(context.Background(), request); err == nil {
			t.Fatalf("CreateOrBindLease(%+v) succeeded", request)
		}
		if len(authority.requests) != 0 {
			t.Fatalf("authority writes = %d, want zero", len(authority.requests))
		}
	}

	if _, err := providercatalog.CanonicalProviderID("ionos-managed"); !errors.Is(err, providercatalog.ErrCompositeProviderID) {
		t.Fatalf("alias policy error = %v", err)
	}
}

func TestVMLeaseManagerAdapterCreatesIndependentIONOSLease(t *testing.T) {
	now := time.Date(2026, 5, 12, 10, 0, 0, 0, time.UTC)
	authority := &fakeLeaseAuthority{}
	adapter := NewVMLeaseManagerAdapter(authority)
	adapter.Now = func() time.Time { return now }

	result, err := adapter.CreateOrBindLease(context.Background(), ManagedLeaseRequest{
		StackID:   "stack-ionos",
		StackName: "BasementKit IONOS",
		StackKit:  DefaultBasementKitRef,
		TenantID:  "org-1",
		OwnerID:   "user-1",
		Provider:  "ionos",
		Metadata: map[string]string{
			metadataKeyProviderID:        "ionos",
			metadataKeyIONOSDatacenter:   "us-ewr",
			metadataKeyRuntimeOfferingID: defaultRuntimeOfferingID,
		},
	})
	if err != nil {
		t.Fatalf("CreateOrBindLease: %v", err)
	}
	if result.Provider != "ionos" {
		t.Fatalf("Provider = %q, want ionos", result.Provider)
	}
	if len(authority.requests) != 1 {
		t.Fatalf("requests = %d, want 1", len(authority.requests))
	}
	req := authority.requests[0]
	if req.Lease.ID != "lease-stack-ionos" {
		t.Fatalf("lease id = %q, want lease-stack-ionos", req.Lease.ID)
	}
	if req.IdempotencyKey != "org-1:stack-ionos:main" {
		t.Fatalf("idempotency key = %q", req.IdempotencyKey)
	}
	if req.Lease.Resource.ProviderID != "ionos" {
		t.Fatalf("lease provider = %q", req.Lease.Resource.ProviderID)
	}
	if req.Lease.Resource.Region != "us/ewr" {
		t.Fatalf("lease region = %q, want us/ewr", req.Lease.Resource.Region)
	}
	if req.Lease.Metadata[metadataKeyIONOSDatacenter] != "us/ewr" || req.Lease.Metadata[metadataKeyProviderRegion] != "us/ewr" {
		t.Fatalf("lease metadata = %+v, want IONOS region us/ewr", req.Lease.Metadata)
	}
	if req.Lease.Metadata[metadataKeyScenarioID] != "stack-ionos:ionos" {
		t.Fatalf("scenario_id = %q", req.Lease.Metadata[metadataKeyScenarioID])
	}
}

func TestVMLeaseManagerAdapterPreservesPremiumOfferingSelection(t *testing.T) {
	now := time.Date(2026, 5, 12, 10, 0, 0, 0, time.UTC)
	authority := &fakeLeaseAuthority{}
	adapter := NewVMLeaseManagerAdapter(authority)
	adapter.Now = func() time.Time { return now }

	_, err := adapter.CreateOrBindLease(context.Background(), ManagedLeaseRequest{
		StackID:   "stack-premium",
		StackName: "Premium Stack",
		TenantID:  "org-1",
		OwnerID:   "user-1",
		Provider:  DefaultMonthlyRuntimeProvider,
		Metadata: map[string]string{
			"runtime_offering_id": "monthly-runtime-premium",
		},
	})
	if err != nil {
		t.Fatalf("CreateOrBindLease: %v", err)
	}
	if len(authority.requests) != 1 {
		t.Fatalf("requests = %d, want 1", len(authority.requests))
	}
	lease := authority.requests[0].Lease
	if lease.Metadata["runtime_offering_id"] != "monthly-runtime-premium" {
		t.Fatalf("metadata = %+v, want premium offering", lease.Metadata)
	}
}

func TestVMLeaseManagerAdapterExtractsManagedRuntimeTargetMetadata(t *testing.T) {
	authority := &fakeLeaseAuthority{}
	adapter := NewVMLeaseManagerAdapter(authority)

	result, err := adapter.CreateOrBindLease(context.Background(), ManagedLeaseRequest{
		StackID:   "stack-address",
		StackName: "Address Stack",
		TenantID:  "org-1",
		OwnerID:   "user-1",
		Provider:  DefaultMonthlyRuntimeProvider,
		Metadata: map[string]string{
			metadataKeyRuntimeSSHHost:  "203.0.113.20",
			metadataKeyRuntimePublicIP: "203.0.113.20",
			metadataKeyRuntimeSSHUser:  "ubuntu",
			metadataKeyRuntimeSSHPort:  "2222",
		},
	})
	if err != nil {
		t.Fatalf("CreateOrBindLease: %v", err)
	}
	if result.Target == nil {
		t.Fatal("expected managed runtime target")
	}
	if result.Target.Host != "203.0.113.20" || result.Target.PublicIP != "203.0.113.20" {
		t.Fatalf("target address = %+v, want metadata address", result.Target)
	}
	if result.Target.SSHUser != "ubuntu" || result.Target.SSHPort != 2222 {
		t.Fatalf("target ssh = %+v, want metadata ssh", result.Target)
	}
}

func TestVMLeaseManagerAdapterDecommissionsStackManagedLeasesForBothProviders(t *testing.T) {
	now := time.Date(2026, 5, 12, 10, 0, 0, 0, time.UTC)
	leases := vmleases.NewService(vmleases.NewMemoryStore(), vmleases.ServiceConfig{Now: func() time.Time { return now }, SnapshotSecret: []byte("secret")})
	createRuntimeTestLease(t, leases, "lease-centron", "centron", "stack-1")
	createRuntimeTestLease(t, leases, "lease-ionos", "ionos", "stack-1")
	createRuntimeTestLease(t, leases, "lease-other-stack", "centron", "stack-2")
	runtime := &recordingMonthlyRuntimeClient{}
	adapter := NewVMLeaseManagerAdapter(nativeRuntimeLeaseAuthority{Service: leases})
	adapter.Runtime = runtime

	result, err := adapter.DecommissionManagedLeases(context.Background(), ManagedLeaseDecommissionRequest{
		StackID:  "stack-1",
		TenantID: "org-1",
		OwnerID:  "user-1",
	})
	if err != nil {
		t.Fatalf("DecommissionManagedLeases: %v", err)
	}
	if result.Decommissioned != 2 {
		t.Fatalf("Decommissioned = %d, want 2 (%+v)", result.Decommissioned, result)
	}
	gotLeaseIDs := map[string]bool{}
	for _, leaseID := range result.LeaseIDs {
		gotLeaseIDs[leaseID] = true
	}
	if !gotLeaseIDs["lease-centron"] || !gotLeaseIDs["lease-ionos"] || len(gotLeaseIDs) != 2 {
		t.Fatalf("LeaseIDs = %v, want centron and ionos leases", result.LeaseIDs)
	}
	if len(runtime.requests) != 2 {
		t.Fatalf("runtime requests = %d, want 2", len(runtime.requests))
	}
	for _, leaseID := range []vmlease.LeaseID{"lease-centron", "lease-ionos"} {
		stored, err := leases.Get(context.Background(), "org-1", leaseID)
		if err != nil {
			t.Fatalf("stored lease %s: %v", leaseID, err)
		}
		if stored.CancelledAt == nil || stored.DesiredState != vmlease.DesiredStateStopped {
			t.Fatalf("stored lease %s = %+v, want cancelled/stopped", leaseID, stored)
		}
	}
	other, err := leases.Get(context.Background(), "org-1", "lease-other-stack")
	if err != nil {
		t.Fatalf("stored other lease: %v", err)
	}
	if other.CancelledAt != nil {
		t.Fatalf("other stack lease was cancelled: %+v", other)
	}
}

func TestVMLeaseManagerAdapterReconcilesOnlyExactClaimedCancelledGeneration(t *testing.T) {
	now := time.Date(2026, 5, 12, 10, 0, 0, 0, time.UTC)
	store := vmleases.NewMemoryStore()
	leases := vmleases.NewService(store, vmleases.ServiceConfig{Now: func() time.Time { return now }, SnapshotSecret: []byte("secret")})
	createRuntimeTestLease(t, leases, "lease-target", "centron", "stack-1")
	createRuntimeTestLease(t, leases, "lease-same-stack", "ionos", "stack-1")
	target, err := leases.Get(t.Context(), "org-1", "lease-target")
	if err != nil {
		t.Fatalf("Get target: %v", err)
	}
	digest, err := vmleases.ResourceGenerationDigest("org-1", *target)
	if err != nil {
		t.Fatalf("ResourceGenerationDigest: %v", err)
	}
	if _, patchErr := leases.Patch(t.Context(), "org-1", target.ID, vmleases.PatchRequest{
		ExpectedResourceGenerationDigest: digest,
		ClaimDecommission:                true,
	}); patchErr != nil {
		t.Fatalf("claim target: %v", patchErr)
	}
	if _, patchErr := leases.Patch(t.Context(), "org-1", target.ID, vmleases.PatchRequest{
		Cancel:                           true,
		ExpectedResourceGenerationDigest: digest,
	}); patchErr != nil {
		t.Fatalf("cancel target: %v", patchErr)
	}
	runtime := &recordingMonthlyRuntimeClient{}
	adapter := NewVMLeaseManagerAdapter(nativeRuntimeLeaseAuthority{Service: leases})
	adapter.Runtime = runtime

	result, err := adapter.DecommissionManagedLeases(t.Context(), ManagedLeaseDecommissionRequest{
		StackID:                  "stack-1",
		TenantID:                 "org-1",
		OwnerID:                  "user-1",
		LeaseID:                  "lease-target",
		ResourceGenerationDigest: digest,
	})
	if err != nil {
		t.Fatalf("DecommissionManagedLeases: %v", err)
	}
	if result.Decommissioned != 1 || len(result.LeaseIDs) != 1 || result.LeaseIDs[0] != "lease-target" {
		t.Fatalf("result = %+v, want only exact claimed target", result)
	}
	if len(runtime.requests) != 1 || runtime.requests[0].LeaseID != "lease-target" {
		t.Fatalf("runtime requests = %+v, want exact target only", runtime.requests)
	}
	other, err := leases.Get(t.Context(), "org-1", "lease-same-stack")
	if err != nil {
		t.Fatalf("Get same-stack lease: %v", err)
	}
	if other.CancelledAt != nil {
		t.Fatalf("explicit reconciliation widened to same-stack lease: %+v", other)
	}
	events, err := leases.ListOperations(t.Context(), "org-1", target.ID, 10)
	if err != nil {
		t.Fatalf("ListOperations: %v", err)
	}
	if len(events) != 1 || events[0].EventType != vmleases.OperationEventDecommission || events[0].ResourceGenerationDigest != digest {
		t.Fatalf("reconciliation journal = %+v, want exact durable decommission proof", events)
	}
}

func TestVMLeaseManagerAdapterRejectsWrongClaimBeforeRuntime(t *testing.T) {
	now := time.Date(2026, 5, 12, 10, 0, 0, 0, time.UTC)
	leases := vmleases.NewService(vmleases.NewMemoryStore(), vmleases.ServiceConfig{Now: func() time.Time { return now }})
	createRuntimeTestLease(t, leases, "lease-target", "centron", "stack-1")
	runtime := &recordingMonthlyRuntimeClient{}
	adapter := NewVMLeaseManagerAdapter(nativeRuntimeLeaseAuthority{Service: leases})
	adapter.Runtime = runtime

	_, err := adapter.DecommissionManagedLeases(t.Context(), ManagedLeaseDecommissionRequest{
		StackID:                  "stack-1",
		TenantID:                 "org-1",
		OwnerID:                  "user-1",
		LeaseID:                  "lease-target",
		ResourceGenerationDigest: strings.Repeat("a", 64),
	})
	if !errors.Is(err, vmleases.ErrResourceGenerationSuperseded) {
		t.Fatalf("error = %v, want ErrResourceGenerationSuperseded", err)
	}
	if len(runtime.requests) != 0 {
		t.Fatalf("runtime requests = %+v, wrong claim must fail before provider call", runtime.requests)
	}
}

func createRuntimeTestLease(t *testing.T, leases *vmleases.Service, id, provider, stackID string) {
	t.Helper()
	now := time.Date(2026, 5, 12, 10, 0, 0, 0, time.UTC)
	metadata := monthlyruntime.NormalizeMetadata(map[string]string{
		metadataKeyStackID:            stackID,
		metadataKeyProviderID:         provider,
		metadataKeyRuntimeEnrollState: runtimeEnrollmentStatusPending,
	}, serverruntime.RuntimeOfferingStandard)
	if _, err := leases.CreateOrUpdate(context.Background(), vmleases.CreateRequest{Lease: vmlease.Lease{
		ID:             vmlease.LeaseID(id),
		Subject:        vmlease.Subject{Kind: vmlease.SubjectUser, ID: "user-1", OrgID: "org-1"},
		Resource:       vmlease.ResourceRef{ProviderID: provider, Region: defaultLeaseRegion, EngineVMID: "node-" + id},
		DesiredState:   vmlease.DesiredStateRunning,
		BillingMode:    vmlease.BillingModeSubscription,
		LifecycleClass: vmlease.LifecycleClassSubscription,
		RestartPolicy:  vmlease.RestartPolicyOnUnexpectedStop,
		RecreatePolicy: vmlease.RecreatePolicyManual,
		ValidFrom:      now.Add(-time.Minute),
		ValidUntil:     now.Add(time.Hour),
		RenewedAt:      now,
		Metadata:       metadata,
	}}); err != nil {
		t.Fatalf("CreateOrUpdate(%s): %v", id, err)
	}
}

func TestStaticManagedRuntimeTargetResolverFromEnv(t *testing.T) {
	t.Setenv("TECHSTACK_DEV_MONTHLY_RUNTIME_TARGET_HOST", "stackkits-vm")
	t.Setenv("TECHSTACK_DEV_MONTHLY_RUNTIME_TARGET_SSH_USER", "root")
	t.Setenv("TECHSTACK_DEV_MONTHLY_RUNTIME_TARGET_SSH_PORT", "2222")
	t.Setenv("TECHSTACK_DEV_MONTHLY_RUNTIME_TARGET_DOCKER_HOST", "tcp://techstack-local-runtime:2375")

	resolver := NewStaticManagedRuntimeTargetResolverFromEnv()
	if resolver == nil {
		t.Fatal("expected static managed runtime target resolver")
	}
	target, err := resolver.ResolveManagedRuntimeTarget(context.Background(), ManagedRuntimeTargetRequest{
		TenantID: "user-1",
		OwnerID:  "user-1",
		LeaseID:  "lease-1",
	})
	if err != nil {
		t.Fatalf("ResolveManagedRuntimeTarget: %v", err)
	}
	if target.Host != "stackkits-vm" || target.PublicIP != "stackkits-vm" {
		t.Fatalf("target host = %+v, want stackkits-vm", target)
	}
	if target.SSHUser != "root" || target.SSHPort != 2222 || target.Source != "dev-static-target" {
		t.Fatalf("target ssh/source = %+v", target)
	}
	if target.DockerHost != "tcp://techstack-local-runtime:2375" {
		t.Fatalf("target docker host = %q, want local runtime Docker host", target.DockerHost)
	}
}

func TestMonthlyRuntimeTargetResolverReturnsEnrollmentFailureCause(t *testing.T) {
	now := time.Date(2026, 5, 25, 13, 0, 0, 0, time.UTC)
	leases := vmleases.NewService(vmleases.NewMemoryStore(), vmleases.ServiceConfig{
		Now: func() time.Time { return now },
	})
	_, err := leases.CreateOrUpdate(context.Background(), vmleases.CreateRequest{Lease: vmlease.Lease{
		ID:             "lease-ionos-failed",
		Subject:        vmlease.Subject{Kind: vmlease.SubjectUser, ID: "user-1", OrgID: "org-1"},
		Resource:       vmlease.ResourceRef{ProviderID: "ionos", EngineVMID: "node-ionos-failed", Region: defaultLeaseRegion},
		DesiredState:   vmlease.DesiredStateRunning,
		BillingMode:    vmlease.BillingModeSubscription,
		LifecycleClass: vmlease.LifecycleClassSubscription,
		RestartPolicy:  vmlease.RestartPolicyOnUnexpectedStop,
		RecreatePolicy: vmlease.RecreatePolicyManual,
		ValidFrom:      now.Add(-time.Minute),
		ValidUntil:     now.Add(24 * time.Hour),
		RenewedAt:      now,
		Metadata: map[string]string{
			metadataKeyServerMode:         serverModeMonthlyRuntime,
			metadataKeyRuntimeLane:        serverModeMonthlyRuntime,
			metadataKeyRuntimeOfferingID:  defaultRuntimeOfferingID,
			metadataKeyRuntimeEnrollState: runtimeEnrollmentStatusFailed,
			metadataKeyRuntimeEnrollError: "ionos quota exhausted",
		},
	}})
	if err != nil {
		t.Fatalf("CreateOrUpdate: %v", err)
	}
	resolver := NewMonthlyRuntimeTargetResolver(&monthlyruntime.Service{
		Leases:  nativeRuntimeLeaseAuthority{Service: leases},
		Runtime: fakeMonthlyRuntimeClient{},
	})

	_, err = resolver.ResolveManagedRuntimeTarget(context.Background(), ManagedRuntimeTargetRequest{
		TenantID: "org-1",
		OwnerID:  "user-1",
		LeaseID:  "lease-ionos-failed",
		Provider: "ionos",
	})
	if !errors.Is(err, ErrManagedRuntimeEnrollmentFailed) {
		t.Fatalf("ResolveManagedRuntimeTarget error = %v, want terminal enrollment failure", err)
	}
	if !strings.Contains(err.Error(), "ionos quota exhausted") {
		t.Fatalf("ResolveManagedRuntimeTarget error = %v, want provider cause", err)
	}
}

func TestMonthlyRuntimeTargetResolverReturnsRetryingEnrollmentCause(t *testing.T) {
	now := time.Date(2026, 5, 25, 13, 30, 0, 0, time.UTC)
	leases := vmleases.NewService(vmleases.NewMemoryStore(), vmleases.ServiceConfig{
		Now: func() time.Time { return now },
	})
	_, err := leases.CreateOrUpdate(context.Background(), vmleases.CreateRequest{Lease: vmlease.Lease{
		ID:             "lease-centron-retrying",
		Subject:        vmlease.Subject{Kind: vmlease.SubjectUser, ID: "user-1", OrgID: "org-1"},
		Resource:       vmlease.ResourceRef{ProviderID: "centron", EngineVMID: "node-centron-retrying", Region: defaultLeaseRegion},
		DesiredState:   vmlease.DesiredStateRunning,
		BillingMode:    vmlease.BillingModeSubscription,
		LifecycleClass: vmlease.LifecycleClassSubscription,
		RestartPolicy:  vmlease.RestartPolicyOnUnexpectedStop,
		RecreatePolicy: vmlease.RecreatePolicyManual,
		ValidFrom:      now.Add(-time.Minute),
		ValidUntil:     now.Add(24 * time.Hour),
		RenewedAt:      now,
		Metadata: map[string]string{
			metadataKeyServerMode:         serverModeMonthlyRuntime,
			metadataKeyRuntimeLane:        serverModeMonthlyRuntime,
			metadataKeyRuntimeOfferingID:  defaultRuntimeOfferingID,
			metadataKeyRuntimeEnrollState: runtimeEnrollmentStatusRetrying,
			metadataKeyRuntimeEnrollError: "centron API request timed out",
		},
	}})
	if err != nil {
		t.Fatalf("CreateOrUpdate: %v", err)
	}
	resolver := NewMonthlyRuntimeTargetResolver(&monthlyruntime.Service{
		Leases:  nativeRuntimeLeaseAuthority{Service: leases},
		Runtime: fakeMonthlyRuntimeClient{},
	})

	_, err = resolver.ResolveManagedRuntimeTarget(context.Background(), ManagedRuntimeTargetRequest{
		TenantID: "org-1",
		OwnerID:  "user-1",
		LeaseID:  "lease-centron-retrying",
		Provider: "centron",
	})
	if err == nil {
		t.Fatal("expected enrollment pending error")
	}
	if errors.Is(err, ErrManagedRuntimeEnrollmentFailed) {
		t.Fatalf("ResolveManagedRuntimeTarget error = %v, want non-terminal retrying cause", err)
	}
	if !strings.Contains(err.Error(), "centron API request timed out") {
		t.Fatalf("ResolveManagedRuntimeTarget error = %v, want retrying provider cause", err)
	}
}

func TestMonthlyRuntimeTargetResolverWaitsWhenLeaseHasNoAddress(t *testing.T) {
	now := time.Date(2026, 5, 25, 13, 45, 0, 0, time.UTC)
	leases := vmleases.NewService(vmleases.NewMemoryStore(), vmleases.ServiceConfig{
		Now: func() time.Time { return now },
	})
	_, err := leases.CreateOrUpdate(context.Background(), vmleases.CreateRequest{Lease: vmlease.Lease{
		ID:             "lease-centron-pending",
		Subject:        vmlease.Subject{Kind: vmlease.SubjectUser, ID: "user-1", OrgID: "org-1"},
		Resource:       vmlease.ResourceRef{ProviderID: "centron", EngineVMID: "node-centron-pending", Region: defaultLeaseRegion},
		DesiredState:   vmlease.DesiredStateRunning,
		BillingMode:    vmlease.BillingModeSubscription,
		LifecycleClass: vmlease.LifecycleClassSubscription,
		RestartPolicy:  vmlease.RestartPolicyOnUnexpectedStop,
		RecreatePolicy: vmlease.RecreatePolicyManual,
		ValidFrom:      now.Add(-time.Minute),
		ValidUntil:     now.Add(24 * time.Hour),
		RenewedAt:      now,
		Metadata: map[string]string{
			metadataKeyServerMode:         serverModeMonthlyRuntime,
			metadataKeyRuntimeLane:        serverModeMonthlyRuntime,
			metadataKeyRuntimeOfferingID:  defaultRuntimeOfferingID,
			metadataKeyRuntimeEnrollState: runtimeEnrollmentStatusPending,
		},
	}})
	if err != nil {
		t.Fatalf("CreateOrUpdate: %v", err)
	}
	resolver := NewMonthlyRuntimeTargetResolver(&monthlyruntime.Service{
		Leases:  nativeRuntimeLeaseAuthority{Service: leases},
		Runtime: fakeMonthlyRuntimeClient{},
	})

	_, err = resolver.ResolveManagedRuntimeTarget(context.Background(), ManagedRuntimeTargetRequest{
		TenantID: "org-1",
		OwnerID:  "user-1",
		LeaseID:  "lease-centron-pending",
		Provider: "centron",
	})
	if !errors.Is(err, monthlyruntime.ErrEnrollmentPending) {
		t.Fatalf("ResolveManagedRuntimeTarget error = %v, want enrollment pending", err)
	}
	if !strings.Contains(err.Error(), "enrollment pending") {
		t.Fatalf("ResolveManagedRuntimeTarget error = %v, want pending context", err)
	}
}

func TestMonthlyRuntimeTargetResolverUsesLeaseMetadataAddressBeforeEnrollmentStatus(t *testing.T) {
	now := time.Date(2026, 5, 25, 14, 0, 0, 0, time.UTC)
	leases := vmleases.NewService(vmleases.NewMemoryStore(), vmleases.ServiceConfig{
		Now: func() time.Time { return now },
	})
	_, err := leases.CreateOrUpdate(context.Background(), vmleases.CreateRequest{Lease: vmlease.Lease{
		ID:             "lease-centron-address",
		Subject:        vmlease.Subject{Kind: vmlease.SubjectUser, ID: "user-1", OrgID: "org-1"},
		Resource:       vmlease.ResourceRef{ProviderID: "centron", EngineVMID: "node-centron-address", Region: defaultLeaseRegion},
		DesiredState:   vmlease.DesiredStateRunning,
		BillingMode:    vmlease.BillingModeSubscription,
		LifecycleClass: vmlease.LifecycleClassSubscription,
		RestartPolicy:  vmlease.RestartPolicyOnUnexpectedStop,
		RecreatePolicy: vmlease.RecreatePolicyManual,
		ValidFrom:      now.Add(-time.Minute),
		ValidUntil:     now.Add(24 * time.Hour),
		RenewedAt:      now,
		Metadata: map[string]string{
			metadataKeyServerMode:         serverModeMonthlyRuntime,
			metadataKeyRuntimeLane:        serverModeMonthlyRuntime,
			metadataKeyRuntimeOfferingID:  defaultRuntimeOfferingID,
			metadataKeyRuntimeEnrollState: runtimeEnrollmentStatusPending,
			metadataKeyRuntimeSSHHost:     "203.0.113.50",
			metadataKeyRuntimeSSHUser:     "ubuntu",
			metadataKeyRuntimeSSHPort:     "22",
			"runtime_docker_host":         "tcp://techstack-local-runtime:2375",
		},
	}})
	if err != nil {
		t.Fatalf("CreateOrUpdate: %v", err)
	}
	resolver := NewMonthlyRuntimeTargetResolver(&monthlyruntime.Service{
		Leases:  nativeRuntimeLeaseAuthority{Service: leases},
		Runtime: fakeMonthlyRuntimeClient{},
	})

	target, err := resolver.ResolveManagedRuntimeTarget(context.Background(), ManagedRuntimeTargetRequest{
		TenantID: "org-1",
		OwnerID:  "user-1",
		LeaseID:  "lease-centron-address",
		Provider: "centron",
	})
	if err != nil {
		t.Fatalf("ResolveManagedRuntimeTarget: %v", err)
	}
	if target.Host != "203.0.113.50" || target.SSHUser != "ubuntu" || target.SSHPort != 22 || target.DockerHost == "" {
		t.Fatalf("target = %+v, want credentialed lease metadata address", target)
	}
}

func TestMonthlyRuntimeTargetResolverFetchesSSHCredentialsWhenMetadataHasAddressOnly(t *testing.T) {
	now := time.Date(2026, 6, 12, 13, 0, 0, 0, time.UTC)
	leases := vmleases.NewService(vmleases.NewMemoryStore(), vmleases.ServiceConfig{
		Now: func() time.Time { return now },
	})
	metadata := monthlyruntime.NormalizeMetadata(map[string]string{
		metadataKeyProviderID:         "ionos",
		metadataKeyServerMode:         serverModeMonthlyRuntime,
		metadataKeyRuntimeLane:        serverModeMonthlyRuntime,
		metadataKeyRuntimeOfferingID:  defaultRuntimeOfferingID,
		metadataKeyRuntimeEnrollState: "enrolled",
		metadataKeyRuntimeSSHHost:     "203.0.113.51",
		metadataKeyRuntimeSSHUser:     "root",
		metadataKeyRuntimeSSHPort:     "22",
	}, serverruntime.RuntimeOfferingStandard)
	_, err := leases.CreateOrUpdate(context.Background(), vmleases.CreateRequest{Lease: vmlease.Lease{
		ID:             "lease-ionos-address-only",
		Subject:        vmlease.Subject{Kind: vmlease.SubjectUser, ID: "user-1", OrgID: "org-1"},
		Resource:       vmlease.ResourceRef{ProviderID: "ionos", EngineVMID: "node-ionos-address", Region: defaultLeaseRegion},
		DesiredState:   vmlease.DesiredStateRunning,
		BillingMode:    vmlease.BillingModeSubscription,
		LifecycleClass: vmlease.LifecycleClassSubscription,
		RestartPolicy:  vmlease.RestartPolicyOnUnexpectedStop,
		RecreatePolicy: vmlease.RecreatePolicyManual,
		ValidFrom:      now.Add(-time.Minute),
		ValidUntil:     now.Add(24 * time.Hour),
		RenewedAt:      now,
		Metadata:       metadata,
	}})
	if err != nil {
		t.Fatalf("CreateOrUpdate: %v", err)
	}
	client := &sshInfoMonthlyRuntimeClient{resp: &serverruntime.LeaseRuntimeActionResponse{
		TenantID: "org-1",
		LeaseID:  "lease-ionos-address-only",
		Action:   serverruntime.RuntimeActionSSHInfo,
		Status:   &serverruntime.NodeStatus{ID: "node-1", State: "running", PublicIP: "203.0.113.51"},
		SSH: &serverruntime.SSHInfo{
			Host:             "203.0.113.51",
			User:             "root",
			Port:             22,
			KeyPath:          "/data/ionos-ssh-keys/provider-local.pem",
			PrivateKey:       "test-private-key",
			ClientPrivateKey: "test-client-private-key",
		},
	}}
	resolver := NewMonthlyRuntimeTargetResolver(&monthlyruntime.Service{
		Leases:  nativeRuntimeLeaseAuthority{Service: leases},
		Runtime: client,
	})

	target, err := resolver.ResolveManagedRuntimeTarget(context.Background(), ManagedRuntimeTargetRequest{
		TenantID: "org-1",
		OwnerID:  "user-1",
		LeaseID:  "lease-ionos-address-only",
		Provider: "ionos",
	})
	if err != nil {
		t.Fatalf("ResolveManagedRuntimeTarget: %v", err)
	}
	if len(client.requests) != 2 ||
		client.requests[0].Action != serverruntime.RuntimeActionStatus ||
		client.requests[1].Action != serverruntime.RuntimeActionSSHInfo {
		t.Fatalf("runtime requests = %+v, want status prime then ssh-info request", client.requests)
	}
	if target.Source != "runtime-response" || target.SSHPrivateKey != "test-private-key" || target.SSHClientPrivateKey != "test-client-private-key" {
		t.Fatalf("target = %+v, want credentials from runtime response", target)
	}
}

func TestMonthlyRuntimeTargetResolverUsesEncryptedLeaseCredentialsBeforeSimulate(t *testing.T) {
	now := time.Date(2026, 7, 8, 9, 0, 0, 0, time.UTC)
	leases := vmleases.NewService(vmleases.NewMemoryStore(), vmleases.ServiceConfig{
		Now: func() time.Time { return now },
	})
	encryptor, err := auth.NewSecretEncryptor([]byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatalf("NewSecretEncryptor: %v", err)
	}
	encryptedClientKey, err := encryptor.Encrypt("test-client-private-key")
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	metadata := monthlyruntime.NormalizeMetadata(map[string]string{
		metadataKeyServerMode:         serverModeMonthlyRuntime,
		metadataKeyRuntimeLane:        serverModeMonthlyRuntime,
		metadataKeyRuntimeOfferingID:  defaultRuntimeOfferingID,
		metadataKeyRuntimeEnrollState: "enrolled",
		metadataKeyRuntimeSSHHost:     "203.0.113.71",
		metadataKeyRuntimeSSHUser:     "root",
		metadataKeyRuntimeSSHPort:     "22",
		metadataKeyRuntimeClientKey:   encryptedClientKey,
	}, serverruntime.RuntimeOfferingStandard)
	_, err = leases.CreateOrUpdate(context.Background(), vmleases.CreateRequest{Lease: vmlease.Lease{
		ID:             "lease-centron-encrypted-credential",
		Subject:        vmlease.Subject{Kind: vmlease.SubjectUser, ID: "user-1", OrgID: "org-1"},
		Resource:       vmlease.ResourceRef{ProviderID: "centron", EngineVMID: "node-centron-credential", Region: defaultLeaseRegion},
		DesiredState:   vmlease.DesiredStateRunning,
		BillingMode:    vmlease.BillingModeSubscription,
		LifecycleClass: vmlease.LifecycleClassSubscription,
		RestartPolicy:  vmlease.RestartPolicyOnUnexpectedStop,
		RecreatePolicy: vmlease.RecreatePolicyManual,
		ValidFrom:      now.Add(-time.Minute),
		ValidUntil:     now.Add(24 * time.Hour),
		RenewedAt:      now,
		Metadata:       metadata,
	}})
	if err != nil {
		t.Fatalf("CreateOrUpdate: %v", err)
	}
	client := &sshInfoMonthlyRuntimeClient{err: context.DeadlineExceeded}
	resolver := NewMonthlyRuntimeTargetResolver(&monthlyruntime.Service{
		Leases:  nativeRuntimeLeaseAuthority{Service: leases},
		Runtime: client,
	})
	resolver.CredentialDecryptor = encryptor.Decrypt

	target, err := resolver.ResolveManagedRuntimeTarget(context.Background(), ManagedRuntimeTargetRequest{
		TenantID: "org-1",
		OwnerID:  "user-1",
		LeaseID:  "lease-centron-encrypted-credential",
		Provider: "centron",
	})
	if err != nil {
		t.Fatalf("ResolveManagedRuntimeTarget: %v", err)
	}
	if len(client.requests) != 0 {
		t.Fatalf("runtime requests = %+v, want no simulate ssh-info request", client.requests)
	}
	if target.Source != "lease-metadata" || target.Host != "203.0.113.71" || target.SSHClientPrivateKey != "test-client-private-key" {
		t.Fatalf("target = %+v, want encrypted lease metadata credential", target)
	}
}

func TestMonthlyRuntimeTargetResolverKeepsSSHInfoTimeoutPollableAfterAddressMetadata(t *testing.T) {
	now := time.Date(2026, 7, 7, 20, 0, 0, 0, time.UTC)
	leases := vmleases.NewService(vmleases.NewMemoryStore(), vmleases.ServiceConfig{
		Now: func() time.Time { return now },
	})
	metadata := monthlyruntime.NormalizeMetadata(map[string]string{
		metadataKeyServerMode:         serverModeMonthlyRuntime,
		metadataKeyRuntimeLane:        serverModeMonthlyRuntime,
		metadataKeyRuntimeOfferingID:  defaultRuntimeOfferingID,
		metadataKeyRuntimeEnrollState: "enrolled",
		metadataKeyRuntimeSSHHost:     "203.0.113.61",
		metadataKeyRuntimeSSHUser:     "root",
		metadataKeyRuntimeSSHPort:     "22",
	}, serverruntime.RuntimeOfferingStandard)
	_, err := leases.CreateOrUpdate(context.Background(), vmleases.CreateRequest{Lease: vmlease.Lease{
		ID:             "lease-centron-address-only",
		Subject:        vmlease.Subject{Kind: vmlease.SubjectUser, ID: "user-1", OrgID: "org-1"},
		Resource:       vmlease.ResourceRef{ProviderID: "centron", EngineVMID: "node-centron-timeout", Region: defaultLeaseRegion},
		DesiredState:   vmlease.DesiredStateRunning,
		BillingMode:    vmlease.BillingModeSubscription,
		LifecycleClass: vmlease.LifecycleClassSubscription,
		RestartPolicy:  vmlease.RestartPolicyOnUnexpectedStop,
		RecreatePolicy: vmlease.RecreatePolicyManual,
		ValidFrom:      now.Add(-time.Minute),
		ValidUntil:     now.Add(24 * time.Hour),
		RenewedAt:      now,
		Metadata:       metadata,
	}})
	if err != nil {
		t.Fatalf("CreateOrUpdate: %v", err)
	}
	client := &sshInfoMonthlyRuntimeClient{err: context.DeadlineExceeded}
	resolver := NewMonthlyRuntimeTargetResolver(&monthlyruntime.Service{
		Leases:  nativeRuntimeLeaseAuthority{Service: leases},
		Runtime: client,
	})

	_, err = resolver.ResolveManagedRuntimeTarget(context.Background(), ManagedRuntimeTargetRequest{
		TenantID: "org-1",
		OwnerID:  "user-1",
		LeaseID:  "lease-centron-address-only",
		Provider: "centron",
	})
	if err == nil {
		t.Fatal("expected SSHInfo timeout error")
	}
	if errors.Is(err, ErrManagedRuntimeTargetCredentialFailed) {
		t.Fatalf("ResolveManagedRuntimeTarget error = %v, must stay pollable instead of terminal credential failure", err)
	}
	if managedRuntimeTargetTerminalError(err) {
		t.Fatalf("ResolveManagedRuntimeTarget error = %v, must not be terminal while RuntimeActionSSHInfo timed out", err)
	}
	if !strings.Contains(err.Error(), "RuntimeActionSSHInfo failed") || !strings.Contains(err.Error(), "context deadline exceeded") {
		t.Fatalf("ResolveManagedRuntimeTarget error = %v, want SSHInfo timeout diagnostic", err)
	}
	if len(client.requests) != 2 ||
		client.requests[0].Action != serverruntime.RuntimeActionStatus ||
		client.requests[1].Action != serverruntime.RuntimeActionSSHInfo {
		t.Fatalf("runtime requests = %+v, want status prime then ssh-info request", client.requests)
	}
}

type disabledFeatureChecker struct{}

func (disabledFeatureChecker) IsEnabled(context.Context, string, string) (bool, error) {
	return false, nil
}

// TestMonthlyRuntimeTargetResolverSkipsEntitlementReCheck guards against the
// managed-VM creation failure where the background rollout (prepare_rollout) died
// with "Managed Runtime noch nicht bereit": the resolver must resolve the SSH
// target for an already-authorized lease even when the feature checker reports
// disabled (no SaaS edge entitlement headers in the deploy job context).
func TestMonthlyRuntimeTargetResolverSkipsEntitlementReCheck(t *testing.T) {
	now := time.Date(2026, 6, 14, 8, 30, 0, 0, time.UTC)
	leases := vmleases.NewService(vmleases.NewMemoryStore(), vmleases.ServiceConfig{
		Now: func() time.Time { return now }, SnapshotSecret: []byte("secret"),
	})
	metadata := monthlyruntime.NormalizeMetadata(map[string]string{
		metadataKeyServerMode:         serverModeMonthlyRuntime,
		metadataKeyRuntimeLane:        serverModeMonthlyRuntime,
		metadataKeyRuntimeOfferingID:  defaultRuntimeOfferingID,
		metadataKeyRuntimeEnrollState: "enrolled",
	}, serverruntime.RuntimeOfferingStandard)
	_, err := leases.CreateOrUpdate(context.Background(), vmleases.CreateRequest{Lease: vmlease.Lease{
		ID:             "lease-disabled-features",
		Subject:        vmlease.Subject{Kind: vmlease.SubjectUser, ID: "user-1", OrgID: "org-1"},
		Resource:       vmlease.ResourceRef{ProviderID: "centron", EngineVMID: "node-centron-disabled-features", Region: defaultLeaseRegion},
		DesiredState:   vmlease.DesiredStateRunning,
		BillingMode:    vmlease.BillingModeSubscription,
		LifecycleClass: vmlease.LifecycleClassSubscription,
		RestartPolicy:  vmlease.RestartPolicyOnUnexpectedStop,
		RecreatePolicy: vmlease.RecreatePolicyManual,
		ValidFrom:      now.Add(-time.Minute),
		ValidUntil:     now.Add(24 * time.Hour),
		RenewedAt:      now,
		Metadata:       metadata,
	}})
	if err != nil {
		t.Fatalf("CreateOrUpdate: %v", err)
	}
	client := &sshInfoMonthlyRuntimeClient{resp: &serverruntime.LeaseRuntimeActionResponse{
		TenantID: "org-1",
		LeaseID:  "lease-disabled-features",
		Action:   serverruntime.RuntimeActionSSHInfo,
		Status:   &serverruntime.NodeStatus{ID: "node-1", State: "running", PublicIP: "203.0.113.60"},
		SSH: &serverruntime.SSHInfo{
			Host:       "203.0.113.60",
			User:       "root",
			Port:       22,
			PrivateKey: "test-private-key",
		},
	}}
	resolver := NewMonthlyRuntimeTargetResolver(&monthlyruntime.Service{
		Leases:   nativeRuntimeLeaseAuthority{Service: leases},
		Runtime:  client,
		Features: disabledFeatureChecker{},
	})

	target, err := resolver.ResolveManagedRuntimeTarget(context.Background(), ManagedRuntimeTargetRequest{
		TenantID: "org-1",
		OwnerID:  "user-1",
		LeaseID:  "lease-disabled-features",
		Provider: "centron",
	})
	if err != nil {
		t.Fatalf("ResolveManagedRuntimeTarget with disabled features = %v, want success", err)
	}
	if target == nil || target.Host != "203.0.113.60" || target.SSHPrivateKey != "test-private-key" {
		t.Fatalf("target = %+v, want resolved host + credentials", target)
	}
}

func TestMonthlyRuntimeTargetResolverRejectsProviderLocalKeyPathOnly(t *testing.T) {
	now := time.Date(2026, 6, 12, 13, 30, 0, 0, time.UTC)
	leases := vmleases.NewService(vmleases.NewMemoryStore(), vmleases.ServiceConfig{
		Now: func() time.Time { return now },
	})
	metadata := monthlyruntime.NormalizeMetadata(map[string]string{
		metadataKeyProviderID:         "ionos",
		metadataKeyServerMode:         serverModeMonthlyRuntime,
		metadataKeyRuntimeLane:        serverModeMonthlyRuntime,
		metadataKeyRuntimeOfferingID:  defaultRuntimeOfferingID,
		metadataKeyRuntimeEnrollState: "enrolled",
	}, serverruntime.RuntimeOfferingStandard)
	_, err := leases.CreateOrUpdate(context.Background(), vmleases.CreateRequest{Lease: vmlease.Lease{
		ID:             "lease-ionos-key-path-only",
		Subject:        vmlease.Subject{Kind: vmlease.SubjectUser, ID: "user-1", OrgID: "org-1"},
		Resource:       vmlease.ResourceRef{ProviderID: "ionos", EngineVMID: "node-ionos-key-path", Region: defaultLeaseRegion},
		DesiredState:   vmlease.DesiredStateRunning,
		BillingMode:    vmlease.BillingModeSubscription,
		LifecycleClass: vmlease.LifecycleClassSubscription,
		RestartPolicy:  vmlease.RestartPolicyOnUnexpectedStop,
		RecreatePolicy: vmlease.RecreatePolicyManual,
		ValidFrom:      now.Add(-time.Minute),
		ValidUntil:     now.Add(24 * time.Hour),
		RenewedAt:      now,
		Metadata:       metadata,
	}})
	if err != nil {
		t.Fatalf("CreateOrUpdate: %v", err)
	}
	client := &sshInfoMonthlyRuntimeClient{resp: &serverruntime.LeaseRuntimeActionResponse{
		TenantID: "org-1",
		LeaseID:  "lease-ionos-key-path-only",
		Action:   serverruntime.RuntimeActionSSHInfo,
		Status:   &serverruntime.NodeStatus{ID: "node-1", State: "running", PublicIP: "203.0.113.52"},
		SSH: &serverruntime.SSHInfo{
			Host:    "203.0.113.52",
			User:    "root",
			Port:    22,
			KeyPath: "/data/ionos-ssh-keys/provider-local.pem",
		},
	}}
	resolver := NewMonthlyRuntimeTargetResolver(&monthlyruntime.Service{
		Leases:  nativeRuntimeLeaseAuthority{Service: leases},
		Runtime: client,
	})

	_, err = resolver.ResolveManagedRuntimeTarget(context.Background(), ManagedRuntimeTargetRequest{
		TenantID: "org-1",
		OwnerID:  "user-1",
		LeaseID:  "lease-ionos-key-path-only",
		Provider: "ionos",
	})
	if !errors.Is(err, ErrManagedRuntimeTargetCredentialFailed) {
		t.Fatalf("ResolveManagedRuntimeTarget error = %v, want credential contract failure", err)
	}
	if !strings.Contains(err.Error(), "provider-local key_path") {
		t.Fatalf("ResolveManagedRuntimeTarget error = %v, want key_path diagnostic", err)
	}
}

func TestManagedRuntimeTargetFromRuntimeResponseCarriesSSHCredential(t *testing.T) {
	target := ManagedRuntimeTargetFromRuntimeResponse(&monthlyruntime.RuntimeResponse{
		SSH: &serverruntime.SSHInfo{
			Host:             "203.0.113.44",
			User:             "ubuntu",
			Port:             2222,
			PrivateKey:       " private-key ",
			ClientPrivateKey: " client-private-key ",
			KeyPath:          " /tmp/key ",
		},
	})
	if target == nil {
		t.Fatal("expected managed runtime target")
	}
	if target.SSHPrivateKey != "private-key" || target.SSHClientPrivateKey != "client-private-key" || target.SSHKeyPath != "/tmp/key" {
		t.Fatalf("target ssh credentials = %+v", target)
	}
}

func TestRuntimeActionTargetFromManagedRuntimeTargetDropsProviderLocalKeyPathWhenInlineCredentialExists(t *testing.T) {
	target := runtimeActionTargetFromManagedRuntimeTarget(&ManagedRuntimeTarget{
		Host:          "203.0.113.44",
		SSHUser:       "ubuntu",
		SSHKeyPath:    "/data/ionos-ssh-keys/provider-local.pem",
		SSHPrivateKey: "private-key",
	})
	if target == nil {
		t.Fatal("expected runtime action target")
	}
	if target.PrivateKey != "private-key" {
		t.Fatalf("PrivateKey = %q, want inline key", target.PrivateKey)
	}
	if target.KeyPath != "" {
		t.Fatalf("KeyPath = %q, want provider-local key path dropped", target.KeyPath)
	}
}

func TestRuntimeActionContractUsesSharedSchema(t *testing.T) {
	if runtimeActionTargetStackKits != runtimeaction.TargetStackKits {
		t.Fatalf("StackKits target = %q, want shared %q", runtimeActionTargetStackKits, runtimeaction.TargetStackKits)
	}
	if runtimeActionTargetSimulate != runtimeaction.TargetSimulate {
		t.Fatalf("Simulate target = %q, want shared %q", runtimeActionTargetSimulate, runtimeaction.TargetSimulate)
	}
	if StepSimulationGate != string(runtimeaction.ActionSimulateUpdate) ||
		StepRolloutRunner != string(runtimeaction.ActionStackKitRollout) ||
		StepVerifyRollout != string(runtimeaction.ActionVerifyRollout) ||
		StepRestoreDrill != string(runtimeaction.ActionRestoreDrill) {
		t.Fatalf("job step actions drifted from shared runtimeaction constants")
	}
	if defaultSimulationGatePath != runtimeaction.PathSimulateUpdate ||
		defaultStackKitsRolloutPath != runtimeaction.ArchitectureV2PathStackKitRollout ||
		defaultStackKitsVerifyPath != runtimeaction.ArchitectureV2PathStackKitVerify ||
		defaultRestoreDrillPath != runtimeaction.PathRestoreDrill {
		t.Fatalf("runtime action default paths drifted from shared runtimeaction constants")
	}

	body, err := json.Marshal(RuntimeActionRequest{
		Action:      runtimeaction.ActionStackKitRollout,
		StackID:     "stack-1",
		StackName:   "Demo Stack",
		StackKit:    DefaultBasementKitRef,
		TofuDir:     "/work/tofu",
		UnifiedPath: "/work/unified.yaml",
	})
	if err != nil {
		t.Fatalf("marshal contract payload: %v", err)
	}
	want := `{"action":"stackkit_rollout","stack_id":"stack-1","stack_name":"Demo Stack","stackkit":"basement-kit","tofu_dir":"/work/tofu","unified_path":"/work/unified.yaml"}`
	if string(body) != want {
		t.Fatalf("payload JSON = %s, want %s", body, want)
	}
}

func TestRuntimeActionHTTPClientDefaultTimeoutStaysInsideBudget(t *testing.T) {
	client := runtimeActionHTTPClient(nil)

	if client.Timeout != 14*time.Minute+30*time.Second {
		t.Fatalf("runtime action HTTP timeout = %s, want 14m30s", client.Timeout)
	}
	if client.Timeout > 15*time.Minute {
		t.Fatalf("runtime action HTTP timeout = %s, exceeds 15m policy", client.Timeout)
	}

	custom := &http.Client{Timeout: time.Second}
	if got := runtimeActionHTTPClient(custom); got != custom {
		t.Fatal("runtimeActionHTTPClient should preserve an explicitly configured client")
	}
}

func TestRuntimeActionContractIncludesOwnerSpecBootstrapWhenPresent(t *testing.T) {
	body, err := json.Marshal(RuntimeActionRequest{
		Action:    runtimeaction.ActionStackKitRollout,
		StackID:   "stack-1",
		StackName: "Demo Stack",
		StackKit:  DefaultBasementKitRef,
		OwnerSpecBootstrap: &OwnerSpecBootstrap{
			Endpoint:  "/api/v1/stacks/stack-1/owner-spec",
			Token:     "bootstrap-token",
			ExpiresAt: "2026-05-14T10:15:00Z",
			Scopes:    []string{"read:owner-spec"},
		},
	})
	if err != nil {
		t.Fatalf("marshal contract payload: %v", err)
	}
	want := `{"action":"stackkit_rollout","stack_id":"stack-1","stack_name":"Demo Stack","stackkit":"basement-kit","owner_spec_bootstrap":{"endpoint":"/api/v1/stacks/stack-1/owner-spec","token":"bootstrap-token","expires_at":"2026-05-14T10:15:00Z","scopes":["read:owner-spec"]}}`
	if string(body) != want {
		t.Fatalf("payload JSON = %s, want %s", body, want)
	}
	if strings.Contains(string(body), "passphrase") {
		t.Fatalf("owner bootstrap runtime action must not contain recovery material: %s", body)
	}
}

func TestRuntimeActionContractIncludesRuntimeTargetWhenPresent(t *testing.T) {
	body, err := json.Marshal(RuntimeActionRequest{
		Action:   runtimeaction.ActionStackKitRollout,
		StackID:  "stack-1",
		StackKit: DefaultBasementKitRef,
		RuntimeTarget: &RuntimeActionTarget{
			Host:       "203.0.113.10",
			User:       "ubuntu",
			Port:       2222,
			DockerHost: "tcp://techstack-local-runtime:2375",
			PrivateKey: "test-private-key",
		},
	})
	if err != nil {
		t.Fatalf("marshal contract payload: %v", err)
	}
	want := `{"action":"stackkit_rollout","stack_id":"stack-1","stackkit":"basement-kit","runtime_target":{"host":"203.0.113.10","user":"ubuntu","port":2222,"docker_host":"tcp://techstack-local-runtime:2375","private_key":"test-private-key"}}`
	if string(body) != want {
		t.Fatalf("payload JSON = %s, want %s", body, want)
	}
}

func TestRuntimeActionContractIncludesPlatformNodesWhenPresent(t *testing.T) {
	body, err := json.Marshal(RuntimeActionRequest{
		Action:   runtimeaction.ActionStackKitRollout,
		StackID:  "stack-1",
		StackKit: DefaultBasementKitRef,
		PlatformNodes: []PlatformNode{{
			Name:     "worker-1",
			Role:     "worker",
			IP:       "203.0.113.11",
			Services: []string{"immich"},
			Platform: NodePlatformTarget{
				ServerID:        "server-worker",
				DestinationUUID: "destination-worker",
			},
			Bootstrap: &NodeBootstrap{
				KomodoCoreAddress:   "https://komodo.example.test",
				KomodoOnboardingKey: "real-onboarding-key",
				SSH: &SSHBootstrap{
					Host:             "203.0.113.11",
					User:             "root",
					ClientPrivateKey: "worker-key",
				},
			},
		}},
	})
	if err != nil {
		t.Fatalf("marshal contract payload: %v", err)
	}
	want := `{"action":"stackkit_rollout","stack_id":"stack-1","stackkit":"basement-kit","platform_nodes":[{"name":"worker-1","role":"worker","ip":"203.0.113.11","services":["immich"],"platform":{"serverId":"server-worker","destinationUuid":"destination-worker"},"bootstrap":{"komodo_core_address":"https://komodo.example.test","komodo_onboarding_key":"real-onboarding-key","ssh":{"host":"203.0.113.11","user":"root","client_private_key":"worker-key"}}}]}`
	if string(body) != want {
		t.Fatalf("payload JSON = %s, want %s", body, want)
	}
}

func TestStackKitsRuntimeActionTargetUsesProcessDockerHostForLocalDockerTarget(t *testing.T) {
	t.Setenv("DOCKER_HOST", "tcp://techstack-local-runtime:2375")

	target := stackKitsRuntimeActionTargetFromManagedRuntimeTarget(&ManagedRuntimeTarget{
		Host:       "techstack-local-runtime",
		PublicIP:   "techstack-local-runtime",
		SSHUser:    "root",
		SSHPort:    22,
		DockerHost: "tcp://techstack-local-runtime:2375",
	})
	if target != nil {
		t.Fatalf("target = %+v, want nil so StackKits uses process DOCKER_HOST without SSH remote preparation", target)
	}
}

func TestStackKitsRuntimeActionTargetKeepsSSHBackedRemoteTarget(t *testing.T) {
	t.Setenv("DOCKER_HOST", "tcp://techstack-local-runtime:2375")

	target := stackKitsRuntimeActionTargetFromManagedRuntimeTarget(&ManagedRuntimeTarget{
		Host:          "203.0.113.10",
		PublicIP:      "203.0.113.10",
		SSHUser:       "ubuntu",
		SSHPort:       2222,
		DockerHost:    "tcp://techstack-local-runtime:2375",
		SSHPrivateKey: "test-private-key",
	})
	if target == nil {
		t.Fatal("target = nil, want SSH-backed remote target")
	}
	if target.Host != "203.0.113.10" || target.DockerHost != "tcp://techstack-local-runtime:2375" || target.PrivateKey == "" {
		t.Fatalf("target = %+v, want remote host, docker host, and SSH key", target)
	}
}

func TestHTTPRuntimeActionRunnerSerializesPlatformNodes(t *testing.T) {
	var got map[string]any
	server := httptest.NewServer(servicecall.RequireServiceAuth(servicecall.Config{
		ServiceName:    "stackkits",
		Secret:         "auth-secret",
		AllowedCallers: []string{"techstack"},
		Enabled:        true,
	})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode: %v", err)
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})))
	defer server.Close()

	runner, err := NewHTTPRuntimeActionRunner(HTTPRuntimeActionRunnerConfig{
		BaseURL:           server.URL,
		Target:            "stackkits",
		Action:            string(StepRolloutRunner),
		Path:              "/api/v1/internal/runtime-actions/stackkit-rollout",
		ServiceAuthSecret: "auth-secret",
	})
	if err != nil {
		t.Fatalf("NewHTTPRuntimeActionRunner: %v", err)
	}

	err = runner.Run(t.Context(), RuntimeActionRequest{
		StackID: "stack-1",
		PlatformNodes: []PlatformNode{{
			Name: "worker-1",
			Role: "worker",
			IP:   "203.0.113.11",
			Platform: NodePlatformTarget{
				ServerID: "server-worker",
			},
		}},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	nodes, ok := got["platform_nodes"].([]any)
	if !ok || len(nodes) != 1 {
		t.Fatalf("platform_nodes missing from payload: %+v", got)
	}
	node, ok := nodes[0].(map[string]any)
	if !ok {
		t.Fatalf("platform node = %#v", nodes[0])
	}
	platform, ok := node["platform"].(map[string]any)
	if !ok {
		t.Fatalf("platform target missing from payload: %+v", node)
	}
	if node["name"] != "worker-1" || node["role"] != "worker" || node["ip"] != "203.0.113.11" || platform["serverId"] != "server-worker" {
		t.Fatalf("platform_nodes[0] = %+v", node)
	}
}

func TestHTTPRuntimeActionRunnerPostsServicecallRequest(t *testing.T) {
	var got runtimeaction.Request
	server := httptest.NewServer(servicecall.RequireServiceAuth(servicecall.Config{
		ServiceName:    "stackkits",
		Secret:         "auth-secret",
		AllowedCallers: []string{"techstack"},
		Enabled:        true,
	})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method = %q, want POST", r.Method)
		}
		if r.URL.Path != "/api/v1/internal/runtime-actions/stackkit-rollout" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		caller := servicecall.FromContext(r.Context())
		if caller == nil || caller.Service != "techstack" {
			t.Fatalf("caller = %+v, want techstack", caller)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode: %v", err)
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})))
	defer server.Close()

	runner, err := NewHTTPRuntimeActionRunner(HTTPRuntimeActionRunnerConfig{
		BaseURL:           server.URL,
		Target:            "stackkits",
		Action:            string(StepRolloutRunner),
		Path:              "/api/v1/internal/runtime-actions/stackkit-rollout",
		ServiceAuthSecret: "auth-secret",
	})
	if err != nil {
		t.Fatalf("NewHTTPRuntimeActionRunner: %v", err)
	}

	err = runner.Run(t.Context(), RuntimeActionRequest{
		StackID:     "stack-1",
		StackName:   "Demo Stack",
		StackKit:    DefaultBasementKitRef,
		TofuDir:     "/work/tofu",
		UnifiedPath: "/work/unified.yaml",
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got.Action != runtimeaction.ActionStackKitRollout ||
		got.StackID != "stack-1" ||
		got.StackName != "Demo Stack" ||
		got.StackKit != DefaultBasementKitRef ||
		got.TofuDir != "/work/tofu" ||
		got.UnifiedPath != "/work/unified.yaml" {
		t.Fatalf("payload = %+v", got)
	}
}

func TestHTTPRuntimeActionRunnerSerializesOwnerSpecContract(t *testing.T) {
	var got map[string]any
	server := httptest.NewServer(servicecall.RequireServiceAuth(servicecall.Config{
		ServiceName:    "stackkits",
		Secret:         "auth-secret",
		AllowedCallers: []string{"techstack"},
		Enabled:        true,
	})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode: %v", err)
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})))
	defer server.Close()

	runner, err := NewHTTPRuntimeActionRunner(HTTPRuntimeActionRunnerConfig{
		BaseURL:           server.URL,
		Target:            "stackkits",
		Action:            string(StepRolloutRunner),
		Path:              "/api/v1/internal/runtime-actions/stackkit-rollout",
		ServiceAuthSecret: "auth-secret",
	})
	if err != nil {
		t.Fatalf("NewHTTPRuntimeActionRunner: %v", err)
	}

	err = runner.Run(t.Context(), RuntimeActionRequest{
		StackID: "stack-1",
		OwnerSpecBootstrap: &OwnerSpecBootstrap{
			Endpoint:  "/api/v1/stacks/stack-1/owner-spec",
			Token:     "bootstrap-token",
			ExpiresAt: "2026-05-14T10:15:00Z",
			Scopes:    []string{"read:owner-spec"},
		},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	bootstrap, ok := got["owner_spec_bootstrap"].(map[string]any)
	if !ok {
		t.Fatalf("owner_spec_bootstrap missing from payload: %+v", got)
	}
	if bootstrap["endpoint"] != "/api/v1/stacks/stack-1/owner-spec" || bootstrap["token"] != "bootstrap-token" {
		t.Fatalf("owner_spec_bootstrap = %+v", bootstrap)
	}
}

func TestHTTPRuntimeActionRunnerSerializesTechStackEnrollment(t *testing.T) {
	var got map[string]any
	server := httptest.NewServer(servicecall.RequireServiceAuth(servicecall.Config{
		ServiceName:    "stackkits",
		Secret:         "auth-secret",
		AllowedCallers: []string{"techstack"},
		Enabled:        true,
	})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode: %v", err)
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})))
	defer server.Close()

	runner, err := NewHTTPRuntimeActionRunner(HTTPRuntimeActionRunnerConfig{
		BaseURL:           server.URL,
		Target:            "stackkits",
		Action:            string(StepRolloutRunner),
		Path:              "/api/v1/internal/runtime-actions/stackkit-rollout",
		ServiceAuthSecret: "auth-secret",
	})
	if err != nil {
		t.Fatalf("NewHTTPRuntimeActionRunner: %v", err)
	}

	err = runner.Run(t.Context(), RuntimeActionRequest{
		StackID:  "stack-1",
		Mode:     "advanced",
		TenantID: "tenant-1",
		OwnerID:  "owner-1",
		TechStackEnrollment: &TechStackEnrollment{
			TenantID:       "tenant-1",
			OwnerID:        "owner-1",
			StackID:        "stack-1",
			ServerURL:      "https://techstack.example",
			ServerID:       "server-1",
			RuntimeAgentID: "runtime-1",
			AgentToken:     "runtime-token",
			InventoryURL:   "https://techstack.example/api/v1/workers/runtime-1/inventory",
			ControlURLs:    []string{"wss://techstack.example/api/v1/workers/runtime-1/control/ws"},
			ChannelBootstrap: map[string]any{
				"grpc_hint": "mtls-if-http2-network-allows",
			},
		},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got["mode"] != "advanced" || got["tenant_id"] != "tenant-1" || got["owner_id"] != "owner-1" {
		t.Fatalf("top-level handoff fields missing: %+v", got)
	}
	enrollment, ok := got["techstack_enrollment"].(map[string]any)
	if !ok {
		t.Fatalf("techstack_enrollment missing from payload: %+v", got)
	}
	if enrollment["server_id"] != "server-1" || enrollment["runtime_agent_id"] != "runtime-1" || enrollment["agent_token"] != "runtime-token" {
		t.Fatalf("techstack_enrollment = %+v", enrollment)
	}
}

func TestHTTPRuntimeActionRunnerPreservesPartialTechStackEnrollmentForStackKitsValidation(t *testing.T) {
	var got map[string]any
	server := httptest.NewServer(servicecall.RequireServiceAuth(servicecall.Config{
		ServiceName:    "stackkits",
		Secret:         "auth-secret",
		AllowedCallers: []string{"techstack"},
		Enabled:        true,
	})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode: %v", err)
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})))
	defer server.Close()

	runner, err := NewHTTPRuntimeActionRunner(HTTPRuntimeActionRunnerConfig{
		BaseURL:           server.URL,
		Target:            "stackkits",
		Action:            string(StepRolloutRunner),
		Path:              "/api/v1/internal/runtime-actions/stackkit-rollout",
		ServiceAuthSecret: "auth-secret",
	})
	if err != nil {
		t.Fatalf("NewHTTPRuntimeActionRunner: %v", err)
	}

	err = runner.Run(t.Context(), RuntimeActionRequest{
		StackID: "stack-1",
		TechStackEnrollment: &TechStackEnrollment{
			ServerURL: "https://techstack.example",
			ServerID:  "server-1",
		},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	enrollment, ok := got["techstack_enrollment"].(map[string]any)
	if !ok {
		t.Fatalf("techstack_enrollment missing from payload: %+v", got)
	}
	if enrollment["server_url"] != "https://techstack.example" || enrollment["server_id"] != "server-1" {
		t.Fatalf("techstack_enrollment = %+v", enrollment)
	}
	if enrollment["runtime_agent_id"] != "" {
		t.Fatalf("partial enrollment should remain partial for StackKits validation: %+v", enrollment)
	}
}

func TestHTTPRuntimeActionRunnerSerializesRuntimeTarget(t *testing.T) {
	var got map[string]any
	server := httptest.NewServer(servicecall.RequireServiceAuth(servicecall.Config{
		ServiceName:    "stackkits",
		Secret:         "auth-secret",
		AllowedCallers: []string{"techstack"},
		Enabled:        true,
	})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode: %v", err)
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})))
	defer server.Close()

	runner, err := NewHTTPRuntimeActionRunner(HTTPRuntimeActionRunnerConfig{
		BaseURL:           server.URL,
		Target:            "stackkits",
		Action:            string(StepRolloutRunner),
		Path:              "/api/v1/internal/runtime-actions/stackkit-rollout",
		ServiceAuthSecret: "auth-secret",
	})
	if err != nil {
		t.Fatalf("NewHTTPRuntimeActionRunner: %v", err)
	}

	err = runner.Run(t.Context(), RuntimeActionRequest{
		StackID: "stack-1",
		RuntimeTarget: &RuntimeActionTarget{
			Host:       "203.0.113.10",
			User:       "ubuntu",
			DockerHost: "tcp://techstack-local-runtime:2375",
			PrivateKey: "test-private-key",
		},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	target, ok := got["runtime_target"].(map[string]any)
	if !ok {
		t.Fatalf("runtime_target missing from payload: %+v", got)
	}
	if target["host"] != "203.0.113.10" ||
		target["user"] != "ubuntu" ||
		target["docker_host"] != "tcp://techstack-local-runtime:2375" ||
		target["private_key"] != "test-private-key" {
		t.Fatalf("runtime_target = %+v", target)
	}
}

func TestHTTPRuntimeActionRunnerSerializesSimulationPreviewPolicy(t *testing.T) {
	var got map[string]any
	server := httptest.NewServer(servicecall.RequireServiceAuth(servicecall.Config{
		ServiceName:    "simulate",
		Secret:         "auth-secret",
		AllowedCallers: []string{"techstack"},
		Enabled:        true,
	})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode: %v", err)
		}
		_ = json.NewEncoder(w).Encode(runtimeaction.Response{Status: runtimeaction.StatusReady})
	})))
	defer server.Close()

	runner, err := NewHTTPRuntimeActionRunner(HTTPRuntimeActionRunnerConfig{
		BaseURL:           server.URL,
		Target:            "simulate",
		Action:            runtimeActionSimulateUpdate,
		Path:              "/api/v1/internal/runtime-actions/simulate-update",
		ServiceAuthSecret: "auth-secret",
	})
	if err != nil {
		t.Fatalf("NewHTTPRuntimeActionRunner: %v", err)
	}

	err = runner.Run(t.Context(), RuntimeActionRequest{
		StackID:       "stack-1",
		StackKit:      "modern-homelab",
		TenantID:      "org-1",
		OwnerID:       "auth0|staff",
		StackSpecPath: "/work/stack-spec.yaml",
		PreviewPolicy: &PreviewPolicy{
			Required:          true,
			Runtime:           "provider-backed",
			Audience:          "staff",
			Visibility:        "private",
			TTLSeconds:        3600,
			StaffOnly:         true,
			PublicBetaPreview: true,
		},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got["tenant_id"] != "org-1" || got["owner_id"] != "auth0|staff" || got["stack_spec_path"] != "/work/stack-spec.yaml" {
		t.Fatalf("preview request scope = %+v", got)
	}
	policy, ok := got["preview_policy"].(map[string]any)
	if !ok || policy["runtime"] != "provider-backed" || policy["staff_only"] != true {
		t.Fatalf("preview_policy = %+v", got["preview_policy"])
	}
}

func TestHTTPRuntimeActionRunnerReturnsStructuredResult(t *testing.T) {
	server := httptest.NewServer(servicecall.RequireServiceAuth(servicecall.Config{
		ServiceName:    "stackkits",
		Secret:         "auth-secret",
		AllowedCallers: []string{"techstack"},
		Enabled:        true,
	})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"stackkit_outputs": map[string]any{
					"login_gateway": map[string]any{
						"url": "https://login.example.com",
					},
				},
			},
		})
	})))
	defer server.Close()

	runner, err := NewHTTPRuntimeActionRunner(HTTPRuntimeActionRunnerConfig{
		BaseURL:           server.URL,
		Target:            "stackkits",
		Action:            string(StepRolloutRunner),
		Path:              "/api/v1/internal/runtime-actions/stackkit-rollout",
		ServiceAuthSecret: "auth-secret",
	})
	if err != nil {
		t.Fatalf("NewHTTPRuntimeActionRunner: %v", err)
	}

	result, err := runner.RunWithResult(t.Context(), RuntimeActionRequest{StackID: "stack-1"})
	if err != nil {
		t.Fatalf("RunWithResult: %v", err)
	}
	outputs, ok := result["stackkit_outputs"].(map[string]interface{})
	if !ok {
		t.Fatalf("stackkit_outputs = %#v", result["stackkit_outputs"])
	}
	gateway, ok := outputs["login_gateway"].(map[string]interface{})
	if !ok || gateway["url"] != "https://login.example.com" {
		t.Fatalf("login_gateway = %#v", outputs["login_gateway"])
	}
}

func TestHTTPRuntimeActionRunnerReturnsStatusDiagnostics(t *testing.T) {
	server := httptest.NewServer(servicecall.RequireServiceAuth(servicecall.Config{
		ServiceName:    "simulate",
		Secret:         "auth-secret",
		AllowedCallers: []string{"techstack"},
		Enabled:        true,
	})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "backend down", http.StatusServiceUnavailable)
	})))
	defer server.Close()

	runner, err := NewHTTPRuntimeActionRunner(HTTPRuntimeActionRunnerConfig{
		BaseURL:           server.URL,
		Target:            "simulate",
		Action:            runtimeActionSimulateUpdate,
		Path:              "/api/v1/internal/runtime-actions/simulate-update",
		ServiceAuthSecret: "auth-secret",
	})
	if err != nil {
		t.Fatalf("NewHTTPRuntimeActionRunner: %v", err)
	}

	err = runner.Run(t.Context(), RuntimeActionRequest{StackID: "stack-1", StackKit: DefaultBasementKitRef})
	if err == nil {
		t.Fatal("Run error = nil, want non-2xx diagnostics")
	}
	msg := err.Error()
	if !strings.Contains(msg, runtimeActionSimulateUpdate) ||
		!strings.Contains(msg, "503") ||
		!strings.Contains(msg, "backend down") {
		t.Fatalf("error = %q, want action, status, and body", msg)
	}
}

func TestRuntimeActionsFromEnvWiresConfiguredRunners(t *testing.T) {
	t.Setenv("SERVICE_AUTH_SECRET", "auth-secret")
	t.Setenv("SERVICE_AUTH_SECRET_NEXT", "next-secret")
	t.Setenv("TECHSTACK_STACKKITS_ACTIONS_URL", "http://stackkits.internal")
	t.Setenv("TECHSTACK_SIMULATE_ACTIONS_URL", "http://simulate.internal")

	actions, diagnostics := RuntimeActionsFromEnv(RuntimeActions{})
	if len(diagnostics.Warnings) != 0 {
		t.Fatalf("warnings = %v, want none", diagnostics.Warnings)
	}
	if actions.SimulationGate == nil ||
		actions.RolloutRunner == nil ||
		actions.RolloutVerifier == nil ||
		actions.RestoreDrill == nil ||
		actions.TargetBootstrapper == nil {
		t.Fatalf("actions not fully wired: %+v", actions)
	}
	rollout, ok := actions.RolloutRunner.(*HTTPRuntimeActionRunner)
	if !ok {
		t.Fatalf("RolloutRunner = %T, want *HTTPRuntimeActionRunner", actions.RolloutRunner)
	}
	if rollout.baseURL != "http://stackkits.internal" ||
		rollout.path != defaultStackKitsRolloutPath ||
		rollout.action != string(runtimeaction.ActionStackKitRollout) {
		t.Fatalf("rollout runner = %+v", rollout)
	}
	simulation, ok := actions.SimulationGate.(*HTTPRuntimeActionRunner)
	if !ok {
		t.Fatalf("SimulationGate = %T, want *HTTPRuntimeActionRunner", actions.SimulationGate)
	}
	if simulation.baseURL != "http://simulate.internal" ||
		simulation.path != defaultSimulationGatePath ||
		simulation.action != runtimeActionSimulateUpdate {
		t.Fatalf("simulation runner = %+v", simulation)
	}
}

func TestRuntimeActionsFromEnvReportsMissingServiceAuthSecret(t *testing.T) {
	// Explicitly clear inherited secrets so the test asserts the
	// "missing secret" branch deterministically. Without this the test
	// passes on a clean dev machine but fails on CI runners where
	// SERVICE_AUTH_SECRET / SERVICE_AUTH_SECRET_NEXT are exported for
	// other jobs in the same workflow context.
	t.Setenv("SERVICE_AUTH_SECRET", "")
	t.Setenv("SERVICE_AUTH_SECRET_NEXT", "")
	t.Setenv("TECHSTACK_STACKKITS_ACTIONS_URL", "http://stackkits.internal")
	t.Setenv("TECHSTACK_SIMULATE_ACTIONS_URL", "http://simulate.internal")

	actions, diagnostics := RuntimeActionsFromEnv(RuntimeActions{})
	if actions.SimulationGate != nil || actions.RolloutRunner != nil || actions.RolloutVerifier != nil || actions.RestoreDrill != nil {
		t.Fatalf("actions = %+v, want no servicecall runners without SERVICE_AUTH_SECRET", actions)
	}
	got := strings.Join(diagnostics.Warnings, "\n")
	if !strings.Contains(got, "SERVICE_AUTH_SECRET") ||
		!strings.Contains(got, "StackKits") ||
		!strings.Contains(got, "Simulate") {
		t.Fatalf("warnings = %v, want clear missing-secret diagnostics", diagnostics.Warnings)
	}
}

func TestRuntimeActionsFromEnvAllowsExplicitLocalSimulationGate(t *testing.T) {
	t.Setenv("SERVICE_AUTH_SECRET", "")
	t.Setenv("TECHSTACK_SIMULATE_ACTIONS_URL", "")
	t.Setenv("TECHSTACK_KOMBISIM_URL", "")
	t.Setenv("KOMBIFY_URL_VPS_SIMULATE", "")
	t.Setenv("KOMBIFY_URL_PUBLIC_SIMULATE", "")
	t.Setenv("KOMBISIM_URL", "")
	t.Setenv("TECHSTACK_ALLOW_LOCAL_SIMULATION_GATE", "true")

	actions, diagnostics := RuntimeActionsFromEnv(RuntimeActions{})
	if actions.SimulationGate == nil {
		t.Fatal("SimulationGate = nil, want explicit local simulation gate")
	}
	if _, ok := actions.SimulationGate.(localSimulationGateRunner); !ok {
		t.Fatalf("SimulationGate = %T, want localSimulationGateRunner", actions.SimulationGate)
	}
	got := strings.Join(diagnostics.Configured, "\n")
	if !strings.Contains(got, "Local simulation gate") {
		t.Fatalf("configured = %v, want local simulation gate diagnostic", diagnostics.Configured)
	}
}

// TestRuntimeActionsFromEnvFallbackChain verifies that the StackKits and
// Simulate URL fallback chains pick up the kombify-Administration URL names
// when the dedicated TECHSTACK_*_ACTIONS_URL slot is empty.
func TestRuntimeActionsFromEnvFallbackChain(t *testing.T) {
	// Clear the primary URL slots so the fallback chain is exercised.
	t.Setenv("TECHSTACK_STACKKITS_ACTIONS_URL", "")
	t.Setenv("TECHSTACK_STACKKITS_INTERNAL_URL", "")
	t.Setenv("STACKKITS_INTERNAL_URL", "")
	t.Setenv("STACKKITS_API_URL", "")
	t.Setenv("TECHSTACK_SIMULATE_ACTIONS_URL", "")
	t.Setenv("TECHSTACK_KOMBISIM_URL", "")
	t.Setenv("KOMBISIM_URL", "")

	// Only set the canonical kombify Administration keys: StackKits is on
	// Render (INTERNAL prefix); Simulate is on IONOS Coolify (VPS prefix).
	t.Setenv("SERVICE_AUTH_SECRET", "auth-secret")
	t.Setenv("KOMBIFY_URL_INTERNAL_STACKKITS", "http://kombify-stackkits:5240")
	t.Setenv("KOMBIFY_URL_VPS_SIMULATE", "https://simulate.kombify.io")

	actions, diagnostics := RuntimeActionsFromEnv(RuntimeActions{})
	if len(diagnostics.Warnings) != 0 {
		t.Fatalf("warnings = %v, want none with internal URL fallbacks", diagnostics.Warnings)
	}
	if actions.SimulationGate == nil ||
		actions.RolloutRunner == nil ||
		actions.RolloutVerifier == nil ||
		actions.RestoreDrill == nil {
		t.Fatalf("actions not fully wired via internal URL fallbacks: %+v", actions)
	}
	rollout, ok := actions.RolloutRunner.(*HTTPRuntimeActionRunner)
	if !ok {
		t.Fatalf("RolloutRunner = %T, want *HTTPRuntimeActionRunner", actions.RolloutRunner)
	}
	if rollout.baseURL != "http://kombify-stackkits:5240" {
		t.Fatalf("rollout baseURL = %q, want kombify-stackkits internal URL via KOMBIFY_URL_INTERNAL_STACKKITS", rollout.baseURL)
	}
	sim, ok := actions.SimulationGate.(*HTTPRuntimeActionRunner)
	if !ok {
		t.Fatalf("SimulationGate = %T, want *HTTPRuntimeActionRunner", actions.SimulationGate)
	}
	if sim.baseURL != "https://simulate.kombify.io" {
		t.Fatalf("simulate baseURL = %q, want IONOS-VPS simulate origin via KOMBIFY_URL_VPS_SIMULATE", sim.baseURL)
	}
}

// TestRuntimeActionsFromEnvDoesNotUseStackKitsPublicURL covers the production
// static-site URL case: STACKKITS_PUBLIC_URL is public documentation, not the
// runtime-action API.
func TestRuntimeActionsFromEnvDoesNotUseStackKitsPublicURL(t *testing.T) {
	t.Setenv("TECHSTACK_STACKKITS_ACTIONS_URL", "")
	t.Setenv("TECHSTACK_STACKKITS_INTERNAL_URL", "")
	t.Setenv("KOMBIFY_URL_INTERNAL_STACKKITS", "")
	t.Setenv("STACKKITS_INTERNAL_URL", "")
	t.Setenv("STACKKITS_API_URL", "")
	t.Setenv("STACKKITS_PUBLIC_URL", "https://stackkits.kombify.io")
	t.Setenv("TECHSTACK_SIMULATE_ACTIONS_URL", "http://simulate.internal")
	t.Setenv("SERVICE_AUTH_SECRET", "auth-secret")

	actions, diagnostics := RuntimeActionsFromEnv(RuntimeActions{})
	if actions.RolloutRunner != nil || actions.RolloutVerifier != nil || actions.RestoreDrill != nil {
		t.Fatalf("StackKits actions = %+v, want disabled without an internal/action API URL", actions)
	}
	if actions.SimulationGate == nil {
		t.Fatalf("SimulationGate = nil, want simulate runner still configured")
	}
	got := strings.Join(diagnostics.Warnings, "\n")
	if !strings.Contains(got, "TECHSTACK_STACKKITS_ACTIONS_URL") {
		t.Fatalf("warnings = %v, want missing StackKits action URL warning", diagnostics.Warnings)
	}
}
