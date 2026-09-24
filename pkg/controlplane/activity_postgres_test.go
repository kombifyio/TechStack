package controlplane

import (
	"context"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

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

func activityRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{
		"id", "tenant_id", "instance_id", "stack_id", "actor_subject_id", "action", "category", "severity", "message", "details_json",
		"runtime_scope_key", "server_scope_key", "service_scope_key", "correlation_id", "created_at",
	})
}
