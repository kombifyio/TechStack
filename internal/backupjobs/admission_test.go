package backupjobs

import (
	"context"
	"testing"

	"github.com/kombifyio/techstack/internal/gocommon/edgeauth"

	"github.com/kombifyio/techstack/pkg/backupstore"
	"github.com/kombifyio/techstack/pkg/features"
	"github.com/kombifyio/techstack/pkg/jobs"
	"github.com/kombifyio/techstack/pkg/monthlyruntime"
)

type fakeFeatures struct{ enabled map[string]bool }

func (f fakeFeatures) IsEnabled(_ context.Context, key, _ string) (bool, error) {
	return f.enabled[key], nil
}

type fakeUsage struct{}

func (fakeUsage) Usage(context.Context, string, string) (backupstore.StoredUsage, error) {
	return backupstore.StoredUsage{}, backupstore.ErrUsageNotFound
}

func starterFeatures() fakeFeatures {
	return fakeFeatures{enabled: map[string]bool{monthlyruntime.FeatureBackupConfig: true}}
}

func request() jobs.BackupAdmissionRequest {
	return jobs.BackupAdmissionRequest{TenantID: "tenant-a", StackID: "stack-a", UserID: "user-a"}
}

func TestAdmissionRequiresBothAuthorities(t *testing.T) {
	if _, err := NewAdmission(AdmissionConfig{Usage: fakeUsage{}}); err == nil {
		t.Fatal("admission without a feature checker must not be constructible")
	}
	if _, err := NewAdmission(AdmissionConfig{Features: starterFeatures()}); err == nil {
		t.Fatal("admission without a usage reader must not be constructible")
	}
}

func TestAdmissionDeniesWithoutTheEntitlement(t *testing.T) {
	admission, err := NewAdmission(AdmissionConfig{Features: fakeFeatures{}, Usage: fakeUsage{}})
	if err != nil {
		t.Fatal(err)
	}
	decision, err := admission(context.Background(), request())
	if err != nil || !decision.Denied {
		t.Fatalf("a missing backup entitlement must deny: %+v err=%v", decision, err)
	}
	if decision.Details == nil {
		t.Fatal("a denial must carry the structured envelope")
	}
}

// The production checker honours a signed backup grant, and only that grant.
func TestAdmissionFollowsTheSignedBackupGrant(t *testing.T) {
	svc, err := features.NewService(nil, features.ServiceConfig{})
	if err != nil {
		t.Fatal(err)
	}
	admission, err := NewAdmission(AdmissionConfig{Features: svc, Usage: fakeUsage{}})
	if err != nil {
		t.Fatal(err)
	}
	granted := edgeauth.FlagsToContext(context.Background(), edgeauth.FlagSet{Flags: map[string]bool{
		monthlyruntime.FeatureBackupConfig: true,
	}})
	if decision, err := admission(granted, request()); err != nil || decision.Denied {
		t.Fatalf("a signed configuration-backup grant must admit: %+v err=%v", decision, err)
	}
	content := request()
	content.IncludeContent = true
	decision, err := admission(granted, content)
	if err != nil || !decision.Denied || decision.Details["reason_code"] != monthlyruntime.EntitlementReasonFeatureDisabled {
		t.Fatalf("content backups without the content grant must deny as disabled: %+v err=%v", decision, err)
	}
}

// The local runtime test grant entitles only the lane's own tenant on a local
// Core, and a SaaS Core ignores it even with every test switch set.
func TestAdmissionTestGrantIsLocalAndTenantScoped(t *testing.T) {
	t.Setenv("TECHSTACK_ENV", "local")
	t.Setenv("TECHSTACK_ALLOW_LOCAL_SIMULATION_GATE", "true")
	t.Setenv("TECHSTACK_ALLOW_LOCAL_MANAGED_RUNTIME_E2E", "true")
	t.Setenv("TECHSTACK_RUNTIME_E2E_TENANT_ID", "tenant-a")
	admit := func(saas bool, tenantID string) bool {
		admission, err := NewAdmission(AdmissionConfig{
			Features: fakeFeatures{}, Usage: fakeUsage{},
			TestGrantTenantID: monthlyruntime.LocalRuntimeE2EBackupGrantTenant(saas),
		})
		if err != nil {
			t.Fatal(err)
		}
		req := request()
		req.TenantID = tenantID
		req.IncludeContent = true
		decision, err := admission(context.Background(), req)
		if err != nil {
			t.Fatal(err)
		}
		return !decision.Denied
	}
	if admit(true, "tenant-a") {
		t.Fatal("a SaaS Core honoured the local runtime test grant")
	}
	if !admit(false, "tenant-a") {
		t.Fatal("the local runtime test tenant was not granted")
	}
	if admit(false, "tenant-b") {
		t.Fatal("the local runtime test grant reached another tenant")
	}
}
