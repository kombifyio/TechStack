package controlplane

import (
	"context"
	"errors"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

// Sensitive destructive boundary: a late worker revision conflict rolls back
// the stack projection update from the same reviewed cleanup command.
func TestPostgresOrphanProjectionCleanupIsAtomicOnDrift(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	updatedAt := time.Date(2026, 8, 25, 10, 0, 0, 0, time.UTC)
	lastSeenAt := time.Date(2026, 8, 24, 9, 0, 0, 0, time.UTC)
	staleBefore := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	appliedAt := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	command := OrphanProjectionCleanup{
		TenantID: "tenant-1", OwnerSubjectID: "owner-1", StaleBefore: staleBefore, AppliedAt: appliedAt,
		Stacks:  []OrphanStackProjection{{ID: "stack-e2e", Name: "e2e-ionos-20260826090000", UpdatedAt: updatedAt}},
		Workers: []OrphanWorkerProjection{{ID: "worker-e2e", StackID: "stack-e2e", Hostname: "e2e-ionos-worker", LastSeenAt: lastSeenAt, UpdatedAt: updatedAt}},
	}

	mock.ExpectBegin()
	expectTenantGUC(mock, "tenant-1")
	mock.ExpectExec(regexp.QuoteMeta("UPDATE stacks")).
		WithArgs("tenant-1", "owner-1", "stack-e2e", "e2e-ionos-20260826090000", updatedAt, appliedAt).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("DELETE FROM workers AS worker")).
		WithArgs("tenant-1", "owner-1", "worker-e2e", "stack-e2e", "e2e-ionos-worker", lastSeenAt, updatedAt, staleBefore).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectRollback()

	err = NewPostgresStore(db).ApplyOrphanProjectionCleanup(context.Background(), command)
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("cleanup error = %v, want conflict", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}
