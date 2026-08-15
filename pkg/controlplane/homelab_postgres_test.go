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
	})
}

func TestPostgresStoreCreateHomelabSetsTenantAndDecodesIntent(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	store := NewPostgresStore(db)
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)

	mock.ExpectBegin()
	expectTenantGUC(mock, "tenant-1")
	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO homelabs")).
		WithArgs("hl-1", "tenant-1", "auth0|user-1", "My Homelab", sqlmock.AnyArg()).
		WillReturnRows(homelabRows().AddRow(
			"hl-1", "tenant-1", "auth0|user-1", "My Homelab", `{"goals":["photos"]}`, now, now, nil, nil,
		))
	mock.ExpectCommit()

	got, err := store.CreateHomelab(context.Background(), CreateHomelabRequest{
		ID:             "hl-1",
		TenantID:       "tenant-1",
		OwnerSubjectID: "auth0|user-1",
		Name:           "My Homelab",
		Intent:         map[string]any{"goals": []any{"photos"}},
	})
	if err != nil {
		t.Fatalf("CreateHomelab: %v", err)
	}
	if got.ID != "hl-1" || got.TenantID != "tenant-1" || got.OwnerSubjectID != "auth0|user-1" {
		t.Fatalf("unexpected homelab identity: %#v", got)
	}
	goals, ok := got.Intent["goals"].([]any)
	if !ok || len(goals) != 1 || goals[0] != "photos" {
		t.Fatalf("intent JSON was not decoded: %#v", got.Intent)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPostgresStoreCreateHomelabMapsSingletonConflict(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	store := NewPostgresStore(db)
	mock.ExpectBegin()
	expectTenantGUC(mock, "tenant-1")
	// ON CONFLICT DO NOTHING swallows the unique violation and returns no row.
	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO homelabs")).
		WillReturnRows(homelabRows())
	mock.ExpectRollback()

	_, err = store.CreateHomelab(context.Background(), CreateHomelabRequest{
		ID:             "hl-2",
		TenantID:       "tenant-1",
		OwnerSubjectID: "auth0|user-1",
		Name:           "Second Homelab",
	})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPostgresStoreGetOrCreateHomelabReturnsExistingOnConflict(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	store := NewPostgresStore(db)
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)

	mock.ExpectBegin()
	expectTenantGUC(mock, "tenant-1")
	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO homelabs")).
		WillReturnRows(homelabRows())
	mock.ExpectQuery(regexp.QuoteMeta("FROM homelabs")).
		WithArgs("tenant-1", "auth0|user-1").
		WillReturnRows(homelabRows().AddRow(
			"hl-existing", "tenant-1", "auth0|user-1", "homelab", `{}`, now, now, nil, nil,
		))
	mock.ExpectCommit()

	got, err := store.GetOrCreateHomelabForOwner(context.Background(), CreateHomelabRequest{
		ID:             "hl-new",
		TenantID:       "tenant-1",
		OwnerSubjectID: "auth0|user-1",
		Name:           "homelab",
	})
	if err != nil {
		t.Fatalf("GetOrCreateHomelabForOwner: %v", err)
	}
	if got.ID != "hl-existing" {
		t.Fatalf("expected existing homelab, got %#v", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
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
			"hl-1", "tenant-1", "auth0|user-1", "homelab", `{"goals":["media"]}`, now, now, nil, nil,
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
