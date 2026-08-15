package controlplane

import (
	"context"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestPostgresStoreListObservationSweepTenantsReadsDirectoryOnly(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := NewPostgresStore(db)
	cutoff := time.Date(2026, 8, 12, 9, 0, 0, 0, time.UTC)

	mock.ExpectQuery(`(?s)SELECT tenant_id\s+FROM server_registry_sweep_tenants.*earliest_heartbeat_at < \$2`).
		WithArgs("", cutoff, 50).
		WillReturnRows(sqlmock.NewRows([]string{"tenant_id"}).AddRow("tenant-1").AddRow("tenant-2"))

	tenants, err := store.ListObservationSweepTenants(context.Background(), "", 50, cutoff)
	if err != nil {
		t.Fatalf("ListObservationSweepTenants: %v", err)
	}
	if len(tenants) != 2 || tenants[0] != "tenant-1" || tenants[1] != "tenant-2" {
		t.Fatalf("tenants = %#v", tenants)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresStoreListObservationSweepTenantsBoundsLimit(t *testing.T) {
	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := NewPostgresStore(db)
	if _, err := store.ListObservationSweepTenants(context.Background(), "", 0, time.Now()); err == nil {
		t.Fatal("limit 0 was accepted")
	}
	if _, err := store.ListObservationSweepTenants(context.Background(), "", 101, time.Now()); err == nil {
		t.Fatal("limit above 100 was accepted")
	}
}

func TestPostgresStorePruneServerRegistryOutboxStaysTenantScoped(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := NewPostgresStore(db)
	cutoff := time.Date(2026, 7, 13, 9, 0, 0, 0, time.UTC)

	mock.ExpectBegin()
	expectTenantGUC(mock, "tenant-1")
	mock.ExpectExec(`(?s)DELETE FROM server_registry_outbox.*WHERE tenant_id = \$1 AND created_at < \$2.*LIMIT \$3`).
		WithArgs("tenant-1", cutoff, 1000).
		WillReturnResult(sqlmock.NewResult(0, 42))
	mock.ExpectCommit()

	deleted, err := store.PruneServerRegistryOutbox(context.Background(), "tenant-1", cutoff, 1000)
	if err != nil {
		t.Fatalf("PruneServerRegistryOutbox: %v", err)
	}
	if deleted != 42 {
		t.Fatalf("deleted = %d, want 42", deleted)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresStoreCompactOutboxPruneTenantRetiresIdleEntry(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := NewPostgresStore(db)

	mock.ExpectBegin()
	expectTenantGUC(mock, "tenant-1")
	mock.ExpectQuery(`SELECT MIN\(created_at\) FROM server_registry_outbox`).
		WithArgs("tenant-1").
		WillReturnRows(sqlmock.NewRows([]string{"min"}).AddRow(nil))
	mock.ExpectExec(regexp.QuoteMeta("DELETE FROM server_registry_outbox_prune_tenants")).
		WithArgs("tenant-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	if err := store.CompactOutboxPruneTenant(context.Background(), "tenant-1"); err != nil {
		t.Fatalf("CompactOutboxPruneTenant: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresStoreServerRegistryOutboxStats(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := NewPostgresStore(db)
	oldest := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

	mock.ExpectQuery(`SELECT c\.reltuples`).
		WillReturnRows(sqlmock.NewRows([]string{"reltuples"}).AddRow(float64(1234)))
	mock.ExpectQuery(`SELECT MIN\(oldest_created_at\) FROM server_registry_outbox_prune_tenants`).
		WillReturnRows(sqlmock.NewRows([]string{"min"}).AddRow(oldest))

	stats, err := store.ServerRegistryOutboxStats(context.Background())
	if err != nil {
		t.Fatalf("ServerRegistryOutboxStats: %v", err)
	}
	if stats.EstimatedRows != 1234 {
		t.Fatalf("estimated rows = %d, want 1234", stats.EstimatedRows)
	}
	if stats.OldestCreatedAt == nil || !stats.OldestCreatedAt.Equal(oldest) {
		t.Fatalf("oldest = %v, want %v", stats.OldestCreatedAt, oldest)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
