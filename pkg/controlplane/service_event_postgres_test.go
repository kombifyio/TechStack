package controlplane

import (
	"context"
	"errors"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

func serviceEventPostgresCommand(observedAt time.Time, patch ServiceRuntime) ServiceEvent {
	return ServiceEvent{
		TenantID: "tenant-1", ServiceID: "service-1",
		Authority: ServiceEventAuthorityGuard, Source: "stackkits-inventory",
		ObservedAt: observedAt, Runtime: patch, NodeID: "server-1",
	}
}

func serviceEventStoredRow(observedAt time.Time, revision int64, observed, health, status string) *sqlmock.Rows {
	return serviceAggregateRows().AddRow(
		"service-1", "tenant-1", nil, "stack-1", "server-1", "server",
		nil, nil, nil, nil, nil, nil, nil, "vaultwarden", "default",
		"Vaultwarden", "running", observed, health, "managed", observedAt, "cloud-kit@1.0.0",
		`{"mode":"direct","url":"https://vault.example.test"}`, `["restart"]`,
		"stackkits-inventory", `{"container_id":"container-1"}`, observedAt, observedAt,
		revision, status, "", "server-1", "https://vault.example.test",
	)
}

func TestPostgresStoreApplyServiceEventCommitsHeadAndChangedTransitions(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := NewPostgresStore(db)
	observedAt := time.Date(2026, 8, 12, 10, 0, 0, 0, time.UTC)
	databaseNow := observedAt.Add(time.Second)

	mock.ExpectBegin()
	expectTenantGUC(mock, "tenant-1")
	expectServerEventDatabaseTime(mock, databaseNow)
	mock.ExpectQuery(serviceAggregateHeadPattern()).
		WithArgs("tenant-1", "service-1").
		WillReturnRows(serviceEventStoredRow(observedAt.Add(-time.Minute), 4, "running", "healthy", "running"))
	mock.ExpectQuery(serviceAggregateUpdatePattern()).
		WillReturnRows(serviceEventStoredRow(observedAt, 5, "running", "unhealthy", "running"))
	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO service_state_transitions (")).
		WillReturnRows(serviceTransitionRows().AddRow(
			int64(9), "tenant-1", "service-1", "health", "healthy", "unhealthy",
			"probe_failed", "stackkits-inventory", observedAt, `{}`, databaseNow,
		))
	mock.ExpectCommit()

	event := serviceEventPostgresCommand(observedAt, ServiceRuntime{
		ObservedState: "running", HealthState: "unhealthy", ObservedAt: &observedAt,
	})
	event.ReasonCode = "probe_failed"
	result, err := store.ApplyServiceEvent(context.Background(), event)
	if err != nil {
		t.Fatalf("ApplyServiceEvent: %v", err)
	}
	if !result.Applied || result.Revision != 5 || len(result.Transitions) != 1 {
		t.Fatalf("result = %#v", result)
	}
	// A running-but-unhealthy service keeps a running status projection.
	if result.Status != "running" || result.Service.HealthState != "unhealthy" {
		t.Fatalf("status and health were conflated: status=%q health=%q", result.Status, result.Service.HealthState)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresStoreApplyServiceEventMapsLostCASRaceToConflict(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := NewPostgresStore(db)
	observedAt := time.Date(2026, 8, 12, 10, 30, 0, 0, time.UTC)
	databaseNow := observedAt.Add(time.Second)

	mock.ExpectBegin()
	expectTenantGUC(mock, "tenant-1")
	expectServerEventDatabaseTime(mock, databaseNow)
	mock.ExpectQuery(serviceAggregateHeadPattern()).
		WithArgs("tenant-1", "service-1").
		WillReturnRows(serviceEventStoredRow(observedAt.Add(-time.Minute), 4, "running", "healthy", "running"))
	// A concurrent writer moved the revision between the locked read and the
	// head write: the compare-and-swap update matches no row.
	mock.ExpectQuery(serviceAggregateUpdatePattern()).
		WillReturnRows(serviceAggregateRows())
	mock.ExpectRollback()

	_, err = store.ApplyServiceEvent(context.Background(), serviceEventPostgresCommand(observedAt, ServiceRuntime{
		ObservedState: "stopped", ObservedAt: &observedAt,
	}))
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("error = %v, want ErrConflict", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresStoreApplyServiceEventRejectsStaleExpectedRevision(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := NewPostgresStore(db)
	observedAt := time.Date(2026, 8, 12, 11, 0, 0, 0, time.UTC)
	databaseNow := observedAt.Add(time.Second)

	mock.ExpectBegin()
	expectTenantGUC(mock, "tenant-1")
	expectServerEventDatabaseTime(mock, databaseNow)
	mock.ExpectQuery(serviceAggregateHeadPattern()).
		WithArgs("tenant-1", "service-1").
		WillReturnRows(serviceEventStoredRow(observedAt, 6, "running", "healthy", "running"))
	mock.ExpectRollback()

	stale := int64(4)
	event := serviceEventPostgresCommand(observedAt, ServiceRuntime{DesiredState: "stopped"})
	event.Authority = ServiceEventAuthorityControlPlane
	event.Source = "owner-action"
	event.ExpectedRevision = &stale

	if _, err = store.ApplyServiceEvent(context.Background(), event); !errors.Is(err, ErrConflict) {
		t.Fatalf("error = %v, want ErrConflict", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresStoreApplyServiceEventSkipsWriteForIdenticalReplay(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := NewPostgresStore(db)
	observedAt := time.Date(2026, 8, 12, 11, 30, 0, 0, time.UTC)
	databaseNow := observedAt.Add(time.Second)

	mock.ExpectBegin()
	expectTenantGUC(mock, "tenant-1")
	expectServerEventDatabaseTime(mock, databaseNow)
	mock.ExpectQuery(serviceAggregateHeadPattern()).
		WithArgs("tenant-1", "service-1").
		WillReturnRows(serviceEventStoredRow(observedAt, 4, "running", "healthy", "running"))
	mock.ExpectCommit()

	replay := serviceEventPostgresCommand(observedAt, ServiceRuntime{
		StackID: "stack-1", ServerID: "server-1", ServiceKey: "vaultwarden",
		ServiceInstance: "default", Name: "Vaultwarden", Source: "stackkits-inventory",
		StackKitVersion: "cloud-kit@1.0.0", ObservedState: "running", HealthState: "healthy",
		ObservedAt:   &observedAt,
		Access:       map[string]any{"mode": "direct", "url": "https://vault.example.test"},
		Capabilities: []string{"restart"},
		Metadata:     map[string]any{"container_id": "container-1"},
	})
	replay.URL = "https://vault.example.test"
	result, err := store.ApplyServiceEvent(context.Background(), replay)
	if err != nil {
		t.Fatalf("ApplyServiceEvent: %v", err)
	}
	if result.Applied || result.Revision != 4 || len(result.Transitions) != 0 {
		t.Fatalf("identical replay wrote state: %#v", result)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresStoreListServiceTransitionsIsTenantScopedAndNewestFirst(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := NewPostgresStore(db)
	observedAt := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)

	mock.ExpectBegin()
	expectTenantGUC(mock, "tenant-1")
	mock.ExpectQuery(`(?s)FROM service_state_transitions.*tenant_id = \$1 AND service_id = \$2.*ORDER BY id DESC.*LIMIT \$3`).
		WithArgs("tenant-1", "service-1", 100).
		WillReturnRows(serviceTransitionRows().
			AddRow(int64(2), "tenant-1", "service-1", "health", "healthy", "unhealthy",
				nil, "stackkits-inventory", observedAt, `{}`, observedAt).
			AddRow(int64(1), "tenant-1", "service-1", "observed", "starting", "running",
				nil, "stackkits-inventory", observedAt, `{}`, observedAt))
	mock.ExpectCommit()

	rows, err := store.ListServiceTransitions(context.Background(), "tenant-1", "service-1", 0)
	if err != nil {
		t.Fatalf("ListServiceTransitions: %v", err)
	}
	if len(rows) != 2 || rows[0].Dimension != "health" || rows[1].Dimension != "observed" {
		t.Fatalf("transitions = %#v", rows)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
