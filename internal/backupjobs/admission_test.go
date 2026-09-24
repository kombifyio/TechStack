package backupjobs

import (
	"context"
	"errors"
	"testing"

	"github.com/kombifyio/techstack/pkg/backupstore"
	"github.com/kombifyio/techstack/pkg/jobs"
	"github.com/kombifyio/techstack/pkg/monthlyruntime"
)

type fakeFeatures struct{ enabled map[string]bool }

func (f fakeFeatures) IsEnabled(_ context.Context, key, _ string) (bool, error) {
	return f.enabled[key], nil
}

type fakeUsage struct {
	stored int64
	err    error
}

func (f fakeUsage) Usage(context.Context, string, string) (backupstore.StoredUsage, error) {
	return backupstore.StoredUsage{StoredBytes: f.stored}, f.err
}

func starterFeatures() fakeFeatures {
	return fakeFeatures{enabled: map[string]bool{
		monthlyruntime.FeatureBackupConfig:         true,
		monthlyruntime.FeatureBackupStorageStarter: true,
	}}
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

// TestAdmissionDeniesAnUnmeasuredStackByDefault pins the direction of the
// unknown case. An unmeasured stack is unknown, not empty, and admitting on
// unknown is how a quota stops being a quota.
func TestAdmissionDeniesAnUnmeasuredStackByDefault(t *testing.T) {
	admission, _ := NewAdmission(AdmissionConfig{
		Features: starterFeatures(),
		Usage:    fakeUsage{err: backupstore.ErrUsageNotFound},
	})
	decision, err := admission(context.Background(), request())
	if err != nil || !decision.Denied {
		t.Fatalf("an unmeasured stack must deny by default: %+v err=%v", decision, err)
	}

	permissive, _ := NewAdmission(AdmissionConfig{
		Features:        starterFeatures(),
		Usage:           fakeUsage{err: backupstore.ErrUsageNotFound},
		AllowUnmeasured: true,
	})
	if decision, err := permissive(context.Background(), request()); err != nil || decision.Denied {
		t.Fatalf("the explicit bootstrap opt-in must admit: %+v err=%v", decision, err)
	}
}

// TestAdmissionDeniesWhenTheMeterCannotAnswer covers the failure that would
// otherwise admit every tenant exactly when the control plane has lost sight
// of what they are storing.
func TestAdmissionDeniesWhenTheMeterCannotAnswer(t *testing.T) {
	admission, _ := NewAdmission(AdmissionConfig{
		Features: starterFeatures(),
		Usage:    fakeUsage{err: errors.New("object store unreachable")},
	})
	decision, err := admission(context.Background(), request())
	if err == nil || !decision.Denied {
		t.Fatalf("an unreadable meter must deny: %+v err=%v", decision, err)
	}
}

func TestAdmissionEnforcesTheStarterBudget(t *testing.T) {
	const starterBudget = int64(50) << 30

	under, _ := NewAdmission(AdmissionConfig{
		Features: starterFeatures(), Usage: fakeUsage{stored: starterBudget - 1},
	})
	if decision, err := under(context.Background(), request()); err != nil || decision.Denied {
		t.Fatalf("a stack inside its budget must be admitted: %+v err=%v", decision, err)
	}

	at, _ := NewAdmission(AdmissionConfig{
		Features: starterFeatures(), Usage: fakeUsage{stored: starterBudget},
	})
	decision, err := at(context.Background(), request())
	if err != nil || !decision.Denied {
		t.Fatalf("a stack at its budget must deny: %+v err=%v", decision, err)
	}
	if decision.UsedBytes != starterBudget || decision.QuotaBytes != starterBudget {
		t.Fatalf("the denial must report what it measured: %+v", decision)
	}
	// The envelope must name the caller's own tier, not all-you-need.
	required, _ := decision.Details["required_features"].([]string)
	if len(required) != 1 || required[0] != monthlyruntime.FeatureBackupStorageStarter {
		t.Fatalf("required_features = %v, want the starter tier", required)
	}
}
