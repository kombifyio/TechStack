package providercontroljobs

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/jobs"
)

type stubReaperDecommissioner struct{}

func (stubReaperDecommissioner) DecommissionManagedLeases(context.Context, jobs.ManagedLeaseDecommissionRequest) (*jobs.ManagedLeaseDecommissionResult, error) {
	return &jobs.ManagedLeaseDecommissionResult{}, nil
}

func TestNeverEnrolledRecoveryRefusesUnsafeWindows(t *testing.T) {
	database := &sql.DB{}
	if _, err := NewNeverEnrolledRecovery(NeverEnrolledRecoveryConfig{
		Database: database, Decommissioner: stubReaperDecommissioner{},
		EnrollmentWindow: time.Minute,
	}); err == nil {
		t.Fatal("a window below the managed enrollment floor must be refused")
	}
	if _, err := NewNeverEnrolledRecovery(NeverEnrolledRecoveryConfig{
		Database: database, Decommissioner: stubReaperDecommissioner{},
		EnrollmentWindow: 48 * time.Hour,
	}); err == nil {
		t.Fatal("an out-of-range window must be refused")
	}
	worker, err := NewNeverEnrolledRecovery(NeverEnrolledRecoveryConfig{
		Database: database, Decommissioner: stubReaperDecommissioner{},
	})
	if err != nil {
		t.Fatalf("default configuration: %v", err)
	}
	if worker.window != defaultNeverEnrolledRecoveryWindow {
		t.Fatalf("default window = %s", worker.window)
	}
}
