package controlplane

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestPostgresStoreUpsertJobUsesTenantScope(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	store := NewPostgresStore(db)
	now := time.Date(2026, 5, 28, 12, 30, 0, 0, time.UTC)
	req := UpsertJobRequest{
		ID:           "job-1",
		TenantID:     "tenant-1",
		InstanceID:   "instance-1",
		StackID:      "stack-1",
		Type:         "provision",
		State:        "pending",
		Priority:     3,
		Progress:     10,
		Step:         "plan",
		Message:      "planning",
		Logs:         []map[string]any{{"level": "info"}},
		Result:       map[string]any{"ok": true},
		ScheduledFor: now,
	}

	mock.ExpectBegin()
	expectTenantGUC(mock, "tenant-1")
	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO jobs")).
		WithArgs("job-1", "tenant-1", "instance-1", "stack-1", "provision", "pending", 3, 10, "plan", "planning", "", "", sqlmock.AnyArg(), sqlmock.AnyArg(), now).
		WillReturnRows(jobRows().AddRow(
			"job-1", "tenant-1", "instance-1", "stack-1", "provision", "pending", 3, 10,
			"plan", "planning", "", "", `[{"level":"info"}]`, `{"ok":true}`, now, nil, nil, now, now,
		))
	mock.ExpectCommit()

	got, err := store.UpsertJob(context.Background(), req)
	if err != nil {
		t.Fatalf("UpsertJob: %v", err)
	}
	if got.TenantID != "tenant-1" || got.ID != "job-1" {
		t.Fatalf("unexpected job identity: %#v", got)
	}
	if len(got.Logs) != 1 || got.Logs[0]["level"] != "info" {
		t.Fatalf("logs JSON was not decoded: %#v", got.Logs)
	}
	if got.Result["ok"] != true {
		t.Fatalf("result JSON was not decoded: %#v", got.Result)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPostgresStoreRejectsRunningWritesOutsideStartJob(t *testing.T) {
	db, _, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()
	store := NewPostgresStore(db)

	for _, write := range []struct {
		name string
		run  func() error
	}{
		{name: "create running", run: func() error {
			_, err := store.CreateJob(context.Background(), UpsertJobRequest{ID: "job-create", TenantID: "tenant-1", State: "running"})
			return err
		}},
		{name: "upsert running", run: func() error {
			_, err := store.UpsertJob(context.Background(), UpsertJobRequest{ID: "job-upsert", TenantID: "tenant-1", State: "running"})
			return err
		}},
	} {
		t.Run(write.name, func(t *testing.T) {
			if err := write.run(); !errors.Is(err, ErrConflict) {
				t.Fatalf("write error = %v, want ErrConflict", err)
			}
		})
	}
}

func TestPostgresStoreUpsertJobCannotOverwriteRunningExecution(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()
	store := NewPostgresStore(db)

	mock.ExpectBegin()
	expectTenantGUC(mock, "tenant-1")
	mock.ExpectQuery(`(?s)INSERT INTO jobs.*ON CONFLICT \(id\) DO UPDATE.*WHERE jobs\.tenant_id = EXCLUDED\.tenant_id AND jobs\.state <> 'running'`).
		WillReturnError(sql.ErrNoRows)
	mock.ExpectRollback()

	_, err = store.UpsertJob(context.Background(), UpsertJobRequest{
		ID: "job-running", TenantID: "tenant-1", StackID: "stack-1", Type: "deploy", State: "pending",
	})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("UpsertJob error = %v, want ErrConflict", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPostgresStoreTerminalWritesRequireRunningExecution(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()
	store := NewPostgresStore(db)
	now := time.Date(2026, 7, 19, 10, 0, 0, 0, time.UTC)
	createdAt := now.Add(-time.Minute)

	mock.ExpectBegin()
	expectTenantGUC(mock, "tenant-1")
	mock.ExpectQuery(`(?s)UPDATE jobs.*SET state = 'completed'.*WHERE tenant_id = \$1 AND id = \$2 AND state = 'running'`).
		WithArgs("tenant-1", "job-running", sqlmock.AnyArg(), now).
		WillReturnRows(jobRows().AddRow(
			"job-running", "tenant-1", nil, "stack-1", "deploy", "completed", 0, 100,
			"", "", "", "", `[]`, `{"ok":true}`, createdAt, createdAt, now, createdAt, now,
		))
	mock.ExpectCommit()

	completed, err := store.CompleteJob(context.Background(), "tenant-1", "job-running", map[string]any{"ok": true}, now)
	if err != nil {
		t.Fatalf("CompleteJob error = %v", err)
	}
	if completed.State != "completed" || completed.Progress != 100 {
		t.Fatalf("completed job = %#v", completed)
	}

	mock.ExpectBegin()
	expectTenantGUC(mock, "tenant-1")
	mock.ExpectQuery(`(?s)UPDATE jobs.*SET state = 'failed'.*WHERE tenant_id = \$1 AND id = \$2 AND state = 'running'`).
		WillReturnError(sql.ErrNoRows)
	mock.ExpectRollback()
	if _, err := store.FailJob(context.Background(), "tenant-1", "job-running", "stale", "", now.Add(time.Minute)); !errors.Is(err, ErrConflict) {
		t.Fatalf("FailJob(non-running) error = %v, want ErrConflict", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPostgresStoreStartJobLocksStackBeforeTransition(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()
	store := NewPostgresStore(db)
	startedAt := time.Date(2026, 7, 19, 9, 0, 0, 0, time.UTC)
	createdAt := startedAt.Add(-time.Minute)

	mock.ExpectBegin()
	expectTenantGUC(mock, "tenant-1")
	mock.ExpectQuery(regexp.QuoteMeta("SELECT state, COALESCE(stack_id, '') FROM jobs")).
		WithArgs("tenant-1", "job-first").
		WillReturnRows(sqlmock.NewRows([]string{"state", "stack_id"}).AddRow("pending", "stack-1"))
	mock.ExpectExec(regexp.QuoteMeta("SELECT pg_advisory_xact_lock(hashtextextended($1, 0))")).
		WithArgs("8:tenant-1stack-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(regexp.QuoteMeta("WHERE tenant_id = $1 AND stack_id = $2 AND id <> $3 AND state = 'running'")).
		WithArgs("tenant-1", "stack-1", "job-first").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectQuery(regexp.QuoteMeta("UPDATE jobs")).
		WithArgs("tenant-1", "job-first", startedAt, ProcessExecutionOwnerID()).
		WillReturnRows(jobRows().AddRow(
			"job-first", "tenant-1", nil, "stack-1", "deploy", "running", 0, 0,
			"", "", "", "", `[]`, `{}`, createdAt, startedAt, nil, createdAt, startedAt,
		))
	mock.ExpectCommit()

	got, err := store.StartJob(context.Background(), "tenant-1", "job-first", startedAt)
	if err != nil {
		t.Fatalf("StartJob: %v", err)
	}
	if got.State != "running" || got.StackID != "stack-1" || got.StartedAt == nil || !got.StartedAt.Equal(startedAt) {
		t.Fatalf("started job = %#v", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPostgresStoreStartJobReturnsStackExecutionBusyBeforeTransition(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()
	store := NewPostgresStore(db)

	mock.ExpectBegin()
	expectTenantGUC(mock, "tenant-1")
	mock.ExpectQuery(regexp.QuoteMeta("SELECT state, COALESCE(stack_id, '') FROM jobs")).
		WithArgs("tenant-1", "job-second").
		WillReturnRows(sqlmock.NewRows([]string{"state", "stack_id"}).AddRow("pending", "stack-1"))
	mock.ExpectExec(regexp.QuoteMeta("SELECT pg_advisory_xact_lock(hashtextextended($1, 0))")).
		WithArgs("8:tenant-1stack-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(regexp.QuoteMeta("WHERE tenant_id = $1 AND stack_id = $2 AND id <> $3 AND state = 'running'")).
		WithArgs("tenant-1", "stack-1", "job-second").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectRollback()

	_, err = store.StartJob(context.Background(), "tenant-1", "job-second", time.Date(2026, 7, 19, 9, 0, 1, 0, time.UTC))
	if !errors.Is(err, ErrStackExecutionBusy) {
		t.Fatalf("StartJob error = %v, want ErrStackExecutionBusy", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPostgresStoreSyncJobSnapshotUsesExecutionGenerationCAS(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()
	store := NewPostgresStore(db)
	startedAt := time.Date(2026, 7, 18, 12, 0, 0, 123000, time.UTC)
	scheduledFor := startedAt.Add(time.Minute)

	mock.ExpectBegin()
	expectTenantGUC(mock, "tenant-1")
	mock.ExpectQuery(`(?s)UPDATE jobs\s+SET instance_id = COALESCE\(NULLIF\(\$3, ''\), instance_id\),\s+type = \$5.*AND \(type = \$5 OR \(type = 'provision' AND \$5 = 'deploy'\)\)`).
		WithArgs(
			"tenant-1", "job-wait", "", "stack-1", "deploy", "pending", 0, 82,
			"resolve_managed_runtime", "Waiting for enrollment", "", "", sqlmock.AnyArg(), sqlmock.AnyArg(),
			scheduledFor, "waiting", startedAt, nil, sqlmock.AnyArg(),
		).
		WillReturnRows(jobRows().AddRow(
			"job-wait", "tenant-1", nil, "stack-1", "deploy", "pending", 0, 82,
			"resolve_managed_runtime", "Waiting for enrollment", "", "", `[]`,
			`{"job_wait":{"state":"waiting"}}`, scheduledFor, startedAt, nil, startedAt, startedAt,
		))
	mock.ExpectCommit()

	got, err := store.SyncJobSnapshot(context.Background(), SyncJobSnapshotRequest{
		Job: UpsertJobRequest{
			ID: "job-wait", TenantID: "tenant-1", StackID: "stack-1", Type: "deploy", State: "pending",
			Progress: 82, Step: "resolve_managed_runtime", Message: "Waiting for enrollment",
			Result: map[string]any{"job_wait": map[string]any{"state": "waiting"}}, ScheduledFor: scheduledFor,
		},
		ObservedState: "waiting", AttemptStartedAt: &startedAt,
	})
	if err != nil {
		t.Fatalf("SyncJobSnapshot: %v", err)
	}
	if got.State != "pending" || got.StartedAt == nil || !got.StartedAt.Equal(startedAt) {
		t.Fatalf("synced snapshot = %#v", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPostgresStoreSyncJobSnapshotMapsFencedRowToConflict(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()
	store := NewPostgresStore(db)

	mock.ExpectBegin()
	expectTenantGUC(mock, "tenant-1")
	mock.ExpectQuery(regexp.QuoteMeta("UPDATE jobs")).WillReturnError(sql.ErrNoRows)
	mock.ExpectRollback()
	_, err = store.SyncJobSnapshot(context.Background(), SyncJobSnapshotRequest{
		Job: UpsertJobRequest{
			ID: "job-terminal", TenantID: "tenant-1", StackID: "stack-1", Type: "deploy", State: "pending",
		},
		ObservedState: "waiting",
	})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("SyncJobSnapshot error = %v, want ErrConflict", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPostgresStoreClaimsWaitingResumeWithSingleConditionalUpdate(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()
	store := NewPostgresStore(db)
	claimedAt := time.Date(2026, 7, 18, 10, 3, 0, 0, time.UTC)
	nextResumeAt := "2026-07-18T10:00:00Z"

	mock.ExpectBegin()
	expectTenantGUC(mock, "tenant-1")
	mock.ExpectQuery(regexp.QuoteMeta("UPDATE jobs")).
		WithArgs("tenant-1", "job-wait", "stack-1", "deploy", "waiting_enrollment", nextResumeAt, sqlmock.AnyArg(), claimedAt, "lease-1", "server-1").
		WillReturnRows(jobRows().AddRow(
			"job-wait", "tenant-1", nil, "stack-1", "deploy", "cancelled", 0, 82,
			"resolve_managed_runtime", "Superseded by deterministic enrollment recovery", "", "", `[]`,
			`{"lease_id":"lease-1","enrollment_resume_key":"resume-1"}`, claimedAt, nil, claimedAt, claimedAt, claimedAt,
		))
	mock.ExpectCommit()

	claimed, err := store.ClaimWaitingJobResume(context.Background(), ClaimWaitingJobResumeRequest{
		TenantID: "tenant-1", JobID: "job-wait", StackID: "stack-1", JobType: "deploy",
		WaitReason: "waiting_enrollment", NextResumeAt: nextResumeAt, LeaseID: "lease-1", ServerID: "server-1",
		ResultPatch: map[string]any{"enrollment_resume_key": "resume-1"}, ClaimedAt: claimedAt,
	})
	if err != nil {
		t.Fatalf("ClaimWaitingJobResume: %v", err)
	}
	if claimed.State != "cancelled" || claimed.Result["enrollment_resume_key"] != "resume-1" {
		t.Fatalf("claimed = %#v", claimed)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPostgresStoreListsManagedDestroyRecoveryCandidatesWithExactMarkerScope(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()
	store := NewPostgresStore(db)
	now := time.Date(2026, 8, 12, 1, 0, 0, 0, time.UTC)
	markerKey := "managed_provider_decommission_recovery"
	markerSchema := "techstack.managed-provider-decommission-recovery/v1"

	mock.ExpectBegin()
	expectTenantGUC(mock, "tenant-1")
	mock.ExpectQuery(`(?s)FROM jobs.*type = 'destroy'.*state IN \('pending', 'running'\).*result_json -> \$2::text ->> 'schema'.*scheduled_for <= now\(\).*ORDER BY CASE WHEN state = 'running' THEN updated_at ELSE scheduled_for END ASC`).
		WithArgs("tenant-1", markerKey, markerSchema, 17).
		WillReturnRows(jobRows().AddRow(
			"job-recovery", "tenant-1", nil, "stack-1", "destroy", "running", 0, 50,
			"destroy", "waiting", "", "", `[]`,
			`{"managed_provider_decommission_recovery":{"schema":"techstack.managed-provider-decommission-recovery/v1","tenant_id":"tenant-1","stack_id":"stack-1"}}`,
			now, now, nil, now, now,
		))
	mock.ExpectCommit()

	items, err := store.ListManagedDestroyRecoveryCandidates(context.Background(), "tenant-1", markerKey, markerSchema, 17)
	if err != nil {
		t.Fatalf("ListManagedDestroyRecoveryCandidates: %v", err)
	}
	if len(items) != 1 || items[0].ID != "job-recovery" || items[0].StackID != "stack-1" {
		t.Fatalf("recovery candidates = %#v", items)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPostgresStoreReclaimsOnlyExactStaleManagedDestroyRecovery(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()
	store := NewPostgresStore(db)
	claimedAt := time.Date(2026, 8, 12, 1, 5, 0, 0, time.UTC)
	staleBefore := claimedAt.Add(-3 * time.Second)
	markerKey := "managed_provider_decommission_recovery"
	markerSchema := "techstack.managed-provider-decommission-recovery/v1"

	mock.ExpectBegin()
	expectTenantGUC(mock, "tenant-1")
	mock.ExpectQuery(`(?s)UPDATE jobs.*type = 'destroy' AND state = 'running' AND started_at IS NOT NULL.*updated_at <= \$7.*result_json -> \$4::text ->> 'schema'.*result_json -> \$4::text ->> 'tenant_id'.*result_json -> \$4::text ->> 'stack_id'`).
		WithArgs("tenant-1", "job-stale", "stack-1", markerKey, markerSchema, claimedAt, staleBefore).
		WillReturnRows(jobRows().AddRow(
			"job-stale", "tenant-1", nil, "stack-1", "destroy", "pending", 0, 50,
			"destroy", "Recovering stale managed provider decommission execution", "", "", `[]`,
			`{"managed_provider_decommission_recovery":{"schema":"techstack.managed-provider-decommission-recovery/v1","tenant_id":"tenant-1","stack_id":"stack-1"}}`,
			claimedAt, nil, nil, claimedAt.Add(-time.Minute), claimedAt,
		))
	mock.ExpectCommit()

	reclaimed, err := store.ReclaimStaleManagedDestroyRecovery(context.Background(), ReclaimStaleManagedDestroyRecoveryRequest{
		TenantID: "tenant-1", JobID: "job-stale", StackID: "stack-1",
		RecoveryMarkerKey: markerKey, RecoveryMarkerSchema: markerSchema,
		StaleBefore: staleBefore, ReclaimedAt: claimedAt,
	})
	if err != nil {
		t.Fatalf("ReclaimStaleManagedDestroyRecovery: %v", err)
	}
	if reclaimed.State != jobStatePending || reclaimed.StartedAt != nil || reclaimed.ScheduledFor != claimedAt {
		t.Fatalf("reclaimed job = %#v", reclaimed)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPostgresStoreRefusesFreshManagedDestroyRecoveryHeartbeat(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()
	store := NewPostgresStore(db)
	claimedAt := time.Date(2026, 8, 12, 1, 5, 0, 0, time.UTC)
	markerKey := "managed_provider_decommission_recovery"
	markerSchema := "techstack.managed-provider-decommission-recovery/v1"

	mock.ExpectBegin()
	expectTenantGUC(mock, "tenant-1")
	// A fresh heartbeat fails the UPDATE's `updated_at <= stale_before` clause;
	// SQL returns no row and no caller-visible transition is made.
	mock.ExpectQuery(`(?s)UPDATE jobs.*updated_at <= \$7.*`).
		WithArgs("tenant-1", "job-fresh", "stack-1", markerKey, markerSchema, claimedAt, claimedAt.Add(-3*time.Second)).
		WillReturnError(sql.ErrNoRows)
	mock.ExpectRollback()

	_, err = store.ReclaimStaleManagedDestroyRecovery(context.Background(), ReclaimStaleManagedDestroyRecoveryRequest{
		TenantID: "tenant-1", JobID: "job-fresh", StackID: "stack-1",
		RecoveryMarkerKey: markerKey, RecoveryMarkerSchema: markerSchema,
		StaleBefore: claimedAt.Add(-3 * time.Second), ReclaimedAt: claimedAt,
	})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("fresh heartbeat reclaim error = %v, want ErrConflict", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPostgresStoreGetJobMapsMissingRows(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	store := NewPostgresStore(db)
	mock.ExpectBegin()
	expectTenantGUC(mock, "tenant-1")
	mock.ExpectQuery(regexp.QuoteMeta("SELECT")).
		WithArgs("tenant-1", "job-missing").
		WillReturnError(sql.ErrNoRows)
	mock.ExpectRollback()

	_, err = store.GetJob(context.Background(), "tenant-1", "job-missing")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPostgresStoreListJobsByTenantUsesTenantScope(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	store := NewPostgresStore(db)
	now := time.Date(2026, 5, 28, 13, 0, 0, 0, time.UTC)
	mock.ExpectBegin()
	expectTenantGUC(mock, "tenant-1")
	mock.ExpectQuery(regexp.QuoteMeta("SELECT")).
		WithArgs("tenant-1", 25).
		WillReturnRows(jobRows().AddRow(
			"job-1", "tenant-1", "instance-1", "stack-1", "provision", "pending", 3, 10,
			"plan", "planning", "", "", `[]`, `{}`, now, nil, nil, now, now,
		))
	mock.ExpectCommit()

	jobs, err := store.ListJobsByTenant(context.Background(), "tenant-1", 25)
	if err != nil {
		t.Fatalf("ListJobsByTenant: %v", err)
	}
	if len(jobs) != 1 || jobs[0].ID != "job-1" {
		t.Fatalf("jobs = %#v, want job-1", jobs)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPostgresStoreListsExactProviderProvisionWaitByOperation(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	store := NewPostgresStore(db)
	now := time.Date(2026, 5, 28, 13, 0, 0, 0, time.UTC)
	mock.ExpectBegin()
	expectTenantGUC(mock, "tenant-1")
	mock.ExpectQuery(regexp.QuoteMeta("SELECT")).
		WithArgs("tenant-1", "operation-1", 25).
		WillReturnRows(jobRows().AddRow(
			"job-1", "tenant-1", "instance-1", "stack-1", "provision", "pending", 3, 10,
			"waiting", "waiting for provider", "", "", `[]`,
			`{"operation_id":"operation-1","job_wait":{"state":"waiting","reason":"waiting_provider_provision"}}`,
			now, nil, nil, now, now,
		))
	mock.ExpectCommit()

	jobs, err := store.ListProviderProvisionRecoveryCandidates(context.Background(), "tenant-1", "operation-1", 25)
	if err != nil {
		t.Fatalf("ListProviderProvisionRecoveryCandidates: %v", err)
	}
	if len(jobs) != 1 || jobs[0].ID != "job-1" {
		t.Fatalf("provider recovery candidates = %#v, want job-1", jobs)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func jobRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{
		"id", "tenant_id", "instance_id", "stack_id", "type", "state", "priority", "progress",
		"step", "message", "error", "error_details", "logs_json", "result_json", "scheduled_for", "started_at", "completed_at", "created_at", "updated_at",
	})
}
