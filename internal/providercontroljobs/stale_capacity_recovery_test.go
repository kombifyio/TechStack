package providercontroljobs

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/kombifyio/techstack/pkg/jobs"
)

type staleCapacityTestDecommissioner struct {
	requests []jobs.ManagedLeaseDecommissionRequest
	errors   map[string]error
}

func (d *staleCapacityTestDecommissioner) DecommissionManagedLeases(
	_ context.Context,
	req jobs.ManagedLeaseDecommissionRequest,
) (*jobs.ManagedLeaseDecommissionResult, error) {
	d.requests = append(d.requests, req)
	return &jobs.ManagedLeaseDecommissionResult{LeaseIDs: []string{req.LeaseID}}, d.errors[req.LeaseID]
}

func TestStaleCapacityRecoveryUsesNativeDecommissionAndPaginates(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer database.Close()

	query := regexp.QuoteMeta("FROM provider_control_list_stale_capacity_recovery_candidates($1, $2, $3)")
	mock.ExpectQuery(query).
		WithArgs("", "", 2).
		WillReturnRows(sqlmock.NewRows([]string{"tenant_id", "owner_subject_id", "lease_id", "stack_id"}).
			AddRow("tenant-a", "owner-a", "lease-1", "stack-1").
			AddRow("tenant-b", "owner-b", "lease-2", "stack-2"))
	mock.ExpectQuery(query).
		WithArgs("tenant-b", "lease-2", 2).
		WillReturnRows(sqlmock.NewRows([]string{"tenant_id", "owner_subject_id", "lease_id", "stack_id"}))

	decommissioner := &staleCapacityTestDecommissioner{}
	recovery, err := NewStaleCapacityRecovery(StaleCapacityRecoveryConfig{
		Database: database, Decommissioner: decommissioner, BatchSize: 2,
	})
	if err != nil {
		t.Fatalf("NewStaleCapacityRecovery: %v", err)
	}
	if err := recovery.RecoverOnce(t.Context()); err != nil {
		t.Fatalf("RecoverOnce: %v", err)
	}
	if len(decommissioner.requests) != 2 {
		t.Fatalf("decommission requests = %d, want 2", len(decommissioner.requests))
	}
	want := jobs.ManagedLeaseDecommissionRequest{
		TenantID: "tenant-b", OwnerID: "owner-b", LeaseID: "lease-2", StackID: "stack-2",
	}
	if got := decommissioner.requests[1]; got != want {
		t.Fatalf("second decommission request = %+v, want %+v", got, want)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("database expectations: %v", err)
	}
}

func TestStaleCapacityRecoveryAcceptsWaitAndContinuesAfterFailure(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer database.Close()

	query := regexp.QuoteMeta("FROM provider_control_list_stale_capacity_recovery_candidates($1, $2, $3)")
	mock.ExpectQuery(query).
		WithArgs("", "", 3).
		WillReturnRows(sqlmock.NewRows([]string{"tenant_id", "owner_subject_id", "lease_id", "stack_id"}).
			AddRow("tenant-a", "owner-a", "lease-wait", "stack-wait").
			AddRow("tenant-a", "owner-a", "lease-fail", "stack-fail").
			AddRow("tenant-a", "owner-a", "lease-next", "stack-next"))
	mock.ExpectQuery(query).
		WithArgs("tenant-a", "lease-next", 3).
		WillReturnRows(sqlmock.NewRows([]string{"tenant_id", "owner_subject_id", "lease_id", "stack_id"}))

	wait := &jobs.JobWaitError{Reason: jobs.WaitReasonManagedRuntimeProvider, ResumeAfter: time.Second}
	failed := errors.New("provider read-back unavailable")
	decommissioner := &staleCapacityTestDecommissioner{errors: map[string]error{
		"lease-wait": wait,
		"lease-fail": failed,
	}}
	var reported []error
	recovery, err := NewStaleCapacityRecovery(StaleCapacityRecoveryConfig{
		Database: database, Decommissioner: decommissioner, BatchSize: 3,
		OnError: func(err error) { reported = append(reported, err) },
	})
	if err != nil {
		t.Fatalf("NewStaleCapacityRecovery: %v", err)
	}
	if err := recovery.RecoverOnce(t.Context()); err != nil {
		t.Fatalf("RecoverOnce: %v", err)
	}
	if len(decommissioner.requests) != 3 {
		t.Fatalf("decommission requests = %d, want recovery to continue through all 3", len(decommissioner.requests))
	}
	if len(reported) != 1 || !errors.Is(reported[0], failed) {
		t.Fatalf("reported errors = %v, want only provider failure", reported)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("database expectations: %v", err)
	}
}

func TestStaleCapacityRecoveryRejectsIncompleteDirectoryEntry(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer database.Close()

	query := regexp.QuoteMeta("FROM provider_control_list_stale_capacity_recovery_candidates($1, $2, $3)")
	mock.ExpectQuery(query).
		WithArgs("", "", 1).
		WillReturnRows(sqlmock.NewRows([]string{"tenant_id", "owner_subject_id", "lease_id", "stack_id"}).
			AddRow("tenant-a", "", "lease-1", "stack-1"))

	decommissioner := &staleCapacityTestDecommissioner{}
	recovery, err := NewStaleCapacityRecovery(StaleCapacityRecoveryConfig{
		Database: database, Decommissioner: decommissioner, BatchSize: 1,
	})
	if err != nil {
		t.Fatalf("NewStaleCapacityRecovery: %v", err)
	}
	if err := recovery.RecoverOnce(t.Context()); err == nil {
		t.Fatal("RecoverOnce accepted an incomplete recovery candidate")
	}
	if len(decommissioner.requests) != 0 {
		t.Fatalf("decommission requests = %d, want 0", len(decommissioner.requests))
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("database expectations: %v", err)
	}
}

func TestStaleCapacityRecoveryCarriesCursorAcrossBoundedPasses(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer database.Close()

	query := regexp.QuoteMeta("FROM provider_control_list_stale_capacity_recovery_candidates($1, $2, $3)")
	first := sqlmock.NewRows([]string{"tenant_id", "owner_subject_id", "lease_id", "stack_id"})
	for index := 1; index <= maximumStaleCapacityRecoveryPerPass; index++ {
		leaseID := fmt.Sprintf("lease-%03d", index)
		first.AddRow("tenant-a", "owner-a", leaseID, "stack-"+leaseID)
	}
	mock.ExpectQuery(query).WithArgs("", "", maximumStaleCapacityRecoveryPerPass).WillReturnRows(first)
	mock.ExpectQuery(query).
		WithArgs("tenant-a", "lease-100", maximumStaleCapacityRecoveryPerPass).
		WillReturnRows(sqlmock.NewRows([]string{"tenant_id", "owner_subject_id", "lease_id", "stack_id"}).
			AddRow("tenant-b", "owner-b", "lease-101", "stack-101"))

	decommissioner := &staleCapacityTestDecommissioner{}
	recovery, err := NewStaleCapacityRecovery(StaleCapacityRecoveryConfig{
		Database: database, Decommissioner: decommissioner, BatchSize: maximumStaleCapacityRecoveryPerPass,
	})
	if err != nil {
		t.Fatalf("NewStaleCapacityRecovery: %v", err)
	}
	if err := recovery.RecoverOnce(t.Context()); err != nil {
		t.Fatalf("first RecoverOnce: %v", err)
	}
	if err := recovery.RecoverOnce(t.Context()); err != nil {
		t.Fatalf("second RecoverOnce: %v", err)
	}
	if len(decommissioner.requests) != 101 || decommissioner.requests[100].LeaseID != "lease-101" {
		t.Fatalf("decommission requests = %d last=%+v, want fair continuation through lease-101", len(decommissioner.requests), decommissioner.requests[100])
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("database expectations: %v", err)
	}
}
