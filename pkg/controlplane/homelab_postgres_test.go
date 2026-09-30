package controlplane

import (
	"context"
	"errors"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

func homelabRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{
		"id", "tenant_id", "owner_subject_id", "name", "intent_json",
		"created_at", "updated_at", "deleted_at", "named_at",
		"identity_presentation", "identity_cloud_revision", "identity_pending",
	})
}

func TestPostgresStoreGetHomelabByOwnerMapsNotFound(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	store := NewPostgresStore(db)
	mock.ExpectBegin()
	expectTenantGUC(mock, "tenant-1")
	mock.ExpectQuery(regexp.QuoteMeta("FROM homelabs")).
		WithArgs("tenant-1", "auth0|user-1").
		WillReturnRows(homelabRows())
	mock.ExpectRollback()

	_, err = store.GetHomelabByOwner(context.Background(), "tenant-1", "auth0|user-1")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPostgresStoreUpdateHomelabIntentReturnsUpdatedRow(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	store := NewPostgresStore(db)
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)

	mock.ExpectBegin()
	expectTenantGUC(mock, "tenant-1")
	mock.ExpectQuery(regexp.QuoteMeta("UPDATE homelabs")).
		WithArgs("tenant-1", "hl-1", sqlmock.AnyArg()).
		WillReturnRows(homelabRows().AddRow(
			"hl-1", "tenant-1", "auth0|user-1", "homelab", `{"goals":["media"]}`, now, now, nil, nil, nil, 0, false,
		))
	mock.ExpectCommit()

	got, err := store.UpdateHomelabIntent(context.Background(), "tenant-1", "hl-1", map[string]any{"goals": []any{"media"}})
	if err != nil {
		t.Fatalf("UpdateHomelabIntent: %v", err)
	}
	goals, ok := got.Intent["goals"].([]any)
	if !ok || len(goals) != 1 || goals[0] != "media" {
		t.Fatalf("intent JSON was not decoded: %#v", got.Intent)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}
