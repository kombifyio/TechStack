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

func TestActionDecommissionProtectedLeaseAuthorization(t *testing.T) {
	t.Setenv(demoguard.EnvDemoProtectedLeaseIDs, "lease-1, lease-other")
	for _, test := range []struct {
		name             string
		force            bool
		internal         bool
		wantErr          error
		wantRuntimeCalls int
		wantCancelled    bool
	}{
		{name: "owner", wantErr: ErrDecommissionBlockedProtected},
		{name: "owner forced", force: true, wantErr: ErrDecommissionBlockedProtected},
		{name: "internal", internal: true, wantRuntimeCalls: 1, wantCancelled: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			now := time.Date(2026, 7, 5, 9, 0, 0, 0, time.UTC)
			leases := newLeaseServiceWith(t, now, enrollmentStatusEnrolled)
			runtime := &fakeRuntimeClient{}
			svc := &Service{Leases: nativeLeaseService(leases), Runtime: runtime, Features: fakeFeatureChecker{enabled: true}}

			_, err := svc.Action(t.Context(), ActionRequest{
				TenantID: "org-1", UserID: "user-1", LeaseID: "lease-1",
				Action: serverruntime.RuntimeActionDecommission, Force: test.force, Internal: test.internal,
			})
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("Action error = %v, want %v", err, test.wantErr)
			}
			if len(runtime.requests) != test.wantRuntimeCalls {
				t.Fatalf("runtime requests = %d, want %d", len(runtime.requests), test.wantRuntimeCalls)
			}
			stored, err := leases.Get(t.Context(), "org-1", "lease-1")
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			if got := stored.CancelledAt != nil; got != test.wantCancelled {
				t.Fatalf("cancelled = %v, want %v", got, test.wantCancelled)
			}
		})
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
