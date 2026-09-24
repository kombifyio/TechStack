package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kombifyio/techstack/internal/gocommon/servicecall"
	"github.com/kombifyio/techstack/internal/runtimeproduct/runtimeaction"
	"github.com/kombifyio/techstack/internal/runtimeproduct/serverruntime"
	"github.com/kombifyio/techstack/internal/runtimeproduct/vmlease"
	"github.com/kombifyio/techstack/pkg/auth"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/monthlyruntime"
	"github.com/kombifyio/techstack/pkg/runtimeidentity"
	"github.com/kombifyio/techstack/pkg/vmleases"
)

type stubServerRuntimeLookup struct {
	server *controlplane.ServerRuntime
	err    error
}

func (s stubServerRuntimeLookup) GetServerRuntime(_ context.Context, _, _ string) (*controlplane.ServerRuntime, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.server, nil
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

func (fakeMonthlyRuntimeClient) RuntimeAction(_ context.Context, req serverruntime.LeaseRuntimeActionRequest) (*serverruntime.LeaseRuntimeActionResponse, error) {
	return &serverruntime.LeaseRuntimeActionResponse{
		TenantID: req.TenantID,
		LeaseID:  req.LeaseID,
		Action:   req.Action,
		Status:   &serverruntime.NodeStatus{ID: "node-1", State: "running"},
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

func TestMonthlyRuntimeTargetResolverReportsEnrollmentStateWithoutAddress(t *testing.T) {
	now := time.Date(2026, 5, 25, 13, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name          string
		provider      string
		status        string
		providerCause string
		terminal      bool
	}{
		{name: "failed", provider: "ionos", status: runtimeEnrollmentStatusFailed, providerCause: "ionos quota exhausted", terminal: true},
		{name: "retrying", provider: "centron", status: runtimeEnrollmentStatusRetrying, providerCause: "centron API request timed out"},
		{name: "pending", provider: "centron", status: runtimeEnrollmentStatusPending},
	} {
		t.Run(test.name, func(t *testing.T) {
			leaseID := "lease-" + test.provider + "-" + test.status
			metadata := map[string]string{
				metadataKeyServerMode:         serverModeMonthlyRuntime,
				metadataKeyRuntimeLane:        serverModeMonthlyRuntime,
				metadataKeyRuntimeOfferingID:  defaultRuntimeOfferingID,
				metadataKeyRuntimeEnrollState: test.status,
			}
			if test.providerCause != "" {
				metadata[metadataKeyRuntimeEnrollError] = test.providerCause
			}
			leases := vmleases.NewService(vmleases.NewMemoryStore(), vmleases.ServiceConfig{
				Now: func() time.Time { return now },
			})
			if _, err := leases.CreateOrUpdate(context.Background(), vmleases.CreateRequest{Lease: vmlease.Lease{
				ID:             vmlease.LeaseID(leaseID),
				Subject:        vmlease.Subject{Kind: vmlease.SubjectUser, ID: "user-1", OrgID: "org-1"},
				Resource:       vmlease.ResourceRef{ProviderID: test.provider, EngineVMID: "node-" + test.status, Region: defaultLeaseRegion},
				DesiredState:   vmlease.DesiredStateRunning,
				BillingMode:    vmlease.BillingModeSubscription,
				LifecycleClass: vmlease.LifecycleClassSubscription,
				RestartPolicy:  vmlease.RestartPolicyOnUnexpectedStop,
				RecreatePolicy: vmlease.RecreatePolicyManual,
				ValidFrom:      now.Add(-time.Minute),
				ValidUntil:     now.Add(24 * time.Hour),
				RenewedAt:      now,
				Metadata:       metadata,
			}}); err != nil {
				t.Fatalf("CreateOrUpdate: %v", err)
			}
			resolver := NewMonthlyRuntimeTargetResolver(&monthlyruntime.Service{
				Leases:  nativeRuntimeLeaseAuthority{Service: leases},
				Runtime: fakeMonthlyRuntimeClient{},
			})

			_, err := resolver.ResolveManagedRuntimeTarget(context.Background(), ManagedRuntimeTargetRequest{
				TenantID: "org-1",
				OwnerID:  "user-1",
				LeaseID:  leaseID,
				Provider: test.provider,
			})
			if test.terminal {
				if !errors.Is(err, ErrManagedRuntimeEnrollmentFailed) {
					t.Fatalf("ResolveManagedRuntimeTarget error = %v, want terminal enrollment failure", err)
				}
			} else {
				if !errors.Is(err, monthlyruntime.ErrEnrollmentPending) || errors.Is(err, ErrManagedRuntimeEnrollmentFailed) {
					t.Fatalf("ResolveManagedRuntimeTarget error = %v, want pollable enrollment state", err)
				}
			}
		})
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

func TestMonthlyRuntimeTargetResolverOverlaysGuardPublicIPWhenLeaseSSHHostIsStale(t *testing.T) {
	now := time.Date(2026, 8, 21, 21, 0, 0, 0, time.UTC)
	leases := vmleases.NewService(vmleases.NewMemoryStore(), vmleases.ServiceConfig{
		Now: func() time.Time { return now },
	})
	_, err := leases.CreateOrUpdate(context.Background(), vmleases.CreateRequest{Lease: vmlease.Lease{
		ID:             "lease-ionos-stale-ip",
		Subject:        vmlease.Subject{Kind: vmlease.SubjectUser, ID: "user-1", OrgID: "org-1"},
		Resource:       vmlease.ResourceRef{ProviderID: "ionos", EngineVMID: "node-ionos-stale-ip", Region: defaultLeaseRegion},
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
			metadataKeyRuntimeSSHHost:     "212.132.94.66",
			metadataKeyRuntimePublicIP:    "212.132.94.66",
			metadataKeyRuntimeSSHUser:     "root",
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
	}, stubServerRuntimeLookup{server: &controlplane.ServerRuntime{
		ID:       runtimeidentity.LeaseServerID("lease-ionos-stale-ip"),
		TenantID: "org-1",
		LeaseID:  "lease-ionos-stale-ip",
		Metadata: map[string]any{
			"inventory_source": "guard-inventory",
			"host": map[string]any{
				"public_ip": "85.215.64.233",
			},
		},
	}})

	target, err := resolver.ResolveManagedRuntimeTarget(context.Background(), ManagedRuntimeTargetRequest{
		TenantID: "org-1",
		OwnerID:  "user-1",
		LeaseID:  "lease-ionos-stale-ip",
		Provider: "ionos",
	})
	if err != nil {
		t.Fatalf("ResolveManagedRuntimeTarget: %v", err)
	}
	if target.Host != "85.215.64.233" || target.PublicIP != "85.215.64.233" {
		t.Fatalf("target host/ip = %+v, want Guard public IP", target)
	}
	if target.SSHUser != "root" || target.SSHPort != 22 || target.DockerHost == "" {
		t.Fatalf("target credentials = %+v, want lease SSH/docker credentials kept", target)
	}
}

func TestAttachManagedProviderCredentialKeepsLeaseKeyAndAddsProviderBundleKey(t *testing.T) {
	previous := managedProviderCredentialResolver
	t.Cleanup(func() { managedProviderCredentialResolver = previous })
	managedProviderCredentialResolver = func(string, time.Time) (string, error) {
		return "provider-bundle-key", nil
	}

	withLease := attachManagedProviderCredential(&ManagedRuntimeTarget{Host: "203.0.113.10", SSHPrivateKey: "lease-key"}, "ionos")
	if withLease.SSHPrivateKey != "lease-key" || withLease.SSHProviderPrivateKey != "provider-bundle-key" {
		t.Fatalf("lease+provider = %+v, want lease key kept and provider key attached as fallback", withLease)
	}

	sameKey := attachManagedProviderCredential(&ManagedRuntimeTarget{Host: "203.0.113.10", SSHPrivateKey: "provider-bundle-key"}, "ionos")
	if sameKey.SSHPrivateKey != "provider-bundle-key" || sameKey.SSHProviderPrivateKey != "" {
		t.Fatalf("matching keys = %+v, want no duplicate fallback", sameKey)
	}

	emptyLease := attachManagedProviderCredential(&ManagedRuntimeTarget{Host: "203.0.113.10"}, "ionos")
	if emptyLease.SSHPrivateKey != "provider-bundle-key" || emptyLease.SSHProviderPrivateKey != "" {
		t.Fatalf("empty lease = %+v, want provider key as the primary credential", emptyLease)
	}

	action := runtimeActionTargetFromManagedRuntimeTarget(withLease)
	if action == nil || action.PrivateKey != "lease-key" || action.ProviderPrivateKey != "provider-bundle-key" {
		t.Fatalf("action target = %+v, want both keys handed to SSH bootstrap", action)
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

func TestMonthlyRuntimeTargetResolverUsesEncryptedLeaseCredentialsBeforeRuntimeAction(t *testing.T) {
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
		t.Fatalf("runtime requests = %+v, want no runtime ssh-info request", client.requests)
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
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("ResolveManagedRuntimeTarget error = %v, want wrapped SSHInfo timeout", err)
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

func TestRuntimeActionContractUsesSharedActionsAndPaths(t *testing.T) {
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

func captureRuntimeActionPayload(t *testing.T, target, action, path string, request RuntimeActionRequest) map[string]any {
	t.Helper()
	var got map[string]any
	server := httptest.NewServer(servicecall.RequireServiceAuth(servicecall.Config{
		ServiceName:    target,
		Secret:         "auth-secret",
		AllowedCallers: []string{"techstack"},
		Enabled:        true,
	})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode: %v", err)
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})))
	t.Cleanup(server.Close)

	runner, err := NewHTTPRuntimeActionRunner(HTTPRuntimeActionRunnerConfig{
		BaseURL:           server.URL,
		Target:            target,
		Action:            action,
		Path:              path,
		ServiceAuthSecret: "auth-secret",
	})
	if err != nil {
		t.Fatalf("NewHTTPRuntimeActionRunner: %v", err)
	}

	if err := runner.Run(t.Context(), request); err != nil {
		t.Fatalf("Run: %v", err)
	}
	return got
}

func TestHTTPRuntimeActionRunnerSerializesPlatformNodes(t *testing.T) {
	got := captureRuntimeActionPayload(t, "stackkits", string(StepRolloutRunner), "/api/v1/internal/runtime-actions/stackkit-rollout", RuntimeActionRequest{
		StackID: "stack-1",
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
				KomodoOnboardingKey: "test-onboarding-key",
				SSH: &SSHBootstrap{
					Host:             "203.0.113.11",
					User:             "root",
					ClientPrivateKey: "test-worker-key",
				},
			},
		}},
	})
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
	services, ok := node["services"].([]any)
	if !ok || !slices.Contains(services, any("immich")) {
		t.Fatalf("platform services = %#v", node["services"])
	}
	bootstrap, ok := node["bootstrap"].(map[string]any)
	if !ok {
		t.Fatalf("bootstrap missing from payload: %+v", node)
	}
	ssh, ok := bootstrap["ssh"].(map[string]any)
	if !ok {
		t.Fatalf("SSH bootstrap missing from payload: %+v", bootstrap)
	}
	if node["name"] != "worker-1" || node["role"] != "worker" || node["ip"] != "203.0.113.11" ||
		platform["serverId"] != "server-worker" || platform["destinationUuid"] != "destination-worker" ||
		bootstrap["komodo_core_address"] != "https://komodo.example.test" || bootstrap["komodo_onboarding_key"] != "test-onboarding-key" ||
		ssh["host"] != "203.0.113.11" || ssh["user"] != "root" || ssh["client_private_key"] != "test-worker-key" {
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
	got := captureRuntimeActionPayload(t, "stackkits", string(StepRolloutRunner), "/api/v1/internal/runtime-actions/stackkit-rollout", RuntimeActionRequest{
		StackID: "stack-1",
		OwnerSpecBootstrap: &OwnerSpecBootstrap{
			Endpoint:  "/api/v1/stacks/stack-1/owner-spec",
			Token:     "bootstrap-token",
			ExpiresAt: "2026-05-14T10:15:00Z",
			Scopes:    []string{"read:owner-spec"},
		},
	})
	bootstrap, ok := got["owner_spec_bootstrap"].(map[string]any)
	if !ok {
		t.Fatalf("owner_spec_bootstrap missing from payload: %+v", got)
	}
	scopes, ok := bootstrap["scopes"].([]any)
	if !ok || !slices.Contains(scopes, any("read:owner-spec")) {
		t.Fatalf("owner_spec_bootstrap scopes = %#v", bootstrap["scopes"])
	}
	if bootstrap["endpoint"] != "/api/v1/stacks/stack-1/owner-spec" || bootstrap["token"] != "bootstrap-token" || bootstrap["expires_at"] != "2026-05-14T10:15:00Z" {
		t.Fatalf("owner_spec_bootstrap = %+v", bootstrap)
	}
	if _, leaked := bootstrap["passphrase"]; leaked {
		t.Fatalf("owner bootstrap runtime action contains recovery material: %+v", bootstrap)
	}
}

func TestHTTPRuntimeActionRunnerSerializesTechStackEnrollment(t *testing.T) {
	got := captureRuntimeActionPayload(t, "stackkits", string(StepRolloutRunner), "/api/v1/internal/runtime-actions/stackkit-rollout", RuntimeActionRequest{
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
	got := captureRuntimeActionPayload(t, "stackkits", string(StepRolloutRunner), "/api/v1/internal/runtime-actions/stackkit-rollout", RuntimeActionRequest{
		StackID: "stack-1",
		TechStackEnrollment: &TechStackEnrollment{
			ServerURL: "https://techstack.example",
			ServerID:  "server-1",
		},
	})
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
	got := captureRuntimeActionPayload(t, "stackkits", string(StepRolloutRunner), "/api/v1/internal/runtime-actions/stackkit-rollout", RuntimeActionRequest{
		StackID: "stack-1",
		RuntimeTarget: &RuntimeActionTarget{
			Host:       "203.0.113.10",
			User:       "ubuntu",
			Port:       2222,
			DockerHost: "tcp://techstack-local-runtime:2375",
			PrivateKey: "test-private-key",
		},
	})
	target, ok := got["runtime_target"].(map[string]any)
	if !ok {
		t.Fatalf("runtime_target missing from payload: %+v", got)
	}
	if target["host"] != "203.0.113.10" ||
		target["user"] != "ubuntu" ||
		target["port"] != float64(2222) ||
		target["docker_host"] != "tcp://techstack-local-runtime:2375" ||
		target["private_key"] != "test-private-key" {
		t.Fatalf("runtime_target = %+v", target)
	}
}

func TestHTTPRuntimeActionRunnerSerializesSimulationPreviewPolicy(t *testing.T) {
	got := captureRuntimeActionPayload(t, "simulate", runtimeActionSimulateUpdate, "/api/v1/internal/runtime-actions/simulate-update", RuntimeActionRequest{
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
	var responseErr *RuntimeActionResponseError
	if !errors.As(err, &responseErr) {
		t.Fatalf("Run error = %v, want structured non-2xx response", err)
	}
	if responseErr.Action != runtimeActionSimulateUpdate || responseErr.StatusCode != http.StatusServiceUnavailable || responseErr.Body != "backend down" {
		t.Fatalf("response error = %+v, want action, status, and body", responseErr)
	}
}

func TestRuntimeActionsFromEnvWiresConfiguredRunners(t *testing.T) {
	tests := []struct {
		name              string
		environment       map[string]string
		stackKitsBaseURL  string
		simulationBaseURL string
	}{
		{
			name: "dedicated action URLs",
			environment: map[string]string{
				"TECHSTACK_STACKKITS_ACTIONS_URL": "http://stackkits.internal",
				"TECHSTACK_SIMULATE_ACTIONS_URL":  "http://simulate.internal",
			},
			stackKitsBaseURL:  "http://stackkits.internal",
			simulationBaseURL: "http://simulate.internal",
		},
		{
			name: "administration managed fallbacks",
			environment: map[string]string{
				"KOMBIFY_URL_INTERNAL_STACKKITS": "http://kombify-stackkits:5240",
				"KOMBIFY_URL_VPS_SIMULATE":       "https://simulate.kombify.io",
			},
			stackKitsBaseURL:  "http://kombify-stackkits:5240",
			simulationBaseURL: "https://simulate.kombify.io",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, key := range []string{
				"TECHSTACK_STACKKITS_ACTIONS_URL", "TECHSTACK_STACKKITS_INTERNAL_URL",
				"KOMBIFY_URL_INTERNAL_STACKKITS", "STACKKITS_INTERNAL_URL", "STACKKITS_API_URL",
				"TECHSTACK_SIMULATE_ACTIONS_URL", "TECHSTACK_KOMBISIM_URL",
				"KOMBIFY_URL_VPS_SIMULATE", "KOMBIFY_URL_PUBLIC_SIMULATE", "KOMBISIM_URL",
			} {
				t.Setenv(key, "")
			}
			t.Setenv("SERVICE_AUTH_SECRET", "auth-secret")
			t.Setenv("SERVICE_AUTH_SECRET_NEXT", "next-secret")
			t.Setenv("TECHSTACK_RUNTIME_TARGET_BOOTSTRAP_DISABLED", "")
			for key, value := range tt.environment {
				t.Setenv(key, value)
			}

			actions, diagnostics := RuntimeActionsFromEnv(RuntimeActions{})
			if len(diagnostics.Warnings) != 0 {
				t.Fatalf("warnings = %v, want none", diagnostics.Warnings)
			}
			if actions.SimulationGate == nil || actions.RolloutRunner == nil || actions.RolloutVerifier == nil ||
				actions.RestoreDrill == nil || actions.TargetBootstrapper == nil {
				t.Fatalf("actions not fully wired: %+v", actions)
			}
			rollout, ok := actions.RolloutRunner.(*HTTPRuntimeActionRunner)
			if !ok || rollout.baseURL != tt.stackKitsBaseURL || rollout.path != defaultStackKitsRolloutPath ||
				rollout.action != string(runtimeaction.ActionStackKitRollout) {
				t.Fatalf("rollout runner = %+v", actions.RolloutRunner)
			}
			simulation, ok := actions.SimulationGate.(*HTTPRuntimeActionRunner)
			if !ok || simulation.baseURL != tt.simulationBaseURL || simulation.path != defaultSimulationGatePath ||
				simulation.action != runtimeActionSimulateUpdate {
				t.Fatalf("simulation runner = %+v", actions.SimulationGate)
			}
		})
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
