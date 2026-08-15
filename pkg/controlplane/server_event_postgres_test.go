package controlplane

import (
	"context"
	"errors"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestPostgresStoreApplyServerEventCommitsAtomicChildren(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := NewPostgresStore(db)
	now := time.Date(2026, 7, 21, 16, 0, 0, 0, time.UTC)

	mock.ExpectBegin()
	expectTenantGUC(mock, "tenant-1")
	expectServerEventDatabaseTime(mock, now)
	mock.ExpectQuery(`(?s)SELECT .* FROM servers.*FOR UPDATE`).
		WithArgs("tenant-1", "server-1").
		WillReturnRows(serverEventRuntimeRows().AddRow(
			"server-1", "tenant-1", nil, "stack-1", "owner-1", "guard-1", nil,
			"lease-1", "centron", "unknown", nil, nil, nil, nil, nil, nil, nil, "runtime-1", "enrolling", "running", "connecting", "unknown",
			nil, now, nil, int64(0), int64(1), int64(1), nil, nil, nil, int64(0), nil,
			`[]`, `{}`, nil, "awaiting_guard", "desired_running", "guard_connecting", "health_unknown",
			now, now, now, now, now,
		))
	mock.ExpectQuery(`(?s)UPDATE servers SET.*revision = \$27.*RETURNING`).
		WillReturnRows(serverEventRuntimeRows().AddRow(
			"server-1", "tenant-1", nil, "stack-1", "owner-1", "guard-1", nil,
			"lease-1", "centron", "unknown", nil, nil, nil, nil, nil, nil, nil, "runtime-1", "enrolling", "running", "connected", "healthy",
			nil, now, now, int64(1), int64(2), int64(1), "guard", "guard-1", "epoch-a", int64(1), now,
			`[]`, `{}`, nil, "awaiting_guard", "desired_running", "guard_connected", "guard_healthy",
			now, now, now, now, now,
		))
	for _, dimension := range []string{"connection", "health"} {
		reasonCode := "guard_connected"
		if dimension == "health" {
			reasonCode = "guard_healthy"
		}
		mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO server_state_transitions (")).
			WillReturnRows(serverEventTransitionRows().AddRow(
				int64(1), "tenant-1", "server-1", dimension, "before", "after", reasonCode,
				"guard-inventory", now, `{}`, now,
			))
	}
	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO server_inventory_snapshots (")).
		WillReturnRows(serverEventInventoryRows().AddRow(
			int64(1), "tenant-1", "server-1", int64(1), "guard-inventory", now, `{"host":"observed"}`, now,
		))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO server_guard_source_epochs (")).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO server_registry_outbox (")).
		WillReturnRows(serverEventOutboxRows().AddRow(
			int64(2), "tenant-1", "server-1", int64(2), int64(1), "guard",
			"guard-inventory", "server.aggregate.updated",
			`{"server_id":"server-1","aggregate_revision":2}`, now, now, nil, 0,
		))
	mock.ExpectCommit()

	result, err := store.ApplyServerEvent(context.Background(), ServerEvent{
		TenantID: "tenant-1", ServerID: "server-1", ExpectedRevision: 1, Generation: 1,
		Authority: ServerEventAuthorityGuard, Source: "guard-inventory", SourceID: "guard-1",
		SourceEpoch: "epoch-a", SourceSequence: 1, ObservedAt: now,
		Runtime: ServerRuntime{
			WorkerID: "guard-1", ConnectionState: "connected", HealthState: "healthy", LastHeartbeatAt: &now,
			ConnectionReasonCode: "guard_connected", HealthReasonCode: "guard_healthy",
		},
		Inventory: &ServerInventoryEvent{Source: "guard-inventory", Inventory: map[string]any{"host": "observed"}},
	})
	if err != nil {
		t.Fatalf("ApplyServerEvent: %v", err)
	}
	if !result.Applied || result.Server.Revision != 2 || result.Server.InventoryRevision != 1 || len(result.Transitions) != 2 || result.Inventory == nil || result.Outbox == nil {
		t.Fatalf("result = %#v", result)
	}
	if result.Outbox.AggregateRevision != result.Server.Revision || result.Outbox.Payload["server_id"] != "server-1" {
		t.Fatalf("outbox = %#v", result.Outbox)
	}
	if result.Server.LifecycleReasonCode != "awaiting_guard" || result.Server.DesiredReasonCode != "desired_running" ||
		result.Server.ConnectionReasonCode != "guard_connected" || result.Server.HealthReasonCode != "guard_healthy" {
		t.Fatalf("dimension reasons = %#v", result.Server)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresStoreApplyServerEventTxUsesCallerTransactionAndDatabaseTime(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := NewPostgresStore(db)
	now := time.Date(2026, 7, 21, 16, 30, 0, 0, time.UTC)

	mock.ExpectBegin()
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	mock.ExpectExec(`SELECT set_config`).
		WithArgs(tenantGUC, "tenant-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT clock_timestamp\(\)`).
		WillReturnRows(sqlmock.NewRows([]string{"clock_timestamp"}).AddRow(now))
	mock.ExpectQuery(`(?s)SELECT .* FROM servers.*FOR UPDATE`).
		WithArgs("tenant-1", "server-1").
		WillReturnRows(serverEventRuntimeRows().AddRow(
			"server-1", "tenant-1", nil, "stack-1", "owner-1", "guard-1", nil,
			"lease-1", "centron", "unknown", nil, nil, nil, nil, nil, nil, nil, "runtime-1", "decommissioning", "absent", "offline", "unknown",
			nil, now.Add(-time.Hour), nil, int64(0), int64(2), int64(1), nil, nil, nil, int64(0), nil,
			`[]`, `{}`, nil, "cleanup_pending", "desired_absent", nil, nil,
			now.Add(-time.Hour), now.Add(-time.Hour), now.Add(-time.Hour), now.Add(-time.Hour), now.Add(-time.Hour),
		))
	mock.ExpectQuery(`(?s)UPDATE servers SET.*revision = \$27.*RETURNING`).
		WillReturnRows(serverEventRuntimeRows().AddRow(
			"server-1", "tenant-1", nil, "stack-1", "owner-1", "guard-1", nil,
			"lease-1", "centron", "unknown", nil, nil, nil, nil, nil, nil, nil, "runtime-1", "decommissioned", "absent", "offline", "unknown",
			nil, now.Add(-time.Hour), nil, int64(0), int64(3), int64(1), nil, nil, nil, int64(0), nil,
			`[]`, `{}`, now, "provider_absent", "desired_absent", nil, nil,
			now, now.Add(-time.Hour), now.Add(-time.Hour), now.Add(-time.Hour), now,
		))
	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO server_state_transitions (")).
		WillReturnRows(serverEventTransitionRows().AddRow(
			int64(3), "tenant-1", "server-1", "lifecycle", "decommissioning", "decommissioned",
			"provider_absent", "provider-control-finalizer", now, `{}`, now,
		))
	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO server_registry_outbox (")).
		WillReturnRows(serverEventOutboxRows().AddRow(
			int64(4), "tenant-1", "server-1", int64(3), int64(1), "control-plane",
			"provider-control-finalizer", "server.aggregate.updated",
			`{"server_id":"server-1","aggregate_revision":3}`, now, now, nil, 0,
		))
	mock.ExpectRollback()

	result, err := store.ApplyServerEventTx(t.Context(), tx, ServerEvent{
		TenantID: "tenant-1", ServerID: "server-1", ExpectedRevision: 2, Generation: 1,
		Authority: ServerEventAuthorityControlPlane, Source: "provider-control-finalizer",
		SourceID: "provider-control-finalizer",
		Runtime: ServerRuntime{
			LifecycleState:      "decommissioned",
			DesiredState:        "absent",
			LifecycleReasonCode: "provider_absent",
			DecommissionedAt:    &now,
		},
	})
	if err != nil {
		t.Fatalf("ApplyServerEventTx: %v", err)
	}
	if !result.Applied || result.Server.Revision != 3 || result.Server.DecommissionedAt == nil || result.Outbox == nil {
		t.Fatalf("result = %#v", result)
	}
	if len(result.Transitions) != 1 || !result.Transitions[0].ObservedAt.Equal(now) {
		t.Fatalf("transitions = %#v, want DB timestamp", result.Transitions)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresStoreApplyServerEventRollsBackHeadWhenInventoryFails(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := NewPostgresStore(db)
	now := time.Date(2026, 7, 21, 17, 0, 0, 0, time.UTC)

	mock.ExpectBegin()
	expectTenantGUC(mock, "tenant-1")
	expectServerEventDatabaseTime(mock, now)
	mock.ExpectQuery(`(?s)SELECT .* FROM servers.*FOR UPDATE`).
		WillReturnRows(serverEventRuntimeRows().AddRow(
			"server-1", "tenant-1", nil, "stack-1", "owner-1", "guard-1", nil,
			"lease-1", "centron", "unknown", nil, nil, nil, nil, nil, nil, nil, "runtime-1", "active", "running", "connected", "healthy",
			nil, now, now, int64(0), int64(1), int64(1), "guard", "guard-1", "epoch-a", int64(1), now,
			`[]`, `{}`, nil, nil, nil, nil, nil, now, now, now, now, now,
		))
	mock.ExpectQuery(`(?s)UPDATE servers SET.*RETURNING`).
		WillReturnRows(serverEventRuntimeRows().AddRow(
			"server-1", "tenant-1", nil, "stack-1", "owner-1", "guard-1", nil,
			"lease-1", "centron", "unknown", nil, nil, nil, nil, nil, nil, nil, "runtime-1", "active", "running", "connected", "healthy",
			nil, now, now, int64(1), int64(2), int64(1), "guard", "guard-1", "epoch-a", int64(2), now,
			`[]`, `{}`, nil, nil, nil, nil, nil, now, now, now, now, now,
		))
	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO server_inventory_snapshots (")).
		WillReturnError(errors.New("snapshot write failed"))
	mock.ExpectRollback()

	_, err = store.ApplyServerEvent(context.Background(), ServerEvent{
		TenantID: "tenant-1", ServerID: "server-1", ExpectedRevision: 1, Generation: 1,
		Authority: ServerEventAuthorityGuard, Source: "guard-inventory", SourceID: "guard-1",
		SourceEpoch: "epoch-a", SourceSequence: 2, ObservedAt: now,
		Runtime:   ServerRuntime{WorkerID: "guard-1", ConnectionState: "connected", HealthState: "healthy", LastHeartbeatAt: &now},
		Inventory: &ServerInventoryEvent{Source: "guard-inventory", Inventory: map[string]any{"host": "observed"}},
	})
	if err == nil || err.Error() != "snapshot write failed" {
		t.Fatalf("ApplyServerEvent error = %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresStoreApplyServerEventRollsBackHeadWhenOutboxFails(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := NewPostgresStore(db)
	now := time.Date(2026, 7, 21, 17, 30, 0, 0, time.UTC)

	mock.ExpectBegin()
	expectTenantGUC(mock, "tenant-1")
	expectServerEventDatabaseTime(mock, now)
	mock.ExpectQuery(`(?s)SELECT .* FROM servers.*FOR UPDATE`).
		WithArgs("tenant-1", "server-1").
		WillReturnRows(serverEventRuntimeRows().AddRow(
			"server-1", "tenant-1", nil, "stack-1", "owner-1", "guard-1", nil,
			"lease-1", "centron", "unknown", nil, nil, nil, nil, nil, nil, nil, "runtime-1", "active", "running", "connected", "healthy",
			nil, now, now, int64(0), int64(1), int64(1), "guard", "guard-1", "epoch-a", int64(1), now,
			`[]`, `{}`, nil, nil, nil, nil, nil, now, now, now, now, now,
		))
	mock.ExpectQuery(`(?s)UPDATE servers SET.*RETURNING`).
		WillReturnRows(serverEventRuntimeRows().AddRow(
			"server-1", "tenant-1", nil, "stack-1", "owner-1", "guard-1", nil,
			"lease-1", "centron", "unknown", nil, nil, nil, nil, nil, nil, nil, "runtime-1", "active", "running", "connected", "healthy",
			nil, now, now, int64(0), int64(2), int64(1), "guard", "guard-1", "epoch-a", int64(2), now,
			`[]`, `{}`, nil, nil, nil, nil, nil, now, now, now, now, now,
		))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO server_guard_source_epochs (")).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO server_registry_outbox (")).
		WillReturnError(errors.New("outbox write failed"))
	mock.ExpectRollback()

	_, err = store.ApplyServerEvent(context.Background(), ServerEvent{
		TenantID: "tenant-1", ServerID: "server-1", ExpectedRevision: 1, Generation: 1,
		Authority: ServerEventAuthorityGuard, Source: "guard-heartbeat", SourceID: "guard-1",
		SourceEpoch: "epoch-a", SourceSequence: 2, ObservedAt: now,
		Runtime: ServerRuntime{WorkerID: "guard-1", ConnectionState: "connected", HealthState: "healthy", LastHeartbeatAt: &now},
	})
	if err == nil || err.Error() != "outbox write failed" {
		t.Fatalf("ApplyServerEvent error = %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresStoreApplyServerEventRejectsPreviouslySeenEpoch(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := NewPostgresStore(db)
	now := time.Date(2026, 7, 21, 18, 0, 0, 0, time.UTC)

	mock.ExpectBegin()
	expectTenantGUC(mock, "tenant-1")
	expectServerEventDatabaseTime(mock, now)
	mock.ExpectQuery(`(?s)SELECT .* FROM servers.*FOR UPDATE`).
		WithArgs("tenant-1", "server-1").
		WillReturnRows(serverEventRuntimeRows().AddRow(
			"server-1", "tenant-1", nil, "stack-1", "owner-1", "guard-1", nil,
			"lease-1", "centron", "unknown", nil, nil, nil, nil, nil, nil, nil, "runtime-1", "active", "running", "connected", "healthy",
			nil, now, now, int64(1), int64(3), int64(1), "guard", "guard-1", "epoch-b", int64(1), now,
			`[]`, `{}`, nil, nil, nil, nil, nil, now, now, now, now, now,
		))
	mock.ExpectQuery(`(?s)SELECT EXISTS .*FROM server_guard_source_epochs`).
		WithArgs("tenant-1", "server-1", int64(1), "guard-1", "epoch-a").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectCommit()

	result, err := store.ApplyServerEvent(context.Background(), ServerEvent{
		TenantID: "tenant-1", ServerID: "server-1", ExpectedRevision: 3, Generation: 1,
		Authority: ServerEventAuthorityGuard, Source: "guard-heartbeat", SourceID: "guard-1",
		SourceEpoch: "epoch-a", SourceSequence: 1, ObservedAt: now.Add(time.Minute),
		Runtime: ServerRuntime{WorkerID: "guard-1", ConnectionState: "connected", HealthState: "healthy"},
	})
	if err != nil {
		t.Fatalf("ApplyServerEvent: %v", err)
	}
	if result.Applied || result.Server.Revision != 3 || result.Outbox != nil {
		t.Fatalf("stale epoch result = %#v", result)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func serverEventRuntimeRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{
		"id", "tenant_id", "instance_id", "stack_id", "owner_subject_id", "worker_id", "node_id",
		"lease_id", "provider_ref", "environment_class", "offering", "provider_id", "provider_target_ref",
		"availability_owner", "operations_owner", "runtime_target_evidence_ref", "runtime_target_observed_at",
		"name", "lifecycle_state", "desired_state", "connection_state",
		"health_state", "reason_code", "connection_changed_at", "last_heartbeat_at",
		"inventory_revision", "revision", "generation", "source_authority", "source_id",
		"source_epoch", "source_sequence", "source_observed_at", "channels_json", "metadata_json",
		"decommissioned_at", "lifecycle_reason_code", "desired_reason_code", "connection_reason_code",
		"health_reason_code", "lifecycle_changed_at", "desired_changed_at", "health_changed_at",
		"created_at", "updated_at",
	})
}

func expectServerEventDatabaseTime(mock sqlmock.Sqlmock, now time.Time) {
	mock.ExpectQuery(`SELECT clock_timestamp\(\)`).
		WillReturnRows(sqlmock.NewRows([]string{"clock_timestamp"}).AddRow(now))
}

func serverEventTransitionRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{
		"id", "tenant_id", "server_id", "dimension", "from_state", "to_state",
		"reason_code", "source", "observed_at", "evidence_json", "created_at",
	})
}

func serverEventInventoryRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{
		"id", "tenant_id", "server_id", "revision", "source", "observed_at", "inventory_json", "created_at",
	})
}

func serverEventOutboxRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{
		"id", "tenant_id", "server_id", "aggregate_revision", "generation", "authority",
		"source", "event_type", "payload_json", "occurred_at", "created_at", "published_at", "attempts",
	})
}
