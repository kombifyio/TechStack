package controlplane

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestPostgresInventoryStackBindsTenantAndOwnerInSQL(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	now := time.Date(2026, 7, 21, 10, 0, 0, 0, time.UTC)
	mock.ExpectBegin()
	expectTenantGUC(mock, "tenant-1")
	mock.ExpectQuery(`(?s)SELECT id, tenant_id, instance_id, owner_subject_id, homelab_id, name, description.*FROM stacks`).
		WithArgs("tenant-1", "owner-1", "stack-1", "").
		WillReturnRows(stackRows().AddRow(
			"stack-1", "tenant-1", "instance-1", "owner-1", nil, "Stack", "", "easy", "active",
			`{}`, `[]`, `{}`, "clean", nil, now, now, nil,
		))
	mock.ExpectCommit()

	got, err := NewPostgresStore(db).GetInventoryStack(context.Background(), mustOwnerInventoryScope(t, "tenant-1", "owner-1"), "stack-1")
	if err != nil {
		t.Fatalf("GetInventoryStack: %v", err)
	}
	if got.TenantID != "tenant-1" || got.OwnerSubjectID != "owner-1" {
		t.Fatalf("owner-scoped stack = %#v", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresInventoryListsApplyOwnerPredicateBeforeReadingRows(t *testing.T) {
	for _, test := range []struct {
		name string
		run  func(*PostgresStore) error
		set  func(sqlmock.Sqlmock)
	}{
		{
			name: "servers",
			run: func(store *PostgresStore) error {
				_, err := store.ListInventoryServers(context.Background(), mustOwnerInventoryScope(t, "tenant-1", "owner-1"), InventoryPageRequest{Limit: 10})
				return err
			},
			set: func(mock sqlmock.Sqlmock) {
				mock.ExpectQuery(`(?s)SELECT created_at, id.*owner_subject_id = \$2`).
					WithArgs("tenant-1", "owner-1").
					WillReturnRows(sqlmock.NewRows([]string{"created_at", "id"}))
			},
		},
		{
			name: "services",
			run: func(store *PostgresStore) error {
				_, err := store.ListInventoryServices(context.Background(), mustOwnerInventoryScope(t, "tenant-1", "owner-1"), "", InventoryPageRequest{Limit: 10})
				return err
			},
			set: func(mock sqlmock.Sqlmock) {
				mock.ExpectQuery(`(?s)SELECT EXISTS.*owner_subject_id = \$2`).
					WithArgs("tenant-1", "owner-1", "", "").
					WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
				mock.ExpectQuery(`(?s)WITH inventory_services AS.*owner_subject_id = \$2.*SELECT created_at, id`).
					WithArgs("tenant-1", "owner-1", "", "").
					WillReturnRows(sqlmock.NewRows([]string{"created_at", "id"}))
			},
		},
		{
			name: "server summary",
			run: func(store *PostgresStore) error {
				count, err := store.CountInventoryServers(context.Background(), mustOwnerInventoryScope(t, "tenant-1", "owner-1"))
				if err == nil && count != 1 {
					t.Fatalf("CountInventoryServers = %d, want 1", count)
				}
				return err
			},
			set: func(mock sqlmock.Sqlmock) {
				mock.ExpectQuery(`(?s)SELECT count\(\*\).*tenant_id = \$1 AND \(\$2 = '' OR owner_subject_id = \$2\)`).
					WithArgs("tenant-1", "owner-1").
					WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			mock.ExpectBegin()
			expectTenantGUC(mock, "tenant-1")
			test.set(mock)
			mock.ExpectCommit()
			if err := test.run(NewPostgresStore(db)); err != nil {
				t.Fatal(err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestPostgresInventoryTenantCollectionScopeUsesTenantPredicateWithoutForgedOwner(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	scope, err := NewTenantInventoryCollectionReadScope("tenant-1", InventoryReadTargetServerCollection)
	if err != nil {
		t.Fatal(err)
	}

	mock.ExpectBegin()
	expectTenantGUC(mock, "tenant-1")
	mock.ExpectQuery(`(?s)SELECT created_at, id.*tenant_id = \$1 AND \(\$2 = '' OR owner_subject_id = \$2\)`).
		WithArgs("tenant-1", "").
		WillReturnRows(sqlmock.NewRows([]string{"created_at", "id"}))
	mock.ExpectCommit()

	page, err := NewPostgresStore(db).ListInventoryServers(t.Context(), scope, InventoryPageRequest{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Servers) != 0 {
		t.Fatalf("unexpected rows: %#v", page.Servers)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresInventoryExactObjectScopeCannotBeReusedForAnotherServer(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	scope, err := NewTenantInventoryObjectReadScope("tenant-1", InventoryReadTargetServer, "server-2")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := NewPostgresStore(db).GetInventoryServer(t.Context(), scope, "server-1"); !errors.Is(err, ErrInvalidInventoryReadScope) {
		t.Fatalf("scope widening error = %v, want invalid scope", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("invalid scope reached the database: %v", err)
	}
}

func TestPostgresInventoryExactObjectScopeBindsTenantAndServerInSQL(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	scope, err := NewTenantInventoryObjectReadScope("tenant-1", InventoryReadTargetServer, "server-2")
	if err != nil {
		t.Fatal(err)
	}

	mock.ExpectBegin()
	expectTenantGUC(mock, "tenant-1")
	mock.ExpectQuery(`(?s)FROM servers.*WHERE tenant_id = \$1 AND \(\$2 = '' OR owner_subject_id = \$2\) AND id = \$3`).
		WithArgs("tenant-1", "", "server-2").
		WillReturnRows(sqlmock.NewRows([]string{"id"}))
	mock.ExpectRollback()

	if _, err := NewPostgresStore(db).GetInventoryServer(t.Context(), scope, "server-2"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("exact SQL read error = %v, want not found", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
