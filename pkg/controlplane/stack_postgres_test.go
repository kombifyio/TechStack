package controlplane

import (
	"context"
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
		ID:                 "stack-1",
		TenantID:           "tenant-1",
		InstanceID:         "instance-1",
		OwnerSubjectID:     "auth0|user-1",
		StackKitInstanceID: "media-kit",
		Name:               "Media Stack",
		Description:        "test stack",
		Mode:               "easy",
		Status:             "draft",
		Config:             map[string]any{"profile": "home"},
		Services:           []map[string]any{{"key": "jellyfin"}},
	}

	mock.ExpectBegin()
	expectTenantGUC(mock, "tenant-1")
	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO stacks")).
		WithArgs("stack-1", "tenant-1", "instance-1", "auth0|user-1", "", "media-kit", "Media Stack", "test stack", "easy", "draft", sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnRows(stackRows().AddRow(
			"stack-1", "tenant-1", "instance-1", "auth0|user-1", nil, "media-kit", "Media Stack", "test stack", "easy", "draft",
			`{"profile":"home"}`, `[{"key":"jellyfin"}]`, `{"phase":"created"}`, "clean", nil, now, now, nil,
		))
	mock.ExpectCommit()

	got, err := store.CreateStack(context.Background(), req)
	if err != nil {
		t.Fatalf("CreateStack: %v", err)
	}
	if got.TenantID != "tenant-1" || got.ID != "stack-1" || got.StackKitInstanceID != "media-kit" {
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

func TestPostgresStoreCreateStackMapsStackKitIdentityConflict(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectBegin()
	expectTenantGUC(mock, "tenant-1")
	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO stacks")).WillReturnError(&pgconn.PgError{
		Code: "23505", ConstraintName: "idx_stacks_homelab_stackkit_instance_active",
	})
	mock.ExpectRollback()
	_, err = NewPostgresStore(db).CreateStack(context.Background(), CreateStackRequest{
		ID: "stack-2", TenantID: "tenant-1", HomelabID: "hl-1", StackKitInstanceID: "kit-1", Name: "Two",
	})
	if !errors.Is(err, ErrStackKitInstanceConflict) {
		t.Fatalf("CreateStack error = %v, want ErrStackKitInstanceConflict", err)
	}
}

func TestPostgresStoreStackConfigCASReturnsConflictForStaleRevision(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	expected := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	mock.ExpectBegin()
	expectTenantGUC(mock, "tenant-1")
	mock.ExpectQuery(`(?s)UPDATE stacks.*AND updated_at = \$4`).
		WithArgs("tenant-1", "stack-1", sqlmock.AnyArg(), expected).
		WillReturnRows(stackRows())
	mock.ExpectRollback()

	_, err = NewPostgresStore(db).CompareAndSwapStackConfig(t.Context(), StackConfigCAS{
		TenantID: "tenant-1", StackID: "stack-1", ExpectedUpdatedAt: expected,
		Config: map[string]any{"writer": "stale"},
	})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("CompareAndSwapStackConfig error = %v, want ErrConflict", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func stackRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{
		"id", "tenant_id", "instance_id", "owner_subject_id", "homelab_id", "stackkit_instance_id", "name", "description", "mode", "status",
		"config_json", "services_json", "runtime_summary_json", "drift_status", "drift_checked_at", "created_at", "updated_at", "deleted_at",
	})
}
