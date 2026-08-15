package controlplane

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestPostgresStoreCreateStackSetsTenantAndDecodesJSON(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	store := NewPostgresStore(db)
	now := time.Date(2026, 5, 28, 12, 0, 0, 0, time.UTC)
	req := CreateStackRequest{
		ID:             "stack-1",
		TenantID:       "tenant-1",
		InstanceID:     "instance-1",
		OwnerSubjectID: "auth0|user-1",
		Name:           "Media Stack",
		Description:    "test stack",
		Mode:           "easy",
		Status:         "draft",
		Config:         map[string]any{"profile": "home"},
		Services:       []map[string]any{{"key": "jellyfin"}},
	}

	mock.ExpectBegin()
	expectTenantGUC(mock, "tenant-1")
	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO stacks")).
		WithArgs("stack-1", "tenant-1", "instance-1", "auth0|user-1", "", "Media Stack", "test stack", "easy", "draft", sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnRows(stackRows().AddRow(
			"stack-1", "tenant-1", "instance-1", "auth0|user-1", nil, "Media Stack", "test stack", "easy", "draft",
			`{"profile":"home"}`, `[{"key":"jellyfin"}]`, `{"phase":"created"}`, "clean", nil, now, now, nil,
		))
	mock.ExpectCommit()

	got, err := store.CreateStack(context.Background(), req)
	if err != nil {
		t.Fatalf("CreateStack: %v", err)
	}
	if got.TenantID != "tenant-1" || got.ID != "stack-1" {
		t.Fatalf("unexpected stack identity: %#v", got)
	}
	if got.Config["profile"] != "home" {
		t.Fatalf("config JSON was not decoded: %#v", got.Config)
	}
	if len(got.Services) != 1 || got.Services[0]["key"] != "jellyfin" {
		t.Fatalf("services JSON was not decoded: %#v", got.Services)
	}
	if got.RuntimeSummary["phase"] != "created" {
		t.Fatalf("runtime JSON was not decoded: %#v", got.RuntimeSummary)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPostgresStoreCreateStackMapsUniqueConflict(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	store := NewPostgresStore(db)
	mock.ExpectBegin()
	expectTenantGUC(mock, "tenant-1")
	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO stacks")).
		WillReturnError(&pgconn.PgError{Code: "23505", ConstraintName: "idx_stacks_tenant_name_active"})
	mock.ExpectRollback()

	_, err = store.CreateStack(context.Background(), CreateStackRequest{
		ID:             "stack-1",
		TenantID:       "tenant-1",
		OwnerSubjectID: "auth0|user-1",
		Name:           "Media Stack",
	})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPostgresStoreUpsertServiceRuntimePersistsSeparatedState(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()
	store := NewPostgresStore(db)
	now := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
	service := ServiceRuntime{
		ID: "service-1", TenantID: "tenant-1", StackID: "stack-1", ServerID: "server-1",
		ServiceKey: "vaultwarden", ServiceInstance: "default", Name: "Vaultwarden",
		DesiredState: "running", ObservedState: "running", HealthState: "healthy", ObservedAt: &now,
		StackKitVersion: "basement-kit@1.2.3", Access: map[string]any{"mode": "relay", "url": "https://vault.owner.kombify.me", "route_id": "route-1"},
		Capabilities: []string{"restart"}, Source: "stackkits-inventory", Metadata: map[string]any{"container_id": "container-1"},
	}
	mock.ExpectBegin()
	expectTenantGUC(mock, "tenant-1")
	expectServerEventDatabaseTime(mock, now)
	mock.ExpectQuery(serviceAggregateHeadPattern()).
		WithArgs("tenant-1", "service-1").
		WillReturnRows(serviceAggregateRows())
	// Status is the derived observed projection, never the health value.
	mock.ExpectQuery(serviceAggregateInsertPattern()).
		WithArgs(
			"service-1", "tenant-1", "", "stack-1", "", "server-1",
			"server", "", "", "", "", "", "", nil,
			"vaultwarden", "default",
			"Vaultwarden", "running", "stackkits-inventory", "https://vault.owner.kombify.me", "",
			sqlmock.AnyArg(), "running", "running", "healthy", now, "basement-kit@1.2.3",
			sqlmock.AnyArg(), sqlmock.AnyArg(), int64(1), "managed",
		).
		WillReturnRows(serviceAggregateRows().AddRow(
			"service-1", "tenant-1", nil, "stack-1", "server-1", "server",
			nil, nil, nil, nil, nil, nil, nil,
			"vaultwarden", "default", "Vaultwarden",
			"running", "running", "healthy", "managed", now, "basement-kit@1.2.3",
			`{"mode":"relay","route_id":"route-1","url":"https://vault.owner.kombify.me"}`, `["restart"]`,
			"stackkits-inventory", `{"container_id":"container-1"}`, now, now,
			int64(1), "running", "", "", "https://vault.owner.kombify.me",
		))
	for _, dimension := range []struct{ name, to string }{
		{name: "desired", to: "running"},
		{name: "observed", to: "running"},
		{name: "health", to: "healthy"},
		{name: "management", to: "managed"},
	} {
		mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO service_state_transitions (")).
			WillReturnRows(serviceTransitionRows().AddRow(
				int64(1), "tenant-1", "service-1", dimension.name, nil, dimension.to,
				nil, "stackkits-inventory", now, `{}`, now,
			))
	}
	mock.ExpectCommit()

	got, err := store.UpsertServiceRuntime(context.Background(), service)
	if err != nil {
		t.Fatalf("UpsertServiceRuntime: %v", err)
	}
	if got.HealthState != "healthy" || got.Access["mode"] != "relay" || len(got.Capabilities) != 1 {
		t.Fatalf("unexpected service runtime: %#v", got)
	}
	if got.ObservedState != "running" {
		t.Fatalf("observed state was conflated with health: %#v", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPostgresStoreGetServiceRuntimeFallsBackToUnknownLegacyProjection(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()
	store := NewPostgresStore(db)
	now := time.Date(2026, 7, 17, 14, 0, 0, 0, time.UTC)

	mock.ExpectBegin()
	expectTenantGUC(mock, "tenant-1")
	mock.ExpectQuery(regexp.QuoteMeta("FROM services WHERE tenant_id = $1 AND id = $2")+`.*target_kind = 'managed_workload'`).
		WithArgs("tenant-1", "legacy-service-1").
		WillReturnRows(serviceRuntimeRows())
	mock.ExpectQuery(regexp.QuoteMeta("FROM services legacy")).
		WithArgs("tenant-1", "legacy-service-1").
		WillReturnRows(serviceRuntimeRows().AddRow(
			"legacy-service-1", "tenant-1", nil, "stack-1", "server-1", "server",
			nil, nil, nil, nil, nil, nil, nil,
			"vaultwarden", "default", "Vaultwarden",
			"running", "unknown", "unknown", "observed", nil, nil,
			`{"mode":"unavailable","reason_code":"legacy_backfill_requires_observation"}`, `[]`,
			legacyServiceRuntimeBackfillSource, `{"backfill":true,"legacy_source":"legacy-registry","legacy_status":"healthy"}`, now, now,
		))
	mock.ExpectCommit()

	got, err := store.GetServiceRuntime(context.Background(), "tenant-1", "legacy-service-1")
	if err != nil {
		t.Fatalf("GetServiceRuntime: %v", err)
	}
	if got.ServerID != "server-1" || got.HealthState != legacyServiceRuntimeUnknown || got.ObservedAt != nil || got.Access[legacyServiceAccessModeKey] != legacyServiceRuntimeUnavailable {
		t.Fatalf("unsafe legacy projection: %#v", got)
	}
	if got.Metadata["backfill"] != true || got.Source != legacyServiceRuntimeBackfillSource {
		t.Fatalf("missing backfill provenance: %#v", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPostgresStoreListServiceRuntimesCombinesMeasuredAndSafeBackfill(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()
	store := NewPostgresStore(db)
	now := time.Date(2026, 7, 17, 14, 0, 0, 0, time.UTC)

	mock.ExpectBegin()
	expectTenantGUC(mock, "tenant-1")
	mock.ExpectQuery(regexp.QuoteMeta("FROM services")+".*"+regexp.QuoteMeta("WHERE tenant_id = $1")+`.*target_kind = 'managed_workload'`).
		WithArgs("tenant-1", "stack-1", "").
		WillReturnRows(serviceRuntimeRows().AddRow(
			"service-measured", "tenant-1", nil, "stack-1", "server-1", "server",
			nil, nil, nil, nil, nil, nil, nil,
			"z-service", "default", "Measured",
			legacyServiceDesiredRunning, legacyServiceDesiredRunning, "healthy", "managed", now, "basement-kit@1.2.3",
			`{"mode":"direct","url":"https://service.example.test"}`, `[]`, "stackkits-inventory", `{}`, now, now,
		))
	mock.ExpectQuery(regexp.QuoteMeta("FROM services legacy")).
		WithArgs("tenant-1", "stack-1", "").
		WillReturnRows(serviceRuntimeRows().AddRow(
			"legacy-service", "tenant-1", nil, "stack-1", "server-1", "server",
			nil, nil, nil, nil, nil, nil, nil,
			"a-service", "default", "Backfilled",
			legacyServiceDesiredRunning, legacyServiceRuntimeUnknown, legacyServiceRuntimeUnknown, "observed", nil, nil,
			`{"mode":"unavailable","reason_code":"legacy_backfill_requires_observation"}`, `[]`,
			legacyServiceRuntimeBackfillSource, `{"backfill":true}`, now, now,
		))
	mock.ExpectCommit()

	rows, err := store.ListServiceRuntimes(context.Background(), "tenant-1", "stack-1", "")
	if err != nil {
		t.Fatalf("ListServiceRuntimes: %v", err)
	}
	if len(rows) != 2 || rows[0].ID != "legacy-service" || rows[1].ID != "service-measured" {
		t.Fatalf("combined rows = %#v", rows)
	}
	if rows[0].HealthState != legacyServiceRuntimeUnknown || rows[0].Access[legacyServiceAccessModeKey] != legacyServiceRuntimeUnavailable {
		t.Fatalf("unsafe backfill row: %#v", rows[0])
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

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

func TestPostgresStoreUpsertWalletItemUsesMetadataJSON(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	store := NewPostgresStore(db)
	now := time.Date(2026, 5, 28, 14, 0, 0, 0, time.UTC)
	item := WalletItem{
		ID:          "wallet-1",
		TenantID:    "tenant-1",
		InstanceID:  "instance-1",
		StackID:     "stack-1",
		ItemType:    "password",
		Provider:    "manual",
		ExternalRef: "svc-1",
		Metadata:    map[string]any{"name": "Admin", "has_secret": true},
	}

	mock.ExpectBegin()
	expectTenantGUC(mock, "tenant-1")
	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO wallet_items")).
		WithArgs("wallet-1", "tenant-1", "instance-1", "stack-1", "password", "manual", "svc-1", sqlmock.AnyArg()).
		WillReturnRows(walletRows().AddRow(
			"wallet-1", "tenant-1", "instance-1", "stack-1", "password", "manual", "svc-1",
			`{"name":"Admin","has_secret":true}`, now, now,
		))
	mock.ExpectCommit()

	got, err := store.UpsertWalletItem(context.Background(), item)
	if err != nil {
		t.Fatalf("UpsertWalletItem: %v", err)
	}
	if got.ID != "wallet-1" || got.TenantID != "tenant-1" {
		t.Fatalf("unexpected wallet identity: %#v", got)
	}
	if got.Metadata["name"] != "Admin" || got.Metadata["has_secret"] != true {
		t.Fatalf("metadata JSON was not decoded: %#v", got.Metadata)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPostgresStoreListWalletItemsFiltersByTenantAndStack(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	store := NewPostgresStore(db)
	now := time.Date(2026, 5, 28, 14, 15, 0, 0, time.UTC)

	mock.ExpectBegin()
	expectTenantGUC(mock, "tenant-1")
	mock.ExpectQuery(regexp.QuoteMeta("SELECT")).
		WithArgs("tenant-1", "stack-1").
		WillReturnRows(walletRows().AddRow(
			"wallet-1", "tenant-1", "instance-1", "stack-1", "password", "manual", "svc-1",
			`{"name":"Admin"}`, now, now,
		))
	mock.ExpectCommit()

	items, err := store.ListWalletItems(context.Background(), "tenant-1", "stack-1")
	if err != nil {
		t.Fatalf("ListWalletItems: %v", err)
	}
	if len(items) != 1 || items[0].ID != "wallet-1" {
		t.Fatalf("items = %#v, want wallet-1", items)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPostgresStoreDeleteWalletItemMapsMissingRows(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	store := NewPostgresStore(db)
	mock.ExpectBegin()
	expectTenantGUC(mock, "tenant-1")
	mock.ExpectExec(regexp.QuoteMeta("DELETE FROM wallet_items")).
		WithArgs("tenant-1", "wallet-missing").
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectRollback()

	err = store.DeleteWalletItem(context.Background(), "tenant-1", "wallet-missing")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPostgresStoreUpsertTenantUsesTenantIsolation(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	store := NewPostgresStore(db)
	now := time.Date(2026, 5, 28, 15, 0, 0, 0, time.UTC)
	tenant := Tenant{
		ID:            "tenant-1",
		ExternalOrgID: "org_123",
		DisplayName:   "Tenant One",
		Kind:          "saas",
		Status:        "active",
		Metadata:      map[string]any{"plan": "pro"},
	}

	mock.ExpectBegin()
	expectTenantGUC(mock, "tenant-1")
	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO techstack_tenants")).
		WithArgs("tenant-1", "org_123", "Tenant One", "saas", "active", sqlmock.AnyArg()).
		WillReturnRows(tenantRows().AddRow(
			"tenant-1", "org_123", "Tenant One", "saas", "active", `{"plan":"pro"}`, now, now,
		))
	mock.ExpectCommit()

	got, err := store.UpsertTenant(context.Background(), tenant)
	if err != nil {
		t.Fatalf("UpsertTenant: %v", err)
	}
	if got.ID != "tenant-1" || got.ExternalOrgID != "org_123" || got.Metadata["plan"] != "pro" {
		t.Fatalf("unexpected tenant: %#v", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPostgresStoreEnsureTenantPreservesExistingTenant(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	store := NewPostgresStore(db)
	now := time.Date(2026, 7, 18, 15, 0, 0, 0, time.UTC)

	mock.ExpectBegin()
	expectTenantGUC(mock, "default")
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO techstack_tenants")).
		WithArgs("default", "", "default", "self_hosted", "active", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT id, external_org_id, display_name, kind, status")).
		WithArgs("default").
		WillReturnRows(tenantRows().AddRow(
			"default", "org-existing", "Existing tenant", "embedded", "active", `{"keep":true}`, now, now,
		))
	mock.ExpectCommit()

	got, err := store.EnsureTenant(context.Background(), Tenant{ID: "default"})
	if err != nil {
		t.Fatalf("EnsureTenant: %v", err)
	}
	if got.DisplayName != "Existing tenant" || got.Kind != "embedded" || got.Metadata["keep"] != true {
		t.Fatalf("existing tenant was not preserved: %#v", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPostgresStoreUpsertUserAndMembershipDecodeMetadata(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	store := NewPostgresStore(db)
	now := time.Date(2026, 5, 28, 15, 30, 0, 0, time.UTC)

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO techstack_users")).
		WithArgs("user-1", "owner@example.test", "Owner", "active", sqlmock.AnyArg()).
		WillReturnRows(userRows().AddRow(
			"user-1", "owner@example.test", "Owner", "active", `{"source":"auth0"}`, now, now,
		))
	mock.ExpectCommit()

	user, err := store.UpsertUser(context.Background(), User{
		ID:           "user-1",
		PrimaryEmail: "owner@example.test",
		DisplayName:  "Owner",
		Status:       "active",
		Metadata:     map[string]any{"source": "auth0"},
	})
	if err != nil {
		t.Fatalf("UpsertUser: %v", err)
	}
	if user.ID != "user-1" || user.Metadata["source"] != "auth0" {
		t.Fatalf("unexpected user: %#v", user)
	}

	mock.ExpectBegin()
	expectTenantGUC(mock, "tenant-1")
	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO techstack_memberships")).
		WithArgs("membership-1", "tenant-1", "user-1", "owner", "auth0", "auth0|user-1", "active", sqlmock.AnyArg()).
		WillReturnRows(membershipRows().AddRow(
			"membership-1", "tenant-1", "user-1", "owner", "auth0", "auth0|user-1", "active", `{"org":"org_123"}`, now, now,
		))
	mock.ExpectCommit()

	membership, err := store.UpsertMembership(context.Background(), Membership{
		ID:          "membership-1",
		TenantID:    "tenant-1",
		UserID:      "user-1",
		RoleKey:     "owner",
		ProviderKey: "auth0",
		SubjectID:   "auth0|user-1",
		Status:      "active",
		Metadata:    map[string]any{"org": "org_123"},
	})
	if err != nil {
		t.Fatalf("UpsertMembership: %v", err)
	}
	if membership.ID != "membership-1" || membership.Metadata["org"] != "org_123" {
		t.Fatalf("unexpected membership: %#v", membership)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPostgresStoreGetMembershipUsesTenantScope(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	store := NewPostgresStore(db)
	now := time.Date(2026, 6, 28, 13, 0, 0, 0, time.UTC)

	mock.ExpectBegin()
	expectTenantGUC(mock, "tenant-1")
	mock.ExpectQuery(regexp.QuoteMeta("SELECT id, tenant_id, user_id, role_key, provider_key, subject_id")).
		WithArgs("tenant-1", "auth0|runtime-user").
		WillReturnRows(membershipRows().AddRow(
			"tenant-1:auth0|runtime-user", "tenant-1", "auth0|runtime-user", "global_admin", "cloud", "auth0|runtime-user", "active", `{"source":"auth0"}`, now, now,
		))
	mock.ExpectCommit()

	membership, err := store.GetMembership(context.Background(), "tenant-1", "auth0|runtime-user")
	if err != nil {
		t.Fatalf("GetMembership: %v", err)
	}
	if membership.RoleKey != "global_admin" || membership.Metadata["source"] != "auth0" {
		t.Fatalf("unexpected membership: %#v", membership)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPostgresStoreAuthConfigAndBreakglassUseTenantScope(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	store := NewPostgresStore(db)
	now := time.Date(2026, 5, 28, 16, 0, 0, 0, time.UTC)

	mock.ExpectBegin()
	expectTenantGUC(mock, "tenant-1")
	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO auth_config")).
		WithArgs("auth-1", "tenant-1", "instance-1", "cloud", sqlmock.AnyArg()).
		WillReturnRows(authConfigRows().AddRow(
			"auth-1", "tenant-1", "instance-1", "cloud", `{"issuer":"https://auth.example.test/"}`, now, now,
		))
	mock.ExpectCommit()

	config, err := store.UpsertAuthConfig(context.Background(), AuthConfig{
		ID:         "auth-1",
		TenantID:   "tenant-1",
		InstanceID: "instance-1",
		Mode:       "cloud",
		Config:     map[string]any{"issuer": "https://auth.example.test/"},
	})
	if err != nil {
		t.Fatalf("UpsertAuthConfig: %v", err)
	}
	if config.Mode != "cloud" || config.Config["issuer"] != "https://auth.example.test/" {
		t.Fatalf("unexpected auth config: %#v", config)
	}

	mock.ExpectBegin()
	expectTenantGUC(mock, "tenant-1")
	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO breakglass_admin")).
		WithArgs("breakglass-1", "tenant-1", "user-1", "owner@example.test", "$argon2id$hash", false, sqlmock.AnyArg()).
		WillReturnRows(breakglassRows().AddRow(
			"breakglass-1", "tenant-1", "user-1", "owner@example.test", "$argon2id$hash", false, nil, `{"created_by":"test"}`, now, now,
		))
	mock.ExpectCommit()

	admin, err := store.UpsertBreakglassAdmin(context.Background(), BreakglassAdmin{
		ID:           "breakglass-1",
		TenantID:     "tenant-1",
		UserID:       "user-1",
		Email:        "owner@example.test",
		PasswordHash: "$argon2id$hash",
		Metadata:     map[string]any{"created_by": "test"},
	})
	if err != nil {
		t.Fatalf("UpsertBreakglassAdmin: %v", err)
	}
	if admin.Email != "owner@example.test" || admin.Metadata["created_by"] != "test" {
		t.Fatalf("unexpected breakglass admin: %#v", admin)
	}

	mock.ExpectBegin()
	expectTenantGUC(mock, "tenant-1")
	mock.ExpectQuery(regexp.QuoteMeta("SELECT id, tenant_id")).
		WithArgs("tenant-1").
		WillReturnRows(breakglassRows().AddRow(
			"breakglass-1", "tenant-1", "user-1", "owner@example.test", "$argon2id$hash", false, nil, `{"created_by":"test"}`, now, now,
		))
	mock.ExpectCommit()

	admin, err = store.GetBreakglassAdmin(context.Background(), "tenant-1")
	if err != nil {
		t.Fatalf("GetBreakglassAdmin: %v", err)
	}
	if admin.ID != "breakglass-1" {
		t.Fatalf("unexpected breakglass admin: %#v", admin)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPostgresStoreAppendAndListActivityUseTenantScope(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	store := NewPostgresStore(db)
	now := time.Date(2026, 5, 28, 19, 0, 0, 0, time.UTC)

	mock.ExpectBegin()
	expectTenantGUC(mock, "tenant-1")
	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO activity_log")).
		WithArgs("activity-1", "tenant-1", "instance-1", "stack-1", "auth0|user-1", "wallet_reveal", "wallet", "info", "revealed", sqlmock.AnyArg(), "stack:stack-1", "", "", "").
		WillReturnRows(activityRows().AddRow(
			"activity-1", "tenant-1", "instance-1", "stack-1", "auth0|user-1", "wallet_reveal", "wallet", "info", "revealed", `{"resource_id":"wallet-1"}`, "stack:stack-1", nil, nil, nil, now,
		))
	mock.ExpectCommit()

	event, err := store.AppendActivity(context.Background(), ActivityEvent{
		ID:             "activity-1",
		TenantID:       "tenant-1",
		InstanceID:     "instance-1",
		StackID:        "stack-1",
		ActorSubjectID: "auth0|user-1",
		Action:         "wallet_reveal",
		Category:       "wallet",
		Severity:       "info",
		Message:        "revealed",
		Details:        map[string]any{"resource_id": "wallet-1"},
	})
	if err != nil {
		t.Fatalf("AppendActivity: %v", err)
	}
	if event.ID != "activity-1" || event.Details["resource_id"] != "wallet-1" {
		t.Fatalf("unexpected activity: %#v", event)
	}

	mock.ExpectBegin()
	expectTenantGUC(mock, "tenant-1")
	mock.ExpectQuery(regexp.QuoteMeta("SELECT id, tenant_id")+".*"+regexp.QuoteMeta("FROM activity_log")+".*"+regexp.QuoteMeta("WHERE tenant_id = $1")+".*"+regexp.QuoteMeta("ORDER BY created_at DESC, id DESC")+".*"+regexp.QuoteMeta("LIMIT $8")).
		WithArgs("tenant-1", "stack-1", "", "", "", nil, "", 10).
		WillReturnRows(activityRows().AddRow(
			"activity-1", "tenant-1", "instance-1", "stack-1", "auth0|user-1", "wallet_reveal", "wallet", "info", "revealed", `{"resource_id":"wallet-1"}`, "stack:stack-1", nil, nil, nil, now,
		))
	mock.ExpectCommit()

	events, err := store.ListActivity(context.Background(), " tenant-1 ", " stack-1 ", 10)
	if err != nil {
		t.Fatalf("ListActivity: %v", err)
	}
	if len(events) != 1 || events[0].ID != "activity-1" {
		t.Fatalf("events = %#v, want activity-1", events)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPostgresStoreListActivityUsesDefaultLimitAndDeterministicOrder(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	store := NewPostgresStore(db)
	now := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	mock.ExpectBegin()
	expectTenantGUC(mock, "tenant-1")
	mock.ExpectQuery(regexp.QuoteMeta("SELECT id, tenant_id")+".*"+regexp.QuoteMeta("FROM activity_log")+".*"+regexp.QuoteMeta("WHERE tenant_id = $1")+".*"+regexp.QuoteMeta("ORDER BY created_at DESC, id DESC")+".*"+regexp.QuoteMeta("LIMIT $8")).
		WithArgs("tenant-1", "", "", "", "", nil, "", 50).
		WillReturnRows(activityRows().AddRow(
			"activity-1", "tenant-1", "instance-1", "stack-1", "auth0|user-1", "wallet_reveal", "wallet", "info", "revealed", `{}`, "stack:stack-1", nil, nil, nil, now,
		))
	mock.ExpectCommit()

	events, err := store.ListActivity(context.Background(), " tenant-1 ", "", 0)
	if err != nil {
		t.Fatalf("ListActivity: %v", err)
	}
	if len(events) != 1 || events[0].ID != "activity-1" {
		t.Fatalf("events = %#v, want activity-1", events)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPostgresStoreWorkerAndPairingTokenUseTenantScope(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	store := NewPostgresStore(db)
	now := time.Date(2026, 5, 28, 20, 0, 0, 0, time.UTC)
	expiresAt := now.Add(time.Hour)

	mock.ExpectBegin()
	expectTenantGUC(mock, "tenant-1")
	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO pairing_tokens")).
		WithArgs("pair-1", "tenant-1", "instance-1", "stack-1", "auth0|user-1", "worker setup", "hash-1", "active", expiresAt, nil, sqlmock.AnyArg()).
		WillReturnRows(pairingTokenRows().AddRow(
			"pair-1", "tenant-1", "instance-1", "stack-1", "auth0|user-1", "worker setup", "hash-1", "active", expiresAt, nil, `{"scope":"worker"}`, now, now,
		))
	mock.ExpectCommit()

	token, err := store.UpsertPairingToken(context.Background(), PairingToken{
		ID:             "pair-1",
		TenantID:       "tenant-1",
		InstanceID:     "instance-1",
		StackID:        "stack-1",
		OwnerSubjectID: "auth0|user-1",
		Name:           "worker setup",
		TokenHash:      "hash-1",
		Status:         "active",
		ExpiresAt:      &expiresAt,
		Metadata:       map[string]any{"scope": "worker"},
	})
	if err != nil {
		t.Fatalf("UpsertPairingToken: %v", err)
	}
	if token.ID != "pair-1" || token.Metadata["scope"] != "worker" {
		t.Fatalf("unexpected pairing token: %#v", token)
	}

	mock.ExpectBegin()
	expectTenantGUC(mock, "tenant-1")
	mock.ExpectQuery(regexp.QuoteMeta("SELECT id, tenant_id")+".*"+regexp.QuoteMeta("FROM pairing_tokens")+".*"+regexp.QuoteMeta("WHERE tenant_id = $1 AND token_hash = $2")).
		WithArgs("tenant-1", "hash-1").
		WillReturnRows(pairingTokenRows().AddRow(
			"pair-1", "tenant-1", "instance-1", "stack-1", "auth0|user-1", "worker setup", "hash-1", "active", expiresAt, nil, `{"scope":"worker"}`, now, now,
		))
	mock.ExpectCommit()

	resolvedToken, err := store.GetPairingTokenByHash(context.Background(), "tenant-1", "hash-1")
	if err != nil {
		t.Fatalf("GetPairingTokenByHash: %v", err)
	}
	if resolvedToken.ID != "pair-1" || resolvedToken.TenantID != "tenant-1" {
		t.Fatalf("unexpected resolved pairing token: %#v", resolvedToken)
	}

	mock.ExpectBegin()
	expectTenantGUC(mock, "tenant-1")
	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO workers")).
		WithArgs("worker-1", "tenant-1", "instance-1", "stack-1", "agent-a", "10.0.0.10", "linux", "amd64", "hash-1", "pending", false, nil, sqlmock.AnyArg(), 4, 8192, 100, "", false, true, "25.0", "agent", "self-hosted", sqlmock.AnyArg(), "auth0|user-1", sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnRows(workerRows().AddRow(
			"worker-1", "tenant-1", "instance-1", "stack-1", "agent-a", "10.0.0.10", "linux", "amd64", "hash-1", "pending", false, nil, now, 4, 8192, 100, "", false, true, "25.0", "agent", "self-hosted", `{"zone":"lab"}`, "auth0|user-1", `{"shell":true}`, `{"cpu":"4"}`, now, now,
		))
	mock.ExpectCommit()

	worker, err := store.UpsertWorkerHeartbeat(context.Background(), Worker{
		ID:             "worker-1",
		TenantID:       "tenant-1",
		InstanceID:     "instance-1",
		StackID:        "stack-1",
		Hostname:       "agent-a",
		IP:             "10.0.0.10",
		OS:             "linux",
		Arch:           "amd64",
		TokenHash:      "hash-1",
		Status:         "pending",
		CPUCores:       4,
		RAMMB:          8192,
		DiskGB:         100,
		HasHWTranscode: true,
		DockerVersion:  "25.0",
		Type:           "agent",
		Provider:       "self-hosted",
		Tags:           map[string]any{"zone": "lab"},
		OwnerSubjectID: "auth0|user-1",
		Capabilities:   map[string]any{"shell": true},
		Resources:      map[string]any{"cpu": "4"},
	})
	if err != nil {
		t.Fatalf("UpsertWorkerHeartbeat: %v", err)
	}
	if worker.ID != "worker-1" || worker.StackID != "stack-1" || worker.Tags["zone"] != "lab" {
		t.Fatalf("unexpected worker: %#v", worker)
	}

	mock.ExpectBegin()
	expectTenantGUC(mock, "tenant-1")
	mock.ExpectQuery(regexp.QuoteMeta("UPDATE workers")).
		WithArgs("tenant-1", "worker-1", "auth0|user-1", now).
		WillReturnRows(workerRows().AddRow(
			"worker-1", "tenant-1", "instance-1", "stack-1", "agent-a", "10.0.0.10", "linux", "amd64", "hash-1", "approved", true, now, now, 4, 8192, 100, "", false, true, "25.0", "agent", "self-hosted", `{"zone":"lab"}`, "auth0|user-1", `{"shell":true}`, `{"cpu":"4"}`, now, now,
		))
	mock.ExpectCommit()

	worker, err = store.ApproveWorker(context.Background(), "tenant-1", "worker-1", "auth0|user-1", now)
	if err != nil {
		t.Fatalf("ApproveWorker: %v", err)
	}
	if !worker.Approved || worker.Status != "approved" {
		t.Fatalf("worker was not approved: %#v", worker)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPostgresStoreClaimPairingTokenUsesAtomicEligibilityUpdate(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	store := NewPostgresStore(db)
	claimedAt := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	expiresAt := claimedAt.Add(time.Hour)
	claimQuery := regexp.QuoteMeta("UPDATE pairing_tokens") + ".*" +
		regexp.QuoteMeta("SET status = 'used', used_at = $3, updated_at = $3") + ".*" +
		regexp.QuoteMeta("AND status = 'active' AND used_at IS NULL") + ".*" +
		regexp.QuoteMeta("AND (expires_at IS NULL OR expires_at > $3)") + ".*" +
		regexp.QuoteMeta("RETURNING")

	mock.ExpectBegin()
	expectTenantGUC(mock, "tenant-1")
	mock.ExpectQuery(claimQuery).
		WithArgs("tenant-1", "hash-1", claimedAt).
		WillReturnRows(pairingTokenRows().AddRow(
			"pair-1", "tenant-1", "instance-1", "stack-1", "auth0|user-1", "worker setup", "hash-1", "used", expiresAt, claimedAt, `{}`, claimedAt, claimedAt,
		))
	mock.ExpectCommit()

	claimed, err := store.ClaimPairingToken(context.Background(), "tenant-1", "hash-1", claimedAt)
	if err != nil || claimed.Status != "used" || claimed.UsedAt == nil || !claimed.UsedAt.Equal(claimedAt) {
		t.Fatalf("ClaimPairingToken = %#v, %v", claimed, err)
	}

	mock.ExpectBegin()
	expectTenantGUC(mock, "tenant-1")
	mock.ExpectQuery(claimQuery).
		WithArgs("tenant-1", "hash-1", claimedAt).
		WillReturnRows(pairingTokenRows())
	mock.ExpectRollback()
	if _, err := store.ClaimPairingToken(context.Background(), "tenant-1", "hash-1", claimedAt); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second ClaimPairingToken error = %v, want ErrNotFound", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPostgresStoreRegistryUsesTenantScope(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	store := NewPostgresStore(db)
	now := time.Date(2026, 5, 28, 21, 0, 0, 0, time.UTC)

	mock.ExpectBegin()
	expectTenantGUC(mock, "tenant-1")
	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO nodes")).
		WithArgs("node-1", "tenant-1", "instance-1", "stack-1", "", "media stack", "main", "online", "10.0.0.10", sqlmock.AnyArg()).
		WillReturnRows(nodeRows().AddRow(
			"node-1", "tenant-1", "instance-1", "stack-1", nil, "media stack", "main", "online", "10.0.0.10", `{"source":"stackkit_outputs"}`, now, now,
		))
	mock.ExpectCommit()

	node, err := store.UpsertNode(context.Background(), Node{
		ID:         "node-1",
		TenantID:   "tenant-1",
		InstanceID: "instance-1",
		StackID:    "stack-1",
		Name:       "media stack",
		Role:       "main",
		Status:     "online",
		Address:    "10.0.0.10",
		Metadata:   map[string]any{"source": "stackkit_outputs"},
	})
	if err != nil {
		t.Fatalf("UpsertNode: %v", err)
	}
	if node.ID != "node-1" || node.Metadata["source"] != "stackkit_outputs" {
		t.Fatalf("unexpected node: %#v", node)
	}

	mock.ExpectBegin()
	expectTenantGUC(mock, "tenant-1")
	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO services")).
		WithArgs("service-1", "tenant-1", "instance-1", "stack-1", "node-1", "jellyfin", "Jellyfin", "running", "stackkit_outputs", "https://jellyfin.example.test", "", sqlmock.AnyArg(), "managed").
		WillReturnRows(serviceRows().AddRow(
			"service-1", "tenant-1", "instance-1", "stack-1", "node-1", "jellyfin", "Jellyfin", "running", "stackkit_outputs", "https://jellyfin.example.test", nil, `{"port":8096}`, "managed", now, now,
		))
	mock.ExpectCommit()

	service, err := store.UpsertService(context.Background(), Service{
		ID:         "service-1",
		TenantID:   "tenant-1",
		InstanceID: "instance-1",
		StackID:    "stack-1",
		NodeID:     "node-1",
		ServiceKey: "jellyfin",
		Name:       "Jellyfin",
		Status:     "running",
		Source:     "stackkit_outputs",
		URL:        "https://jellyfin.example.test",
		Metadata:   map[string]any{"port": float64(8096)},
	})
	if err != nil {
		t.Fatalf("UpsertService: %v", err)
	}
	if service.ID != "service-1" || service.Metadata["port"] != float64(8096) {
		t.Fatalf("unexpected service: %#v", service)
	}

	mock.ExpectBegin()
	expectTenantGUC(mock, "tenant-1")
	mock.ExpectQuery(regexp.QuoteMeta("SELECT id, tenant_id")).
		WithArgs("tenant-1", "stack-1").
		WillReturnRows(nodeRows().AddRow(
			"node-1", "tenant-1", "instance-1", "stack-1", nil, "media stack", "main", "online", "10.0.0.10", `{"source":"stackkit_outputs"}`, now, now,
		))
	mock.ExpectCommit()

	nodes, err := store.ListNodesByStack(context.Background(), "tenant-1", "stack-1")
	if err != nil {
		t.Fatalf("ListNodesByStack: %v", err)
	}
	if len(nodes) != 1 || nodes[0].ID != "node-1" {
		t.Fatalf("nodes = %#v, want node-1", nodes)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPostgresStoreRILStateUsesTenantScope(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	store := NewPostgresStore(db)
	now := time.Date(2026, 5, 28, 22, 0, 0, 0, time.UTC)

	mock.ExpectBegin()
	expectTenantGUC(mock, "tenant-1")
	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO ril_servers")).
		WithArgs("server-1", "tenant-1", "instance-1", "stack-1", "node-1", "media host", "healthy", sqlmock.AnyArg(), sqlmock.AnyArg(), now).
		WillReturnRows(rilServerRows().AddRow(
			"server-1", "tenant-1", "instance-1", "stack-1", "node-1", "media host", "healthy", `{"ok":true}`, `{"cpu":"4"}`, now, now, now,
		))
	mock.ExpectCommit()

	server, err := store.UpsertRILServer(context.Background(), RILServer{
		ID:         "server-1",
		TenantID:   "tenant-1",
		InstanceID: "instance-1",
		StackID:    "stack-1",
		NodeID:     "node-1",
		Name:       "media host",
		Status:     "healthy",
		Health:     map[string]any{"ok": true},
		Inventory:  map[string]any{"cpu": "4"},
		LastSeenAt: &now,
	})
	if err != nil {
		t.Fatalf("UpsertRILServer: %v", err)
	}
	if server.ID != "server-1" || server.Health["ok"] != true {
		t.Fatalf("unexpected RIL server: %#v", server)
	}

	mock.ExpectBegin()
	expectTenantGUC(mock, "tenant-1")
	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO ril_commands")).
		WithArgs("cmd-1", "tenant-1", "server-1", "auth0|user-1", "shell", "queued", sqlmock.AnyArg(), sqlmock.AnyArg(), "", nil).
		WillReturnRows(rilCommandRows().AddRow(
			"cmd-1", "tenant-1", "server-1", "auth0|user-1", "shell", "queued", `{"argv":["uptime"]}`, `{}`, nil, now, now, nil,
		))
	mock.ExpectCommit()

	command, err := store.EnqueueRILCommand(context.Background(), RILCommand{
		ID:             "cmd-1",
		TenantID:       "tenant-1",
		ServerID:       "server-1",
		ActorSubjectID: "auth0|user-1",
		CommandClass:   "shell",
		Status:         "queued",
		Request:        map[string]any{"argv": []any{"uptime"}},
	})
	if err != nil {
		t.Fatalf("EnqueueRILCommand: %v", err)
	}
	if command.ID != "cmd-1" || command.Request["argv"] == nil {
		t.Fatalf("unexpected RIL command: %#v", command)
	}

	mock.ExpectBegin()
	expectTenantGUC(mock, "tenant-1")
	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO ril_action_cards")).
		WithArgs("card-1", "tenant-1", "server-1", "stack-1", "Restart service", "open", "warning", sqlmock.AnyArg(), sqlmock.AnyArg(), nil).
		WillReturnRows(rilActionCardRows().AddRow(
			"card-1", "tenant-1", "server-1", "stack-1", "Restart service", "open", "warning", `{"kind":"restart"}`, `{}`, now, now, nil,
		))
	mock.ExpectCommit()

	card, err := store.UpsertActionCard(context.Background(), RILActionCard{
		ID:       "card-1",
		TenantID: "tenant-1",
		ServerID: "server-1",
		StackID:  "stack-1",
		Title:    "Restart service",
		Status:   "open",
		Severity: "warning",
		Action:   map[string]any{"kind": "restart"},
	})
	if err != nil {
		t.Fatalf("UpsertActionCard: %v", err)
	}
	if card.ID != "card-1" || card.Action["kind"] != "restart" {
		t.Fatalf("unexpected action card: %#v", card)
	}

	mock.ExpectBegin()
	expectTenantGUC(mock, "tenant-1")
	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO ril_heal_events")).
		WithArgs("heal-1", "tenant-1", "server-1", "card-1", "completed", "service unhealthy", sqlmock.AnyArg()).
		WillReturnRows(rilHealEventRows().AddRow(
			"heal-1", "tenant-1", "server-1", "card-1", "completed", "service unhealthy", `{"service":"jellyfin"}`, now, now,
		))
	mock.ExpectCommit()

	event, err := store.RecordHealEvent(context.Background(), RILHealEvent{
		ID:           "heal-1",
		TenantID:     "tenant-1",
		ServerID:     "server-1",
		ActionCardID: "card-1",
		Status:       "completed",
		Cause:        "service unhealthy",
		Details:      map[string]any{"service": "jellyfin"},
	})
	if err != nil {
		t.Fatalf("RecordHealEvent: %v", err)
	}
	if event.ID != "heal-1" || event.Details["service"] != "jellyfin" {
		t.Fatalf("unexpected heal event: %#v", event)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func expectTenantGUC(mock sqlmock.Sqlmock, tenantID string) {
	mock.ExpectExec(regexp.QuoteMeta("SELECT set_config($1, $2, true)")).
		WithArgs("app.tenant_id", tenantID).
		WillReturnResult(sqlmock.NewResult(0, 1))
}

func stackRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{
		"id", "tenant_id", "instance_id", "owner_subject_id", "homelab_id", "name", "description", "mode", "status",
		"config_json", "services_json", "runtime_summary_json", "drift_status", "drift_checked_at", "created_at", "updated_at", "deleted_at",
	})
}

func jobRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{
		"id", "tenant_id", "instance_id", "stack_id", "type", "state", "priority", "progress",
		"step", "message", "error", "error_details", "logs_json", "result_json", "scheduled_for", "started_at", "completed_at", "created_at", "updated_at",
	})
}

func walletRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{
		"id", "tenant_id", "instance_id", "stack_id", "item_type", "provider", "external_ref", "metadata_json", "created_at", "updated_at",
	})
}

func tenantRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{
		"id", "external_org_id", "display_name", "kind", "status", "metadata_json", "created_at", "updated_at",
	})
}

func userRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{
		"id", "primary_email", "display_name", "status", "metadata_json", "created_at", "updated_at",
	})
}

func membershipRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{
		"id", "tenant_id", "user_id", "role_key", "provider_key", "subject_id", "status", "metadata_json", "created_at", "updated_at",
	})
}

func authConfigRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{
		"id", "tenant_id", "instance_id", "mode", "config_json", "created_at", "updated_at",
	})
}

func breakglassRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{
		"id", "tenant_id", "user_id", "email", "password_hash", "locked", "last_used_at", "metadata_json", "created_at", "updated_at",
	})
}

func activityRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{
		"id", "tenant_id", "instance_id", "stack_id", "actor_subject_id", "action", "category", "severity", "message", "details_json",
		"runtime_scope_key", "server_scope_key", "service_scope_key", "correlation_id", "created_at",
	})
}

func workerRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{
		"id", "tenant_id", "instance_id", "stack_id", "hostname", "ip", "os", "arch", "token_hash", "status",
		"approved", "approved_at", "last_seen_at", "cpu_cores", "ram_mb", "disk_gb", "gpu", "has_nvme",
		"has_hw_transcode", "docker_version", "type", "provider", "tags_json", "owner_subject_id",
		"capabilities_json", "resources_json", "created_at", "updated_at",
	})
}

func pairingTokenRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{
		"id", "tenant_id", "instance_id", "stack_id", "owner_subject_id", "name", "token_hash", "status", "expires_at", "used_at", "metadata_json", "created_at", "updated_at",
	})
}

func nodeRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{
		"id", "tenant_id", "instance_id", "stack_id", "worker_id", "name", "role", "status", "address", "metadata_json", "created_at", "updated_at",
	})
}

func serviceRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{
		"id", "tenant_id", "instance_id", "stack_id", "node_id", "service_key", "name", "status", "source", "url", "migration_status", "metadata_json", "management_state", "created_at", "updated_at",
	})
}

func serviceRuntimeRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{
		"id", "tenant_id", "instance_id", "stack_id", "server_id",
		"target_kind", "provider_id", "managed_target_ref", "provider_receipt_ref",
		"sla_policy_ref", "backup_policy_ref", "placement_evidence_ref", "placement_observed_at",
		"service_key", "service_instance", "name",
		"desired_state", "observed_state", "health_state", "management_state", "observed_at", "stackkit_version", "access_json", "capabilities_json",
		"source", "metadata_json", "created_at", "updated_at",
	})
}

func rilServerRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{
		"id", "tenant_id", "instance_id", "stack_id", "node_id", "name", "status", "health_json", "inventory_json", "last_seen_at", "created_at", "updated_at",
	})
}

func rilCommandRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{
		"id", "tenant_id", "server_id", "actor_subject_id", "command_class", "status", "request_json", "result_json", "error", "created_at", "updated_at", "completed_at",
	})
}

func rilActionCardRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{
		"id", "tenant_id", "server_id", "stack_id", "title", "status", "severity", "action_json", "decision_json", "created_at", "updated_at", "resolved_at",
	})
}

func rilHealEventRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{
		"id", "tenant_id", "server_id", "action_card_id", "status", "cause", "details_json", "created_at", "updated_at",
	})
}
