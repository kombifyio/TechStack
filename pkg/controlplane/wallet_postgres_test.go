package controlplane

import (
	"context"
	"errors"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

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

func walletRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{
		"id", "tenant_id", "instance_id", "stack_id", "item_type", "provider", "external_ref", "metadata_json", "created_at", "updated_at",
	})
}
