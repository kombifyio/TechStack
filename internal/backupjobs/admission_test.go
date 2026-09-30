package backupjobs

import (
	"context"
	"testing"

	"github.com/kombifyio/techstack/pkg/backupstore"
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
