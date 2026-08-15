package monthlyruntime

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kombifyio/techstack/internal/runtimeproduct/serverruntime"
	"github.com/kombifyio/techstack/pkg/demoguard"
	"github.com/kombifyio/techstack/pkg/vmleases"
)

func TestActionDecommissionBlockedOnProtectedLease(t *testing.T) {
	t.Setenv(demoguard.EnvDemoProtectedLeaseIDs, "lease-1, lease-other")
	now := time.Date(2026, 7, 5, 9, 0, 0, 0, time.UTC)
	leases := vmleases.NewService(vmleases.NewMemoryStore(), vmleases.ServiceConfig{Now: func() time.Time { return now }, SnapshotSecret: []byte("secret")})
	if _, err := leases.CreateOrUpdate(context.Background(), vmleases.CreateRequest{Lease: testMonthlyLease(now, enrollmentStatusEnrolled)}); err != nil {
		t.Fatalf("CreateOrUpdate: %v", err)
	}
	runtime := &fakeRuntimeClient{}
	svc := &Service{Leases: nativeLeaseService(leases), Runtime: runtime, Features: fakeFeatureChecker{enabled: true}}

	_, err := svc.Action(context.Background(), ActionRequest{
		TenantID: "org-1",
		UserID:   "user-1",
		LeaseID:  "lease-1",
		Action:   serverruntime.RuntimeActionDecommission,
	})
	if !errors.Is(err, ErrDecommissionBlockedProtected) {
		t.Fatalf("err = %v, want ErrDecommissionBlockedProtected", err)
	}
	// Force must not bypass the protection on the user-facing path either.
	_, err = svc.Action(context.Background(), ActionRequest{
		TenantID: "org-1",
		UserID:   "user-1",
		LeaseID:  "lease-1",
		Action:   serverruntime.RuntimeActionDecommission,
		Force:    true,
	})
	if !errors.Is(err, ErrDecommissionBlockedProtected) {
		t.Fatalf("force err = %v, want ErrDecommissionBlockedProtected", err)
	}
	if len(runtime.requests) != 0 {
		t.Fatalf("runtime saw %d requests, want none for a protected lease", len(runtime.requests))
	}
	stored, err := leases.Get(context.Background(), "org-1", "lease-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if stored.CancelledAt != nil {
		t.Fatal("protected lease was canceled")
	}
}

func TestActionDecommissionInternalBypassesProtection(t *testing.T) {
	t.Setenv(demoguard.EnvDemoProtectedLeaseIDs, "lease-1")
	now := time.Date(2026, 7, 5, 9, 0, 0, 0, time.UTC)
	leases := vmleases.NewService(vmleases.NewMemoryStore(), vmleases.ServiceConfig{Now: func() time.Time { return now }, SnapshotSecret: []byte("secret")})
	if _, err := leases.CreateOrUpdate(context.Background(), vmleases.CreateRequest{Lease: testMonthlyLease(now, enrollmentStatusEnrolled)}); err != nil {
		t.Fatalf("CreateOrUpdate: %v", err)
	}
	runtime := &fakeRuntimeClient{}
	svc := &Service{Leases: nativeLeaseService(leases), Runtime: runtime, Features: fakeFeatureChecker{enabled: true}}

	if _, err := svc.Action(context.Background(), ActionRequest{
		TenantID: "org-1",
		UserID:   "user-1",
		LeaseID:  "lease-1",
		Action:   serverruntime.RuntimeActionDecommission,
		Internal: true,
	}); err != nil {
		t.Fatalf("internal decommission: %v", err)
	}
	stored, err := leases.Get(context.Background(), "org-1", "lease-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if stored.CancelledAt == nil {
		t.Fatal("internal decommission did not cancel the lease")
	}
}

func TestActionSSHRestrictedForDemoTenant(t *testing.T) {
	t.Setenv(demoguard.EnvDemoTenantID, "org-1")
	now := time.Date(2026, 7, 5, 9, 0, 0, 0, time.UTC)
	leases := vmleases.NewService(vmleases.NewMemoryStore(), vmleases.ServiceConfig{Now: func() time.Time { return now }, SnapshotSecret: []byte("secret")})
	if _, err := leases.CreateOrUpdate(context.Background(), vmleases.CreateRequest{Lease: testMonthlyLease(now, enrollmentStatusEnrolled)}); err != nil {
		t.Fatalf("CreateOrUpdate: %v", err)
	}
	runtime := &fakeRuntimeClient{}
	svc := &Service{Leases: nativeLeaseService(leases), Runtime: runtime, Features: fakeFeatureChecker{enabled: true}}

	for _, action := range []serverruntime.RuntimeAction{serverruntime.RuntimeActionEnableSSH, serverruntime.RuntimeActionSSHInfo} {
		_, err := svc.Action(context.Background(), ActionRequest{
			TenantID: "org-1",
			UserID:   "user-1",
			LeaseID:  "lease-1",
			Action:   action,
		})
		if !errors.Is(err, ErrDemoRestricted) {
			t.Fatalf("action %s err = %v, want ErrDemoRestricted", action, err)
		}
	}
	// Non-demo tenants stay unaffected.
	t.Setenv(demoguard.EnvDemoTenantID, "org-other")
	if _, err := svc.Action(context.Background(), ActionRequest{
		TenantID: "org-1",
		UserID:   "user-1",
		LeaseID:  "lease-1",
		Action:   serverruntime.RuntimeActionEnableSSH,
	}); errors.Is(err, ErrDemoRestricted) {
		t.Fatalf("non-demo tenant unexpectedly demo-restricted: %v", err)
	}
}
